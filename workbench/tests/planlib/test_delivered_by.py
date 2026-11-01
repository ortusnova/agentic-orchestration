"""A story names the pull request that delivered it (agent-platform/delivered-work)."""

from __future__ import annotations

import pytest

from planlib import load_store, validate
from tests.conftest import breakdown, deliverable

CHANGE = "run-homelab-through-sortie"


def no_openspec(*_):
    raise AssertionError("openspec should not be called")


def homelab(store, *delivered_by: str):
    store.plan([(CHANGE, [])])
    extra = {"delivered_by": list(delivered_by)} if delivered_by else {}
    store.change(CHANGE, breakdown(deliverable("homelab-image", **extra), deliverable("homelab-pilot", ["homelab-image"])))


def test_built_by_hand_validates(store):
    homelab(store, "ortusnova/homelab#85")
    r = validate(store.root, show=no_openspec)
    assert r.ok, r.errors
    assert r.stories[f"{CHANGE}/homelab-image"]["delivered_by"] == ["ortusnova/homelab#85"]
    assert r.stories[f"{CHANGE}/homelab-pilot"]["delivered_by"] == []
    # The graph is the contract with Sortie: it does not carry the field.
    node = next(n for n in r.graph["nodes"] if n["plan_key"] == f"{CHANGE}/homelab-image")
    assert "delivered_by" not in node


def test_several_pull_requests(store):
    homelab(store, "ortusnova/homelab#85", "ortusnova/agentic-orchestration#3", "some-org/repo.name_x#1")
    assert validate(store.root, show=no_openspec).ok


@pytest.mark.parametrize("value", ["homelab#85", "ortusnova/homelab", "ortusnova/homelab#0", "ortusnova/homelab#85x",
                                   "https://github.com/ortusnova/homelab/pull/85", "ortusnova/homelab/85", "-x/y#1"])
def test_not_a_pull_request(store, value):
    homelab(store, value)
    errs = load_store(store.root).errors
    assert len(errs) == 1, errs
    assert errs[0].code == "schema"
    assert errs[0].deliverable == "homelab-image"
    assert f"'{value}'" in errs[0].message and "delivered_by" in errs[0].message
    assert not validate(store.root, show=no_openspec).ok


def test_an_empty_list_is_refused(store):
    store.plan([(CHANGE, [])])
    store.change(CHANGE, breakdown(deliverable("homelab-image", delivered_by=[])))
    errs = load_store(store.root).errors
    assert len(errs) == 1 and errs[0].deliverable == "homelab-image" and "delivered_by" in errs[0].message


def test_naming_the_pull_request_keeps_the_approval(store):
    homelab(store)
    before = validate(store.root, show=no_openspec).stories[f"{CHANGE}/homelab-image"]["digest"]
    homelab(store, "ortusnova/homelab#85")
    r = validate(store.root, show=no_openspec, approvals={f"{CHANGE}/homelab-image": before},
                 frozen={f"{CHANGE}/homelab-image": before})
    assert r.ok, r.errors
    assert r.stories[f"{CHANGE}/homelab-image"]["digest"] == before
    node = next(n for n in r.graph["nodes"] if n["plan_key"] == f"{CHANGE}/homelab-image")
    assert node["approval"] == "approved"
