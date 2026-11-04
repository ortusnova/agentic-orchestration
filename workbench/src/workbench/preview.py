"""The preview of each ``review: ui`` story's pull request
(agent-platform/run-supervision, "A UI is approved as a prototype, and the
built story merges after its review").

A ui story is approved as its design prototype before it is written, and is
built with no further approval. Once its criteria review has passed and CI
passes on its pull request's head, the kit deploys the branch as a
read-only workbench against live data, at an address of its own, and
comments that address and the commit on the pull request. Sortie reads that
comment as feedback on a ui story, so it starts the review on the preview:
the reviewer clicks every action the story's criteria name there and, when
they pass, records it on the pull request with the line
``sortie:click-proofs passed <head sha>``. Sortie merges the pull request
once that record names its head; no person approves it.

:class:`PreviewSync` is one pass of that, run by the kit's timer:

- a ui story whose pull request is open, whose run is over and whose review
  passed (the story is in the handoff state), and whose head passed CI, is
  deployed at that head by the deploy command; its pull request gets one
  comment with the address for each head deployed, once the preview
  answers there through the control plane's front (:class:`FrontChecker`);
- the address (:func:`preview_address`) is a name under the front's host
  that a job's machine on any worker opens through its HTTP proxy, which
  the egress gate behind Pomerium serves from the preview's port on p1's
  loopback; it is never a loopback address;
- a preview that does not answer there is deployed again, on every pass
  until it does: it is never left to a person;
- the preview follows the branch: a new head is deployed once its run is
  over and CI passes on it, and until then the preview at the last head
  stays up;
- the record keeps whether the pull request holds the click-proofs record
  for its head (``click_proofs``), so the workbench shows the story as
  merging rather than waiting for them;
- a pull request that closes, merged or not, takes its preview down.

What is deployed is kept in the record file, which the live workbench reads
(``WORKBENCH_PREVIEWS``) to show each story's preview and state.
"""

from __future__ import annotations

import asyncio
import ipaddress
import json
import logging
import os
import ssl
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
# ui story's pull request that no bot wrote as feedback, so this one starts
# the review on the preview.
MARKER = "<!-- workbench-preview -->"

# Opens the line of a pull request comment that records a ui story's
# click-proofs as passed on its preview, for the head commit it names
# (Sortie's ClickProofsMarker, internal/orchestrator/plan_control.go).
CLICK_PROOFS = "sortie:click-proofs passed"
# The shortest commit prefix a record may name, as Sortie reads it.
MIN_CLICK_PROOFS_SHA = 7


class PreviewError(Exception):
    pass


def click_proofs_record(body: str) -> str | None:
    """The lower-cased commit a click-proofs record line in ``body`` names,
    or None; read as Sortie reads it, ignoring code ticks and quote marks."""
    for line in (body or "").splitlines():
        fields = line.strip().strip("`>* ").split()
        if len(fields) < 3 or f"{fields[0]} {fields[1]}".lower() != CLICK_PROOFS:
            continue
        sha = fields[2].strip("`.,;:").lower()
        if len(sha) >= MIN_CLICK_PROOFS_SHA and all(c in "0123456789abcdef" for c in sha):
            return sha
    return None


def click_proofs_recorded(comments: list[dict[str, Any]], head: str) -> bool:
    """Whether a comment no bot wrote records the click-proofs for ``head``.
    A record naming an earlier commit does not count: a fix pushed after
    the click-proofs needs them again."""
    head = (head or "").lower()
    for c in comments:
        if ((c.get("user") or {}).get("type") or "").lower() == "bot":
            continue
        sha = click_proofs_record(c.get("body") or "")
        if sha and head.startswith(sha):
            return True
    return False


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


def preview_domain(front_url: str) -> str:
    """The previews' domain under the control plane's front, Pomerium's
    public URL (``https://p1.example.org:8443`` gives
    ``preview.p1.example.org``): the egress gate behind its egress route
    serves every name under it from a preview's port on p1's loopback."""
    u = urlsplit(front_url.strip()) if front_url and front_url.strip() else None
    host = (u.hostname or "") if u else ""
    if not host or _is_ip(host) or host == "localhost" or "." not in host:
        raise PreviewError(f"the control plane's front {front_url!r} has no domain name for the previews")
    return f"preview.{host.lower()}"


