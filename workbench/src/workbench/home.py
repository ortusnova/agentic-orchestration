"""The project home: how far the plan is from its goal, whether the agents
will stay busy, and which work is not ready to execute (planning/project-home).

Everything here is computed from what the hub already holds: the validated
plan with its milestones and tasks, the merged graph with each story's plan
status, Sortie's state, and the last sync. Nothing here writes anywhere.
"""

from __future__ import annotations

import time
from collections import deque
from typing import Any, Iterable

import networkx as nx

from planlib import milestones as ms

# Slack, in stories, up to which an item is "soon" rather than "later".
SOON = 4
HISTORY_HOURS = 10
HISTORY_BUCKET = 3600.0
SAMPLE_EVERY = 30.0

# The reasons a unit of work is not ready to execute, in the order they apply.
REASONS = ("no_breakdown", "no_spec", "needs_rereview", "not_approved", "held_for_approval", "waits_on_person",
           "waits_on_work", "operator_task")
COLUMN = {"no_breakdown": "spec", "no_spec": "spec", "needs_rereview": "approve", "not_approved": "approve",
          "held_for_approval": "approve", "waits_on_person": "operator", "operator_task": "operator", "waits_on_work": None}


# Story states ----------------------------------------------------------------


def tracker_states(nodes: Iterable[dict[str, Any]], plan_states: dict[str, str] | None) -> tuple[set[str], set[str]]:
    """The plan keys whose tracker issue is done, and those that were cancelled."""
    done_state = ((plan_states or {}).get("done") or "").lower()
    cancel_state = ((plan_states or {}).get("cancel") or "").lower()
    done, dropped = set(), set()
    for n in nodes:
        t = n.get("tracker")
        if not t or n.get("kind") != "story":
            continue
        state = (t.get("state") or "").lower()
        if cancel_state and state == cancel_state:
            dropped.add(n["plan_key"])
        elif (done_state and state == done_state) or (not done_state and t.get("terminal")):
            done.add(n["plan_key"])
    return done, dropped


# Execution health (D5) ----------------------------------------------------------


def held_runs(state: dict[str, Any], key_of: dict[str, str]) -> list[dict[str, Any]]:
    """Runs that wait on a permission, a budget or a person."""
    out = []
    for r in state.get("running") or []:
        if r.get("held"):
            out.append({"identifier": r["issue_identifier"], "key": key_of.get(r["issue_identifier"]),
                        "reason": _words(r.get("hold_reason") or "held"), "holder": r.get("holder") or None})
    for r in state.get("budget_exhausted") or []:
        out.append({"identifier": r["issue_identifier"], "key": key_of.get(r["issue_identifier"]),
                    "reason": _words(r.get("reason") or "budget exhausted"), "holder": None})
    for r in state.get("parked") or []:
        out.append({"identifier": r["issue_identifier"], "key": key_of.get(r["issue_identifier"]),
                    "reason": _words(r.get("reason") or "waiting on a person"), "holder": None})
    return out


def _words(code: str) -> str:
    return code.replace("_", " ").strip()


def slots_of(state: dict[str, Any] | None) -> int | None:
    agent = ((state or {}).get("config") or {}).get("agent") or {}
    cap = agent.get("max_concurrent_agents")
    return cap if isinstance(cap, int) and cap > 0 else None


