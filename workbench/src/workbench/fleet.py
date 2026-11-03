"""What each worker host is doing and what waits on it
(agent-platform/run-supervision).

The workbench reads the state of every Sortie it knows: p1's, and any
other, such as taichi's homelab Sortie. A Sortie that runs its agents on
its own host (environment kind smolvm) is one worker; one that hands jobs
to worker daemons (kind remote) lists its workers in ``workers``. From
those states this module builds the fleet document: per worker its
capacity and use, its agent slots as lanes, its jobs with their phase and
the step they report, what waits on it and why, the machines no Sortie
started, and every open pull request of a story with what it waits on.

Everything here is computed from the states alone; nothing here writes.
Fields Sortie may not serve yet are read where they are and skipped where
they are not, so an older Sortie still gives slots, jobs and holds.
"""

from __future__ import annotations

import time
from datetime import datetime
from typing import Any, Iterable

PHASES = ("build", "review", "fix", "live_proof", "deploy")
PHASE_TEXT = {"build": "build", "review": "review", "fix": "fix", "live_proof": "live proof", "deploy": "deploy"}
# How long a job of each phase is expected to run when Sortie gives no
# estimate, in seconds: the length of its box in the lanes.
PHASE_ESTIMATE = {"build": 3600.0, "review": 900.0, "fix": 1800.0, "live_proof": 2700.0, "deploy": 900.0}
# A pull request that waits on nothing for this long is stuck: nothing merges it.
READY_STUCK_SECONDS = 600.0

# What a pull request may wait on, and its words.
PULL_WAITS = {"review": "its review", "ci": "CI", "conflict": "a merge conflict", "live_proof": "a live proof",
              "person": "a person", "none": "nothing: ready to merge"}

# The reaction kinds that make a run a fix, and what the pull request then waits on.
_FIX_REACTIONS = {"ci_failure": "ci", "ci": "ci", "review_comments": "review", "review": "review",
                  "conflict": "conflict", "merge_conflict": "conflict"}


def ts(v: Any) -> float | None:
    """A time in Unix seconds, from seconds, milliseconds or an ISO string."""
    if isinstance(v, bool) or v is None:
        return None
    if isinstance(v, (int, float)):
        return float(v) / 1000 if v > 1e10 else float(v)
    if isinstance(v, str) and v.strip():
        try:
            t = datetime.fromisoformat(v.strip().replace("Z", "+00:00")).timestamp()
        except ValueError:
            return None
        # Go's zero time is no time.
        return t if t > 0 else None
    return None


def _words(code: str | None) -> str:
    return (code or "").replace("_", " ").strip()


def phase_of(entry: dict[str, Any]) -> str:
    """A run's phase: Sortie's, the job's own, or read from its rule and reaction."""
    for v in (entry.get("phase"), (entry.get("step") or {}).get("phase") if isinstance(entry.get("step"), dict) else None):
        if isinstance(v, str) and v.strip():
            p = v.strip().lower().replace(" ", "_").replace("-", "_")
            if p in PHASES:
                return p
    if entry.get("reaction_kind"):
        return "fix"
    rule = " ".join(str(x or "") for x in (entry.get("rule_name"), (entry.get("stage_position") or {}).get("current"))).lower()
    for word, phase in (("proof", "live_proof"), ("deploy", "deploy"), ("fix", "fix"), ("review", "review")):
        if word in rule:
            return phase
    return "build"


def step_of(entry: dict[str, Any]) -> tuple[str | None, float | None]:
    """The step the job reports, and when it reported it."""
    step = entry.get("step")
    if isinstance(step, dict):
        name = step.get("name") or step.get("step")
        return (name.strip() if isinstance(name, str) and name.strip() else None), ts(step.get("at") or step.get("step_at"))
    if isinstance(step, str) and step.strip():
        return step.strip(), ts(entry.get("step_at"))
    return None, None


def _size(v: Any) -> dict[str, float] | None:
    if not isinstance(v, dict):
        return None
    out = {}
    for k in ("cpu", "mem_gib", "disk_gib"):
        x = v.get(k)
        if isinstance(x, (int, float)) and not isinstance(x, bool):
            out[k] = float(x)
    return out or None


