"""A schema upgrade reaches a tracked store as a pull request (agent-platform/store-upkeep).

The upstream is a local bare repository: the store's remote names GitHub,
and git's ``insteadOf`` sends it to the bare one, as ``~/dev/ortus-nova-specs``
names github.com. GitHub itself is FakeGitHub."""

from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

import httpx
import pytest
from fastapi.testclient import TestClient

import workbench
from tests.conftest import breakdown, deliverable
from tests.workbench.conftest import P, make_config, settle, sign_in
from tests.workbench.fake_github import API, FakeGitHub
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_watch import validate_offline
from workbench.app import create_app
from workbench.delivery import GitHubClient, GitHubError
from workbench.scaffold import UPGRADE_BRANCH, Upstream, branch_name, push_upgrade, upgrade_schemas

GITHUB_URL = "https://github.com/ortusnova/ortus-nova-specs.git"
REPO = "ortusnova/ortus-nova-specs"
BREAKDOWN = "openspec/schemas/planning/breakdown.schema.json"
SHIPPED = Path(workbench.__file__).parent / "store_template" / "openspec" / "schemas" / "planning"
STORE_SYNC = Path(__file__).resolve().parents[3] / "dev" / "stage0" / "store-sync.sh"
C = "run-homelab-through-sortie"


