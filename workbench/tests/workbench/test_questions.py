"""The questions agents ask people (agent-platform/agent-questions): the
workbench shows every question and takes the answer, through Sortie."""

from __future__ import annotations

import json
from datetime import datetime

from fastapi.testclient import TestClient

from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_oidc import CLIENT_ID, CLIENT_SECRET, ISSUER, REDIRECT, FakeProvider
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import app_for, billing, status
from tests.workbench.test_identity import sign_in_oidc
from tests.workbench.test_watch import validate_offline
from workbench.app import create_app
from workbench.oidc import OIDCClient
from workbench.questions import home_questions, parked_at_of, question, statement_of

STATEMENT = ("ONS-25 was dispatched into ortusnova/sortie, but its acceptance criteria say the worker daemon lives in "
             "agentic-orchestration.\n\nPlease either record agentic-orchestration on ONS-25 and re-dispatch it, or "
             "confirm that the daemon should be built in ortusnova/sortie instead.")
PARKED_AT = "2026-11-01T18:35:26Z"


def parked_story(store):
    """billing/b1 is T-1, and its agent stopped with STATEMENT; the state
    lists the statement shortened."""
    billing(store)
    fake = FakeSortie()
    fake.issue("billing/b1", state="In Progress")
    fake.issue("billing/b2")
    fake.park("T-1", STATEMENT, PARKED_AT, listed=STATEMENT[:40] + "…")
    return fake


def home(app):
    return project(app).hub.home


# Scenario: A question on the home page ---------------------------------------------


def test_a_parked_story_is_first_on_the_home_with_its_whole_statement(store):
    fake = parked_story(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)  # one refresh
        doc = client.get(f"{P}/graph").json()
        qs = doc["home"]["questions"]
        assert len(qs) == 1
        q = qs[0]
        assert q["key"] == "billing/b1" and q["identifier"] == "T-1" and q["title"] == status(app, "billing/b1")["title"]
        # The whole statement, from the issue's own response, not the state's shortened one.
        assert q["statement"] == STATEMENT
        assert q["parked_at"] == datetime.fromisoformat("2026-11-01T18:35:26+00:00").timestamp()
        assert q["rank"] == 1
        # First of everything that needs a person: every other item ranks after it.
        needs = doc["home"]["needs_person"]
        assert needs["questions"] == 1 and needs["nearest"] == "billing/b1"
        ranks = sorted(i["rank"] for i in doc["home"]["not_ready"] if i["column"])
        assert ranks == list(range(2, 2 + len(ranks)))
        assert needs["total"] == 1 + len(ranks)
        # The story's card shows the same.
        node = next(n for n in doc["nodes"] if n["plan_key"] == "billing/b1")
        assert node["question"]["statement"] == STATEMENT and node["question"]["identifier"] == "T-1"
        assert next(n for n in doc["nodes"] if n["plan_key"] == "billing/b2")["question"] is None


def test_the_execution_panel_names_the_question(store):
    fake = parked_story(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        health = home(app)["health"]
        assert health["verdict"] == "stuck"
        reason = next(r for r in health["reasons"] if r["key"] == "billing/b1")
        assert reason == {"code": "question", "text": f"billing/b1 asks: {STATEMENT}", "key": "billing/b1"}
        assert "held" not in reason["text"]


def test_a_new_park_shows_at_the_next_refresh(store):
    billing(store)
    fake = FakeSortie()
    fake.issue("billing/b1")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert home(app)["questions"] == [] and home(app)["needs_person"]["questions"] == 0
        fake.park("T-1", "Which repository should the daemon live in?")
        settle(client)
        assert [q["statement"] for q in home(app)["questions"]] == ["Which repository should the daemon live in?"]


def test_a_park_on_another_projects_issue_is_not_this_projects_question(store):
    fake = parked_story(store)
    fake.park("T-99", "Not ours")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [q["identifier"] for q in home(app)["questions"]] == ["T-1"]


# Scenario: Answered ------------------------------------------------------------------


def test_an_approver_answers_through_sortie_and_the_question_leaves(store):
    fake = parked_story(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "  agentic-orchestration  "})
        assert r.status_code == 200, r.text
        assert r.json()["answered"] == "billing/b1" and r.json()["identifier"] == "T-1"
        assert fake.answers == [{"identifier": "T-1", "text": "agentic-orchestration", "author": "Local operator"}]
        # The text went to Sortie's answer API and nowhere else.
        carried = [(m, p) for m, p, b in fake.requests if "agentic-orchestration\"" in json.dumps(b or {})]
        assert carried == [("POST", "/api/v1/T-1/answer")]
        # Sortie released the park: the question leaves at once.
        assert home(app)["questions"] == []
        assert status(app, "billing/b1")["question"] is None
        assert r.json()["node"]["question"] is None
        assert not any(rr["code"] == "question" for rr in home(app)["health"]["reasons"])
        # Who answered, in the story's worker history.
        rows = client.get(f"{P}/workers/T-1/messages").json()["commands"]
        assert [(c["op"], c["text"], c["actor_name"]) for c in rows] == [("answer", "agentic-orchestration", "Local operator")]


