"""What each released story is doing (agent-platform/workbench-home): the list's
rows, their states and their order."""

from __future__ import annotations

from workbench.released import ReleaseOrder, released_list

STATES = {"release": "Todo", "backlog": "Backlog", "cancel": "Canceled", "done": "Done"}


def story(key, ident, state="Todo", runtime=None, terminal=False, pr=None, status="synced"):
    return {"plan_key": key, "kind": "story", "change": key.split("/")[0], "title": f"Do {key}", "status": status,
            "tracker": {"identifier": ident, "state": state, "terminal": terminal, "url": f"https://tracker/{ident}", "pr": pr},
            "runtime": runtime}


def rows(nodes, state=None, questions=None, order=None, settled=()):
    return released_list(nodes, STATES, {}, set(settled), state, questions, order)


def by_key(nodes, **kw):
    return {r["key"]: r for r in rows(nodes, **kw)}


def test_only_stories_released_and_not_done_are_listed():
    nodes = [
        story("ons/a", "ONS-1", "Backlog"),
        story("ons/b", "ONS-2"),
        story("ons/c", "ONS-3", "Done", terminal=True),
        story("ons/d", "ONS-4", "Canceled", terminal=True),
        story("ons/e", "ONS-5", "In Progress"),
        # A story the store no longer holds is still Sortie's to run.
        story("ons/f", "ONS-6", "Todo", status="orphaned"),
        {"plan_key": "ons", "kind": "epic", "change": "ons", "tracker": {"identifier": "ONS-0", "state": "Todo"}, "runtime": None},
        {"plan_key": "ons/g", "kind": "story", "change": "ons", "status": "draft", "tracker": None, "runtime": None},
    ]
    assert [r["key"] for r in rows(nodes)] == ["ons/b", "ons/e", "ons/f"]
    # A story the home counts done, delivered by its pull requests, leaves the list too.
    assert [r["key"] for r in rows(nodes, settled={"ons/e"})] == ["ons/b", "ons/f"]


def test_waiting_for_a_slot():
    r = by_key([story("ons/a", "ONS-1")])["ons/a"]
    assert r["state"] == "waiting" and r["identifier"] == "ONS-1" and r["url"] == "https://tracker/ONS-1"


def test_working_with_the_turn_and_when_it_started():
    state = {"running": [{"issue_identifier": "ONS-1", "turn_count": 3, "started_at": "2026-11-02T10:00:00Z", "held": False}]}
    r = by_key([story("ons/a", "ONS-1", "In Progress", runtime={"status": "running", "turn_count": 3})], state=state)["ons/a"]
    assert r["state"] == "working" and r["turn"] == 3 and r["started_at"] == 1793613600.0


def test_starting_before_the_first_turn():
    state = {"running": [{"issue_identifier": "ONS-1", "turn_count": 0, "held": False}]}
    r = by_key([story("ons/a", "ONS-1", runtime={"status": "running", "turn_count": 0})], state=state)["ons/a"]
    assert r["state"] == "starting" and r["attempt"] == 1 and r["error"] is None


def test_a_retry_is_starting_with_its_attempt_and_last_error():
    state = {"retrying": [{"issue_identifier": "ONS-11", "attempt": 2, "error": "response_timeout"}]}
    r = by_key([story("ons/b", "ONS-11", runtime={"status": "retrying", "attempt": 2})], state=state)["ons/b"]
    assert (r["state"], r["attempt"], r["error"]) == ("starting", 2, "response_timeout")
    # Without Sortie's state, the plan graph's runtime still says it retries.
    r = by_key([story("ons/b", "ONS-11", runtime={"status": "retrying", "attempt": 2})])["ons/b"]
    assert (r["state"], r["attempt"], r["error"]) == ("starting", 2, None)


