from __future__ import annotations

import json

from planlib import milestones as ms
from planlib.validate import validate
from tests.conftest import FIXTURES, breakdown, deliverable, milestone
from tests.planlib.test_checks import codes, no_openspec
from tests.planlib.test_load import STORE_SCHEMAS


def billing_store(store):
    store.plan([("accounts", []), ("billing", ["accounts"])])
    store.change("accounts", breakdown(deliverable("close")))
    store.change("billing", breakdown(deliverable("invoice"), deliverable("tax", ["invoice"])))


def check(store):
    return validate(store.root, show=no_openspec)


# 1.1 The milestones file -----------------------------------------------------


def test_schema_ships_identically_in_the_template_and_the_fixtures():
    shipped = STORE_SCHEMAS / "milestones.schema.json"
    assert shipped.read_bytes() == (FIXTURES / "schemas" / "milestones.schema.json").read_bytes()
    assert json.loads(shipped.read_text())["properties"]["schema_version"]["const"] == 1


def test_a_milestone_collects_a_change_and_a_story(store):
    billing_store(store)
    store.milestones([milestone("m1", [{"change": "billing"}, {"story": "accounts/close"}])])
    r = check(store)
    assert r.errors == []
    assert r.milestones_declared is True
    out = ms.progress(r.milestones, r.graph, r.tasks, done=set())
    assert out["milestones"][0]["stories"] == ["billing/invoice", "billing/tax", "accounts/close"]


def test_an_unknown_field_names_the_milestone_and_the_field(store):
    billing_store(store)
    store.milestones([milestone("m1", [{"change": "billing"}], owner="ann")])
    r = check(store)
    assert codes(r) == ["schema"]
    assert "m1" in r.errors[0].message and "owner" in r.errors[0].message
    assert r.errors[0].members == ("m1",)


def test_unknown_change_story_gate_and_dependency(store):
    billing_store(store)
    store.milestones([milestone("m1", [{"change": "shipping"}, {"story": "billing/refund"}], depends_on=["m0"], gate="billing/nope")])
    r = check(store)
    assert codes(r) == ["unknown_change", "unknown_milestone", "unknown_story", "unknown_story"]
    assert all("m1" in e.members for e in r.errors)


def test_a_cycle_names_both(store):
    billing_store(store)
    store.milestones([milestone("m1", [{"change": "billing"}], ["m2"]), milestone("m2", [{"change": "accounts"}], ["m1"])])
    r = check(store)
    assert codes(r) == ["milestone_cycle"]
    assert set(r.errors[0].members) == {"m1", "m2"}


def test_two_goals_name_both(store):
    billing_store(store)
    store.milestones([milestone("m1", [{"change": "accounts"}]), milestone("m2", [{"change": "billing"}], goal=True),
                      milestone("m3", [{"change": "billing", "tasks": "metal"}], goal=True)])
    r = check(store)
    assert codes(r) == ["several_goals"]
    assert r.errors[0].members == ("m2", "m3")


def test_duplicate_ids(store):
    billing_store(store)
    store.milestones([milestone("m1", [{"change": "accounts"}]), milestone("m1", [{"change": "billing"}])])
    assert codes(check(store)) == ["duplicate_milestone"]


def test_a_store_without_the_file_validates_as_before(store):
    billing_store(store)
    r = check(store)
    assert r.errors == [] and r.milestones_declared is False
    assert [m["id"] for m in r.milestones] == [ms.IMPLICIT_ID]
    assert r.milestones[0]["goal"] is True
    assert [i["change"] for i in r.milestones[0]["includes"]] == ["accounts", "billing"]


def test_a_store_without_the_schema_still_validates_without_the_file(store):
    (store.root / "openspec" / "schemas" / "planning" / "milestones.schema.json").unlink()
    billing_store(store)
    assert check(store).errors == []


def test_the_file_without_its_schema_is_reported(store):
    (store.root / "openspec" / "schemas" / "planning" / "milestones.schema.json").unlink()
    billing_store(store)
    store.milestones([milestone("m1", [{"change": "billing"}])])
    assert codes(check(store)) == ["missing_schema"]