def git(cwd: Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout


def commit_all(cwd: Path, message: str) -> None:
    git(cwd, "add", "-A")
    git(cwd, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", message)


def rev(cwd: Path, ref: str = "HEAD") -> str:
    return git(cwd, "rev-parse", ref).strip()


def predating_delivered_by(store) -> None:
    """A store whose breakdown schema predates ``delivered_by``, with a story that uses it."""
    path = store.root / BREAKDOWN
    old = json.loads(path.read_text())
    del old["$defs"]["deliverable"]["properties"]["delivered_by"]
    path.write_text(json.dumps(old, indent=2))
    store.plan([(C, [])])
    store.change(C, breakdown(deliverable("homelab-image", delivered_by=["ortusnova/homelab#85"])))


@pytest.fixture
def upstream(tmp_path, store) -> Path:
    """The store as ~/dev/ortus-nova-specs: main tracks origin/main, on GitHub."""
    predating_delivered_by(store)
    bare = tmp_path / "origin.git"
    git(tmp_path, "init", "-q", "--bare", "-b", "main", str(bare))
    git(store.root, "init", "-q", "-b", "main")
    commit_all(store.root, "The store")
    git(store.root, "config", f"url.{bare}.insteadOf", GITHUB_URL)
    git(store.root, "remote", "add", "origin", GITHUB_URL)
    git(store.root, "push", "-q", "-u", "origin", "main")
    return bare


def start(store, github: FakeGitHub) -> dict:
    """Run the workbench against the store once; returns the plan's validation."""
    app = create_app(make_config(store.root), validate=validate_offline, sortie=FakeSortie().client(),
                     github=github.client())
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        return client.get(f"{P}/graph").json()["validation"]


def store_sync(tmp_path: Path, store) -> None:
    home = tmp_path / "stage0"
    home.mkdir(exist_ok=True)
    subprocess.run([str(STORE_SYNC), str(store.root)], check=True, capture_output=True,
                   env={**os.environ, "STAGE0_HOME": str(home)})


def push_to_upstream(tmp_path: Path, bare: Path, name: str) -> Path:
    """Someone merges a change into origin/main."""
    other = tmp_path / name
    git(tmp_path, "clone", "-q", str(bare), str(other))
    (other / "openspec" / "changes" / C / "proposal.md").write_text(f"## Why\n\n{name}\n")
    commit_all(other, f"Change from {name}")
    git(other, "push", "-q", "origin", "main")
    return other


def test_a_newer_workbench_starts_against_a_tracked_store(tmp_path, store, upstream):
    head = rev(store.root)
    github = FakeGitHub()

    validation = start(store, github)

    # A story that uses the new field validates before the pull request merges.
    assert validation["valid"] is True, validation["errors"]
    # main stays equal to origin/main, with no tracked file changed.
    assert rev(store.root) == head == rev(store.root, "origin/main") == rev(upstream, "main")
    assert git(store.root, "status", "--porcelain") == ""
    assert "delivered_by" not in (store.root / BREAKDOWN).read_text()
    # One pull request carries the schema files, from a branch named after them.
    [pull] = github.opened
    assert (pull["repo"], pull["base"], pull["title"]) == (REPO, "main", "Update the planning schemas")
    assert pull["head"].startswith(UPGRADE_BRANCH)
    assert BREAKDOWN in pull["body"]
    assert git(upstream, "diff", "--name-only", "main", pull["head"]).split() == [BREAKDOWN]
    assert git(upstream, "show", f"{pull['head']}:{BREAKDOWN}") == (SHIPPED / "breakdown.schema.json").read_text()
    assert rev(upstream, f"{pull['head']}^") == head

    # The store timer keeps fast-forwarding main.
    other = push_to_upstream(tmp_path, upstream, "colleague")
    store_sync(tmp_path, store)
    assert rev(store.root) == rev(other) == rev(store.root, "origin/main")


def test_restarted_before_the_merge(store, upstream):
    github = FakeGitHub()
    assert start(store, github)["valid"] is True
    [pull] = github.opened

    assert start(store, github)["valid"] is True

    assert github.opened == [pull]
    assert github.pulls[pull["ref"]]["state"] == "open"
    assert git(upstream, "for-each-ref", "--format=%(refname)", "refs/heads/workbench/").split() == \
        [f"refs/heads/{pull['head']}"]


def test_a_closed_upgrade_is_not_opened_again(store, upstream):
    github = FakeGitHub()
    start(store, github)
    [pull] = github.opened
    github.close(pull["ref"])

    start(store, github)

    assert github.opened == [pull]


def test_once_merged_the_store_schemas_rule_again(tmp_path, store, upstream):
    github = FakeGitHub()
    start(store, github)
    [pull] = github.opened
    merger = tmp_path / "merger"
    git(tmp_path, "clone", "-q", str(upstream), str(merger))
    git(merger, "merge", "-q", "--ff-only", f"origin/{pull['head']}")
    git(merger, "push", "-q", "origin", "main")
    store_sync(tmp_path, store)
    assert (store.root / BREAKDOWN).read_text() == (SHIPPED / "breakdown.schema.json").read_text()

    assert start(store, github)["valid"] is True

    assert github.opened == [pull]
    assert git(store.root, "status", "--porcelain") == ""


def test_a_local_store(store):
    predating_delivered_by(store)
    git(store.root, "init", "-q", "-b", "main")
    commit_all(store.root, "The store")
    github = FakeGitHub()

    assert start(store, github)["valid"] is True

    assert git(store.root, "log", "-1", "--format=%s") == "Update the planning schemas\n"
    assert git(store.root, "status", "--porcelain") == ""
    assert (store.root / BREAKDOWN).read_text() == (SHIPPED / "breakdown.schema.json").read_text()
    assert github.opened == [] and not any(p.endswith("/pulls") for p in github.requests)


def test_a_tracked_store_whose_upstream_has_the_schemas_needs_no_branch(tmp_path, store, upstream):
    other = tmp_path / "other"
    git(tmp_path, "clone", "-q", str(upstream), str(other))
    (other / BREAKDOWN).write_bytes((SHIPPED / "breakdown.schema.json").read_bytes())
    commit_all(other, "Upgrade by hand")
    git(other, "push", "-q", "origin", "main")
    git(store.root, "fetch", "-q", "origin")

    up = upgrade_schemas(store.root)
    push_upgrade(store.root, up)

    assert up.paths == [BREAKDOWN] and not up.written and up.branch is None and not up.pushed
    assert set(up.replaced()) == {"breakdown"}
    assert git(upstream, "for-each-ref", "refs/heads/workbench/") == ""


def test_the_branch_is_named_after_the_files():
    a = {BREAKDOWN: b"one"}
    assert branch_name(a) == branch_name(dict(a))
    assert branch_name(a) != branch_name({BREAKDOWN: b"two"})
    assert branch_name(a).startswith(UPGRADE_BRANCH)


@pytest.mark.parametrize("url", [
    "https://github.com/ortusnova/ortus-nova-specs.git",
    "https://github.com/ortusnova/ortus-nova-specs",
    "https://token@github.com/ortusnova/ortus-nova-specs.git",
    "git@github.com:ortusnova/ortus-nova-specs.git",
    "ssh://git@github.com/ortusnova/ortus-nova-specs.git",
])
def test_a_github_remote(url):
    assert Upstream("main", "origin", "main", url).github_repo() == ("ortusnova", "ortus-nova-specs")


@pytest.mark.parametrize("url", ["/srv/git/specs.git", "https://gitlab.com/o/r.git", ""])
def test_not_a_github_remote(url):
    assert Upstream("main", "origin", "main", url).github_repo() is None


def test_an_unreachable_github_leaves_the_branch_for_the_next_start(store, upstream):
    github = FakeGitHub()
    github.unreachable = True
    assert start(store, github)["valid"] is True
    assert github.opened == []
    branches = git(upstream, "for-each-ref", "--format=%(refname)", "refs/heads/workbench/").split()
    assert len(branches) == 1

    github.unreachable = False
    start(store, github)

    [pull] = github.opened
    assert f"refs/heads/{pull['head']}" == branches[0]


def test_ensure_pull_reports_what_github_refuses():
    import asyncio

    def refuse(request: httpx.Request) -> httpx.Response:
        if request.method == "GET":
            return httpx.Response(200, json=[])
        return httpx.Response(403, json={"message": "Resource not accessible by integration"})

    async def run():
        client = GitHubClient(API, "t", transport=httpx.MockTransport(refuse))
        try:
            with pytest.raises(GitHubError, match="403: Resource not accessible"):
                await client.ensure_pull("o", "r", "b", "main", "t", "")
        finally:
            await client.aclose()

    asyncio.run(run())
