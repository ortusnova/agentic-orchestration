"""Every stuck or waiting lane has an action that resolves it in place
(agent-platform/run-supervision: Unblock from the lane). The answer, the
session grant, the session and the last run each go to the Sortie that holds
the story: this host's or another's."""

from __future__ import annotations

from fastapi.testclient import TestClient

from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import STATES, billing, status
from tests.workbench.test_watch import validate_offline
from tests.workbench.test_workers import TOKEN, FakeBrokers, update
from workbench.app import create_app


def two(store, fake: FakeSortie, other: FakeSortie, brokers: FakeBrokers | None = None):
    """p1's Sortie serves the plan; taichi's runs some of its stories."""
    billing(store)
    fake.plan_states = STATES
    for key in ("billing/b1", "billing/b2", "accounts/a3"):
        fake.issue(key, state="In Progress")
    config = make_config(store.root, sortie_name="p1")
    services = {"broker_transport": brokers.transport()} if brokers else {}
    return create_app(config, validate=validate_offline, sortie=fake.client(), sorties={"taichi": other.client()}, **services)


def lane_of(home, worker, ident):
    w = next(w for w in home["workers"] if w["name"] == worker)
    return next((lane for lane in w["lanes"] if (lane.get("job") or {}).get("identifier") == ident), None)


def test_an_answer_goes_to_the_sortie_that_parked_the_story(store):
    fake, other = FakeSortie(), FakeSortie(token="taichi-token")
    other.park("T-1", "Is the bound per check, or per phase?\n\nSay which, and why.")
    app = two(store, fake, other)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        node = status(app, "billing/b1")
        assert node["sortie"] == "taichi"
        assert node["question"]["statement"].startswith("Is the bound per check") and node["question"]["sortie"] == "taichi"
        home = client.get(f"{P}/home").json()
        assert [q["key"] for q in home["questions"]] == ["billing/b1"]
        lane = lane_of(home, "taichi", "T-1")
        assert lane["stuck"]["code"] == "question"
        assert lane["job"]["question"]["statement"] == "Is the bound per check, or per phase?\n\nSay which, and why."

        r = client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "Per check: record it in design.md."})
        assert r.status_code == 200, r.text
        assert other.answers == [{"identifier": "T-1", "text": "Per check: record it in design.md.", "author": "Local operator"}]
        assert fake.answers == [] and other.refreshes == 1
        # Released: the question leaves at once, and the next poll shows it running.
        assert status(app, "billing/b1")["question"] is None
        other.run("T-1", "s2", stage="build")
        settle(client)
        assert lane_of(client.get(f"{P}/home").json(), "taichi", "T-1")["kind"] == "job"


def test_more_sessions_release_a_story_held_at_its_budget(store):
    fake, other = FakeSortie(), FakeSortie(token="taichi-token")
    fake.budget_exhausted = [{"issue_identifier": "T-1", "reason": "session_budget", "used_sessions": 16, "budget_sessions": 16}]
    app = two(store, fake, other)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        home = client.get(f"{P}/home").json()
        p1 = home["workers"][0]
        assert p1["can_grant"] is True
        assert ("GET", "/api/v1/T-1/sessions", None) in fake.requests
        lane = lane_of(home, "p1", "T-1")
        assert lane["stuck"]["code"] == "budget" and lane["job"]["budget"] == {"reason": "session_budget", "used": 16, "sessions": 16}

        r = client.post(f"{P}/workers/T-1/sessions", json={"add": 4, "reason": "finish the review"})
        assert r.status_code == 200, r.text
        assert fake.grants == [{"identifier": "T-1", "add": 4, "by": "Local operator", "reason": "finish the review"}]
        assert other.grants == [] and fake.refreshes >= 1
        assert lane_of(client.get(f"{P}/home").json(), "p1", "T-1") is None
        assert client.post(f"{P}/workers/T-1/sessions", json={"add": 0}).status_code == 422


def test_a_sortie_without_the_grant_route_says_so_and_names_the_setting(store):
    fake, other = FakeSortie(), FakeSortie(token="taichi-token")
    fake.grant_supported = False
    fake.max_sessions = 16
    fake.budget_exhausted = [{"issue_identifier": "T-1", "reason": "session_budget", "used_sessions": 16, "budget_sessions": 16}]
    app = two(store, fake, other)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert client.get(f"{P}/home").json()["workers"][0]["can_grant"] is False
        r = client.post(f"{P}/workers/T-1/sessions", json={"add": 4})
        assert r.status_code == 501
        err = r.json()["error"]
        assert err["code"] == "grant_unsupported"
        assert err["message"] == "p1's Sortie cannot grant sessions yet. Raise agent.max_sessions (now 16) in p1's workflow instead."
        assert fake.grants == []