def test_a_newer_version_is_refused(store):
    billing_store(store)
    store.milestones({"schema_version": 2, "milestones": []})
    assert codes(check(store)) == ["unsupported_version"]


# 1.2 Task kinds --------------------------------------------------------------


def test_three_kinds():
    tasks = ms.parse_tasks("## 1. Rules\n\n- [x] 1.1 Add rules.\n- [ ] 1.2 Apply rules on metal. [metal]\n"
                           "- [ ] 1.3 Enroll the key. [operator]\n", "p3")
    assert [(t["number"], t["kind"], t["done"]) for t in tasks] == [("1.1", "agent", True), ("1.2", "metal", False),
                                                                     ("1.3", "operator", False)]
    assert tasks[2]["text"] == "Enroll the key."
    assert tasks[2]["id"] == "p3#1.3"
    assert ms.task_counts(tasks) == {"agent": {"done": 1, "total": 1}, "metal": {"done": 0, "total": 1},
                                     "operator": {"done": 0, "total": 1}}


def test_ticked_versus_open_and_odd_lines():
    tasks = ms.parse_tasks("- [X] Done in caps\n  - [ ] nested [metal] \n- [ ] tag mid [metal] line\nnot - [ ] an item\n")
    assert [(t["kind"], t["done"], t["number"]) for t in tasks] == [("agent", True, None), ("metal", False, None),
                                                                     ("agent", False, None)]


def test_tasks_are_read_for_each_planned_change(store):
    billing_store(store)
    store.tasks("billing", "- [x] 1.1 a\n- [ ] 1.2 b [metal]\n")
    r = check(store)
    assert [t["kind"] for t in r.tasks["billing"]] == ["agent", "metal"]
    assert r.tasks["accounts"] == []


# 1.3 Membership, progress, the goal, the next milestone, the critical path ---


def three_chain(store):
    store.plan([("work", [])])
    store.change("work", breakdown(deliverable("a"), deliverable("b", ["a"]), deliverable("g", ["b"]), deliverable("c")))


def test_no_goal_declared_means_the_last_is_the_goal(store):
    three_chain(store)
    store.milestones([milestone("m3", [{"story": "work/c"}], ["m2"]), milestone("m1", [{"story": "work/a"}]),
                      milestone("m2", [{"story": "work/b"}], ["m1"])])
    r = check(store)
    assert ms.goal(r.milestones) == "m3"
    assert [m["id"] for m in ms.order(r.milestones)] == ["m1", "m2", "m3"]


def test_a_change_finished_by_hand(store):
    store.plan([("move-plan", [])])
    store.change("move-plan", breakdown(*(deliverable(f"s{i}") for i in range(5))))
    store.tasks("move-plan", "".join(f"- [x] 1.{i} step\n" for i in range(12)))
    store.milestones([milestone("m0", [{"change": "move-plan"}])])
    r = check(store)
    m0 = ms.progress(r.milestones, r.graph, r.tasks, done=set())["milestones"][0]
    assert m0["progress"]["stories"] == {"done": 0, "total": 5}
    assert m0["progress"]["agent_tasks"] == {"done": 12, "total": 12}
    assert m0["progress"]["person_tasks"] == {"done": 0, "total": 0}


def test_a_milestone_declared_done_keeps_its_counts(store):
    store.plan([("move-plan", []), ("next", [])])
    store.change("move-plan", breakdown(*(deliverable(f"s{i}") for i in range(5))))
    store.change("next", breakdown(deliverable("n")))
    store.tasks("move-plan", "- [x] 1.1 step\n")
    store.milestones([milestone("m0", [{"change": "move-plan"}], done=True), milestone("m1", [{"change": "next"}], ["m0"])])
    r = check(store)
    assert r.errors == []
    out = ms.progress(r.milestones, r.graph, r.tasks, done=set())
    m0 = out["milestones"][0]
    assert m0["done"] is True and m0["progress"]["stories"] == {"done": 0, "total": 5}
    assert out["next"] == "m1" and out["critical_path"] == ["next/n"]


