"""Each registered project at run time: its plan, its merged graph, its session, its syncs."""

from __future__ import annotations

import asyncio
import logging
import shutil
from pathlib import Path
from typing import Any, Callable

from fastapi import HTTPException

from .autosync import SyncLoop, sync_project
from .config import Config
from .delivery import DeliveryWatch, GitHubClient, GitHubError
from .hub import Hub
from .plan_state import PlanState, watch
from .registry import Project, Registry, RegistryError, check_new, suggest_slug
from .scaffold import UPGRADE_MESSAGE, SchemaUpgrade, ScaffoldError, push_upgrade, scaffold_store, upgrade_schemas
from .sessions import SessionManager, runtime_base
from .sortie import SortieClient, SortieError
from .workers import WorkerRelay

log = logging.getLogger(__name__)

# Builds a project's session manager: (project, store, runtime directory).
SessionFactory = Callable[[Project, Path, Path], SessionManager]


class ProjectRuntime:
    """One project's live state. Started when the project opens, stopped when
    it closes; its planning session runs apart and survives both."""

    def __init__(self, project: Project, store: Path, config: Config, sortie: SortieClient,
                 sessions: SessionManager, validate=None, registry: Registry | None = None, broker_transport=None,
                 github: GitHubClient | None = None, others: dict[str, SortieClient] | None = None):
        self.project = project
        self.store = store
        self.config = config
        self.registry = registry
        self.plan = PlanState(store, inputs=self.plan_inputs, **({"validate": validate} if validate else {}))
        # The pull requests the stories name in delivered_by, looked up on GitHub.
        self.deliveries = DeliveryWatch(github or GitHubClient(config.github_api_url, config.github_token),
                                        self.delivered_by, registry=registry, recheck=config.delivery_recheck_seconds)
        # Every Sortie whose workers the home shows; the main one also serves the plan.
        self.hub = Hub(self.plan, sortie.bind(project.huly_project), records=self.records, pulls=self.deliveries.pulls,
                       sortie_name=config.sortie_name,
                       others={name: c.bind(project.huly_project) for name, c in (others or {}).items()})
        self.deliveries.on_change = self.hub.rebuild
        self._own_github = github is None
        self.sessions = sessions
        # The brokers of the workers Sortie runs for this project's stories.
        self.workers = WorkerRelay(sortie.bind(project.huly_project), transport=broker_transport)
        # The approved plan syncs to the tracker on its own.
        self.sync = SyncLoop(lambda: sync_project(self.hub), debounce=config.debounce_ms / 1000,
                             first_backoff=config.sync_backoff_first, max_backoff=config.sync_backoff_max)
        self.sync.on_status(self.hub.publish_sync)
        self.hub.sync_status = self.sync.status()
        if config.sync_enabled:
            self.hub.on_approved_change = lambda: self.sync.trigger("graph")
        else:
            self.hub.read_only = True
            self.hub.sync_status = {**self.hub.sync_status, "state": "disabled"}
        self._stop = asyncio.Event()
        self._tasks: list[asyncio.Task] = []

    @property
    def slug(self) -> str:
        return self.project.slug

    def records(self) -> tuple[dict[str, dict], dict[str, dict]]:
        if self.registry is None:
            return {}, {}
        return self.registry.approvals(self.project.id), self.registry.rereviews(self.project.id)

    def delivered_by(self) -> set[str]:
        """Every pull request a story of the plan names."""
        return {ref for meta in self.plan.stories.values() for ref in meta.get("delivered_by") or []}

    def plan_inputs(self) -> tuple[dict[str, str], dict[str, str]]:
        """What the validator needs: approved digests and the frozen set."""
        approvals, _ = self.records()
        return {k: r["digest"] for k, r in approvals.items()}, dict(self.hub.frozen)

    def import_legacy_approvals(self) -> list[str]:
        """Import approvals made in breakdown files under the old format, once:
        each still-valid approved breakdown becomes batch 0 of its epic."""
        if self.registry is None or not (self.store / "openspec").is_dir():
            return []
        import planlib

        try:
            store = planlib.load_store(self.store, planlib.Schemas.load(self.store, self.plan.replaced_schemas))
        except planlib.PlanLoadError:
            return []
        approvals = self.registry.approvals(self.project.id)
        imported = []
        for change, bd in sorted(store.breakdowns.items()):
            keys = {f"{change}/{d['id']}": planlib.story_digest(planlib.resolved(bd.doc, d)) for d in bd.doc["deliverables"]}
            if planlib.legacy_approved(bd.doc) and not any(k in approvals for k in keys):
                self.registry.approve(self.project.id, keys, f"migration|{change}", "Imported approval")
                imported.append(change)
                log.info("project %s: imported the approval of %s from its breakdown", self.slug, change)
        return imported

    def upgrade_schemas(self) -> SchemaUpgrade | None:
        """Bring the store's planning schemas up to the ones the workbench
        ships: in the store itself, or, when its branch tracks an upstream,
        on a pushed branch. Until that branch merges the plan is validated
        against the shipped schemas."""
        try:
            up = upgrade_schemas(self.store)
        except (OSError, ScaffoldError) as exc:
            log.warning("project %s: could not update its planning schemas: %s", self.slug, exc)
            return None
        if up.written:
            log.info("project %s: updated its planning schemas: %s", self.slug, ", ".join(up.paths))
            return up
        self.plan.replaced_schemas = up.replaced()
        try:
            push_upgrade(self.store, up)
        except (OSError, ScaffoldError) as exc:
            log.warning("project %s: could not push its planning schema upgrade: %s", self.slug, exc)
            return None
        if up.pushed:
            log.info("project %s: pushed its planning schema upgrade to %s on %s: %s", self.slug, up.branch,
                     up.upstream.remote, ", ".join(up.paths))
        return up

    async def propose_schemas(self, up: SchemaUpgrade | None) -> None:
        """Open the pull request for the pushed schema upgrade, unless GitHub has one."""
        if up is None or up.upstream is None or up.branch is None:
            return
        repo = up.upstream.github_repo()
        if repo is None:
            log.warning("project %s: %s is not on GitHub; merge %s into %s by hand", self.slug,
                        up.upstream.remote, up.branch, up.upstream.base)
            return
        body = ("The plan workbench ships newer planning schemas than this store has. "
                "It validates the plan against them until this merges.\n\n"
                + "".join(f"- `{p}`\n" for p in up.paths))
        try:
            pull = await self.deliveries.github.ensure_pull(*repo, head=up.branch, base=up.upstream.base,
                                                            title=UPGRADE_MESSAGE, body=body)
        except GitHubError as exc:
            log.warning("project %s: could not open the pull request for %s: %s", self.slug, up.branch, exc)
            return
        if pull["created"]:
            log.info("project %s: opened %s for its planning schemas", self.slug, pull["url"])

    async def start(self) -> None:
        self.hub.bind(asyncio.get_running_loop())
        self.sync.bind(asyncio.get_running_loop())
        await self.propose_schemas(await asyncio.to_thread(self.upgrade_schemas))
        await asyncio.to_thread(self.import_legacy_approvals)
        await asyncio.to_thread(self.plan.refresh)
        if (self.store / "openspec").is_dir():
            self._tasks.append(asyncio.create_task(watch(self.plan, self.config.debounce_ms, self._stop)))
        else:
            log.warning("project %s: store %s has no openspec/ directory; not watching it", self.slug, self.store)
        self._tasks.append(asyncio.create_task(self.hub.poll_loop(self.config.poll_seconds, self._stop)))
        self._tasks.append(asyncio.create_task(self.deliveries.run(self._stop, self.config.poll_seconds)))
        if self.config.sync_enabled:
            self._tasks.append(asyncio.create_task(self.sync.run(self._stop)))

    async def stop(self) -> None:
        self._stop.set()
        for t in self._tasks:
            t.cancel()
        await asyncio.gather(*self._tasks, return_exceptions=True)
        self._tasks.clear()
        await self.sessions.aclose()
        await self.workers.aclose()
        if self._own_github:
            await self.deliveries.github.aclose()

    def summary(self) -> dict[str, Any]:
        doc = self.hub.doc or {}
        nodes = doc.get("nodes") or []
        snap = self.plan.snapshot()
        sortie = doc.get("sortie") or {}
        return {
            **self.project.to_dict(),
            "store_path": str(self.store),
            "valid": snap["valid"],
            "errors": len(snap["errors"]),
            "epics": sum(1 for n in nodes if n.get("kind") == "epic"),
            "stories": sum(1 for n in nodes if n.get("kind") == "story"),
            "session_active": bool(self.sessions.status().get("active")),
            "sortie": {"reachable": bool(sortie.get("reachable")), "dispatched": bool(sortie.get("dispatched"))},
        }


