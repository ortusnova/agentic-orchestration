"""An approval task is done when its stories are approved, with no tasks.md edit
(agent-platform/supervisor-autonomy#An approval task is done when its story is approved)."""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from tests.conftest import breakdown, deliverable, milestone
from tests.workbench.conftest import P, project, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import STATES, app_for, billing
from tests.workbench.test_home import home_of, items_by_key
from workbench.home import APPROVED_STATUSES

TASKS = ("- [ ] 1.1 Approve `b1` and `b2` in the workbench. [operator]\n"
         "- [ ] 1.2 Approve `b2`, select it, and choose **Confirm: replaces** `b1` in the workbench. [operator]\n")


def ops_store(store, tasks=TASKS):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("b1", kind="enabler"), deliverable("b2", kind="enabler")))
    store.tasks("billing", tasks)
    store.milestones([milestone("m1", [{"change": "billing"}])])


def task_keys(home):
    return {k for k, i in items_by_key(home).items() if i["kind"] == "task"}


def test_an_unticked_task_stays_listed_while_any_story_is_unapproved(store):
    ops_store(store)
    home = home_of(store, {"billing/b1": "ready"})
    assert task_keys(home) == {"billing#1.1", "billing#1.2"}
    assert home["done_by_approval"] == []
    m = home["milestones"][0]
    assert m["progress"]["operator_tasks"] == {"done": 0, "total": 2}


@pytest.mark.parametrize("status", sorted(APPROVED_STATUSES))
def test_a_task_is_done_by_approval_once_each_story_is_approved_started_or_done(store, status):
    ops_store(store)
    home = home_of(store, {"billing/b1": "ready", "billing/b2": status})
    # Not listed under Needs a person; the task that also asks to select and confirm still is.
    assert task_keys(home) == {"billing#1.2"}
    assert home["needs_person"]["operator"] == 1
    [task] = home["done_by_approval"]
    assert task["id"] == "billing#1.1" and task["number"] == "1.1" and task["approves"] == ["billing/b1", "billing/b2"]
    assert home["milestones"][0]["progress"]["operator_tasks"] == {"done": 1, "total": 2}
    # The tasks.md line is not edited.
    assert (store.root / "openspec/changes/billing/tasks.md").read_text() == TASKS


def test_a_story_whose_tracker_issue_is_done_counts_as_approved(store):
    ops_store(store)
    home = home_of(store, {"billing/b1": "ready"}, done={"billing/b2"})
    assert "billing#1.1" not in task_keys(home)
    assert [t["id"] for t in home["done_by_approval"]] == ["billing#1.1"]


@pytest.mark.parametrize("status", ["draft", "needs_spec", "needs_rereview", "delivery_unconfirmed"])
def test_a_story_not_approved_keeps_the_task_open(store, status):
    ops_store(store)
    home = home_of(store, {"billing/b1": "ready", "billing/b2": status})
    assert "billing#1.1" in task_keys(home) and home["done_by_approval"] == []


def test_a_ticked_task_is_done_by_tasks_md_not_by_approval(store):
    ops_store(store, "- [x] 1.1 Approve `b1` and `b2` in the workbench. [operator]\n")
    home = home_of(store, {"billing/b1": "ready", "billing/b2": "ready"})
    assert task_keys(home) == set() and home["done_by_approval"] == []
    assert home["milestones"][0]["progress"]["operator_tasks"] == {"done": 1, "total": 1}


def test_an_approval_already_given(store):
    """The operator approves the stories in the workbench and their approval
    task is unticked in tasks.md: the workbench shows the task done, and
    "Needs a person" does not list it."""
    billing(store)
    store.tasks("billing", "- [ ] 1.1 Approve `b1`, then `b2`, in the workbench. [operator]\n")
    fake = FakeSortie()
    fake.plan_states = STATES
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)

        def home():
            return project(app).hub.home

        assert "billing#1.1" in task_keys(home()) and home()["done_by_approval"] == []
        assert client.post(f"{P}/approve", json={"keys": ["billing/b1"]}).status_code == 200
        # One of its two stories is approved: still listed.
        assert "billing#1.1" in task_keys(home()) and home()["done_by_approval"] == []
        before = home()["needs_person"]["total"]

        assert client.post(f"{P}/approve", json={"keys": ["billing/b2"]}).status_code == 200
        assert "billing#1.1" not in task_keys(home())
        assert [t["id"] for t in home()["done_by_approval"]] == ["billing#1.1"]
        assert home()["needs_person"]["total"] == before - 2  # b2's approval, and the task
        assert "- [ ] 1.1" in (store.root / "openspec/changes/billing/tasks.md").read_text()