def execution_health(*, reachable: bool, sortie_error: str | None, dispatched: bool, project: str,
                     state: dict[str, Any] | None, state_error: str | None, sync: dict[str, Any] | None,
                     runway: int, remaining: int, key_of: dict[str, str]) -> dict[str, Any]:
    """The verdict on whether the agents will stay busy, with its reasons.

    The first that applies wins: stuck, done, idle now, will idle, healthy.
    ``runway`` counts released, unstarted stories; ``remaining`` the planned
    stories not yet done."""
    reasons: list[dict[str, Any]] = []
    slots = slots_of(state) if state is not None else None
    running = [{"identifier": r["issue_identifier"], "key": key_of.get(r["issue_identifier"])}
               for r in (state or {}).get("running") or []]
    busy = len(running) if state is not None else None
    held = held_runs(state, key_of) if state is not None else []

    if not reachable:
        reasons.append({"code": "sortie_unreachable", "text": f"Sortie is unreachable: {sortie_error}" if sortie_error else "Sortie is unreachable"})
    elif dispatched and state is None:
        reasons.append({"code": "state_unavailable", "text": f"Sortie's agent state is unavailable: {state_error or 'no answer'}"})
    if sync and sync.get("state") == "failed":
        message = (sync.get("reason") or {}).get("message") or "unknown error"
        reasons.append({"code": "sync_failed", "text": f"The last sync failed: {message}"})
    for h in held:
        reasons.append({"code": "held", "text": f"{h['key'] or h['identifier']} is held: {h['reason']}", "key": h["key"]})

    out = {"verdict": None, "reasons": reasons, "slots": slots, "busy": busy,
           "free": max(0, slots - busy) if slots is not None and busy is not None else None,
           "running": running, "held": held, "runway": runway if reachable else None}
    if reasons:
        return {**out, "verdict": "stuck"}
    if not dispatched:
        return {**out, "reasons": [{"code": "not_dispatched",
                                    "text": f"Sortie runs agents for its configured project only, not {project}"}]}
    if remaining == 0 and not busy:
        return {**out, "verdict": "done", "reasons": [{"code": "done", "text": "Every planned story is done"}]}
    if slots is None:
        return {**out, "reasons": [{"code": "no_slots", "text": "Sortie does not report its agent concurrency cap"}]}
    free = out["free"]
    if free and runway == 0:
        return {**out, "verdict": "idle_now",
                "reasons": [{"code": "idle", "text": f"{_n(free, 'agent slot')} {'is' if free == 1 else 'are'} free and 0 stories are ready"}]}
    if runway < slots:
        return {**out, "verdict": "will_idle",
                "reasons": [{"code": "short", "text": f"{_n(runway, 'story', 'stories')} ready, fewer than the {slots} agent slots"}]}
    return {**out, "verdict": "healthy",
            "reasons": [{"code": "enough", "text": f"A runway of {_n(runway, 'story', 'stories')} against {_n(slots, 'agent slot')}"}]}


def _n(n: int, one: str, many: str | None = None) -> str:
    return f"{n} {one if n == 1 else (many or one + 's')}"


class History:
    """Busy slots and runway over the last hours, kept in memory."""

    def __init__(self, hours: int = HISTORY_HOURS, bucket: float = HISTORY_BUCKET):
        self.hours = hours
        self.bucket = bucket
        self.samples: deque[tuple[float, int | None, int | None]] = deque()

    def add(self, busy: int | None, runway: int | None, now: float | None = None) -> None:
        now = time.time() if now is None else now
        if self.samples and now - self.samples[-1][0] < SAMPLE_EVERY:
            # A burst of updates folds into one sample: the most slots busy, the newest runway.
            t, b, _ = self.samples.pop()
            busy = max(x for x in (b, busy) if x is not None) if b is not None or busy is not None else None
            now = t
        self.samples.append((now, busy, runway))
        horizon = now - self.hours * self.bucket
        while self.samples and self.samples[0][0] < horizon:
            self.samples.popleft()

    def buckets(self, now: float | None = None) -> list[dict[str, Any]]:
        """One entry per bucket, oldest first: the most slots busy and the
        last runway seen in it, or None where nothing was seen."""
        now = time.time() if now is None else now
        out = []
        for i in range(self.hours):
            end = now - (self.hours - 1 - i) * self.bucket
            start = end - self.bucket
            seen = [s for s in self.samples if start < s[0] <= end]
            busy = [s[1] for s in seen if s[1] is not None]
            runway = [s[2] for s in seen if s[2] is not None]
            out.append({"end": end, "busy": max(busy) if busy else None, "runway": runway[-1] if runway else None})
        return out