def _sum(a: dict[str, float] | None, b: dict[str, float] | None) -> dict[str, float] | None:
    if a is None or b is None:
        return None
    return {k: a.get(k, 0.0) + b.get(k, 0.0) for k in ("cpu", "mem_gib", "disk_gib")}


def _plural(n: int, one: str, many: str | None = None) -> str:
    return f"{n} {one if n == 1 else (many or one + 's')}"


def _ago(seconds: float | None) -> str:
    if seconds is None:
        return "for an unknown time"
    s = max(0, int(seconds))
    if s < 90:
        return f"{s} s"
    if s < 5400:
        return f"{round(s / 60)} min"
    if s < 172800:
        return f"{s / 3600:.1f} h"
    return f"{round(s / 86400)} d"


class _Stories:
    """The project's stories, by tracker identifier."""

    def __init__(self, nodes: Iterable[dict[str, Any]]):
        self.by_ident: dict[str, dict[str, Any]] = {}
        for n in nodes:
            ident = (n.get("tracker") or {}).get("identifier")
            if n.get("kind") == "story" and ident:
                self.by_ident[ident] = n

    def ref(self, ident: str | None, title: str | None = None) -> dict[str, Any]:
        n = self.by_ident.get(ident or "")
        key = n["plan_key"] if n else None
        return {"identifier": ident, "key": key, "label": key.split("/", 1)[-1] if key else ident,
                "title": (n or {}).get("title") or title or ident}

    def ours(self, ident: str | None) -> bool:
        return bool(ident) and ident in self.by_ident


def _pull(entry: dict[str, Any] | None) -> dict[str, Any] | None:
    pr = (entry or {}).get("pull_request")
    if not isinstance(pr, dict) or not pr.get("number"):
        return None
    return {"number": pr.get("number"), "repository": pr.get("repository") or None, "url": pr.get("url") or None}


class _Worker:
    def __init__(self, name: str, sortie: str, *, slots: int | None, used: dict | None = None, free: dict | None = None,
                 capacity: dict | None = None, last_seen: float | None = None, unmanaged: list | None = None,
                 capabilities: list[str] | None = None, remote: bool = False):
        self.name = name
        self.sortie = sortie
        self.slots = slots
        self.used = used
        self.capacity = capacity or _sum(used, free)
        self.last_seen = last_seen
        self.unmanaged = unmanaged or []
        self.capabilities = capabilities or []
        self.remote = remote
        self.jobs: list[dict[str, Any]] = []
        self.stuck: list[dict[str, Any]] = []
        self.waits: list[dict[str, Any]] = []
        self.reasons: list[dict[str, Any]] = []
        self.draining: dict[str, Any] | None = None
        # Whether its Sortie serves its use, its unmanaged machines and its jobs' steps.
        self.known = {"use": used is not None, "unmanaged": unmanaged is not None, "steps": True}


# The Sortie state fields the view reads that an older Sortie may not
# serve, and what the view cannot show without each.
MISSING_TEXT = {
    "host": "its host's capacity and use, and the machines it did not start",
    "workers[].declaration.used": "each worker's use of its capacity",
    "workers[].declaration.unmanaged": "the machines on each worker that no Sortie started",
    "running[].step": "the step each job reports",
    "waits": "what waits on a worker beyond its slot holds and queue",
    "pulls": "what each open pull request waits on, beyond those of running or parked stories",
}


def missing_fields(state: dict[str, Any]) -> list[dict[str, str]]:
    """The fields of MISSING_TEXT this Sortie's state lacks: the view says
    so plainly instead of showing them as empty."""
    out = []
    remote = state.get("workers")
    listed = remote.get("workers") if isinstance(remote, dict) and isinstance(remote.get("workers"), list) else []
    if listed:
        decls = [(w, w.get("declaration") or {}) for w in listed if isinstance(w, dict)]
        if any("used" not in d for _, d in decls):
            out.append("workers[].declaration.used")
        if any("unmanaged" not in d and "unmanaged" not in w for w, d in decls):
            out.append("workers[].declaration.unmanaged")
    elif not isinstance(state.get("host"), dict):
        out.append("host")
    running = [r for r in state.get("running") or [] if isinstance(r, dict)]
    leases = [x for x in (remote.get("leases") or []) if isinstance(x, dict)] if isinstance(remote, dict) else []
    # Sortie serves a job's step only from a worker daemon's heartbeat
    # (kind remote): on its own host there is no step to serve.
    if listed and running and not any("step" in r for r in running) and not any("step" in x for x in leases):
        out.append("running[].step")
    for key in ("waits", "pulls"):
        if key not in state:
            out.append(key)
    return [{"field": f, "text": MISSING_TEXT[f]} for f in out]


