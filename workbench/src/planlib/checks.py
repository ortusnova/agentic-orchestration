"""Graph checks over the plan and every breakdown, run after the schema check.

Every problem is collected; no check stops the others.
"""

from __future__ import annotations

from collections import Counter
from typing import Callable

import networkx as nx

from .errors import PlanError
from .requirements import RequirementsError, openspec_show, requirement_names
from .store import CHANGES_DIR, PLAN_FILE, LoadedStore

NamesFor = Callable[[str], dict[str, set[str]]]


def resolve_ref(change: str, ref: str) -> str:
    """Return the plan key a ``depends_on`` ref names, seen from ``change``."""
    return ref if "/" in ref else f"{change}/{ref}"


def plan_graph(store: LoadedStore) -> nx.DiGraph:
    """Epic graph: an edge from each change to every change it depends on."""
    g = nx.DiGraph()
    for entry in (store.plan or {}).get("changes", []):
        g.add_node(entry["name"])
        for dep in entry["depends_on"]:
            g.add_edge(entry["name"], dep)
    return g


def make_names_for(store: LoadedStore, show=openspec_show) -> NamesFor:
    cache: dict[str, dict[str, set[str]]] = {}

    def names_for(change: str) -> dict[str, set[str]]:
        if change not in cache:
            if not (store.root / CHANGES_DIR / change / "specs").is_dir():
                cache[change] = {}
            else:
                cache[change] = requirement_names(store.root, change, show=show)
        return cache[change]

    return names_for


def check_graph(store: LoadedStore, names_for: NamesFor) -> list[PlanError]:
    errors: list[PlanError] = []
    errors += _check_plan(store)
    errors += _check_deliverables(store)
    errors += _check_cycles(store)
    errors += _check_cross_change(store)
    errors += _check_covers(store, names_for)
    errors += _check_supersedes(store)
    return errors


def check_frozen(store: LoadedStore, frozen: dict[str, str]) -> list[PlanError]:
    """A story agents have started may not change or disappear."""
    from .approval import resolved, story_digest

    current = {f"{c}/{d['id']}": (bd, d) for c, bd in store.breakdowns.items() for d in bd.doc["deliverables"]}
    errors = []
    for key, digest in sorted(frozen.items()):
        change, _, did = key.partition("/")
        if key not in current:
            errors.append(PlanError("frozen", f"'{key}' has started and cannot be removed; add a story that supersedes it instead",
                                    file=str(CHANGES_DIR / change / "breakdown.yaml"), change=change, deliverable=did, members=(key,)))
        elif story_digest(resolved(current[key][0].doc, current[key][1])) != digest:
            errors.append(PlanError("frozen", f"'{key}' has started and cannot change; add an amending story that depends on it, "
                                    "or a replacement that supersedes it", file=current[key][0].file, change=change, deliverable=did, members=(key,)))
    return errors


def _check_supersedes(store: LoadedStore) -> list[PlanError]:
    errors = []
    known = {f"{c}/{d['id']}" for c, bd in store.breakdowns.items() for d in bd.doc["deliverables"]}
    seen: dict[str, str] = {}
    for change, bd in sorted(store.breakdowns.items()):
        for d in bd.doc["deliverables"]:
            if not d.get("supersedes"):
                continue
            key, target = f"{change}/{d['id']}", resolve_ref(change, d["supersedes"])
            if target == key:
                errors.append(PlanError("bad_supersedes", "a deliverable cannot supersede itself", file=bd.file, change=change, deliverable=d["id"]))
            elif target not in known:
                errors.append(PlanError("bad_supersedes", f"supersedes '{d['supersedes']}', which is not a deliverable in the store",
                                        file=bd.file, change=change, deliverable=d["id"], members=(key, target)))
            elif target in seen:
                errors.append(PlanError("bad_supersedes", f"'{target}' is already superseded by '{seen[target]}'",
                                        file=bd.file, change=change, deliverable=d["id"], members=(key, seen[target], target)))
            else:
                seen[target] = key
    return errors


def _check_plan(store: LoadedStore) -> list[PlanError]:
    if store.plan is None:
        return []
    errors = []
    file = str(PLAN_FILE)
    entries = store.plan["changes"]
    counts = Counter(e["name"] for e in entries)
    for name, n in sorted(counts.items()):
        if n > 1:
            errors.append(PlanError("duplicate_change", f"change '{name}' is listed {n} times", file=file, change=name))
    listed = set(counts)
    active = set(store.changes)
    for entry in entries:
        name = entry["name"]
        if name not in active:
            errors.append(
                PlanError("unknown_change", f"change '{name}' is not an active change in the store", file=file, change=name)
            )
        for dep in entry["depends_on"]:
            if dep not in listed:
                errors.append(
                    PlanError(
                        "dangling_change_ref",
                        f"change '{name}' depends on '{dep}', which the plan does not list",
                        file=file,
                        change=name,
                        members=(name, dep),
                    )
                )
    return errors