def preview_address(front_url: str, identifier: str) -> str:
    """The preview's address, which the kit comments and the review opens:
    a name under the previews' domain, on plain http (``ONS-93`` gives
    ``http://ons-93.preview.p1.example.org/``). A job's machine on any
    worker opens it through its HTTP proxy: the worker's egress forwarder
    sends it as a CONNECT on the egress flow, through Pomerium with the
    worker's certificate and the job's token, to the egress gate, which
    serves it from the preview's port on p1's loopback. That port is never
    opened on p1's network, and the address is never a loopback one."""
    return f"http://{identifier.lower()}.{preview_domain(front_url)}/"


def public_address(live_url: str, identifier: str) -> str | None:
    """The preview's name for people, beside the live workbench's TLS name,
    so that the same TLS setup serves it (``https://workbench.example.net``
    gives ``https://workbench-ons-93.example.net/``); None when the live
    workbench has no name (STAGE0_WORKBENCH_URL is empty)."""
    u = urlsplit(live_url.strip()) if live_url and live_url.strip() else None
    host = u.hostname if u else None
    if not host or _is_ip(host) or host == "localhost":
        return None
    first, _, rest = host.partition(".")
    name = f"{first}-{identifier.lower()}" + (f".{rest}" if rest else "")
    return f"{u.scheme or 'https'}://{name}{f':{u.port}' if u.port else ''}/"


def _is_ip(host: str) -> bool:
    try:
        ipaddress.ip_address(host.strip("[]"))
    except ValueError:
        return False
    return True


def comment_text(identifier: str, address: str, sha: str, pull: str, public: str | None = None) -> str:
    """The kit's comment for one deployed head, once it answers at its
    address through the control plane's front. No line of it starts with
    the click-proofs record, so it is never read as one."""
    people = f"People reach it at {public}.\n\n" if public else ""
    return (f"{MARKER}\nPreview of {identifier} at `{sha}`: {address}\n\n"
            f"This is the branch's workbench at that commit, read-only toward the tracker, against live data. "
            f"A job's machine on any worker opens that address through its HTTP proxy, which reaches it through "
            f"the control plane's front with the job's token; the kit checked that it answers there before this "
            f"comment. {people}"
            f"This comment starts the review on the preview: it clicks every action the story's acceptance "
            f"criteria name there and, when they pass, comments on {pull} a line that starts with "
            f"`{CLICK_PROOFS}` and names this commit. Sortie merges the pull request once that record names its "
            f"head. Each new commit is deployed once its run is over and CI passes, and gets a comment like this one.\n")


class Deployer(Protocol):
    async def up(self, preview: dict[str, Any]) -> None: ...

    async def down(self, identifier: str) -> None: ...


class CommandDeployer:
    """Deploys through the kit's command: ``<command> up <identifier>
    <branch> <sha> <port> <address> [<public address>]`` and ``<command>
    down <identifier>``."""

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
        public = [preview["public_url"]] if preview.get("public_url") else []
        await self._run("up", preview["identifier"], preview["branch"], preview["sha"], str(preview["port"]),
                        preview["url"], *public)

    async def down(self, identifier: str) -> None:
        await self._run("down", identifier)


class Checker(Protocol):
    async def answers(self, address: str) -> str | None:
        """None when the preview answers at ``address`` through the route a
        job's machine takes; otherwise what went wrong."""
        ...


# What the answer check asks for: the preview's home, and an API route
# that its home calls and that needs no sign-in.
CHECK_PATHS = ("/", "/api/auth/mode")


