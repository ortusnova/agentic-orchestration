"""The slot lanes in time: each slot's past runs from Sortie's run history,
packed so that no two overlap in a row, and the jobs that wait for a slot
projected onto the slots that free first on a worker that can take them."""

from __future__ import annotations

import json
import time

from fastapi.testclient import TestClient

from tests.workbench.conftest import P, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import app_for, billing
from tests.workbench.test_fleet import NODES, NOW, iso, story, worker
from workbench.fleet import build_fleet, lanes_summary
from workbench.timeline import DISPATCH_GAP as GAP, RunHistory, RunWorkers, rule_medians

MORE = NODES + [story(i, f"homelab/{k}") for i, k in (("ONS-80", "q1"), ("ONS-81", "q2"), ("ONS-82", "q3"),
                                                      ("ONS-83", "q4"), ("ONS-84", "nested"), ("ONS-90", "ran-a"),
                                                      ("ONS-91", "ran-b"), ("ONS-92", "ran-c"), ("ONS-93", "ran-d"),
                                                      ("ONS-94", "old"))]


def running(ident: str, w: str, start: float, end: float | None, rule: str = "build") -> dict:
    r = {"issue_identifier": ident, "worker": w, "rule_name": rule, "started_at": iso(start), "dispatch_id": f"d-{ident}",
         "step": "build"}
    if end is not None:
        r["estimated_end"] = iso(end)
    return r


def run(ident: str, start: float, end: float, status: str = "succeeded", rule: str = "build") -> dict:
    return {"issue_identifier": ident, "dispatch_id": f"d-{ident}", "rule_name": rule, "status": status,
            "started_at": iso(start), "completed_at": iso(end), "ssh_host": ""}


def state(runs: list[dict], waits: list[dict] | None = None) -> dict:
    """p1's one Sortie under the remote kind: p1's worker with 3 run slots, taichi's with 2."""
    return {
        "generated_at": iso(NOW), "config": {"agent": {"max_concurrent_agents": 16},
                                             "dispatch": {"rules": [{"name": "build", "selected_by": "catch_all"},
                                                                    {"name": "review", "selected_by": "stage",
                                                                     "stage": "sortie-stage:review"}]}},
        "running": runs, "retrying": [], "budget_exhausted": [], "parked": [], "held": [], "pulls": [],
        "waits": waits or [],
        "workers": {"workers": [{"id": "p1", "last_check_in": iso(NOW - 2), "declaration": {"run_slots": 3}},
                                {"id": "taichi", "last_check_in": iso(NOW - 2),
                                 "declaration": {"run_slots": 2, "nested": True}}],
                    "queue": [], "leases": []},
    }


def slot_wait(ident: str, since: float, worker: str | None = None, kind: str = "slot") -> dict:
    return {"issue_identifier": ident, "kind": kind, "reason": "worker p1: all 3 run slots in use",
            "release": "a worker that can run it frees a slot", "since": iso(since), **({"worker": worker} if worker else {})}


def history_with(past: list[dict], seen: list[tuple[str, str]]) -> RunHistory:
    h = RunHistory(RunWorkers())
    # The workbench saw each of these runs running on its worker.
    h.workers.observe("p1", {"running": [{"issue_identifier": i, "dispatch_id": f"d-{i}", "worker": w} for i, w in seen]})
    h.recent["p1"] = past
    h.medians["p1"] = rule_medians(past)
    return h


def lane_idents(w: dict, key: str) -> list[list[str]]:
    return [[x["identifier"] for x in lane[key]] for lane in w["lanes"]]


def test_past_runs_fill_each_worker_s_rows_with_no_two_overlapping():
    past = [run("ONS-90", NOW - 1700, NOW - 900), run("ONS-91", NOW - 1500, NOW - 700),
            run("ONS-92", NOW - 800, NOW - 100, status="failed"), run("ONS-93", NOW - 2000, NOW - 1000),
            # Ended before the window, and on a worker the workbench never saw.
            run("ONS-94", NOW - 9000, NOW - 8000), run("ONS-80", NOW - 1200, NOW - 300)]
    h = history_with(past, [("ONS-90", "p1"), ("ONS-91", "p1"), ("ONS-92", "p1"), ("ONS-93", "taichi"), ("ONS-94", "p1")])
    fleet = build_fleet([("p1", state([running("ONS-34", "p1", NOW - 600, NOW + 600)]), None)], MORE, now=NOW, history=h)
    p1 = worker(fleet, "p1")
    # Slot 1 holds the running job from NOW-600 on: ONS-90 ended before it, ONS-91
    # overlaps ONS-90, and ONS-92 overlaps both ONS-91 and the running job.
    assert lane_idents(p1, "past") == [["ONS-90"], ["ONS-91"], ["ONS-92"]]
    assert p1["lanes"][2]["past"][0]["status"] == "failed"
    assert p1["lanes"][0]["past"][0]["started_at"] == NOW - 1700 and p1["lanes"][0]["past"][0]["ended_at"] == NOW - 900
    assert lane_idents(worker(fleet, "taichi"), "past") == [["ONS-93"], []]
    assert fleet["sorties"][0]["unattributed"] == 1
    assert lanes_summary(fleet, None)["unattributed"] == 1


