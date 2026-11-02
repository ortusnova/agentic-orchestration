"""Each worker's agent slots in time, for the project home
(agent-platform/run-supervision: the workers shown as slot lanes).

A worker is one Sortie and the host it runs agents on. Each of its agent
slots is a lane: the job that runs in it, or the work that stopped there and
waits, or, when it is free, what runs next in it or why nothing does. What
waits on the worker after its lanes, its blocked stories and its pull
requests, is its ``later`` list. Everything here is read from Sortie's
``/api/v1/state`` and its run history; nothing here writes anywhere.
"""

from __future__ import annotations

import statistics
from datetime import datetime
from typing import Any

# A median needs this many finished runs before it is an estimate.
MIN_SAMPLES = 3
# A pull request ready to merge for this long makes its worker stuck.
READY_STUCK_SECONDS = 3600.0
# The longest question text a lane carries; the Needs a person section has it whole.
QUESTION_CHARS = 240


def parse_time(value: Any) -> float | None:
    if not isinstance(value, str) or not value:
        return None
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return None


# Estimates -----------------------------------------------------------------------


def estimates(runs: list[dict[str, Any]]) -> dict[str, Any]:
    """The median duration of the succeeded runs, by rule and over all rules,
    where enough runs finished to make one."""
    by_rule: dict[str, list[float]] = {}
    every: list[float] = []
    for r in runs:
        if r.get("status") != "succeeded" or not r.get("rule_name"):
            continue
        start, end = parse_time(r.get("started_at")), parse_time(r.get("completed_at"))
        if start is None or end is None or end < start:
            continue
        every.append(end - start)
        by_rule.setdefault(r["rule_name"], []).append(end - start)
    return {
        "by_rule": {k: {"seconds": statistics.median(v), "runs": len(v)} for k, v in by_rule.items() if len(v) >= MIN_SAMPLES},
        "all": {"seconds": statistics.median(every), "runs": len(every)} if len(every) >= MIN_SAMPLES else None,
    }


def estimate_for(est: dict[str, Any] | None, rule: str | None) -> dict[str, Any] | None:
    """How long a run of the rule takes, and what that rests on; None when
    the history cannot say."""
    if not est:
        return None
    own = (est.get("by_rule") or {}).get(rule or "")
    if own:
        return {"seconds": own["seconds"], "basis": f"median of {own['runs']} past {rule} runs"}
    every = est.get("all")
    if every:
        return {"seconds": every["seconds"], "basis": f"median of {every['runs']} past runs"}
    return None


# One worker ----------------------------------------------------------------------


def _phase(rule: str | None, reaction: str | None = None) -> str:
    if reaction:
        return {"ci": "Fix CI", "merge-conflict": "Fix a conflict"}.get(reaction, reaction.replace("-", " ").capitalize())
    return (rule or "run").replace("_", " ").replace("-", " ").capitalize()


def _words(code: str | None) -> str:
    return (code or "").replace("_", " ").strip()


def _first(text: str | None, limit: int = QUESTION_CHARS) -> str:
    line = " ".join((text or "").split())
    return line if len(line) <= limit else line[: limit - 1].rstrip() + "…"


def _issue(r: dict[str, Any], key_of: dict[str, str]) -> dict[str, Any]:
    ident = r.get("issue_identifier") or ""
    return {"identifier": ident, "key": key_of.get(ident), "title": r.get("issue_title") or ident}


def _job(r: dict[str, Any], key_of: dict[str, str], est: dict[str, Any] | None, now: float) -> dict[str, Any]:
    rule = r.get("rule_name") or None
    reaction = r.get("reaction_kind") or None
    tokens = (r.get("tokens") or {}).get("total_tokens")
    job = {
        **_issue(r, key_of),
        "phase": _phase(rule, reaction), "rule": rule, "started_at": parse_time(r.get("started_at")),
        "estimate": estimate_for(est, rule),
        "last_event": r.get("last_message") or r.get("last_event") or None,
        "last_event_at": parse_time(r.get("last_event_at")),
        "tokens": tokens if isinstance(tokens, int) else None, "turns": r.get("turn_count"),
        "session_id": r.get("session_id") or None, "watchable": bool((r.get("broker") or {}).get("http_url")),
        "repository": r.get("repository") or None, "status": r.get("status_text") or None,
    }
    stuck = None
    if r.get("held"):
        why = _words(r.get("hold_reason")) or "held"
        stuck = {"code": "held", "text": f"held: {why}" + (f", by {r['holder']}" if r.get("holder") else "")}
    else:
        stall_at = parse_time(r.get("stall_at"))
        if stall_at is not None and stall_at <= now:
            stuck = {"code": "stalled", "text": r.get("stall_text") or "no agent event for too long"}
    return {"job": job, "stuck": stuck}