def test_the_question_stays_until_sortie_no_longer_reports_the_park(store):
    fake = parked_story(store)
    fake.answer_releases = False  # Sortie takes the answer; the park ends a moment later.
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "yes"}).status_code == 200
        assert [q["key"] for q in home(app)["questions"]] == ["billing/b1"]
        fake.unpark("T-1")
        settle(client)
        assert home(app)["questions"] == []


def test_a_park_released_in_the_tracker_leaves_the_list(store):
    fake = parked_story(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        fake.unpark("T-1")
        settle(client)
        assert home(app)["questions"] == [] and status(app, "billing/b1")["question"] is None


def test_answering_a_story_that_is_not_parked_is_refused(store):
    fake = parked_story(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/stories/answer", json={"key": "billing/b2", "text": "hello"})
        assert r.status_code == 409 and r.json()["error"]["code"] == "not_parked"
        assert fake.answers == []


def test_an_empty_answer_or_an_unknown_story_reaches_no_sortie(store):
    fake = parked_story(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "   "})
        assert r.status_code == 400 and r.json()["error"]["code"] == "empty_answer"
        r = client.post(f"{P}/stories/answer", json={"key": "billing/nope", "text": "x"})
        assert r.status_code == 404
        assert not any(p.endswith("/answer") for _, p, _ in fake.requests)


def test_a_read_only_workbench_does_not_answer(store):
    fake = parked_story(store)
    app = app_for(store, fake, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert home(app)["questions"][0]["key"] == "billing/b1"
        r = client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "x"})
        assert r.status_code == 409 and r.json()["error"]["code"] == "sync_disabled"
        assert fake.answers == []


def oidc_app(store, fake, provider):
    cfg = make_config(store.root, password_hash="", oidc_issuer=ISSUER, oidc_client_id=CLIENT_ID, oidc_client_secret=CLIENT_SECRET,
                      oidc_redirect_url=REDIRECT, oidc_planner_group="eng", oidc_approver_group="plan-approvers")
    return create_app(cfg, validate=validate_offline, sortie=fake.client(),
                      oidc=OIDCClient(ISSUER, CLIENT_ID, CLIENT_SECRET, REDIRECT, transport=provider.transport()))


def test_the_answer_carries_the_approvers_name(store):
    fake = parked_story(store)
    provider = FakeProvider()
    app = oidc_app(store, fake, provider)
    with TestClient(app) as client:
        sign_in_oidc(client, provider)
        settle(client)
        assert client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "In agentic-orchestration."}).status_code == 200
        assert fake.answers == [{"identifier": "T-1", "text": "In agentic-orchestration.", "author": "Ana Ruiz"}]


def test_a_planner_sees_the_question_but_cannot_answer(store):
    fake = parked_story(store)
    provider = FakeProvider()
    provider.claims["groups"] = ["eng"]
    app = oidc_app(store, fake, provider)
    with TestClient(app) as client:
        sign_in_oidc(client, provider)
        settle(client)
        assert client.get(f"{P}/graph").json()["home"]["questions"][0]["statement"] == STATEMENT
        r = client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "x"})
        assert r.status_code == 403 and r.json()["error"]["code"] == "role_required"
        assert fake.answers == []


# Reading Sortie's parks --------------------------------------------------------------


def test_reading_a_park_entry():
    assert statement_of({"statement": "  Why?  "}) == "Why?"
    assert statement_of({"parked": {"statement": "Full"}}) == "Full"
    assert statement_of({"park": {"stop_statement": "Full"}}) == "Full"
    assert statement_of({"reason": "agent_blocked"}) == "" and statement_of(None) == ""
    assert parked_at_of({"parked_at": "2026-11-01T18:35:26Z"}) == datetime.fromisoformat("2026-11-01T18:35:26+00:00").timestamp()
    assert parked_at_of({"parked_at": 1_793_000_000}) == 1_793_000_000.0
    assert parked_at_of({"parked_at": 1_793_000_000_000}) == 1_793_000_000.0
    assert parked_at_of({"parked_at": "soon"}) is None and parked_at_of({}) is None
    q = question({"issue_identifier": "T-1", "reason": "agent_blocked", "statement": "Sho…"}, {"parked": {"statement": "Short?"}})
    assert q == {"identifier": "T-1", "statement": "Short?", "parked_at": None, "reason": "agent blocked"}


def test_questions_are_ranked_oldest_first():
    nodes = [{"plan_key": f"c/s{i}", "kind": "story", "change": "c", "title": f"S{i}",
              "tracker": {"identifier": f"T-{i}", "url": None}} for i in (1, 2, 3)]
    qs = home_questions({"T-1": {"identifier": "T-1", "statement": "a", "parked_at": 30.0, "reason": None},
                         "T-2": {"identifier": "T-2", "statement": "b", "parked_at": 10.0, "reason": None},
                         "T-3": {"identifier": "T-3", "statement": "c", "parked_at": None, "reason": None}}, nodes)
    assert [(q["key"], q["rank"]) for q in qs] == [("c/s2", 1), ("c/s1", 2), ("c/s3", 3)]