def test_a_run_more_than_the_slots_hold_at_once_is_counted_not_drawn():
    past = [run(i, NOW - 1000, NOW - 500) for i in ("ONS-90", "ONS-91", "ONS-92", "ONS-93")]
    h = history_with(past, [(i, "p1") for i in ("ONS-90", "ONS-91", "ONS-92", "ONS-93")])
    fleet = build_fleet([("p1", state([]), None)], MORE, now=NOW, history=h)
    p1 = worker(fleet, "p1")
    assert sum(len(lane["past"]) for lane in p1["lanes"]) == 3 and p1["past_hidden"] == 1


def test_a_repository_s_one_worker_survives_a_restart_but_draws_no_run_it_never_saw(tmp_path):
    w = RunWorkers(tmp_path / "w.json")
    w.observe("p1", {"retrying": [{"issue_identifier": "ONS-93", "issue_labels": HOMELAB}],
                     "waits": [slot_wait("ONS-93", NOW - 20, worker="taichi")]})
    again = RunWorkers(tmp_path / "w.json")
    assert again.eligible("p1", "ONS-93") == ["taichi"]
    # A run counted as running that no worker took ran nowhere, though only taichi could take it.
    assert again.worker_of("p1", run("ONS-93", NOW - 900, NOW - 100)) is None
    # Several workers can take homelab's jobs now: Sortie names none.
    again.observe("p1", {"waits": [slot_wait("ONS-93", NOW - 20)]})
    assert again.eligible("p1", "ONS-93") is None


def test_the_worker_of_a_run_survives_a_restart(tmp_path):
    path = tmp_path / "run-workers" / "p.json"
    RunWorkers(path).observe("p1", {"running": [{"issue_identifier": "ONS-90", "dispatch_id": "d-ONS-90", "worker": "taichi"},
                                                # Counted as running, but no worker took it.
                                                {"issue_identifier": "ONS-91", "dispatch_id": "d-ONS-91", "worker": ""}]})
    again = RunWorkers(path)
    assert again.worker_of("p1", {"dispatch_id": "d-ONS-90"}) == "taichi"
    assert again.worker_of("p1", {"dispatch_id": "d-ONS-91"}) is None
    assert list(json.loads(path.read_text())["runs"]) == ["p1/d-ONS-90"]


def forecast(runs: list[dict], waits: list[dict]) -> dict:
    # Succeeded build runs of 600, 1000 and 1400 seconds: a median of 1000.
    past = [run(f"ONS-9{i}", NOW - 90000 - d, NOW - 90000, rule="build") for i, d in enumerate((600, 1000, 1400))]
    h = history_with(past, [])
    return build_fleet([("p1", state(runs, waits), None)], MORE, now=NOW, history=h)


RUNS = [running("ONS-34", "p1", NOW - 600, NOW + 300), running("ONS-52", "p1", NOW - 300, NOW + 900),
        running("ONS-48", "taichi", NOW - 200, NOW + 1200)]
WAITS = [slot_wait("ONS-83", NOW - 10), slot_wait("ONS-80", NOW - 40), slot_wait("ONS-81", NOW - 30),
         slot_wait("ONS-82", NOW - 20, worker="taichi")]


def test_the_queue_is_projected_in_order_onto_the_slot_that_frees_first():
    fleet = forecast(RUNS, WAITS)
    p1, t = worker(fleet, "p1"), worker(fleet, "taichi")
    # In the order they began to wait: ONS-80 takes p1's free slot now, ONS-81
    # taichi's; ONS-82 only taichi can take: its slot 2 frees first, after
    # ONS-81's median; ONS-83 takes p1's slot that frees first, at NOW+300;
    # each a dispatch gap after the job before it.
    assert lane_idents(p1, "next") == [["ONS-83"], [], ["ONS-80"]]
    assert lane_idents(t, "next") == [[], ["ONS-81", "ONS-82"]]
    by = {x["identifier"]: x for w in (p1, t) for lane in w["lanes"] for x in lane["next"]}
    assert by["ONS-80"]["starts_at"] == NOW and by["ONS-80"]["ends_at"] == NOW + 1000
    assert by["ONS-81"]["starts_at"] == NOW
    assert by["ONS-82"]["starts_at"] == NOW + 1000 + GAP and by["ONS-82"]["ends_at"] == NOW + 2000 + GAP
    assert by["ONS-83"]["starts_at"] == NOW + 300 + GAP
    assert by["ONS-80"]["basis"] == "the median of 3 succeeded build runs"
    assert fleet["waiting"] == []


