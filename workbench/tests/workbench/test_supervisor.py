"""The supervisor's actions and its pause, in the workbench (agent-platform/
run-supervision#The supervisor acts only through a scoped API, and each
action is recorded): each action shows beside the story it touched, with its
evidence and result, and a person pauses and resumes the supervisor."""

from __future__ import annotations

from datetime import datetime

from fastapi.testclient import TestClient

from tests.workbench.conftest import P, settle, sign_in
from tests.workbench.fake_oidc import FakeProvider
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import app_for, billing, status
from tests.workbench.test_identity import sign_in_oidc
from tests.workbench.test_questions import oidc_app
from tests.workbench.test_supervision import fleet_app
from workbench.supervisor import evidence_of, pull_of, supervisor_view

REPO = "ortusnova/agentic-orchestration"
PULL = f"{REPO}#75"
PULL_URL = f"https://github.com/{REPO}/pull/75"
MERGE_COMMIT = "6684940c0ffee6684940c0ffee6684940c0ffee0"
MERGED_AT = "2026-11-03T10:12:00Z"


def ts(iso: str) -> float:
    return datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp()


def stories(store) -> FakeSortie:
    """billing/b1 is T-1, parked; billing/b2 is T-2."""
    billing(store)
    fake = FakeSortie()
    fake.issue("billing/b1", state="In Review")
    fake.issue("billing/b2")
    fake.park("T-1", "The pull request is merged, but the issue is not Done.")
    return fake


def supervisor(app_or_client) -> dict:
    app = getattr(app_or_client, "app", app_or_client)
    return app.state.projects.runtimes["store"].hub.doc["supervisor"]


# Scenario: An action and its evidence -----------------------------------------------


def test_a_recorded_merge_names_its_pull_request_merge_commit_and_time_beside_the_story(store):
    fake = stories(store)
    fake.supervise("record_merge", "T-1", pull=PULL, merge_commit=MERGE_COMMIT, at=MERGED_AT,
                   evidence="The forge shows the pull request merged; the story is parked.")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        doc = client.get(f"{P}/graph").json()
        sup = doc["supervisor"]
        assert sup["status"] == "active" and sup["total"] == 1
        a = sup["actions"][0]
        assert (a["action"], a["result"], a["identifier"], a["key"], a["label"]) == ("record_merge", "done", "T-1", "billing/b1", "b1")
        assert a["title"] == status(app, "billing/b1")["title"]
        assert a["pull"] == {"ref": f"{REPO}#75", "url": PULL_URL}
        assert a["at"] == ts(MERGED_AT) and a["sortie"] == "sortie"
        assert a["evidence"] == [
            {"name": None, "value": "The forge shows the pull request merged; the story is parked.", "url": None},
            {"name": "merge commit", "value": MERGE_COMMIT, "url": f"https://github.com/{REPO}/commit/{MERGE_COMMIT}"}]
        # Beside the story it touched, and no other.
        b1 = next(n for n in doc["nodes"] if n["plan_key"] == "billing/b1")
        b2 = next(n for n in doc["nodes"] if n["plan_key"] == "billing/b2")
        assert b1["supervisor_actions"] == [a] and b2["supervisor_actions"] == []