def _workers_of(name: str, state: dict[str, Any], now: float) -> list[_Worker]:
    """The worker hosts one Sortie's state names: those of kind remote, or
    the Sortie's own host."""
    remote = state.get("workers")
    if isinstance(remote, dict) and isinstance(remote.get("workers"), list):
        out = []
        for w in remote["workers"]:
            d = w.get("declaration") or {}
            caps = [c for c in [d.get("os"), d.get("arch"), "kvm" if d.get("kvm") else None,
                                "nested" if d.get("nested") else None, *(d.get("backends") or [])] if c]
            out.append(_Worker(w.get("id") or "worker", name, slots=d.get("run_slots"), used=_size(d.get("used")),
                               free=_size(d.get("free")), last_seen=ts(w.get("last_check_in")),
                               unmanaged=d.get("unmanaged", w.get("unmanaged")), capabilities=caps, remote=True))
        if out:
            return out
    host = state.get("host") if isinstance(state.get("host"), dict) else {}
    cap = ((state.get("config") or {}).get("agent") or {}).get("max_concurrent_agents")
    return [_Worker(host.get("name") or name, name, slots=cap if isinstance(cap, int) and cap > 0 else None,
                    used=_size(host.get("used")), free=_size(host.get("free")), capacity=_size(host.get("capacity")),
                    last_seen=ts(state.get("generated_at")) or now, unmanaged=host.get("unmanaged"))]


def _pick(workers: list[_Worker], name: str | None) -> _Worker:
    if name:
        for w in workers:
            if w.name == name:
                return w
    return workers[0]


def _hold(r: dict[str, Any]) -> dict[str, Any] | None:
    if not r.get("held"):
        return None
    reason = _words(r.get("hold_reason")) or "held"
    return {"kind": "permission", "reason": f"held: {reason}" + (f", by {r['holder']}" if r.get("holder") else ""),
            "since": ts(r.get("held_since")), "action": "open"}


