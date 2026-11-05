"""A story declares a post-deploy check, and its issue carries it
(agent-platform/supervisor-autonomy, "A control-plane change is proven by a
check its deploy runs"; design D15, point 1)."""

from __future__ import annotations

import json
import re

import pytest
import yaml

from planlib import load_store, validate
from planlib.cli import main
from tests.conftest import breakdown, deliverable

CHANGE = "credential-revoked-restart"
KEY = f"{CHANGE}/post-deploy"
CHECK = {"check": "credential-restart", "args": ["--unit", "ortus-stage0-sortie"], "hosts": ["p1"]}


def no_openspec(*_):
    raise AssertionError("openspec should not be called")


def declare(store, check=None, **extra):
    store.plan([(CHANGE, [])])
    if check is not None:
        extra["post_deploy_check"] = check
    store.change(CHANGE, breakdown(deliverable("post-deploy", **extra), deliverable("other")))


def kit(tmp_path, *checks: str):
    d = tmp_path / "agentic-orchestration" / "dev" / "stage0" / "checks"
    d.mkdir(parents=True)
    for c in checks:
        (d / c).write_text("#!/bin/sh\n")
    return d.parents[2]


def node(result, key=KEY):
    return next(n for n in result.graph["nodes"] if n["plan_key"] == key)


def section(body: str) -> dict:
    m = re.search(r"^## Post-deploy check\n\n```yaml\n(.*?)```$", body, re.MULTILINE | re.DOTALL)
    assert m, body
    return yaml.safe_load(m.group(1))


# The schema


@pytest.mark.parametrize("check", [
    CHECK,
    {"check": "credential-restart", "args": []},
    {"check": "c1", "args": ["", "a b", "--x=1"], "hosts": ["p1", "taichi", "worker-2"]},
])
def test_a_valid_declaration(store, check):
    declare(store, check)
    r = validate(store.root, show=no_openspec, kit=None)
    assert r.ok, r.errors
    assert r.warnings == []


@pytest.mark.parametrize("check, needle", [
    ("credential-restart", "post_deploy_check"),
    (["credential-restart"], "post_deploy_check"),
    ({}, "post_deploy_check.check"),
    ({"args": []}, "post_deploy_check.check"),
    ({"check": "credential-restart"}, "post_deploy_check.args"),
    ({"check": "Credential_Restart", "args": []}, "post_deploy_check.check"),
    ({"check": "credential-restart.sh", "args": []}, "post_deploy_check.check"),
    ({"check": "", "args": []}, "post_deploy_check.check"),
    ({"check": 7, "args": []}, "post_deploy_check.check"),
    ({"check": "c", "args": "--unit x"}, "post_deploy_check.args"),
    ({"check": "c", "args": [1]}, "post_deploy_check.args"),
    ({"check": "c", "args": [None]}, "post_deploy_check.args"),
    ({"check": "c", "args": [], "hosts": []}, "post_deploy_check.hosts"),
    ({"check": "c", "args": [], "hosts": "p1"}, "post_deploy_check.hosts"),
    ({"check": "c", "args": [], "hosts": ["p1", "p1"]}, "post_deploy_check.hosts"),
    ({"check": "c", "args": [], "hosts": ["P1"]}, "post_deploy_check.hosts"),
    ({"check": "c", "args": [], "hosts": ["p1 taichi"]}, "post_deploy_check.hosts"),
    ({"check": "c", "args": [], "hosts": [""]}, "post_deploy_check.hosts"),
    ({"check": "c", "args": [], "timeout": 60}, "post_deploy_check.timeout"),
])
def test_any_other_shape_is_refused(store, check, needle):
    declare(store, check)
    errs = load_store(store.root).errors
    assert errs, check
    assert all(e.code == "schema" and e.deliverable == "post-deploy" for e in errs), errs
    assert any(needle in e.message for e in errs), errs
    assert not validate(store.root, show=no_openspec, kit=None).ok
    assert main(["validate", "--store", str(store.root), "--kit", ""]) == 1


def test_a_check_the_kit_beside_the_store_lacks_is_a_warning(store, tmp_path, capsys):
    kit(tmp_path, "egress-reachable.sh")
    declare(store, CHECK)
    r = validate(store.root, show=no_openspec)
    assert r.ok, r.errors
    assert [(w.code, w.deliverable) for w in r.warnings] == [("unknown_check", "post-deploy")]
    assert "credential-restart" in r.warnings[0].message and "dev/stage0/checks" in r.warnings[0].message

    assert main(["validate", "--store", str(store.root)]) == 0
    captured = capsys.readouterr()
    assert "warning:" in captured.err and "unknown_check" in captured.err
    assert main(["validate", "--store", str(store.root), "--json"]) == 0
    out = json.loads(capsys.readouterr().out)
    assert out["ok"] and [w["code"] for w in out["warnings"]] == ["unknown_check"]


@pytest.mark.parametrize("name", ["credential-restart", "credential-restart.sh", "credential-restart.py"])
def test_a_check_the_kit_has_is_no_warning(store, tmp_path, name):
    kit(tmp_path, name)
    declare(store, CHECK)
    r = validate(store.root, show=no_openspec)
    assert r.ok and r.warnings == []


