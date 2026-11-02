"""A delivered story reads as done (agent-platform/delivered-work)."""

from __future__ import annotations

import asyncio
import json
import time

import httpx
import planlib
from fastapi.testclient import TestClient

from tests.conftest import breakdown, deliverable
from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_github import API, GITHUB_TOKEN, FakeGitHub
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_watch import specs_on_disk, validate_offline, wait_for
from workbench.config import Config, ConfigError
from workbench.delivery import DeliveryWatch, GitHubClient
from workbench.registry import Registry
from workbench.scaffold import upgrade_schemas
from workbench.app import create_app

STATES = {"release": "Todo", "backlog": "Backlog", "cancel": "Canceled", "done": "Done"}
C = "run-homelab-through-sortie"
IMAGE, PILOT, TAICHI, SHAPE = (f"{C}/{d}" for d in ("homelab-image", "homelab-pilot", "taichi-instance", "smolvm-machine-shape"))
PR = "ortusnova/homelab#85"
SHA = "0123456789abcdef0123456789abcdef01234567"
RECHECK = 300.0


def homelab(store, image_by=(PR,), taichi_by=("ortusnova/homelab#90",)):
    store.plan([(C, [])])
    store.change(C, breakdown(
        deliverable("homelab-image", delivered_by=list(image_by)),
        deliverable("taichi-instance", delivered_by=list(taichi_by)),
        deliverable("smolvm-machine-shape", covers=[f"{C}#The machine's user is a workflow setting"]),
        deliverable("homelab-pilot", ["taichi-instance", "smolvm-machine-shape"], covers=[f"{C}#A homelab story runs through Sortie"]),
    ))


class Clock:
    def __init__(self):
        self.now = time.monotonic()

    def __call__(self) -> float:
        return self.now


def app_for(store, fake, github, **cfg):
    config = make_config(store.root, delivery_recheck_seconds=RECHECK, **cfg)
    return create_app(config, validate=validate_offline, sortie=fake.client(), github=github.client())


def start(client) -> Clock:
    """Sign in, put the delivery watch on a clock the test moves, and look up now."""
    sign_in(client)
    rt = project(client.app)
    clock = Clock()
    rt.deliveries.clock = clock
    check(client)
    return clock


def check(client) -> None:
    rt = project(client.app)
    client.portal.call(rt.deliveries.check)
    settle(client)


def node(app, key):
    return next(n for n in project(app).hub.doc["nodes"] if n["plan_key"] == key)


def sync_graphs(fake):
    return [r[2]["graph"] for r in fake.requests if r[1] == "/api/v1/plan/sync" and r[2].get("apply")]


def sent(graph, key):
    return next(n for n in graph["nodes"] if n["plan_key"] == key)


def test_merged_by_hand(store):
    homelab(store)
    fake, github = FakeSortie(), FakeGitHub()
    fake.plan_states = STATES
    github.merge(PR, SHA)
    github.merge("ortusnova/homelab#90")
    app = app_for(store, fake, github)
    with TestClient(app) as client:
        start(client)
        n = node(app, IMAGE)
        assert n["plan_status"] == "delivered"
        assert n["delivery"]["state"] == "delivered"
        (p,) = n["delivery"]["pulls"]
        assert p["ref"] == PR and p["state"] == "merged"
        assert p["url"] == "https://github.test/ortusnova/homelab/pull/85"
        assert p["merge_commit"] == SHA and p["merge_commit_url"] == f"https://github.test/ortusnova/homelab/commit/{SHA}"

        # No Approve: the workbench refuses one, and the epic's approval leaves it out.
        r = client.post(f"{P}/approve", json={"keys": [IMAGE]})
        assert r.status_code == 409 and r.json()["error"]["code"] == "delivered_by"
        assert client.post(f"{P}/approve", json={"epic": C}).json()["approved"] == [SHAPE, PILOT]
        assert node(app, IMAGE)["plan_status"] == "delivered" and node(app, IMAGE)["approved_by"] is None

        # A sync does not release it.
        assert wait_for(lambda: any(sent(g, PILOT).get("release") for g in sync_graphs(fake)), timeout=3)
        for g in sync_graphs(fake):
            assert sent(g, IMAGE)["approval"] == "draft" and sent(g, IMAGE)["release"] is False
        assert IMAGE not in fake.released and TAICHI not in fake.released

        # It counts as done for its epic's progress.
        (m,) = project(app).hub.home["milestones"]
        assert m["progress"]["stories"] == {"done": 2, "total": 4}