def build_fleet(states: list[tuple[str, dict[str, Any] | None, str | None]], nodes: list[dict[str, Any]],
                statements: dict[str, str] | None = None, now: float | None = None) -> dict[str, Any]:
    """The fleet document. ``states`` holds, per Sortie, its name, its state
    (None when it could not be read) and why not; ``nodes`` the merged
    graph's nodes; ``statements`` the full question of each parked story."""
    now = time.time() if now is None else now
    stories = _Stories(nodes)
    statements = statements or {}
    workers: list[_Worker] = []
    pulls: list[dict[str, Any]] = []
    sorties: list[dict[str, Any]] = []
    where: dict[str, str] = {}

    for name, state, error in states:
        if state is None:
            sorties.append({"name": name, "reachable": False, "error": error, "missing": []})
            continue
        missing = missing_fields(state)
        sorties.append({"name": name, "reachable": True, "error": None, "missing": missing})
        mine = _workers_of(name, state, now)
        running = [r for r in state.get("running") or [] if isinstance(r, dict)]
        if running and not any(r.get("step") for r in running):
            for w in mine:
                w.known["steps"] = False
        workers += mine
        leases = {l.get("job"): l for l in ((state.get("workers") or {}).get("leases") or []) if isinstance(l, dict)}
        # Sortie's waits[] already holds its slot holds, its queue and its
        # drain; built again from held[], workers.queue[] and dispatch_paused
        # they would show twice. Those are read only from a Sortie without it.
        served_waits = isinstance(state.get("waits"), list)
        if isinstance(state.get("dispatch_paused"), dict) and not served_waits:
            for w in mine:
                w.draining = {"reason": state["dispatch_paused"].get("reason") or None,
                              "since": ts(state["dispatch_paused"].get("paused_at") or state["dispatch_paused"].get("since"))}

        def ours(ident: str | None) -> bool:
            # The Sortie that dispatches the project lists only its issues;
            # another may run issues of other projects too.
            return stories.ours(ident)

        for r in state.get("running") or []:
            ident = r.get("issue_identifier")
            if not ours(ident):
                continue
            where[ident] = name
            lease = leases.get(ident) or {}
            w = _pick(mine, r.get("worker") or lease.get("worker"))
            step, step_at = step_of(r)
            if step is None:
                step, step_at = step_of(lease)
            phase = phase_of({**lease, **r} if lease.get("phase") and not r.get("phase") else r)
            started = ts(r.get("started_at")) or ts(r.get("dispatched_at")) or now
            ends = ts(r.get("estimated_end"))
            w.jobs.append({
                **stories.ref(ident, r.get("issue_title")), "sortie": name, "worker": w.name,
                "phase": phase, "phase_text": PHASE_TEXT[phase], "stage": r.get("rule_name") or None,
                "step": step, "step_at": step_at, "started_at": started, "elapsed": max(0.0, now - started),
                "ends_at": ends if ends else started + PHASE_ESTIMATE[phase], "estimated": not ends,
                "session_id": r.get("session_id") or None, "watchable": bool((r.get("broker") or {}).get("http_url")),
                "hold": _hold(r), "pull": _pull(r), "turn": r.get("turn_count") or None,
                "status_text": r.get("status_text") or r.get("last_message") or None,
            })

        for p in state.get("parked") or []:
            ident = p.get("issue_identifier")
            if not ours(ident):
                continue
            where[ident] = name
            w = _pick(mine, p.get("worker"))
            q = statements.get(ident) or p.get("statement") or ""
            asks = p.get("reason") in (None, "", "agent_blocked")
            stopped = _words(p.get("reason")) or "parked"
            w.stuck.append({**stories.ref(ident, p.get("issue_title")), "sortie": name, "worker": w.name,
                            "kind": "question", "since": ts(p.get("parked_at")), "phase": phase_of(p),
                            "reason": (f"asks: {q}" if asks else f"stopped ({stopped}): {q}") if q
                                      else "stopped for a person without saying why",
                            "question": q or None, "action": "answer"})
        for b in state.get("budget_exhausted") or []:
            ident = b.get("issue_identifier")
            if not ours(ident):
                continue
            where[ident] = name
            w = _pick(mine, b.get("worker"))
            used, budget = b.get("used_sessions"), b.get("budget_sessions")
            # Sortie's reason: session_budget, token_budget or money_budget.
            # A grant of sessions releases a session budget only.
            sessions = b.get("reason") == "session_budget"
            reason = _words(b.get("reason")) or "budget exhausted"
            if sessions and isinstance(used, int) and isinstance(budget, int):
                reason = f"used {used} of its {budget} sessions"
            elif not sessions:
                reason = f"{reason} exhausted"
            w.stuck.append({**stories.ref(ident, b.get("issue_title")), "sortie": name, "worker": w.name,
                            "kind": "budget", "since": ts(b.get("exhausted_at")), "phase": phase_of(b),
                            "reason": reason, "budget": {"used_sessions": used, "budget_sessions": budget,
                                                         "session_budget": sessions},
                            "action": "sessions" if sessions else "story"})
        for r in state.get("retrying") or []:
            ident = r.get("issue_identifier")
            if ours(ident):
                where.setdefault(ident, name)

        # What waits on a worker: Sortie's own waits, its slot holds, and
        # its queue of jobs no worker took.
        for x in state.get("waits") or []:
            ident = x.get("issue_identifier")
            if ident and not ours(ident):
                continue
            w = _pick(mine, x.get("worker"))
            if x.get("kind") == "drain":
                # A paused dispatch: the jobs on the worker end, and it takes no new one.
                w.draining = {"reason": x.get("reason") or None, "since": ts(x.get("since"))}
                if not ident:
                    w.waits.append({"identifier": None, "key": None, "label": "deploy", "title": "A deploy", "sortie": name,
                                    "kind": "drain", "reason": x.get("reason") or "waits for the host to drain",
                                    "release": x.get("release") or None, "since": ts(x.get("since"))})
                    continue
            w.waits.append({**stories.ref(ident), "sortie": name, "kind": x.get("kind") or "slot",
                            "reason": x.get("reason") or x.get("reason_text") or _words(x.get("kind")),
                            "release": x.get("release") or x.get("release_text") or None, "since": ts(x.get("since"))})
        for h in [] if served_waits else state.get("held") or []:
            ident = h.get("issue_identifier")
            hold = h.get("hold") or {}
            if not ours(ident) or hold.get("reason") != "agent_slots_full":
                continue
            w = _pick(mine, h.get("worker"))
            slot = hold.get("slot") or {}
            w.waits.append({**stories.ref(ident, h.get("issue_title")), "sortie": name, "kind": "slot",
                            "reason": hold.get("reason_text") or f"waits for an agent slot ({slot.get('used')} of {slot.get('limit')} busy)",
                            "release": hold.get("release_text") or "a slot frees", "since": ts(hold.get("held_since"))})
        for q in [] if served_waits else ((state.get("workers") or {}).get("queue") or []):
            job = q.get("job") or {}
            ident = (job.get("issue") or {}).get("identifier") or job.get("id")
            if not ours(ident):
                continue
            waiting = [str(s) for s in q.get("waiting") or []]
            caps = (job.get("requires") or {}).get("capabilities") or []
            lacking = [c for c in caps if not any(c in w.capabilities for w in mine)]
            kind = "capability" if lacking or any("capab" in s for s in waiting) else "slot"
            reason = "; ".join(waiting) or (f"no worker has {', '.join(lacking)}" if lacking else "waits for a worker with a free slot")
            targets = [w for w in mine if q.get("worker") == w.name] or (
                [w for w in mine if all(c in w.capabilities for c in caps)] if kind == "slot" else mine) or mine
            for w in targets:
                w.waits.append({**stories.ref(ident, (job.get("issue") or {}).get("title")), "sortie": name, "kind": kind,
                                "reason": reason, "release": "a worker with it checks in" if kind == "capability" else "a slot frees",
                                "since": ts(q.get("queued_at"))})

        # Every open pull request of a story, and what it waits on.
        seen_pulls = set()
        for p in state.get("pulls") or []:
            ident = p.get("issue_identifier")
            if not ours(ident):
                continue
            waits_on = p.get("waits_on") if p.get("waits_on") in PULL_WAITS else "review"
            w = _pick(mine, p.get("worker"))
            pull = _pull_doc(stories.ref(ident), p, waits_on, p.get("reason"), ts(p.get("since")), name, w.name, now)
            pulls.append(pull)
            seen_pulls.add(ident)
        for r in state.get("running") or []:
            ident = r.get("issue_identifier")
            pr = _pull(r)
            if not ours(ident) or pr is None or ident in seen_pulls:
                continue
            phase = phase_of(r)
            waits_on = _FIX_REACTIONS.get(r.get("reaction_kind") or "", "review" if phase in ("review", "fix") else
                                          ("live_proof" if phase == "live_proof" else "review"))
            w = _pick(mine, r.get("worker") or (leases.get(ident) or {}).get("worker"))
            pulls.append(_pull_doc(stories.ref(ident), pr, waits_on, f"{PHASE_TEXT[phase]} runs now", ts(r.get("started_at")),
                                   name, w.name, now))
            seen_pulls.add(ident)
        for p in state.get("parked") or []:
            ident = p.get("issue_identifier")
            pr = _pull(p)
            if ours(ident) and pr and ident not in seen_pulls:
                pulls.append(_pull_doc(stories.ref(ident), pr, "person", "its agent asks a person", ts(p.get("parked_at")),
                                       name, _pick(mine, p.get("worker")).name, now))

    by_name = {(w.sortie, w.name): w for w in workers}
    for pull in pulls:
        w = by_name.get((pull["sortie"], pull["worker"]))
        if w is None:
            continue
        if pull["waits_on"] == "live_proof":
            if not any(x["identifier"] == pull["identifier"] and x["kind"] == "live_proof" for x in w.waits):
                w.waits.append({"identifier": pull["identifier"], "key": pull["key"], "label": pull["label"], "title": pull["title"],
                                "sortie": pull["sortie"], "kind": "live_proof", "pull": _pr_ref(pull),
                                "reason": pull["reason"] or "its pull request waits for a live proof",
                                "release": "the live proof running here passes", "since": pull["since"]})
        if pull["stuck"]:
            w.reasons.append({"code": "ready_to_merge", "identifier": pull["identifier"], "key": pull["key"],
                              "text": f"{pull['label']}'s pull request #{pull['number']} is ready to merge and has waited "
                                      f"{_ago(now - pull['since'] if pull['since'] else None)} with nothing merging it",
                              "action": "pull", "url": pull["url"]})
    for w in workers:
        for x in w.waits:
            if x["kind"] == "live_proof" and "pull" not in x:
                p = next((p for p in pulls if p["identifier"] == x["identifier"]), None)
                x["pull"] = _pr_ref(p) if p else None
        if w.draining and not any(x["kind"] == "drain" for x in w.waits):
            w.waits.append({"identifier": None, "key": None, "label": "deploy", "title": "A deploy", "sortie": w.sortie,
                            "kind": "drain", "since": w.draining["since"], "release": "the jobs running here end",
                            "reason": "waits for the host to drain" + (f": {w.draining['reason']}" if w.draining["reason"] else "")
                                      + (f", {_plural(len(w.jobs), 'job')} still running" if w.jobs else "")})

    return {"generated_at": now, "sorties": sorties, "workers": [_worker_doc(w, now) for w in workers],
            "pulls": pulls, "where": where}


