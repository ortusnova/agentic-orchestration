"""The notifications endpoint each Sortie's webhook backend posts to, and
what a person does with what it received (workbench.notifications)."""

from __future__ import annotations

import asyncio
import logging
from typing import Any, Callable

from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel

from .auth import Identity
from .notifications import MAX_BODY, HostTokens, PayloadError, parse, project_notifications
from .projects import ProjectManager
from .registry import Registry

log = logging.getLogger(__name__)


class SeenBody(BaseModel):
    ids: list[int]


def _refused() -> HTTPException:
    # A wrong host and a wrong token read the same: neither says which hosts exist.
    return HTTPException(status_code=401, detail={"code": "unauthorized", "message": "a host's notifications need its token"})


def _identifiers(rt) -> list[str]:
    """The tracker identifiers of the project's stories."""
    return [n["tracker"]["identifier"] for n in rt.hub.doc.get("nodes") or [] if n.get("kind") == "story" and n.get("tracker")]


def notification_router(projects: ProjectManager, registry: Registry, tokens: HostTokens, require_operator,
                        clock: Callable[[], float]) -> APIRouter:
    router = APIRouter()

    @router.post("/api/notifications/{host}")
    async def no_token(host: str) -> dict:
        raise _refused()

    @router.post("/api/notifications/{host}/{token}")
    async def receive(host: str, token: str, request: Request) -> JSONResponse:
        """One notification of the host's Sortie, as its webhook backend posts it."""
        if not tokens.check(host, token):
            log.warning("refused a notification for host %r: wrong or missing token", host)
            raise _refused()
        raw = await request.body()
        if len(raw) > MAX_BODY:
            raise HTTPException(status_code=413, detail={"code": "too_large", "message": f"a notification is at most {MAX_BODY} bytes"})
        try:
            body = parse(raw)
        except PayloadError as exc:
            raise HTTPException(status_code=400, detail={"code": "invalid_notification", "message": str(exc)}) from None
        ident = (body.get("identifier") or "").strip()
        event = body.get("event_type") or ""
        row, new = await asyncio.to_thread(registry.record_notification, host, body["notification_id"], ident, event,
                                           raw.decode(), clock())
        if new:
            log.info("notification from %s: %s %s", host, event or "agent.message", ident or "(no issue)")
            # Shown within this refresh: the projects whose story it names publish now.
            for rt in list(projects.runtimes.values()):
                if ident in _identifiers(rt):
                    rt.hub.rebuild()
        return JSONResponse({"id": row, "new": new}, status_code=202)

    @router.get("/api/projects/{slug}/notifications", dependencies=[Depends(require_operator)])
    def listing(slug: str) -> dict[str, Any]:
        """Every notification kept of the project's stories, seen or not."""
        rt = projects.get(slug)
        return {"notifications": project_notifications(registry.notifications(_identifiers(rt)), rt.hub.doc.get("nodes") or [],
                                                       rt.hub.open_questions(), clock(), shown_only=False)}

    @router.post("/api/projects/{slug}/notifications/seen")
    async def seen(slug: str, body: SeenBody, who: Identity = Depends(require_operator)) -> dict[str, Any]:
        """A person saw these notifications: they are no longer unread."""
        rt = projects.get(slug)
        mine = {r["id"] for r in await asyncio.to_thread(registry.notifications, _identifiers(rt))}
        ids = sorted(set(body.ids) & mine)
        n = await asyncio.to_thread(registry.see_notifications, ids, who.name or who.email or who.key, clock())
        rt.hub.rebuild()
        return {"seen": n, "home": rt.hub.home}

    return router
