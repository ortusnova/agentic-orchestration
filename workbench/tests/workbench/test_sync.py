"""The approved plan syncs on its own (workbench-auto-sync)."""

from __future__ import annotations

import time

import planlib
from fastapi.testclient import TestClient

from workbench.app import create_app
from tests.conftest import breakdown, deliverable
from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_watch import validate_offline, wait_for

FAST = {"sync_backoff_first": 0.05, "sync_backoff_max": 0.2}


def setup(store, fake, **cfg):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("invoice", kind="enabler"), deliverable("tax", ["invoice"], kind="enabler")))
    return create_app(make_config(store.root, **{**FAST, **cfg}), validate=validate_offline, sortie=fake.client())


def previews(fake):
    return [r for r in fake.requests if r[1] == "/api/v1/plan/sync" and not r[2].get("apply")]


def apply_requests(fake):
    return [r for r in fake.requests if r[1] == "/api/v1/plan/sync" and r[2].get("apply")]


def sync_status(app):
    return project(app).sync.status()


def synced(app, after: int = 0) -> bool:
    """Wait until a sync that started after run ``after`` has succeeded."""
    return wait_for(lambda: sync_status(app)["runs"] > after and sync_status(app)["state"] == "synced", timeout=3)


def node(graph, key):
    return next(n for n in graph["nodes"] if n["plan_key"] == key)


def test_approval_syncs_and_starts_the_work_within_two_seconds(store):
    fake = FakeSortie()
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        assert synced(app)  # the startup sync: nothing approved yet
        assert fake.refreshes == 0
        n = len(fake.applied)

        started = time.monotonic()
        assert client.post(f"{P}/approve", json={"keys": ["billing/invoice"]}).status_code == 200
        assert wait_for(lambda: len(fake.applied) > n, timeout=2)
        assert time.monotonic() - started < 2
        graph = fake.applied[-1]["graph"]
        assert node(graph, "billing/invoice")["approval"] == "approved"
        assert node(graph, "billing/invoice")["release"] is True
        assert node(graph, "billing/invoice")["approved_by"]["name"] == "Local operator"

        assert wait_for(lambda: fake.refreshes == 1)
        assert synced(app)
        assert [r["plan_key"] for r in sync_status(app)["result"]["released"]] == ["billing/invoice"]


def test_a_failed_refresh_leaves_the_sync_successful(store):
    fake = FakeSortie()
    fake.refresh_down = True
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        assert synced(app)
        runs = sync_status(app)["runs"]
        client.post(f"{P}/approve", json={"keys": ["billing/invoice"]})
        assert wait_for(lambda: fake.refreshes == 1)
        assert synced(app, runs)
        assert sync_status(app)["reason"] is None


def test_a_stale_preview_is_retried_once(store):
    fake = FakeSortie()
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        assert synced(app)
        runs, tries = sync_status(app)["runs"], len(apply_requests(fake))
        fake.stale_once = True
        client.post(f"{P}/approve", json={"keys": ["billing/invoice"]})
        assert synced(app, runs)
        assert len(apply_requests(fake)) == tries + 2
        assert apply_requests(fake)[-1][2]["preview_id"] != apply_requests(fake)[-2][2]["preview_id"]


def test_a_missing_capability_stops_before_apply(store):
    fake = FakeSortie()
    fake.capabilities = ["create_issue", "set_parent"]
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        assert wait_for(lambda: sync_status(app)["state"] == "failed")
        reason = sync_status(app)["reason"]
        assert reason["code"] == "tracker_unsupported_operation" and "link_dependency" in reason["message"]
        time.sleep(0.4)
        assert apply_requests(fake) == [] and len(previews(fake)) == 1


