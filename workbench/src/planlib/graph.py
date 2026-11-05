"""Build the validated plan-graph document, the only form of the plan that leaves the store."""

from __future__ import annotations

import hashlib
import re
from typing import Any, Callable

import networkx as nx
import yaml

from .approval import post_deploy_check, resolved, story_digest
from .checks import NamesFor, resolve_ref
from .errors import PlanError
from .requirements import RequirementsError, main_requirement_details, renamed_from, requirement_details
from .schemas import Schemas
from .store import CHANGES_DIR, PLAN_FILE, LoadedStore

_WHY = re.compile(r"^##\s+Why\s*$(.*?)(?=^##\s|\Z)", re.MULTILINE | re.DOTALL)


def source_digest(store: LoadedStore) -> str:
    files: list[tuple[str, bytes]] = []
    if store.plan_raw is not None:
        files.append((str(PLAN_FILE), store.plan_raw))
    files += [(bd.file, bd.raw) for bd in store.breakdowns.values()]
    h = hashlib.sha256()
    for path, raw in sorted(files):
        h.update(path.encode() + b"\0" + str(len(raw)).encode() + b"\0" + raw)
    return "sha256:" + h.hexdigest()


def build_graph(store: LoadedStore, names_for: NamesFor, schemas: Schemas,
                approvals: dict[str, str] | None = None,
                stories: dict[str, dict[str, Any]] | None = None) -> tuple[dict[str, Any] | None, list[PlanError]]:
    """Return the graph document, or the errors that prevent building it.

    ``stories``, when given, is filled with each story's digest, review
    gate, the story it supersedes, the pull requests that delivered it, and
    its post-deploy check:
    what the workbench needs beside the graph, which the graph schema does
    not carry.

    ``approvals`` maps plan keys to the digest each story's approval
    covers; a story is approved while its digest matches. Without
    approvals every story reads as draft. Call only after every check
    passed.
    """
    assert store.plan is not None
    approvals = approvals or {}
    stories = {} if stories is None else stories
    graph_v2 = schemas.version("plan-graph") >= 2
    errors: list[PlanError] = []
    nodes: list[dict[str, Any]] = []
    parents: list[dict[str, str]] = []
    blocks: list[dict[str, str]] = []

    planned = [e["name"] for e in store.plan["changes"]]
    epic_graph = nx.DiGraph()
    for entry in store.plan["changes"]:
        epic_graph.add_node(entry["name"])
        for dep in entry["depends_on"]:
            epic_graph.add_edge(dep, entry["name"])
            blocks.append({"blocker": dep, "blocked": entry["name"]})
    epic_depth = _depths(epic_graph)

    # A replacement takes over the dependents of the story it supersedes.
    replaced_by = {
        resolve_ref(change, d["supersedes"]): f"{change}/{d['id']}"
        for change, bd in store.breakdowns.items() for d in bd.doc["deliverables"] if d.get("supersedes")
    }

    for change in planned:
        try:
            has_spec = any(names_for(change).values())
        except RequirementsError as exc:
            errors.append(PlanError("openspec", str(exc), change=change))
            continue
        bd = store.breakdowns.get(change)
        story_approval = {}
        if bd:
            for d in bd.doc["deliverables"]:
                key = f"{change}/{d['id']}"
                story_approval[key] = "approved" if approvals.get(key) == story_digest(resolved(bd.doc, d)) else "draft"
        if bd is None:
            approval = "unplanned"
        else:
            approval = "approved" if "approved" in story_approval.values() else "draft"
        nodes.append(
            {
                "plan_key": change,
                "kind": "epic",
                "change": change,
                "title": change,
                "body": _epic_body(store, change),
                "approval": approval,
                "has_spec": has_spec,
                "depth": epic_depth[change],
                "deliverable_kind": None,
                "covers": [],
                "acceptance_criteria": [],
            }
        )
        if bd is None:
            continue

        story_graph = nx.DiGraph()
        for d in bd.doc["deliverables"]:
            key = f"{change}/{d['id']}"
            story_graph.add_node(key)
            for ref in d["depends_on"]:
                blocker = resolve_ref(change, ref)
                if blocker in replaced_by and replaced_by[blocker] != key:
                    blocker = replaced_by[blocker]
                blocks.append({"blocker": blocker, "blocked": key})
                if blocker.split("/", 1)[0] == change:
                    story_graph.add_edge(blocker, key)
        story_depth = _depths(story_graph)
        requirement = requirement_lookup(store, change)
        for d in bd.doc["deliverables"]:
            key = f"{change}/{d['id']}"
            repository = resolved(bd.doc, d).get("repository")
            nodes.append(
                {
                    "plan_key": key,
                    "kind": "story",
                    "change": change,
                    "title": d["title"],
                    "body": _story_body(d, requirement),
                    "approval": story_approval[key],
                    "has_spec": bool(d["covers"]),
                    "depth": story_depth[key],
                    "deliverable_kind": d["kind"],
                    "covers": list(d["covers"]),
                    "acceptance_criteria": list(d["acceptance_criteria"]),
                    **({"review": d.get("review", "human")} if graph_v2 else {}),
                    **({"repository": repository} if graph_v2 and repository else {}),
                }
            )
            stories[key] = {
                "digest": story_digest(resolved(bd.doc, d)),
                "review": d.get("review", "human"),
                "supersedes": resolve_ref(change, d["supersedes"]) if d.get("supersedes") else None,
                "delivered_by": list(d.get("delivered_by") or []),
                "post_deploy_check": post_deploy_check(d),
            }
            parents.append({"child": key, "parent": change})

    if errors:
        return None, errors
    doc = {
        "schema_version": schemas.version("plan-graph"),
        "source_digest": source_digest(store),
        "nodes": nodes,
        "parents": parents,
        "blocks": blocks,
        "unplanned_changes": [c for c in store.changes if c not in set(planned)],
    }
    problems = schemas.check("plan-graph", doc, file="<plan graph>")
    if problems:  # pragma: no cover - a bug in this module, not in the store
        raise AssertionError("built graph does not match plan-graph.schema.json: " + "; ".join(map(str, problems)))
    return doc, []


