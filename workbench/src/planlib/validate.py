"""The validator's entry point: schema check, then graph checks, then the graph document."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .checks import check_frozen, check_graph, check_post_deploy, kit_beside, make_names_for
from .graph import build_graph
from .errors import PlanError
from .milestones import check_approval_links, check_milestones, implicit, normalize, read_tasks
from .requirements import openspec_show
from .schemas import Schemas
from .store import LoadedStore, load_store

# Passed as ``kit``: look for the kit beside the store.
BESIDE = "beside"


@dataclass
class ValidationResult:
    errors: list[PlanError]
    store: LoadedStore
    graph: dict[str, Any] | None = None
    names: dict[str, dict[str, set[str]]] = field(default_factory=dict)
    # Each story's digest, review gate, superseded story, and the pull
    # requests that delivered it, and its post-deploy check, by plan key.
    stories: dict[str, dict[str, Any]] = field(default_factory=dict)
    # The store's milestones with defaults filled in (one implicit milestone
    # without a milestones file), and each planned change's tasks.md items.
    milestones: list[dict[str, Any]] = field(default_factory=list)
    milestones_declared: bool = False
    tasks: dict[str, list[dict[str, Any]]] = field(default_factory=dict)
    # Problems that leave the plan valid, such as a post-deploy check the
    # kit beside the store does not have.
    warnings: list[PlanError] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return not self.errors


def validate(root: str | Path, *, show=openspec_show, schemas: Schemas | None = None,
             approvals: dict[str, str] | None = None, frozen: dict[str, str] | None = None,
             kit: str | Path | None = BESIDE) -> ValidationResult:
    """Validate the store at ``root``. Collects every problem; never stops at the first.

    ``approvals`` maps plan keys to the digests their approvals cover.
    ``frozen`` maps the plan keys of stories agents have started to their
    approved digests: such a story may not change or disappear. ``kit`` is
    the kit's checkout, whose checks a post-deploy check names: by default
    the one beside the store, if there is one; None checks no names."""
    root = Path(root).expanduser().resolve()
    schemas = schemas or Schemas.load(root)
    store = load_store(root, schemas)
    kit_root = kit_beside(root) if kit == BESIDE else (Path(kit).expanduser() if kit else None)
    warnings = check_post_deploy(store, kit_root)
    names_for = make_names_for(store, show=show)
    tasks = {e["name"]: read_tasks(root, e["name"]) for e in store.plan["changes"]} if store.plan else {}
    errors = (list(store.errors) + check_graph(store, names_for) + check_frozen(store, frozen or {})
              + check_milestones(store) + check_approval_links(store, tasks))
    if errors or store.plan is None:
        return ValidationResult(errors=errors, store=store, warnings=warnings)
    stories: dict[str, dict[str, Any]] = {}
    graph, errors = build_graph(store, names_for, schemas, approvals, stories)
    declared = store.milestones is not None
    return ValidationResult(
        errors=errors, store=store, graph=graph, stories=stories,
        milestones=normalize(store.milestones) if declared else implicit(store.plan),
        milestones_declared=declared,
        tasks=tasks,
        warnings=warnings,
    )
