"""The leases a supervisor holds on stories.

A story the supervisor has taken over, such as one whose live proof it runs
by hand, waits on the supervisor, not on a person. Sortie keeps it out of
dispatch under a lease (``POST /api/v1/{identifier}/hold``): the lease names
who holds it, why, the evidence of what is being done, when it was last
renewed and when it runs out. The supervisor renews it with new evidence
before then; a lease that runs out expires, and the story waits on a person
again. Each Sortie's state lists its leases in ``holds``.
"""

from __future__ import annotations

from datetime import datetime, timezone
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


def leases_of(states: Iterable[tuple[str, dict[str, Any] | None, str | None]]) -> dict[str, dict[str, Any]]:
    """Every lease of every Sortie, by tracker identifier, with the Sortie
    that holds it. An older Sortie serves no ``holds`` and gives none."""
    out: dict[str, dict[str, Any]] = {}
    for name, state, _ in states:
        for h in (state or {}).get("holds") or []:
            if not isinstance(h, dict) or not h.get("issue_identifier"):
                continue
            ident = h["issue_identifier"]
            out[ident] = {
                "identifier": ident, "sortie": name,
                "by": (h.get("by") or "").strip() or None,
                "reason": (h.get("reason") or "").strip() or None,
                "evidence": (h.get("evidence") or "").strip() or None,
                "held_since": _ts(h.get("held_since")), "renewed_at": _ts(h.get("renewed_at")),
                "until": _ts(h.get("until")), "expired": bool(h.get("expired")),
                "expired_at": _ts(h.get("expired_at")),
            }
    return out


def live(leases: dict[str, dict[str, Any]]) -> dict[str, dict[str, Any]]:
    return {i: lease for i, lease in leases.items() if not lease["expired"]}


def expired(leases: dict[str, dict[str, Any]]) -> dict[str, dict[str, Any]]:
    return {i: lease for i, lease in leases.items() if lease["expired"]}


def holder(lease: dict[str, Any]) -> str:
    """"the supervisor", or whoever else holds it."""
    by = lease.get("by")
    return "the supervisor" if not by or by == "supervisor" else by


def clock(t: float | None) -> str:
    """A time in words, in UTC: "2026-11-03 14:00 UTC"."""
    if t is None:
        return "an unknown time"
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d %H:%M UTC")


def waiting_text(lease: dict[str, Any]) -> str:
    """"waiting on the supervisor: full vm chain proof running"."""
    return f"waiting on {holder(lease)}" + (f": {lease['reason']}" if lease.get("reason") else "")


def expired_text(lease: dict[str, Any]) -> str:
    """"the supervisor's hold expired at …; last evidence: …"."""
    return (f"{holder(lease)}'s hold expired at {clock(lease.get('expired_at') or lease.get('until'))}; "
            f"last evidence: {lease.get('evidence') or 'none given'}")
