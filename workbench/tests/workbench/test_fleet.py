"""The fleet document: what each worker does and what waits on it
(agent-platform/run-supervision), from the states of p1's and taichi's
Sorties. These tests need nothing but the module."""

from __future__ import annotations

from workbench.fleet import READY_STUCK_SECONDS, build_fleet, lanes_summary, story_states

NOW = 1_793_700_000.0


def iso(t: float) -> str:
    from datetime import datetime, timezone

    return datetime.fromtimestamp(t, timezone.utc).isoformat().replace("+00:00", "Z")


def story(ident: str, key: str) -> dict:
    return {"kind": "story", "plan_key": key, "title": f"title of {key}", "tracker": {"identifier": ident}}


NODES = [story(i, f"homelab/{k}") for i, k in (("ONS-34", "vm-chain"), ("ONS-52", "node-a"), ("ONS-48", "node-b"),
                                                ("ONS-61", "node-c"), ("ONS-50", "question"), ("ONS-68", "budget"),
                                                ("ONS-70", "ready"), ("ONS-71", "queued"))]


def taichi_state(**extra) -> dict:
    """taichi's homelab Sortie, on its own host: two slots, ONS-34's live proof at seed-vm-recreate."""
    state = {
        "generated_at": iso(NOW),
        "config": {"agent": {"max_concurrent_agents": 2}},
        "host": {"name": "taichi", "capacity": {"cpu": 32, "mem_gib": 128, "disk_gib": 2000},
                 "used": {"cpu": 8, "mem_gib": 20, "disk_gib": 200}, "unmanaged": []},
        "running": [{"issue_identifier": "ONS-34", "issue_title": "vm chain", "rule_name": "build", "phase": "live_proof",
                     "step": "seed-vm-recreate", "step_at": iso(NOW - 40), "started_at": iso(NOW - 1500),
                     "session_id": "s34", "broker": {"http_url": "http://broker-34.test"}, "held": False}],
        "retrying": [], "budget_exhausted": [], "parked": [], "held": [],
        "pulls": [{"issue_identifier": i, "number": n, "repository": "ortusnova/homelab", "url": f"https://gh/{n}",
                   "waits_on": "live_proof", "reason": "its live proof needs a working vm chain", "since": iso(NOW - 3000)}
                  for i, n in (("ONS-52", 152), ("ONS-48", 148), ("ONS-61", 161))],
    }
    state.update(extra)
    return state


def p1_state(**extra) -> dict:
    state = {"generated_at": iso(NOW), "config": {"agent": {"max_concurrent_agents": 3}},
             "running": [], "retrying": [], "budget_exhausted": [], "parked": [], "held": []}
    state.update(extra)
    return state


def worker(fleet: dict, name: str) -> dict:
    return next(w for w in fleet["workers"] if w["name"] == name)


# Scenario: A live proof running on taichi
def test_a_live_proof_running_on_taichi():
    fleet = build_fleet([("p1", p1_state(), None), ("taichi", taichi_state(), None)], NODES, now=NOW)
    t = worker(fleet, "taichi")
    job = t["jobs"][0]
    assert job["identifier"] == "ONS-34" and job["key"] == "homelab/vm-chain"
    assert job["phase"] == "live_proof" and job["phase_text"] == "live proof"
    assert job["step"] == "seed-vm-recreate" and job["step_at"] == NOW - 40
    assert job["elapsed"] == 1500 and job["slot"] == 1 and job["watchable"]
    waits = [x for x in t["waits"] if x["kind"] == "live_proof"]
    assert [x["identifier"] for x in waits] == ["ONS-48", "ONS-52", "ONS-61"]
    assert all(x["reason"] == "its live proof needs a working vm chain" for x in waits)
    assert all(x["pull"]["waits_on"] == "live_proof" for x in waits)
    assert t["capacity"] == {"cpu": 32.0, "mem_gib": 128.0, "disk_gib": 2000.0} and t["used"]["cpu"] == 8.0
    assert t["slots"] == 2 and t["busy"] == 1 and t["verdict"] == "moving"
    assert [p["waits_text"] for p in fleet["pulls"]] == ["waits on a live proof"] * 3
    # p1 runs nothing: its own row, idle.
    assert worker(fleet, "p1")["verdict"] == "idle" and worker(fleet, "p1")["slots"] == 3


