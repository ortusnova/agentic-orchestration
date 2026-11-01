"""Each story's plan status, from the workbench's approvals and Sortie's view.

A story is, in order of precedence:

- ``delivered``: it names the pull requests that delivered it, and every one
  is merged. It is done, wherever and however the work was done;
- ``delivery_unconfirmed``: it names pull requests, and some is open, closed
  without merging, or cannot be looked up. It is not done;
- ``needs_rereview``: a person must look again (a cancelled blocker, or an
  unverified no-change claim);
- ``frozen``: an agent started it, so its content can no longer change;
- ``draft``: no approval matches its current content;
- ``released``: approved and in the tracker's release state;
- ``ready``: approved, and every story it directly depends on is approved
  or delivered;
- ``held``: approved, but some direct blocker is neither.

A story that names its pull requests is never the agents' work: no sync
creates or releases it.
"""

from __future__ import annotations

from typing import Any

from .delivery import pending


def tracker_by_key(tracker: dict[str, Any] | None) -> dict[str, dict[str, Any]]:
    return {n["plan_key"]: n for n in (tracker or {}).get("nodes") or [] if n.get("plan_key")}


def is_started(t: dict[str, Any] | None, plan_states: dict[str, str] | None) -> bool:
    """Whether Sortie's view shows that agents started the story."""
    if not t:
        return False
    if t.get("runtime") or t.get("satisfaction"):
        return True
    state = (t.get("state") or "").lower()
    if plan_states:
        return state not in {(plan_states.get("backlog") or "").lower(), (plan_states.get("release") or "").lower()}
    return bool(t.get("terminal"))


def frozen_set(stories: dict[str, dict], approvals: dict[str, dict], tracker: dict[str, Any] | None) -> dict[str, str]:
    """The started stories, with the digests their content must keep: the
    approved digest, or the current one for a story started without one."""
    by_key = tracker_by_key(tracker)
    plan_states = (tracker or {}).get("plan_states")
    out = {}
    for key, meta in stories.items():
        if is_started(by_key.get(key), plan_states):
            out[key] = approvals[key]["digest"] if key in approvals else meta["digest"]
    for key, row in approvals.items():  # a started story the store no longer holds
        if key not in out and is_started(by_key.get(key), plan_states):
            out[key] = row["digest"]
    return out


def delivery(refs: list[str] | None, pulls: dict[str, dict[str, Any]] | None) -> dict[str, Any] | None:
    """Whether the pull requests a story names delivered it, and each one's state."""
    if not refs:
        return None
    items = [(pulls or {}).get(ref) or pending(ref) for ref in refs]
    done = all(p["state"] == "merged" for p in items)
    return {"state": "delivered" if done else "unconfirmed", "pulls": items}


