"""What an approver does to stories: approve, pull back, cancel, supersede."""

from __future__ import annotations

import asyncio
import logging
from typing import Any

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from .auth import APPROVER, Identity
from .projects import ProjectManager, ProjectRuntime
from .sortie import SortieError

log = logging.getLogger(__name__)


class ApproveBody(BaseModel):
    keys: list[str] = []
    epic: str | None = None


class StoryBody(BaseModel):
    key: str


def story_router(projects: ProjectManager, require_role) -> APIRouter:
    router = APIRouter(prefix="/api/projects/{slug}")
    approver = require_role(APPROVER)

    def node(rt: ProjectRuntime, key: str) -> dict[str, Any] | None:
        return next((n for n in (rt.hub.doc.get("nodes") or []) if n.get("plan_key") == key), None)

    async def refreshed(rt: ProjectRuntime) -> None:
        await asyncio.to_thread(rt.plan.refresh)

    def writable(rt: ProjectRuntime) -> None:
        if not rt.config.sync_enabled:
            raise HTTPException(status_code=409, detail={"code": "sync_disabled",
                                                         "message": "this workbench does not write to the tracker (WORKBENCH_SYNC=0)"})

    def sortie_failure(exc: SortieError) -> HTTPException:
        return HTTPException(status_code=exc.status if exc.status >= 400 else 502, detail={"code": exc.code, "message": exc.message})

    @router.post("/approve")
    async def approve(slug: str, body: ApproveBody, who: Identity = Depends(approver)) -> dict:
        rt = projects.get(slug)
        snap = rt.plan.snapshot()
        if snap["errors"] or snap["graph"] is None:
            raise HTTPException(status_code=409, detail={"code": "plan_invalid", "message": "fix the plan's validation errors first",
                                                         "errors": snap["errors"]})
        stories = rt.plan.stories
        approvals, _ = rt.records()
        if body.epic:
            # Batch 0: every story of an epic none of whose stories is approved yet.
            # A story its pull requests deliver is no one's to approve.
            keys = [k for k in stories if k.split("/", 1)[0] == body.epic and not stories[k].get("delivered_by")]
            if not keys:
                raise HTTPException(status_code=404, detail={"code": "no_stories", "message": f"epic {body.epic} has no stories"})
            if any(k in approvals and approvals[k]["digest"] == stories[k]["digest"] for k in keys):
                raise HTTPException(status_code=409, detail={"code": "epic_already_approved",
                                                             "message": "the epic is approved: approve its new stories one by one or together"})
        else:
            keys = list(dict.fromkeys(body.keys))
            unknown = [k for k in keys if k not in stories]
            if not keys or unknown:
                raise HTTPException(status_code=404, detail={"code": "unknown_story", "message": f"no such story: {', '.join(unknown) or '(none)'}"})
            delivered = [k for k in keys if stories[k].get("delivered_by")]
            if delivered:
                raise HTTPException(status_code=409, detail={"code": "delivered_by", "message":
                                    f"{', '.join(delivered)} names the pull requests that deliver it, so no agent takes it: nothing to approve"})
        await asyncio.to_thread(rt.registry.approve, rt.project.id, {k: stories[k]["digest"] for k in keys}, who.key, who.name)
        log.info("project %s: %s approved %s", slug, who.key, ", ".join(keys))
        await refreshed(rt)
        rt.sync.trigger("approve")
        return {"approved": keys, "by": who.public()}

    @router.post("/stories/pull-back")
    async def pull_back(slug: str, body: StoryBody, who: Identity = Depends(approver)) -> dict:
        rt = projects.get(slug)
        writable(rt)
        try:
            await rt.hub.sortie.pull_back(body.key)
        except SortieError as exc:
            raise sortie_failure(exc) from exc
        log.info("project %s: %s pulled back %s", slug, who.key, body.key)
        await rt.hub.poll_once()
        rt.sync.trigger("pull-back")
        return {"pulled_back": body.key, "node": node(rt, body.key)}

    def dependents(rt: ProjectRuntime, key: str) -> list[str]:
        graph = rt.plan.snapshot()["graph"] or {"blocks": []}
        return sorted({b["blocked"] for b in graph["blocks"] if b["blocker"] == key and "/" in b["blocked"]})

    @router.post("/stories/cancel")
    async def cancel(slug: str, body: StoryBody, who: Identity = Depends(approver)) -> dict:
        rt = projects.get(slug)
        writable(rt)
        try:
            await rt.hub.sortie.cancel(body.key)
        except SortieError as exc:
            raise sortie_failure(exc) from exc
        # The direct dependents were planned on the cancelled story's work:
        # a person must look at them again.
        deps = dependents(rt, body.key)
        if deps:
            await asyncio.to_thread(rt.registry.withdraw, rt.project.id, deps, "blocker_canceled", body.key)
        log.info("project %s: %s cancelled %s; re-review %s", slug, who.key, body.key, deps)
        await refreshed(rt)  # the withdrawn approvals change the graph
        await rt.hub.poll_once()
        rt.sync.trigger("cancel")
        return {"cancelled": body.key, "rereview": deps}

    @router.post("/stories/supersede")
    async def supersede(slug: str, body: StoryBody, who: Identity = Depends(approver)) -> dict:
        """Confirm the supersede that replacement ``key`` declares: cancel the
        story it replaces. The dependents follow the replacement and keep
        their approval."""
        rt = projects.get(slug)
        meta = rt.plan.stories.get(body.key)
        if not meta or not meta.get("supersedes"):
            raise HTTPException(status_code=409, detail={"code": "no_supersedes", "message": f"{body.key} declares no supersedes"})
        if rt.hub.fields.get(body.key, {}).get("plan_status") == "draft":
            raise HTTPException(status_code=409, detail={"code": "replacement_draft", "message": "approve the replacement first"})
        replaced = meta["supersedes"]
        writable(rt)
        try:
            await rt.hub.sortie.cancel(replaced)
        except SortieError as exc:
            raise sortie_failure(exc) from exc
        log.info("project %s: %s confirmed %s superseding %s", slug, who.key, body.key, replaced)
        await rt.hub.poll_once()
        rt.sync.trigger("supersede")
        return {"superseded": replaced, "by": body.key}

    return router