# Scenario: A pull request with nothing left to do
def test_a_pull_request_with_nothing_left_to_do_reads_ready_and_stuck_if_it_stays_so():
    fresh = {"issue_identifier": "ONS-70", "number": 170, "repository": "ortusnova/agentic-orchestration",
             "url": "https://gh/170", "waits_on": "none", "since": iso(NOW - 60)}
    fleet = build_fleet([("p1", p1_state(pulls=[fresh]), None)], NODES, now=NOW)
    pull = fleet["pulls"][0]
    assert pull["waits_text"] == "ready to merge" and not pull["stuck"]
    assert worker(fleet, "p1")["verdict"] == "idle"

    stale = {**fresh, "since": iso(NOW - READY_STUCK_SECONDS - 1)}
    fleet = build_fleet([("p1", p1_state(pulls=[stale]), None)], NODES, now=NOW)
    p1 = worker(fleet, "p1")
    assert fleet["pulls"][0]["stuck"] and p1["verdict"] == "stuck"
    assert p1["reasons"][0]["code"] == "ready_to_merge" and "ready to merge" in p1["reasons"][0]["text"]
    assert p1["reasons"][0]["url"] == "https://gh/170"
    assert story_states(fleet)["ONS-70"]["pull"]["waits_text"] == "ready to merge"


def unblock_states():
    taichi = taichi_state(parked=[{"issue_identifier": "ONS-50", "reason": "agent_blocked", "parked_at": iso(NOW - 900),
                                   "statement": "Which disk holds the seed image?"}])
    p1 = p1_state(budget_exhausted=[{"issue_identifier": "ONS-68", "reason": "session_budget", "used_sessions": 32,
                                     "budget_sessions": 32, "exhausted_at": iso(NOW - 300)}])
    return [("p1", p1, None), ("taichi", taichi, None)]


# Scenario: The home page's top row, and: Unblock from the lane
def test_a_stuck_slot_reads_stuck_in_its_own_lane_with_why_and_its_action():
    fleet = build_fleet(unblock_states(), NODES, now=NOW)
    t, p1 = worker(fleet, "taichi"), worker(fleet, "p1")
    # taichi: slot 1 runs the proof, so ONS-50's question holds slot 2.
    assert t["lanes"][1]["stuck"]["identifier"] == "ONS-50"
    assert t["lanes"][1]["stuck"]["action"] == "answer"
    assert "Which disk holds the seed image?" in t["lanes"][1]["stuck"]["reason"]
    assert t["verdict"] == "stuck" and t["reasons"][0]["text"].startswith("slot 2: question asks:")
    assert p1["lanes"][0]["stuck"]["identifier"] == "ONS-68" and p1["lanes"][0]["stuck"]["action"] == "sessions"
    assert p1["lanes"][0]["stuck"]["reason"] == "used 32 of its 32 sessions"
    assert p1["verdict"] == "stuck"
    assert fleet["where"] == {"ONS-34": "taichi", "ONS-50": "taichi", "ONS-68": "p1"}

    summary = lanes_summary(fleet, {"verdict": "healthy", "reasons": [{"code": "enough", "text": "fine"}], "runway": 4})
    assert summary["verdict"] == "stuck"
    assert {r["worker"] for r in summary["reasons"]} == {"p1", "taichi"}
    assert summary["busy"] == 1 and summary["slots"] == 5 and summary["runway"] == 4

    states = story_states(fleet)
    assert states["ONS-50"]["action"] == "answer" and states["ONS-50"]["sortie"] == "taichi"
    assert states["ONS-68"]["action"] == "sessions" and states["ONS-68"]["release"] == "more sessions"
    assert states["ONS-34"]["action"] == "open" and "seed-vm-recreate" in states["ONS-34"]["why"]
    assert states["ONS-52"]["status"] == "waiting" and states["ONS-52"]["pull"]["number"] == 152


def test_a_held_run_is_stuck_in_its_slot_and_opens_its_session():
    p1 = p1_state(running=[{"issue_identifier": "ONS-71", "rule_name": "review", "started_at": iso(NOW - 60), "held": True,
                            "hold_reason": "permission_request", "holder": "agent", "broker": {"http_url": "http://b"}}])
    w = worker(build_fleet([("p1", p1, None)], NODES, now=NOW), "p1")
    lane = w["lanes"][0]
    assert lane["job"]["phase"] == "review" and lane["stuck"]["kind"] == "permission" and lane["stuck"]["action"] == "open"
    assert w["verdict"] == "stuck"


# Scenario: A machine Sortie did not start
def test_a_machine_sortie_did_not_start_shows_with_its_name_and_age():
    host = {**taichi_state()["host"], "unmanaged": [{"name": "brian-scratch", "since": iso(NOW - 7200), "state": "running"}]}
    t = worker(build_fleet([("taichi", taichi_state(host=host), None)], NODES, now=NOW), "taichi")
    assert t["unmanaged"] == [{"name": "brian-scratch", "since": NOW - 7200, "age": 7200, "age_text": "2.0 h", "state": "running"}]


