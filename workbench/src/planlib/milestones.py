"""Milestones: the outcomes a plan works toward, and how far it is from each.

A store may declare them in ``openspec/milestones.yaml``. Each milestone
collects whole changes or single stories, may count a change's tasks.md
items by kind, names an optional gate story, and depends on earlier
milestones. Without the file the project is one implicit milestone that
holds every planned change.
"""

from __future__ import annotations

import re
from collections import Counter
from pathlib import Path
from typing import Any, Iterable

import networkx as nx

from .errors import PlanError
from .store import BREAKDOWN_FILE, CHANGES_DIR, MILESTONES_FILE, LoadedStore
TASKS_FILE = "tasks.md"

TASK_KINDS = ("agent", "metal", "operator")
PERSON_KINDS = ("metal", "operator")
# Which tasks.md items a change's ``tasks`` setting counts, and whether its
# stories, which are agent work, count with them.
SELECTS = {"all": set(TASK_KINDS), "agent": {"agent"}, "metal": {"metal"}, "operator": {"operator"}}
STORIES_WITH = {"all", "agent"}

_ITEM = re.compile(r"^\s*[-*]\s+\[([ xX])\]\s+(.*?)\s*$")
_NUMBER = re.compile(r"^(\d+(?:\.\d+)*)\.?\s+")
_TAG = re.compile(r"\s*\[(metal|operator)\]\s*$")
_NAMED = re.compile(r"`([^`]*)`")
_STORY_REF = re.compile(r"^([a-z0-9]+(-[a-z0-9]+)*/)?[a-z0-9]+(-[a-z0-9]+)*$")
# The words an approval task may hold beside the stories it names.
_APPROVAL_WORDS = {"approve", "and", "then", "both", "in", "the", "workbench"}

IMPLICIT_ID = "project"


# Task kinds -----------------------------------------------------------------


def approval_link(text: str, change: str) -> list[str]:
    """The plan keys of the stories an operator task asks to approve, when
    it asks for nothing else; empty when it is not an approval task.

    It starts with "Approve" and names each story in backticks, by its id
    in ``change`` or as ``change/id``, joined only by "and", "then", commas
    and "in the workbench". A condition after "once" ("once `a` has
    merged") says when, not what, so the stories it names are not linked.
    A task that asks for any other step ("select it", "choose Confirm:
    replaces") is not linked."""
    text = text.strip().rstrip(".").strip()
    if not re.match(r"Approve\s", text):
        return []
    when = re.search(r"\s+once\s+", text)
    if when:
        if re.search(r"[.;:](\s|$)", text[when.end():]):
            return []
        text = text[: when.start()]
    names = _NAMED.findall(text)
    if not names or not all(_STORY_REF.match(n) for n in names):
        return []
    rest = _NAMED.sub(" ", text)
    if any(w.lower() not in _APPROVAL_WORDS for w in re.findall(r"[^\s,]+", rest)):
        return []
    keys = [n if "/" in n else f"{change}/{n}" for n in names]
    return list(dict.fromkeys(keys))


def parse_tasks(text: str, change: str = "") -> list[dict[str, Any]]:
    """Each checklist item of a tasks.md: its number, text, kind and whether it is ticked.

    An item whose line ends with ``[metal]`` is a metal task, with
    ``[operator]`` an operator task, and any other an agent task. An
    operator task that only asks for stories to be approved lists their
    plan keys in ``approves`` (see :func:`approval_link`)."""
    out = []
    for lineno, line in enumerate(text.splitlines(), start=1):
        m = _ITEM.match(line)
        if not m:
            continue
        body = m.group(2)
        tag = _TAG.search(body)
        kind = tag.group(1) if tag else "agent"
        if tag:
            body = body[: tag.start()]
        num = _NUMBER.match(body)
        number = num.group(1) if num else None
        if num:
            body = body[num.end():]
        out.append({
            "change": change,
            "id": f"{change}#{number or f'L{lineno}'}",
            "number": number,
            "text": body.strip(),
            "kind": kind,
            "done": m.group(1) != " ",
            "line": lineno,
            "approves": approval_link(body, change) if kind == "operator" else [],
        })
    return out


def check_approval_links(store: LoadedStore, tasks: dict[str, list[dict[str, Any]]]) -> list[PlanError]:
    """An approval task names only stories the store holds. A story of a
    change whose breakdown did not load is not checked: that breakdown's
    own error says why."""
    held = {f"{c}/{d['id']}" for c, bd in store.breakdowns.items() for d in bd.doc["deliverables"]}
    errors: list[PlanError] = []
    for change, items in tasks.items():
        for t in items:
            for key in t.get("approves") or []:
                holder = key.split("/", 1)[0]
                if key in held or (holder not in store.breakdowns
                                   and (store.root / CHANGES_DIR / holder / BREAKDOWN_FILE).is_file()):
                    continue
                errors.append(PlanError(
                    "approval_task_link",
                    f"task {t['number'] or t['id']} asks to approve '{key}', which no breakdown holds",
                    file=str(CHANGES_DIR / change / TASKS_FILE), change=change, members=(t["id"], key)))
    return errors


