"""The workbench shows what each worker is doing and what waits on it
(agent-platform/run-supervision): every scenario against two fake Sorties,
p1's, which dispatches the project, and taichi's homelab Sortie."""

from __future__ import annotations

import json
from datetime import datetime, timedelta, timezone

import pytest
from fastapi.testclient import TestClient

from tests.conftest import deliverable
from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import billing
from tests.workbench.test_watch import validate_offline
from tests.workbench.test_workers import TOKEN, FakeBrokers, sse_events, update
from workbench.app import create_app
from workbench.config import Config, ConfigError
from workbench.fleet import READY_STUCK_SECONDS

QUESTION = "Which disk holds the seed image: the NVMe or the SATA one?"


def ago(seconds: float) -> str:
    return (datetime.now(timezone.utc) - timedelta(seconds=seconds)).isoformat().replace("+00:00", "Z")


def fleet_app(store, *, brokers: FakeBrokers | None = None):
    """billing's stories are T-1 (a3), T-2 (b1), T-3 (b2), T-4 (b3) and T-5 (b4) on
    both Sorties' tracker; p1 is this workbench's Sortie, taichi another."""
    billing(store, deliverable("b3", covers=["billing#Do b3"]), deliverable("b4", covers=["billing#Do b4"]))
    p1, taichi = FakeSortie(), FakeSortie()
    for key in ("accounts/a3", "billing/b1", "billing/b2", "billing/b3", "billing/b4"):
        p1.issue(key)
    taichi.max_agents = 2
    taichi.extra_state = {"host": {"name": "taichi", "capacity": {"cpu": 32, "mem_gib": 128, "disk_gib": 2000},
                                   "used": {"cpu": 8, "mem_gib": 20, "disk_gib": 200}, "unmanaged": []}}
    services = {"broker_transport": brokers.transport()} if brokers else {}
    app = create_app(make_config(store.root, sortie_name="p1"), validate=validate_offline, sortie=p1.client(),
                     peers={"taichi": taichi.client()}, **services)
    return app, p1, taichi


def workers(client) -> dict:
    r = client.get(f"{P}/workers")
    assert r.status_code == 200, r.text
    return {w["name"]: w for w in r.json()["workers"]}


def node(app, key):
    return next(n for n in project(app).hub.doc["nodes"] if n["plan_key"] == key)


# Scenario: A live proof running on taichi
def test_a_live_proof_running_on_taichi(store):
    app, p1, taichi = fleet_app(store)
    taichi.run("T-2", "s2", stage="build", broker="http://broker-2.test", phase="live_proof",
               step="seed-vm-recreate", step_at=ago(30), started_at=ago(1500))
    taichi.extra_state["pulls"] = [
        {"issue_identifier": ident, "number": n, "repository": "ortusnova/homelab", "url": f"https://github.com/ortusnova/homelab/pull/{n}",
         "waits_on": "live_proof", "reason": "its live proof needs a working vm chain", "since": ago(3000)}
        for ident, n in (("T-3", 152), ("T-4", 148), ("T-5", 161))]
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        t = workers(client)["taichi"]
        job = t["jobs"][0]
        assert (job["identifier"], job["key"], job["phase"], job["step"]) == ("T-2", "billing/b1", "live_proof", "seed-vm-recreate")
        assert 1490 <= job["elapsed"] <= 1600 and job["slot"] == 1
        waiting = {x["identifier"]: x for x in t["waits"] if x["kind"] == "live_proof"}
        assert set(waiting) == {"T-3", "T-4", "T-5"}
        assert all(x["reason"] == "its live proof needs a working vm chain" for x in waiting.values())
        assert {x["pull"]["number"] for x in waiting.values()} == {152, 148, 161}
        assert t["capacity"]["cpu"] == 32 and t["used"]["mem_gib"] == 20 and t["slots"] == 2 and t["busy"] == 1
        # The story panel says the same, and the graph carries it, so the view updates live.
        assert node(app, "billing/b2")["run_state"]["why"] == "its live proof needs a working vm chain"
        assert node(app, "billing/b2")["run_state"]["pull"]["waits_on"] == "live_proof"
        assert node(app, "billing/b1")["run_state"]["step"] == "seed-vm-recreate"
        assert project(app).hub.doc["fleet"]["workers"][1]["name"] == "taichi"
        # The step moves on: the next refresh shows it.
        taichi.running["T-2"]["step"] = "seed-vm-boot"
        settle(client)
        assert workers(client)["taichi"]["jobs"][0]["step"] == "seed-vm-boot"
        assert p1.requests and ("GET", "/api/v1/state", None) in taichi.requests


