"""Sortie's cap on agents at once, every worker's together: at the cap,
stories that wait for a slot wait on the cap, so the lanes read full, not
stuck, even where a worker's lane is free; a free lane counts only while
the cap has room."""

from __future__ import annotations

from fastapi.testclient import TestClient

from tests.workbench.conftest import P, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import app_for, billing
from tests.workbench.test_fleet import NODES, NOW, iso, worker
from workbench.fleet import build_fleet, lanes_summary, story_states

WAITING = ("ONS-50", "ONS-68", "ONS-70", "ONS-71")


def remote_state(cap: int, running: list[str], waiting=WAITING) -> dict:
    """p1's one Sortie under the remote kind: p1's worker with 3 run slots,
    taichi's with 2, every job on p1, and stories that wait for a slot."""
    return {
        "generated_at": iso(NOW), "config": {"agent": {"max_concurrent_agents": cap}},
        "running": [{"issue_identifier": i, "worker": "p1", "started_at": iso(NOW - 60), "step": "build"} for i in running],
        "retrying": [], "budget_exhausted": [], "parked": [], "held": [], "pulls": [],
        "waits": [{"issue_identifier": i, "kind": "slot", "reason": f"No free agent slot: {len(running)} of {cap} running",
                   "release": "Released when a running session ends.", "since": iso(NOW - 30)} for i in waiting],
        "workers": {"workers": [{"id": "p1", "last_check_in": iso(NOW - 2), "declaration": {"run_slots": 3}},
                                {"id": "taichi", "last_check_in": iso(NOW - 2), "declaration": {"run_slots": 2}}],
                    "queue": [], "leases": []},
    }


def lanes_of(state: dict) -> tuple[dict, dict]:
    fleet = build_fleet([("p1", state, None)], NODES, now=NOW)
    return fleet, lanes_summary(fleet, None)


def test_at_the_cap_the_lanes_are_full_not_stuck():
    # The live case of 2026-11-03: a cap of 3, all 3 on p1, taichi's 2 slots free.
    fleet, summary = lanes_of(remote_state(3, ["ONS-34", "ONS-52", "ONS-48"]))
    assert summary["verdict"] == "full"
    assert summary["reasons"][0]["code"] == "cap_full"
    assert summary["reasons"][0]["text"] == ("p1: 4 stories wait for a slot: Sortie runs 3 agents, "
                                             "its cap of 3 at once (agent.max_concurrent_agents)")
    assert fleet["sorties"][0]["cap"] == {"limit": 3, "running": 3}
    t = worker(fleet, "taichi")
    assert t["verdict"] == "idle" and t["capped"]
    assert not any(r["code"] == "slot_idle" for r in t["reasons"])
    assert any(r["code"] == "cap_full" for r in t["reasons"])
    assert worker(fleet, "p1")["verdict"] == "moving"


def test_a_free_slot_under_the_cap_with_work_waiting_is_stuck():
    fleet, summary = lanes_of(remote_state(16, ["ONS-34", "ONS-52", "ONS-48"]))
    assert summary["verdict"] == "stuck"
    t = worker(fleet, "taichi")
    assert t["verdict"] == "stuck" and not t["capped"]
    assert [r["text"] for r in t["reasons"]] == ["4 stories wait for a slot while 2 slots are free"]


def test_only_the_cap_s_room_counts_as_free():
    # A cap of 4 with 3 running: one more agent may start, though taichi has 2 free lanes.
    fleet, summary = lanes_of(remote_state(4, ["ONS-34", "ONS-52", "ONS-48"]))
    assert summary["verdict"] == "stuck"
    assert [r["text"] for r in worker(fleet, "taichi")["reasons"]] == ["4 stories wait for a slot while 1 slot is free"]


def test_a_stuck_story_still_makes_the_lanes_stuck_at_the_cap():
    state = remote_state(3, ["ONS-34", "ONS-52", "ONS-48"], waiting=("ONS-70",))
    state["parked"] = [{"issue_identifier": "ONS-50", "worker": "taichi", "statement": "Which disk?", "parked_at": iso(NOW - 99)}]
    fleet, summary = lanes_of(state)
    assert summary["verdict"] == "stuck"
    assert any(r["code"] == "question" for r in summary["reasons"])


def test_at_the_cap_with_nothing_waiting_the_lanes_are_not_full():
    _, summary = lanes_of(remote_state(3, ["ONS-34", "ONS-52", "ONS-48"], waiting=()))
    assert summary["verdict"] == "healthy"


def test_the_home_lanes_follow_the_fake_sortie_s_cap(store):
    billing(store)
    fake = FakeSortie()
    fake.issue("billing/b1", state="In Progress")
    fake.issue("billing/b2")
    fake.run("T-1", "s1", worker="p1")
    fake.max_agents = 1
    fake.extra_state = {
        "waits": [{"issue_identifier": "T-2", "kind": "slot", "reason": "No free agent slot: 1 of 1 running",
                   "release": "Released when a running session ends.", "since": "2026-11-03T15:05:39Z"}],
        "workers": {"workers": [{"id": "p1", "declaration": {"run_slots": 3}}, {"id": "taichi", "declaration": {"run_slots": 2}}],
                    "queue": [], "leases": []},
    }
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        lanes = client.get(f"{P}/graph").json()["home"]["lanes"]
        assert lanes["verdict"] == "full"
        assert lanes["reasons"][0]["code"] == "cap_full"
        # The cap rises: taichi's free slots now take work, and stories still wait.
        fake.max_agents = 16
        settle(client)
        lanes = client.get(f"{P}/graph").json()["home"]["lanes"]
        assert lanes["verdict"] == "stuck"
        assert any(r["code"] == "slot_idle" for r in lanes["reasons"])


def test_a_queued_job_runs_on_no_worker_s_lane():
    # Live, 2026-11-03 16:15Z: under a cap of 16 Sortie lists 7 queued jobs
    # in running[] with no worker; every worker's slots are in use.
    state = remote_state(16, ["ONS-34", "ONS-52", "ONS-48"], waiting=("ONS-70", "ONS-71"))
    state["running"] += [{"issue_identifier": "ONS-61", "worker": "taichi", "started_at": iso(NOW - 60), "step": "build"},
                         {"issue_identifier": "ONS-50", "worker": "taichi", "started_at": iso(NOW - 60), "step": "build"}]
    state["running"] += [{"issue_identifier": i, "started_at": iso(NOW - 30)} for i in ("ONS-70", "ONS-71")]
    state["workers"]["queue"] = [{"job": {"id": i, "issue": {"identifier": i}}, "queued_at": iso(NOW - 30),
                                  "waiting": ["worker p1: all 3 run slots in use", "worker taichi: all 2 run slots in use"]}
                                 for i in ("ONS-70", "ONS-71")]
    fleet, summary = lanes_of(state)
    p1, t = worker(fleet, "p1"), worker(fleet, "taichi")
    assert (p1["busy"], len(p1["lanes"]), t["busy"], len(t["lanes"])) == (3, 3, 2, 2)
    assert fleet["sorties"][0]["cap"] == {"limit": 16, "running": 7}
    assert summary["verdict"] == "healthy" and summary["busy"] == 5
    states = story_states(fleet)
    assert states["ONS-70"]["status"] == "waiting" and states["ONS-70"]["kind"] == "slot"