def read_tasks(root: Path, change: str) -> list[dict[str, Any]]:
    path = Path(root) / CHANGES_DIR / change / TASKS_FILE
    if not path.is_file():
        return []
    return parse_tasks(path.read_text(errors="replace"), change)


def task_counts(tasks: Iterable[dict[str, Any]]) -> dict[str, dict[str, int]]:
    """Done and total of each kind."""
    out = {k: {"done": 0, "total": 0} for k in TASK_KINDS}
    for t in tasks:
        out[t["kind"]]["total"] += 1
        out[t["kind"]]["done"] += int(t["done"])
    return out


# The file -------------------------------------------------------------------


def normalize(doc: dict[str, Any]) -> list[dict[str, Any]]:
    """The milestones of a schema-valid file, with every default filled in."""
    out = []
    for m in doc.get("milestones") or []:
        includes = []
        for inc in m["includes"]:
            if "change" in inc:
                includes.append({"change": inc["change"], "tasks": inc.get("tasks", "all")})
            else:
                includes.append({"story": inc["story"]})
        out.append({
            "id": m["id"], "title": m["title"], "depends_on": list(m["depends_on"]), "includes": includes,
            "gate": m.get("gate"), "goal": bool(m.get("goal", False)), "declared_done": bool(m.get("done", False)),
            "implicit": False,
        })
    return out


def implicit(plan: dict[str, Any] | None, title: str = "The plan") -> list[dict[str, Any]]:
    """The one milestone of a store without a milestones file: every planned change."""
    changes = [e["name"] for e in (plan or {}).get("changes", [])]
    return [{
        "id": IMPLICIT_ID, "title": title, "depends_on": [], "gate": None, "goal": True, "declared_done": False, "implicit": True,
        "includes": [{"change": c, "tasks": "all"} for c in changes],
    }]


def check_milestones(store: LoadedStore) -> list[PlanError]:
    """References, duplicates, cycles and goals. Runs after the schema check."""
    if store.milestones is None:
        return []
    file = str(MILESTONES_FILE)
    ms = normalize(store.milestones)
    planned = {e["name"] for e in (store.plan or {}).get("changes", [])}
    stories = {f"{c}/{d['id']}" for c, bd in store.breakdowns.items() for d in bd.doc["deliverables"]}
    errors: list[PlanError] = []

    counts = Counter(m["id"] for m in ms)
    for mid, n in sorted(counts.items()):
        if n > 1:
            errors.append(PlanError("duplicate_milestone", f"milestone '{mid}' is declared {n} times", file=file, members=(mid,)))
    ids = set(counts)
    for m in ms:
        mid = m["id"]
        for inc in m["includes"]:
            if "change" in inc and store.plan is not None and inc["change"] not in planned:
                errors.append(PlanError("unknown_change", f"milestone '{mid}' includes change '{inc['change']}', which plan.yaml does not list",
                                        file=file, change=inc["change"], members=(mid,)))
            if "story" in inc and inc["story"] not in stories:
                errors.append(PlanError("unknown_story", f"milestone '{mid}' includes story '{inc['story']}', which no breakdown holds",
                                        file=file, members=(mid, inc["story"])))
        if m["gate"] and m["gate"] not in stories:
            errors.append(PlanError("unknown_story", f"milestone '{mid}' has gate '{m['gate']}', which no breakdown holds",
                                    file=file, members=(mid, m["gate"])))
        for dep in m["depends_on"]:
            if dep not in ids:
                errors.append(PlanError("unknown_milestone", f"milestone '{mid}' depends on '{dep}', which no milestone has",
                                        file=file, members=(mid, dep)))

    g = nx.DiGraph()
    for m in ms:
        g.add_node(m["id"])
        for dep in m["depends_on"]:
            if dep in ids:
                g.add_edge(m["id"], dep)
    for comp in sorted((sorted(c) for c in nx.strongly_connected_components(g)), key=lambda c: c[0]):
        if len(comp) > 1 or g.has_edge(comp[0], comp[0]):
            errors.append(PlanError("milestone_cycle", "milestones depend on each other in a cycle: " + ", ".join(comp),
                                    file=file, members=tuple(comp)))

    goals = [m["id"] for m in ms if m["goal"]]
    if len(goals) > 1:
        errors.append(PlanError("several_goals", "more than one milestone is the goal: " + ", ".join(goals),
                                file=file, members=tuple(goals)))
    return errors