def test_a_dependent_is_released(store):
    homelab(store)
    fake, github = FakeSortie(), FakeGitHub()
    fake.plan_states = STATES
    github.merge(PR)
    github.merge("ortusnova/homelab#90")
    app = app_for(store, fake, github)
    with TestClient(app) as client:
        start(client)
        assert node(app, TAICHI)["plan_status"] == "delivered"
        assert client.post(f"{P}/approve", json={"keys": [SHAPE, PILOT]}).status_code == 200
        assert node(app, SHAPE)["plan_status"] == "ready"
        pilot = node(app, PILOT)
        assert pilot["plan_status"] == "ready" and pilot["held_by"] == []

        assert wait_for(lambda: PILOT in fake.released, timeout=3)
        graph = sync_graphs(fake)[-1]
        assert sent(graph, PILOT)["release"] is True
        # Sortie holds no merge record for work done outside it: the delivered
        # story blocks nothing in the tracker, and is not written there.
        assert {(b["blocker"], b["blocked"]) for b in graph["blocks"]} == {(SHAPE, PILOT)}
        assert sent(graph, TAICHI)["approval"] == "draft"
        assert sent(graph, C)["approval"] == "approved"


def test_not_merged_yet(store):
    homelab(store)
    fake, github = FakeSortie(), FakeGitHub()
    fake.plan_states = STATES
    github.open(PR)
    github.open("ortusnova/homelab#90")
    app = app_for(store, fake, github)
    with TestClient(app) as client:
        clock = start(client)
        n = node(app, IMAGE)
        assert n["plan_status"] == "delivery_unconfirmed"
        assert n["delivery"]["state"] == "unconfirmed"
        assert [(p["ref"], p["state"], p["reason"]) for p in n["delivery"]["pulls"]] == [(PR, "open", "it is open")]

        # Not done: the dependent waits, and its epic counts nothing done.
        client.post(f"{P}/approve", json={"keys": [SHAPE, PILOT]})
        assert node(app, PILOT)["plan_status"] == "held" and node(app, PILOT)["held_by"] == [TAICHI]
        assert project(app).hub.home["milestones"][0]["progress"]["stories"]["done"] == 0
        assert github.lookups(PR) == 1

        # Merged: no lookup until the recheck is due, then Done, pushed to the open page.
        github.merge(PR, SHA)
        check(client)
        assert github.lookups(PR) == 1 and node(app, IMAGE)["plan_status"] == "delivery_unconfirmed"
        queue = project(app).hub.subscribe()
        clock.now += RECHECK
        check(client)
        assert github.lookups(PR) == 2
        assert node(app, IMAGE)["plan_status"] == "delivered"
        pushed = []
        while not queue.empty():
            pushed.append(queue.get_nowait())
        assert any(next(x for x in d["nodes"] if x["plan_key"] == IMAGE)["plan_status"] == "delivered" for d in pushed)

        # Never looked up again once merged; the open one still is, every recheck.
        clock.now += 10 * RECHECK
        check(client)
        assert github.lookups(PR) == 2 and github.lookups("ortusnova/homelab#90") == 3


