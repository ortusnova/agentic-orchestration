"""The pull requests that delivered stories, as GitHub reports them
(agent-platform/delivered-work).

A story whose deliverable names ``delivered_by`` was built outside an agent
run. The workbench looks up each pull request it names on GitHub, again
every few minutes until it is merged, and never again after: a merge is
final, and the registry keeps it across restarts.
"""

from __future__ import annotations

import asyncio
import logging
import re
import time
from typing import Any, Callable
from urllib.parse import quote

import httpx

log = logging.getLogger(__name__)

PULL_REF = re.compile(r"^(?P<owner>[^/#\s]+)/(?P<repo>[^/#\s]+)#(?P<number>[1-9][0-9]*)$")


def pull_url(ref: str) -> str:
    """The pull request's page on github.com, before GitHub has answered."""
    m = PULL_REF.match(ref)
    return f"https://github.com/{m['owner']}/{m['repo']}/pull/{m['number']}" if m else ""


def pending(ref: str) -> dict[str, Any]:
    return {"ref": ref, "state": "pending", "url": pull_url(ref), "merge_commit": None, "merge_commit_url": None,
            "merged_at": None, "reason": "not looked up yet"}


class GitHubClient:
    """Reads pull requests from GitHub's REST API with the workbench's token."""

    def __init__(self, api_url: str = "https://api.github.com", token: str = "",
                 transport: httpx.AsyncBaseTransport | None = None, timeout: float = 20.0):
        headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28",
                   "User-Agent": "plan-workbench"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        self._client = httpx.AsyncClient(base_url=api_url.rstrip("/"), headers=headers, transport=transport, timeout=timeout)

    async def aclose(self) -> None:
        await self._client.aclose()

    async def pull(self, ref: str) -> dict[str, Any]:
        """The pull request's state: ``merged``, ``open``, ``closed`` (closed
        without merging), or ``unknown`` when it cannot be looked up, with
        the reason."""
        out = pending(ref)
        m = PULL_REF.match(ref)
        if not m:
            return {**out, "state": "unknown", "reason": "not a pull request: write owner/repo#number"}
        path = f"/repos/{quote(m['owner'], safe='')}/{quote(m['repo'], safe='')}/pulls/{m['number']}"
        try:
            r = await self._client.get(path)
        except httpx.HTTPError as exc:
            return {**out, "state": "unknown", "reason": f"GitHub is unreachable: {str(exc) or type(exc).__name__}"}
        try:
            body = r.json()
        except ValueError:
            body = {}
        if not isinstance(body, dict):
            body = {}
        if r.status_code == 404:
            return {**out, "state": "unknown", "reason": "GitHub has no such pull request, or the workbench's token cannot see it"}
        if r.status_code >= 400:
            message = body.get("message") or r.reason_phrase
            return {**out, "state": "unknown", "reason": f"GitHub answered {r.status_code}: {message}"}
        url = body.get("html_url") or out["url"]
        if body.get("merged") or body.get("merged_at"):
            sha = body.get("merge_commit_sha") or ""
            return {**out, "state": "merged", "url": url, "merge_commit": sha or None,
                    "merge_commit_url": commit_url(url, sha), "merged_at": body.get("merged_at"), "reason": None}
        if body.get("state") == "open":
            return {**out, "state": "open", "url": url, "reason": "it is open"}
        if body.get("state") == "closed":
            return {**out, "state": "closed", "url": url, "reason": "it was closed without merging"}
        return {**out, "state": "unknown", "url": url, "reason": f"GitHub reports the state {body.get('state')!r}"}

    async def ensure_pull(self, owner: str, repo: str, head: str, base: str, title: str, body: str) -> dict[str, Any]:
        """The pull request from branch ``head`` into ``base``: the one GitHub
        has, open or closed, or else a new one. Returns its ``url``,
        ``number``, and whether it was ``created``; raises GitHubError."""
        path = f"/repos/{quote(owner, safe='')}/{quote(repo, safe='')}/pulls"
        found = await self._call("GET", path, params={"head": f"{owner}:{head}", "state": "all", "per_page": "1"})
        if isinstance(found, list) and found:
            return {"url": found[0].get("html_url"), "number": found[0].get("number"), "created": False}
        made = await self._call("POST", path, json={"title": title, "head": head, "base": base, "body": body})
        if not isinstance(made, dict):
            raise GitHubError("GitHub answered the new pull request with no object")
        return {"url": made.get("html_url"), "number": made.get("number"), "created": True}

    def _repo(self, owner: str, repo: str) -> str:
        return f"/repos/{quote(owner, safe='')}/{quote(repo, safe='')}"

    async def pull_from(self, owner: str, repo: str, branch: str) -> dict[str, Any] | None:
        """The newest pull request from ``branch``, open or closed, as GitHub
        returns it, or None; raises GitHubError."""
        found = await self._call("GET", self._repo(owner, repo) + "/pulls",
                                 params={"head": f"{owner}:{branch}", "state": "all", "per_page": "1"})
        return found[0] if isinstance(found, list) and found and isinstance(found[0], dict) else None

    async def pull_by_number(self, owner: str, repo: str, number: int) -> dict[str, Any]:
        """The pull request as GitHub returns it; raises GitHubError."""
        body = await self._call("GET", self._repo(owner, repo) + f"/pulls/{int(number)}")
        if not isinstance(body, dict):
            raise GitHubError("GitHub answered the pull request with no object")
        return body

    async def ci_state(self, owner: str, repo: str, sha: str) -> str:
        """``success`` when every check run and commit status of ``sha`` has
        passed and there is at least one, ``failure`` when one failed, else
        ``pending``; raises GitHubError."""
        base = self._repo(owner, repo) + f"/commits/{quote(sha, safe='')}"
        runs = await self._call("GET", base + "/check-runs", params={"per_page": "100"})
        status = await self._call("GET", base + "/status")
        checks = (runs.get("check_runs") if isinstance(runs, dict) else None) or []
        statuses = (status.get("statuses") if isinstance(status, dict) else None) or []
        if any(c.get("status") == "completed" and c.get("conclusion") not in ("success", "neutral", "skipped")
               for c in checks) or any(s.get("state") in ("failure", "error") for s in statuses):
            return "failure"
        if not checks and not statuses:
            return "pending"
        if all(c.get("status") == "completed" for c in checks) and all(s.get("state") == "success" for s in statuses):
            return "success"
        return "pending"

    async def comments(self, owner: str, repo: str, number: int) -> list[dict[str, Any]]:
        """The conversation comments of a pull request; raises GitHubError."""
        body = await self._call("GET", self._repo(owner, repo) + f"/issues/{int(number)}/comments",
                                params={"per_page": "100"})
        return [c for c in body if isinstance(c, dict)] if isinstance(body, list) else []

    async def comment(self, owner: str, repo: str, number: int, text: str) -> dict[str, Any]:
        """Comment on a pull request's conversation; raises GitHubError."""
        body = await self._call("POST", self._repo(owner, repo) + f"/issues/{int(number)}/comments", json={"body": text})
        return body if isinstance(body, dict) else {}

    async def _call(self, method: str, path: str, **kw: Any) -> Any:
        try:
            r = await self._client.request(method, path, **kw)
        except httpx.HTTPError as exc:
            raise GitHubError(f"GitHub is unreachable: {str(exc) or type(exc).__name__}") from exc
        try:
            body = r.json()
        except ValueError:
            body = None
        if r.status_code >= 400:
            message = (body.get("message") if isinstance(body, dict) else None) or r.reason_phrase
            raise GitHubError(f"GitHub answered {r.status_code}: {message}")
        if body is None:
            raise GitHubError(f"GitHub answered {r.status_code} without JSON")
        return body


