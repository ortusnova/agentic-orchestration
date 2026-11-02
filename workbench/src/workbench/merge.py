"""One graph from three sources: the validated plan, Sortie's plan graph, and runtime state.

Nodes match by plan key. A node only in the store is a draft, a node in
both is synced and shows the tracker's state, and a plan-keyed issue the
store no longer holds is orphaned.
"""

from __future__ import annotations

from typing import Any


def merge(plan: dict[str, Any] | None, tracker: dict[str, Any] | None) -> dict[str, Any]:
    plan = plan or {"nodes": [], "parents": [], "blocks": [], "unplanned_changes": []}
    tracker_nodes = (tracker or {}).get("nodes") or []
    by_key = {n["plan_key"]: n for n in tracker_nodes if n.get("plan_key")}
    by_id = {n["id"]: n for n in tracker_nodes}

    nodes: list[dict[str, Any]] = []
    in_plan = set()
    for n in plan["nodes"]:
        in_plan.add(n["plan_key"])
        t = by_key.get(n["plan_key"])
        nodes.append({**n, "status": "synced" if t else "draft", "tracker": _tracker_view(t), "runtime": (t or {}).get("runtime")})

    parents = list(plan["parents"])
    blocks = list(plan["blocks"])
    for key, t in by_key.items():
        if key in in_plan:
            continue
        change = key.split("/", 1)[0]
        kind = t.get("kind") or ("story" if "/" in key else "epic")
        nodes.append({
            "plan_key": key, "kind": kind, "change": change, "title": t.get("title") or key, "body": "",
            "approval": None, "has_spec": None, "depth": 0, "deliverable_kind": None, "covers": [],
            "acceptance_criteria": [], "status": "orphaned", "tracker": _tracker_view(t), "runtime": t.get("runtime"),
        })
        parent = by_id.get(t.get("parent_id") or "")
        if parent and parent.get("plan_key"):
            parents.append({"child": key, "parent": parent["plan_key"]})

    # Relations the tracker holds between nodes that are not both in the
    # plan (orphans) are shown too; the plan's own edges are its intent.
    known = {n["plan_key"] for n in nodes}
    seen = {(b["blocker"], b["blocked"]) for b in blocks}
    for e in (tracker or {}).get("edges") or []:
        a, b = by_id.get(e["blocker_id"]), by_id.get(e["blocked_id"])
        if not a or not b or not a.get("plan_key") or not b.get("plan_key"):
            continue
        pair = (a["plan_key"], b["plan_key"])
        if pair in seen or (pair[0] in in_plan and pair[1] in in_plan):
            continue
        if pair[0] in known and pair[1] in known:
            blocks.append({"blocker": pair[0], "blocked": pair[1]})
            seen.add(pair)

    return {
        "nodes": nodes,
        "parents": parents,
        "blocks": blocks,
        "unplanned_changes": list(plan.get("unplanned_changes", [])),
    }


def _tracker_view(t: dict[str, Any] | None) -> dict[str, Any] | None:
    if not t:
        return None
    return {k: t.get(k) for k in ("id", "identifier", "title", "state", "terminal", "url", "parent_id")}