def _depths(g: nx.DiGraph) -> dict[str, int]:
    """Column of each node: a node's generation is one past its latest blocker's,
    which is the length of the longest dependency chain ending at it."""
    return {n: i for i, generation in enumerate(nx.topological_generations(g)) for n in generation}


def _epic_body(store: LoadedStore, change: str) -> str:
    body = f"OpenSpec change `{change}`."
    proposal = store.root / CHANGES_DIR / change / "proposal.md"
    if proposal.is_file():
        m = _WHY.search(proposal.read_text())
        if m and m.group(1).strip():
            body += "\n\n## Why\n\n" + m.group(1).strip()
    return body


def _story_body(d: dict, requirement: Callable[[str], dict[str, Any] | None]) -> str:
    """The issue description: the notes, the acceptance criteria, the
    post-deploy check, and the full text of each covered requirement. The
    agent works from this alone, without the store. Only the deliverable's
    fields count toward its approval digest, so the spec text here never
    changes an approval."""
    parts = []
    if d.get("notes"):
        parts.append(d["notes"].strip())
    parts.append("## Acceptance criteria\n\n" + "\n".join(f"- {c}" for c in d["acceptance_criteria"]))
    check = post_deploy_check(d)
    if check:
        parts.append(POST_DEPLOY_CHECK_HEADING + "\n\n" + post_deploy_check_block(check))
    if d["covers"]:
        parts.append("## Covers\n\n" + "\n".join(f"- `{c}`" for c in d["covers"]))
        parts.append("## Requirements\n\n" + "\n\n".join(_requirement_text(ref, requirement(ref)) for ref in d["covers"]))
    return "\n\n".join(parts)


POST_DEPLOY_CHECK_HEADING = "## Post-deploy check"


def post_deploy_check_block(check: dict) -> str:
    """The post-deploy check as the issue carries it, for the kit to read:
    one fenced YAML block holding check, args and hosts, in that order."""
    text = yaml.safe_dump(check, sort_keys=False, default_flow_style=False, allow_unicode=True,
                          width=1 << 20)
    return f"```yaml\n{text}```"


def _requirement_text(ref: str, req: dict[str, Any] | None) -> str:
    capability, _, name = ref.partition("#")
    head = f"### {name}\n\n`{capability}`"
    if req is None:
        return head + "\n\nNo spec file in the store holds this requirement's text."
    out = [head, req["text"]] if req["text"] else [head]
    for s in req["scenarios"]:
        steps = "\n".join(f"- **{st['keyword']}** {st['text']}" for st in s["steps"])
        out.append(f"#### Scenario: {s['name']}" + (f"\n\n{steps}" if steps else ""))
    return "\n\n".join(out)


def requirement_lookup(store: LoadedStore, change: str) -> Callable[[str], dict[str, Any] | None]:
    """Find a ``covers`` ref's requirement: in the change's delta specs, or,
    for one the change only renames, in the main specs under its old name."""
    delta: dict[str, list[dict[str, Any]]] | None = None
    renames: dict[tuple[str, str], str] = {}
    main: dict[str, list[dict[str, Any]]] = {}

    def find(ref: str) -> dict[str, Any] | None:
        nonlocal delta, renames
        capability, _, name = ref.partition("#")
        if delta is None:
            delta = requirement_details(store.root, change)
            renames = renamed_from(store.root, change)
        for req in delta.get(capability, []):
            if req["name"] == name:
                return req
        if capability not in main:
            main[capability] = main_requirement_details(store.root, capability)
        for wanted in (name, renames.get((capability, name))):
            for req in main[capability]:
                if wanted and req["name"] == wanted:
                    return req
        return None

    return find