# Not ready to execute (D6) -------------------------------------------------------


class _Plan:
    """The story graph and what the home needs to ask of it."""

    def __init__(self, graph: dict[str, Any], done: set[str], dropped: set[str], estimates: dict[str, float]):
        self.nodes = {n["plan_key"]: n for n in graph.get("nodes") or []}
        self.order = {k: i for i, k in enumerate(self.nodes)}
        self.stories = nx.DiGraph()
        self.epics = nx.DiGraph()
        for k, n in self.nodes.items():
            (self.stories if n.get("kind") == "story" else self.epics).add_node(k)
        for b in graph.get("blocks") or []:
            a, c = b["blocker"], b["blocked"]
            if a in self.stories and c in self.stories:
                self.stories.add_edge(a, c)
            elif a in self.epics and c in self.epics:
                self.epics.add_edge(a, c)
        self.done = done
        self.dropped = dropped
        self.estimates = estimates

    def open(self, k: str) -> bool:
        return k not in self.done and k not in self.dropped

    def size(self, keys: Iterable[str]) -> float:
        return sum(self.estimates.get(k, 1) for k in keys)

    def ancestors(self, k: str) -> set[str]:
        return {a for a in nx.ancestors(self.stories, k) if self.open(a)} if k in self.stories else set()

    def descendants(self, k: str) -> set[str]:
        return {d for d in nx.descendants(self.stories, k) if self.open(d)} if k in self.stories else set()

    def stories_of(self, change: str) -> list[str]:
        return [k for k, n in self.nodes.items() if n.get("kind") == "story" and n["change"] == change]


def _story_reason(p: _Plan, key: str, fields: dict[str, Any], gate_tasks: list[dict[str, Any]] | None) -> str | None:
    n = p.nodes[key]
    status = fields.get("plan_status")
    if status == "frozen":
        return None
    draft = status in ("draft", "needs_rereview")
    if status == "needs_spec" or (draft and n.get("deliverable_kind") == "story" and not n.get("covers")):
        return "no_spec"
    if status == "needs_rereview":
        return "needs_rereview"
    if status == "draft":
        return "not_approved"
    if status == "held":
        return "held_for_approval"
    if gate_tasks:
        return "waits_on_person"
    if any(p.open(b) for b in p.stories.predecessors(key)):
        return "waits_on_work"
    return None