def _check_deliverables(store: LoadedStore) -> list[PlanError]:
    errors = []
    known = {
        f"{c}/{d['id']}" for c, bd in store.breakdowns.items() for d in bd.doc["deliverables"]
    }
    for change, bd in sorted(store.breakdowns.items()):
        counts = Counter(d["id"] for d in bd.doc["deliverables"])
        for did, n in sorted(counts.items()):
            if n > 1:
                errors.append(
                    PlanError("duplicate_id", f"id '{did}' is used {n} times", file=bd.file, change=change, deliverable=did)
                )
        for d in bd.doc["deliverables"]:
            for ref in d["depends_on"]:
                key = resolve_ref(change, ref)
                if key not in known:
                    errors.append(
                        PlanError(
                            "dangling_ref",
                            f"depends on '{ref}', which is not a deliverable in the store",
                            file=bd.file,
                            change=change,
                            deliverable=d["id"],
                            members=(f"{change}/{d['id']}", key),
                        )
                    )
    return errors


def _check_cycles(store: LoadedStore) -> list[PlanError]:
    errors = []
    epics = plan_graph(store)
    for members in _cycles(epics):
        errors.append(
            PlanError(
                "cycle",
                "changes depend on each other in a cycle: " + " -> ".join(members),
                file=str(PLAN_FILE),
                change=members[0],
                members=tuple(members),
            )
        )

    stories = nx.DiGraph()
    files: dict[str, str] = {}
    for change, bd in store.breakdowns.items():
        for d in bd.doc["deliverables"]:
            key = f"{change}/{d['id']}"
            files[key] = bd.file
            stories.add_node(key)
            for ref in d["depends_on"]:
                stories.add_edge(key, resolve_ref(change, ref))
    for members in _cycles(stories):
        first = members[0]
        change, _, did = first.partition("/")
        errors.append(
            PlanError(
                "cycle",
                "deliverables depend on each other in a cycle: " + " -> ".join(members),
                file=files.get(first),
                change=change,
                deliverable=did,
                members=tuple(members),
            )
        )
    return errors


def _cycles(g: nx.DiGraph) -> list[list[str]]:
    """Each strongly connected set of nodes that forms a cycle, with all its members.

    Members are ordered along one cycle through them when there is a simple
    one, so the message reads as a path.
    """
    out = []
    for comp in nx.strongly_connected_components(g):
        if len(comp) == 1:
            (n,) = comp
            if not g.has_edge(n, n):
                continue
            out.append([n])
            continue
        sub = g.subgraph(comp)
        try:
            path = [u for u, _ in nx.find_cycle(sub, source=min(comp))]
        except nx.NetworkXNoCycle:  # pragma: no cover - a strong component always has one
            path = []
        rest = sorted(comp - set(path))
        out.append(path + rest)
    return sorted(out)


def _check_cross_change(store: LoadedStore) -> list[PlanError]:
    if store.plan is None:
        return []
    epics = plan_graph(store)
    errors = []
    for change, bd in sorted(store.breakdowns.items()):
        if change not in epics:
            continue  # an unplanned change is never synced, so its links are not checked yet
        for d in bd.doc["deliverables"]:
            for ref in d["depends_on"]:
                if "/" not in ref:
                    continue
                other = ref.split("/", 1)[0]
                if other == change:
                    continue
                if other in epics and nx.has_path(epics, change, other):
                    continue
                errors.append(
                    PlanError(
                        "cross_change_not_in_plan",
                        f"'{change}/{d['id']}' depends on '{ref}', but the plan does not order "
                        f"change '{change}' after change '{other}'",
                        file=bd.file,
                        change=change,
                        deliverable=d["id"],
                        members=(f"{change}/{d['id']}", ref, change, other),
                    )
                )
    return errors


def _check_covers(store: LoadedStore, names_for: NamesFor) -> list[PlanError]:
    errors = []
    for change, bd in sorted(store.breakdowns.items()):
        if not any(d["covers"] for d in bd.doc["deliverables"]):
            continue
        try:
            names = names_for(change)
        except RequirementsError as exc:
            errors.append(PlanError("openspec", str(exc), file=bd.file, change=change))
            continue
        for d in bd.doc["deliverables"]:
            for ref in d["covers"]:
                capability, _, name = ref.partition("#")
                if capability not in names:
                    msg = f"covers '{ref}', but the change has no spec '{capability}'"
                elif name not in names[capability]:
                    msg = f"covers '{ref}', but spec '{capability}' has no requirement '{name}'"
                else:
                    continue
                errors.append(
                    PlanError("unknown_requirement", msg, file=bd.file, change=change, deliverable=d["id"])
                )
    return errors
