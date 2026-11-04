"""Platform faults and their person actions (agent-platform/run-supervision,
"A stopped failure goes to whoever can act on it"): the workbench asks each
fault only a person can clear under "Needs a person", beside the agents'
questions, and its Done makes Sortie check the fault again at once. A
story's card shows its failure's full cause."""

from __future__ import annotations

import time
from datetime import datetime, timezone

from fastapi.testclient import TestClient

from tests.workbench.conftest import P, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import app_for, billing
from tests.workbench.test_questions import home
from workbench.faults import failures, fault_text, faults_of, home_faults, project_faults

FAULT = "pf-0123456789ab"
CAUSE = ("refusing to allow a Personal Access Token to create or update workflow `.github/workflows/ci.yml` "
         "without `workflow` scope")
DETAIL = ("To github.com:ortusnova/agentic-orchestration.git\n"
          " ! [remote rejected] sortie/ONS-49 -> sortie/ONS-49 (" + CAUSE + ")\n"
          "error: failed to push some refs to 'github.com:ortusnova/agentic-orchestration.git'")
ACTION = "Give taichi's GitHub token \"Workflows: Read and write\""
WHERE = "github.com → Settings → Developer settings → Fine-grained tokens → the token Sortie on taichi pushes with"


def three_held(store, **fault):
    """billing/b1, billing/b2 and accounts/a3 are T-1, T-2 and T-3, and each
    stopped on a push refused for the workflow scope: one fault holds them."""
    billing(store)
    fake = FakeSortie()
    fake.issue("billing/b1", state="In Progress")
    fake.issue("billing/b2", state="In Progress")
    fake.issue("accounts/a3", state="In Progress")
    fake.platform_fault(FAULT, ["T-1", "T-2", "T-3"], CAUSE, detail=DETAIL,
                        **({"action": ACTION, "where": WHERE} | fault))
    return fake


def node(app, key):
    return next(n for n in project(app).hub.doc["nodes"] if n["plan_key"] == key)


# Scenario: A missing token scope -----------------------------------------------------


