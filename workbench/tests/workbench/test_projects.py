from __future__ import annotations

import planlib
import yaml
from fastapi.testclient import TestClient

from workbench.app import create_app
from tests.conftest import breakdown, deliverable
from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_watch import validate_offline, wait_for


def make(store, fake=None, **config):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("invoice")))
    fake = fake or FakeSortie()
    cfg = make_config(store.root, **config)
    return create_app(cfg, validate=validate_offline, sortie=fake.client()), fake, cfg


def create(client, name="Onboarding", slug="onboarding", key="ONB"):
    return client.post("/api/projects", json={"name": name, "slug": slug, "huly_project": key})


def test_the_configured_store_is_imported_once(store):
    app, _, cfg = make(store)
    with TestClient(app) as client:
        sign_in(client)
        listed = client.get("/api/projects").json()["projects"]
    assert [(p["slug"], p["huly_project"], p["store_path"]) for p in listed] == [("store", "T", str(store.root))]
    assert listed[0]["epics"] == 1 and listed[0]["valid"] is True

    again = create_app(cfg, validate=validate_offline, sortie=FakeSortie().client())
    with TestClient(again) as client:
        sign_in(client)
        assert len(client.get("/api/projects").json()["projects"]) == 1


def test_create_ensures_the_tracker_project_and_scaffolds_a_store(store):
    app, fake, cfg = make(store)
    with TestClient(app) as client:
        sign_in(client)
        r = create(client)
        assert r.status_code == 201, r.text
        body = r.json()
        assert body["tracker"]["created"] is True and body["tracker"]["ready"] is True
        assert body["project"]["slug"] == "onboarding" and body["project"]["name"] == "Onboarding"
        assert fake.projects["ONB"]["name"] == "Onboarding"

        root = cfg.home / "projects" / "onboarding"
        assert body["project"]["store_path"] == str(root)
        assert planlib.validate(root, show=lambda *_: None).errors == []

        slugs = [p["slug"] for p in client.get("/api/projects").json()["projects"]]
        assert slugs == ["onboarding", "store"]
        graph = client.get("/api/projects/onboarding/graph").json()
        assert graph["nodes"] == [] and graph["validation"]["valid"] is True

        detail = client.get("/api/projects/onboarding").json()
        assert detail["tracker"]["exists"] is True and detail["tracker"]["dispatched"] is False


