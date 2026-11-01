"""Requirement names of a change, for checking ``covers`` refs.

OpenSpec is the authority on its spec format, but its JSON output (tested
with 1.13) carries each delta requirement's text and not its name. So the
names come from the ``### Requirement:`` headers of the change's delta specs,
and every header's requirement is cross-checked against ``openspec show
<change> --type change --json``: the same capabilities, the same number of
requirements, the same text, in the same order. Any disagreement is an error,
never a silent pass.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import subprocess
import threading
from collections import OrderedDict
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from .store import CHANGES_DIR

# The OpenSpec version whose `show --json` output this module was tested with.
TESTED_OPENSPEC_VERSION = "1.13"

# OpenSpec's CLI sends usage telemetry on every command and waits for the send
# before it exits: about 0.25s of each call, and a network request per plan
# check. planlib turns it off for every call it makes (an operator who wants
# it can set these variables to other values; an explicit value is kept).
OPENSPEC_ENV = {"OPENSPEC_TELEMETRY": "0", "DO_NOT_TRACK": "1"}

# Parsed `openspec show` output, by change and the bytes it was read from.
_CACHE_SIZE = 256
_cache: OrderedDict[tuple, Any] = OrderedDict()
_cache_lock = threading.Lock()


def openspec_env() -> dict[str, str]:
    """The environment for an OpenSpec subprocess: this process's, with
    telemetry off unless the operator set the variables explicitly."""
    return {**OPENSPEC_ENV, **os.environ}


# Delta operations whose requirements exist in the change once it is applied.
_LIVE_OPERATIONS = ("ADDED", "MODIFIED")
_SECTION = re.compile(r"^##\s+(ADDED|MODIFIED|REMOVED|RENAMED)\s+Requirements\s*$")
_REQUIREMENT = re.compile(r"^###\s+Requirement:\s*(.+?)\s*$")
_RENAME_TO = re.compile(r"^\s*-?\s*TO:\s*`?###\s+Requirement:\s*(.+?)`?\s*$")
_FENCE = re.compile(r"^\s*(```|~~~)")


class RequirementsError(Exception):
    """OpenSpec could not be run, or its output did not have the expected shape."""


ShowRunner = Callable[[Path, str], Any]


@dataclass(frozen=True)
class _HeaderRequirement:
    operation: str
    name: str
    text: str


def _run_openspec_show(store: Path, change: str) -> Any:
    cmd = ["openspec", "show", change, "--type", "change", "--json", "--no-interactive"]
    try:
        proc = subprocess.run(cmd, cwd=store, capture_output=True, text=True, timeout=60, env=openspec_env())
    except FileNotFoundError as exc:
        raise RequirementsError("openspec is not installed or not on PATH") from exc
    except subprocess.TimeoutExpired as exc:
        raise RequirementsError(f"`{' '.join(cmd)}` timed out") from exc
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout).strip().splitlines()
        raise RequirementsError(f"`{' '.join(cmd)}` failed: {detail[-1] if detail else proc.returncode}")
    try:
        return json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise RequirementsError(f"`{' '.join(cmd)}` did not print JSON: {exc}") from exc


def _cache_key(store: Path, change: str) -> tuple:
    """What `openspec show <change>` reads: the change's files, the store's
    OpenSpec config, and the OpenSpec install itself."""
    h = hashlib.sha256()
    root = Path(store) / CHANGES_DIR / change
    for f in sorted(p for p in root.rglob("*") if p.is_file()):
        h.update(str(f.relative_to(root)).encode() + b"\0" + f.read_bytes() + b"\0")
    config = Path(store) / "openspec" / "config.yaml"
    if config.is_file():
        h.update(b"config\0" + config.read_bytes())
    exe = shutil.which("openspec") or ""
    real = os.path.realpath(exe) if exe else ""
    try:
        stamp = os.stat(real).st_mtime_ns if real else 0
    except OSError:
        stamp = 0
    return (str(Path(store).resolve()), change, h.hexdigest(), real, stamp)


def openspec_show(store: Path, change: str) -> Any:
    """``openspec show <change> --type change --json`` in the store, cached by
    what it reads: the same files give the same output, so an unchanged change
    costs no subprocess. A failure is never cached."""
    key = _cache_key(store, change)
    with _cache_lock:
        if key in _cache:
            _cache.move_to_end(key)
            return json.loads(_cache[key])
    result = _run_openspec_show(store, change)
    with _cache_lock:
        # Kept as text, so no caller can change the cached copy.
        _cache[key] = json.dumps(result)
        while len(_cache) > _CACHE_SIZE:
            _cache.popitem(last=False)
    return result


def clear_openspec_cache() -> None:
    with _cache_lock:
        _cache.clear()


def requirement_names(store: Path, change: str, show: ShowRunner = openspec_show) -> dict[str, set[str]]:
    """Map each capability path in the change to its live requirement names.

    Raises ``RequirementsError`` when OpenSpec fails or disagrees with the
    delta spec headers.
    """
    headers = _read_headers(Path(store) / CHANGES_DIR / change / "specs")
    reported = _reported_texts(show(Path(store), change), change)

    problems: list[str] = []
    for capability in sorted(set(headers) | set(reported)):
        mine = [_norm(h.text) for h in headers.get(capability, []) if h.operation in _LIVE_OPERATIONS]
        theirs = reported.get(capability, [])
        if len(mine) != len(theirs):
            problems.append(
                f"{capability}: the delta spec has {len(mine)} added or modified requirements, "
                f"OpenSpec reports {len(theirs)}"
            )
            continue
        for i, (a, b) in enumerate(zip(mine, theirs)):
            if a != b and not (b and a.startswith(b)):
                name = [h.name for h in headers[capability] if h.operation in _LIVE_OPERATIONS][i]
                problems.append(f"{capability}: requirement '{name}' does not match OpenSpec's text for it")
    if problems:
        raise RequirementsError(
            "requirement headers disagree with `openspec show --json` "
            f"(tested with OpenSpec {TESTED_OPENSPEC_VERSION}): " + "; ".join(problems)
        )

    names: dict[str, set[str]] = {}
    for capability, items in headers.items():
        live = {h.name for h in items if h.operation in _LIVE_OPERATIONS}
        live |= {h.name for h in items if h.operation == "RENAMED"}
        names[capability] = live
    return names


def _reported_texts(payload: Any, change: str) -> dict[str, list[str]]:
    def shape(msg: str) -> RequirementsError:
        return RequirementsError(
            f"unexpected `openspec show {change} --type change --json` output "
            f"(tested with OpenSpec {TESTED_OPENSPEC_VERSION}): {msg}"
        )

    if not isinstance(payload, dict) or not isinstance(payload.get("deltas"), list):
        raise shape("no 'deltas' list")
    out: dict[str, list[str]] = {}
    for i, delta in enumerate(payload["deltas"]):
        if not isinstance(delta, dict):
            raise shape(f"delta {i} is not an object")
        spec, op = delta.get("spec"), delta.get("operation")
        if not isinstance(spec, str) or not isinstance(op, str):
            raise shape(f"delta {i} has no 'spec' or 'operation' string")
        if op not in _LIVE_OPERATIONS:
            continue
        req = delta.get("requirement")
        if not isinstance(req, dict) or not isinstance(req.get("text"), str):
            raise shape(f"delta {i} ({spec}) has no 'requirement.text' string")
        out.setdefault(spec, []).append(_norm(req["text"]))
    return out


def _read_headers(specs_dir: Path) -> dict[str, list[_HeaderRequirement]]:
    out: dict[str, list[_HeaderRequirement]] = {}
    if not specs_dir.is_dir():
        return out
    for spec in sorted(specs_dir.rglob("spec.md")):
        capability = spec.parent.relative_to(specs_dir).as_posix()
        out[capability] = list(_parse_delta(spec.read_text()))
    return out


def _parse_delta(text: str):
    section: str | None = None
    current: tuple[str, str] | None = None
    body: list[str] = []
    in_fence = False

    def flush():
        if current is not None:
            yield _HeaderRequirement(current[0], current[1], "\n".join(body))

    for line in text.splitlines():
        # OpenSpec leaves fenced code out of a requirement's text, and a header
        # inside a fence is not a header.
        if _FENCE.match(line):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        m = _SECTION.match(line)
        if m or (line.startswith("## ") and not line.startswith("### ")):
            yield from flush()
            current, body = None, []
            section = m.group(1) if m else None
            continue
        if section is None:
            continue
        if section == "RENAMED":
            r = _RENAME_TO.match(line)
            if r:
                yield _HeaderRequirement("RENAMED", r.group(1), "")
            continue
        m = _REQUIREMENT.match(line)
        if m:
            yield from flush()
            current, body = (section, m.group(1)), []
            continue
        if current is not None:
            if line.startswith("#### ") or line.startswith("### "):
                # Scenarios and anything after them are not part of the requirement text.
                yield from flush()
                current, body = None, []
                continue
            body.append(line)
    yield from flush()


_SCENARIO = re.compile(r"^####\s+Scenario:\s*(.+?)\s*$")
_STEP = re.compile(r"^\s*[-*]\s+\*\*([A-Z]+)\*\*\s*(.*?)\s*$")


def requirement_details(store: Path, change: str) -> dict[str, list[dict[str, Any]]]:
    """Each capability of the change, with its added and modified requirements
    as people read them: name, operation, text, and scenarios with their
    WHEN/THEN steps, in file order. Read from the delta specs, which the
    validator has already checked against OpenSpec; a missing change has none.
    """
    specs_dir = Path(store) / CHANGES_DIR / change / "specs"
    out: dict[str, list[dict[str, Any]]] = {}
    if not specs_dir.is_dir():
        return out
    for spec in sorted(specs_dir.rglob("spec.md")):
        capability = spec.parent.relative_to(specs_dir).as_posix()
        out[capability] = list(_parse_details(spec.read_text()))
    return out


def main_requirement_details(store: Path, capability: str) -> list[dict[str, Any]]:
    """The requirements of a capability's main spec, in the form
    :func:`requirement_details` gives, with operation ``None``; a missing
    spec has none."""
    spec = Path(store) / "openspec" / "specs" / capability / "spec.md"
    if not spec.is_file():
        return []
    return list(_parse_details(spec.read_text(), main=True))


_MAIN_SECTION = re.compile(r"^##\s+Requirements\s*$")
_RENAME_FROM = re.compile(r"^\s*-?\s*FROM:\s*`?###\s+Requirement:\s*(.+?)`?\s*$")


def renamed_from(store: Path, change: str) -> dict[tuple[str, str], str]:
    """The change's renames, as (capability, new name) to old name."""
    specs_dir = Path(store) / CHANGES_DIR / change / "specs"
    out: dict[tuple[str, str], str] = {}
    if not specs_dir.is_dir():
        return out
    for spec in sorted(specs_dir.rglob("spec.md")):
        capability = spec.parent.relative_to(specs_dir).as_posix()
        section, old = None, None
        for line in spec.read_text().splitlines():
            m = _SECTION.match(line)
            if m or (line.startswith("## ") and not line.startswith("### ")):
                section, old = (m.group(1) if m else None), None
            elif section == "RENAMED":
                if f := _RENAME_FROM.match(line):
                    old = f.group(1)
                elif (t := _RENAME_TO.match(line)) and old is not None:
                    out[(capability, t.group(1))] = old
                    old = None
    return out


