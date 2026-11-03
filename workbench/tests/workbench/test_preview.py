"""The preview of each review: ui story's pull request
(agent-platform/run-supervision#A UI story is built before it is approved,
and its merge is the approval), against the fake Sortie and the fake GitHub.
The deployer stands in for the kit's deploy command."""

from __future__ import annotations

import asyncio
import os
import stat

import pytest

from tests.workbench.fake_github import FakeGitHub
from tests.workbench.fake_sortie import FakeSortie
from workbench.preview import (MARKER, CommandDeployer, PreviewError, PreviewSync, Settings, preview_address,
                               read_previews, write_previews)

REPO = "ortusnova/agentic-orchestration"
PULL = f"{REPO}#80"
LIVE = "https://workbench.ortus.example"
KEY = "sortie-supervises-runs/worker-activity-view"


class FakeDeployer:
    def __init__(self):
        self.calls: list[tuple] = []
        self.fail = False

    async def up(self, preview):
        if self.fail:
            raise PreviewError("build failed")
        self.calls.append(("up", preview["identifier"], preview["branch"], preview["sha"], preview["port"], preview["url"]))

    async def down(self, identifier):
        self.calls.append(("down", identifier))


class Kit:
    """One ui story, T-1, with its pull request, and a pass of the previews."""

    def __init__(self, tmp_path, state="In Review", runtime=None):
        self.sortie = FakeSortie()
        self.node = self.sortie.issue(KEY, state=state, runtime=runtime, review="ui")
        self.github = FakeGitHub()
        self.deployer = FakeDeployer()
        self.record = tmp_path / "previews.json"
        self.settings = Settings(repo=REPO, record=self.record, live_url=LIVE, first_port=8800)

    def open_pull(self, sha="1" * 40, ci="success"):
        self.github.branch_pull(PULL, "sortie/T-1", sha)
        if ci:
            self.github.ci(sha, ci)

    def run(self):
        async def go():
            sortie = self.sortie.client()
            github = self.github.client()
            try:
                return await PreviewSync(self.settings, sortie.bind("T"), github, self.deployer).run_once()
            finally:
                await sortie.aclose()
                await github.aclose()

        return asyncio.run(go())

    def kit_comments(self):
        return [c for c in self.github.comments.get(PULL, []) if MARKER in c["body"]]