def test_create_links_an_existing_tracker_project_and_reports_its_problems(store):
    fake = FakeSortie()
    fake.projects["OLD"] = {"name": "Old project", "problems": ['it has no task type "Epic"']}
    app, _, _ = make(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        body = create(client, "Legacy", "legacy", "OLD").json()
    assert body["tracker"]["created"] is False and body["tracker"]["ready"] is False
    assert body["tracker"]["problems"] == ['it has no task type "Epic"']


def test_create_without_a_slug_derives_one(store):
    app, _, _ = make(store)
    with TestClient(app) as client:
        sign_in(client)
        r = client.post("/api/projects", json={"name": "Billing v2!", "huly_project": "BIL"})
    assert r.status_code == 201 and r.json()["project"]["slug"] == "billing-v2"


def test_create_refusals_name_the_field_and_leave_nothing_behind(store):
    app, fake, cfg = make(store)
    with TestClient(app) as client:
        sign_in(client)
        cases = [
            (create(client, slug="Bad Slug"), 400, "invalid_slug", "slug"),
            (create(client, key="onb"), 400, "invalid_huly_project", "huly_project"),
            (create(client, name="  "), 400, "invalid_name", "name"),
            (create(client, slug="store"), 409, "slug_taken", "slug"),
            (create(client, key="T"), 409, "huly_project_taken", "huly_project"),
        ]
        for r, status, code, field in cases:
            assert r.status_code == status, r.text
            assert r.json()["error"]["code"] == code and r.json()["error"]["field"] == field

        (cfg.home / "projects" / "taken").mkdir(parents=True)
        r = create(client, slug="taken")
        assert r.status_code == 409 and r.json()["error"]["code"] == "store_exists"

        fake.ensure_error = (502, "tracker_transport_error")
        r = create(client)
        assert r.status_code == 502 and r.json()["error"]["field"] == "huly_project"
        assert not (cfg.home / "projects" / "onboarding").exists()
        assert [p["slug"] for p in client.get("/api/projects").json()["projects"]] == ["store"]

        # The failure left nothing reserved: a retry succeeds.
        fake.ensure_error = None
        assert create(client).status_code == 201


def test_two_projects_plan_and_sync_apart(store):
    app, fake, cfg = make(store)
    with TestClient(app) as client:
        sign_in(client)
        create(client)
        other = project(app, "onboarding")
        (other.store / "openspec" / "plan.yaml").write_text("schema_version: 1\nchanges:\n  - name: welcome\n    depends_on: []\n")
        change = other.store / "openspec" / "changes" / "welcome"
        change.mkdir()
        (change / "proposal.md").write_text("## Why\n\nwelcome\n")
        (change / "breakdown.yaml").write_text(yaml.safe_dump(breakdown(deliverable("greet", kind="enabler"))))
        settle(client, "onboarding")
        settle(client)

        keys = lambda slug: {n["plan_key"] for n in client.get(f"/api/projects/{slug}/graph").json()["nodes"]}  # noqa: E731
        assert keys("onboarding") == {"welcome", "welcome/greet"}
        assert keys("store") == {"billing", "billing/invoice"}

        assert client.post("/api/projects/onboarding/approve", json={"epic": "welcome"}).status_code == 200
        # Each project syncs into its own tracker project, on its own.
        onb = lambda: [a for a in fake.applied if a["project"] == "ONB"]  # noqa: E731
        assert wait_for(lambda: any(n.get("approval") == "approved" for a in onb() for n in a["graph"]["nodes"]), timeout=3)
        assert {n["plan_key"] for n in onb()[-1]["graph"]["nodes"]} == {"welcome", "welcome/greet"}
        assert all(a["project"] in ("ONB", "T") for a in fake.applied)

        settle(client, "onboarding")
        settle(client)
        assert project(app, "onboarding").hub.doc["sortie"]["reachable"] is True
        assert project(app, "onboarding").hub.doc["sortie"]["dispatched"] is False
        assert project(app).hub.doc["sortie"]["reachable"] is True
        assert project(app).hub.doc["sortie"]["dispatched"] is True


def test_rename_keeps_the_slug_and_delete_keeps_the_files(store):
    app, fake, cfg = make(store)
    with TestClient(app) as client:
        sign_in(client)
        create(client)
        r = client.patch("/api/projects/onboarding", json={"name": "Onboarding 2"})
        assert r.status_code == 200 and r.json()["project"]["name"] == "Onboarding 2"
        assert client.get("/api/projects/onboarding/graph").status_code == 200
        assert client.patch("/api/projects/onboarding", json={"name": " "}).status_code == 400

        r = client.delete("/api/projects/onboarding")
        assert r.status_code == 200
        assert r.json() == {"deleted": "onboarding", "store_path": str(cfg.home / "projects" / "onboarding"), "huly_project": "ONB"}
        assert (cfg.home / "projects" / "onboarding" / "openspec" / "plan.yaml").is_file()
        assert "ONB" in fake.projects
        assert [p["slug"] for p in client.get("/api/projects").json()["projects"]] == ["store"]
        assert client.get("/api/projects/onboarding/graph").status_code == 404

        # The slug is free again, but its directory is not.
        r = create(client)
        assert r.status_code == 409 and r.json()["error"]["code"] == "store_exists"


def test_an_unknown_project_is_404_everywhere(store):
    app, _, _ = make(store)
    with TestClient(app) as client:
        sign_in(client)
        for method, path in [("get", ""), ("get", "/graph"), ("get", "/validation"), ("get", "/events"),
                             ("get", "/sync"), ("post", "/sync/retry"), ("get", "/session"), ("delete", "")]:
            r = getattr(client, method)(f"/api/projects/nope{path}")
            assert r.status_code == 404 and r.json()["error"]["code"] == "project_not_found", (method, path, r.text)
        assert client.patch("/api/projects/nope", json={"name": "x"}).status_code == 404


def test_projects_need_sign_in(store):
    app, _, _ = make(store)
    with TestClient(app) as client:
        assert client.get("/api/projects").status_code == 401
        assert create(client).status_code == 401
        assert client.delete(f"{P}").status_code == 401


def test_config_from_env(tmp_path):
    import pytest

    from workbench.config import Config, ConfigError

    base = {"WORKBENCH_SORTIE_URL": "http://s/", "WORKBENCH_PASSWORD_HASH": "h", "WORKBENCH_SECRET_KEY": "k",
            "XDG_DATA_HOME": str(tmp_path / "data")}
    cfg = Config.from_env(base)
    assert cfg.home == (tmp_path / "data" / "plan-workbench").resolve() and cfg.store is None
    assert Config.from_env({**base, "WORKBENCH_HOME": str(tmp_path / "h")}).home == (tmp_path / "h").resolve()

    (tmp_path / "s" / "openspec").mkdir(parents=True)
    with pytest.raises(ConfigError, match="WORKBENCH_HULY_PROJECT"):
        Config.from_env({**base, "WORKBENCH_STORE": str(tmp_path / "s")})
    cfg = Config.from_env({**base, "WORKBENCH_STORE": str(tmp_path / "s"), "WORKBENCH_HULY_PROJECT": "DEV"})
    assert (cfg.store, cfg.huly_project) == ((tmp_path / "s").resolve(), "DEV")


def test_a_changes_requirements_for_the_story_card(store):
    spec = store.change("billing") / "specs" / "billing"
    spec.mkdir(parents=True)
    (spec / "spec.md").write_text("## ADDED Requirements\n\n### Requirement: Kept\nIt SHALL stay.\n\n#### Scenario: s\n- **WHEN** x\n- **THEN** y\n")
    app, _, _ = make(store)
    with TestClient(app) as client:
        assert client.get(f"{P}/changes/billing/requirements").status_code == 401
        sign_in(client)
        r = client.get(f"{P}/changes/billing/requirements")
        assert r.status_code == 200, r.text
        assert r.json() == {"change": "billing", "capabilities": {"billing": [{
            "name": "Kept", "operation": "ADDED", "text": "It SHALL stay.",
            "scenarios": [{"name": "s", "steps": [{"keyword": "WHEN", "text": "x"}, {"keyword": "THEN", "text": "y"}]}]}]}}
        (store.root / "openspec" / "changes" / "archive").mkdir(exist_ok=True)
        for bad in ("nope", "..", "Billing", "billing%2F..%2F..", "archive"):
            r = client.get(f"{P}/changes/{bad}/requirements")
            assert r.status_code == 404, (bad, r.text)