class ProjectManager:
    """The registry plus a runtime for every registered project."""

    def __init__(self, config: Config, registry: Registry, sortie: SortieClient,
                 session_factory: SessionFactory | None = None, validate=None, broker_transport=None,
                 github: GitHubClient | None = None, others: dict[str, SortieClient] | None = None):
        self.config = config
        self.github = github
        self.others = others or {}
        self.broker_transport = broker_transport
        self.registry = registry
        self.sortie = sortie
        self.validate = validate
        self.session_factory = session_factory or self._default_sessions
        self.runtimes: dict[str, ProjectRuntime] = {}
        self._lock = asyncio.Lock()

    def _default_sessions(self, project: Project, store: Path, runtime_dir: Path) -> SessionManager:
        c = self.config
        return SessionManager(store, c.broker_command, c.agent_command, c.prompt_file,
                              runtime_dir=runtime_dir, agent_model=c.agent_model)

    def runtime_dir(self, project: Project) -> Path:
        return (self.config.runtime_dir or runtime_base()) / project.slug

    def get(self, slug: str) -> ProjectRuntime:
        rt = self.runtimes.get(slug)
        if rt is None:
            raise HTTPException(status_code=404, detail={"code": "project_not_found", "message": f"no project {slug}"})
        return rt

    async def start(self) -> None:
        if self.config.store is not None:
            self.import_store(self.config.store, self.config.huly_project)
        for project in self.registry.list():
            await self._open(project)

    def import_store(self, store: Path, huly_project: str) -> None:
        """Register an existing store as a project, once."""
        if self.registry.by_store(str(store)) is not None:
            return
        name = store.name
        try:
            slug, name, key = check_new(suggest_slug(name) or "project", name, huly_project)
            with self.registry.reserve(slug, name, key, str(store)):
                pass
        except RegistryError as exc:
            log.error("could not register WORKBENCH_STORE %s as a project: %s", store, exc.message)
            return
        log.info("registered %s as project %s, syncing into %s", store, slug, key)

    async def _open(self, project: Project) -> ProjectRuntime:
        store = project.store(self.config.home)
        sessions = self.session_factory(project, store, self.runtime_dir(project))
        rt = ProjectRuntime(project, store, self.config, self.sortie, sessions, self.validate, self.registry,
                            broker_transport=self.broker_transport, github=self.github, others=self.others)
        self.runtimes[project.slug] = rt
        await rt.start()
        return rt

    async def stop(self) -> None:
        for rt in list(self.runtimes.values()):
            await rt.stop()
        self.runtimes.clear()

    async def create(self, slug: str, name: str, huly_project: str) -> tuple[ProjectRuntime, dict[str, Any]]:
        """Reserve the project, make sure its tracker project exists, create
        its store, and only then commit it. Any failure leaves no row and no
        store; a tracker project that was created stays, and is reused by a
        retry."""
        slug, name, huly_project = check_new(slug, name, huly_project)
        rel = Path("projects") / slug
        store = self.config.home / rel
        if store.exists():
            raise RegistryError(409, "store_exists", f"{store} already exists; choose another slug", "slug")
        async with self._lock:
            with self.registry.reserve(slug, name, huly_project, str(rel)) as project:
                try:
                    tracker = await self.sortie.ensure_project(huly_project, name)
                except SortieError as exc:
                    raise RegistryError(502 if exc.status in (0, 502, 503) else 422, "tracker_" + exc.code,
                                        f"Huly project {huly_project}: {exc.message}", "huly_project") from exc
                try:
                    await asyncio.to_thread(scaffold_store, store, name)
                except ScaffoldError as exc:
                    raise RegistryError(500, "store_failed", str(exc)) from exc
            try:
                rt = await self._open(self.registry.get(slug) or project)
            except BaseException:
                log.exception("project %s was created but could not be opened", slug)
                raise
        return rt, tracker

    async def delete(self, slug: str) -> dict[str, Any]:
        """End the project's session and forget the project. Its store and its
        tracker project are kept."""
        rt = self.get(slug)
        async with self._lock:
            await rt.sessions.stop()
            await rt.stop()
            self.runtimes.pop(slug, None)
            self.registry.delete(slug)
            shutil.rmtree(self.runtime_dir(rt.project), ignore_errors=True)
        return {"deleted": slug, "store_path": str(rt.store), "huly_project": rt.project.huly_project}

    def rename(self, slug: str, name: str) -> ProjectRuntime:
        rt = self.get(slug)
        rt.project = self.registry.rename(slug, name)
        return rt

