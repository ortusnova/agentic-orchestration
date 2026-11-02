"""What each story released to Sortie is doing (agent-platform/workbench-home).

A released story stays in the list until it is done: a retry, a hold or a
question is its state, never a reason to leave the list. The rows keep the
order the stories were released in. Each row says, in fields the home puts
into words, whether the story waits for a slot, is starting, works, is in
review, needs a person, or is held.
"""

from __future__ import annotations

import re
from datetime import datetime
from typing import Any, Iterable

from .delivery import pull_url

STATES = ("waiting", "starting", "working", "in_review", "needs_you", "held")


class ReleaseOrder:
    """The order the workbench saw the stories released in. A story seen
    released for the first time goes after every one seen before; stories
    first seen together keep their tracker order. A story that leaves the
    list (done, or pulled back) forgets its place."""

    def __init__(self) -> None:
        self._seq: dict[str, int] = {}
        self._next = 0

    def update(self, keys: Iterable[str]) -> dict[str, int]:
        keys = list(keys)
        current = set(keys)
        self._seq = {k: v for k, v in self._seq.items() if k in current}
        for k in keys:
            if k not in self._seq:
                self._seq[k] = self._next
                self._next += 1
        return dict(self._seq)


def _seconds(v: Any) -> float | None:
    """A time Sortie reports, in Unix seconds, or None."""
    if isinstance(v, bool) or v is None:
        return None
    if isinstance(v, (int, float)):
        return float(v) / 1000 if v > 1e10 else float(v)
    if isinstance(v, str) and v.strip():
        try:
            return datetime.fromisoformat(v.strip().replace("Z", "+00:00")).timestamp()
        except ValueError:
            return None
    return None


def _number(identifier: str) -> int:
    m = re.search(r"(\d+)$", identifier or "")
    return int(m.group(1)) if m else 0


def _words(code: str | None) -> str:
    return (code or "").replace("_", " ").strip()


def _pull(ref: str | None) -> dict[str, Any] | None:
    if not ref:
        return None
    return {"ref": ref, "url": pull_url(ref) or None}


def is_released(node: dict[str, Any], plan_states: dict[str, str] | None, plan_status: str | None) -> bool:
    """Whether Sortie holds the story: its issue left the backlog, or it runs."""
    t = node.get("tracker")
    if not t:
        return False
    if node.get("runtime"):
        return True
    state = (t.get("state") or "").lower()
    backlog = ((plan_states or {}).get("backlog") or "").lower()
    if backlog:
        return state != backlog
    return plan_status in ("released", "frozen")


def released_keys(nodes: Iterable[dict[str, Any]], plan_states: dict[str, str] | None,
                  fields: dict[str, dict[str, Any]], settled: set[str]) -> list[str]:
    """The plan keys of the stories released to Sortie and not yet done, in
    tracker order. ``settled`` holds the stories done or cancelled."""
    out = []
    for n in nodes:
        if n.get("kind") != "story" or n["plan_key"] in settled:
            continue
        t = n.get("tracker") or {}
        if t.get("terminal"):
            continue
        if is_released(n, plan_states, (fields.get(n["plan_key"]) or {}).get("plan_status")):
            out.append(n)
    out.sort(key=lambda n: (_number(n["tracker"]["identifier"]), n["tracker"]["identifier"], n["plan_key"]))
    return [n["plan_key"] for n in out]


def _by_ident(entries: Any) -> dict[str, dict[str, Any]]:
    return {e["issue_identifier"]: e for e in entries or [] if isinstance(e, dict) and e.get("issue_identifier")}


def doing(node: dict[str, Any], state: dict[str, Any] | None, question: dict[str, Any] | None,
          plan_states: dict[str, str] | None) -> dict[str, Any]:
    """What one released story is doing, from Sortie's state where it has
    one and the plan graph's runtime otherwise."""
    t = node.get("tracker") or {}
    ident = t.get("identifier") or ""
    rt = node.get("runtime") or {}
    running = _by_ident((state or {}).get("running")).get(ident)
    retrying = _by_ident((state or {}).get("retrying")).get(ident)
    budget = _by_ident((state or {}).get("budget_exhausted")).get(ident)
    parked = _by_ident((state or {}).get("parked")).get(ident)
    out: dict[str, Any] = {"state": "waiting", "attempt": None, "error": None, "turn": None, "started_at": None,
                           "pr": None, "question": None, "reason": None}

    if question is not None or parked is not None:
        statement = (question or {}).get("statement") or (parked or {}).get("statement") or ""
        return {**out, "state": "needs_you", "question": statement.strip() or None}
    if running and running.get("held"):
        return {**out, "state": "held", "reason": _words(running.get("hold_reason")) or "held"}
    if budget is not None or rt.get("status") == "budget_held":
        return {**out, "state": "held", "reason": _words((budget or {}).get("reason")) or "budget exhausted"}
    if retrying is not None or (rt.get("status") == "retrying" and not running):
        r = retrying or {}
        error = r.get("error") or r.get("last_error") or rt.get("error") or rt.get("last_error")
        return {**out, "state": "starting", "attempt": r.get("attempt") or rt.get("attempt"), "error": error or None}
    if running is not None or rt.get("status") == "running":
        r = running or {}
        turn = r.get("turn_count") if r.get("turn_count") is not None else rt.get("turn_count")
        started = _seconds(r.get("started_at")) or _seconds(rt.get("started_at"))
        if not turn:
            # The session is not up yet: its first turn has not begun.
            return {**out, "state": "starting", "attempt": r.get("attempt") or rt.get("attempt") or 1,
                    "error": r.get("last_error") or None, "started_at": started}
        return {**out, "state": "working", "turn": turn, "started_at": started}

    pr = _pull(t.get("pr"))
    state_name = (t.get("state") or "").lower()
    release = ((plan_states or {}).get("release") or "").lower()
    if "review" in state_name or (pr and state_name != release):
        return {**out, "state": "in_review", "pr": pr}
    return out


def released_list(nodes: list[dict[str, Any]], plan_states: dict[str, str] | None,
                  fields: dict[str, dict[str, Any]], settled: set[str], state: dict[str, Any] | None,
                  questions: dict[str, dict[str, Any]] | None = None,
                  order: dict[str, int] | None = None) -> list[dict[str, Any]]:
    """Every story released to Sortie and not yet done, in release order,
    each with what it is doing. ``questions`` holds the open questions by
    tracker identifier; ``order`` each story's place in the release order."""
    by_key = {n["plan_key"]: n for n in nodes}
    keys = released_keys(nodes, plan_states, fields, settled)
    tracker_rank = {k: i for i, k in enumerate(keys)}
    if order:
        keys.sort(key=lambda k: (order.get(k, len(order) + tracker_rank[k]), tracker_rank[k]))
    out = []
    for k in keys:
        n = by_key[k]
        t = n["tracker"]
        out.append({
            "key": k, "label": k.split("/", 1)[-1], "change": n.get("change") or k.split("/", 1)[0],
            "title": n.get("title") or k, "identifier": t.get("identifier"), "url": t.get("url"),
            **doing(n, state, (questions or {}).get(t.get("identifier") or ""), plan_states),
        })
    return out
