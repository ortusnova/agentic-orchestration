from __future__ import annotations

import os
import shutil
from pathlib import Path
from typing import Any, Callable

import pytest
import yaml

from planlib.requirements import clear_openspec_cache

FIXTURES = Path(__file__).parent / "fixtures"


class StoreBuilder:
    """Builds a throwaway spec store under a temp dir."""

    def __init__(self, root: Path, specs_for_covers: bool = False):
        self.root = root
        # Workbench tests turn this on: a breakdown's covers then get the
        # delta spec they name, so a story can be approved offline.
        self.specs_for_covers = specs_for_covers
        schema_dir = root / "openspec" / "schemas" / "planning"
        schema_dir.mkdir(parents=True)
        for f in (FIXTURES / "schemas").glob("*.schema.json"):
            shutil.copy(f, schema_dir / f.name)
        (root / "openspec" / "changes").mkdir(parents=True)

    def plan(self, changes: list[tuple[str, list[str]]] | dict | str) -> Path:
        if isinstance(changes, list):
            changes = {
                "schema_version": 1,
                "changes": [{"name": n, "depends_on": d} for n, d in changes],
            }
        return self._write(self.root / "openspec" / "plan.yaml", changes)

    def change(self, name: str, breakdown: dict | str | None = None) -> Path:
        d = self.root / "openspec" / "changes" / name
        d.mkdir(parents=True, exist_ok=True)
        (d / "proposal.md").write_text(f"## Why\n\n{name}\n")
        if breakdown is not None:
            self._write(d / "breakdown.yaml", breakdown)
            if self.specs_for_covers and isinstance(breakdown, dict):
                self._spec_for_covers(d, breakdown)
        return d

    @staticmethod
    def _spec_for_covers(change_dir: Path, breakdown: dict) -> None:
        """Write the delta spec a breakdown's ``covers`` names, adding only
        the requirements a spec file there does not have yet."""
        wanted: dict[str, list[str]] = {}
        for d in breakdown.get("deliverables", []):
            for ref in d.get("covers") or []:
                cap, _, name = ref.partition("#")
                if name and name not in wanted.setdefault(cap, []):
                    wanted[cap].append(name)
        for cap, names in wanted.items():
            f = change_dir / "specs" / cap / "spec.md"
            f.parent.mkdir(parents=True, exist_ok=True)
            body = f.read_text() if f.exists() else "## ADDED Requirements\n\n"
            for n in names:
                if f"### Requirement: {n}\n" not in body:
                    body += f"### Requirement: {n}\nIt SHALL {n}.\n\n#### Scenario: s\n- **WHEN** a\n- **THEN** b\n\n"
            f.write_text(body)

    def milestones(self, milestones: list[dict] | dict | str) -> Path:
        if isinstance(milestones, list):
            milestones = {"schema_version": 1, "milestones": milestones}
        return self._write(self.root / "openspec" / "milestones.yaml", milestones)

    def tasks(self, change: str, text: str) -> Path:
        d = self.root / "openspec" / "changes" / change
        d.mkdir(parents=True, exist_ok=True)
        path = d / "tasks.md"
        path.write_text(text)
        return path

    @staticmethod
    def _write(path: Path, content: Any) -> Path:
        text = content if isinstance(content, str) else yaml.safe_dump(content, sort_keys=False)
        path.write_text(text)
        return path


def deliverable(id: str, depends_on: list[str] | None = None, covers: list[str] | None = None, **extra) -> dict:
    d = {
        "id": id,
        "title": extra.pop("title", f"Do {id}"),
        "kind": extra.pop("kind", "story"),
        "depends_on": depends_on or [],
        "covers": covers or [],
        "acceptance_criteria": extra.pop("acceptance_criteria", [f"{id} works"]),
    }
    d.update(extra)
    return d


def milestone(id: str, includes: list[dict], depends_on: list[str] | None = None, **extra) -> dict:
    return {"id": id, "title": extra.pop("title", id.upper()), "depends_on": depends_on or [], "includes": includes, **extra}


def breakdown(*deliverables: dict, **top) -> dict:
    doc = {"schema_version": 1, "status": "draft", "deliverables": list(deliverables)}
    doc.update(top)
    return doc


@pytest.fixture
def store(tmp_path: Path) -> StoreBuilder:
    return StoreBuilder(tmp_path / "store")


@pytest.fixture
def make_store(tmp_path: Path) -> Callable[[str], StoreBuilder]:
    def make(name: str) -> StoreBuilder:
        return StoreBuilder(tmp_path / name)

    return make


def pytest_sessionfinish(session: pytest.Session, exitstatus: int) -> None:
    """With WORKBENCH_TEST_NO_SKIP=1, as in CI, a skipped test fails the run:
    a missing broker, Sortie checkout, or openspec CLI must not pass as green."""
    if os.environ.get("WORKBENCH_TEST_NO_SKIP") != "1" or exitstatus != pytest.ExitCode.OK:
        return
    reporter = session.config.pluginmanager.get_plugin("terminalreporter")
    skipped = reporter.stats.get("skipped", []) if reporter else []
    if skipped:
        reporter.write_line("")
        reporter.write_line(f"WORKBENCH_TEST_NO_SKIP=1: {len(skipped)} test(s) skipped; see the skip reasons", red=True)
        session.exitstatus = pytest.ExitCode.TESTS_FAILED


@pytest.fixture(autouse=True)
def _fresh_openspec_cache():
    """planlib caches `openspec show` output for the life of the process: each test starts empty."""
    clear_openspec_cache()
    yield
    clear_openspec_cache()