def test_a_merge_is_remembered_across_restarts(store):
    homelab(store)
    github = FakeGitHub()
    github.merge(PR, SHA)
    github.merge("ortusnova/homelab#90")
    for _ in range(2):
        app = app_for(store, FakeSortie(), github)
        with TestClient(app) as client:
            start(client)
            assert node(app, IMAGE)["plan_status"] == "delivered"
            assert node(app, IMAGE)["delivery"]["pulls"][0]["merge_commit"] == SHA
    assert github.lookups(PR) == 1


def test_closed_without_merging_or_not_found(store):
    homelab(store, image_by=(PR, "ortusnova/homelab#86"), taichi_by=("ortusnova/gone#1",))
    fake, github = FakeSortie(), FakeGitHub()
    github.merge(PR)
    github.close("ortusnova/homelab#86")
    app = app_for(store, fake, github)
    with TestClient(app) as client:
        start(client)
        image = node(app, IMAGE)
        assert image["plan_status"] == "delivery_unconfirmed"
        assert [(p["ref"], p["state"]) for p in image["delivery"]["pulls"]] == [(PR, "merged"), ("ortusnova/homelab#86", "closed")]
        assert image["delivery"]["pulls"][1]["reason"] == "it was closed without merging"
        assert image["delivery"]["pulls"][1]["merge_commit"] is None
        taichi = node(app, TAICHI)
        assert taichi["plan_status"] == "delivery_unconfirmed"
        assert taichi["delivery"]["pulls"][0]["state"] == "unknown"
        assert "no such pull request" in taichi["delivery"]["pulls"][0]["reason"]


def run(coro):
    return asyncio.run(coro)


def test_a_lookup_that_fails_says_why():
    github = FakeGitHub()
    github.merge(PR)

    async def lookup(client: GitHubClient):
        try:
            return await client.pull(PR)
        finally:
            await client.aclose()

    assert run(lookup(github.client()))["state"] == "merged"
    refused = run(lookup(github.client(token="wrong")))
    assert refused["state"] == "unknown" and refused["reason"] == "GitHub answered 401: Bad credentials"
    github.status = 403
    assert run(lookup(github.client()))["reason"] == "GitHub answered 403: API rate limit exceeded"
    github.status = None
    github.unreachable = True
    down = run(lookup(github.client()))
    assert down["state"] == "unknown" and down["reason"].startswith("GitHub is unreachable")
    assert down["url"] == "https://github.com/ortusnova/homelab/pull/85"


def test_the_lookup_carries_the_token_from_the_environment(tmp_path):
    seen = []

    def handle(request: httpx.Request) -> httpx.Response:
        seen.append((str(request.url), request.headers.get("authorization")))
        return httpx.Response(200, json={"state": "open", "html_url": "https://github.com/o/r/pull/1"})

    env = {"WORKBENCH_HOME": str(tmp_path), "WORKBENCH_SORTIE_URL": "http://sortie", "WORKBENCH_PASSWORD_HASH": "x",
           "WORKBENCH_SECRET_KEY": "k", "WORKBENCH_GITHUB_TOKEN": GITHUB_TOKEN, "WORKBENCH_GITHUB_API_URL": API + "/"}
    config = Config.from_env(env)
    assert config.github_token == GITHUB_TOKEN and config.github_api_url == API
    assert config.delivery_recheck_seconds <= 540

    async def lookup():
        client = GitHubClient(config.github_api_url, config.github_token, transport=httpx.MockTransport(handle))
        try:
            return await client.pull("o/r#1")
        finally:
            await client.aclose()

    assert run(lookup())["state"] == "open"
    assert seen == [(f"{API}/repos/o/r/pulls/1", f"Bearer {GITHUB_TOKEN}")]
    defaults = Config.from_env({k: v for k, v in env.items() if not k.startswith("WORKBENCH_GITHUB")})
    assert defaults.github_token == "" and defaults.github_api_url == "https://api.github.com"
    for bad in ("0", "601", "soon"):
        try:
            Config.from_env({**env, "WORKBENCH_DELIVERY_RECHECK_SECONDS": bad})
        except ConfigError:
            continue
        raise AssertionError(f"accepted a recheck of {bad}")