# Scenario: A pull request with nothing left to do
def test_a_pull_request_with_nothing_left_to_do(store):
    app, p1, _ = fleet_app(store)
    pull = {"issue_identifier": "T-2", "number": 77, "repository": "ortusnova/agentic-orchestration",
            "url": "https://github.com/ortusnova/agentic-orchestration/pull/77", "waits_on": "none", "since": ago(30)}
    p1.extra_state["pulls"] = [pull]
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        pulls = client.get(f"{P}/workers").json()["pulls"]
        assert [(p["identifier"], p["waits_text"], p["stuck"]) for p in pulls] == [("T-2", "ready to merge", False)]
        assert workers(client)["p1"]["verdict"] == "idle"
        p1.extra_state["pulls"] = [{**pull, "since": ago(READY_STUCK_SECONDS + 60)}]
        settle(client)
        w = workers(client)["p1"]
        assert w["verdict"] == "stuck" and w["reasons"][0]["code"] == "ready_to_merge"
        assert w["reasons"][0]["url"] == pull["url"]
        assert node(app, "billing/b1")["run_state"]["pull"]["waits_text"] == "ready to merge"


# Scenario: The home page's top row
def test_the_home_page_shows_the_slot_lanes_and_a_stuck_slot_says_why(store):
    app, p1, taichi = fleet_app(store)
    p1.run("T-3", "s3", stage="implement", started_at=ago(600))
    taichi.park("T-2", QUESTION, ago(900))
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        lanes = client.get(f"{P}/home").json()["lanes"]
        by = {w["name"]: w for w in lanes["workers"]}
        assert list(by) == ["p1", "taichi"]
        assert [len(w["lanes"]) for w in lanes["workers"]] == [3, 2]
        assert by["p1"]["lanes"][0]["job"]["identifier"] == "T-3" and by["p1"]["verdict"] == "moving"
        stuck = by["taichi"]["lanes"][0]["stuck"]
        assert stuck["identifier"] == "T-2" and stuck["action"] == "answer" and QUESTION in stuck["reason"]
        assert by["taichi"]["verdict"] == "stuck"
        assert lanes["verdict"] == "stuck" and lanes["busy"] == 1 and lanes["slots"] == 5
        assert any(r["worker"] == "taichi" and QUESTION in r["text"] for r in lanes["reasons"])
        # The question is also the Needs a person section's, which counts it.
        assert [q["identifier"] for q in client.get(f"{P}/home").json()["questions"]] == ["T-2"]
        assert "total" not in lanes


# Scenario: Unblock from the lane
def test_answer_in_taichis_lane_reaches_taichis_sortie(store):
    app, p1, taichi = fleet_app(store)
    taichi.park("T-2", QUESTION, ago(900))
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "the NVMe"})
        assert r.status_code == 200, r.text
        assert taichi.answers == [{"identifier": "T-2", "text": "the NVMe", "author": "Local operator"}]
        assert p1.answers == []
        # taichi's Sortie released the park, and runs the story again.
        taichi.run("T-2", "s2b", stage="build")
        settle(client)
        lane = workers(client)["taichi"]["lanes"][0]
        assert lane["stuck"] is None and lane["job"]["identifier"] == "T-2"


def test_more_sessions_in_p1s_lane_reach_p1s_sortie(store):
    app, p1, taichi = fleet_app(store)
    p1.budget_exhausted = [{"issue_identifier": "T-3", "reason": "session_budget", "used_sessions": 32, "budget_sessions": 32,
                            "exhausted_at": ago(300)}]
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        stuck = workers(client)["p1"]["lanes"][0]["stuck"]
        assert stuck["identifier"] == "T-3" and stuck["action"] == "sessions" and stuck["reason"] == "used 32 of its 32 sessions"
        assert node(app, "billing/b2")["run_state"]["action"] == "sessions"
        r = client.post(f"{P}/stories/sessions", json={"key": "billing/b2", "sessions": 4})
        assert r.status_code == 200, r.text
        assert r.json()["sortie"] == "p1"
        assert p1.grants == [{"identifier": "T-3", "sessions": 4, "author": "Local operator"}] and taichi.grants == []
        # p1's Sortie runs it again: the lane is no longer stuck.
        assert workers(client)["p1"]["lanes"][0]["stuck"] is None
        rows = client.get(f"{P}/workers/T-3/messages").json()["commands"]
        assert [(c["op"], c["text"]) for c in rows] == [("sessions", "4")]
        # A story the budget no longer holds is refused, and so is a silly grant.
        assert client.post(f"{P}/stories/sessions", json={"key": "billing/b2", "sessions": 4}).status_code == 409
        assert client.post(f"{P}/stories/sessions", json={"key": "billing/b2", "sessions": 0}).status_code == 400