def test_a_kit_without_a_checks_directory_warns(store, tmp_path):
    (tmp_path / "agentic-orchestration" / "dev" / "stage0").mkdir(parents=True)
    declare(store, CHECK)
    assert [w.code for w in validate(store.root, show=no_openspec).warnings] == ["unknown_check"]


def test_a_store_inside_the_kit_reads_its_checks(tmp_path, make_store):
    store = make_store("specs")
    (tmp_path / "dev" / "stage0" / "checks").mkdir(parents=True)
    (tmp_path / "dev" / "stage0" / "checks" / "credential-restart").write_text("")
    declare(store, CHECK)
    assert validate(store.root, show=no_openspec).warnings == []
    declare(store, {**CHECK, "check": "missing"})
    assert [w.code for w in validate(store.root, show=no_openspec).warnings] == ["unknown_check"]


def test_without_a_kit_beside_the_store_no_name_is_checked(store):
    declare(store, CHECK)
    r = validate(store.root, show=no_openspec)
    assert r.ok and r.warnings == []


def test_an_explicit_kit(store, tmp_path):
    other = tmp_path / "elsewhere"
    (other / "dev" / "stage0" / "checks").mkdir(parents=True)
    declare(store, CHECK)
    assert [w.code for w in validate(store.root, show=no_openspec, kit=other).warnings] == ["unknown_check"]
    (other / "dev" / "stage0" / "checks" / "credential-restart").write_text("")
    assert validate(store.root, show=no_openspec, kit=other).warnings == []


# The issue section


def test_the_issue_carries_the_check(store):
    """A credential restart on p1: the story's issue says what p1's deploy runs."""
    declare(store, CHECK)
    r = validate(store.root, show=no_openspec, kit=None)
    body = node(r)["body"]
    assert section(body) == CHECK
    assert ("## Post-deploy check\n\n```yaml\ncheck: credential-restart\nargs:\n- --unit\n- ortus-stage0-sortie\n"
            "hosts:\n- p1\n```") in body
    assert body.index("## Acceptance criteria") < body.index("## Post-deploy check")
    assert r.stories[KEY]["post_deploy_check"] == CHECK
    # The graph is the contract with Sortie: the check travels in the body only.
    assert "post_deploy_check" not in node(r)


def test_the_section_fills_in_the_default_hosts_and_keeps_its_order(store):
    declare(store, {"hosts": ["taichi", "p1"], "args": [], "check": "c"})
    assert "```yaml\ncheck: c\nargs: []\nhosts:\n- taichi\n- p1\n```" in node(validate(store.root, show=no_openspec, kit=None))["body"]
    declare(store, {"check": "c", "args": ["yes", "1", "a: b", "#x", "ünï"]})
    body = node(validate(store.root, show=no_openspec, kit=None))["body"]
    assert section(body) == {"check": "c", "args": ["yes", "1", "a: b", "#x", "ünï"], "hosts": ["p1"]}


def test_a_story_without_a_check_has_no_section(store):
    declare(store)
    r = validate(store.root, show=no_openspec, kit=None)
    assert "Post-deploy check" not in node(r)["body"]
    assert r.stories[KEY]["post_deploy_check"] is None


# The approval digest


def digest(store, key=KEY):
    return validate(store.root, show=no_openspec, kit=None).stories[key]["digest"]


def test_adding_the_check_needs_a_new_approval(store):
    declare(store)
    before = digest(store)
    other = digest(store, f"{CHANGE}/other")
    declare(store, CHECK)
    r = validate(store.root, show=no_openspec, kit=None, approvals={KEY: before, f"{CHANGE}/other": other})
    assert r.stories[KEY]["digest"] != before
    assert node(r)["approval"] == "draft"
    assert node(r, f"{CHANGE}/other")["approval"] == "approved"


@pytest.mark.parametrize("change", [
    {"check": "other-check"},
    {"args": ["--unit", "ortus-stage0-workbench"]},
    {"args": ["ortus-stage0-sortie", "--unit"]},
    {"args": []},
    {"hosts": ["p1", "taichi"]},
    {"hosts": ["taichi"]},
])
def test_changing_the_check_needs_a_new_approval(store, change):
    declare(store, CHECK)
    before = digest(store)
    declare(store, {**CHECK, **change})
    r = validate(store.root, show=no_openspec, kit=None, approvals={KEY: before})
    assert node(r)["approval"] == "draft"
    # A started story cannot change its check.
    frozen = validate(store.root, show=no_openspec, kit=None, frozen={KEY: before})
    assert [e.code for e in frozen.errors] == ["frozen"]


def test_naming_the_default_hosts_keeps_the_approval(store):
    declare(store, {"check": "c", "args": []})
    before = digest(store)
    declare(store, {"check": "c", "args": [], "hosts": ["p1"]})
    r = validate(store.root, show=no_openspec, kit=None, approvals={KEY: before}, frozen={KEY: before})
    assert r.ok, r.errors
    assert node(r)["approval"] == "approved"