def test_the_watch_looks_up_each_new_ref_once_per_recheck(tmp_path):
    github = FakeGitHub()
    github.open(PR)
    refs = {PR}
    clock = Clock()
    changes = []
    with Registry(tmp_path / "wb.db") as registry:
        watch = DeliveryWatch(github.client(), lambda: set(refs), registry=registry, recheck=RECHECK, clock=clock,
                              on_change=lambda: changes.append(1))

        async def scenario():
            assert watch.pulls()[PR]["state"] == "pending"
            assert await watch.check() is True
            assert await watch.check() is False
            refs.add("o/r#2")
            await watch.check()
            clock.now += RECHECK - 1
            await watch.check()
            assert (github.lookups(PR), github.lookups("o/r#2")) == (1, 1)
            clock.now += 1
            await watch.check()
            assert github.lookups(PR) == 2
            await watch.github.aclose()

        run(scenario())
    assert len(changes) == 2


def test_the_store_schema_that_predates_it_is_upgraded(store):
    """A store created before delivered_by refuses the field until the
    workbench writes its current schema in, which it does when it opens the project."""
    path = store.root / "openspec/schemas/planning/breakdown.schema.json"
    old = json.loads(path.read_text())
    del old["$defs"]["deliverable"]["properties"]["delivered_by"]
    path.write_text(json.dumps(old, indent=2))
    homelab(store)
    errs = planlib.load_store(store.root).errors
    assert {e.deliverable for e in errs} == {"homelab-image", "taichi-instance"}
    assert all("unknown field delivered_by" in e.message for e in errs)

    assert upgrade_schemas(store.root).paths == ["openspec/schemas/planning/breakdown.schema.json"]
    assert planlib.validate(store.root, show=specs_on_disk).ok


def test_an_approved_story_whose_delivery_is_not_confirmed_holds_its_dependents():
    """Approved before it named its pull request: until that merges, it is
    not done, and the sync neither releases it nor lets it go as approved."""
    from workbench.delivery import pending
    from workbench.plan_overlay import overlay, sync_graph

    graph = {"nodes": [{"plan_key": C, "kind": "epic", "change": C, "approval": "approved"}]
             + [{"plan_key": k, "kind": "story", "change": C, "approval": "approved"} for k in (TAICHI, SHAPE, PILOT)],
             "blocks": [{"blocker": TAICHI, "blocked": PILOT}, {"blocker": SHAPE, "blocked": PILOT}], "parents": []}
    stories = {TAICHI: {"delivered_by": [PR]}, SHAPE: {}, PILOT: {}}
    row = {"approver_name": "Ann", "approver_id": "a", "approved_at": 1, "digest": "d"}
    approvals = {k: row for k in (TAICHI, SHAPE, PILOT)}

    fields = overlay(graph, stories, approvals, {}, None, {PR: {**pending(PR), "state": "open", "reason": "it is open"}})
    assert fields[TAICHI]["plan_status"] == "delivery_unconfirmed"
    assert fields[PILOT]["plan_status"] == "held" and fields[PILOT]["held_by"] == [TAICHI]
    sent_graph = sync_graph(graph, fields)
    assert sent(sent_graph, TAICHI)["approval"] == "draft" and sent(sent_graph, TAICHI)["release"] is False
    assert sent(sent_graph, PILOT)["release"] is False

    fields = overlay(graph, stories, approvals, {}, None, {PR: {**pending(PR), "state": "merged", "reason": None}})
    assert fields[TAICHI]["plan_status"] == "delivered" and fields[PILOT]["plan_status"] == "ready"
    sent_graph = sync_graph(graph, fields)
    assert sent(sent_graph, PILOT)["release"] is True
    assert [(b["blocker"], b["blocked"]) for b in sent_graph["blocks"]] == [(SHAPE, PILOT)]