def test_a_sortie_that_cannot_grant_sessions_says_so(store):
    app, p1, _ = fleet_app(store)
    p1.grants_supported = False
    p1.budget_exhausted = [{"issue_identifier": "T-3", "reason": "session_budget", "used_sessions": 32, "budget_sessions": 32}]
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/stories/sessions", json={"key": "billing/b2", "sessions": 4})
        assert r.status_code == 501 and r.json()["error"]["code"] == "grant_unsupported"


def test_open_on_a_job_shows_its_session_live_and_its_last_transcript_after(store):
    brokers = FakeBrokers()
    app, p1, taichi = fleet_app(store, brokers=brokers)
    taichi.run("T-4", "s4", stage="build", broker=brokers.add("broker-4.test", TOKEN, [update(1, "recreating the seed vm")]), token=TOKEN)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert node(app, "billing/b3")["runtime"]["watchable"] is True
        assert node(app, "billing/b3")["run_state"]["action"] == "open"
        r = client.post(f"{P}/workers/T-4/commands", json={"op": "steer", "text": "keep the old disk"})
        assert r.status_code == 200, r.text
        assert brokers.commands == [("broker-4.test", {"op": "steer", "text": "keep the old disk"})]
        project(app).workers.follow_poll = 0.01
        brokers.brokers["broker-4.test"]["after"] = lambda: taichi.end("T-4")
        assert "recreating the seed vm" in client.get(f"{P}/workers/T-4/events").text

        # After the run: its last transcript, from taichi's Sortie.
        taichi.history["T-4"] = {"dispatch_id": "d-4", "events": [
            {"seq": 1, "at": ago(60), "event": "notification", "message": "seed vm recreated", "text": "seed vm recreated"}]}
        settle(client)
        events = sse_events(client.get(f"{P}/workers/T-4/events").text)
        assert [e["event"] for e in events] == ["transcript", "ended"]
        last = json.loads(events[0]["data"])
        assert last["sortie"] == "taichi" and last["dispatch_id"] == "d-4" and last["events"][0]["text"] == "seed vm recreated"
        assert ("GET", "/api/v1/runs/d-4/events", None) in taichi.requests


# Scenario: A machine Sortie did not start
def test_a_machine_sortie_did_not_start(store):
    app, _, taichi = fleet_app(store)
    taichi.extra_state["host"]["unmanaged"] = [{"name": "brian-scratch", "since": ago(7200), "state": "running"}]
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        [m] = workers(client)["taichi"]["unmanaged"]
        assert m["name"] == "brian-scratch" and 7190 <= m["age"] <= 7300 and m["age_text"] == "2.0 h"


def test_an_unreachable_peer_is_said_and_the_rest_still_shows(store):
    app, p1, taichi = fleet_app(store)
    taichi.state_down = True
    p1.run("T-3", "s3")
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        doc = client.get(f"{P}/workers").json()
        assert {s["name"]: s["reachable"] for s in doc["sorties"]} == {"p1": True, "taichi": False}
        assert [w["name"] for w in doc["workers"]] == ["p1"]
        lanes = client.get(f"{P}/home").json()["lanes"]
        assert lanes["verdict"] == "stuck" and "taichi's Sortie is unreachable" in lanes["reasons"][0]["text"]


def test_other_sorties_are_configured_by_name(tmp_path):
    token = tmp_path / "taichi-token"
    token.write_text("secret\n")
    env = {"WORKBENCH_SORTIE_URL": "http://127.0.0.1:7680", "WORKBENCH_SECRET_KEY": "k", "WORKBENCH_PASSWORD_HASH": "h",
           "WORKBENCH_HOME": str(tmp_path), "WORKBENCH_SORTIE_NAME": "p1",
           "WORKBENCH_SORTIES": "taichi=http://127.0.0.1:7781/, mac-mini=https://mac.test",
           "WORKBENCH_SORTIE_TOKEN_TAICHI_FILE": str(token), "WORKBENCH_SORTIE_TOKEN_MAC_MINI": "t2"}
    c = Config.from_env(env)
    assert c.sortie_name == "p1"
    assert c.peer_sorties == (("taichi", "http://127.0.0.1:7781", "secret"), ("mac-mini", "https://mac.test", "t2"))
    for bad in ("taichi", "Taichi=http://x", "p1=http://x", "a=http://x,a=http://y", "a=ftp://x"):
        with pytest.raises(ConfigError):
            Config.from_env({**env, "WORKBENCH_SORTIES": bad})