# Scenario: A UI story from spec to merge. Its review and CI passed, so its
# pull request is deployed and the address is commented on it, once.
def test_a_reviewed_green_pull_request_is_deployed_and_its_address_commented(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()

    record = kit.run()

    address = "https://workbench-t-1.ortus.example/"
    assert kit.deployer.calls == [("up", "T-1", "sortie/T-1", "1" * 40, 8800, address)]
    [comment] = kit.kit_comments()
    assert address in comment["body"]
    assert record["T-1"]["url"] == address and record["T-1"]["pull"] == PULL and record["T-1"]["plan_key"] == KEY
    # The live workbench reads the record.
    assert read_previews(kit.record)["T-1"]["url"] == address

    # The next pass changes nothing: same head, comment already there.
    kit.run()
    assert len(kit.deployer.calls) == 1 and len(kit.kit_comments()) == 1


@pytest.mark.parametrize("state,runtime,ci", [
    ("In Progress", {"status": "running"}, "success"),  # still building, or under review
    ("In Review", {"status": "retrying"}, "success"),  # a run is not over
    ("In Review", None, None),  # CI has not reported
    ("In Review", None, "failure"),  # CI failed: Sortie's fix run is next
])
def test_no_preview_before_the_review_and_ci_pass(tmp_path, state, runtime, ci):
    kit = Kit(tmp_path, state=state, runtime=runtime)
    kit.open_pull(ci=ci)

    assert kit.run() == {}

    assert kit.deployer.calls == [] and kit.kit_comments() == []
    assert not kit.record.exists()


def test_ci_still_running_is_not_a_pass(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull(ci=None)
    kit.github.ci("1" * 40, None)

    kit.run()

    assert kit.deployer.calls == []


def test_only_ui_stories_are_previewed(tmp_path):
    kit = Kit(tmp_path)
    kit.node["review"] = "criteria"
    kit.open_pull()

    kit.run()

    assert kit.deployer.calls == [] and kit.github.comments == {}


# Scenario: The operator asks for a change. Their comment starts a fix run
# (Sortie's part); the preview stays at the last head until the fix's run is
# over and CI passes on the new head, then follows the branch.
def test_the_preview_follows_the_branch(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.run()
    kit.github.say(PULL, "The worker column is missing.")

    # The fix run adds a commit; CI runs on it.
    kit.node.update(state="In Progress", runtime={"status": "running"})
    kit.github.push(PULL, "2" * 40)
    kit.run()
    kit.node.update(state="In Review", runtime=None)
    kit.github.ci("2" * 40, None)
    record = kit.run()
    assert [c[0] for c in kit.deployer.calls] == ["up"] and record["T-1"]["sha"] == "1" * 40

    kit.github.ci("2" * 40, "success")
    record = kit.run()

    assert kit.deployer.calls[-1] == ("up", "T-1", "sortie/T-1", "2" * 40, 8800, "https://workbench-t-1.ortus.example/")
    assert record["T-1"]["sha"] == "2" * 40
    # The address did not change, so the pull request gets no second comment.
    assert len(kit.kit_comments()) == 1


# Scenario: The operator is satisfied. The merge takes the preview down.
def test_a_merged_pull_request_removes_its_preview(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.run()

    kit.github.merge(PULL)
    kit.node.update(state="Done", terminal=True)
    record = kit.run()

    assert kit.deployer.calls[-1] == ("down", "T-1")
    assert record == {} and read_previews(kit.record) == {}


def test_a_closed_pull_request_removes_its_preview(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.run()

    kit.github.close(PULL)
    kit.run()

    assert kit.deployer.calls[-1] == ("down", "T-1") and read_previews(kit.record) == {}


def test_a_story_gone_from_the_graph_keeps_its_preview_until_the_pull_request_closes(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.run()
    kit.sortie.nodes.clear()

    kit.run()
    assert read_previews(kit.record)["T-1"]["sha"] == "1" * 40

    kit.github.close(PULL)
    kit.run()
    assert kit.deployer.calls[-1] == ("down", "T-1") and read_previews(kit.record) == {}


def test_a_failed_deploy_is_tried_again_next_pass(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.deployer.fail = True

    assert kit.run() == {}
    assert kit.kit_comments() == []

    kit.deployer.fail = False
    kit.run()
    assert len(kit.deployer.calls) == 1 and len(kit.kit_comments()) == 1


def test_a_comment_already_on_the_pull_request_is_not_repeated(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.github.say(PULL, f"{MARKER}\nPreview of T-1: https://workbench-t-1.ortus.example/", login="kit")

    kit.run()

    assert len(kit.kit_comments()) == 1 and read_previews(kit.record)["T-1"]["commented"] is True


def test_each_preview_gets_its_own_port(tmp_path):
    kit = Kit(tmp_path)
    write_previews(kit.record, {"T-9": {"url": "https://workbench-t-9.ortus.example/", "port": 8800, "number": 79,
                                        "sha": "9" * 40, "pull": f"{REPO}#79", "commented": True}})
    kit.github.branch_pull(f"{REPO}#79", "sortie/T-9", "9" * 40)
    kit.open_pull()

    record = kit.run()

    assert record["T-1"]["port"] == 8801 and record["T-9"]["port"] == 8800


def test_sortie_unreachable_changes_nothing(tmp_path):
    from workbench.sortie import SortieError

    kit = Kit(tmp_path)
    kit.open_pull()
    kit.sortie.unreachable = True

    with pytest.raises(SortieError):
        kit.run()
    assert kit.deployer.calls == []


@pytest.mark.parametrize("live,want", [
    ("https://workbench.ortus.example", "https://workbench-ons-93.ortus.example/"),
    ("https://workbench.ortus.example:8443/", "https://workbench-ons-93.ortus.example:8443/"),
    ("https://p1", "https://p1-ons-93/"),
    ("", "http://127.0.0.1:8801/"),
    ("http://127.0.0.1:8793", "http://127.0.0.1:8801/"),
])
def test_the_address_is_a_name_beside_the_live_one(live, want):
    assert preview_address(live, "ONS-93", 8801) == want


def test_the_deploy_command(tmp_path):
    log = tmp_path / "calls"
    script = tmp_path / "deploy"
    script.write_text(f"#!/bin/sh\necho \"$@\" >> {log}\n[ \"$1\" != fail ]\n")
    script.chmod(script.stat().st_mode | stat.S_IXUSR)
    deployer = CommandDeployer([str(script)])
    preview = {"identifier": "T-1", "branch": "sortie/T-1", "sha": "abc", "port": 8800, "url": "https://x/"}

    asyncio.run(deployer.up(preview))
    asyncio.run(deployer.down("T-1"))

    assert log.read_text().splitlines() == ["up T-1 sortie/T-1 abc 8800 https://x/", "down T-1"]
    with pytest.raises(PreviewError, match="fail"):
        asyncio.run(CommandDeployer([str(script), "fail"]).down("T-1"))


def test_an_unreadable_record_reads_as_none(tmp_path):
    path = tmp_path / "previews.json"
    assert read_previews(None) == {} and read_previews(path) == {}
    path.write_text("{not json")
    assert read_previews(path) == {}
    write_previews(path, {"T-1": {"url": "https://x/"}, "T-2": {"port": 1}})
    assert read_previews(path) == {"T-1": {"url": "https://x/"}}
    assert not [p for p in os.listdir(tmp_path) if p.startswith(".")]
