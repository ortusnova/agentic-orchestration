"""The preview of each review: ui story's pull request
(agent-platform/run-supervision#A UI is approved as a prototype, and the
built story merges after its review), against the fake Sortie and the fake
GitHub. The deployer stands in for the kit's deploy command."""

from __future__ import annotations

import asyncio
import os
import stat

import pytest

from tests.workbench.fake_github import FakeGitHub
from tests.workbench.fake_sortie import FakeSortie
from workbench.preview import (CLICK_PROOFS, MARKER, CommandDeployer, FrontChecker, PreviewError, PreviewSync,
                               Settings, click_proofs_record, preview_address, preview_domain, public_address,
                               read_previews, write_previews)

REPO = "ortusnova/agentic-orchestration"
PULL = f"{REPO}#80"
LIVE = "https://workbench.ortus.example"
FRONT = "https://p1.ortus.example:8443"
ADDRESS = "http://t-1.preview.p1.ortus.example/"
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


class FakeChecker:
    """Stands in for the check through the control plane's front: every
    address answers but those in ``down``."""

    def __init__(self):
        self.down: set[str] = set()
        self.calls: list[str] = []

    async def answers(self, address):
        self.calls.append(address)
        return f"GET {address} answered 502 Bad Gateway" if address in self.down else None


class Kit:
    """One ui story, T-1, with its pull request, and a pass of the previews."""

    def __init__(self, tmp_path, state="In Review", runtime=None):
        self.sortie = FakeSortie()
        self.node = self.sortie.issue(KEY, state=state, runtime=runtime, review="ui")
        self.github = FakeGitHub()
        self.deployer = FakeDeployer()
        self.checker = FakeChecker()
        self.record = tmp_path / "previews.json"
        self.settings = Settings(repo=REPO, record=self.record, front_url=FRONT, live_url=LIVE, first_port=8800)

    def open_pull(self, sha="1" * 40, ci="success"):
        self.github.branch_pull(PULL, "sortie/T-1", sha)
        if ci:
            self.github.ci(sha, ci)

    def run(self):
        async def go():
            sortie = self.sortie.client()
            github = self.github.client()
            try:
                return await PreviewSync(self.settings, sortie.bind("T"), github, self.deployer, self.checker).run_once()
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

    address = ADDRESS
    assert kit.deployer.calls == [("up", "T-1", "sortie/T-1", "1" * 40, 8800, address)]
    [comment] = kit.kit_comments()
    assert address in comment["body"] and "1" * 40 in comment["body"]
    # It starts the review on the preview, and asks no one to approve or merge.
    assert CLICK_PROOFS in comment["body"] and "approve" not in comment["body"]
    assert click_proofs_record(comment["body"]) is None
    assert record["T-1"]["url"] == address and record["T-1"]["pull"] == PULL and record["T-1"]["plan_key"] == KEY
    # It was checked through the front before the comment; people get the
    # name beside the live workbench too, and no address is a loopback one.
    assert kit.checker.calls == [address] and record["T-1"]["answers"] is True
    assert "https://workbench-t-1.ortus.example/" in comment["body"] and "127.0.0.1" not in comment["body"]
    assert record["T-1"]["public_url"] == "https://workbench-t-1.ortus.example/"
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

    assert kit.deployer.calls[-1] == ("up", "T-1", "sortie/T-1", "2" * 40, 8800, ADDRESS)
    assert record["T-1"]["sha"] == "2" * 40
    # The new head gets its own comment, which starts the review on it.
    first, second = kit.kit_comments()
    assert "1" * 40 in first["body"] and "2" * 40 in second["body"]
    assert ADDRESS in second["body"] and record["T-1"]["commented"] == "2" * 40
    kit.run()
    assert len(kit.kit_comments()) == 2


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
    kit.github.say(PULL, f"{MARKER}\nPreview of T-1 at `{'1' * 40}`: {ADDRESS}", login="kit")

    kit.run()

    assert len(kit.kit_comments()) == 1 and read_previews(kit.record)["T-1"]["commented"] == "1" * 40


