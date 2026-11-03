"""Sortie's plan API, called with the workbench's server-side token."""

from __future__ import annotations

from typing import Any
from urllib.parse import quote

import httpx


class SortieError(Exception):
    """Sortie answered with an error, or could not be reached."""

    def __init__(self, status: int, code: str, message: str, body: dict[str, Any] | None = None):
        super().__init__(f"{code}: {message}")
        self.status = status
        self.code = code
        self.message = message
        self.body = body or {}


class SortieClient:
    def __init__(self, base_url: str, token: str, transport: httpx.AsyncBaseTransport | None = None, timeout: float = 30.0):
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        self._client = httpx.AsyncClient(base_url=base_url, headers=headers, transport=transport, timeout=timeout)

    async def aclose(self) -> None:
        await self._client.aclose()

    async def _request(self, method: str, path: str, *, timeout: float | None = None, **kwargs) -> dict[str, Any]:
        try:
            r = await self._client.request(method, path, timeout=timeout or httpx.USE_CLIENT_DEFAULT, **kwargs)
        except httpx.HTTPError as exc:
            raise SortieError(0, "sortie_unreachable", f"cannot reach Sortie: {exc}") from exc
        try:
            body = r.json()
        except ValueError:
            body = {}
        if r.status_code >= 400:
            err = body.get("error") if isinstance(body, dict) else None
            code = err.get("code", "sortie_error") if isinstance(err, dict) else "sortie_error"
            message = err.get("message", r.text) if isinstance(err, dict) else r.text
            raise SortieError(r.status_code, code, message, body if isinstance(body, dict) else None)
        return body

    async def plan_graph(self, include_unkeyed: bool = False, project: str | None = None) -> dict[str, Any]:
        params = {"include_unkeyed": "true"} if include_unkeyed else {}
        if project:
            params["project"] = project
        return await self._request("GET", "/api/v1/plan/graph", params=params or None)

    async def capabilities(self, project: str | None = None) -> dict[str, Any]:
        return await self._request("GET", "/api/v1/plan/capabilities", params={"project": project} if project else None)

    async def preview(self, graph: dict[str, Any], project: str | None = None) -> dict[str, Any]:
        return await self._request("POST", "/api/v1/plan/sync", params={"project": project} if project else None,
                                   json={"graph": graph, "apply": False})

    async def apply(self, graph: dict[str, Any], preview_id: str, project: str | None = None) -> dict[str, Any]:
        return await self._request(
            "POST", "/api/v1/plan/sync", params={"project": project} if project else None,
            json={"graph": graph, "apply": True, "preview_id": preview_id}, timeout=600,
        )

    async def project(self, key: str) -> dict[str, Any]:
        """Whether the tracker project exists and what keeps plan writes from working in it."""
        return await self._request("GET", f"/api/v1/plan/projects/{key}")

    async def ensure_project(self, key: str, name: str) -> dict[str, Any]:
        """Create the tracker project when it is missing, then report it."""
        return await self._request("PUT", f"/api/v1/plan/projects/{key}", json={"name": name})

    async def pull_back(self, plan_key: str) -> dict[str, Any]:
        return await self._request("POST", "/api/v1/plan/pull-back", json={"plan_key": plan_key})

    async def cancel(self, plan_key: str) -> dict[str, Any]:
        return await self._request("POST", "/api/v1/plan/cancel", json={"plan_key": plan_key})

    async def refresh(self) -> dict[str, Any]:
        """Ask Sortie to poll its tracker now, rather than at its next interval."""
        return await self._request("POST", "/api/v1/refresh")

    # Workers. Sortie runs agents for its configured project only.

    async def state(self) -> dict[str, Any]:
        """The running, retrying, and budget-held issues."""
        return await self._request("GET", "/api/v1/state")

    async def issue(self, identifier: str) -> dict[str, Any]:
        """One issue's runtime: its running entry or its retry entry."""
        return await self._request("GET", f"/api/v1/{quote(identifier, safe='')}")

    async def broker(self, identifier: str) -> dict[str, Any]:
        """The address and viewer token of the broker of the issue's run."""
        return await self._request("GET", f"/api/v1/{quote(identifier, safe='')}/broker")

    async def answer(self, identifier: str, text: str, author: str) -> dict[str, Any]:
        """Answer a parked issue: Sortie posts the text as the author's comment
        and releases the park. Sortie answers 409 for an issue not parked."""
        return await self._request("POST", f"/api/v1/{quote(identifier, safe='')}/answer",
                                   json={"text": text, "author": author})

    async def supervisor(self, op: str, author: str) -> dict[str, Any]:
        """Pause or resume Sortie's supervisor (``op`` is "pause" or "resume"):
        paused, it takes no action until it is resumed. Sortie records who
        paused it as the pause's ``by``."""
        if op not in ("pause", "resume"):
            raise ValueError(f"unknown supervisor operation {op!r}")
        return await self._request("POST", f"/api/v1/supervisor/{op}", json={"by": author} if op == "pause" else {})

    async def grant_sessions(self, identifier: str, sessions: int, author: str,
                             reason: str = "granted from the plan workbench") -> dict[str, Any]:
        """Give the issue more sessions than the workflow's budget: Sortie
        raises that issue's session budget by ``sessions`` and dispatches it
        again, with no change to the workflow. Sortie answers 404
        ``issue_not_found`` for an issue it does not hold."""
        return await self._request("POST", f"/api/v1/{quote(identifier, safe='')}/sessions",
                                   json={"add": sessions, "by": author, "reason": reason})

    async def run_events(self, dispatch_id: str) -> dict[str, Any]:
        """The agent events Sortie kept of one run, oldest first."""
        return await self._request("GET", f"/api/v1/runs/{quote(dispatch_id, safe='')}/events")

    async def runs(self, *, since: str | None = None, status: str | None = None, limit: int = 50) -> dict[str, Any]:
        """Finished runs, newest first: those that started at ``since`` or
        later, of ``status`` only when given."""
        params = {"limit": str(limit), **({"since": since} if since else {}), **({"status": status} if status else {})}
        return await self._request("GET", "/api/v1/runs", params=params)

    def bind(self, project: str, name: str = "sortie") -> "ProjectSortie":
        return ProjectSortie(self, project, name)