def _pr_ref(p: dict[str, Any]) -> dict[str, Any]:
    return {"number": p["number"], "repository": p["repository"], "url": p["url"], "waits_on": p["waits_on"]}


def _pull_doc(ref: dict[str, Any], p: dict[str, Any], waits_on: str, reason: Any, since: float | None,
              sortie: str, worker: str, now: float) -> dict[str, Any]:
    stuck = waits_on == "none" and since is not None and now - since >= READY_STUCK_SECONDS
    text = PULL_WAITS[waits_on]
    return {**ref, "number": p.get("number"), "repository": p.get("repository") or None, "url": p.get("url") or None,
            "waits_on": waits_on, "waits_text": "ready to merge" if waits_on == "none" else f"waits on {text}",
            "reason": (reason if isinstance(reason, str) and reason.strip() else None), "since": since,
            "sortie": sortie, "worker": worker, "stuck": stuck}


def _worker_doc(w: _Worker, now: float) -> dict[str, Any]:
    jobs = sorted(w.jobs, key=lambda j: (j["started_at"], j["identifier"] or ""))
    slots = w.slots if isinstance(w.slots, int) and w.slots > 0 else max(1, len(jobs))
    lanes: list[dict[str, Any]] = []
    for i in range(max(slots, len(jobs))):
        job = jobs[i] if i < len(jobs) else None
        if job is not None:
            job["slot"] = i + 1
        lanes.append({"slot": i + 1, "job": job, "stuck": None, "extra": i >= slots})
    # A story that stopped for a person or a budget holds the next free lane
    # in sight, so the lane reads stuck, with why, where its slot is.
    free = [lane for lane in lanes if lane["job"] is None]
    overflow = []
    for s in sorted(w.stuck, key=lambda s: (s["since"] or 0.0, s["identifier"] or "")):
        if free:
            lane = free.pop(0)
            lane["stuck"] = s
            s["slot"] = lane["slot"]
        else:
            s["slot"] = None
            overflow.append(s)
    for lane in lanes:
        j = lane["job"]
        if j is not None and j["hold"]:
            lane["stuck"] = {**{k: j[k] for k in ("identifier", "key", "label", "title", "sortie", "worker", "phase")},
                             **j["hold"], "slot": lane["slot"]}

    busy = len(jobs)
    reasons = list(w.reasons)
    for lane in lanes:
        s = lane["stuck"]
        if s:
            reasons.insert(0, {"code": s["kind"], "identifier": s["identifier"], "key": s["key"],
                               "text": f"slot {lane['slot']}: {s['label']} {s['reason']}", "action": s["action"]})
    for s in overflow:
        reasons.append({"code": s["kind"], "identifier": s["identifier"], "key": s["key"],
                        "text": f"{s['label']} {s['reason']}", "action": s["action"]})
    slot_waits = [x for x in w.waits if x["kind"] == "slot"]
    idle_lanes = [lane for lane in lanes if lane["job"] is None and lane["stuck"] is None and not lane["extra"]]
    if slot_waits and idle_lanes and not w.draining:
        reasons.append({"code": "slot_idle", "identifier": slot_waits[0]["identifier"], "key": slot_waits[0]["key"],
                        "text": f"{_plural(len(slot_waits), 'story', 'stories')} wait for a slot while "
                                f"{_plural(len(idle_lanes), 'slot')} {'is' if len(idle_lanes) == 1 else 'are'} free", "action": "story"})
    if reasons:
        verdict = "stuck"
    elif w.draining:
        verdict = "draining"
        reasons.append({"code": "draining", "text": "draining for a deploy: it takes no new job" + (
            f" ({w.draining['reason']})" if w.draining["reason"] else ""), "identifier": None, "key": None, "action": None})
    elif busy:
        verdict = "moving"
        reasons.append({"code": "moving", "identifier": None, "key": None, "action": None,
                        "text": f"{_plural(busy, 'job')} running in {_plural(slots, 'slot')}"
                                + (f", {_plural(len(w.waits), 'item')} waiting" if w.waits else "")})
    else:
        verdict = "idle"
        reasons.append({"code": "idle", "identifier": None, "key": None, "action": None,
                        "text": "nothing runs and nothing waits" if not w.waits else f"nothing runs; {_plural(len(w.waits), 'item')} waiting"})
    order = {"slot": 0, "capability": 1, "live_proof": 2, "drain": 3}
    waits = sorted(w.waits, key=lambda x: (order.get(x["kind"], 9), x["since"] or 0.0, x["identifier"] or ""))
    nxt = [x for x in waits if x["kind"] in ("slot", "live_proof") and x["identifier"]]
    unmanaged = []
    for m in w.unmanaged:
        if not isinstance(m, dict) or not m.get("name"):
            continue
        since = ts(m.get("since") or m.get("created_at"))
        unmanaged.append({"name": m["name"], "since": since, "age": (now - since) if since else None,
                          "age_text": _ago(now - since) if since else "age unknown", "state": m.get("state") or None})
    return {
        "name": w.name, "sortie": w.sortie, "remote": w.remote, "last_seen": w.last_seen,
        "slots": slots, "slots_known": isinstance(w.slots, int) and w.slots > 0, "busy": busy,
        "capacity": w.capacity, "used": w.used,
        "verdict": verdict, "reasons": reasons, "lanes": lanes, "jobs": jobs, "stuck": sorted(w.stuck, key=lambda s: s["slot"] or 99),
        "waits": waits, "next": nxt[:3], "unmanaged": unmanaged, "draining": w.draining, "known": dict(w.known),
    }


