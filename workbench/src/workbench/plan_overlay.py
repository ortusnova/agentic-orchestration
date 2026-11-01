"""Each story's plan status, from the workbench's approvals and Sortie's view.

A story is, in order of precedence:

- ``needs_rereview``: a person must look again (a cancelled blocker, or an
  unverified no-change claim);
- ``frozen``: an agent started it, so its content can no longer change;
- ``needs_spec``: no approval matches, it covers no requirement, and it is
  not delivered: it cannot be approved until it has a spec;
- ``draft``: no approval matches its current content;
- ``released``: approved and in the tracker's release state;
- ``ready``: approved, and every story it directly depends on is approved;
- ``held``: approved, but some direct blocker is not.
"""

from __future__ import annotations

from typing import Any


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


def is_delivered(t: dict[str, Any] | None, plan_states: dict[str, str] | None) -> bool:
    """Whether the story's tracker issue is done, not cancelled."""
    if not t:
        return False
    state = (t.get("state") or "").lower()
    done = ((plan_states or {}).get("done") or "").lower()
    cancel = ((plan_states or {}).get("cancel") or "").lower()
    if done:
        return state == done
    return bool(t.get("terminal")) and not (cancel and state == cancel)


def needs_spec(node: dict[str, Any], t: dict[str, Any] | None, plan_states: dict[str, str] | None) -> bool:
    """Whether a deliverable cannot be approved for want of a spec: it
    covers no requirement and is not delivered. A person approves a spec,
    so an enabler needs one too."""
    return node.get("kind") == "story" and not node.get("covers") and not is_delivered(t, plan_states)


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


def overlay(graph: dict[str, Any] | None, stories: dict[str, dict], approvals: dict[str, dict],
            rereviews: dict[str, dict], tracker: dict[str, Any] | None) -> dict[str, dict[str, Any]]:
    """The plan fields of each story node, by plan key."""
    if not graph:
        return {}
    by_key = tracker_by_key(tracker)
    plan_states = (tracker or {}).get("plan_states")
    release = (plan_states or {}).get("release", "").lower()
    story_keys = {n["plan_key"] for n in graph["nodes"] if n["kind"] == "story"}
    nodes = {n["plan_key"]: n for n in graph["nodes"]}
    approved = {n["plan_key"] for n in graph["nodes"] if n["kind"] == "story" and n["approval"] == "approved"}
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
        }
        if key in rereviews:
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
            missing = [b for b in blockers.get(key, []) if b not in approved]
            fields["plan_status"] = "held" if missing else "ready"
            fields["held_by"] = missing
        # Only a story without an approval reads so: one approved before a
        # spec was required keeps its approval.
        if fields["plan_status"] in ("draft", "needs_rereview") and key not in approved \
                and needs_spec(nodes[key], t, plan_states):
            fields["plan_status"] = "needs_spec"
        out[key] = fields
    return out


def sync_graph(graph: dict[str, Any], fields: dict[str, dict[str, Any]]) -> dict[str, Any]:
    """The graph a sync sends: version 2, with each story's release, review
    gate, and approver."""
    nodes = []
    for n in graph["nodes"]:
        n = dict(n)
        f = fields.get(n["plan_key"])
        if n["kind"] == "story" and f is not None:
            n["release"] = f["plan_status"] in ("ready", "released")
            n["review"] = f["review"]
            if n["approval"] == "approved" and f["approved_by"]:
                n["approved_by"] = f["approved_by"]
                n["approved_at"] = _rfc3339(f["approved_at"])
        nodes.append(n)
    return {**graph, "schema_version": 2, "nodes": nodes}


def _rfc3339(ts: int | None) -> str:
    import datetime

    return datetime.datetime.fromtimestamp(ts or 0, datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")
