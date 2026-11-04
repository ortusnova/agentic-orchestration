"""HTTP routes that let a person watch and steer the workers Sortie runs for
a project's stories, under /api/projects/{slug}/workers/{identifier}."""

from __future__ import annotations

import asyncio
import logging
import time
from typing import Any

from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import JSONResponse, StreamingResponse
from pydantic import BaseModel, Field

from .auth import APPROVER, PLANNER, Identity
from .projects import ProjectManager, ProjectRuntime
from .runstate import run_state
from .session_routes import ALLOWED_OPS
from .sortie import GrantUnsupported, SortieError
from .workers import WorkerError

log = logging.getLogger(__name__)

# What a planner may send: it reads, and changes nothing an agent does.
READ_OPS = {"state"}
# The most sessions one grant gives.
MAX_GRANT = 50


class GrantBody(BaseModel):
    add: int = Field(ge=1, le=MAX_GRANT)
    reason: str = ""


async def poll_now(client) -> None:
    """Ask the Sortie to poll now, so what was released runs at once."""
    try:
        await client.refresh()
    except SortieError as exc:
        log.info("sortie refresh after a release failed: %s", exc.message)


def worker_router(projects: ProjectManager, require_role) -> APIRouter:
    router = APIRouter(prefix="/api/projects/{slug}/workers/{identifier}")
    planner = require_role(PLANNER)

    def story(rt: ProjectRuntime, identifier: str) -> dict[str, Any]:
        """The story whose tracker issue is ``identifier``; 404 for any other,
        before anything reaches Sortie."""
        for n in rt.hub.doc.get("nodes") or []:
            if n.get("kind") == "story" and (n.get("tracker") or {}).get("identifier") == identifier:
                return n
        raise HTTPException(status_code=404, detail={"code": "worker_not_found", "message": f"{identifier} is not a story of this project"})

    def fail(exc: WorkerError) -> HTTPException:
        return HTTPException(status_code=exc.status, detail={"code": exc.code, "message": exc.message})

    @router.get("/messages")
    def messages(slug: str, identifier: str, _who: Identity = Depends(planner)) -> dict:
        rt = projects.get(slug)
        story(rt, identifier)
        rows = rt.registry.commands(rt.project.id, identifier) if rt.registry else []
        return {"identifier": identifier, "commands": rows}

    @router.post("/commands")
    async def command(slug: str, identifier: str, cmd: dict[str, Any], who: Identity = Depends(planner)) -> JSONResponse:
        rt = projects.get(slug)
        node = story(rt, identifier)
        op = cmd.get("op")
        if op not in ALLOWED_OPS:
            raise HTTPException(status_code=400, detail={"code": "invalid_command", "message": f"op must be one of {sorted(ALLOWED_OPS)}"})
        if op not in READ_OPS and not who.has(APPROVER):
            raise HTTPException(status_code=403, detail={"code": "role_required", "message": "steering a worker needs the approver role"})
        try:
            code, body, run = await rt.relay(identifier).command(identifier, cmd)
        except WorkerError as exc:
            raise fail(exc) from exc
        if 200 <= code < 300 and op not in READ_OPS and rt.registry is not None:
            text = cmd.get("text") if isinstance(cmd.get("text"), str) else None
            if op == "permit":
                text = str(cmd.get("option") or "")
            rec = await asyncio.to_thread(rt.registry.record_command, rt.project.id, identifier, run, op, text, who.key, who.name)
            log.info("project %s: %s sent %s to %s (%s)", slug, who.key, op, identifier, node["plan_key"])
            body = {**body, "recorded": rec} if isinstance(body, dict) else {"result": body, "recorded": rec}
        return JSONResponse(body, status_code=code)

    def sortie_failure(exc: SortieError) -> HTTPException:
        return HTTPException(status_code=exc.status if exc.status >= 400 else 502, detail={"code": exc.code, "message": exc.message})

    @router.get("/run")
    async def run(slug: str, identifier: str, _who: Identity = Depends(planner)) -> dict:
        """What the Sortie that holds the story does with it, why, and what
        releases it (agent-platform/run-supervision)."""
        rt = projects.get(slug)
        story(rt, identifier)
        name = rt.hub.holder(identifier) or rt.hub.sortie_name
        client = rt.hub.client_for(identifier)
        try:
            detail = await client.issue(identifier)
        except SortieError as exc:
            if exc.status != 404:
                raise sortie_failure(exc) from exc
            detail = None
        state = (rt.hub.worker_states.get(name) or (None, None))[0]
        return run_state(name, identifier, detail, state, can_grant=rt.hub.can_grant.get(name), now=time.time())

    @router.get("/runs/{dispatch_id}/events")
    async def run_events(slug: str, identifier: str, dispatch_id: str, _who: Identity = Depends(planner)) -> dict:
        """The agent events Sortie kept for one of the story's runs."""
        rt = projects.get(slug)
        story(rt, identifier)
        client = rt.hub.client_for(identifier)
        try:
            runs = (await client.issue_runs(identifier, limit=50)).get("runs") or []
            if not any(r.get("dispatch_id") == dispatch_id for r in runs):
                raise HTTPException(status_code=404, detail={"code": "run_not_found", "message": f"{dispatch_id} is not a run of {identifier}"})
            return await client.run_events(dispatch_id)
        except SortieError as exc:
            raise sortie_failure(exc) from exc

    @router.post("/sessions")
    async def grant(slug: str, identifier: str, body: GrantBody, who: Identity = Depends(require_role(APPROVER))) -> dict:
        """Give the story more sessions than its Sortie's budget, through that
        Sortie's grant route; say plainly when it has none yet."""
        rt = projects.get(slug)
        story(rt, identifier)
        name = rt.hub.holder(identifier) or rt.hub.sortie_name
        client = rt.hub.client_for(identifier)
        by = who.name or who.email or who.key
        try:
            result = await client.grant_sessions(identifier, body.add, by, body.reason.strip() or f"granted in the workbench by {by}")
        except GrantUnsupported as exc:
            rt.hub.can_grant[name] = False
            state = (rt.hub.worker_states.get(name) or (None, None))[0] or {}
            max_sessions = ((state.get("config") or {}).get("agent") or {}).get("max_sessions")
            raise HTTPException(status_code=501, detail={
                "code": "grant_unsupported", "sortie": name, "max_sessions": max_sessions,
                "message": f"{name}'s Sortie cannot grant sessions yet. Raise agent.max_sessions"
                           + (f" (now {max_sessions})" if max_sessions else "") + f" in {name}'s workflow instead."}) from exc
        except SortieError as exc:
            raise sortie_failure(exc) from exc
        rt.hub.can_grant[name] = True
        if rt.registry is not None:
            await asyncio.to_thread(rt.registry.record_command, rt.project.id, identifier, None, "grant", str(body.add), who.key, who.name)
        log.info("project %s: %s granted %s %d more sessions on %s", slug, who.key, identifier, body.add, name)
        await poll_now(client)
        await rt.hub.poll_once()
        return {"granted": body.add, "identifier": identifier, "sortie": name, "result": result}

    @router.get("/events")
    async def events(slug: str, identifier: str, request: Request, _who: Identity = Depends(planner)) -> StreamingResponse:
        rt = projects.get(slug)
        story(rt, identifier)

        async def connected() -> bool:
            return not await request.is_disconnected()

        last = request.headers.get("last-event-id") or request.query_params.get("last_event_id")
        stream = rt.relay(identifier).events(identifier, last, connected)

        async def relay():
            try:
                async for chunk in stream:
                    yield chunk
            except WorkerError as exc:
                log.warning("worker %s stream failed: %s", identifier, exc.message)

        return StreamingResponse(relay(), media_type="text/event-stream", headers={"Cache-Control": "no-store"})

    return router