def _parse_details(text: str, main: bool = False):
    section: str | None = None
    req: dict[str, Any] | None = None
    scenario: dict[str, Any] | None = None
    body: list[str] = []
    in_fence = False

    def flush():
        if req is not None:
            req["text"] = "\n".join(body).strip()
            yield req

    for line in text.splitlines():
        if _FENCE.match(line):
            in_fence = not in_fence
            if req is not None and scenario is None:
                body.append(line)
            continue
        if in_fence:
            if req is not None and scenario is None:
                body.append(line)
            continue
        m = _SECTION.match(line)
        if m or (line.startswith("## ") and not line.startswith("### ")):
            yield from flush()
            req, scenario, body = None, None, []
            if main:
                section = "MAIN" if _MAIN_SECTION.match(line) else None
            else:
                section = m.group(1) if m else None
            continue
        if section not in _LIVE_OPERATIONS and section != "MAIN":
            continue
        m = _REQUIREMENT.match(line)
        if m:
            yield from flush()
            op = None if section == "MAIN" else section
            req, scenario, body = {"name": m.group(1), "operation": op, "text": "", "scenarios": []}, None, []
            continue
        if req is None:
            continue
        m = _SCENARIO.match(line)
        if m:
            scenario = {"name": m.group(1), "steps": []}
            req["scenarios"].append(scenario)
            continue
        if scenario is None:
            body.append(line)
            continue
        m = _STEP.match(line)
        if m:
            scenario["steps"].append({"keyword": m.group(1), "text": m.group(2)})
        elif line.strip() and scenario["steps"]:
            scenario["steps"][-1]["text"] += " " + line.strip()
    yield from flush()


def _norm(text: str) -> str:
    return " ".join(text.split())
