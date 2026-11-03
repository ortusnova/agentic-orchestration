"""A story the supervisor holds under a lease waits on the supervisor, not
on a person; once the lease runs out with no renewal, it waits on a person."""

from __future__ import annotations

from fastapi.testclient import TestClient

from tests.workbench.conftest import P, settle, sign_in
from tests.workbench.test_approvals import app_for
from tests.workbench.test_questions import home, parked_story
from workbench.home import execution_health
from workbench.leases import expired_text, leases_of, waiting_text

REASON = "full vm chain proof running"
EVIDENCE = "sandbox hl-ons-54-proof-1 on taichi"


def node(doc, key):
    return next(n for n in doc["nodes"] if n["plan_key"] == key)


def supervisor_waits(doc):
    return [x for w in doc["home"]["lanes"]["workers"] for x in w["waits"] if x["kind"] == "supervisor"]


def test_a_story_under_a_live_lease_waits_on_the_supervisor_and_asks_no_person(store):
    fake = parked_story(store)
    fake.hold("T-1", REASON, EVIDENCE)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        doc = client.get(f"{P}/graph").json()
        h = doc["home"]
        # Not a question for the operator, nor a reason to be stuck.
        assert h["questions"] == [] and h["needs_person"]["questions"] == 0 and h["expired_holds"] == []
        assert all(r["code"] not in ("question", "hold_expired") for r in h["health"]["reasons"])
        assert h["health"]["verdict"] != "stuck" and h["lanes"]["verdict"] != "stuck"
        assert all(not w["stuck"] for w in h["lanes"]["workers"])
        # The lanes say who it waits on, why, the evidence and the lease.
        [wait] = supervisor_waits(doc)
        assert wait["identifier"] == "T-1" and wait["reason"] == f"waiting on the supervisor: {REASON}"
        assert wait["lease"]["evidence"] == EVIDENCE and wait["lease"]["until"] is not None and wait["lease"]["renewed_at"] is not None
        # The story panel: waiting on the supervisor, no question to answer.
        b1 = node(doc, "billing/b1")
        assert b1["question"] is None and b1["lease"]["reason"] == REASON
        run = b1["run_state"]
        assert run["status"] == "waiting" and run["kind"] == "supervisor" and run["action"] is None
        assert run["why"] == f"waiting on the supervisor: {REASON}" and run["lease"]["evidence"] == EVIDENCE


def test_a_story_whose_lease_expired_is_stuck_for_a_person(store):
    fake = parked_story(store)
    fake.hold("T-1", REASON, EVIDENCE, expired=True)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        doc = client.get(f"{P}/graph").json()
        h = doc["home"]
        text = f"the supervisor's hold expired at 2026-11-03 14:00 UTC; last evidence: {EVIDENCE}"
        # Its own entry for a person, not the agent's old question.
        assert h["questions"] == []
        [held] = h["expired_holds"]
        assert held["key"] == "billing/b1" and held["text"] == text and held["rank"] == 1
        assert h["needs_person"]["expired_holds"] == 1 and h["needs_person"]["nearest"] == "billing/b1"
        assert h["health"]["verdict"] == "stuck"
        assert {"code": "hold_expired", "text": f"billing/b1: {text}", "key": "billing/b1"} in h["health"]["reasons"]
        assert h["lanes"]["verdict"] == "stuck"
        [stuck] = [s for w in h["lanes"]["workers"] for s in w["stuck"]]
        assert stuck["kind"] == "hold_expired" and stuck["reason"] == text and stuck["action"] == "story"
        run = node(doc, "billing/b1")["run_state"]
        assert run["status"] == "stuck" and run["kind"] == "hold_expired" and run["why"] == text
        assert node(doc, "billing/b1")["question"] is None


def test_a_story_released_from_its_lease_asks_its_question_again(store):
    fake = parked_story(store)
    fake.hold("T-1", REASON, EVIDENCE)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert home(app)["questions"] == []
        fake.extra_state["holds"] = []
        settle(client)
        assert [q["key"] for q in home(app)["questions"]] == ["billing/b1"]


def test_only_supervisor_waits_make_the_verdict_waiting_not_stuck():
    waits = [{"kind": "supervisor", "pr": None, "reason": REASON, "stories": [{"key": "billing/b1", "identifier": "T-1"}],
              "who": "the supervisor", "does": "proves it and releases it"}]
    state = {"running": [], "parked": [], "budget_exhausted": [], "config": {"agent": {"max_concurrent_agents": 2}}}
    health = execution_health(reachable=True, sortie_error=None, dispatched=True, project="T", state=state,
                              state_error=None, sync=None, runway=0, remaining=3, key_of={"T-1": "billing/b1"},
                              waits=waits)
    assert health["verdict"] == "waiting"
    assert health["reasons"] == [{"code": "supervisor", "key": None,
                                  "text": "1 story waits on 1 supervisor hold: the supervisor proves them"}]


def test_reading_leases_and_their_words():
    state = {"holds": [{"issue_identifier": "T-1", "by": "supervisor", "reason": REASON, "evidence": EVIDENCE,
                        "held_since": "2026-11-03T13:00:00Z", "renewed_at": "2026-11-03T13:30:00Z",
                        "until": "2026-11-03T14:30:00Z", "expired": False, "expired_at": None},
                       {"issue_identifier": "T-2", "by": "alice", "reason": "", "evidence": "pr #9", "until": "2026-11-03T14:00:00Z",
                        "expired": True, "expired_at": "2026-11-03T14:00:00Z"}, "junk"]}
    leases = leases_of([("p1", state, None), ("taichi", None, "down")])
    assert set(leases) == {"T-1", "T-2"} and leases["T-1"]["sortie"] == "p1"
    assert waiting_text(leases["T-1"]) == f"waiting on the supervisor: {REASON}"
    assert waiting_text(leases["T-2"]) == "waiting on alice"
    assert expired_text(leases["T-2"]) == "alice's hold expired at 2026-11-03 14:00 UTC; last evidence: pr #9"