def lanes_summary(fleet: dict[str, Any], health: dict[str, Any] | None) -> dict[str, Any]:
    """What the home's slot lanes say above the lanes: whether work moves or
    is stuck and why, and how many slots are busy. The count of what needs a
    person is the Needs a person section's, not this."""
    workers = fleet.get("workers") or []
    reasons: list[dict[str, Any]] = []
    for s in fleet.get("sorties") or []:
        if not s["reachable"]:
            reasons.append({"code": "sortie_unreachable", "text": f"{s['name']}'s Sortie is unreachable: {s['error'] or 'no answer'}",
                            "worker": s["name"], "key": None, "identifier": None, "action": None})
    for w in workers:
        if w["verdict"] == "stuck":
            for r in w["reasons"]:
                reasons.append({**r, "worker": w["name"], "text": f"{w['name']} {r['text']}"})
    for r in (health or {}).get("reasons") or []:
        # Sortie and sync failures are the project's; the held runs the lanes already say.
        if r.get("code") in ("sync_failed", "state_unavailable") or (r.get("code") == "sortie_unreachable" and not fleet.get("sorties")):
            reasons.append({**r, "worker": None, "identifier": None, "action": None})
    busy = sum(w["busy"] for w in workers)
    slots = sum(w["slots"] for w in workers if w["slots_known"]) or None
    if reasons:
        verdict = "stuck"
    else:
        verdict = (health or {}).get("verdict") or ("healthy" if busy else None)
        reasons = [{**r, "worker": None, "identifier": None, "action": None} for r in (health or {}).get("reasons") or []]
    missing = [{**m, "sortie": s["name"]} for s in fleet.get("sorties") or [] for m in s.get("missing") or []]
    return {"verdict": verdict, "reasons": reasons, "busy": busy, "slots": slots, "missing": missing,
            "runway": (health or {}).get("runway"), "generated_at": fleet.get("generated_at")}