def test_one_change_in_two_milestones(store):
    store.plan([("p3", [])])
    store.change("p3", breakdown(deliverable("vm")))
    store.tasks("p3", "- [x] 1.1 vm rules\n- [ ] 1.2 metal rules [metal]\n- [ ] 1.3 key [operator]\n")
    store.milestones([milestone("m1", [{"change": "p3", "tasks": "agent"}]),
                      milestone("m2", [{"change": "p3", "tasks": "metal"}], ["m1"])])
    r = check(store)
    m1, m2 = ms.progress(r.milestones, r.graph, r.tasks, done=set())["milestones"]
    assert [t["number"] for t in m1["tasks"]] == ["1.1"] and m1["stories"] == ["p3/vm"]
    assert [t["number"] for t in m2["tasks"]] == ["1.2"] and m2["stories"] == []
    assert m2["progress"]["person_tasks"] == {"done": 0, "total": 1}
    assert m2["progress"]["metal_tasks"] == {"done": 0, "total": 1}


def test_done_by_gate_or_by_contents(store):
    three_chain(store)
    store.tasks("work", "- [x] 1.1 a\n")
    store.milestones([milestone("m1", [{"story": "work/a"}, {"story": "work/b"}], gate="work/g"),
                      milestone("m2", [{"change": "work"}], ["m1"])])
    r = check(store)
    out = ms.progress(r.milestones, r.graph, r.tasks, done={"work/g"})
    assert [m["done"] for m in out["milestones"]] == [True, False]
    assert out["next"] == "m2"
    out = ms.progress(r.milestones, r.graph, r.tasks, done={"work/a", "work/b", "work/g", "work/c"})
    assert [m["done"] for m in out["milestones"]] == [True, True]
    assert out["next"] is None and out["critical_path"] == []


def test_a_chain_to_the_gate(store):
    three_chain(store)
    store.milestones([milestone("m1", [{"change": "work"}], gate="work/g")])
    r = check(store)
    out = ms.progress(r.milestones, r.graph, r.tasks, done=set())
    assert out["next"] == "m1"
    assert out["critical_path"] == ["work/a", "work/b", "work/g"]


def test_without_a_gate_the_chain_ends_at_a_last_story_and_ties_break_by_plan_order(store):
    store.plan([("work", [])])
    store.change("work", breakdown(deliverable("x"), deliverable("y"), deliverable("x2", ["x"]), deliverable("y2", ["y"])))
    store.milestones([milestone("m1", [{"change": "work"}])])
    r = check(store)
    assert ms.progress(r.milestones, r.graph, r.tasks, done=set())["critical_path"] == ["work/x", "work/x2"]
    # A done story drops out of the chain; an estimate lengthens a story.
    assert ms.progress(r.milestones, r.graph, r.tasks, done={"work/x"})["critical_path"] == ["work/y", "work/y2"]
    assert ms.progress(r.milestones, r.graph, r.tasks, done=set(), estimates={"work/y": 3})["critical_path"] == ["work/y", "work/y2"]


def test_cancelled_stories_count_toward_nothing(store):
    three_chain(store)
    store.milestones([milestone("m1", [{"change": "work"}])])
    r = check(store)
    m1 = ms.progress(r.milestones, r.graph, r.tasks, done={"work/a"}, dropped={"work/c"})["milestones"][0]
    assert m1["progress"]["stories"] == {"done": 1, "total": 3}


def test_the_cli_reports_the_milestones(store, capsys):
    from planlib.cli import main

    three_chain(store)
    store.milestones([milestone("m1", [{"change": "work"}])])
    assert main(["validate", "--store", str(store.root)]) == 0
    assert "1 milestone(s)" in capsys.readouterr().out