def overlay(graph: dict[str, Any] | None, stories: dict[str, dict], approvals: dict[str, dict],
            rereviews: dict[str, dict], tracker: dict[str, Any] | None,
            pulls: dict[str, dict[str, Any]] | None = None) -> dict[str, dict[str, Any]]:
    """The plan fields of each story node, by plan key. ``pulls`` holds what
    GitHub reported of each pull request a story names."""
    if not graph:
        return {}
    by_key = tracker_by_key(tracker)
    plan_states = (tracker or {}).get("plan_states")
    release = (plan_states or {}).get("release", "").lower()
    story_keys = {n["plan_key"] for n in graph["nodes"] if n["kind"] == "story"}
    approved = {n["plan_key"] for n in graph["nodes"] if n["kind"] == "story" and n["approval"] == "approved"}
    deliveries = {key: delivery((stories.get(key) or {}).get("delivered_by"), pulls) for key in story_keys}
    delivered = {key for key, d in deliveries.items() if d and d["state"] == "delivered"}
    # What lets a dependent go: approved, or delivered. A story whose pull
    # requests do not confirm its delivery is not done, approved or not.
    passes = (approved | delivered) - {key for key, d in deliveries.items() if d and key not in delivered}
    blockers: dict[str, list[str]] = {}
    for b in graph["blocks"]:
        if b["blocker"] in story_keys and b["blocked"] in story_keys:
            blockers.setdefault(b["blocked"], []).append(b["blocker"])

    out = {}
    for key in story_keys:
        t = by_key.get(key)
        row = approvals.get(key) if key in approved else None
        fields: dict[str, Any] = {
            "plan_status": None, "held_by": [], "rereview": None, "frozen": is_started(t, plan_states),
            "approved_by": {"name": row["approver_name"], "id": row["approver_id"]} if row else None,
            "approved_at": row["approved_at"] if row else None,
            "review": (stories.get(key) or {}).get("review", "human"),
            "supersedes": (stories.get(key) or {}).get("supersedes"),
            "stale_release": False,
            "delivery": deliveries[key],
        }
        if key in delivered:
            fields["plan_status"] = "delivered"
        elif deliveries[key]:
            fields["plan_status"] = "delivery_unconfirmed"
        elif key in rereviews:
            fields["plan_status"] = "needs_rereview"
            fields["rereview"] = {"reason": rereviews[key]["reason"], "ref": rereviews[key]["ref"]}
        elif t and t.get("satisfaction") == "unverified_no_change":
            fields["plan_status"] = "needs_rereview"
            fields["rereview"] = {"reason": "unverified_no_change", "ref": t.get("unverified_reason") or ""}
        elif fields["frozen"]:
            fields["plan_status"] = "frozen"
        elif key not in approved:
            fields["plan_status"] = "draft"
            # Released before, edited since: it must come back before an agent takes it.
            fields["stale_release"] = bool(t and release and (t.get("state") or "").lower() == release and key in approvals)
        elif t and release and (t.get("state") or "").lower() == release:
            fields["plan_status"] = "released"
        else:
            missing = [b for b in blockers.get(key, []) if b not in passes]
            fields["plan_status"] = "held" if missing else "ready"
            fields["held_by"] = missing
        out[key] = fields
    return out


def sync_graph(graph: dict[str, Any], fields: dict[str, dict[str, Any]]) -> dict[str, Any]:
    """The graph a sync sends: version 2, with each story's release, review
    gate, and approver.

    A story that names its pull requests goes as a draft, so the sync
    neither creates nor releases it, and a delivered one blocks nothing:
    Sortie holds no merge record for work done outside it. An epic left
    with no approved story goes as a draft too."""
    named = {k for k, f in fields.items() if f.get("delivery")}
    delivered = {k for k, f in fields.items() if f.get("plan_status") == "delivered"}
    nodes = []
    for n in graph["nodes"]:
        n = dict(n)
        f = fields.get(n["plan_key"])
        if n["kind"] == "story" and n["plan_key"] in named:
            n["approval"] = "draft"
            n["release"] = False
            n["review"] = f["review"]
        elif n["kind"] == "story" and f is not None:
            n["release"] = f["plan_status"] in ("ready", "released")
            n["review"] = f["review"]
            if n["approval"] == "approved" and f["approved_by"]:
                n["approved_by"] = f["approved_by"]
                n["approved_at"] = _rfc3339(f["approved_at"])
        nodes.append(n)
    blocks = graph["blocks"]
    if named:
        approved_epics = {n["change"] for n in nodes if n["kind"] == "story" and n["approval"] == "approved"}
        for n in nodes:
            if n["kind"] == "epic" and n["approval"] == "approved" and n["plan_key"] not in approved_epics:
                n["approval"] = "draft"
        blocks = [b for b in blocks if b["blocker"] not in delivered]
    return {**graph, "schema_version": 2, "nodes": nodes, "blocks": blocks}


def _rfc3339(ts: int | None) -> str:
    import datetime

    return datetime.datetime.fromtimestamp(ts or 0, datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")
