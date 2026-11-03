from __future__ import annotations

from planlib import story_digest, validate
from planlib.approval import deliverables_digest, legacy_approved
from tests.conftest import breakdown, deliverable


def no_openspec(*_):
    raise AssertionError("openspec should not be called")


def nodes(result):
    return {n["plan_key"]: n for n in result.graph["nodes"]}


def billing(store, *extra):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("a"), deliverable("b", ["a"]), deliverable("c"), *extra))


def digests(store, *keys):
    r = validate(store.root, show=no_openspec)
    return {k: r.stories[k]["digest"] for k in keys}


def test_without_approvals_every_story_is_draft(store):
    billing(store)
    n = nodes(validate(store.root, show=no_openspec))
    assert {n[k]["approval"] for k in ("billing/a", "billing/b", "billing/c")} == {"draft"}
    assert n["billing"]["approval"] == "draft"


def test_a_status_in_the_file_approves_nothing(store):
    store.plan([("billing", [])])
    doc = breakdown(deliverable("a"))
    doc.update(status="approved", approved_digest=deliverables_digest(doc))
    store.change("billing", doc)
    assert nodes(validate(store.root, show=no_openspec))["billing/a"]["approval"] == "draft"


def test_batch_0_then_one_more(store):
    billing(store)
    approved = digests(store, "billing/a", "billing/b", "billing/c")
    billing(store, deliverable("d", ["c"]))
    n = nodes(validate(store.root, show=no_openspec, approvals=approved))
    assert [n[k]["approval"] for k in ("billing/a", "billing/b", "billing/c", "billing/d")] == ["approved"] * 3 + ["draft"]
    assert n["billing"]["approval"] == "approved"

    approved.update(digests(store, "billing/d"))
    assert nodes(validate(store.root, show=no_openspec, approvals=approved))["billing/d"]["approval"] == "approved"


def test_one_story_edited(store):
    billing(store)
    approved = digests(store, "billing/a", "billing/b", "billing/c")
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("a"), deliverable("b", ["a"], acceptance_criteria=["changed"]), deliverable("c")))
    n = nodes(validate(store.root, show=no_openspec, approvals=approved))
    assert n["billing/b"]["approval"] == "draft"
    assert n["billing/a"]["approval"] == n["billing/c"]["approval"] == "approved"


def test_the_digest_covers_every_field():
    base = deliverable("x")
    assert story_digest(base) == story_digest(dict(reversed(list(base.items()))))
    for field, value in [("title", "Other"), ("review", "criteria"), ("supersedes", "y"), ("notes", "n"), ("depends_on", ["z"])]:
        assert story_digest({**base, field: value}) != story_digest(base), field


def test_review_defaults_to_human_and_reaches_the_graph(store):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("a"), deliverable("b", review="criteria")))
    r = validate(store.root, show=no_openspec)
    n = nodes(r)
    assert n["billing/a"]["review"] == "human" and n["billing/b"]["review"] == "criteria"
    assert r.stories["billing/b"]["review"] == "criteria"


def test_legacy_approval_is_recognized_for_import():
    doc = breakdown(deliverable("a"))
    assert not legacy_approved(doc)
    doc.update(status="approved", approved_digest=deliverables_digest(doc))
    assert legacy_approved(doc)
    doc["deliverables"].append(deliverable("b"))
    assert not legacy_approved(doc)


def test_review_ui_reaches_the_graph_and_another_value_is_refused(store):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("u", review="ui")))
    r = validate(store.root, show=no_openspec)
    assert r.ok, r.errors
    assert nodes(r)["billing/u"]["review"] == "ui" and r.stories["billing/u"]["review"] == "ui"
    # A ui story is not approved by anyone: it reads as a draft.
    assert nodes(r)["billing/u"]["approval"] == "draft"

    store.change("billing", breakdown(deliverable("u", review="preview")))
    r = validate(store.root, show=no_openspec)
    assert not r.ok and any("preview" in str(e) for e in r.errors)