# Scenario: A built UI story. The review on the preview records its
# click-proofs for the head, which Sortie waits for; the record keeps it, so
# the workbench shows the story as merging.
def test_the_click_proofs_record_for_the_head_is_kept_with_the_preview(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.run()
    assert read_previews(kit.record)["T-1"]["click_proofs"] is None

    kit.github.say(PULL, f"Clicked every action on the preview.\n\n`{CLICK_PROOFS} {'1' * 12}`")
    record = kit.run()

    assert record["T-1"]["click_proofs"] == "1" * 40
    assert read_previews(kit.record)["T-1"]["click_proofs"] == "1" * 40

    # A fix pushed after the click-proofs needs them again.
    kit.node.update(state="In Progress", runtime={"status": "running"})
    kit.github.push(PULL, "2" * 40)
    record = kit.run()
    assert record["T-1"]["click_proofs"] is None and record["T-1"]["sha"] == "1" * 40


@pytest.mark.parametrize("body,login,kind", [
    (f"{CLICK_PROOFS} {'9' * 40}", "operator", "User"),  # another commit's
    (f"{CLICK_PROOFS} {'1' * 40}", "github-actions[bot]", "Bot"),  # a bot's, which Sortie does not read
    (f"{CLICK_PROOFS} 111", "operator", "User"),  # too short a prefix
    (f"Run the click-proofs, then: {CLICK_PROOFS} {'1' * 40}", "operator", "User"),  # not a line of its own
])
def test_what_is_not_a_click_proofs_record_for_the_head(tmp_path, body, login, kind):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.run()
    kit.github.say(PULL, body, login=login)
    kit.github.comments[PULL][-1]["user"]["type"] = kind

    assert kit.run()["T-1"]["click_proofs"] is None


@pytest.mark.parametrize("body,want", [
    (f"{CLICK_PROOFS} ABCDEF1", "abcdef1"),
    (f"> **{CLICK_PROOFS} abcdef1234.**", "abcdef1234"),
    (f"notes\n  `{CLICK_PROOFS} abcdef1`  \nmore", "abcdef1"),
    (f"{CLICK_PROOFS} not-a-sha", None),
    ("sortie:click-proofs failed abcdef1", None),
])
def test_a_click_proofs_record_is_read_as_sortie_reads_it(body, want):
    assert click_proofs_record(body) == want


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


# The address a job's machine on any worker opens: a name under the
# control plane's front, with STAGE0_WORKBENCH_URL or without it, and never
# a loopback one; people get a name beside the live workbench when it has
# one.
@pytest.mark.parametrize("live,public", [
    ("https://workbench.ortus.example", "https://workbench-ons-93.ortus.example/"),
    ("https://workbench.ortus.example:8443/", "https://workbench-ons-93.ortus.example:8443/"),
    ("https://p1", "https://p1-ons-93/"),
    ("", None),
    ("http://127.0.0.1:8793", None),
])
def test_the_address_is_the_fronts_with_or_without_a_live_name(live, public):
    for front in ("https://p1.ortus.example:8443", "https://P1.ortus.example/"):
        address = preview_address(front, "ONS-93")
        assert address == "http://ons-93.preview.p1.ortus.example/"
        assert "127.0.0.1" not in address and "localhost" not in address
    assert public_address(live, "ONS-93") == public
    assert preview_domain(FRONT) == "preview.p1.ortus.example"


@pytest.mark.parametrize("front", ["", "https://127.0.0.1:8443", "https://localhost:8443", "https://p1:8443"])
def test_a_front_with_no_domain_name_gives_no_address(front):
    with pytest.raises(PreviewError):
        preview_address(front, "ONS-93")


def test_without_STAGE0_WORKBENCH_URL_the_comment_holds_the_fronts_address(tmp_path):
    kit = Kit(tmp_path)
    kit.settings = Settings(repo=REPO, record=kit.record, front_url=FRONT, live_url="", first_port=8800)
    kit.open_pull()

    record = kit.run()

    [comment] = kit.kit_comments()
    assert ADDRESS in comment["body"] and "127.0.0.1" not in comment["body"] and "People reach" not in comment["body"]
    assert record["T-1"]["url"] == ADDRESS and record["T-1"]["public_url"] is None


# The kit comments the address only once the preview answers there through
# the route; one that does not is deployed again on each pass until it
# does, and no person is asked to.
def test_a_preview_that_does_not_answer_is_not_commented_and_is_deployed_again(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.checker.down.add(ADDRESS)

    record = kit.run()

    assert kit.deployer.calls == [("up", "T-1", "sortie/T-1", "1" * 40, 8800, ADDRESS)]
    assert kit.kit_comments() == []
    assert record["T-1"]["answers"] is False and "502" in record["T-1"]["answer_error"]
    assert record["T-1"]["commented"] is None

    # The next pass deploys it again, and still comments nothing.
    record = kit.run()
    assert [c[0] for c in kit.deployer.calls] == ["up", "up"] and kit.kit_comments() == []
    assert record["T-1"]["redeploys"] == 1

    # Once it answers, its address is commented, once.
    kit.checker.down.clear()
    record = kit.run()
    assert len(kit.kit_comments()) == 1 and record["T-1"]["commented"] == "1" * 40 and record["T-1"]["answers"] is True
    calls = len(kit.deployer.calls)
    kit.run()
    assert len(kit.deployer.calls) == calls and len(kit.kit_comments()) == 1


def test_a_commented_preview_that_stops_answering_is_deployed_again(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.run()
    kit.checker.down.add(ADDRESS)

    kit.run()
    assert [c[0] for c in kit.deployer.calls] == ["up", "up"]

    kit.checker.down.clear()
    record = kit.run()
    assert [c[0] for c in kit.deployer.calls] == ["up", "up"] and record["T-1"]["answers"] is True
    # Its head was commented already: no second comment.
    assert len(kit.kit_comments()) == 1


def test_a_failed_redeploy_is_tried_again_and_comments_nothing(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    kit.checker.down.add(ADDRESS)
    kit.run()
    kit.deployer.fail = True

    kit.run()
    assert kit.kit_comments() == []

    kit.deployer.fail = False
    kit.checker.down.clear()
    kit.run()
    assert len(kit.kit_comments()) == 1


# A preview the kit deployed at the loopback address it gave before the
# front's route moves to its address: deployed again there, checked, and
# commented anew, since the old comment's address does not open.
def test_a_preview_at_a_loopback_address_moves_to_the_fronts(tmp_path):
    kit = Kit(tmp_path)
    kit.open_pull()
    old = "http://127.0.0.1:8800/"
    write_previews(kit.record, {"T-1": {"identifier": "T-1", "branch": "sortie/T-1", "sha": "1" * 40, "port": 8800,
                                        "url": old, "pull": PULL, "number": 80, "commented": "1" * 40}})
    kit.github.say(PULL, f"{MARKER}\nPreview of T-1 at `{'1' * 40}`: {old}", login="kit")

    record = kit.run()

    assert kit.deployer.calls == [("up", "T-1", "sortie/T-1", "1" * 40, 8800, ADDRESS)]
    assert record["T-1"]["url"] == ADDRESS and record["T-1"]["commented"] == "1" * 40
    old_comment, new_comment = kit.kit_comments()
    assert old in old_comment["body"] and ADDRESS in new_comment["body"]


def test_the_deploy_command(tmp_path):
    log = tmp_path / "calls"
    script = tmp_path / "deploy"
    script.write_text(f"#!/bin/sh\necho \"$@\" >> {log}\n[ \"$1\" != fail ]\n")
    script.chmod(script.stat().st_mode | stat.S_IXUSR)
    deployer = CommandDeployer([str(script)])
    preview = {"identifier": "T-1", "branch": "sortie/T-1", "sha": "abc", "port": 8800, "url": ADDRESS}

    asyncio.run(deployer.up(preview))
    asyncio.run(deployer.up({**preview, "public_url": "https://workbench-t-1.ortus.example/"}))
    asyncio.run(deployer.down("T-1"))

    assert log.read_text().splitlines() == [f"up T-1 sortie/T-1 abc 8800 {ADDRESS}",
                                            f"up T-1 sortie/T-1 abc 8800 {ADDRESS} https://workbench-t-1.ortus.example/",
                                            "down T-1"]
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