def test_the_forecast_flows_again_when_a_slot_opens():
    before = forecast(RUNS, WAITS)
    assert lane_idents(worker(before, "taichi"), "next") == [[], ["ONS-81", "ONS-82"]]
    # ONS-34 ends early: its p1 slot opens now, ONS-81 moves there, and
    # ONS-82 no longer waits behind it on taichi.
    after = forecast(RUNS[1:], WAITS)
    p1, t = worker(after, "p1"), worker(after, "taichi")
    assert lane_idents(p1, "next") == [["ONS-83"], ["ONS-80"], ["ONS-81"]]
    assert lane_idents(t, "next") == [[], ["ONS-82"]]
    by = {x["identifier"]: x for w in (p1, t) for lane in w["lanes"] for x in lane["next"]}
    assert by["ONS-81"]["starts_at"] == NOW and by["ONS-82"]["starts_at"] == NOW
    assert by["ONS-83"]["starts_at"] == NOW + 900 + GAP


def test_a_job_no_worker_can_take_is_not_projected_and_waits_with_its_reason():
    waits = [slot_wait("ONS-80", NOW - 40),
             {"issue_identifier": "ONS-84", "kind": "capability", "reason": "worker p1: no nested virtualization",
              "release": "a worker checks in that has gpu", "since": iso(NOW - 50)}]
    fleet = forecast(RUNS, waits)
    nexts = [x["identifier"] for w in fleet["workers"] for lane in w["lanes"] for x in lane["next"]]
    assert nexts == ["ONS-80"]
    assert [(x["identifier"], x["kind"], x["reason"]) for x in fleet["waiting"]] == [
        ("ONS-84", "capability", "worker p1: no nested virtualization")]
    assert lanes_summary(fleet, None)["waiting"][0]["release"] == "a worker checks in that has gpu"


def test_a_job_whose_only_worker_drains_waits_for_the_deploy():
    s = state(RUNS, [slot_wait("ONS-82", NOW - 20, worker="taichi"),
                     {"kind": "drain", "worker": "taichi", "reason": "dispatch is paused by deploy", "since": iso(NOW - 60)}])
    fleet = build_fleet([("p1", s, None)], MORE, now=NOW, history=history_with([], []))
    assert all(not lane["next"] for w in fleet["workers"] for lane in w["lanes"])
    assert fleet["waiting"][0]["reason"] == "taichi drains for a deploy and takes no new job"


def test_without_run_history_a_queued_job_takes_sortie_s_estimate_of_its_rule():
    fleet = build_fleet([("p1", state(RUNS, [slot_wait("ONS-80", NOW - 40)]), None)], MORE, now=NOW,
                        history=history_with([], []))
    x = worker(fleet, "p1")["lanes"][2]["next"][0]
    # Sortie's estimate of a running build: ONS-34 started at NOW-600, ends at NOW+300.
    assert x["estimate"] == 900 and x["basis"] == "Sortie's estimate of a build run"


def test_the_home_lanes_show_the_past_and_the_forecast_from_the_fake_sortie(store):
    billing(store)
    fake = FakeSortie()
    fake.issue("billing/b1", state="In Progress")
    fake.issue("billing/b2")
    fake.issue("billing/b3")
    now = time.time()
    fake.run("T-1", "s1", worker="p1", dispatch_id="d-T-1", started_at=iso(now - 120), estimated_end=iso(now + 480))
    fake.max_agents = 16
    fake.extra_state = {
        "waits": [{"issue_identifier": "T-2", "kind": "slot", "reason": "worker p1: all 1 run slots in use",
                   "release": "a worker that can run it frees a slot", "since": iso(now - 30)}],
        "workers": {"workers": [{"id": "p1", "declaration": {"run_slots": 1}}], "queue": [], "leases": []},
    }
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        # T-1 ends; T-3 ran on p1 before it, as the workbench saw.
        fake.run("T-3", "s3", worker="p1", dispatch_id="d-T-3", started_at=iso(now - 1500))
        settle(client)
        fake.end("T-3")
        fake.runs = [{"issue_identifier": "T-3", "dispatch_id": "d-T-3", "rule_name": "implement", "status": "succeeded",
                      "started_at": iso(now - 1500), "completed_at": iso(now - 300), "ssh_host": ""}]
        # The run history is read every 30 seconds; read it again now.
        project(app).hub.run_history._read.clear()
        settle(client)
        lanes = client.get(f"{P}/graph").json()["home"]["lanes"]
        p1 = lanes["workers"][0]
        assert [x["identifier"] for x in p1["lanes"][0]["past"]] == ["T-3"]
        assert [x["identifier"] for x in p1["lanes"][0]["next"]] == ["T-2"]
        assert p1["lanes"][0]["next"][0]["starts_at"] == p1["lanes"][0]["job"]["ends_at"] + GAP


