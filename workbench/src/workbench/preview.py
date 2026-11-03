"""The preview of each ``review: ui`` story's pull request
(agent-platform/run-supervision, "A UI story is built before it is approved,
and its merge is the approval").

A ui story is built before anyone approves it. Once its criteria review has
passed and CI passes on its pull request's head, the kit deploys the branch
as a read-only workbench against live data, at an address of its own, and
comments that address on the pull request. Sortie never merges the pull
request: the operator looks at the preview, comments to ask for a change,
which starts a fix run on the branch, and merges it to approve the story.

:class:`PreviewSync` is one pass of that, run by the kit's timer:

- a ui story whose pull request is open, whose run is over and whose review
  passed (the story is in the handoff state), and whose head passed CI, is
  deployed at that head by the deploy command; its pull request gets one
  comment with the address;
- the preview follows the branch: a new head is deployed once its run is
  over and CI passes on it, and until then the preview at the last head
  stays up;
- a pull request that closes, merged or not, takes its preview down.

What is deployed is kept in the record file, which the live workbench reads
(``WORKBENCH_PREVIEWS``) to show each story's preview.
"""

from __future__ import annotations

import asyncio
import ipaddress
import json
import logging
import os
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Protocol
from urllib.parse import urlsplit

from .delivery import GitHubClient, GitHubError
from .sortie import ProjectSortie, SortieError

log = logging.getLogger(__name__)

# Marks the kit's comment on a pull request. Sortie reads every comment on a
# ui story's pull request that no bot wrote as the operator's; the kit's
# workflow tells its review fix runs to pass over this one.
MARKER = "<!-- workbench-preview -->"


class PreviewError(Exception):
    pass


def read_previews(path: Path | None) -> dict[str, dict[str, Any]]:
    """The deployed previews, by tracker identifier; none when the record
    is missing or unreadable."""
    if path is None:
        return {}
    try:
        doc = json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return {}
    previews = doc.get("previews") if isinstance(doc, dict) else None
    if not isinstance(previews, dict):
        return {}
    return {k: v for k, v in previews.items() if isinstance(v, dict) and v.get("url")}