class GitHubError(Exception):
    pass


def commit_url(pull_html_url: str, sha: str) -> str | None:
    """The merge commit's page, beside the pull request's: .../owner/repo/commit/<sha>."""
    if not sha or "/pull/" not in pull_html_url:
        return None
    return pull_html_url.rsplit("/pull/", 1)[0] + f"/commit/{sha}"


class DeliveryWatch:
    """The pull requests one project's stories name, each looked up on
    GitHub when first named, then every ``recheck`` seconds until it is
    merged. A merge is kept in the registry and never looked up again."""

    def __init__(self, github: GitHubClient, refs: Callable[[], set[str]], *, registry=None,
                 on_change: Callable[[], None] = lambda: None, recheck: float = 300.0,
                 clock: Callable[[], float] = time.monotonic):
        self.github = github
        self.refs = refs
        self.registry = registry
        self.on_change = on_change
        self.recheck = recheck
        self.clock = clock
        self.results: dict[str, dict[str, Any]] = {}
        self._checked: dict[str, float] = {}
        # Each lookup made, by ref: what tests and the log count.
        self.lookups: dict[str, int] = {}

    def pulls(self) -> dict[str, dict[str, Any]]:
        """Every ref a story names, with what is known of it."""
        return {ref: self.results.get(ref) or pending(ref) for ref in self.refs()}

    def _known_merged(self, ref: str) -> dict[str, Any] | None:
        if self.registry is None:
            return None
        row = self.registry.merged_pulls().get(ref)
        if row is None:
            return None
        return {**pending(ref), "state": "merged", "url": row["url"], "merge_commit": row["merge_commit"] or None,
                "merge_commit_url": commit_url(row["url"], row["merge_commit"]), "merged_at": row["merged_at"] or None,
                "reason": None}

    async def check(self) -> bool:
        """Look up every ref that is due. Returns whether anything changed,
        after telling ``on_change``."""
        changed = False
        now = self.clock()
        for ref in sorted(self.refs()):
            if (self.results.get(ref) or {}).get("state") == "merged":
                continue
            known = await asyncio.to_thread(self._known_merged, ref)
            if known is None:
                last = self._checked.get(ref)
                if last is not None and now - last < self.recheck:
                    continue
                self._checked[ref] = now
                self.lookups[ref] = self.lookups.get(ref, 0) + 1
                result = await self.github.pull(ref)
                if result["state"] == "merged" and self.registry is not None:
                    await asyncio.to_thread(self.registry.record_merged, ref, result["url"], result["merge_commit"] or "",
                                            result["merged_at"] or "")
                if result["state"] == "unknown":
                    log.warning("could not look up %s: %s", ref, result["reason"])
            else:
                result = known
            if result != self.results.get(ref):
                self.results[ref] = result
                changed = True
        if changed:
            self.on_change()
        return changed

    async def run(self, stop: asyncio.Event, tick: float) -> None:
        """Check every ``tick`` seconds, so a newly named pull request is
        looked up within one tick, and a due one within ``recheck + tick``."""
        while not stop.is_set():
            try:
                await self.check()
            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("delivery check failed")
            try:
                await asyncio.wait_for(stop.wait(), timeout=tick)
            except TimeoutError:
                pass