def order(milestones: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Dependency order, ties broken by file order. Assumes no cycle."""
    index = {m["id"]: i for i, m in enumerate(milestones)}
    waiting = {m["id"]: {d for d in m["depends_on"] if d in index} for m in milestones}
    out: list[dict[str, Any]] = []
    while waiting:
        ready = sorted((mid for mid, deps in waiting.items() if not deps), key=index.__getitem__)
        if not ready:  # a cycle: keep file order for the rest
            ready = sorted(waiting, key=index.__getitem__)
        mid = ready[0]
        out.append(milestones[index[mid]])
        del waiting[mid]
        for deps in waiting.values():
            deps.discard(mid)
    return out


def goal(milestones: list[dict[str, Any]]) -> str | None:
    """The milestone with ``goal: true``, or else the last in dependency order."""
    for m in milestones:
        if m["goal"]:
            return m["id"]
    ordered = order(milestones)
    return ordered[-1]["id"] if ordered else None


# Progress -------------------------------------------------------------------


def stories_by_change(graph: dict[str, Any] | None) -> dict[str, list[str]]:
    out: dict[str, list[str]] = {}
    for n in (graph or {}).get("nodes") or []:
        if n.get("kind") == "story":
            out.setdefault(n["change"], []).append(n["plan_key"])
    return out


def story_blocks(graph: dict[str, Any] | None) -> list[tuple[str, str]]:
    """(blocker, blocked) between stories: the stories' own dependencies, and
    the plan's order between changes. When change B comes after change A, the
    stories B starts with wait for the stories A ends with, so whatever is
    counted along story dependencies (slack, dependents, the critical path)
    also follows plan.yaml."""
    nodes = (graph or {}).get("nodes") or []
    stories = {n["plan_key"] for n in nodes if n.get("kind") == "story"}
    epics = {n["plan_key"] for n in nodes if n.get("kind") != "story"}
    blocks = (graph or {}).get("blocks") or []
    out = [(b["blocker"], b["blocked"]) for b in blocks if b["blocker"] in stories and b["blocked"] in stories]
    by_change = stories_by_change(graph)
    inner = {(a, b) for a, b in out if a.split("/", 1)[0] == b.split("/", 1)[0]}
    def ends(change: str) -> list[str]:  # stories no other story of the change waits on
        return [s for s in by_change.get(change, []) if not any(a == s for a, _ in inner)]
    def starts(change: str) -> list[str]:  # stories that wait on no other story of the change
        return [s for s in by_change.get(change, []) if not any(b == s for _, b in inner)]
    seen = set(out)
    for b in blocks:
        if b["blocker"] in epics and b["blocked"] in epics:
            for a in ends(b["blocker"]):
                for c in starts(b["blocked"]):
                    if (a, c) not in seen:
                        seen.add((a, c))
                        out.append((a, c))
    return out


def members(m: dict[str, Any], by_change: dict[str, list[str]], tasks: dict[str, list[dict[str, Any]]]
            ) -> tuple[list[str], list[dict[str, Any]], list[str]]:
    """The stories, the tasks.md items, and the changes a milestone counts."""
    stories: list[str] = []
    counted: list[dict[str, Any]] = []
    changes: list[str] = []
    seen_tasks: set[str] = set()
    for inc in m["includes"]:
        if "change" in inc:
            change = inc["change"]
            if change not in changes:
                changes.append(change)
            if inc["tasks"] in STORIES_WITH:
                stories += by_change.get(change, [])
            kinds = SELECTS[inc["tasks"]]
            for t in tasks.get(change, []):
                if t["kind"] in kinds and t["id"] not in seen_tasks:
                    seen_tasks.add(t["id"])
                    counted.append(t)
        else:
            stories.append(inc["story"])
            change = inc["story"].split("/", 1)[0]
            if change not in changes:
                changes.append(change)
    if m.get("gate"):
        stories.append(m["gate"])
    return list(dict.fromkeys(stories)), counted, changes


def membership(ordered: list[dict[str, Any]], stories: dict[str, list[str]], graph: dict[str, Any] | None,
               dropped: set[str]) -> dict[str, tuple[list[str], list[str]]]:
    """The stories each milestone counts, and those of them it only waits on.

    A milestone waits on the stories its stories depend on, directly or
    through other stories. One that no milestone includes, or only a later
    one does, counts with the milestone that waits on it, after its own, in
    plan order. A story counts once, in the earliest milestone, in dependency
    order, that includes or waits on it; a milestone's gate always counts
    with it, and a cancelled story counts nowhere."""
    nodes = [n["plan_key"] for n in (graph or {}).get("nodes") or [] if n.get("kind") == "story"]
    rank = {k: i for i, k in enumerate(nodes)}
    deps = nx.DiGraph()
    deps.add_nodes_from(nodes)
    deps.add_edges_from((b["blocker"], b["blocked"]) for b in (graph or {}).get("blocks") or []
                        if b["blocker"] in rank and b["blocked"] in rank)
    gates = {m["gate"] for m in ordered if m.get("gate")}
    counted: set[str] = set()
    out: dict[str, tuple[list[str], list[str]]] = {}
    for m in ordered:
        own = [s for s in stories[m["id"]] if s not in dropped and (s not in counted or s == m.get("gate"))]
        mine = set(own)
        found: set[str] = set()
        todo = [s for s in own if s in deps]
        seen = set(todo)
        while todo:
            for a in deps.predecessors(todo.pop()):
                if a in seen or a in dropped:
                    continue
                seen.add(a)
                todo.append(a)
                if a not in mine and a not in counted and a not in gates:
                    found.add(a)
        waits = sorted(found, key=rank.__getitem__)
        out[m["id"]] = (own + waits, waits)
        counted |= mine | found
    return out


def progress(milestones: list[dict[str, Any]], graph: dict[str, Any] | None, tasks: dict[str, list[dict[str, Any]]],
             done: set[str], dropped: Iterable[str] = (), estimates: dict[str, float] | None = None) -> dict[str, Any]:
    """Each milestone's membership and progress in dependency order, the
    goal, the next milestone and its critical path.

    A milestone is done when it is declared done, when its gate story is
    done, or, without a gate, when everything it counts is done.
    ``done`` holds the plan keys whose tracker issue is in the done state;
    ``dropped`` the cancelled ones, which count toward nothing. Stories,
    agent tasks and person tasks are reported apart, never summed."""
    dropped = set(dropped)
    by_change = stories_by_change(graph)
    ordered = order(milestones)
    base = {m["id"]: members(m, by_change, tasks) for m in ordered}
    counts = membership(ordered, {mid: b[0] for mid, b in base.items()}, graph, dropped)
    out = []
    for m in ordered:
        _, counted, changes = base[m["id"]]
        stories, waits = counts[m["id"]]
        changes = list(dict.fromkeys(changes + [s.split("/", 1)[0] for s in waits]))
        kinds = task_counts(counted)
        person = {"done": sum(kinds[k]["done"] for k in PERSON_KINDS), "total": sum(kinds[k]["total"] for k in PERSON_KINDS)}
        stories_done = sum(1 for s in stories if s in done)
        if m.get("declared_done"):
            is_done = True
        elif m.get("gate"):
            is_done = m["gate"] in done
        else:
            is_done = stories_done == len(stories) and all(t["done"] for t in counted)
        out.append({
            **m,
            "stories": stories,
            "waits_on": waits,
            "changes": changes,
            "tasks": counted,
            "progress": {
                "stories": {"done": stories_done, "total": len(stories)},
                "agent_tasks": kinds["agent"],
                "person_tasks": person,
                "metal_tasks": kinds["metal"],
                "operator_tasks": kinds["operator"],
            },
            "done": is_done,
        })
    nxt = next((m for m in out if not m["done"]), None)
    path = critical_path(nxt, graph, done, estimates) if nxt else []
    return {"milestones": out, "goal": goal(milestones), "next": nxt["id"] if nxt else None, "critical_path": path}


def critical_path(m: dict[str, Any], graph: dict[str, Any] | None, done: set[str],
                  estimates: dict[str, float] | None = None) -> list[str]:
    """The longest chain of the milestone's unfinished stories, by dependency,
    that ends at its gate, or at any story with no unfinished dependent in
    the milestone when it has no gate. A story counts as one unless it has
    an estimate. Ties break by plan order."""
    estimates = estimates or {}
    rank = {n["plan_key"]: i for i, n in enumerate((graph or {}).get("nodes") or [])}
    open_ = [s for s in m["stories"] if s not in done]
    keep = set(open_)
    g = nx.DiGraph()
    g.add_nodes_from(open_)
    g.add_edges_from((a, b) for a, b in story_blocks(graph) if a in keep and b in keep)
    gate = m.get("gate")
    if gate and gate in keep:
        g = g.subgraph(nx.ancestors(g, gate) | {gate}).copy()
        ends = [gate]
    elif gate:
        return []
    else:
        ends = [n for n in g if g.out_degree(n) == 0]
    if not g:
        return []

    def key(path: list[str]) -> tuple:
        length = sum(estimates.get(k, 1) for k in path)
        return (length, tuple(-rank.get(k, len(rank)) for k in path))

    best: dict[str, list[str]] = {}
    try:
        topo = list(nx.lexicographical_topological_sort(g, key=lambda k: rank.get(k, len(rank))))
    except nx.NetworkXUnfeasible:  # a cycle fails validation first
        return []
    for n in topo:
        preds = [best[p] for p in g.predecessors(n)]
        best[n] = (max(preds, key=key) if preds else []) + [n]
    return max((best[e] for e in ends), key=key)
