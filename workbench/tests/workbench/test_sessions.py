from __future__ import annotations

import asyncio
import json
import os
import shutil
import sys
import time
from pathlib import Path

import httpx
import pytest

from workbench.app import create_app
from workbench.sessions import SessionManager
from tests.workbench.conftest import P, PASSWORD, make_config, project
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.live import live_server
from tests.workbench.test_watch import validate_offline

BROKER = os.environ.get("WORKBENCH_TEST_BROKER") or shutil.which("acp-broker")
FAKE_AGENT = Path(__file__).parent / "fixtures" / "fake_acp_agent.py"

pytestmark = pytest.mark.skipif(not BROKER, reason="set WORKBENCH_TEST_BROKER or put acp-broker on PATH")


def events(stream):
    """Yield (id, event) pairs from an SSE line iterator."""
    event_id = None
    for line in stream:
        if line.startswith("id: "):
            event_id = line[4:]
        elif line.startswith("data: "):
            yield event_id, json.loads(line[6:])
            event_id = None


def until(gen, predicate, limit=200):
    seen = []
    for pair in gen:
        seen.append(pair)
        if predicate(pair[1]):
            return pair, seen
        if len(seen) > limit:
            break
    raise AssertionError(f"event not seen; got {[e for _, e in seen][-5:]}")


def text_of(ev):
    if ev.get("type") != "update":
        return ""
    upd = ev.get("update") or ev.get("params", {}).get("update") or {}
    if "update" in ev.get("data", {}):
        upd = ev["data"]["update"]
    content = upd.get("content") or {}
    return content.get("text", "") if isinstance(content, dict) else ""


def flat(ev):
    return json.dumps(ev)


def make_app(store, tmp_path, model=""):
    prompt = tmp_path / "prompt.md"
    prompt.write_text("You plan in {{store}}.")
    manager = SessionManager(store.root, [BROKER], [sys.executable, str(FAKE_AGENT)], prompt,
                             runtime_dir=tmp_path / "rt", agent_model=model)
    app = create_app(make_config(store.root), validate=validate_offline, sortie=FakeSortie().client(), sessions=lambda *_: manager)
    return app, manager


def broker_pid(manager):
    return json.loads((manager.runtime_dir / "state.json").read_text())["broker_pid"]