def test_the_run_state_says_why_a_story_waits_and_what_releases_it(store):
    fake, other = FakeSortie(), FakeSortie(token="taichi-token")
    fake.budget_exhausted = [{"issue_identifier": "T-1", "reason": "session_budget", "used_sessions": 16, "budget_sessions": 16}]
    fake.runs = [{"issue_identifier": "T-1", "dispatch_id": "D1", "status": "cancelled", "rule_name": "review",
                  "started_at": "2026-11-02T17:05:06Z", "completed_at": "2026-11-02T17:22:44Z"}]
    fake.run_events["D1"] = [{"seq": 1, "at": "2026-11-02T17:05:08Z", "event": "notification", "message": "The review passes.", "text": "The review passes."}]
    other.park("T-2", "Which repository?")
    other.pending_reactions = [{"issue_identifier": "T-3", "kind": "ci", "created_at": "2026-11-02T17:00:00Z",
                                "pull_request": {"number": 31, "url": "https://github.com/o/r/pull/31", "repository": "o/r"},
                                "last_check": {"status": "failing"}}]
    other.held = [{"issue_identifier": "T-3", "hold": {"reason": "blocked_by", "blockers": [{"issue_identifier": "T-9", "state": "Todo", "reason": "not_done"}],
                                                       "reason_text": "Blocked by T-9", "release_text": "Released when the blocker is done."}}]
    app = two(store, fake, other)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        budget = client.get(f"{P}/workers/T-1/run").json()
        assert budget["sortie"] == "p1" and budget["status"] == "budget" and budget["action"] == "grant"
        assert budget["why"] == ["It used 16 of its 16 sessions, so Sortie starts no more."]
        assert budget["runs"][0]["dispatch_id"] == "D1" and budget["can_grant"] is True
        events = client.get(f"{P}/workers/T-1/runs/D1/events").json()["events"]
        assert events[0]["text"] == "The review passes."
        assert client.get(f"{P}/workers/T-2/runs/D1/events").status_code == 404

        parked = client.get(f"{P}/workers/T-2/run").json()
        assert parked["sortie"] == "taichi" and parked["status"] == "parked" and parked["action"] == "answer"
        assert parked["question"]["statement"] == "Which repository?"

        reaction = client.get(f"{P}/workers/T-3/run").json()
        assert reaction["status"] == "reaction" and reaction["action"] == "open_pr"
        assert reaction["pulls"] == [{"number": 31, "url": "https://github.com/o/r/pull/31", "repository": "o/r",
                                      "waits": "#31 waits on CI, which fails", "ready": False}]
        other.pending_reactions = []
        settle(client)
        blocked = client.get(f"{P}/workers/T-3/run").json()
        assert blocked["status"] == "blocked" and blocked["action"] == "blockers"
        assert blocked["blockers"] == [{"identifier": "T-9", "state": "Todo", "reason": "not done"}]
        assert blocked["releases"] == "Released when the blocker is done."


def test_a_job_on_another_sortie_is_watched_and_steered_through_it(store):
    fake, other, brokers = FakeSortie(), FakeSortie(token="taichi-token"), FakeBrokers()
    app = two(store, fake, other, brokers)
    other.run("T-1", "s1", stage="review", broker=brokers.add("taichi-broker.test", TOKEN, [update(1, "reading the plan")]), token=TOKEN)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        rt = status(app, "billing/b1")["runtime"]
        assert rt["status"] == "running" and rt["watchable"] is True and rt["sortie"] == "taichi"
        r = client.post(f"{P}/workers/T-1/commands", json={"op": "steer", "text": "record the bound per check"})
        assert r.status_code == 200, r.text
        assert brokers.commands == [("taichi-broker.test", {"op": "steer", "text": "record the bound per check"})]
        assert ("GET", "/api/v1/T-1/broker", None) in other.requests
        assert not any(path == "/api/v1/T-1/broker" for _, path, _ in fake.requests)
        project(app).relays["taichi"].follow_poll = 0.01
        brokers.brokers["taichi-broker.test"]["after"] = lambda: other.end("T-1")
        assert "reading the plan" in client.get(f"{P}/workers/T-1/events").text
