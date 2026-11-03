"""A review: ui story in the workbench (agent-platform/run-supervision#A UI
story is built before it is approved, and its merge is the approval): no
Approve, released once it covers a requirement, shown as building, then
"ready for you to look" with its preview's address. Against the fake Sortie."""

from __future__ import annotations

import json

import jsonschema
from fastapi.testclient import TestClient

from tests.conftest import breakdown, deliverable
from tests.workbench.conftest import P, sign_in, settle
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import STATES, app_for, status
from tests.workbench.test_watch import wait_for
from workbench.preview import write_previews

CHANGE = "sortie-supervises-runs"
UI = f"{CHANGE}/worker-activity-view"
ADDRESS = "https://workbench-t-1.ortus.example/"


def plan(store, *extra, covers=("run-supervision#Show worker activity",)):
    store.plan([(CHANGE, [])])
    store.change(CHANGE, breakdown(deliverable("worker-activity-view", covers=list(covers), review="ui"), *extra))


def synced_graphs(fake):
    return [r[2]["graph"] for r in fake.requests if r[1] == "/api/v1/plan/sync"]


def preview_record(path):
    write_previews(path, {"T-1": {"identifier": "T-1", "plan_key": UI, "url": ADDRESS, "pull": "ortusnova/agentic-orchestration#80",
                                  "pull_url": "https://github.com/ortusnova/agentic-orchestration/pull/80", "number": 80,
                                  "sha": "1" * 40, "port": 8800, "commented": True}})


# Scenario: A UI story from spec to merge.
def test_a_ui_story_from_spec_to_merge(store, tmp_path):
    plan(store)
    fake = FakeSortie()
    fake.plan_states = STATES
    previews = tmp_path / "previews.json"
    app = app_for(store, fake, previews_file=previews)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        n = status(app, UI)
        assert (n["review"], n["approval"], n["plan_status"], n["approved_by"]) == ("ui", "draft", "ready", None)

        # No one approves it.
        r = client.post(f"{P}/approve", json={"keys": [UI]})
        assert r.status_code == 409 and r.json()["error"]["code"] == "ui_story"

        # It syncs and is released without an approval, and carries no approver.
        assert wait_for(lambda: UI in fake.released, timeout=3)
        graph = synced_graphs(fake)[-1]
        schema = json.loads((store.root / "openspec/schemas/planning/plan-graph.schema.json").read_text())
        jsonschema.validate(graph, schema)
        node = next(x for x in graph["nodes"] if x["plan_key"] == UI)
        assert node["review"] == "ui" and node["release"] is True
        assert "approved_by" not in node and "approved_at" not in node

        # Sortie builds it.
        issue = fake.issue(UI, state="In Progress", runtime={"status": "running"}, review="ui")
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "building" and n["frozen"] is True and n["preview"] is None

        # Its review and CI pass, and the kit deploys its preview.
        issue.update(state="In Review", runtime=None)
        settle(client)
        assert status(app, UI)["plan_status"] == "building"
        preview_record(previews)
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "preview"
        assert n["preview"]["url"] == ADDRESS and n["preview"]["pull"] == "ortusnova/agentic-orchestration#80"

        # Scenario: The operator is satisfied: the merge makes it Done.
        issue.update(state="Done", terminal=True)
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "frozen" and n["preview"] is None


def test_the_epic_batch_leaves_a_ui_story_out(store):
    plan(store, deliverable("b1", covers=["run-supervision#Do b1"]))
    fake = FakeSortie()
    fake.plan_states = STATES
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        r = client.post(f"{P}/approve", json={"epic": CHANGE})
        assert r.status_code == 200 and r.json()["approved"] == [f"{CHANGE}/b1"]
        assert status(app, UI)["approved_by"] is None
        r = client.post(f"{P}/approve", json={"keys": [f"{CHANGE}/b1", UI]})
        assert r.status_code == 409 and r.json()["error"]["keys"] == [UI]


def test_a_ui_story_without_a_spec_waits_for_one(store):
    plan(store, covers=())
    fake = FakeSortie()
    fake.plan_states = STATES
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert status(app, UI)["plan_status"] == "needs_spec"
        assert client.post(f"{P}/approve", json={"keys": [UI]}).status_code == 409
        settle(client)
        assert UI not in fake.released


def test_a_story_after_a_ui_story_waits_on_its_work_not_its_approval(store):
    plan(store, deliverable("after", ["worker-activity-view"], covers=["run-supervision#Do after"]))
    fake = FakeSortie()
    fake.plan_states = STATES
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        assert client.post(f"{P}/approve", json={"keys": [f"{CHANGE}/after"]}).status_code == 200
        n = status(app, f"{CHANGE}/after")
        assert n["plan_status"] == "ready" and n["held_by"] == []


def test_a_preview_of_another_story_is_not_this_ones(store, tmp_path):
    plan(store, deliverable("b1", covers=["run-supervision#Do b1"]))
    fake = FakeSortie()
    fake.plan_states = STATES
    fake.issue(f"{CHANGE}/b1", state="In Review")
    previews = tmp_path / "previews.json"
    preview_record(previews)
    app = app_for(store, fake, previews_file=previews)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        # T-1 is b1 here, which has review: human.
        assert status(app, f"{CHANGE}/b1")["preview"] is None
