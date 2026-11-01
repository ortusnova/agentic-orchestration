"""Load the planning JSON Schemas from a spec store and check documents against them."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from .errors import PlanError, PlanLoadError

SCHEMA_DIR = Path("openspec") / "schemas" / "planning"

# The format versions this planlib reads and writes. A document or a store
# schema with a newer version is refused, never guessed at.
SUPPORTED_VERSIONS: dict[str, int] = {
    "breakdown": 1,
    "plan": 1,
    "plan-graph": 2,
    "milestones": 1,
}

# Schemas a store may lack: it then has no file of that kind. A store
# created before milestones existed has no milestones schema.
OPTIONAL = frozenset({"milestones"})


@dataclass(frozen=True)
class Schemas:
    """The store's planning schemas, each with a ready validator."""

    validators: dict[str, Draft202012Validator]
    # The version each store schema declares. A store created before a
    # format change keeps its older schema, and planlib writes that version.
    versions: dict[str, int] = field(default_factory=dict)

    @classmethod
    def load(cls, store: Path) -> "Schemas":
        validators: dict[str, Draft202012Validator] = {}
        versions: dict[str, int] = {}
        for kind, supported in SUPPORTED_VERSIONS.items():
            path = Path(store) / SCHEMA_DIR / f"{kind}.schema.json"
            try:
                schema = json.loads(path.read_text())
            except FileNotFoundError as exc:
                if kind in OPTIONAL:
                    continue
                raise PlanLoadError(f"{path}: planning schema not found") from exc
            except json.JSONDecodeError as exc:
                raise PlanLoadError(f"{path}: not valid JSON: {exc}") from exc
            declared = schema.get("properties", {}).get("schema_version", {}).get("const")
            if not isinstance(declared, int):
                raise PlanLoadError(f"{path}: schema does not declare schema_version as a constant")
            if declared > supported:
                raise PlanLoadError(
                    f"{path}: the store's {kind} schema is version {declared}, "
                    f"but this planlib supports version {supported}; upgrade planlib"
                )
            Draft202012Validator.check_schema(schema)
            validators[kind] = Draft202012Validator(schema)
            versions[kind] = declared
        return cls(validators, versions)

    def has(self, kind: str) -> bool:
        return kind in self.validators

    def version(self, kind: str) -> int:
        """The version of ``kind`` the store's schema declares."""
        return self.versions.get(kind, SUPPORTED_VERSIONS[kind])

    def check(self, kind: str, doc: Any, *, file: str, change: str | None = None) -> list[PlanError]:
        """Check ``doc`` against the ``kind`` schema, version first.

        A document with a newer ``schema_version`` gets one error that names both
        versions and is not checked further.
        """
        if not isinstance(doc, dict):
            return [PlanError("schema", "the file must hold a mapping", file=file, change=change)]
        version_error = check_version(kind, doc, file=file, change=change)
        if version_error:
            return [version_error]
        errors = sorted(self.validators[kind].iter_errors(doc), key=lambda e: list(e.absolute_path))
        return [_to_plan_error(e, doc, file=file, change=change) for e in errors]


def check_version(kind: str, doc: dict, *, file: str, change: str | None = None) -> PlanError | None:
    supported = SUPPORTED_VERSIONS[kind]
    version = doc.get("schema_version")
    if isinstance(version, int) and not isinstance(version, bool) and version > supported:
        return PlanError(
            "unsupported_version",
            f"schema_version {version} is newer than the supported {kind} version {supported}",
            file=file,
            change=change,
        )
    return None


def _to_plan_error(err: ValidationError, doc: dict, *, file: str, change: str | None) -> PlanError:
    path = list(err.absolute_path)
    deliverable = None
    rest = path
    milestone = None
    if len(path) >= 2 and path[0] in ("deliverables", "changes", "nodes", "milestones") and isinstance(path[1], int):
        items = doc.get(path[0]) or []
        item = items[path[1]] if path[1] < len(items) else None
        key = "name" if path[0] == "changes" else ("plan_key" if path[0] == "nodes" else "id")
        label = item.get(key) if isinstance(item, dict) else None
        if path[0] == "deliverables":
            deliverable = label if isinstance(label, str) else f"#{path[1]}"
        elif path[0] == "changes" and isinstance(label, str) and change is None:
            change = label
        elif path[0] == "milestones":
            milestone = label if isinstance(label, str) else f"#{path[1]}"
        rest = path[2:]

    field = ".".join(str(p) for p in rest)
    if err.validator == "additionalProperties" and isinstance(err.instance, dict):
        allowed = set(err.schema.get("properties", {}))
        extra = sorted(set(err.instance) - allowed)
        names = ", ".join(_join(field, x) for x in extra)
        message = f"unknown field {names}"
    elif err.validator == "not" and err.validator_value == {"required": ["approved_digest"]}:
        message = "approved_digest is allowed only when status is approved"
    elif err.validator == "pattern" and rest and rest[0] == "delivered_by":
        message = f"delivered_by: '{err.instance}' is not a pull request; write owner/repo#number"
    elif err.validator == "required" and isinstance(err.instance, dict):
        missing = [r for r in err.validator_value if r not in err.instance]
        message = "missing field " + ", ".join(_join(field, m) for m in missing)
    else:
        target = f"field {field}" if field else "document"
        message = f"{target}: {err.message}"
    if milestone is not None:
        message = f"milestone '{milestone}': {message}"
    return PlanError("schema", message, file=file, change=change, deliverable=deliverable,
                     members=(milestone,) if milestone is not None else ())


def _join(prefix: str, name: str) -> str:
    return f"{prefix}.{name}" if prefix else name
