"""Keeps the merged graph current and fans it out to SSE subscribers."""

from __future__ import annotations

import asyncio
import json
import logging
import time
from typing import Any, Awaitable, Callable

from .autosync import approved_digest
from .fleet import build_fleet, lanes_summary, story_states
from .leases import leases_of
from .home import History, build_home
from .merge import merge
from .plan_overlay import frozen_set, overlay
from .plan_state import PlanState
from .questions import home_questions, parked, parked_at_of, question
from .sortie import ProjectSortie, SortieError
from .timeline import RunHistory, RunWorkers
from .workers import worker_fields

log = logging.getLogger(__name__)

_UNSET = object()


class Hub:
    def __init__(self, plan: PlanState, sortie: ProjectSortie, records=None, pulls=None,
                 peers: list[ProjectSortie] | None = None, previews=None,
                 alerts: Callable[[list[dict[str, Any]]], Awaitable[None]] | None = None,
                 history: RunHistory | None = None):
        self.plan = plan
        # The other Sorties whose workers this project's stories may run on,
        # and the last state each gave, or why it gave none.
        self.peers = list(peers or [])
        self.peer_states: dict[str, tuple[dict[str, Any] | None, str | None]] = {}
        # The last questions each other Sortie reported, kept while it cannot be read.
        self._peer_questions: dict[str, dict[str, dict[str, Any]]] = {}
        # Pushes the open questions to the operator's channel at every poll.
        self.alerts = alerts
        # What each worker does and what waits on it (agent-platform/run-supervision).
        self.fleet: dict[str, Any] = {"workers": [], "pulls": [], "sorties": [], "where": {}}
        # Each Sortie's recent runs, the worker each ran on, and its rules'
        # medians: the lanes' past and their forecast (timeline.py).
        self.run_history = history or RunHistory(RunWorkers())
        # The Sortie that last ran, parked or held each issue, by identifier.
        self.last_where: dict[str, str] = {}
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
        # The supervisor's leases on stories, by identifier (leases.py): a
        # story under one asks no person, whatever its park says.
        self.leases: dict[str, dict[str, Any]] = {}
        self._park_details: dict[tuple[str, str, Any], dict[str, Any]] = {}
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
        await self._poll()
        await self.alert()

    async def alert(self) -> None:
        """Push the project's open questions to the operator's channel. Not
        while the main Sortie cannot be read, when the list may be short, and
        not from a read-only workbench, which runs beside the one that does."""
        if self.alerts is None or self.read_only or self.sortie_error is not None or self.tracker is None:
            return
        questions = (self.home or {}).get("questions")
        if questions is None:
            return
        try:
            await self.alerts(list(questions))
        except Exception:  # pushing must never stop the poll
            log.exception("could not push the open questions")

    async def _poll(self) -> None:
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
        # which Sortie keeps for the project it dispatches only. The other
        # Sorties are read whatever this one does.
        await self._peer_states()
        if not graph.get("dispatched"):
            self.agent_state, self.state_error = None, None
            self.questions = await self._questions(self._peer_pairs())
            return self._peer_fields()
        try:
            state = await self.sortie.state()
        except SortieError as exc:
            log.warning("sortie state unavailable: %s", exc)
            self.agent_state, self.state_error = None, exc.message
            self.questions = await self._questions(self._peer_pairs())
            return self._peer_fields()
        self.agent_state, self.state_error = state, None
        await self._runs(self.sortie, state)
        self.questions = await self._questions([(self.sortie, state)] + self._peer_pairs())
        fields = worker_fields(state) if any(n.get("runtime") for n in graph.get("nodes") or []) else {}
        return {**self._peer_fields(), **fields}

    def _peer_fields(self) -> dict[str, dict[str, Any]]:
        """The worker fields of the runs the other Sorties hold, marked so
        that a story with no runtime in this Sortie's graph gets one."""
        out: dict[str, dict[str, Any]] = {}
        for p, s in self._peer_pairs():
            for ident, f in worker_fields(s).items():
                out[ident] = {**f, "sortie": p.name}
        return out

    async def _peer_states(self) -> None:
        for peer in self.peers:
            try:
                self.peer_states[peer.name] = (await peer.state(), None)
            except SortieError as exc:
                log.warning("sortie %s state unavailable: %s", peer.name, exc)
                self.peer_states[peer.name] = (None, exc.message)
            else:
                await self._runs(peer, self.peer_states[peer.name][0])

    async def _runs(self, sortie: ProjectSortie, state: dict[str, Any] | None) -> None:
        """Note the worker of each run the state lists, and read the Sortie's
        recent runs when due: the lanes' past and forecast."""
        self.run_history.workers.observe(sortie.name, state)
        try:
            await self.run_history.refresh(sortie.name, sortie)
        except Exception:  # the run history must never stop the poll
            log.exception("could not read %s's run history", sortie.name)

    def _peer_pairs(self) -> list[tuple[ProjectSortie, dict[str, Any]]]:
        return [(p, s) for p in self.peers if (s := self.peer_states.get(p.name, (None, None))[0]) is not None]

    def sortie_for(self, identifier: str) -> ProjectSortie:
        """The Sortie that runs, parks or holds the issue: the one whose
        state lists it last, or whose question it holds, else this project's own."""
        name = self.last_where.get(identifier) or (self.questions.get(identifier) or {}).get("sortie")
        return next((p for p in self.peers if p.name == name), self.sortie)

    def states(self) -> list[tuple[str, dict[str, Any] | None, str | None]]:
        """Each Sortie's name, its last state, and why there is none."""
        own: list[tuple[str, dict[str, Any] | None, str | None]] = []
        if self.agent_state is not None or self.state_error is not None or self.sortie_error is not None:
            own = [(self.sortie.name, self.agent_state, self.state_error or self.sortie_error)]
        return own + [(p.name, *self.peer_states.get(p.name, (None, "not read yet"))) for p in self.peers]

    def merged_state(self) -> dict[str, Any] | None:
        """Every Sortie's runs and holds as one state, for the verdict on the
        whole project; None while this project's own Sortie gave none."""
        if self.agent_state is None:
            return None
        out: dict[str, Any] = {k: list(self.agent_state.get(k) or []) for k in ("running", "retrying", "budget_exhausted", "parked")}
        cap = ((self.agent_state.get("config") or {}).get("agent") or {}).get("max_concurrent_agents")
        workers = list(((self.agent_state.get("workers") or {}).get("workers")) or [])
        for _, s in self._peer_pairs():
            for k in out:
                out[k] += list(s.get(k) or [])
            workers += list(((s.get("workers") or {}).get("workers")) or [])
            more = ((s.get("config") or {}).get("agent") or {}).get("max_concurrent_agents")
            if isinstance(cap, int) and isinstance(more, int):
                cap += more
        out["config"] = {"agent": {"max_concurrent_agents": cap} if isinstance(cap, int) else {}}
        if workers:
            out["workers"] = {"workers": workers}
        return out

    async def _questions(self, states: list[tuple[ProjectSortie, dict[str, Any]]]) -> dict[str, dict[str, Any]]:
        """The open question of each parked issue, by tracker identifier, from
        every Sortie in ``states``; each question names the Sortie that holds
        it. Another Sortie that cannot be read keeps the questions it last
        reported. The state may shorten a stop statement; the issue's own
        response holds it whole, and is read once per park."""
        out: dict[str, dict[str, Any]] = {}
        details: dict[tuple[str, str, Any], dict[str, Any] | None] = {}
        for sortie, st in states:
            held: dict[str, dict[str, Any]] = {}
            for entry in parked(st):
                ident = entry["issue_identifier"]
                park = (sortie.name, ident, parked_at_of(entry))
                detail = self._park_details.get(park) if park[2] is not None else None
                if detail is None:
                    try:
                        detail = await sortie.issue(ident)
                    except SortieError as exc:
                        log.info("%s's sortie has no details of %s's park: %s", sortie.name, ident, exc.message)
                        detail = None
                details[park] = detail
                held[ident] = {**question(entry, detail), "sortie": sortie.name}
            if sortie is not self.sortie:
                self._peer_questions[sortie.name] = held
            for ident, q in held.items():
                out.setdefault(ident, q)
        read = {s.name for s, _ in states}
        for peer in self.peers:
            if peer.name in read:
                continue
            # Unreadable now: its questions, and their park details, stay.
            details.update({k: v for k, v in self._park_details.items() if k[0] == peer.name})
            for ident, q in self._peer_questions.get(peer.name, {}).items():
                out.setdefault(ident, q)
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
        statements = {i: q["statement"] for i, q in self.questions.items() if q["statement"]}
        self.leases = leases_of(self.states())
        try:
            self.fleet = build_fleet(self.states(), doc["nodes"], statements, leases=self.leases,
                                     history=self.run_history)
            self.last_where.update(self.fleet["where"])
            held = {h["issue_identifier"]: {**h, "sortie": name} for name, s, _ in self.states() if s
                    for h in s.get("held") or [] if isinstance(h, dict) and h.get("issue_identifier")}
            runs = story_states(self.fleet, held)
        except Exception:  # the fleet must never take the graph down with it
            log.exception("could not build the workers' view")
            self.fleet, runs = {"workers": [], "pulls": [], "sorties": [], "where": {}, "error": "see the workbench log"}, {}
        for n in doc["nodes"]:
            n.update(self.fields.get(n["plan_key"], {}))
            ident = (n.get("tracker") or {}).get("identifier")
            if n.get("kind") == "story":
                n["question"] = self.questions.get(ident) if ident and ident not in self.leases else None
                n["lease"] = self.leases.get(ident) if ident else None
                n["run_state"] = runs.get(ident) if ident else None
                peer = self.workers.get(ident or "", {}).get("sortie")
                if peer and not n.get("runtime") and n.get("tracker"):
                    # Another Sortie runs it: this Sortie's graph knows no runtime of it.
                    n["runtime"] = {"status": "running", "turn_count": None, "last_message": None, "attempt": None}
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
        doc["sync"] = self.sync_status
        doc["fleet"] = self.fleet
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
                questions=home_questions(self.open_questions(), doc.get("nodes") or []),
                health_inputs={
                    "reachable": bool(sortie.get("reachable")), "sortie_error": sortie.get("error"),
                    "dispatched": bool(sortie.get("dispatched")), "project": self.sortie.project,
                    "state": self.merged_state(), "state_error": self.state_error, "sync": doc.get("sync"),
                    "statements": {i: q["statement"] for i, q in self.open_questions().items() if q["statement"]},
                    "leases": self.leases,
                },
            )
        except Exception:  # the home must never take the graph down with it
            log.exception("could not build the project home")
            return {"error": "the home could not be computed; see the workbench log"}
        home["lanes"] = {**lanes_summary(self.fleet, home.get("health")), "workers": self.fleet.get("workers") or []}
        home["version"] = doc.get("version")
        return home

    def open_questions(self) -> dict[str, dict[str, Any]]:
        """The questions that wait on a person: none of a story the
        supervisor holds, whose lease, live or expired, says what it waits on."""
        return {i: q for i, q in self.questions.items() if i not in self.leases}

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
