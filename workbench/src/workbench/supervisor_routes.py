"""Pause and resume Sortie's supervisor, under /api/projects/{slug}/supervisor."""

from __future__ import annotations

import logging
from typing import Literal

from fastapi import APIRouter, Depends, HTTPException

from .auth import APPROVER, Identity
from .projects import ProjectManager
from .sortie import SortieError

log = logging.getLogger(__name__)


def supervisor_router(projects: ProjectManager, require_role) -> APIRouter:
    router = APIRouter(prefix="/api/projects/{slug}/supervisor")
    approver = require_role(APPROVER)

    @router.post("/{op}")
    async def control(slug: str, op: Literal["pause", "resume"], sortie: str | None = None,
                      who: Identity = Depends(approver)) -> dict:
        """Pause or resume a supervisor through its Sortie's control API, in the
        person's name: the one of ``sortie``, by name, else the one that
        dispatches the project. This writes nothing to the tracker, so a
        read-only workbench (WORKBENCH_SYNC=0), such as a ui story's preview,
        may do it."""
        rt = projects.get(slug)
        hub = rt.hub
        if sortie is None or sortie == hub.sortie.name:
            target = hub.sortie
            if not (hub.doc.get("sortie") or {}).get("dispatched"):
                raise HTTPException(status_code=409, detail={"code": "not_dispatched", "message":
                                    f"Sortie runs agents for its configured project only, not {rt.project.huly_project}"})
        else:
            target = next((p for p in hub.peers if p.name == sortie), None)
            if target is None:
                raise HTTPException(status_code=404, detail={"code": "unknown_sortie", "message":
                                    f"this workbench reads no Sortie named {sortie}"})
        try:
            await target.supervisor(op, who.name or who.email or who.key)
        except SortieError as exc:
            if exc.status == 404 or exc.code == "supervisor_not_configured":
                raise HTTPException(status_code=409, detail={"code": "no_supervisor", "message":
                                    "this Sortie has no supervisor to pause or resume"}) from exc
            raise HTTPException(status_code=exc.status if exc.status >= 400 else 502,
                                detail={"code": exc.code, "message": exc.message}) from exc
        log.info("project %s: %s chose %s on the supervisor of %s", slug, who.key, op, target.name)
        # The new status shows now, not at the next poll.
        await hub.poll_once()
        return {"supervisor": hub.doc.get("supervisor"), "by": who.public()}

    return router