def test_remote_workers_their_leases_queue_and_drain():
    state = p1_state(
        running=[{"issue_identifier": "ONS-34", "rule_name": "build", "started_at": iso(NOW - 100), "step": {"name": "make test", "at": iso(NOW - 5)}}],
        workers={"workers": [{"id": "mac", "last_check_in": iso(NOW - 2),
                              "declaration": {"os": "darwin", "arch": "arm64", "kvm": False, "nested": False, "backends": ["smolvm"],
                                              "run_slots": 1, "running": 1, "free": {"cpu": 2, "mem_gib": 8, "disk_gib": 50},
                                              "used": {"cpu": 2, "mem_gib": 4, "disk_gib": 10},
                                              "unmanaged": [{"name": "hand-made", "since": iso(NOW - 60)}]}}],
                 "queue": [{"job": {"id": "ONS-71", "issue": {"identifier": "ONS-71"}, "requires": {"capabilities": ["kvm"]}},
                            "queued_at": iso(NOW - 30), "waiting": []}],
                 "leases": [{"job": "ONS-34", "attempt": 1, "worker": "mac"}]},
        dispatch_paused={"reason": "deploy of the kit", "paused_at": iso(NOW - 20)},
    )
    fleet = build_fleet([("p1", state, None)], NODES, now=NOW)
    mac = worker(fleet, "mac")
    assert mac["remote"] and mac["jobs"][0]["step"] == "make test" and mac["slots"] == 1
    assert mac["capacity"] == {"cpu": 4.0, "mem_gib": 12.0, "disk_gib": 60.0}
    kinds = {x["kind"]: x for x in mac["waits"]}
    assert kinds["capability"]["identifier"] == "ONS-71" and "kvm" in kinds["capability"]["reason"]
    assert "drain" in kinds and "1 job still running" in kinds["drain"]["reason"]
    assert mac["verdict"] == "draining" and mac["unmanaged"][0]["name"] == "hand-made"


def test_an_unreachable_sortie_reads_stuck_and_an_older_state_still_gives_slots():
    fleet = build_fleet([("p1", {"running": [{"issue_identifier": "ONS-34"}], "config": {"agent": {"max_concurrent_agents": 2}}}, None),
                         ("taichi", None, "connection refused")], NODES, now=NOW)
    p1 = worker(fleet, "p1")
    assert p1["slots"] == 2 and p1["busy"] == 1 and p1["jobs"][0]["phase"] == "build" and p1["jobs"][0]["step"] is None
    summary = lanes_summary(fleet, None)
    assert summary["verdict"] == "stuck" and "taichi's Sortie is unreachable" in summary["reasons"][0]["text"]


def test_issues_of_other_projects_are_not_shown():
    other = taichi_state(running=[{"issue_identifier": "XYZ-1", "started_at": iso(NOW)}], pulls=[])
    assert worker(build_fleet([("taichi", other, None)], NODES, now=NOW), "taichi")["jobs"] == []


def test_a_remote_sortie_with_no_worker_checked_in_still_shows_its_runs():
    state = p1_state(running=[{"issue_identifier": "ONS-34", "started_at": iso(NOW - 10)}], workers={"workers": [], "queue": [], "leases": []})
    p1 = worker(build_fleet([("p1", state, None)], NODES, now=NOW), "p1")
    assert p1["jobs"][0]["identifier"] == "ONS-34"


def test_the_fields_a_sortie_does_not_serve_are_named_not_shown_empty():
    full = taichi_state(waits=[])
    fleet = build_fleet([("taichi", full, None)], NODES, now=NOW)
    assert fleet["sorties"][0]["missing"] == []
    assert worker(fleet, "taichi")["known"] == {"use": True, "unmanaged": True, "steps": True}

    older = {"running": [{"issue_identifier": "ONS-34", "started_at": iso(NOW - 10)}], "config": {"agent": {"max_concurrent_agents": 2}}}
    fleet = build_fleet([("p1", older, None)], NODES, now=NOW)
    assert [m["field"] for m in fleet["sorties"][0]["missing"]] == ["host", "running[].step", "waits", "pulls"]
    assert all(m["text"] for m in fleet["sorties"][0]["missing"])
    assert worker(fleet, "p1")["known"] == {"use": False, "unmanaged": False, "steps": False}
    summary = lanes_summary(fleet, None)
    assert [(m["sortie"], m["field"]) for m in summary["missing"]][0] == ("p1", "host")

    remote = p1_state(waits=[], pulls=[], workers={"workers": [{"id": "mac", "declaration": {"run_slots": 1, "free": {"cpu": 2}}}]})
    fleet = build_fleet([("p1", remote, None)], NODES, now=NOW)
    assert [m["field"] for m in fleet["sorties"][0]["missing"]] == ["workers[].declaration.used", "workers[].declaration.unmanaged"]
    assert worker(fleet, "mac")["known"] == {"use": False, "unmanaged": False, "steps": True}
