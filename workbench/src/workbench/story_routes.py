"""What an approver does to stories: approve, pull back, cancel, supersede."""

from __future__ import annotations

import asyncio
import logging
from typing import Any

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from .auth import APPROVER, Identity
from .faults import fault_of
from .plan_overlay import needs_spec, tracker_by_key
from .projects import ProjectManager, ProjectRuntime
from .sortie import SortieError

log = logging.getLogger(__name__)


class ApproveBody(BaseModel):
    keys: list[str] = []
    epic: str | None = None


class StoryBody(BaseModel):
    key: str


class AnswerBody(BaseModel):
    key: str
    text: str


class FaultBody(BaseModel):
    id: str


class SessionsBody(BaseModel):
    key: str
    sessions: int = 4

# The most sessions one grant gives a story.
MAX_GRANT = 32


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

    def without_spec(rt: ProjectRuntime, graph: dict[str, Any], approvals: dict[str, dict], keys: list[str]) -> list[str]:
        """The keys that read "Needs a spec": no spec, not delivered, and no
        approval that matches their content."""
        nodes = {n["plan_key"]: n for n in graph["nodes"]}
        tracker = tracker_by_key(rt.hub.tracker)
        plan_states = (rt.hub.tracker or {}).get("plan_states")
        stories = rt.plan.stories
        return [k for k in keys
                if not (k in approvals and approvals[k]["digest"] == stories[k]["digest"])
                and needs_spec(nodes.get(k, {}), tracker.get(k), plan_states)]

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
            # A story its pull requests deliver is no one's to approve, and
            # a ui story is approved by the merge of its pull request.
            keys = [k for k in stories if k.split("/", 1)[0] == body.epic and not stories[k].get("delivered_by")
                    and stories[k].get("review") != "ui"]
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
            ui = [k for k in keys if stories[k].get("review") == "ui"]
            if ui:
                raise HTTPException(status_code=409, detail={"code": "ui_story", "keys": ui, "message":
                                    f"{', '.join(ui)} has review: ui: it is built without an approval, "
                                    "and merging its pull request approves it"})
        # A story with no spec is never approved; the epic's batch leaves it out.
        unspecified = without_spec(rt, snap["graph"], approvals, keys)
        if unspecified and (not body.epic or len(unspecified) == len(keys)):
            raise HTTPException(status_code=409, detail={"code": "no_spec", "plan_key": unspecified[0], "keys": unspecified,
                                                         "message": f"{', '.join(unspecified)} covers no requirement: give it a spec first"})
        keys = [k for k in keys if k not in unspecified]
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
        if rt.hub.fields.get(body.key, {}).get("plan_status") in ("draft", "needs_spec"):
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

    @router.post("/stories/answer")
    async def answer(slug: str, body: AnswerBody, who: Identity = Depends(approver)) -> dict:
        """Answer the question a parked story's agent asked, through Sortie's
        answer API: Sortie posts it on the issue, in the person's name, and
        releases the park. The workbench writes nothing to the tracker."""
        rt = projects.get(slug)
        text = body.text.strip()
        if not text:
            raise HTTPException(status_code=400, detail={"code": "empty_answer", "message": "write an answer first"})
        n = node(rt, body.key)
        ident = ((n or {}).get("tracker") or {}).get("identifier")
        if not n or n.get("kind") != "story" or not ident:
            raise HTTPException(status_code=404, detail={"code": "unknown_story", "message": f"no synced story {body.key}"})
        writable(rt)
        try:
            # The Sortie that holds the park takes the answer: p1's or another's.
            await rt.hub.sortie_for(ident).answer(ident, text, who.name or who.email or who.key)
        except SortieError as exc:
            if exc.status == 409:
                await rt.hub.poll_once()
                raise HTTPException(status_code=409, detail={"code": exc.code or "not_parked",
                                                             "message": f"{body.key} no longer waits on an answer"}) from exc
            raise sortie_failure(exc) from exc
        if rt.registry is not None:
            await asyncio.to_thread(rt.registry.record_command, rt.project.id, ident, None, "answer", text, who.key, who.name)
        log.info("project %s: %s answered %s (%s)", slug, who.key, body.key, ident)
        # The park is released: the question leaves the list now, not at the next poll.
        await rt.hub.poll_once()
        return {"answered": body.key, "identifier": ident, "by": who.public(), "node": node(rt, body.key)}

    @router.post("/faults/done")
    async def fault_done(slug: str, body: FaultBody, who: Identity = Depends(approver)) -> dict:
        """Say that the person action of a platform fault is done: the Sortie
        that holds the fault checks it again at once, and releases the
        stories it holds when the check passes."""
        rt = projects.get(slug)
        fault = next((f for f in rt.hub.faults if f["id"] == body.id), None)
        sortie = rt.hub.sortie_named(fault["sortie"]) if fault else None
        if fault is None or sortie is None:
            raise HTTPException(status_code=404, detail={"code": "platform_fault_not_found",
                                                         "message": f"no open platform fault {body.id} holds this project's stories"})
        writable(rt)
        author = who.name or who.email or who.key
        try:
            result = await sortie.platform_fault_done(fault["id"], author)
        except SortieError as exc:
            if exc.status == 404:
                await rt.hub.poll_once()
                raise HTTPException(status_code=404, detail={"code": exc.code or "platform_fault_not_found",
                                                             "message": "the fault is no longer open: Sortie cleared it"}) from exc
            raise sortie_failure(exc) from exc
        resolved = bool(result.get("resolved"))
        if rt.registry is not None:
            for ident in [s["identifier"] for s in fault["stories"] if s["key"]]:
                await asyncio.to_thread(rt.registry.record_command, rt.project.id, ident, None, "fault_done",
                                        fault["action"], who.key, who.name)
        log.info("project %s: %s marked platform fault %s done on %s; resolved: %s", slug, who.key, fault["id"],
                 sortie.name, resolved)
        # A passing check released the stories: the fault leaves the list now.
        await rt.hub.poll_once()
        after = fault_of(sortie.name, result.get("platform_fault") or {})
        return {"done": fault["id"], "resolved": resolved, "sortie": sortie.name, "by": who.public(),
                "released": fault["keys"] if resolved else [],
                "last_check_error": (after or {}).get("last_check_error"),
                "fault": next((f for f in rt.hub.faults if f["id"] == fault["id"]), None)}

    @router.post("/stories/sessions")
    async def sessions(slug: str, body: SessionsBody, who: Identity = Depends(approver)) -> dict:
        """Give a story whose session budget is used more sessions, through
        the Sortie that holds it, which runs the story again with no change
        to its workflow."""
        rt = projects.get(slug)
        if not 1 <= body.sessions <= MAX_GRANT:
            raise HTTPException(status_code=400, detail={"code": "invalid_sessions", "message": f"grant between 1 and {MAX_GRANT} sessions"})
        n = node(rt, body.key)
        ident = ((n or {}).get("tracker") or {}).get("identifier")
        if not n or n.get("kind") != "story" or not ident:
            raise HTTPException(status_code=404, detail={"code": "unknown_story", "message": f"no synced story {body.key}"})
        writable(rt)
        not_held = HTTPException(status_code=409, detail={"code": "not_budget_held",
                                                          "message": f"{body.key} is not held by its session budget"})
        # Sortie grants sessions to any issue it holds; the workbench offers
        # them only to a story its session budget holds.
        if not any(s["identifier"] == ident and s["kind"] == "budget" and (s.get("budget") or {}).get("session_budget")
                   for w in rt.hub.fleet.get("workers") or [] for s in w.get("stuck") or []):
            raise not_held
        sortie = rt.hub.sortie_for(ident)
        try:
            result = await sortie.grant_sessions(ident, body.sessions, who.name or who.email or who.key)
        except SortieError as exc:
            if exc.status == 409 or (exc.status == 404 and exc.code == "issue_not_found"):
                await rt.hub.poll_once()
                raise not_held from exc
            if exc.status in (404, 405):
                raise HTTPException(status_code=501, detail={"code": "grant_unsupported",
                                                             "message": f"{sortie.name}'s Sortie cannot grant sessions per issue yet"}) from exc
            raise sortie_failure(exc) from exc
        if rt.registry is not None:
            await asyncio.to_thread(rt.registry.record_command, rt.project.id, ident, None, "sessions", str(body.sessions), who.key, who.name)
        log.info("project %s: %s granted %s %d more sessions on %s", slug, who.key, body.key, body.sessions, sortie.name)
        await rt.hub.poll_once()
        return {"granted": body.sessions, "key": body.key, "identifier": ident, "sortie": sortie.name, "result": result,
                "node": node(rt, body.key)}

    return router