def wait_dead(pid, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        time.sleep(0.05)
    return False


@pytest.fixture
def session_app(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path)
    yield app, manager
    asyncio.run(manager.stop())


def test_round_trip_permission_and_reattach(session_app):
    app, manager = session_app
    with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
        assert client.get(f"{P}/session").status_code == 401
        assert client.post("/api/login", json={"password": PASSWORD}).status_code == 200

        # A session starts only with the person's first message.
        assert client.post(f"{P}/session").status_code == 400
        assert client.post(f"{P}/session", json={"message": "  "}).status_code == 400
        assert client.get(f"{P}/session").json()["active"] is False

        started = client.post(f"{P}/session", json={"message": "plan billing"}).json()
        assert started["active"] is True
        assert client.post(f"{P}/session", json={"message": "again"}).status_code == 409
        pid = broker_pid(manager)
        rec = next((manager.runtime_dir / "registry").glob(f"{started['key']}.*.json"))
        assert json.loads(rec.read_text())["pid"] == pid

        with client.stream("GET", f"{P}/session/events") as r:
            gen = events(r.iter_lines())
            # One first turn: the planning prompt, with the store path, then the person's message.
            until(gen, lambda e: f"echo: You plan in {manager.store}. plan billing" in flat(e))

            assert client.post(f"{P}/session/commands", json={"op": "steer", "text": "hello agent"}).status_code == 200
            (last_id, _), _ = until(gen, lambda e: "echo: hello agent" in flat(e))

            client.post(f"{P}/session/commands", json={"op": "steer", "text": "ASK-PERMISSION please"})
            (_, perm), _ = until(gen, lambda e: e.get("type") == "permission")
            perm_id = perm.get("id") or perm.get("data", {}).get("id")
            assert perm_id is not None, perm
            r2 = client.post(f"{P}/session/commands", json={"op": "permit", "id": perm_id, "option": "allow"})
            assert r2.status_code == 200, r2.text
            until(gen, lambda e: "permission: " in flat(e) and "selected" in flat(e) and "allow" in flat(e))

        # A reload reattaches: a fresh stream replays the history, and one
        # with Last-Event-ID gets only what came after.
        with client.stream("GET", f"{P}/session/events") as r:
            until(events(r.iter_lines()), lambda e: "echo: hello agent" in flat(e))
        with client.stream("GET", f"{P}/session/events", headers={"Last-Event-ID": last_id}) as r:
            (_, ev), seen = until(events(r.iter_lines()), lambda e: "permission:" in flat(e))
            assert not any("echo: hello agent" in flat(e) for _, e in seen)

        assert client.post(f"{P}/session/commands", json={"op": "rm -rf"}).status_code == 400
        texts = [m["text"] for m in client.get(f"{P}/session").json()["messages"]]
        assert texts == ["plan billing", "hello agent", "ASK-PERMISSION please"]

        assert client.delete(f"{P}/session").json() == {"active": False}
        assert wait_dead(pid), "broker still running after the session ended"
        assert client.post(f"{P}/session/commands", json={"op": "state"}).status_code == 409


def test_session_survives_a_backend_restart(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path)
    try:
        with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            client.post(f"{P}/session", json={"message": "plan billing"})
            pid = broker_pid(manager)
            with client.stream("GET", f"{P}/session/events") as r:
                gen = events(r.iter_lines())
                until(gen, lambda e: "echo: You plan in " in flat(e))
                client.post(f"{P}/session/commands", json={"op": "steer", "text": "before restart"})
                until(gen, lambda e: "echo: before restart" in flat(e))
        # The first backend is gone: its server and lifespan have shut down.

        app2, manager2 = make_app(store, tmp_path)
        with live_server(app2) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            status = client.get(f"{P}/session").json()
            assert status["active"] is True and broker_pid(manager2) == pid
            assert [m["text"] for m in status["messages"]] == ["plan billing", "before restart"]
            with client.stream("GET", f"{P}/session/events") as r:
                gen = events(r.iter_lines())
                until(gen, lambda e: "echo: before restart" in flat(e))  # replayed history
                client.post(f"{P}/session/commands", json={"op": "steer", "text": "after restart"})
                until(gen, lambda e: "echo: after restart" in flat(e))
            assert client.delete(f"{P}/session").json() == {"active": False}
        assert wait_dead(pid)
    finally:
        asyncio.run(manager.stop())


def test_a_dead_session_reads_as_ended_and_restarts(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path)
    try:
        with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            client.post(f"{P}/session", json={"message": "plan billing"})
            pid = broker_pid(manager)
            os.kill(pid, 9)
            assert wait_dead(pid)
            deadline = time.monotonic() + 10
            while client.get(f"{P}/session").json()["active"] and time.monotonic() < deadline:
                time.sleep(0.05)
            assert client.get(f"{P}/session").json()["active"] is False
            assert client.post(f"{P}/session/commands", json={"op": "state"}).status_code == 409
            assert client.post(f"{P}/session", json={"message": "plan billing"}).json()["active"] is True
            assert broker_pid(manager) != pid
    finally:
        asyncio.run(manager.stop())


def wait_status(client, predicate, timeout=10):
    deadline = time.monotonic() + timeout
    status = client.get(f"{P}/session").json()
    while not predicate(status) and time.monotonic() < deadline:
        time.sleep(0.05)
        status = client.get(f"{P}/session").json()
    return status


def test_the_configured_model_is_selected_through_acp_before_the_first_turn(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path, model="fake/planner")
    try:
        with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            client.post(f"{P}/session", json={"message": "plan billing"})
            # The session shows the model once it is selected, never the agent's default before it.
            status = wait_status(client, lambda s: s.get("model") == "fake/planner")
            assert status["active"] is True and status["model"] == "fake/planner"
            with client.stream("GET", f"{P}/session/events") as r:
                until(events(r.iter_lines()), lambda e: "echo: You plan in " in flat(e))
    finally:
        asyncio.run(manager.stop())


def test_a_model_the_agent_does_not_offer_ends_the_session_with_the_reason(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path, model="fake/missing")
    try:
        with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            client.post(f"{P}/session", json={"message": "plan billing"})
            pid = broker_pid(manager)
            status = wait_status(client, lambda s: not s["active"])
            assert status["active"] is False
            assert "fake/missing" in status["error"]
            assert wait_dead(pid)
    finally:
        asyncio.run(manager.stop())


def test_a_person_chooses_model_and_thinking_and_the_next_session_keeps_them(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path, model="fake/default")
    try:
        with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            assert client.get(f"{P}/session/config").status_code == 409  # no session yet
            client.post(f"{P}/session", json={"message": "plan billing"})
            wait_status(client, lambda s: s.get("model"))
            with client.stream("GET", f"{P}/session/events") as r:
                until(events(r.iter_lines()), lambda e: "echo: You plan in " in flat(e))

            options = client.get(f"{P}/session/config").json()["options"]
            assert [(o["category"], o["currentValue"]) for o in options] == [("model", "fake/default")]
            assert [v["value"] for v in options[0]["options"]] == ["fake/default", "fake/planner"]  # groups flattened

            # A new model brings its own thinking levels, at that model's default.
            r = client.put(f"{P}/session/config", json={"category": "model", "value": "fake/planner"})
            assert r.status_code == 200, r.text
            by_cat = {o["category"]: o for o in r.json()["options"]}
            assert by_cat["thought_level"]["currentValue"] == "medium"
            assert [v["value"] for v in by_cat["thought_level"]["options"]] == ["low", "medium", "high"]

            r = client.put(f"{P}/session/config", json={"category": "thought_level", "value": "high"})
            assert {o["category"]: o["currentValue"] for o in r.json()["options"]} == {"model": "fake/planner", "thought_level": "high"}
            status = client.get(f"{P}/session").json()
            assert (status["model"], status["thought_level"]) == ("fake/planner", "high")

            # Refused: a value the agent does not offer, and an option a person may not set.
            assert client.put(f"{P}/session/config", json={"category": "thought_level", "value": "max"}).status_code == 409
            assert client.put(f"{P}/session/config", json={"category": "mode", "value": "plan"}).status_code == 400

            assert client.delete(f"{P}/session").json() == {"active": False}
            client.post(f"{P}/session", json={"message": "plan billing"})
            status = wait_status(client, lambda s: s.get("thought_level"))
            assert (status["model"], status["thought_level"]) == ("fake/planner", "high")
    finally:
        asyncio.run(manager.stop())


def test_a_session_from_an_older_driver_says_it_cannot_change_model(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path)
    try:
        with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            client.post(f"{P}/session", json={"message": "plan billing"})
            assert client.get(f"{P}/session").json()["can_configure"] is True
            # A driver from before model choice wrote no can_configure flag.
            path = manager.runtime_dir / "state.json"
            state = json.loads(path.read_text())
            del state["can_configure"]
            path.write_text(json.dumps(state))
            assert client.get(f"{P}/session").json()["can_configure"] is False
            r = client.get(f"{P}/session/config")
            assert r.status_code == 409 and "end it" in r.json()["error"]["message"]
            assert client.put(f"{P}/session/config", json={"category": "model", "value": "fake/planner"}).status_code == 409
    finally:
        asyncio.run(manager.stop())


def test_an_agent_message_larger_than_a_line_buffer_keeps_the_session(store, tmp_path):
    store.plan([])
    app, manager = make_app(store, tmp_path)
    try:
        with live_server(app) as base, httpx.Client(base_url=base, timeout=20) as client:
            client.post("/api/login", json={"password": PASSWORD})
            # The first turn is the driver's own prompt, so its updates reach the driver's reader.
            client.post(f"{P}/session", json={"message": "BIG-OUTPUT please"})
            with client.stream("GET", f"{P}/session/events") as r:
                until(events(r.iter_lines()), lambda e: "after big output" in flat(e))
            # The driver read the broker's oversized line and kept going.
            time.sleep(0.5)
            assert client.get(f"{P}/session").json()["active"] is True
            assert client.post(f"{P}/session/commands", json={"op": "state"}).status_code == 200
    finally:
        asyncio.run(manager.stop())