def not_ready(milestones: list[dict[str, Any]], graph: dict[str, Any], fields: dict[str, dict[str, Any]],
              done: set[str], dropped: set[str], critical: list[str], *, free: int, runway: int,
              estimates: dict[str, float] | None = None) -> list[dict[str, Any]]:
    """Every unit of planned work that cannot run yet, each with the first
    reason that applies and its slack, ranked by urgency. Items that need a
    person carry their rank across all three columns; items that wait on
    work only carry none."""
    p = _Plan(graph, done, dropped, estimates or {})
    crit = set(critical)
    by_id = {m["id"]: m for m in milestones}
    items: list[dict[str, Any]] = []
    seen: set[str] = set()

    def queue_slack(ahead: float) -> float:
        # Nothing ahead on its path: it blocks now if a slot is free, else
        # once the released stories ahead of it have run.
        return ahead if ahead else (0 if free else runway)

    for mi, m in enumerate(milestones):
        if m["done"]:
            continue
        before = _milestones_before(m, by_id)
        stories_before = [s for b in before if not by_id[b]["done"] for s in by_id[b]["stories"] if p.open(s)]
        open_person = [t for t in m["tasks"] if not t["done"] and t["kind"] in ms.PERSON_KINDS]

        for inc in m["includes"]:
            change = inc.get("change")
            if not change or change in seen or inc["tasks"] not in ms.STORIES_WITH:
                continue
            epic = p.nodes.get(change)
            if not epic or epic.get("approval") != "unplanned":
                continue
            agent = [t for t in m["tasks"] if t["change"] == change and t["kind"] == "agent"]
            if agent and all(t["done"] for t in agent):
                continue
            seen.add(change)
            ahead = {s for dep in nx.ancestors(p.epics, change) for s in p.stories_of(dep) if p.open(s)}
            later = {d for dep in nx.descendants(p.epics, change) for d in p.stories_of(dep) if p.open(d)}
            items.append(_item("change", change, change, change, m, "no_breakdown", queue_slack(p.size(ahead)),
                               False, len(later), p.order.get(change, 0), action={"kind": "plan", "epic": change}))

        for key in m["stories"]:
            if key in seen or not p.open(key) or key not in p.nodes:
                continue
            reason = _story_reason(p, key, fields.get(key, {}), open_person if key == m.get("gate") else None)
            if reason is None:
                continue
            seen.add(key)
            n = p.nodes[key]
            ahead = p.ancestors(key)
            blockers = [b for b in p.stories.predecessors(key) if p.open(b)]
            action: dict[str, Any]
            if reason == "not_approved":
                action = {"kind": "approve", "keys": [key]}
            elif reason == "held_for_approval":
                held_by = fields.get(key, {}).get("held_by") or blockers
                action = {"kind": "review", "story": held_by[0] if held_by else key}
            elif reason == "needs_rereview":
                action = {"kind": "review", "story": key}
            elif reason == "waits_on_person":
                action = {"kind": "task", "epic": open_person[0]["change"], "task": open_person[0]["id"]}
            else:
                action = {"kind": "plan", "story": key}
            item = _item("story", key, key.split("/", 1)[1], n.get("title") or key, m, reason, queue_slack(p.size(ahead)),
                         key in crit, len(p.descendants(key)), p.order.get(key, 0), action=action)
            item["change"] = n["change"]
            item["after"] = blockers
            item["has_spec"] = bool(n.get("covers")) or n.get("deliverable_kind") != "story"
            if reason == "waits_on_person":
                item["task"] = _task_ref(open_person[0])
            items.append(item)

        gate = m.get("gate")
        gate_ahead = p.ancestors(gate) | ({gate} - p.done - p.dropped) if gate else set()
        for t in m["tasks"]:
            if t["done"] or t["kind"] != "operator" or t["id"] in seen:
                continue
            seen.add(t["id"])
            ahead = set(stories_before) | (p.ancestors(gate) if gate else set())
            item = _item("task", t["id"], t["number"] or t["id"], t["text"], m, "operator_task", p.size(ahead),
                         bool(gate) and gate in crit, len(gate_ahead), 10_000_000 + mi * 10_000 + t["line"],
                         action={"kind": "task", "epic": t["change"], "task": t["id"]})
            item["change"] = t["change"]
            item["task"] = _task_ref(t)
            item["after_milestones"] = [_short(by_id[b]) for b in before if not by_id[b]["done"]]
            items.append(item)

    items = _group_reviews(items)
    items.sort(key=lambda i: (i["slack"], not i["critical"], -i["dependents"], i["order"]))
    rank = 0
    for i in items:
        if i["column"] is not None:
            rank += 1
            i["rank"] = rank
    return items


def _item(kind: str, key: str, label: str, title: str, m: dict[str, Any], reason: str, slack: float, critical: bool,
          dependents: int, order: int, *, action: dict[str, Any]) -> dict[str, Any]:
    return {
        "kind": kind, "key": key, "label": label, "title": title, "milestone": m["id"], "reason": reason,
        "column": COLUMN[reason], "slack": slack, "urgency": urgency(slack), "critical": critical,
        "dependents": dependents, "order": order, "rank": None, "action": action,
    }


def urgency(slack: float) -> str:
    """Blocking now, within a few stories, or with room to spare."""
    return "now" if slack <= 0 else ("soon" if slack <= SOON else "later")


def _task_ref(t: dict[str, Any]) -> dict[str, Any]:
    return {"id": t["id"], "change": t["change"], "number": t["number"], "text": t["text"], "kind": t["kind"], "line": t["line"]}