class FrontChecker:
    """Checks a preview through the control plane's front, as a job's
    machine reaches it: TLS to Pomerium at ``front_url`` with p1's own
    worker certificate, a CONNECT for the address's name on port 80 on the
    egress route, and each of :data:`CHECK_PATHS` inside the tunnel, which
    must answer 200. The CONNECT carries Sortie's control token, which the
    egress gate admits for a preview's name only, as the kit's check."""

    def __init__(self, front_url: str, cert: str, key: str, ca: str, token: str, timeout: float = 15.0):
        u = urlsplit(front_url)
        if u.scheme != "https" or not u.hostname:
            raise PreviewError(f"the control plane's front {front_url!r} is not an https URL")
        self.host, self.port = u.hostname, u.port or 443
        self.cert, self.key, self.ca, self.token, self.timeout = cert, key, ca, token, timeout

    def _tls(self) -> ssl.SSLContext:
        ctx = ssl.create_default_context(cafile=self.ca)
        ctx.load_cert_chain(self.cert, self.key)
        # The control plane's CA is its own (cpctl): what matters is that it
        # signed the front's certificate, not the web PKI's strict profile.
        ctx.verify_flags &= ~ssl.VERIFY_X509_STRICT
        # The CONNECT is HTTP/1.1: a front that agreed h2 would drop it.
        ctx.set_alpn_protocols(["http/1.1"])
        return ctx

    async def _get(self, name: str, path: str) -> str | None:
        reader, writer = await asyncio.open_connection(self.host, self.port, ssl=self._tls(),
                                                       server_hostname=self.host)
        try:
            writer.write(f"CONNECT {name}:80 HTTP/1.1\r\nHost: {name}:80\r\n"
                         f"Proxy-Authorization: Bearer {self.token}\r\n\r\n".encode())
            await writer.drain()
            status, head = await _read_head(reader)
            if status != 200:
                return f"CONNECT {name}:80 through {self.host}:{self.port} answered {status} {head}".strip()
            writer.write(f"GET {path} HTTP/1.1\r\nHost: {name}\r\nConnection: close\r\n\r\n".encode())
            await writer.drain()
            status, head = await _read_head(reader)
            if status != 200:
                return f"GET http://{name}{path} answered {status} {head}".strip()
            return None
        finally:
            writer.close()
            try:
                await writer.wait_closed()
            except (OSError, ssl.SSLError):
                pass

    async def answers(self, address: str) -> str | None:
        name = urlsplit(address).hostname or ""
        try:
            for path in CHECK_PATHS:
                err = await asyncio.wait_for(self._get(name, path), timeout=self.timeout)
                if err:
                    return err
        except TimeoutError:
            return f"{address} did not answer through {self.host}:{self.port} in {self.timeout:.0f}s"
        except (OSError, ssl.SSLError, ValueError) as exc:
            return f"{address} through {self.host}:{self.port}: {exc}"
        return None


async def _read_head(reader: asyncio.StreamReader) -> tuple[int, str]:
    """The status of an HTTP/1.1 answer and its reason, read up to its
    blank line."""
    line = (await reader.readline()).decode(errors="replace").strip()
    parts = line.split(" ", 2)
    if len(parts) < 2 or not parts[0].startswith("HTTP/") or not parts[1].isdigit():
        raise ValueError(f"not an HTTP answer: {line!r}")
    while (await reader.readline()) not in (b"\r\n", b"\n", b""):
        pass
    return int(parts[1]), parts[2] if len(parts) > 2 else ""


@dataclass(frozen=True)
class Settings:
    # The repository whose pull requests are previewed: the workbench's own.
    repo: str
    record: Path
    # The control plane's front, Pomerium's public URL: each preview's
    # address is a name under it (preview_address).
    front_url: str = ""
    # The live workbench's TLS name, which each preview's name for people
    # sits beside (public_address); empty: none.
    live_url: str = ""
    first_port: int = 8800
    handoff_state: str = "In Review"
    branch_prefix: str = "sortie/"


