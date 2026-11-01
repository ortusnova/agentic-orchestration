"""The questions agents ask people (agent-platform/agent-questions).

An agent that cannot go on without a person stops and states why, and
Sortie parks its story. Sortie's state lists the parks, and its per-issue
response carries each park's stop statement in full. The workbench shows
them and takes the answer, which it sends through Sortie's answer API: it
never writes to the tracker itself.
"""

from __future__ import annotations

from datetime import datetime
from typing import Any

# Where Sortie may put a park's details, and what it may call them.
_PARK_KEYS = ("park", "parked")
_STATEMENT_KEYS = ("statement", "stop_statement")
_TIME_KEYS = ("parked_at", "since", "at", "created_at")


def _sources(entry: dict[str, Any]) -> list[dict[str, Any]]:
    out = [entry]
    out += [entry[k] for k in _PARK_KEYS if isinstance(entry.get(k), dict)]
    return out


def statement_of(entry: dict[str, Any] | None) -> str:
    """The agent's stop statement in a park entry or a per-issue response, or ""."""
    for src in _sources(entry or {}):
        for k in _STATEMENT_KEYS:
            v = src.get(k)
            if isinstance(v, str) and v.strip():
                return v.strip()
    return ""


def parked_at_of(entry: dict[str, Any] | None) -> float | None:
    """When the story stopped, in Unix seconds, or None when Sortie does not say."""
    for src in _sources(entry or {}):
        for k in _TIME_KEYS:
            v = src.get(k)
            if isinstance(v, bool) or v is None:
                continue
            if isinstance(v, (int, float)):
                # Milliseconds, if it reads as a date past 2286 in seconds.
                return float(v) / 1000 if v > 1e10 else float(v)
            if isinstance(v, str) and v.strip():
                try:
                    return datetime.fromisoformat(v.strip().replace("Z", "+00:00")).timestamp()
                except ValueError:
                    continue
    return None


def parked(state: dict[str, Any] | None) -> list[dict[str, Any]]:
    """The park entries of Sortie's state, oldest first where Sortie says when."""
    entries = [e for e in (state or {}).get("parked") or [] if isinstance(e, dict) and e.get("issue_identifier")]
    return sorted(entries, key=lambda e: parked_at_of(e) or 0.0)


def question(entry: dict[str, Any], detail: dict[str, Any] | None = None) -> dict[str, Any]:
    """One open question: the park entry, with the full statement from the
    issue's own response when Sortie gave it."""
    return {
        "identifier": entry["issue_identifier"],
        "statement": statement_of(detail) or statement_of(entry),
        "parked_at": parked_at_of(entry) or parked_at_of(detail),
        "reason": (entry.get("reason") or "").replace("_", " ").strip() or None,
    }


def home_questions(questions: dict[str, dict[str, Any]], nodes: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The open questions of the project's stories, oldest first, each with
    its story. A park on an issue that is not a story of this project is not
    this project's question."""
    by_ident = {n["tracker"]["identifier"]: n for n in nodes if n.get("kind") == "story" and n.get("tracker")}
    # A story the plan replaces is closed by confirming its replacement, not
    # by an answer: name the replacement, so the question can offer that.
    replacement = {n["supersedes"]: n for n in nodes if n.get("kind") == "story" and n.get("supersedes")}
    out = []
    for ident, q in questions.items():
        n = by_ident.get(ident)
        if n is None:
            continue
        key = n["plan_key"]
        out.append({**q, "key": key, "label": key.split("/", 1)[-1], "change": n.get("change") or key.split("/", 1)[0],
                    "title": n.get("title") or key, "url": (n.get("tracker") or {}).get("url"),
                    "replaced_by": ({"key": r["plan_key"], "label": r["plan_key"].split("/", 1)[-1], "title": r.get("title") or r["plan_key"]}
                                    if (r := replacement.get(key)) else None)})
    out.sort(key=lambda q: (q["parked_at"] is None, q["parked_at"] or 0.0, q["key"]))
    for i, q in enumerate(out, 1):
        q["rank"] = i
    return out
