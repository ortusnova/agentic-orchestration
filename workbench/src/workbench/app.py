"""The FastAPI application: sign-in, projects, and each project's plan graph, sync, approval, and planning session."""

from __future__ import annotations

import asyncio
import re
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import Depends, FastAPI, HTTPException, Request
from fastapi.responses import FileResponse, JSONResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from planlib.requirements import requirement_details

from .auth import PLANNER, Sessions
from .auth_routes import auth_router, make_checks
from .config import Config
from .delivery import GitHubClient
from .hub import sse
from .oidc import OIDCClient
from .project_routes import project_router
from .projects import ProjectManager
from .registry import Registry, RegistryError
from .sortie import SortieClient
from .session_routes import session_router
from .story_routes import story_router
from .supervisor_routes import supervisor_router
from .sync import sync_router
from .worker_routes import worker_router

# A change name, as the planning schemas define it; it also keeps the path inside the store.
CHANGE_NAME = re.compile(r"[a-z0-9]+(-[a-z0-9]+)*")


_CODES = {401: "unauthorized", 403: "forbidden", 404: "not_found"}


def create_app(config: Config, **services) -> FastAPI:
    """Build the app. ``services`` lets tests inject collaborators:
    ``validate`` replaces planlib.validate, ``sortie`` the Sortie client, and
    ``sessions`` builds each project's session manager from
    ``(project, store, runtime_dir)``, and ``broker_transport`` carries the
    requests to workers' brokers, and ``github`` the GitHub client that looks up
    the pull requests stories name."""
    sortie = services.get("sortie") or SortieClient(config.sortie_url, config.sortie_token)
    # The other Sorties whose workers the workbench shows, by name.
    peers = services.get("peers")
    if peers is None:
        peers = {name: SortieClient(url, token) for name, url, token in config.peer_sorties}
    github = services.get("github") or GitHubClient(config.github_api_url, config.github_token)
    registry = Registry(config.home / "workbench.db")
    projects = ProjectManager(config, registry, sortie, services.get("sessions"), services.get("validate"),
                              broker_transport=services.get("broker_transport"), github=github, peers=peers)

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        await projects.start()
        try:
            yield
        finally:
            await projects.stop()
            await sortie.aclose()
            for peer in peers.values():
                await peer.aclose()
            await github.aclose()
            if app.state.oidc is not None:
                await app.state.oidc.aclose()
            registry.close()

    app = FastAPI(title="plan workbench", docs_url=None, redoc_url=None, openapi_url=None, lifespan=lifespan)
    app.state.projects = projects
    app.state.sortie = sortie
    sessions = Sessions(config.secret_key, config.session_max_age)
    app.state.config = config
    app.state.sessions = sessions
    app.state.services = services

    oidc = services.get("oidc")
    if oidc is None and config.oidc:
        oidc = OIDCClient(config.oidc_issuer, config.oidc_client_id, config.oidc_client_secret, config.oidc_redirect_url,
                          scopes=config.oidc_scopes)
    app.state.oidc = oidc
    current_identity, require_role = make_checks(sessions)
    # Every signed-in person holds at least the planner role.
    require_operator = require_role(PLANNER)
    app.state.require_operator = require_operator
    app.state.require_role = require_role
    app.state.current_identity = current_identity

    app.include_router(auth_router(config, sessions, oidc, current_identity))

    app.include_router(project_router(projects, require_operator))

    @app.get("/api/projects/{slug}/validation", dependencies=[Depends(require_operator)])
    def validation(slug: str) -> dict:
        snap = projects.get(slug).plan.snapshot()
        return {"version": snap["version"], "valid": snap["valid"], "errors": snap["errors"], "has_graph": snap["graph"] is not None}

    @app.get("/api/projects/{slug}/graph", dependencies=[Depends(require_operator)])
    def graph(slug: str) -> dict:
        return projects.get(slug).hub.doc

    @app.get("/api/projects/{slug}/home", dependencies=[Depends(require_operator)])
    def home(slug: str) -> dict:
        return projects.get(slug).hub.home

    @app.get("/api/projects/{slug}/workers", dependencies=[Depends(require_operator)])
    def workers(slug: str) -> dict:
        """What each worker host does and what waits on it."""
        return projects.get(slug).hub.fleet

    @app.get("/api/projects/{slug}/changes/{change}/requirements", dependencies=[Depends(require_operator)])
    def requirements(slug: str, change: str) -> dict:
        rt = projects.get(slug)
        if not CHANGE_NAME.fullmatch(change) or change == "archive" or not (rt.store / "openspec" / "changes" / change).is_dir():
            raise HTTPException(status_code=404, detail={"code": "unknown_change", "message": f"no change {change!r} in the store"})
        return {"change": change, "capabilities": requirement_details(rt.store, change)}

    @app.get("/api/projects/{slug}/events", dependencies=[Depends(require_operator)])
    async def events(slug: str, request: Request) -> StreamingResponse:
        hub = projects.get(slug).hub
        queue = hub.subscribe()

        async def stream():
            try:
                yield sse("graph", hub.doc)
                idle = 0.0
                while not await request.is_disconnected():
                    if projects.runtimes.get(slug) is None:
                        yield sse("deleted", {"slug": slug})
                        return
                    try:
                        doc = await asyncio.wait_for(queue.get(), timeout=1)
                    except TimeoutError:
                        idle += 1
                        if idle >= 15:
                            idle = 0
                            yield ": keepalive\n\n"
                        continue
                    idle = 0
                    yield sse("graph", doc)
            finally:
                hub.unsubscribe(queue)

        return StreamingResponse(stream(), media_type="text/event-stream", headers={"Cache-Control": "no-store"})

    app.include_router(sync_router(projects, require_operator))
    app.include_router(story_router(projects, require_role))
    app.include_router(supervisor_router(projects, require_role))
    app.include_router(session_router(projects, require_operator))
    app.include_router(worker_router(projects, require_role))

    @app.exception_handler(RegistryError)
    async def registry_error(_request: Request, exc: RegistryError) -> JSONResponse:
        detail = {"code": exc.code, "message": exc.message, **({"field": exc.field} if exc.field else {})}
        return JSONResponse({"error": detail}, status_code=exc.status)

    @app.exception_handler(HTTPException)
    async def http_error(_request: Request, exc: HTTPException) -> JSONResponse:
        detail = exc.detail if isinstance(exc.detail, dict) else {"code": _CODES.get(exc.status_code, "error"), "message": str(exc.detail)}
        return JSONResponse({"error": detail}, status_code=exc.status_code)

    _mount_frontend(app, config.frontend_dir)
    return app


def create_app_from_env() -> FastAPI:
    """The app for ``uvicorn --factory``, configured from the environment."""
    return create_app(Config.from_env())


def _mount_frontend(app: FastAPI, dist: Path | None) -> None:
    if dist is None or not (dist / "index.html").is_file():
        return
    assets = dist / "assets"
    if assets.is_dir():
        app.mount("/assets", StaticFiles(directory=assets), name="assets")

    @app.get("/{path:path}", include_in_schema=False)
    def spa(path: str) -> FileResponse:
        if path.startswith("api/"):
            raise HTTPException(status_code=404, detail="not found")
        candidate = (dist / path).resolve()
        if path and candidate.is_file() and dist.resolve() in candidate.parents and candidate.name != "index.html":
            return FileResponse(candidate)
        # index.html names the hashed bundles of this build, so the browser
        # must ask for it every time; otherwise it keeps running an old build.
        return FileResponse(dist / "index.html", headers={"Cache-Control": "no-cache"})