def _retry(r: dict[str, Any], key_of: dict[str, str], est: dict[str, Any] | None) -> dict[str, Any]:
    rule = r.get("rule_name") or None
    attempt = r.get("attempt")
    error = _first(r.get("error"), 160)
    text = f"retrying, attempt {attempt}" if attempt else "retrying"
    return {"job": {**_issue(r, key_of), "phase": _phase(rule, r.get("reaction_kind")), "rule": rule,
                    "due_at": parse_time(r.get("due_at")), "attempt": attempt, "estimate": estimate_for(est, rule),
                    "status": r.get("status_text") or None},
            "stuck": {"code": "retrying", "text": f"{text}: {error}" if error else text}}


def _budget(r: dict[str, Any], key_of: dict[str, str]) -> dict[str, Any]:
    reason = r.get("reason") or "budget"
    if reason == "session_budget" and r.get("budget_sessions"):
        text = f"used its {r['budget_sessions']} sessions; the session budget holds it"
    else:
        text = f"held: {_words(reason)} reached"
    return {"job": {**_issue(r, key_of), "phase": _phase(r.get("rule_name")), "rule": r.get("rule_name") or None,
                    "since": parse_time(r.get("exhausted_at")), "status": r.get("status_text_full") or r.get("status_text") or None},
            "stuck": {"code": "budget", "text": text}}


def _parked(r: dict[str, Any], key_of: dict[str, str], statements: dict[str, str]) -> dict[str, Any]:
    ident = r.get("issue_identifier") or ""
    statement = statements.get(ident) or r.get("statement") or ""
    text = f"asks: {_first(statement)}" if statement else "stopped for a person without saying why"
    return {"job": {**_issue(r, key_of), "phase": _phase(r.get("rule_name")), "rule": r.get("rule_name") or None,
                    "since": parse_time(r.get("parked_at")), "status": r.get("status_text") or None},
            "stuck": {"code": "question", "text": text}}


def _pulls(state: dict[str, Any], key_of: dict[str, str], now: float) -> list[dict[str, Any]]:
    """Each pull request Sortie watches, with what it waits on."""
    by_pr: dict[tuple[str, Any], list[dict[str, Any]]] = {}
    for r in state.get("pending_reactions") or []:
        pr = r.get("pull_request") or {}
        by_pr.setdefault((r.get("issue_identifier") or "", pr.get("number")), []).append(r)
    out = []
    for (ident, number), rs in by_pr.items():
        kinds = {r.get("kind"): r for r in rs}
        pr = next((r.get("pull_request") for r in rs if r.get("pull_request")), {}) or {}
        conflict = kinds.get("merge-conflict")
        ci = kinds.get("ci")
        ci_status = ((ci or {}).get("last_check") or {}).get("status")
        if conflict and (conflict.get("last_check") or {}).get("mergeable") is False:
            waits, kind = "a conflict", "waiting"
        elif ci and ci_status == "failing":
            waits, kind = "CI, which fails", "waiting"
        elif ci and ci_status != "passing":
            waits, kind = "CI", "waiting"
        elif "merge" in kinds:
            waits, kind = "its auto-merge", "waiting"
        else:
            waits, kind = "nothing: ready to merge", "ready"
        since = min((t for t in (parse_time(r.get("created_at")) for r in rs) if t is not None), default=None)
        out.append({"kind": kind, **_issue(rs[0], key_of),
                    "pull": {"number": number, "url": pr.get("url"), "repository": pr.get("repository")},
                    "text": f"#{number} waits on {waits}" if number else f"waits on {waits}",
                    "since": since,
                    "stuck": kind == "ready" and since is not None and now - since >= READY_STUCK_SECONDS})
    return out


def _held(state: dict[str, Any], key_of: dict[str, str]) -> list[dict[str, Any]]:
    out = []
    for r in state.get("held") or []:
        hold = r.get("hold") or {}
        blockers = [b.get("issue_identifier") for b in hold.get("blockers") or [] if b.get("issue_identifier")]
        text = f"after {', '.join(blockers)}" if blockers else (hold.get("reason_text") or r.get("status_text") or "held")
        out.append({"kind": "blocked", **_issue(r, key_of), "text": text, "status": r.get("status_text_full") or None})
    return out


def _paused(state: dict[str, Any]) -> str | None:
    paused = state.get("dispatch_paused")
    if not paused:
        return None
    if isinstance(paused, dict):
        return paused.get("reason_text") or paused.get("status_text") or "Dispatch is paused"
    return "Dispatch is paused"


