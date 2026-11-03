"""A review: ui story in the workbench (agent-platform/run-supervision#A UI
is approved as a prototype, and the built story merges after its review): no
Approve, released once it covers a requirement, and once started it shows
its state on the way to Sortie's merge (building, in review, waiting for
click-proofs, merging) and its preview's address, with nothing to approve
and no person asked. Against the fake Sortie."""

from __future__ import annotations

import json
from pathlib import Path

import jsonschema
from fastapi.testclient import TestClient

from tests.conftest import breakdown, deliverable
from tests.workbench.conftest import P, project, sign_in, settle
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import STATES, app_for, status
from tests.workbench.test_watch import wait_for
from workbench.preview import write_previews

CHANGE = "sortie-supervises-runs"
UI = f"{CHANGE}/worker-activity-view"
ADDRESS = "https://workbench-t-1.ortus.example/"
HEAD = "1" * 40


def plan(store, *extra, covers=("run-supervision#Show worker activity",)):
    store.plan([(CHANGE, [])])
    store.change(CHANGE, breakdown(deliverable("worker-activity-view", covers=list(covers), review="ui"), *extra))


def synced_graphs(fake):
    return [r[2]["graph"] for r in fake.requests if r[1] == "/api/v1/plan/sync"]


def preview_record(path, click_proofs=None):
    write_previews(path, {"T-1": {"identifier": "T-1", "plan_key": UI, "url": ADDRESS, "pull": "ortusnova/agentic-orchestration#80",
                                  "pull_url": "https://github.com/ortusnova/agentic-orchestration/pull/80", "number": 80,
                                  "sha": HEAD, "port": 8800, "commented": HEAD, "click_proofs": click_proofs}})


def merge_watch(fake):
    """Sortie's merge watch on the story's pull request, which a passing review seeds."""
    fake.extra_state["pending_reactions"] = [{"issue_identifier": "T-1", "kind": "merge", "config_key": "auto_merge",
                                              "pull_request": {"number": 80, "repository": "ortusnova/agentic-orchestration"}}]


def asks_no_one(client, app):
    """Nothing to approve on the story, and the home asks no person about it."""
    r = client.post(f"{P}/approve", json={"keys": [UI]})
    assert r.status_code == 409 and r.json()["error"]["code"] == "ui_story"
    needs = project(app).hub.home["needs_person"]
    assert needs["total"] == 0 and needs["approve"] == 0


# Scenario: A built UI story. It passes its review, CI and click-proofs, and
# Sortie merges it: the workbench shows each step, and asks no one.
def test_a_built_ui_story_shows_its_state_until_sortie_merges_it(store, tmp_path):
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

        # No one approves it: its prototype was approved before it was written.
        r = client.post(f"{P}/approve", json={"keys": [UI]})
        assert r.status_code == 409 and r.json()["error"]["code"] == "ui_story"
        assert "design prototype" in r.json()["error"]["message"]

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
        fake.run("T-1", "s-build", stage="build")
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "building" and n["frozen"] is True and n["preview"] is None
        asks_no_one(client, app)

        # The build is over, and its review is next; then the review runs.
        fake.end("T-1")
        issue.update(runtime=None)
        settle(client)
        assert status(app, UI)["plan_status"] == "in_review"
        fake.run("T-1", "s-review", stage="review")
        issue.update(runtime={"status": "running"})
        settle(client)
        assert status(app, UI)["plan_status"] == "in_review"
        asks_no_one(client, app)

        # Its review passed: Sortie's merge waits for the click-proofs, and
        # the kit deploys its preview once CI passes.
        fake.end("T-1")
        issue.update(state="In Review", runtime=None)
        merge_watch(fake)
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "click_proofs" and n["preview"] is None
        preview_record(previews)
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "click_proofs"
        assert n["preview"]["url"] == ADDRESS and n["preview"]["pull"] == "ortusnova/agentic-orchestration#80"
        asks_no_one(client, app)

        # The kit's comment starts the review on the preview, a review fix run.
        fake.run("T-1", "s-proofs", stage="review", reaction_kind="review")
        issue.update(runtime={"status": "running"})
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "in_review" and n["preview"]["url"] == ADDRESS

        # The click-proofs passed, and the pull request records them for its head.
        fake.end("T-1")
        issue.update(runtime=None)
        preview_record(previews, click_proofs=HEAD)
        settle(client)
        assert status(app, UI)["plan_status"] == "merging"
        asks_no_one(client, app)

        # Sortie merges it, and merge completion makes it Done.
        fake.extra_state.pop("pending_reactions")
        issue.update(state="Done", terminal=True)
        settle(client)
        n = status(app, UI)
        assert n["plan_status"] == "frozen" and n["preview"] is None


def test_a_ui_story_whose_review_passed_waits_for_its_click_proofs_with_no_merge_watch_known(store, tmp_path):
    """An older Sortie serves no merge watch: the preview alone says the
    review passed."""
    plan(store)
    fake = FakeSortie()
    fake.plan_states = STATES
    previews = tmp_path / "previews.json"
    preview_record(previews)
    fake.issue(UI, state="In Review", review="ui")
    app = app_for(store, fake, previews_file=previews)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert status(app, UI)["plan_status"] == "click_proofs"


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


# Scenario: No prototype. A story that would change an interface names its
# approved prototype; without one the planning session and the store's
# instructions send it to prototyping first, and the operator is asked on
# the decision board, not on a built pull request.
def test_a_ui_change_with_no_approved_prototype_goes_to_prototyping_first():
    import workbench

    prompt = " ".join((Path(__file__).resolve().parents[2] / "prompts/planning-session.md").read_text().split())
    template = Path(workbench.__file__).parent / "store_template/openspec/schemas/spec-driven-planned/schema.yaml"
    instructions = " ".join(template.read_text().split())
    for text in (prompt, instructions):
        assert "approves the interface as a design prototype before the story is written" in text
        assert "acceptance criteria name that approved prototype" in text
        assert "A UI change with no approved prototype is not a story yet" in text
        assert "asked on the decision board, not on a" in text
        assert "Sortie merges it once those click-proofs are recorded" in text
        assert "their merge is its approval" not in text and "Sortie never merges it" not in text