def test_an_invalid_plan_is_not_synced(store):
    fake = FakeSortie()
    app = setup(store, fake)
    store.change("billing", breakdown(deliverable("a", ["a"], kind="enabler")))
    with TestClient(app) as client:
        sign_in(client)
        assert wait_for(lambda: sync_status(app)["state"] == "failed")
        assert sync_status(app)["reason"]["code"] == "plan_invalid"
        assert previews(fake) == []
        assert project(app).hub.doc["sync"]["reason"]["code"] == "plan_invalid"

        store.change("billing", breakdown(deliverable("a", kind="enabler")))
        settle(client)
        assert wait_for(lambda: sync_status(app)["state"] == "synced", timeout=3)


def test_a_draft_edit_does_not_sync_but_an_approved_story_edit_does(store):
    fake = FakeSortie()
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        client.post(f"{P}/approve", json={"epic": "billing"})
        assert wait_for(lambda: any(node(a["graph"], "billing/tax")["approval"] == "approved" for a in fake.applied), timeout=3)
        assert synced(app)
        time.sleep(0.2)
        n = len(previews(fake))

        doc = planlib.load_store(store.root).breakdowns["billing"].doc
        doc["deliverables"].append(deliverable("vat", kind="enabler"))
        store.change("billing", doc)
        settle(client)
        time.sleep(0.4)
        assert len(previews(fake)) == n

        doc["deliverables"][1]["depends_on"] = []
        store.change("billing", doc)
        settle(client)
        assert wait_for(lambda: len(previews(fake)) > n, timeout=2)


def test_story_actions_trigger_a_sync(store):
    fake = FakeSortie()
    fake.plan_states = {"release": "Todo", "backlog": "Backlog", "cancel": "Canceled", "done": "Done"}
    app = setup(store, fake)
    store.change("billing", breakdown(deliverable("invoice", kind="enabler"), deliverable("tax", ["invoice"], kind="enabler"),
                                      deliverable("tax2", supersedes="tax", kind="enabler")))
    fake.issue("billing/invoice")
    fake.issue("billing/tax")
    with TestClient(app) as client:
        sign_in(client)
        client.post(f"{P}/approve", json={"epic": "billing"})
        reasons = []
        loop = project(app).sync
        real = loop.trigger
        loop.trigger = lambda why="": (reasons.append(why), real(why))[1]

        assert client.post(f"{P}/stories/pull-back", json={"key": "billing/invoice"}).status_code == 200
        assert client.post(f"{P}/stories/supersede", json={"key": "billing/tax2"}).status_code == 200
        assert client.post(f"{P}/stories/cancel", json={"key": "billing/invoice"}).status_code == 200
        for why in ("pull-back", "supersede", "cancel"):
            assert why in reasons, reasons


def test_approve_triggers_a_sync(store):
    fake = FakeSortie()
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        reasons = []
        loop = project(app).sync
        real = loop.trigger
        loop.trigger = lambda why="": (reasons.append(why), real(why))[1]
        client.post(f"{P}/approve", json={"keys": ["billing/invoice"]})
        assert "approve" in reasons


def test_sortie_unreachable_then_back(store):
    fake = FakeSortie()
    fake.unreachable = True
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        assert wait_for(lambda: sync_status(app)["state"] == "failed")
        status = sync_status(app)
        assert status["reason"]["code"] == "sortie_unreachable" and status["retry_at"] is not None
        fake.unreachable = False
        assert wait_for(lambda: sync_status(app)["state"] == "synced", timeout=3)


def test_status_routes_and_retry(store):
    fake = FakeSortie()
    app = setup(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        assert synced(app)
        r = client.get(f"{P}/sync")
        assert r.status_code == 200 and r.json()["state"] == "synced"
        assert project(app).hub.doc["sync"]["state"] == "synced"
        runs = r.json()["runs"]
        r = client.post(f"{P}/sync/retry")
        assert r.status_code == 200
        assert synced(app, runs)
        # The confirmation routes are gone.
        assert client.post(f"{P}/sync/preview").status_code in (404, 405)
        assert client.post(f"{P}/sync/apply", json={"preview_id": "p1", "confirm": True}).status_code in (404, 405)
