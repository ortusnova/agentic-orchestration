"""Platform faults and their person actions (agent-platform/run-supervision).

Sortie routes a stopped failure by its class. A platform fault, such as a
token without a scope, is recorded once with its cause and every story it
holds; Sortie checks it again on a schedule and releases the stories when
the check passes. A fault only a person can clear names the exact action
and where to take it. Each Sortie's state lists its open faults in
``platform_faults``; the workbench asks each one with a person action as a
question under "Needs a person", and its Done calls Sortie's
``POST /api/v1/platform-faults/{id}/action-done``, which checks it again at
once.

A story's card shows the full cause of its failure: the fault that holds
it, or else its newest finished run when that run failed.
"""

from __future__ import annotations

from datetime import datetime
from typing import Any, Iterable


def _ts(v: Any) -> float | None:
    """An RFC 3339 time in Unix seconds, or None."""
    if not isinstance(v, str) or not v.strip():
        return None
    try:
        t = datetime.fromisoformat(v.strip().replace("Z", "+00:00")).timestamp()
    except ValueError:
        return None
    return t if t > 0 else None


def _text(v: Any) -> str | None:
    return v.strip() or None if isinstance(v, str) else None


def fault_of(name: str, f: dict[str, Any]) -> dict[str, Any] | None:
    """One fault as Sortie ``name`` serves it, read; None for an entry with no id."""
    if not isinstance(f, dict) or not _text(f.get("id")):
        return None
    action = f.get("person_action") if isinstance(f.get("person_action"), dict) else None
    stories = []
    for s in f.get("stories") or []:
        if isinstance(s, dict) and _text(s.get("issue_identifier")):
            stories.append({"identifier": s["issue_identifier"].strip(),
                            "display": _text(s.get("display_identifier")), "held_at": _ts(s.get("held_at"))})
    return {
        "id": f["id"].strip(), "sortie": name,
        "class": _text(f.get("class")), "step": _text(f.get("step")),
        "cause": _text(f.get("cause")) or "", "detail": _text(f.get("detail")),
        "action": _text((action or {}).get("action")), "where": _text((action or {}).get("where")),
        "done_at": _ts((action or {}).get("done_at")), "done_by": _text((action or {}).get("done_by")),
        "stories": stories,
        "recorded_at": _ts(f.get("recorded_at")), "last_check_at": _ts(f.get("last_check_at")),
        "last_check_error": _text(f.get("last_check_error")), "next_check_at": _ts(f.get("next_check_at")),
        "checking": bool(f.get("checking")),
    }


def faults_of(states: Iterable[tuple[str, dict[str, Any] | None, str | None]]) -> list[dict[str, Any]]:
    """Every open fault of every Sortie, oldest first. An older Sortie
    serves no ``platform_faults`` and gives none."""
    out = []
    for name, state, _ in states:
        for f in (state or {}).get("platform_faults") or []:
            if (doc := fault_of(name, f)) is not None:
                out.append(doc)
    out.sort(key=lambda f: (f["recorded_at"] is None, f["recorded_at"] or 0.0, f["id"]))
    return out


def project_faults(faults: list[dict[str, Any]], nodes: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The faults that hold at least one of the project's stories, each
    story named by its plan key where it is the project's. A fault holds
    every story that hit it, of any project; it is listed whole."""
    by_ident = {n["tracker"]["identifier"]: n for n in nodes if n.get("kind") == "story" and n.get("tracker")}
    out = []
    for f in faults:
        stories = []
        for s in f["stories"]:
            n = by_ident.get(s["identifier"])
            key = n["plan_key"] if n else None
            stories.append({**s, "key": key, "label": key.split("/", 1)[-1] if key else None,
                            "change": (n.get("change") or key.split("/", 1)[0]) if n and key else None,
                            "title": (n.get("title") or key) if n else None})
        if any(s["key"] for s in stories):
            out.append({**f, "stories": stories, "keys": [s["key"] for s in stories if s["key"]]})
    return out


def home_faults(faults: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The project's faults that only a person can clear: each one is a
    question on the home."""
    return [{**f, "key": f["keys"][0]} for f in faults if f["action"]]


def fault_text(f: dict[str, Any]) -> str:
    """"A platform fault holds b1, b2 and b3: <cause>. <action> (<where>)"."""
    keys = f.get("keys") or [s["identifier"] for s in f["stories"]]
    held = keys[0] if len(keys) == 1 else ", ".join(keys[:-1]) + " and " + keys[-1]
    text = f"A platform fault holds {held}: {f['cause'] or 'no cause given'}"
    if f["action"]:
        text += f". {f['action']}" + (f" ({f['where']})" if f["where"] else "")
    return text


def failures(faults: list[dict[str, Any]], runs: dict[str, list[dict[str, Any]]]) -> dict[str, dict[str, Any]]:
    """The failure of each story, by tracker identifier, with its full
    cause: the fault that holds it, or else its newest finished run among
    ``runs`` (each Sortie's recent runs, by name) when that run failed."""
    out: dict[str, dict[str, Any]] = {}
    newest: dict[str, tuple[float, str, dict[str, Any]]] = {}
    for name, rs in runs.items():
        for r in rs or []:
            ident = r.get("issue_identifier") if isinstance(r, dict) else None
            if not ident:
                continue
            at = _ts(r.get("completed_at")) or _ts(r.get("started_at")) or 0.0
            if ident not in newest or at > newest[ident][0]:
                newest[ident] = (at, name, r)
    for ident, (_, name, r) in newest.items():
        cause = _text(r.get("error"))
        if r.get("status") == "succeeded" or not cause:
            continue
        out[ident] = {"source": "run", "sortie": name, "cause": cause, "detail": _text(r.get("error_detail")),
                      "step": None, "class": None, "status": _text(r.get("status")),
                      "at": _ts(r.get("completed_at")), "fault_id": None, "action": None, "where": None,
                      "done_at": None, "last_check_error": None, "next_check_at": None}
    for f in faults:
        for s in f["stories"]:
            out[s["identifier"]] = {"source": "platform_fault", "sortie": f["sortie"], "cause": f["cause"],
                                    "detail": f["detail"], "step": f["step"], "class": f["class"], "status": None,
                                    "at": s["held_at"] or f["recorded_at"], "fault_id": f["id"], "action": f["action"],
                                    "where": f["where"], "done_at": f["done_at"],
                                    "last_check_error": f["last_check_error"], "next_check_at": f["next_check_at"]}
    return out