def _release(s: dict[str, Any]) -> str | None:
    """What releases a stuck story."""
    if s["kind"] == "budget":
        return "more sessions" if s.get("action") == "sessions" else "a larger budget in its workflow"
    return {"question": "your answer", "permission": "a person in its session"}.get(s["kind"])


def story_states(fleet: dict[str, Any], held: dict[str, dict[str, Any]] | None = None) -> dict[str, dict[str, Any]]:
    """Each story's run state, by tracker identifier: where it runs or why it
    waits, what releases it, its pull request and what that waits on, and
    the action that unblocks it. ``held`` holds Sortie's hold rows of the
    stories it keeps back, by identifier."""
    out: dict[str, dict[str, Any]] = {}
    pulls = {p["identifier"]: p for p in fleet.get("pulls") or []}

    def put(ident: str | None, doc: dict[str, Any]) -> None:
        if ident and ident not in out:
            p = pulls.get(ident)
            out[ident] = {**doc, "pull": ({**_pr_ref(p), "waits_text": p["waits_text"], "reason": p["reason"], "stuck": p["stuck"]}
                                           if p else None)}

    for w in fleet.get("workers") or []:
        for lane in w["lanes"]:
            s = lane["stuck"]
            if s:
                put(s["identifier"], {"status": "stuck", "worker": w["name"], "sortie": s["sortie"], "slot": lane["slot"],
                                      "why": s["reason"], "release": _release(s),
                                      "kind": s["kind"], "action": s["action"], "phase": s.get("phase"), "step": None})
        for s in w["stuck"]:
            if s["slot"] is None:
                put(s["identifier"], {"status": "stuck", "worker": w["name"], "sortie": s["sortie"], "slot": None, "why": s["reason"],
                                      "release": _release(s),
                                      "kind": s["kind"], "action": s["action"], "phase": s.get("phase"), "step": None})
        for j in w["jobs"]:
            put(j["identifier"], {"status": "running", "worker": w["name"], "sortie": j["sortie"], "slot": j.get("slot"),
                                  "why": f"{j['phase_text']} runs on {w['name']}" + (f", at {j['step']}" if j["step"] else ""),
                                  "release": None, "kind": None, "action": "open", "phase": j["phase"], "step": j["step"],
                                  "started_at": j["started_at"]})
        for x in w["waits"]:
            if x["kind"] == "drain":
                continue
            put(x["identifier"], {"status": "waiting", "worker": w["name"], "sortie": x["sortie"], "slot": None,
                                  "why": x["reason"], "release": x["release"], "kind": x["kind"],
                                  "action": "pull" if x["kind"] == "live_proof" else None, "phase": None, "step": None})
    for ident, h in (held or {}).items():
        hold = h.get("hold") or {}
        put(ident, {"status": "waiting", "worker": None, "sortie": h.get("sortie"), "slot": None,
                    "why": hold.get("reason_text") or _words(hold.get("reason")) or "held back",
                    "release": hold.get("release_text") or None, "kind": hold.get("reason") or None,
                    "action": None, "phase": None, "step": None})
    for ident, p in pulls.items():
        put(ident, {"status": "pull", "worker": p["worker"], "sortie": p["sortie"], "slot": None,
                    "why": f"its pull request #{p['number']} {p['waits_text']}", "release": None, "kind": p["waits_on"],
                    "action": "pull", "phase": None, "step": None})
    return out