def test_a_new_action_shows_at_the_next_refresh_newest_first(store):
    fake = stories(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert supervisor(app)["actions"] == []
        fake.supervise("fix_run", "T-2", at="2026-11-03T09:00:00Z", evidence="CI failed on the head: lint")
        fake.supervise("release", "T-1", at="2026-11-03T09:30:00Z", evidence="The pull request merged.")
        settle(client)
        acts = supervisor(app)["actions"]
        assert [(x["action"], x["key"]) for x in acts] == [("release", "billing/b1"), ("fix_run", "billing/b2")]
        assert acts[1]["evidence"] == [{"name": None, "value": "CI failed on the head: lint", "url": None}]


# Scenario: A refused action ----------------------------------------------------------


def test_a_refused_hold_shows_as_refused_beside_the_story(store):
    fake = stories(store)
    record = fake.supervise("POST /api/v1/{identifier}/hold", "T-2", evidence="It looks stuck.")
    assert record["result"] == "refused"
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        a = supervisor(app)["actions"][0]
        assert (a["action"], a["result"], a["key"]) == ("POST /api/v1/{identifier}/hold", "refused", "billing/b2")
        assert a["detail"] == "forbidden: not a supervisor action"
        assert status(app, "billing/b2")["supervisor_actions"] == [a]


def test_a_refused_approval_names_no_story_and_approves_nothing(store):
    """Sortie records a refused plan route with no issue: it shows on the home only."""
    fake = stories(store)
    fake.supervise("POST /api/v1/plan/sync", None, evidence="All criteria pass.")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        a = supervisor(app)["actions"][0]
        assert (a["action"], a["result"], a["key"]) == ("POST /api/v1/plan/sync", "refused", None)
        assert status(app, "billing/b2")["plan_status"] == "draft"


def test_an_action_on_another_projects_issue_is_left_out_and_one_on_no_issue_stays(store):
    fake = stories(store)
    fake.supervise("grant_sessions", "X-9", evidence="It ran out of sessions on a green build.")
    fake.supervise("file_issue", None, evidence="Pushes fail without the workflow scope.")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [(a["action"], a["key"]) for a in supervisor(app)["actions"]] == [("file_issue", None)]


# Pause and resume ---------------------------------------------------------------------


def test_pause_and_resume_change_the_supervisors_status_in_sortie(store):
    fake = stories(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/supervisor/pause")
        assert r.status_code == 200, r.text
        assert fake.supervisor_controls == [{"op": "pause", "author": "Local operator"}]
        assert fake.supervisor["status"] == "paused"
        sup = r.json()["supervisor"]
        assert sup["status"] == "paused" and sup["paused_by"] == "Local operator" and sup["paused_at"] == ts("2026-11-03T11:00:00Z")
        assert supervisor(app)["status"] == "paused"
        # Paused, it acts no more: Sortie refuses, and records the refusal.
        fake.supervise("merge", "T-1", pull=PULL)
        r = client.post(f"{P}/supervisor/resume")
        assert r.status_code == 200, r.text
        assert fake.supervisor["status"] == "active"
        sup = r.json()["supervisor"]
        assert sup["status"] == "active" and sup["paused_by"] is None
        assert (sup["actions"][0]["result"], sup["actions"][0]["detail"]) == ("refused", "the supervisor is paused")


def test_a_read_only_workbench_pauses_and_resumes(store):
    """A ui story's preview runs read-only toward the tracker; the pause is no tracker write."""
    fake = stories(store)
    app = app_for(store, fake, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert client.post(f"{P}/supervisor/pause").status_code == 200
        assert client.post(f"{P}/supervisor/resume").status_code == 200
        assert [c["op"] for c in fake.supervisor_controls] == ["pause", "resume"]


def test_a_planner_sees_the_supervisor_but_cannot_pause_it(store):
    fake = stories(store)
    provider = FakeProvider()
    provider.claims["groups"] = ["eng"]
    app = oidc_app(store, fake, provider)
    with TestClient(app) as client:
        sign_in_oidc(client, provider)
        settle(client)
        assert client.get(f"{P}/graph").json()["supervisor"]["status"] == "active"
        r = client.post(f"{P}/supervisor/pause")
        assert r.status_code == 403 and r.json()["error"]["code"] == "role_required"
        assert fake.supervisor_controls == []


def test_the_pause_carries_the_approvers_name(store):
    fake = stories(store)
    provider = FakeProvider()
    app = oidc_app(store, fake, provider)
    with TestClient(app) as client:
        sign_in_oidc(client, provider)
        settle(client)
        assert client.post(f"{P}/supervisor/pause").status_code == 200
        assert fake.supervisor_controls == [{"op": "pause", "author": "Ana Ruiz"}]


def test_a_sortie_with_no_supervisor(store):
    fake = stories(store)
    fake.supervisor = None
    fake.supervisor_actions = None
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        doc = client.get(f"{P}/graph").json()
        assert doc["supervisor"] is None
        assert all(n.get("supervisor_actions") == [] for n in doc["nodes"] if n["kind"] == "story")
        r = client.post(f"{P}/supervisor/pause")
        assert r.status_code == 409 and r.json()["error"]["code"] == "no_supervisor"


def test_a_sortie_that_records_actions_but_serves_no_status_reads_off(store):
    """sortie#40 serves supervisor_actions; the status and the pause come with the supervisor session."""
    fake = stories(store)
    fake.supervisor = None
    fake.supervise("release", "T-1", evidence="The pull request merged.")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        sup = client.get(f"{P}/graph").json()["supervisor"]
        assert sup["status"] == "off" and "no supervisor session" in sup["reason"]
        assert [a["key"] for a in sup["actions"]] == ["billing/b1"]
        r = client.post(f"{P}/supervisor/pause")
        assert r.status_code == 409 and r.json()["error"]["code"] == "no_supervisor"


def test_another_sorties_actions_show_beside_their_stories(store):
    app, p1, taichi = fleet_app(store)
    taichi.supervise("fix_run", "T-2", at="2026-11-03T09:00:00Z", evidence="CI failed.")
    p1.supervise("release", "T-3", at="2026-11-03T09:30:00Z", evidence="Merged.")
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        sup = client.get(f"{P}/graph").json()["supervisor"]
        assert [(a["action"], a["identifier"], a["sortie"]) for a in sup["actions"]] == [
            ("release", "T-3", "p1"), ("fix_run", "T-2", "taichi")]
        assert sup["status"] == "active"


def test_an_unknown_operation_reaches_no_sortie(store):
    fake = stories(store)
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert client.post(f"{P}/supervisor/approve").status_code == 422
        assert not any(p.startswith("/api/v1/supervisor") for _, p, _ in fake.requests)


# How Sortie's records read -----------------------------------------------------------


def test_pull_requests_in_each_form_sortie_may_give():
    assert pull_of(f"https://github.com/{REPO}/pull/12") == {"ref": f"{REPO}#12", "url": f"https://github.com/{REPO}/pull/12"}
    assert pull_of({"repository": REPO, "number": 12}) == {"ref": f"{REPO}#12", "url": f"https://github.com/{REPO}/pull/12"}
    assert pull_of(f"{REPO}#12") == {"ref": f"{REPO}#12", "url": f"https://github.com/{REPO}/pull/12"}
    assert pull_of({"url": f"https://github.com/{REPO}/pull/12"}) == {"ref": f"{REPO}#12", "url": f"https://github.com/{REPO}/pull/12"}
    assert pull_of("#12") == {"ref": "#12", "url": None}
    assert pull_of(None) is None and pull_of({}) is None


def test_evidence_in_each_form_sortie_may_give():
    pull = pull_of(PULL)
    assert evidence_of(None, pull) == [] and evidence_of({}, pull) == []
    assert evidence_of(["a", {"ci": "passed"}], pull) == [{"name": None, "value": "a", "url": None},
                                                         {"name": "ci", "value": "passed", "url": None}]
    assert evidence_of({"run": "https://ci/1", "checks": ["lint", "test"]}, None) == [
        {"name": "run", "value": "https://ci/1", "url": "https://ci/1"}, {"name": "checks", "value": "lint, test", "url": None}]


def test_a_supervisor_with_no_status_reads_from_paused():
    nodes = [{"plan_key": "c/s", "kind": "story", "title": "S", "tracker": {"identifier": "T-1", "url": None}}]
    view, by_key = supervisor_view({"supervisor": {"paused": True, "actions": [{"action": "x", "issue_identifier": "T-1",
                                                                              "result": {"status": "failed", "message": "forge down"}}]}}, nodes)
    assert view["status"] == "paused"
    assert (view["actions"][0]["result"], view["actions"][0]["detail"]) == ("failed", "forge down")
    assert by_key == {"c/s": view["actions"]}
    assert supervisor_view({}, nodes) == (None, {})


def test_an_action_on_this_projects_epic_stays():
    nodes = [{"plan_key": "c", "kind": "epic", "title": "C", "tracker": {"identifier": "T-1", "url": None}},
             {"plan_key": "c/s", "kind": "story", "title": "S", "tracker": {"identifier": "T-2", "url": None}}]
    view, by_key = supervisor_view({"supervisor_actions": [
        {"id": 2, "action": "file_issue", "issue_identifier": "T-1", "result": "done"},
        {"id": 1, "action": "grant_sessions", "issue_identifier": "X-9", "result": "done"}]}, nodes)
    assert [(a["action"], a["key"], a["label"]) for a in view["actions"]] == [("file_issue", "c", "c")]
    assert by_key == {"c": view["actions"]}