def write_previews(path: Path, previews: dict[str, dict[str, Any]]) -> None:
    """Replace the record at once, so a reader never sees half of it."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump({"previews": previews}, f, indent=2, sort_keys=True)
            f.write("\n")
        os.replace(tmp, path)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def preview_address(live_url: str, identifier: str, port: int) -> str:
    """The preview's address: a name beside the live workbench's, so that the
    same TLS setup serves it (``https://workbench.example.net`` gives
    ``https://workbench-ons-93.example.net/``), or its loopback port when
    the live workbench has no name."""
    u = urlsplit(live_url.strip()) if live_url.strip() else None
    host = u.hostname if u else None
    if not host or _is_ip(host) or host == "localhost":
        return f"http://127.0.0.1:{port}/"
    first, _, rest = host.partition(".")
    name = f"{first}-{identifier.lower()}" + (f".{rest}" if rest else "")
    return f"{u.scheme or 'https'}://{name}{f':{u.port}' if u.port else ''}/"


def _is_ip(host: str) -> bool:
    try:
        ipaddress.ip_address(host.strip("[]"))
    except ValueError:
        return False
    return True


def comment_text(identifier: str, address: str) -> str:
    return (f"{MARKER}\nPreview of {identifier}: {address}\n\n"
            "This branch's workbench, read-only toward the tracker, against live data. It follows the "
            "branch: each new commit is deployed once its run is over and CI passes. Comment here to ask "
            "for a change, and merge this pull request to approve the story.\n")


class Deployer(Protocol):
    async def up(self, preview: dict[str, Any]) -> None: ...

    async def down(self, identifier: str) -> None: ...


class CommandDeployer:
    """Deploys through the kit's command: ``<command> up <identifier>
    <branch> <sha> <port> <address>`` and ``<command> down <identifier>``."""

    def __init__(self, command: list[str], timeout: float = 1800.0):
        self.command = command
        self.timeout = timeout

    async def _run(self, *args: str) -> None:
        proc = await asyncio.create_subprocess_exec(*self.command, *args, stdout=asyncio.subprocess.PIPE,
                                                    stderr=asyncio.subprocess.STDOUT)
        try:
            out, _ = await asyncio.wait_for(proc.communicate(), timeout=self.timeout)
        except TimeoutError as exc:
            proc.kill()
            await proc.wait()
            raise PreviewError(f"{args[0]} {args[1]} did not finish in {self.timeout:.0f}s") from exc
        if proc.returncode != 0:
            tail = out.decode(errors="replace").strip().splitlines()[-5:]
            raise PreviewError(f"{args[0]} {args[1]} failed ({proc.returncode}): " + " | ".join(tail))

    async def up(self, preview: dict[str, Any]) -> None:
        await self._run("up", preview["identifier"], preview["branch"], preview["sha"], str(preview["port"]),
                        preview["url"])

    async def down(self, identifier: str) -> None:
        await self._run("down", identifier)


@dataclass(frozen=True)
class Settings:
    # The repository whose pull requests are previewed: the workbench's own.
    repo: str
    record: Path
    live_url: str = ""
    first_port: int = 8800
    handoff_state: str = "In Review"
    branch_prefix: str = "sortie/"


class PreviewSync:
    def __init__(self, settings: Settings, sortie: ProjectSortie, github: GitHubClient, deployer: Deployer,
                 clock=time.time):
        self.settings = settings
        self.sortie = sortie
        self.github = github
        self.deployer = deployer
        self.clock = clock
        self.owner, _, self.repo = settings.repo.partition("/")

    def reviewed(self, node: dict[str, Any]) -> bool:
        """Whether the story's run is over and its review passed: it waits
        in the handoff state."""
        return (not node.get("runtime") and not node.get("terminal")
                and (node.get("state") or "").lower() == self.settings.handoff_state.lower())

    def free_port(self, record: dict[str, dict[str, Any]]) -> int:
        taken = {e.get("port") for e in record.values()}
        port = self.settings.first_port
        while port in taken:
            port += 1
        return port

    async def run_once(self) -> dict[str, dict[str, Any]]:
        """One pass; returns the record it leaves. Raises SortieError when
        Sortie's plan graph cannot be read: then nothing changes."""
        record = read_previews(self.settings.record)
        graph = await self.sortie.plan_graph()
        ui = {n["identifier"]: n for n in graph.get("nodes") or []
              if n.get("kind") == "story" and n.get("review") == "ui" and n.get("identifier")}
        for ident in sorted(ui):
            try:
                await self._story(ident, ui[ident], record)
            except GitHubError as exc:
                log.warning("preview of %s: %s", ident, exc)
        # A story that is no longer a ui story, or no longer in the tracker,
        # keeps its preview until its pull request closes.
        for ident in sorted(set(record) - set(ui)):
            try:
                pull = await self.github.pull_by_number(self.owner, self.repo, int(record[ident]["number"]))
            except (GitHubError, KeyError, TypeError, ValueError) as exc:
                log.warning("preview of %s: %s", ident, exc)
                continue
            if pull.get("state") != "open":
                await self._down(ident, record)
        return record

    async def _story(self, ident: str, node: dict[str, Any], record: dict[str, dict[str, Any]]) -> None:
        pull = await self.github.pull_from(self.owner, self.repo, self.settings.branch_prefix + ident)
        entry = record.get(ident)
        if pull is None:
            return
        if pull.get("state") != "open":
            if entry:
                await self._down(ident, record)
            return
        head = pull.get("head") or {}
        sha, branch = head.get("sha") or "", head.get("ref") or self.settings.branch_prefix + ident
        if entry and entry.get("sha") == sha:
            if not entry.get("commented"):
                await self._comment(ident, record)
            return
        if not sha or not self.reviewed(node):
            return
        if await self.github.ci_state(self.owner, self.repo, sha) != "success":
            return
        port = entry["port"] if entry and entry.get("port") else self.free_port(record)
        preview = {
            "identifier": ident, "plan_key": node.get("plan_key"), "branch": branch, "sha": sha, "port": port,
            "url": preview_address(self.settings.live_url, ident, port),
            "pull": f"{self.settings.repo}#{pull.get('number')}", "number": pull.get("number"),
            "pull_url": pull.get("html_url") or "", "deployed_at": self.clock(),
            "commented": bool(entry and entry.get("commented")),
        }
        try:
            await self.deployer.up(preview)
        except PreviewError as exc:
            log.warning("could not deploy the preview of %s at %s: %s", ident, sha[:12], exc)
            return
        log.info("deployed the preview of %s at %s: %s", ident, sha[:12], preview["url"])
        record[ident] = preview
        write_previews(self.settings.record, record)
        if not preview["commented"]:
            await self._comment(ident, record)

    async def _comment(self, ident: str, record: dict[str, dict[str, Any]]) -> None:
        """Comment the preview's address on the pull request, once."""
        entry = record[ident]
        number = int(entry["number"])
        comments = await self.github.comments(self.owner, self.repo, number)
        if not any(MARKER in (c.get("body") or "") for c in comments):
            await self.github.comment(self.owner, self.repo, number, comment_text(ident, entry["url"]))
            log.info("commented the preview of %s on %s", ident, entry["pull"])
        entry["commented"] = True
        write_previews(self.settings.record, record)

    async def _down(self, ident: str, record: dict[str, dict[str, Any]]) -> None:
        try:
            await self.deployer.down(ident)
        except PreviewError as exc:
            log.warning("could not remove the preview of %s: %s", ident, exc)
            return
        log.info("removed the preview of %s: its pull request closed", ident)
        record.pop(ident, None)
        write_previews(self.settings.record, record)


async def sync_once(settings: Settings, sortie: ProjectSortie, github: GitHubClient, deployer: Deployer) -> int:
    """One pass for the command line: 0, or 1 when Sortie cannot be read."""
    try:
        await PreviewSync(settings, sortie, github, deployer).run_once()
    except SortieError as exc:
        log.warning("Sortie's plan graph is unavailable: %s", exc.message)
        return 1
    return 0