def test_one_fault_holds_the_three_stories_and_asks_a_person_its_action(store):
    fake = three_held(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        h = client.get(f"{P}/graph").json()["home"]
        [f] = h["faults"]
        # The cause, the action and where to take it, and the stories it holds.
        assert f["id"] == FAULT and f["cause"] == CAUSE and f["detail"] == DETAIL
        assert f["action"] == ACTION and f["where"] == WHERE
        assert [(s["identifier"], s["key"]) for s in f["stories"]] == [
            ("T-1", "billing/b1"), ("T-2", "billing/b2"), ("T-3", "accounts/a3")]
        assert f["keys"] == ["billing/b1", "billing/b2", "accounts/a3"] and f["sortie"] == "sortie"
        # Under "Needs a person", first, with the agents' questions.
        assert f["rank"] == 1 and h["questions"] == []
        needs = h["needs_person"]
        assert needs["faults"] == 1 and needs["nearest"] == "billing/b1"
        ranks = sorted(i["rank"] for i in h["not_ready"] if i["column"])
        assert ranks == list(range(2, 2 + len(ranks))) and needs["total"] == 1 + len(ranks)
        # The agents are stuck on it, and none of the held stories is ready to run.
        assert h["health"]["verdict"] == "stuck"
        reason = next(r for r in h["health"]["reasons"] if r["code"] == "platform_fault")
        assert reason == {"code": "platform_fault", "key": "billing/b1",
                          "text": f"A platform fault holds billing/b1, billing/b2 and accounts/a3: {CAUSE}. {ACTION} ({WHERE})"}
        assert h["health"]["runway"] == 0


def test_each_held_storys_card_shows_the_faults_full_cause(store):
    fake = three_held(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        for key in ("billing/b1", "billing/b2", "accounts/a3"):
            failure = node(app, key)["failure"]
            assert failure["source"] == "platform_fault" and failure["fault_id"] == FAULT
            assert failure["cause"] == CAUSE and failure["detail"] == DETAIL
            assert failure["action"] == ACTION and failure["where"] == WHERE and failure["step"] == "export"


def test_done_makes_sortie_check_again_and_a_pass_releases_the_stories(store):
    fake = three_held(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/faults/done", json={"id": FAULT})
        assert r.status_code == 200, r.text
        body = r.json()
        assert body["resolved"] is True and body["done"] == FAULT and body["fault"] is None
        assert body["released"] == ["billing/b1", "billing/b2", "accounts/a3"]
        # Sortie's re-check, called once, in the person's name.
        assert fake.faults_done == [{"id": FAULT, "by": "Local operator"}]
        assert [(m, p) for m, p, _ in fake.requests if "platform-faults" in p] == [
            ("POST", f"/api/v1/platform-faults/{FAULT}/action-done")]
        # The fault leaves at once, and the stories' cards with it.
        assert home(app)["faults"] == [] and home(app)["needs_person"]["faults"] == 0
        assert not any(r["code"] == "platform_fault" for r in home(app)["health"]["reasons"])
        assert all(node(app, k)["failure"] is None for k in ("billing/b1", "billing/b2", "accounts/a3"))
        # Who did it, in each story's worker history.
        rows = client.get(f"{P}/workers/T-2/messages").json()["commands"]
        assert [(c["op"], c["text"], c["actor_name"]) for c in rows] == [("fault_done", ACTION, "Local operator")]


def test_a_failed_check_keeps_the_fault_with_its_error(store):
    fake = three_held(store)
    fake.fault_check_passes = False
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/faults/done", json={"id": FAULT})
        assert r.status_code == 200, r.text
        body = r.json()
        error = "push refused again: the token still lacks the workflow scope"
        assert body["resolved"] is False and body["released"] == [] and body["last_check_error"] == error
        [f] = home(app)["faults"]
        assert f["last_check_error"] == error and f["done_by"] == "Local operator" and f["done_at"] is not None
        assert body["fault"]["id"] == FAULT
        assert node(app, "billing/b1")["failure"]["last_check_error"] == error


def test_a_fault_sortie_clears_on_its_own_asks_no_person(store):
    fake = three_held(store, action=None, where="")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        h = home(app)
        assert h["faults"] == [] and h["needs_person"]["faults"] == 0
        assert not any(r["code"] == "platform_fault" for r in h["health"]["reasons"])
        # The story still shows why it does not run.
        failure = node(app, "billing/b1")["failure"]
        assert failure["cause"] == CAUSE and failure["action"] is None


def test_a_fault_ranks_with_the_agents_questions_oldest_first(store):
    fake = three_held(store)
    fake.platform_faults[0]["stories"] = fake.platform_faults[0]["stories"][2:]  # holds accounts/a3 only
    fake.park("T-1", "Which currency?", parked_at="2026-11-03T11:00:00Z")
    fake.park("T-2", "Which tax rule?", parked_at="2026-11-03T13:00:00Z")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        h = home(app)
        order = sorted([(q["rank"], q["key"]) for q in h["questions"]] + [(f["rank"], "fault") for f in h["faults"]])
        assert order == [(1, "billing/b1"), (2, "fault"), (3, "billing/b2")]
        assert h["needs_person"]["nearest"] == "billing/b1" and h["needs_person"]["questions"] == 2


def test_another_projects_fault_is_not_this_projects(store):
    fake = three_held(store)
    fake.platform_fault("pf-other", ["X-9"], "push refused", action="Fix it", recorded_at="2026-11-03T11:00:00Z")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [f["id"] for f in home(app)["faults"]] == [FAULT]
        r = client.post(f"{P}/faults/done", json={"id": "pf-other"})
        assert r.status_code == 404 and r.json()["error"]["code"] == "platform_fault_not_found"
        assert fake.faults_done == []


def test_a_fault_sortie_cleared_meanwhile_leaves(store):
    fake = three_held(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        fake.platform_faults = []
        r = client.post(f"{P}/faults/done", json={"id": FAULT})
        assert r.status_code == 404 and r.json()["error"]["code"] == "platform_fault_not_found"
        assert home(app)["faults"] == []


def test_a_read_only_workbench_does_not_mark_a_fault_done(store):
    fake = three_held(store)
    app = app_for(store, fake, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert home(app)["faults"][0]["id"] == FAULT
        r = client.post(f"{P}/faults/done", json={"id": FAULT})
        assert r.status_code == 409 and r.json()["error"]["code"] == "sync_disabled"
        assert fake.faults_done == []


# Scenario: Tests fail twice ----------------------------------------------------------

MAKE_TEST = ("FAILED tests/workbench/test_home.py::test_ranks - AssertionError: assert [2, 3] == [1, 2]\n"
             "1 failed, 425 passed in 140.2s\nmake: *** [Makefile:8: test] Error 1")


def iso(t: float) -> str:
    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def test_a_storys_card_shows_its_failed_runs_full_cause(store):
    billing(store)
    fake = FakeSortie()
    fake.issue("billing/b1", state="In Progress")
    fake.issue("billing/b2", state="In Progress")
    now = time.time()
    fake.runs = [
        {"issue_identifier": "T-1", "status": "failed", "started_at": iso(now - 900), "completed_at": iso(now - 600),
         "error": "make test failed", "error_detail": "an older failure"},
        {"issue_identifier": "T-1", "status": "failed", "started_at": iso(now - 500), "completed_at": iso(now - 300),
         "error": "worker exited: make test: exit status 2", "error_detail": MAKE_TEST},
        {"issue_identifier": "T-2", "status": "failed", "started_at": iso(now - 900), "completed_at": iso(now - 600),
         "error": "make test failed", "error_detail": "x"},
        {"issue_identifier": "T-2", "status": "succeeded", "started_at": iso(now - 500), "completed_at": iso(now - 300),
         "error": None, "error_detail": None},
    ]
    # A story fault: Sortie starts a new session with the failure in its prompt.
    fake.run("T-1", "s3", trigger="story_fault")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        failure = node(app, "billing/b1")["failure"]
        assert failure["source"] == "run" and failure["cause"] == "worker exited: make test: exit status 2"
        assert failure["detail"] == MAKE_TEST and failure["status"] == "failed"
        # A story whose newest run succeeded shows no failure.
        assert node(app, "billing/b2")["failure"] is None
        # A failure on a story is not a platform fault: no person is asked.
        assert home(app)["faults"] == []


# Reading Sortie's faults -------------------------------------------------------------


def test_reading_platform_faults():
    state = {"platform_faults": [
        {"id": "pf-2", "class": "unclassified", "step": "export", "cause": " odd ", "detail": "", "person_action": None,
         "stories": [{"issue_identifier": "T-2", "held_at": "2026-11-03T12:30:00Z"}], "recorded_at": "2026-11-03T12:30:00Z"},
        {"id": "pf-1", "class": "token_scope", "step": "export", "cause": CAUSE, "detail": DETAIL,
         "person_action": {"action": ACTION, "where": WHERE, "done_at": None, "done_by": ""},
         "stories": [{"issue_identifier": "T-1", "display_identifier": "", "held_at": "2026-11-03T12:00:00Z"}, "junk"],
         "recorded_at": "2026-11-03T12:00:00Z", "last_check_at": None, "last_check_error": "",
         "next_check_at": "2026-11-03T12:05:00Z", "checking": False},
        {"class": "no id"}, "junk"]}
    faults = faults_of([("taichi", state, None), ("down", None, "unreachable"), ("old", {}, None)])
    assert [f["id"] for f in faults] == ["pf-1", "pf-2"]
    f1, f2 = faults
    assert f1["sortie"] == "taichi" and f1["action"] == ACTION and f1["where"] == WHERE and f1["done_at"] is None
    assert f1["stories"] == [{"identifier": "T-1", "display": None,
                              "held_at": datetime.fromisoformat("2026-11-03T12:00:00+00:00").timestamp()}]
    assert f1["last_check_error"] is None and f2["action"] is None and f2["cause"] == "odd" and f2["detail"] is None
    nodes = [{"plan_key": "c/s1", "kind": "story", "change": "c", "title": "S1", "tracker": {"identifier": "T-1"}}]
    ours = project_faults(faults, nodes)
    assert [f["id"] for f in ours] == ["pf-1"] and ours[0]["keys"] == ["c/s1"]
    assert [f["key"] for f in home_faults(ours)] == ["c/s1"]
    assert fault_text(ours[0]) == f"A platform fault holds c/s1: {CAUSE}. {ACTION} ({WHERE})"
    by_ident = failures(ours, {"taichi": [{"issue_identifier": "T-1", "status": "failed", "error": "e",
                                           "completed_at": "2026-11-03T11:00:00Z"}]})
    # The fault that holds the story is its failure, before any run's.
    assert by_ident["T-1"]["source"] == "platform_fault" and by_ident["T-1"]["detail"] == DETAIL
