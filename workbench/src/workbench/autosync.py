"""The approved plan reaches the tracker on its own (workbench-auto-sync).

Each project has one :class:`SyncLoop`. A trigger (an approval, a pull-back,
a cancel, a supersede, or a change to the approved part of the graph) wakes
it; a short debounce folds a burst into one sync, and a trigger during a sync
causes exactly one more. A sync previews, then applies that preview, through
Sortie, and asks Sortie to poll at once when it released a story.
"""

from __future__ import annotations

import asyncio
import copy
import hashlib
import json
import logging
import time
from typing import Any, Awaitable, Callable

from .plan_overlay import built_unapproved, sync_graph
from .sortie import SortieError

log = logging.getLogger(__name__)


class SyncFailure(Exception):
    """A sync that did not finish. ``retry`` is False when only a person can
    fix it (an invalid plan, a tracker without a capability): the loop then
    waits for the next trigger instead of retrying on a timer."""

    def __init__(self, code: str, message: str, *, retry: bool = True, result: dict[str, Any] | None = None):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
        self.retry = retry
        self.result = result


class SyncLoop:
    def __init__(self, run_once: Callable[[], Awaitable[dict[str, Any]]], *, debounce: float = 0.3,
                 first_backoff: float = 1.0, max_backoff: float = 60.0):
        self.run_once = run_once
        self.debounce = debounce
        self.first_backoff = first_backoff
        self.max_backoff = max_backoff
        self._wake = asyncio.Event()
        self._loop: asyncio.AbstractEventLoop | None = None
        self._listeners: list[Callable[[dict[str, Any]], None]] = []
        self._status: dict[str, Any] = {"state": "idle", "synced_at": None, "failed_at": None, "reason": None,
                                        "result": None, "retry_at": None, "runs": 0}

    def bind(self, loop: asyncio.AbstractEventLoop) -> None:
        self._loop = loop

    def on_status(self, listener: Callable[[dict[str, Any]], None]) -> None:
        self._listeners.append(listener)

    def status(self) -> dict[str, Any]:
        return dict(self._status)

    def trigger(self, why: str = "") -> None:
        """Ask for a sync. Safe from any thread."""
        if why:
            log.debug("sync triggered: %s", why)
        if self._loop is None:
            self._wake.set()
            return
        self._loop.call_soon_threadsafe(self._wake.set)

    def _set(self, **changes: Any) -> None:
        self._status.update(changes)
        snapshot = self.status()
        for listener in list(self._listeners):
            try:
                listener(snapshot)
            except Exception:
                log.exception("sync status listener failed")

    async def run(self, stop: asyncio.Event) -> None:
        backoff: float | None = None
        while not stop.is_set():
            try:
                await asyncio.wait_for(self._wake.wait(), timeout=backoff)
                await asyncio.sleep(self.debounce)
            except TimeoutError:
                pass
            if stop.is_set():
                return
            # Triggers from here on cause one more sync after this one.
            self._wake.clear()
            self._set(state="syncing", runs=self._status["runs"] + 1)
            try:
                result = await self.run_once()
            except SyncFailure as f:
                backoff = self._next_backoff(backoff) if f.retry else None
                self._failed(f.code, f.message, f.result, backoff)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                log.exception("sync failed")
                backoff = self._next_backoff(backoff)
                self._failed("sync_error", str(exc) or type(exc).__name__, None, backoff)
            else:
                backoff = None
                self._set(state="synced", synced_at=time.time(), reason=None, result=result, retry_at=None)

    def _next_backoff(self, current: float | None) -> float:
        return min(self.max_backoff, current * 2 if current else self.first_backoff)

    def _failed(self, code: str, message: str, result: dict[str, Any] | None, backoff: float | None) -> None:
        log.warning("sync failed: %s: %s%s", code, message, f"; retrying in {backoff:g}s" if backoff else "")
        now = time.time()
        self._set(state="failed", failed_at=now, reason={"code": code, "message": message}, result=result,
                  retry_at=now + backoff if backoff else None)


def approved_digest(graph: dict[str, Any] | None, fields: dict[str, dict[str, Any]]) -> str | None:
    """A digest of what a sync would write: the approved nodes, and the ui
    stories built before their approval, as a sync sends them, and the
    relations among them. A draft edit leaves it unchanged."""
    if graph is None:
        return None
    g = sync_graph(graph, fields)
    keep = {n["plan_key"] for n in g["nodes"]
            if n.get("approval") == "approved" or built_unapproved(n, n.get("review"))}
    nodes = []
    for n in g["nodes"]:
        if n["plan_key"] not in keep:
            continue
        # Release is one way: a story that started stays released, and needs no sync.
        if fields.get(n["plan_key"], {}).get("frozen"):
            n = {**n, "release": True}
        nodes.append(n)
    doc = {
        "nodes": sorted(nodes, key=lambda n: n["plan_key"]),
        "blocks": sorted((b["blocker"], b["blocked"]) for b in g.get("blocks") or []
                         if b["blocker"] in keep and b["blocked"] in keep),
        "parents": sorted((p["child"], p["parent"]) for p in g.get("parents") or []
                          if p["child"] in keep and p["parent"] in keep),
    }
    return hashlib.sha256(json.dumps(doc, sort_keys=True, default=str).encode()).hexdigest()


async def sync_project(hub) -> dict[str, Any]:
    """One sync of a project: preview, then apply that preview. Returns
    Sortie's apply result; raises :class:`SyncFailure`."""
    try:
        return await _sync(hub)
    finally:
        # Show the tracker's new state, as a manual sync did.
        await hub.poll_once()


async def _sync(hub) -> dict[str, Any]:
    snap = hub.plan.snapshot()
    if snap["errors"] or snap["graph"] is None:
        raise SyncFailure("plan_invalid", "the plan has validation errors; it syncs once they are fixed", retry=False)
    graph = sync_graph(copy.deepcopy(snap["graph"]), hub.fields)
    for attempt in (1, 2):
        try:
            preview = await hub.sortie.preview(graph)
        except SortieError as exc:
            raise SyncFailure(exc.code, exc.message) from exc
        missing = preview["preview"].get("missing_capabilities") or []
        if missing:
            raise SyncFailure("tracker_unsupported_operation", "the tracker lacks " + ", ".join(missing), retry=False)
        try:
            applied = await hub.sortie.apply(graph, preview["preview_id"])
            break
        except SortieError as exc:
            # The tracker changed between preview and apply: preview again, once.
            if exc.code == "stale_preview" and attempt == 1:
                continue
            raise SyncFailure(exc.code, exc.message, result=exc.body.get("result")) from exc
    result = applied.get("result") or {}
    if result.get("released"):
        try:
            await hub.sortie.refresh()
        except SortieError as exc:
            log.warning("released %d stories, but Sortie did not poll at once: %s", len(result["released"]), exc.message)
    return result