def test_example_store_file_is_valid_against_the_schema():
    from jsonschema import Draft202012Validator
    import yaml

    schema = json.loads((STORE_SCHEMAS / "milestones.schema.json").read_text())
    doc = yaml.safe_load((STORE_SCHEMAS / "examples" / "milestones.yaml").read_text())
    assert list(Draft202012Validator(schema).iter_errors(doc)) == []


def route_store(store):
    """The pilot proves the route; two later changes wait on its change in
    plan.yaml, not on its stories; the gate comes after them."""
    store.plan([("route", []), ("later", ["route"]), ("other", ["route"]), ("gate", ["later", "other"])])
    store.change("route", breakdown(deliverable("image"), deliverable("pilot", ["image"]), deliverable("after-pilot", ["pilot"])))
    store.change("later", breakdown(deliverable("l1"), deliverable("l2", ["l1"])))
    store.change("other", breakdown(deliverable("o1")))
    store.change("gate", breakdown(deliverable("g", ["later/l2", "other/o1"])))
    store.milestones([milestone("m1", [{"change": "route"}, {"change": "later"}, {"change": "other"}, {"change": "gate"}],
                                gate="gate/g")])
    return check(store)


def test_the_order_between_changes_is_a_dependency_of_their_stories(store):
    r = route_store(store)
    blocks = set(ms.story_blocks(r.graph))
    # A later change starts after the change before it ends.
    assert ("route/after-pilot", "later/l1") in blocks and ("route/after-pilot", "other/o1") in blocks
    # Only the ends meet the starts: no edge from a story the change still continues.
    assert ("route/pilot", "later/l1") not in blocks and ("route/after-pilot", "later/l2") not in blocks
    out = ms.progress(r.milestones, r.graph, r.tasks, done={"route/image"})
    assert out["critical_path"] == ["route/pilot", "route/after-pilot", "later/l1", "later/l2", "gate/g"]


# What a milestone waits on (agent-platform/workbench-home) -----------------------


def waits_store(store):
    """m1's gate needs feed/b, which needs feed/a; feed is in no milestone."""
    store.plan([("feed", []), ("work", ["feed"]), ("next", ["feed"])])
    store.change("feed", breakdown(deliverable("a"), deliverable("b", ["a"]), deliverable("c", ["b"])))
    store.change("work", breakdown(deliverable("g", ["feed/b"])))
    store.change("next", breakdown(deliverable("n", ["feed/c"])))


def test_a_milestone_counts_the_stories_it_waits_on_once(store):
    waits_store(store)
    store.milestones([milestone("m1", [{"change": "work"}], gate="work/g"),
                      milestone("m2", [{"change": "next"}, {"change": "feed"}], ["m1"])])
    r = check(store)
    assert r.errors == []
    m1, m2 = ms.progress(r.milestones, r.graph, r.tasks, done={"feed/a"})["milestones"]
    assert m1["stories"] == ["work/g", "feed/a", "feed/b"] and m1["waits_on"] == ["feed/a", "feed/b"]
    assert m1["progress"]["stories"] == {"done": 1, "total": 3}
    # m2 includes feed, but m1 counts what it waits on: m2 keeps feed/c only.
    assert m2["stories"] == ["next/n", "feed/c"] and m2["waits_on"] == []


def test_a_cancelled_story_and_another_milestones_gate_are_not_waited_on(store):
    waits_store(store)
    store.milestones([milestone("m1", [{"change": "work"}]),
                      milestone("m2", [{"change": "next"}], ["m1"], gate="feed/b")])
    r = check(store)
    assert r.errors == []
    m1, m2 = ms.progress(r.milestones, r.graph, r.tasks, done=set())["milestones"]
    # feed/b is m2's gate, so it stays there; m1 still waits on feed/a, through it.
    assert m1["waits_on"] == ["feed/a"] and "feed/b" in m2["stories"]
    m1, m2 = ms.progress(r.milestones, r.graph, r.tasks, done=set(), dropped={"feed/b"})["milestones"]
    assert m1["waits_on"] == [] and m1["stories"] == ["work/g"]
    assert m2["waits_on"] == ["feed/c"]
