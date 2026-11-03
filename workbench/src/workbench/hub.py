"""Keeps the merged graph current and fans it out to SSE subscribers."""

from __future__ import annotations

import asyncio
import json
import logging
import time
from typing import Any

from .autosync import approved_digest
from .home import History, build_home
from .merge import merge
from .plan_overlay import frozen_set, overlay
from .plan_state import PlanState
from .questions import home_questions, parked, parked_at_of, question
from .sortie import ProjectSortie, SortieError
from .supervisor import supervisor_view
from .workers import worker_fields

log = logging.getLogger(__name__)

_UNSET = object()


class Hub:
    def __init__(self, plan: PlanState, sortie: ProjectSortie, records=None, pulls=None, previews=None):
        self.plan = plan
        # Called, from any thread, when the approved part of the graph changes.
        self.on_approved_change = lambda: None
        # Unset until the first build, so that build triggers the startup sync.
        self._approved: str | None | object = _UNSET
        self.sync_status: dict[str, Any] | None = None
        self.sortie = sortie
        # (approvals, rereviews) of the project, by plan key.
        self.records = records or (lambda: ({}, {}))
        # What GitHub reported of each pull request a story names, by ref.
        self.pulls = pulls or (lambda: {})
        # The previews of the ui stories' pull requests, by tracker identifier.
        self.previews = previews or (lambda: {})
        self.frozen: dict[str, str] = {}
        self.fields: dict[str, dict[str, Any]] = {}
        self._pulled_back: set[str] = set()
        self.tracker: dict[str, Any] | None = None
        # Worker fields by tracker identifier, from Sortie's state.
        self.workers: dict[str, dict[str, Any]] = {}
        self.sortie_error: str | None = None
        # Sortie's state (runs, holds, the agent concurrency cap) for a
        # project it dispatches, and why the last read failed.
        self.agent_state: dict[str, Any] | None = None
        self.state_error: str | None = None
        # The open question of each parked issue, by tracker identifier, and
        # the per-issue responses that gave their statements, by park.
        self.questions: dict[str, dict[str, Any]] = {}
        self._park_details: dict[tuple[str, Any], dict[str, Any]] = {}
        self.history = History()
        # Read-only: never pull back an edited story on its own.
        self.read_only = False
        self.home: dict[str, Any] = {}
        self.capabilities: list[str] = []
        self.version = 0
        self.doc: dict[str, Any] = {}
        self._subscribers: set[asyncio.Queue] = set()
        self._loop: asyncio.AbstractEventLoop | None = None
        plan.on_change(self.rebuild)

    def bind(self, loop: asyncio.AbstractEventLoop) -> None:
        self._loop = loop

    async def poll_once(self) -> None:
        # Catch store edits the file watcher missed.
        if await asyncio.to_thread(self.plan.stale):
            await asyncio.to_thread(self.plan.refresh)
        try:
            graph = await self.sortie.plan_graph()
        except SortieError as exc:
            self.sortie_error = exc.message
            log.warning("sortie plan graph unavailable: %s", exc)
        else:
            self.tracker = graph
            self.capabilities = list(graph.get("capabilities") or [])
            self.sortie_error = None
            self.workers = await self._workers(graph)
            approvals, _ = self.records()
            frozen = frozen_set(self.plan.stories, approvals, graph)
            if frozen != self.frozen:
                self.frozen = frozen
                # Edits to stories that just started are now refused.
                await asyncio.to_thread(self.plan.refresh)
                return
        self.rebuild()
        await self.pull_back_stale()

    async def _workers(self, graph: dict[str, Any]) -> dict[str, dict[str, Any]]:
        # The home reads the agent slots and held runs from Sortie's state,
        # which Sortie keeps for the project it dispatches only.
        if not graph.get("dispatched"):
            self.agent_state, self.state_error, self.questions = None, None, {}
            return {}
        try:
            state = await self.sortie.state()
        except SortieError as exc:
            log.warning("sortie state unavailable: %s", exc)
            self.agent_state, self.state_error, self.questions = None, exc.message, {}
            return {}
        self.agent_state, self.state_error = state, None
        self.questions = await self._questions(state)
        if not any(n.get("runtime") for n in graph.get("nodes") or []):
            return {}
        return worker_fields(state)

    async def _questions(self, state: dict[str, Any]) -> dict[str, dict[str, Any]]:
        """The open question of each parked issue, by tracker identifier. The
        state may shorten a stop statement; the issue's own response holds it
        whole, and is read once per park."""
        out: dict[str, dict[str, Any]] = {}
        details: dict[tuple[str, Any], dict[str, Any] | None] = {}
        for entry in parked(state):
            ident = entry["issue_identifier"]
            park = (ident, parked_at_of(entry))
            detail = self._park_details.get(park) if park[1] is not None else None
            if detail is None:
                try:
                    detail = await self.sortie.issue(ident)
                except SortieError as exc:
                    log.info("sortie has no details of %s's park: %s", ident, exc.message)
                    detail = None
            details[park] = detail
            out[ident] = question(entry, detail)
        self._park_details = {k: v for k, v in details.items() if v is not None}
        return out

    async def pull_back_stale(self) -> None:
        """Pull back each released story whose content changed since its
        approval, before an agent takes the old content."""
        if self.read_only:
            return
        # A story pulled back and released again may need pulling back again.
        self._pulled_back = {k for k in self._pulled_back if self.fields.get(k, {}).get("stale_release")}
        for key, f in list(self.fields.items()):
            if not f.get("stale_release") or key in self._pulled_back:
                continue
            self._pulled_back.add(key)
            try:
                await self.sortie.pull_back(key)
                log.info("pulled back %s: its content changed after its release", key)
            except SortieError as exc:
                log.warning("could not pull back edited story %s: %s", key, exc.message)

    async def poll_loop(self, interval: float, stop: asyncio.Event) -> None:
        while not stop.is_set():
            await self.poll_once()
            try:
                await asyncio.wait_for(stop.wait(), timeout=interval)
            except TimeoutError:
                pass

    def rebuild(self) -> None:
        snap = self.plan.snapshot()
        doc = merge(snap["graph"], self.tracker)
        approvals, rereviews = self.records()
        self.fields = overlay(snap["graph"], self.plan.stories, approvals, rereviews, self.tracker, self.pulls(),
                              self.previews())
        supervisor, acted_on = supervisor_view(self.agent_state, doc["nodes"])
        for n in doc["nodes"]:
            n.update(self.fields.get(n["plan_key"], {}))
            ident = (n.get("tracker") or {}).get("identifier")
            if n.get("kind") == "story":
                n["question"] = self.questions.get(ident) if ident else None
                n["supervisor_actions"] = acted_on.get(n["plan_key"], [])
            if n.get("runtime") and n.get("tracker"):
                n["runtime"] = {"stage": None, "session_id": None, "held": False, "holder": None, "watchable": False,
                                **n["runtime"], **self.workers.get(n["tracker"]["identifier"], {})}
        self.version += 1
        doc.update(
            version=self.version,
            generated_at=time.time(),
            validation={"valid": snap["valid"], "errors": snap["errors"], "version": snap["version"]},
            sortie={
                "reachable": self.sortie_error is None and self.tracker is not None,
                "error": self.sortie_error,
                "capabilities": self.capabilities,
                "project": self.sortie.project,
                # Sortie runs agents for its configured project only.
                "dispatched": bool((self.tracker or {}).get("dispatched", False)),
                "generated_at": (self.tracker or {}).get("generated_at"),
            },
        )
        doc["plan_states"] = (self.tracker or {}).get("plan_states")
        # Sortie's supervisor and its recent actions, from the state of the
        # project Sortie dispatches; None when Sortie reports none.
        doc["supervisor"] = supervisor
        doc["sync"] = self.sync_status
        doc["home"] = self.home = self._home(doc, snap)
        self.doc = doc
        self._publish(doc)
        approved = approved_digest(snap["graph"], self.fields) if snap["valid"] else None
        if approved != self._approved:
            self._approved = approved
            self.on_approved_change()

    def publish_sync(self, status: dict[str, Any]) -> None:
        """Show a new sync status, without rebuilding the graph."""
        self.sync_status = status
        if not self.doc:
            return
        self.version += 1
        self.doc = {**self.doc, "sync": status, "version": self.version}
        # A failed sync makes the agents stuck.
        self.doc["home"] = self.home = self._home(self.doc, self.plan.snapshot())
        self._publish(self.doc)

    def _home(self, doc: dict[str, Any], snap: dict[str, Any]) -> dict[str, Any]:
        sortie = doc.get("sortie") or {}
        try:
            home = build_home(
                graph=snap["graph"], nodes=doc.get("nodes") or [], fields=self.fields, planning=self.plan.planning,
                plan_states=doc.get("plan_states"), tracker_known=self.tracker is not None, valid=snap["valid"],
                errors=len(snap["errors"]), history=self.history,
                questions=home_questions(self.questions, doc.get("nodes") or []),
                health_inputs={
                    "reachable": bool(sortie.get("reachable")), "sortie_error": sortie.get("error"),
                    "dispatched": bool(sortie.get("dispatched")), "project": self.sortie.project,
                    "state": self.agent_state, "state_error": self.state_error, "sync": doc.get("sync"),
                    "statements": {i: q["statement"] for i, q in self.questions.items() if q["statement"]},
                },
            )
        except Exception:  # the home must never take the graph down with it
            log.exception("could not build the project home")
            return {"error": "the home could not be computed; see the workbench log"}
        home["version"] = doc.get("version")
        return home

    def _publish(self, doc: dict[str, Any]) -> None:
        if self._loop is None:
            return
        for q in list(self._subscribers):
            self._loop.call_soon_threadsafe(_offer, q, doc)

    def subscribe(self) -> asyncio.Queue:
        q: asyncio.Queue = asyncio.Queue(maxsize=8)
        self._subscribers.add(q)
        return q

    def unsubscribe(self, q: asyncio.Queue) -> None:
        self._subscribers.discard(q)


def _offer(q: asyncio.Queue, doc: dict[str, Any]) -> None:
    # A slow subscriber only needs the newest graph.
    while q.full():
        q.get_nowait()
    q.put_nowait(doc)


def sse(event: str, data: Any) -> str:
    return f"event: {event}\ndata: {json.dumps(data, separators=(',', ':'))}\n\n"