HOMELAB = ["sortie-repository:homelab"]


def test_a_retry_is_projected_once_it_comes_due_on_the_worker_its_repository_needs():
    h = history_with([run(f"ONS-9{i}", NOW - 90000 - 1000, NOW - 90000) for i in range(3)], [])
    # Sortie named taichi as the one worker for a homelab job, ONS-84; a retry names none.
    seen = state(RUNS, [slot_wait("ONS-84", NOW - 20, worker="taichi")])
    seen["retrying"] = [{"issue_identifier": "ONS-84", "issue_labels": HOMELAB, "attempt": 1, "due_at": iso(NOW - 30)}]
    h.workers.observe("p1", seen)
    s = state(RUNS)
    s["retrying"] = [{"issue_identifier": "ONS-82", "issue_labels": HOMELAB, "attempt": 2, "due_at": iso(NOW + 600)},
                     {"issue_identifier": "ONS-80", "attempt": 1, "due_at": iso(NOW + 60)}]
    h.workers.observe("p1", s)
    fleet = build_fleet([("p1", s, None)], MORE, now=NOW, history=h)
    p1, t = worker(fleet, "p1"), worker(fleet, "taichi")
    by = {x["identifier"]: (w["name"], x) for w in (p1, t) for lane in w["lanes"] for x in lane["next"]}
    assert by["ONS-80"][0] == "p1" and by["ONS-80"][1]["starts_at"] == NOW + 60
    assert by["ONS-82"][0] == "taichi" and by["ONS-82"][1]["starts_at"] == NOW + 600


def test_while_dispatch_is_paused_for_a_deploy_the_queue_resumes_after_the_runs_end():
    s = state(RUNS, [slot_wait("ONS-80", NOW - 40)])
    s["dispatch_paused"] = {"since": iso(NOW - 60), "by": "operator", "reason": "deploy"}
    s["waits"].append({"kind": "drain", "worker": "p1", "reason": "dispatch is paused by operator", "since": iso(NOW - 60)})
    fleet = build_fleet([("p1", s, None)], MORE, now=NOW, history=history_with([], []))
    x = next(x for w in fleet["workers"] for lane in w["lanes"] for x in lane["next"])
    # The last run, ONS-48 on taichi, ends at NOW+1200: the deploy applies then.
    assert x["identifier"] == "ONS-80" and x["starts_at"] == NOW + 1200
    assert x["basis"].endswith("after the deploy, once the runs here end")
    assert fleet["waiting"] == []


def test_a_retry_that_needs_a_capability_goes_only_where_a_worker_declares_it():
    s = state(RUNS)
    s["retrying"] = [{"issue_identifier": "ONS-84", "attempt": 1, "due_at": iso(NOW - 5), "worker_wait": {"capabilities": ["nested"]}},
                     {"issue_identifier": "ONS-80", "attempt": 1, "due_at": iso(NOW - 5), "worker_wait": {"capabilities": ["gpu"]}}]
    fleet = build_fleet([("p1", s, None)], MORE, now=NOW, history=history_with([], []))
    assert lane_idents(worker(fleet, "taichi"), "next") == [[], ["ONS-84"]]
    assert [(x["identifier"], x["kind"], x["reason"]) for x in fleet["waiting"]] == [("ONS-80", "capability", "no worker has gpu")]


def test_retries_keep_their_order_while_sortie_moves_their_due_times_by_seconds():
    def order(shift: float) -> list[str]:
        s = state(RUNS)
        s["retrying"] = [{"issue_identifier": i, "attempt": 1, "dispatched_at": iso(NOW - d), "due_at": iso(NOW + shift + k)}
                         for k, (i, d) in enumerate((("ONS-81", 600), ("ONS-80", 900), ("ONS-83", 300)))]
        fleet = build_fleet([("p1", s, None)], MORE, now=NOW, history=history_with([], []))
        return [lane_idents(worker(fleet, n), "next") for n in ("p1", "taichi")]
    # The first dispatched takes the first free slot, wherever in the minute Sortie puts their retries.
    assert order(-20) == order(5) == [[["ONS-83"], [], ["ONS-80"]], [[], ["ONS-81"]]]
