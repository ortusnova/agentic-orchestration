"""An operator task that only asks for stories to be approved is linked to them
(agent-platform/supervisor-autonomy#An approval task is done when its story is approved)."""

from __future__ import annotations

import pytest

from planlib import milestones as ms
from planlib.validate import validate
from tests.conftest import breakdown, deliverable
from tests.planlib.test_checks import codes, no_openspec

# ortus-nova-specs repo-declared-egress/tasks.md as it was before
# ortus-nova-specs#82 ticked its six approval tasks (2.1, 3.1, 4.1, 6.1, 8.1, 8.3).
REPO_DECLARED_EGRESS = """\
## 2. sortie-reads-egress

- [ ] 2.1 Approve `sortie-reads-egress` in the workbench. [operator]
- [ ] 2.2 An agent checks its pull request against the story's criteria and merges it once CI passes; Sortie on p1 deploys it. Verify: `GET /api/v1/state` shows the build, and an offer the worker daemon logs carries `run.egress`.

## 3. daemon-applies-egress

- [ ] 3.1 Approve `daemon-applies-egress` in the workbench. [operator]

## 4. homelab-egress-file

- [ ] 4.1 Approve `homelab-egress-file` in the workbench. [operator]

## 5. kit-credential-egress

- [ ] 5.1 Approve `kit-credential-egress`, select it, and choose **Confirm: replaces** `run-homelab-through-sortie/kit-reads-homelab-egress` (ONS-20) in the workbench. [operator]

## 6. vm-chain-in-a-job

- [ ] 6.1 Approve `vm-chain-in-a-job` in the workbench. [operator]

## 8. Follow-ups from daemon-applies-egress's live proof

- [ ] 8.1 Approve `daemon-denies-private-answers`, then `daemon-private-as-allow-cidr`, in the workbench. [operator]
- [ ] 8.3 Approve `sandbox-follows-private-deny` once `homelab-egress-file` has merged. [operator]
"""

RDE = "repo-declared-egress"
RDE_LINKS = {
    "2.1": [f"{RDE}/sortie-reads-egress"],
    "3.1": [f"{RDE}/daemon-applies-egress"],
    "4.1": [f"{RDE}/homelab-egress-file"],
    "6.1": [f"{RDE}/vm-chain-in-a-job"],
    "8.1": [f"{RDE}/daemon-denies-private-answers", f"{RDE}/daemon-private-as-allow-cidr"],
    "8.3": [f"{RDE}/sandbox-follows-private-deny"],
}

# ortus-nova-specs supervisor-needs-no-oob/tasks.md, the change of this story.
SUPERVISOR_NEEDS_NO_OOB = """\
- [ ] 2.1 Approve `supervisor-wakes-on-change`, then `supervisor-spend-guard`, in the workbench. [operator]
- [ ] 3.1 Approve `fix-route-finds-pr`. [operator]
- [ ] 6.1 Approve `kit-credential-egress-reland`, select it, and choose **Confirm: replaces** `repo-declared-egress/kit-credential-egress` (ONS-121) in the workbench. [operator]
- [ ] 7.2 After 1.1 is confirmed, approve `standing-approval-class`. [operator]
- [ ] 10.1 Approve `kit-deploy-state-repositories`, `merge-and-prove-per-repository`, `merge-and-prove-fresh-review` and `supervisor-marks-own-fault-done`. [operator]
- [ ] 11.4 Approve `credential-revoked-restart-post-deploy`, select it, and choose **Confirm: replaces** `credential-revoked-restart` (ONS-141) in the workbench. [operator]
"""

SNO = "supervisor-needs-no-oob"
SNO_LINKS = {
    "2.1": [f"{SNO}/supervisor-wakes-on-change", f"{SNO}/supervisor-spend-guard"],
    "3.1": [f"{SNO}/fix-route-finds-pr"],
    "10.1": [f"{SNO}/kit-deploy-state-repositories", f"{SNO}/merge-and-prove-per-repository",
             f"{SNO}/merge-and-prove-fresh-review", f"{SNO}/supervisor-marks-own-fault-done"],
}


def links(text, change):
    return {t["number"]: t["approves"] for t in ms.parse_tasks(text, change)}


def test_the_six_repo_declared_egress_approval_tasks_are_linked():
    got = links(REPO_DECLARED_EGRESS, RDE)
    assert {n: k for n, k in got.items() if k} == RDE_LINKS
    # An agent task and a task that also asks for another step are not.
    assert got["2.2"] == [] and got["5.1"] == []


def test_this_changes_approval_tasks_are_linked_and_the_replace_tasks_are_not():
    got = links(SUPERVISOR_NEEDS_NO_OOB, SNO)
    assert {n: k for n, k in got.items() if k} == SNO_LINKS
    assert got["6.1"] == [] and got["11.4"] == [] and got["7.2"] == []


@pytest.mark.parametrize("text", [
    "Approve `a`, select it, and choose **Confirm: replaces** `b` (ONS-1) in the workbench.",
    "Approve `a` and record its pull request here.",
    "Approve the four stories in the workbench.",
    "Approve the stories in the workbench, `a` first.",
    "Read the measurement. If it nests: cancel `a`. If not: approve `a`.",
    "Approve `a` once it has merged. Then cancel `b`.",
    "Approve `--allow-cidr` in the workbench.",
    "Approved `a`.",
])
def test_a_task_that_asks_for_more_than_approvals_is_not_linked(text):
    assert ms.approval_link(text, "c") == []


def test_a_story_of_another_change_is_named_change_slash_id():
    assert ms.approval_link("Approve `a` and `other/b` in the workbench.", "c") == ["c/a", "other/b"]


def test_only_an_operator_task_is_linked():
    text = "- [ ] 1.1 Approve `a`.\n- [ ] 1.2 Approve `a`. [metal]\n- [ ] 1.3 Approve `a`. [operator]\n"
    assert links(text, "c") == {"1.1": [], "1.2": [], "1.3": ["c/a"]}


def egress_store(store, *ids):
    store.plan([("other", []), (RDE, [])])
    store.change("other", breakdown(deliverable("x")))
    store.change(RDE, breakdown(*(deliverable(i) for i in ids)))


def test_validate_holds_each_link_of_a_store_that_has_the_stories(store):
    egress_store(store, "sortie-reads-egress", "daemon-applies-egress", "homelab-egress-file", "vm-chain-in-a-job",
                 "daemon-denies-private-answers", "daemon-private-as-allow-cidr", "sandbox-follows-private-deny")
    store.tasks(RDE, REPO_DECLARED_EGRESS)
    r = validate(store.root, show=no_openspec)
    assert r.errors == []
    assert {t["number"]: t["approves"] for t in r.tasks[RDE] if t["approves"]} == RDE_LINKS


def test_validate_reports_a_link_to_a_story_the_store_does_not_hold(store):
    egress_store(store, "sortie-reads-egress")
    store.tasks(RDE, "- [ ] 2.1 Approve `sortie-reads-egress` and `sortie-reads-egres` in the workbench. [operator]\n"
                     "- [ ] 2.2 Approve `other/x`, then `other/y`. [operator]\n")
    r = validate(store.root, show=no_openspec)
    assert codes(r) == ["approval_task_link", "approval_task_link"]
    first, second = r.errors
    assert first.file == f"openspec/changes/{RDE}/tasks.md" and first.change == RDE
    assert first.members == (f"{RDE}#2.1", f"{RDE}/sortie-reads-egres") and "2.1" in first.message
    assert second.members == (f"{RDE}#2.2", "other/y")