def test_in_review_with_its_pull_request():
    r = by_key([story("hl/x", "ONS-23", "In Review", pr="ortusnova/homelab#97")])["hl/x"]
    assert r["state"] == "in_review"
    assert r["pr"] == {"ref": "ortusnova/homelab#97", "url": "https://github.com/ortusnova/homelab/pull/97"}
    # A review state with no pull request Sortie knows of still reads in review.
    assert by_key([story("hl/x", "ONS-23", "In Review")])["hl/x"]["state"] == "in_review"


def test_needs_you_with_the_question():
    state = {"parked": [{"issue_identifier": "ONS-1", "statement": "Which repo…"}]}
    r = by_key([story("ons/a", "ONS-1", "In Progress")], state=state)["ons/a"]
    assert r["state"] == "needs_you" and r["question"] == "Which repo…"
    # The whole statement, when the hub read it.
    r = by_key([story("ons/a", "ONS-1", "In Progress")], state=state,
               questions={"ONS-1": {"statement": "Which repository should the daemon live in?"}})["ons/a"]
    assert r["question"] == "Which repository should the daemon live in?"


def test_held_with_the_reason():
    state = {"running": [{"issue_identifier": "ONS-1", "turn_count": 4, "held": True, "hold_reason": "permission_request"}],
             "budget_exhausted": [{"issue_identifier": "ONS-2", "reason": "token_budget"}]}
    rs = by_key([story("ons/a", "ONS-1", "In Progress", runtime={"status": "running"}),
                 story("ons/b", "ONS-2", "In Progress", runtime={"status": "budget_held"})], state=state)
    assert (rs["ons/a"]["state"], rs["ons/a"]["reason"]) == ("held", "permission request")
    assert (rs["ons/b"]["state"], rs["ons/b"]["reason"]) == ("held", "token budget")


def test_the_list_keeps_the_release_order():
    order = ReleaseOrder()
    nodes = [story("ons/a", "ONS-10"), story("ons/b", "ONS-11"), story("ons/c", "ONS-12", "Backlog")]
    first = order.update(["ons/a", "ons/b"])
    assert [r["key"] for r in rows(nodes, order=first)] == ["ons/a", "ons/b"]
    # ONS-12 is released after the others: it goes last, whatever its number.
    nodes = [story("ons/c", "ONS-9"), *nodes[:2]]
    then = order.update(["ons/c", "ons/a", "ons/b"])
    assert [r["key"] for r in rows(nodes, order=then)] == ["ons/a", "ons/b", "ons/c"]
    # Pulled back and released again, a story goes to the end.
    order.update(["ons/b", "ons/c"])
    again = order.update(["ons/c", "ons/a", "ons/b"])
    assert [r["key"] for r in rows(nodes, order=again)] == ["ons/b", "ons/c", "ons/a"]


def test_scenario_a_retry_keeps_its_place():
    """ONS-10, ONS-11 and ONS-13 are released, and ONS-11's session start times out."""
    order = ReleaseOrder()
    nodes = [story("ons/s10", "ONS-10"), story("ons/s11", "ONS-11"), story("ons/s13", "ONS-13")]
    order.update(["ons/s10", "ons/s11", "ons/s13"])
    nodes[0].update(runtime={"status": "running", "turn_count": 2})
    nodes[0]["tracker"]["state"] = "In Progress"
    nodes[1].update(runtime={"status": "retrying", "attempt": 2})
    state = {"running": [{"issue_identifier": "ONS-10", "turn_count": 2, "held": False}],
             "retrying": [{"issue_identifier": "ONS-11", "attempt": 2, "error": "response_timeout"}]}
    out = rows(nodes, state=state, order=order.update(["ons/s10", "ons/s11", "ons/s13"]))
    assert [r["identifier"] for r in out] == ["ONS-10", "ONS-11", "ONS-13"]
    assert [r["state"] for r in out] == ["working", "starting", "waiting"]
    assert (out[1]["attempt"], out[1]["error"]) == (2, "response_timeout")