class ProjectSortie:
    """The plan API of one tracker project, through a shared client."""

    def __init__(self, client: SortieClient, project: str, name: str = "sortie"):
        self.client = client
        self.project = project
        # The Sortie's name, as the workbench shows its host.
        self.name = name

    async def plan_graph(self, include_unkeyed: bool = False) -> dict[str, Any]:
        return await self.client.plan_graph(include_unkeyed, project=self.project)

    async def capabilities(self) -> dict[str, Any]:
        return await self.client.capabilities(project=self.project)

    async def preview(self, graph: dict[str, Any]) -> dict[str, Any]:
        return await self.client.preview(graph, project=self.project)

    async def apply(self, graph: dict[str, Any], preview_id: str) -> dict[str, Any]:
        return await self.client.apply(graph, preview_id, project=self.project)

    # Pull-back and cancel act on the dispatched project's stories only.
    async def pull_back(self, plan_key: str) -> dict[str, Any]:
        return await self.client.pull_back(plan_key)

    async def cancel(self, plan_key: str) -> dict[str, Any]:
        return await self.client.cancel(plan_key)

    async def refresh(self) -> dict[str, Any]:
        return await self.client.refresh()

    async def state(self) -> dict[str, Any]:
        return await self.client.state()

    async def issue(self, identifier: str) -> dict[str, Any]:
        return await self.client.issue(identifier)

    async def broker(self, identifier: str) -> dict[str, Any]:
        return await self.client.broker(identifier)

    async def answer(self, identifier: str, text: str, author: str) -> dict[str, Any]:
        return await self.client.answer(identifier, text, author)

    # The supervisor is Sortie's, not the project's: it acts on every
    # project Sortie dispatches.
    async def supervisor(self, op: str, author: str) -> dict[str, Any]:
        return await self.client.supervisor(op, author)

    async def grant_sessions(self, identifier: str, sessions: int, author: str) -> dict[str, Any]:
        return await self.client.grant_sessions(identifier, sessions, author)

    async def run_events(self, dispatch_id: str) -> dict[str, Any]:
        return await self.client.run_events(dispatch_id)

    async def runs(self, *, since: str | None = None, status: str | None = None, limit: int = 50) -> dict[str, Any]:
        return await self.client.runs(since=since, status=status, limit=limit)