class PreviewSync:
    def __init__(self, settings: Settings, sortie: ProjectSortie, github: GitHubClient, deployer: Deployer,
                 checker: Checker, clock=time.time):
        self.settings = settings
        self.sortie = sortie
        self.github = github
        self.deployer = deployer
        self.checker = checker
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
        comments = None
        if entry:
            comments = await self.github.comments(self.owner, self.repo, int(entry["number"]))
            proofs = sha if sha and click_proofs_recorded(comments, sha) else None
            if entry.get("click_proofs") != proofs:
                entry["click_proofs"] = proofs
                write_previews(self.settings.record, record)
                if proofs:
                    log.info("%s's click-proofs are recorded for %s: Sortie merges it", ident, sha[:12])
        if entry and entry.get("sha") == sha:
            # The deployed head: it must answer at its address, or the kit
            # deploys it again; no person is asked to. A preview deployed at
            # an older address, such as a loopback one, moves to its own.
            moved = self._readdress(ident, entry)
            if moved:
                write_previews(self.settings.record, record)
            if (moved or not await self._answers(ident, record)) and not await self._redeploy(ident, record):
                return
            if entry.get("commented") != sha:
                await self._comment(ident, record, comments)
            return
        if not sha or not self.reviewed(node):
            return
        if await self.github.ci_state(self.owner, self.repo, sha) != "success":
            return
        port = entry["port"] if entry and entry.get("port") else self.free_port(record)
        try:
            address = preview_address(self.settings.front_url, ident)
        except PreviewError as exc:
            log.warning("no address for the preview of %s: %s", ident, exc)
            return
        preview = {
            "identifier": ident, "plan_key": node.get("plan_key"), "branch": branch, "sha": sha, "port": port,
            "url": address, "public_url": public_address(self.settings.live_url, ident),
            "pull": f"{self.settings.repo}#{pull.get('number')}", "number": pull.get("number"),
            "pull_url": pull.get("html_url") or "", "deployed_at": self.clock(),
            "commented": None, "click_proofs": entry.get("click_proofs") if entry else None,
            "answers": None,
        }
        try:
            await self.deployer.up(preview)
        except PreviewError as exc:
            log.warning("could not deploy the preview of %s at %s: %s", ident, sha[:12], exc)
            return
        log.info("deployed the preview of %s at %s: %s", ident, sha[:12], preview["url"])
        record[ident] = preview
        write_previews(self.settings.record, record)
        if await self._answers(ident, record):
            await self._comment(ident, record, comments)

    def _readdress(self, ident: str, entry: dict[str, Any]) -> bool:
        """Give a deployed preview the address it has now, when it was
        deployed at another; True when it moved."""
        try:
            address = preview_address(self.settings.front_url, ident)
        except PreviewError as exc:
            log.warning("no address for the preview of %s: %s", ident, exc)
            return False
        if entry.get("url") == address:
            return False
        log.info("the preview of %s moves from %s to %s", ident, entry.get("url"), address)
        entry.update(url=address, public_url=public_address(self.settings.live_url, ident), answers=None,
                     commented=None)
        return True

    async def _answers(self, ident: str, record: dict[str, dict[str, Any]]) -> bool:
        """Check that the preview answers at its address through the route;
        keep the verdict in the record."""
        entry = record[ident]
        err = await self.checker.answers(entry["url"])
        verdict = err is None
        if entry.get("answers") != verdict or entry.get("answer_error") != err:
            entry["answers"], entry["answer_error"] = verdict, err
            write_previews(self.settings.record, record)
        if err:
            log.warning("the preview of %s does not answer at %s: %s", ident, entry["url"], err)
        return verdict

    async def _redeploy(self, ident: str, record: dict[str, dict[str, Any]]) -> bool:
        """Deploy a preview that does not answer again, at the same head and
        port; True once it answers."""
        entry = record[ident]
        try:
            await self.deployer.up(entry)
        except PreviewError as exc:
            log.warning("could not deploy the preview of %s at %s again: %s", ident, entry["sha"][:12], exc)
            return False
        entry["deployed_at"] = self.clock()
        entry["redeploys"] = int(entry.get("redeploys") or 0) + 1
        write_previews(self.settings.record, record)
        log.info("deployed the preview of %s at %s again: it did not answer", ident, entry["sha"][:12])
        return await self._answers(ident, record)

    async def _comment(self, ident: str, record: dict[str, dict[str, Any]],
                       comments: list[dict[str, Any]] | None = None) -> None:
        """Comment the preview's address and commit on the pull request,
        once per head deployed, after it answers there: each comment starts
        a review on it."""
        entry = record[ident]
        number, sha = int(entry["number"]), entry["sha"]
        if comments is None:
            comments = await self.github.comments(self.owner, self.repo, number)
        url = entry["url"]
        if not any(MARKER in (c.get("body") or "") and sha in (c.get("body") or "") and url in (c.get("body") or "")
                   for c in comments):
            await self.github.comment(self.owner, self.repo, number,
                                      comment_text(ident, entry["url"], sha, entry["pull"], entry.get("public_url")))
            log.info("commented the preview of %s at %s on %s", ident, sha[:12], entry["pull"])
        entry["commented"] = sha
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


async def sync_once(settings: Settings, sortie: ProjectSortie, github: GitHubClient, deployer: Deployer,
                    checker: Checker) -> int:
    """One pass for the command line: 0, or 1 when Sortie cannot be read."""
    try:
        await PreviewSync(settings, sortie, github, deployer, checker).run_once()
    except SortieError as exc:
        log.warning("Sortie's plan graph is unavailable: %s", exc.message)
        return 1
    return 0
