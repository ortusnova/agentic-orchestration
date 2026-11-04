"""What Sortie does with one story, for its panel (agent-platform/run-supervision:
every stuck or waiting story names what releases it and the action that does).

Read from the per-issue response of the Sortie that holds the story, and from
that Sortie's state, whose ``held`` list the per-issue response does not read.
"""

from __future__ import annotations

from typing import Any

from .lanes import _pulls, parse_time
from .questions import statement_of

# How many of the story's newest runs the panel lists.
RUNS_SHOWN = 5


def _held_entry(state: dict[str, Any] | None, identifier: str) -> dict[str, Any] | None:
    return next((r for r in (state or {}).get("held") or [] if r.get("issue_identifier") == identifier), None)


def _runs(detail: dict[str, Any]) -> list[dict[str, Any]]:
    out = []
    for r in ((detail.get("history") or {}).get("runs") or [])[:RUNS_SHOWN]:
        if not r.get("dispatch_id"):
            continue
        out.append({"dispatch_id": r["dispatch_id"], "status": r.get("status"), "rule": r.get("rule_name") or None,
                    "started_at": parse_time(r.get("started_at")), "completed_at": parse_time(r.get("completed_at")),
                    "error": (r.get("error") or None)})
    return out


def run_state(name: str, identifier: str, detail: dict[str, Any] | None, state: dict[str, Any] | None,
              *, can_grant: bool | None, now: float) -> dict[str, Any]:
    """The story's run state: its status, why, what releases it, its pull
    request and what that waits on, and the action that unblocks it."""
    detail = detail or {}
    config = (state or {}).get("config") or {}
    max_sessions = (config.get("agent") or {}).get("max_sessions")
    held = _held_entry(state, identifier)
    status = detail.get("status")
    if held and status in (None, "history"):
        status = "held"
    out: dict[str, Any] = {
        "sortie": name, "identifier": identifier, "status": None, "headline": detail.get("status_text") or None,
        "text": detail.get("status_text_full") or None, "why": [], "releases": None, "action": None,
        "question": None, "budget": None, "blockers": [], "retry": None, "running": None,
        "pulls": [], "next": [{"condition": o.get("condition"), "text": o.get("text")} for o in detail.get("next_outcomes") or []],
        "runs": _runs(detail), "can_grant": can_grant, "max_sessions": max_sessions if isinstance(max_sessions, int) else None,
    }
    pulls = _pulls({"pending_reactions": detail.get("pending_reactions") or []}, {}, now)
    out["pulls"] = [{**p["pull"], "waits": p["text"], "ready": p["kind"] == "ready"} for p in pulls]

    if status == "running":
        r = detail.get("running") or {}
        out["running"] = {"session_id": r.get("session_id") or None, "dispatch_id": r.get("dispatch_id") or None,
                          "rule": r.get("rule_name") or None, "started_at": parse_time(r.get("started_at")),
                          "watchable": bool((r.get("broker") or {}).get("http_url")), "held": bool(r.get("held")),
                          "holder": r.get("holder") or None}
        out["status"] = "running"
        out["releases"] = "It runs now."
        out["action"] = "watch" if out["running"]["watchable"] else None
        if r.get("held"):
            out["why"].append(f"A person holds its session{(' (' + r['holder'] + ')') if r.get('holder') else ''}: its clocks stop until they hand it back.")
    elif status == "retrying":
        r = detail.get("retry") or {}
        due = parse_time(r.get("due_at"))
        out["status"] = "retrying"
        out["retry"] = {"attempt": r.get("attempt"), "due_at": due, "error": r.get("error") or detail.get("last_error") or None}
        if out["retry"]["error"]:
            out["why"].append(out["retry"]["error"])
        out["releases"] = "Sortie runs it again on its own" + (" now." if due is None or due <= now else ", when the retry is due.")
        out["action"] = "last_run" if out["runs"] else None
    elif status == "budget_exhausted":
        b = detail.get("budget_exhausted") or {}
        out["status"] = "budget"
        out["budget"] = {"reason": b.get("reason") or None, "used": b.get("used_sessions"), "sessions": b.get("budget_sessions"),
                         "since": parse_time(b.get("exhausted_at"))}
        if b.get("reason") == "session_budget":
            out["why"].append(f"It used {b.get('used_sessions')} of its {b.get('budget_sessions')} sessions, so Sortie starts no more.")
            out["releases"] = "More sessions release it: Sortie runs it again at its next poll."
            out["action"] = "grant"
        else:
            out["why"].append(f"It reached its {(b.get('reason') or 'budget').replace('_', ' ')}.")
            out["releases"] = f"A higher ceiling in {name}'s workflow releases it."
            out["action"] = "last_run" if out["runs"] else None
    elif status == "parked":
        p = detail.get("parked") or {}
        out["status"] = "parked"
        out["question"] = {"identifier": identifier, "statement": statement_of(p) or statement_of(detail),
                           "parked_at": parse_time(p.get("parked_at")), "reason": (p.get("reason") or "").replace("_", " ") or None}
        out["why"].append("The agent stopped: it cannot go on without a person.")
        out["releases"] = "Your answer releases it: Sortie posts it on the issue and runs the story again with it."
        out["action"] = "answer"
    elif status == "pending_reactions":
        out["status"] = "reaction"
        if out["pulls"]:
            out["why"] += [f"Its pull request {p['waits']}." if p.get("number") else p["waits"] for p in out["pulls"]]
        out["releases"] = "The pull request merging releases it; Sortie then moves it to done."
        out["action"] = "open_pr" if any(p.get("url") for p in out["pulls"]) else None
    elif status == "between_stages":
        out["status"] = "between"
        out["releases"] = "Its next stage starts at the next free slot."
        out["action"] = "last_run" if out["runs"] else None
    elif status == "held":
        hold = (held or {}).get("hold") or {}
        out["status"] = "blocked"
        out["headline"] = out["headline"] or held.get("status_text") or hold.get("reason_text")
        out["text"] = out["text"] or held.get("status_text_full")
        out["blockers"] = [{"identifier": b.get("issue_identifier"), "state": b.get("state"), "reason": (b.get("reason") or "").replace("_", " ") or None}
                           for b in hold.get("blockers") or [] if b.get("issue_identifier")]
        if hold.get("reason_text"):
            out["why"].append(hold["reason_text"])
        out["releases"] = hold.get("release_text") or "Its blockers finishing releases it."
        out["action"] = "blockers" if out["blockers"] else None
    else:
        out["status"] = "idle"
        outcome = (detail.get("history") or {}).get("issue_outcome") or {}
        if outcome.get("status_text_full"):
            out["why"].append(outcome["status_text_full"])
        out["action"] = "last_run" if out["runs"] else None
    return out