def _group_reviews(items: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """A change whose draft stories all have specs is reviewed as one item."""
    drafts: dict[str, list[dict[str, Any]]] = {}
    no_spec: set[str] = set()
    for i in items:
        if i["kind"] != "story":
            continue
        if i["reason"] == "not_approved":
            drafts.setdefault(i["change"], []).append(i)
        elif i["reason"] == "no_spec":
            no_spec.add(i["change"])
    out = [i for i in items if not (i["kind"] == "story" and i["reason"] == "not_approved"
                                    and i["change"] not in no_spec and len(drafts.get(i["change"], [])) > 1)]
    for change, group in drafts.items():
        if change in no_spec or len(group) < 2:
            continue
        first = min(group, key=lambda i: (i["slack"], i["order"]))
        out.append({
            **first, "kind": "change", "key": change, "label": change, "title": change,
            "slack": min(i["slack"] for i in group), "urgency": urgency(min(i["slack"] for i in group)),
            "critical": any(i["critical"] for i in group), "dependents": max(i["dependents"] for i in group),
            "order": min(i["order"] for i in group), "stories": [i["key"] for i in group], "after": [],
            "action": {"kind": "review", "epic": change, "keys": [i["key"] for i in group]},
        })
    return out


def _milestones_before(m: dict[str, Any], by_id: dict[str, dict[str, Any]]) -> list[str]:
    out: list[str] = []
    todo = list(m["depends_on"])
    while todo:
        mid = todo.pop()
        if mid in out or mid not in by_id:
            continue
        out.append(mid)
        todo += by_id[mid]["depends_on"]
    order = list(by_id)
    return sorted(out, key=order.index)


def _short(m: dict[str, Any]) -> str:
    """"M1" from "M1: vm-complete"; the title otherwise."""
    title = m["title"]
    return title.split(":", 1)[0].strip() if ":" in title else title


# The timeline (D7) ---------------------------------------------------------------


def timeline(milestones: list[dict[str, Any]], goal: str | None, items: list[dict[str, Any]],
             estimates: dict[str, float], done_keys: set[str]) -> dict[str, Any]:
    """Each milestone's span, proportional to the work it counts, the "we are
    here" share, and the blockers that need a person at each milestone."""
    use_estimates = bool(estimates) and all(s in estimates for m in milestones for s in m["stories"])

    def work(m: dict[str, Any]) -> tuple[float, float]:
        stories = m["stories"]
        story_total = sum(estimates[s] for s in stories) if use_estimates else len(stories)
        story_done = (sum(estimates[s] for s in stories if s in done_keys) if use_estimates
                      else m["progress"]["stories"]["done"])
        tasks = len(m["tasks"])
        return story_total + tasks, story_done + sum(1 for t in m["tasks"] if t["done"])

    sizes = [work(m) for m in milestones]
    total = sum(s for s, _ in sizes) or 1
    done = sum(d for _, d in sizes)
    spans = []
    at = 0.0
    goal_at = None
    goal_size = goal_done = 0.0
    for m, (size, d) in zip(milestones, sizes):
        start = at / total
        at += size
        spans.append({"id": m["id"], "start": start, "end": at / total, "size": size, "done": d,
                      "blockers": [{"key": i["key"], "label": i["label"], "reason": i["reason"], "rank": i["rank"]}
                                   for i in items if i["milestone"] == m["id"] and i["column"] is not None]})
        if goal_at is None:
            goal_size += size
            goal_done += d
            if m["id"] == goal:
                goal_at = at / total
    return {"spans": spans, "here": done / total, "to_goal": (goal_done / goal_size) if goal_size else 1.0,
            "axis": "estimates" if use_estimates else "stories_and_tasks"}


# The document --------------------------------------------------------------------


def build_home(*, graph: dict[str, Any] | None, nodes: list[dict[str, Any]], fields: dict[str, dict[str, Any]],
               planning: dict[str, Any], plan_states: dict[str, str] | None, tracker_known: bool, valid: bool,
               errors: int, health_inputs: dict[str, Any], history: History | None = None,
               estimates: dict[str, float] | None = None) -> dict[str, Any]:
    """The home document. ``graph`` is the last valid plan graph; ``nodes`` the
    merged graph's nodes, with tracker state and plan fields."""
    estimates = estimates or {}
    done, dropped = tracker_states(nodes, plan_states)
    key_of = {n["tracker"]["identifier"]: n["plan_key"] for n in nodes if n.get("tracker")}
    stories = [n for n in nodes if n.get("kind") == "story" and n.get("status") != "orphaned"]
    running_keys = {n["plan_key"] for n in stories if n.get("runtime")}
    runway = sum(1 for n in stories if fields.get(n["plan_key"], {}).get("plan_status") == "released"
                 and n["plan_key"] not in running_keys and n["plan_key"] not in done)
    remaining = sum(1 for n in stories if n["plan_key"] not in done and n["plan_key"] not in dropped)

    health = execution_health(runway=runway, remaining=remaining, key_of=key_of, **health_inputs)
    if history is not None:
        history.add(health["busy"], health["runway"])
        health["history"] = history.buckets()

    base = {
        "generated_at": time.time(),
        "plan": {"valid": valid, "errors": errors, "has_graph": graph is not None},
        "milestones_declared": bool(planning.get("declared")),
        "story_progress": "live" if health_inputs["reachable"] else ("last_known" if tracker_known else "unknown"),
        "health": health,
    }
    if graph is None:
        return {**base, "milestones": [], "goal": None, "next": None, "critical_path": None, "not_ready": [],
                "needs_person": _needs([]), "timeline": None}

    out = ms.progress(planning.get("milestones") or [], graph, planning.get("tasks") or {}, done, dropped, estimates)
    # A milestone declared done was finished by hand: its stories are not
    # work still ahead of anything, whatever the tracker says.
    settled = done | {s for m in out["milestones"] if m.get("declared_done") for s in m["stories"]}
    items = not_ready(out["milestones"], graph, fields, settled, dropped, out["critical_path"],
                      free=health["free"] or 0, runway=runway, estimates=estimates)
    line = timeline(out["milestones"], out["goal"], items, estimates, done)
    by_key = {n["plan_key"]: n for n in nodes}
    reason_of = {i["key"]: i["reason"] for i in items if i["kind"] == "story"}
    nxt = next((m for m in out["milestones"] if m["id"] == out["next"]), None)
    path = None
    if nxt is not None:
        path = {
            "milestone": nxt["id"], "gate": nxt.get("gate"),
            "stories": [{"key": k, "label": k.split("/", 1)[1], "title": (by_key.get(k) or {}).get("title") or k,
                         "change": k.split("/", 1)[0], "needs": reason_of.get(k),
                         "running": k in running_keys, "estimate": estimates.get(k)}
                        for k in out["critical_path"]],
        }
    milestones = []
    for m in out["milestones"]:
        milestones.append({
            "id": m["id"], "title": m["title"], "short": _short(m), "goal": m["id"] == out["goal"],
            "next": m["id"] == out["next"], "done": m["done"], "implicit": m["implicit"],
            "depends_on": m["depends_on"], "gate": m.get("gate"), "changes": m["changes"],
            "progress": m["progress"],
        })
    return {
        **base,
        "milestones": milestones, "goal": out["goal"], "next": out["next"], "critical_path": path,
        "not_ready": items, "needs_person": _needs(items), "timeline": line,
    }


def _needs(items: list[dict[str, Any]]) -> dict[str, Any]:
    person = [i for i in items if i["column"] is not None]
    approve = [i for i in person if i["column"] == "approve"]
    return {
        "total": len(person),
        "approve": len(approve),
        "approve_stories": sum(len(i.get("stories") or [i["key"]]) for i in approve),
        "spec": sum(1 for i in person if i["column"] == "spec"),
        "operator": sum(1 for i in person if i["column"] == "operator"),
        "nearest": person[0]["key"] if person else None,
    }