def worker_lanes(name: str, *, main: bool, state: dict[str, Any] | None, error: str | None,
                 runs: dict[str, Any] | None, key_of: dict[str, str], now: float,
                 statements: dict[str, str] | None = None) -> dict[str, Any]:
    """One worker: its slots as lanes, what waits on it, and whether its
    work moves or is stuck, and why."""
    out: dict[str, Any] = {"name": name, "main": main, "reachable": state is not None, "error": error,
                           "slots": None, "busy": 0, "started_at": None, "poll_seconds": None, "paused": None,
                           "verdict": "unreachable", "reasons": [], "lanes": [], "later": []}
    if state is None:
        out["reasons"] = [{"code": "unreachable", "text": f"{name}'s Sortie is unreachable" + (f": {error}" if error else "")}]
        return out
    config = state.get("config") or {}
    cap = (config.get("agent") or {}).get("max_concurrent_agents")
    slots = cap if isinstance(cap, int) and cap > 0 else None
    poll = (config.get("polling") or {}).get("interval_ms")
    out.update(slots=slots, started_at=parse_time(state.get("started_at")),
               poll_seconds=poll / 1000 if isinstance(poll, (int, float)) else None, paused=_paused(state))

    running = sorted(state.get("running") or [], key=lambda r: parse_time(r.get("started_at")) or now)
    lanes = [{"kind": "job", **_job(r, key_of, runs, now)} for r in running]
    out["busy"] = len(lanes)
    # Work that stopped in a slot and waits: it reads as stuck in a free
    # lane, the one it will run in again; what does not fit waits below.
    waiting = ([{"kind": "retry", **_retry(r, key_of, runs)} for r in state.get("retrying") or []]
               + [{"kind": "stuck", **_budget(r, key_of)} for r in state.get("budget_exhausted") or []]
               + [{"kind": "stuck", **_parked(r, key_of, statements or {})} for r in state.get("parked") or []])
    room = max(0, (slots if slots is not None else len(lanes)) - len(lanes))
    lanes += waiting[:room]
    later: list[dict[str, Any]] = []
    for w in waiting[room:]:
        j = w["job"]
        later.append({"kind": "you" if w["stuck"]["code"] == "question" else "held",
                      "identifier": j["identifier"], "key": j["key"], "title": j["title"], "text": w["stuck"]["text"],
                      "stuck": True})

    # A free slot says what runs in it next, or why nothing does.
    nexts = [{**_issue(h, key_of), "phase": _phase(h.get("target_rule")), "rule": h.get("target_rule") or None,
              "estimate": estimate_for(runs, h.get("target_rule"))} for h in state.get("between_stages") or []]
    held = _held(state, key_of)
    free = max(0, (slots or 0) - len(lanes))
    for i in range(free):
        nxt = nexts[i:i + 1] if i < len(nexts) else []
        if out["paused"]:
            reason = out["paused"] if i == 0 else ""
        elif nxt:
            reason = f"next: {nxt[0]['identifier']} moves to {nxt[0]['phase'].lower()}"
        elif i == len(nexts):
            reason = (f"Nothing ready for {name}: {len(held)} {'story waits' if len(held) == 1 else 'stories wait'} on other work"
                      if held else f"Nothing approved is ready for {name}.")
        else:
            reason = ""
        lanes.append({"kind": "free", "job": None, "stuck": None, "reason": reason, "next": nxt})
    for k in range(free, len(nexts)):
        n = nexts[k]
        later.append({"kind": "ready", "identifier": n["identifier"], "key": n["key"], "title": n["title"],
                      "text": f"moves to {n['phase'].lower()} at the next free slot", "stuck": False})
    for i, lane in enumerate(lanes):
        lane["slot"] = i + 1
        lane.setdefault("next", [])
        lane.setdefault("reason", None)

    pulls = _pulls(state, key_of, now)
    out["lanes"] = lanes
    out["later"] = later + [p for p in pulls if p["kind"] == "ready"] + held + [p for p in pulls if p["kind"] != "ready"]

    reasons = [{"code": lane["stuck"]["code"], "text": f"{lane['job']['identifier']} {lane['stuck']['text']}",
                "identifier": lane["job"]["identifier"], "key": lane["job"]["key"]}
               for lane in lanes if lane.get("stuck")]
    reasons += [{"code": "held" if i["kind"] == "held" else "question", "text": f"{i['identifier']} {i['text']}",
                 "identifier": i["identifier"], "key": i["key"]} for i in later if i.get("stuck")]
    reasons += [{"code": "ready", "text": f"{p['identifier']} {p['text']}, for over an hour",
                 "identifier": p["identifier"], "key": p["key"]} for p in pulls if p["stuck"]]
    out["reasons"] = reasons
    out["verdict"] = "stuck" if reasons else "paused" if out["paused"] else "working" if out["busy"] else "idle"
    return out
