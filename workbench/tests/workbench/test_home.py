"""The project home: execution health, not ready to execute, and the home document."""

from __future__ import annotations

import json

import httpx
import planlib
import pytest
from fastapi.testclient import TestClient

from tests.conftest import breakdown, deliverable, milestone
from tests.planlib.test_checks import no_openspec
from tests.workbench.conftest import P, PASSWORD, make_config, project, settle, sign_in
from tests.workbench.live import live_server
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import STATES, app_for
from tests.workbench.test_watch import validate_offline
from workbench.app import create_app
from workbench.config import Config
from workbench.home import History, build_home, execution_health

# 2.1 Execution health -----------------------------------------------------------


def state(running=(), slots=3, **extra):
    return {"running": [{"issue_identifier": i, "held": False, "hold_reason": "", "holder": ""} for i in running],
            "config": {"agent": {"max_concurrent_agents": slots}}, **extra}


def health(**kw):
    args = dict(reachable=True, sortie_error=None, dispatched=True, project="T", state=state(), state_error=None,
                sync=None, runway=0, remaining=5, key_of={})
    args.update(kw)
    return execution_health(**args)


def test_free_slots_and_nothing_ready_is_idle_now():
    h = health()
    assert h["verdict"] == "idle_now"
    assert h["slots"] == 3 and h["busy"] == 0 and h["free"] == 3 and h["runway"] == 0
    assert h["reasons"][0]["text"] == "3 agent slots are free and 0 stories are ready"


def test_a_held_run_is_stuck_naming_the_story_and_why():
    s = state(["T-1"])
    s["running"][0].update(held=True, hold_reason="permission_request", holder="human")
    h = health(state=s, key_of={"T-1": "billing/b1"})
    assert h["verdict"] == "stuck"
    assert h["reasons"] == [{"code": "held", "text": "billing/b1 is held: permission request", "key": "billing/b1"}]


def test_a_run_out_of_budget_or_parked_is_stuck():
    h = health(state=state(budget_exhausted=[{"issue_identifier": "T-2", "reason": "token_budget"}],
                           parked=[{"issue_identifier": "T-3", "reason": ""}]))
    assert h["verdict"] == "stuck"
    assert [r["text"] for r in h["reasons"]] == ["T-2 is held: token budget", "T-3 stopped for a person without saying why"]


def test_a_parked_story_names_its_question_never_only_stuck():
    s = state(parked=[{"issue_identifier": "T-3", "reason": "agent_blocked", "statement": "Which repo…"}])
    h = health(state=s, key_of={"T-3": "apw/worker-daemon"})
    assert h["reasons"] == [{"code": "question", "text": "apw/worker-daemon asks: Which repo…", "key": "apw/worker-daemon"}]
    # The whole statement, when the hub read it from the issue.
    h = health(state=s, key_of={"T-3": "apw/worker-daemon"}, statements={"T-3": "Which repository should the daemon live in?"})
    assert h["reasons"][0]["text"] == "apw/worker-daemon asks: Which repository should the daemon live in?"


def test_healthy_when_the_runway_covers_the_slots():
    h = health(state=state(["T-1", "T-2", "T-3"]), runway=4)
    assert h["verdict"] == "healthy" and h["runway"] == 4 and h["slots"] == 3 and h["free"] == 0
    assert "4 stories against 3 agent slots" in h["reasons"][0]["text"]


def test_will_idle_when_fewer_wait_than_there_are_slots():
    h = health(state=state(["T-1", "T-2", "T-3"]), runway=1)
    assert h["verdict"] == "will_idle"
    assert h["reasons"][0]["text"] == "1 story ready, fewer than the 3 agent slots"


def test_done_when_no_planned_story_is_left():
    assert health(remaining=0)["verdict"] == "done"
    # Stuck still wins over done.
    assert health(remaining=0, sync={"state": "failed", "reason": {"message": "tracker down"}})["verdict"] == "stuck"


def test_sortie_unreachable_or_a_failed_sync_is_stuck():
    h = health(reachable=False, sortie_error="connection refused", state=None)
    assert h["verdict"] == "stuck" and h["reasons"][0]["code"] == "sortie_unreachable"
    assert "connection refused" in h["reasons"][0]["text"]
    assert h["slots"] is None and h["runway"] is None
    h = health(sync={"state": "failed", "reason": {"code": "x", "message": "tracker down"}})
    assert h["verdict"] == "stuck" and h["reasons"][0]["text"] == "The last sync failed: tracker down"
    h = health(state=None, state_error="503")
    assert h["verdict"] == "stuck" and h["reasons"][0]["code"] == "state_unavailable"


def test_no_verdict_without_the_sources_to_support_one():
    assert health(dispatched=False, state=None)["verdict"] is None
    assert health(state={"running": []})["verdict"] is None


def test_history_keeps_the_most_busy_and_the_newest_runway_per_hour():
    h = History(hours=3, bucket=100)
    h.add(1, 5, now=1060)
    h.add(3, 4, now=1070)
    h.add(0, 2, now=1150)
    b = h.buckets(now=1200)
    assert [(x["busy"], x["runway"]) for x in b] == [(None, None), (3, 4), (0, 2)]


# 2.2 Not ready to execute --------------------------------------------------------


def platform(store):
    """worker-daemon (spec'd, draft) and control-plane-images (spec'd,
    approved) start the chain; homelab-instance has no spec and needs
    control-plane-images; the gate needs everything."""
    store.plan([("apw", []), ("later", ["apw"]), ("bare", []), ("ops", [])])
    store.change("apw", breakdown(
        deliverable("worker-daemon", kind="enabler"),
        deliverable("control-plane-images", kind="enabler"),
        deliverable("homelab-instance", ["control-plane-images"]),
        deliverable("stage-1", ["worker-daemon", "homelab-instance"], kind="enabler"),
        deliverable("gate", ["stage-1"], kind="enabler"),
    ))
    store.change("later", breakdown(*(deliverable(f"t{i}", kind="enabler") for i in range(3))))
    store.change("bare")
    store.change("ops")
    store.tasks("apw", "- [x] 1.1 code\n- [ ] 1.2 Enroll the YubiKey. [operator]\n")
    store.tasks("ops", "- [ ] 2.1 Order hardware. [operator]\n- [ ] 2.2 Rack it. [metal]\n")
    store.milestones([
        milestone("m1", [{"change": "apw"}, {"change": "bare"}], gate="apw/gate", title="M1: vm"),
        milestone("m2", [{"change": "later"}, {"change": "ops", "tasks": "operator"}], ["m1"], goal=True, title="M2: metal"),
    ])


def home_of(store, statuses=None, done=(), **health_kw):
    r = planlib.validate(store.root, show=no_openspec)
    assert r.errors == [], r.errors
    statuses = statuses or {}
    nodes = []
    for n in r.graph["nodes"]:
        k = n["plan_key"]
        tracker = {"identifier": f"T-{k}", "state": "Done" if k in done else "Todo", "terminal": k in done} if k in done or statuses.get(k) == "released" else None
        nodes.append({**n, "status": "synced" if tracker else "draft", "tracker": tracker, "runtime": None})
    fields = {n["plan_key"]: {"plan_status": statuses.get(n["plan_key"], "draft"), "held_by": []}
              for n in r.graph["nodes"] if n["kind"] == "story"}
    hi = dict(reachable=True, sortie_error=None, dispatched=True, project="T", state=state(), state_error=None, sync=None)
    hi.update(health_kw)
    return build_home(graph=r.graph, nodes=nodes, fields=fields,
                      planning={"milestones": r.milestones, "declared": r.milestones_declared, "tasks": r.tasks},
                      plan_states=STATES, tracker_known=True, valid=True, errors=0, health_inputs=hi)


def items_by_key(home):
    return {i["key"]: i for i in home["not_ready"]}


def test_ranked_by_slack(store):
    platform(store)
    home = home_of(store, {"apw/control-plane-images": "ready"})
    items = items_by_key(home)
    wd, hi = items["apw/worker-daemon"], items["apw/homelab-instance"]
    assert wd["reason"] == "not_approved" and wd["slack"] == 0 and wd["urgency"] == "now"
    assert hi["reason"] == "no_spec" and hi["slack"] == 1 and hi["urgency"] == "soon"
    assert wd["rank"] < hi["rank"]
    # The longest chain to the gate runs through control-plane-images.
    assert hi["critical"] and not wd["critical"]


def test_grouped_by_what_a_person_does(store):
    platform(store)
    home = home_of(store, {"apw/control-plane-images": "ready"})
    items = items_by_key(home)
    assert items["apw/worker-daemon"]["column"] == "approve"
    assert items["apw/homelab-instance"]["column"] == "spec"
    yubikey = items["apw#1.2"]
    assert yubikey["column"] == "operator" and yubikey["title"] == "Enroll the YubiKey."
    assert yubikey["milestone"] == "m1"
    ranks = sorted(i["rank"] for i in home["not_ready"] if i["column"])
    assert ranks == list(range(1, len(ranks) + 1))
    assert home["needs_person"]["total"] == len(ranks)


def test_each_reason(store):
    platform(store)
    home = home_of(store, {"apw/control-plane-images": "ready", "apw/stage-1": "held", "apw/worker-daemon": "needs_rereview"})
    items = items_by_key(home)
    assert items["bare"]["reason"] == "no_breakdown" and items["bare"]["column"] == "spec"
    assert items["apw/worker-daemon"]["reason"] == "needs_rereview"
    assert items["apw/stage-1"]["reason"] == "held_for_approval" and items["apw/stage-1"]["column"] == "approve"
    # The gate is approved, but an open person task in its milestone blocks it.
    home = home_of(store, {"apw/control-plane-images": "ready", "apw/gate": "ready"})
    gate = items_by_key(home)["apw/gate"]
    assert gate["reason"] == "waits_on_person" and gate["task"]["text"] == "Enroll the YubiKey."
    # With the person task done, the gate waits on work only, ranked with no column.
    store.tasks("apw", "- [x] 1.1 code\n- [x] 1.2 Enroll the YubiKey. [operator]\n")
    gate = items_by_key(home_of(store, {"apw/control-plane-images": "ready", "apw/gate": "ready"}))["apw/gate"]
    assert gate["reason"] == "waits_on_work" and gate["column"] is None and gate["rank"] is None


def test_a_change_reviewed_as_one(store):
    platform(store)
    home = home_of(store, {"apw/control-plane-images": "ready"}, done={"apw/worker-daemon", "apw/homelab-instance",
                                                                       "apw/stage-1", "apw/gate", "apw/control-plane-images"})
    items = items_by_key(home)
    later = items["later"]
    assert later["kind"] == "change" and later["column"] == "approve"
    assert later["stories"] == ["later/t0", "later/t1", "later/t2"]
    assert later["action"] == {"kind": "review", "epic": "later", "keys": ["later/t0", "later/t1", "later/t2"]}
    assert not any(k.startswith("later/") for k in items)


def test_an_operator_task_waits_for_the_milestones_before_it(store):
    platform(store)
    home = home_of(store, {"apw/control-plane-images": "ready"})
    task = items_by_key(home)["ops#2.1"]
    # m2 counts ops's operator tasks only; m1's five open stories run first.
    assert task["milestone"] == "m2" and task["after_milestones"] == ["M1"]
    assert "ops#2.2" not in items_by_key(home)
    assert task["slack"] == 5 and task["urgency"] == "later"


def test_a_milestone_declared_done_is_not_work_ahead(store):
    store.plan([("m0", []), ("ops", [])])
    store.change("m0", breakdown(deliverable("old", kind="enabler")))
    store.change("ops")
    store.tasks("ops", "- [ ] 1.1 Rack it. [operator]\n")
    store.milestones([milestone("zero", [{"change": "m0"}], done=True),
                      milestone("one", [{"change": "ops", "tasks": "operator"}], ["zero"])])
    home = home_of(store)
    items = items_by_key(home)
    assert "m0/old" not in items
    assert items["ops#1.1"]["slack"] == 0 and items["ops#1.1"]["after_milestones"] == []


def test_milestones_timeline_and_critical_path(store):
    platform(store)
    home = home_of(store, {"apw/control-plane-images": "ready"}, done={"apw/control-plane-images"})
    assert home["next"] == "m1" and home["goal"] == "m2"
    # Two chains of three to the gate: plan order picks worker-daemon's.
    assert [s["key"] for s in home["critical_path"]["stories"]] == ["apw/worker-daemon", "apw/stage-1", "apw/gate"]
    assert home["critical_path"]["stories"][0]["needs"] == "not_approved"
    m1, m2 = home["milestones"]
    assert m1["progress"]["stories"] == {"done": 1, "total": 5}
    assert m1["progress"]["agent_tasks"] == {"done": 1, "total": 1}
    assert m1["progress"]["person_tasks"] == {"done": 0, "total": 1}
    assert m2["progress"]["person_tasks"] == {"done": 0, "total": 1} and m2["goal"] and not m2["next"]
    spans = home["timeline"]["spans"]
    assert spans[0]["start"] == 0 and spans[-1]["end"] == 1
    # m1 counts 5 stories and 2 tasks, m2 3 stories and 1 task: 2 of 11 done.
    assert spans[0]["end"] == pytest.approx(7 / 11) and home["timeline"]["here"] == pytest.approx(2 / 11)
    assert {b["key"] for b in spans[0]["blockers"]} >= {"apw/worker-daemon", "apw/homelab-instance"}


def test_no_milestones_file_is_one_implicit_milestone(store):
    store.plan([("apw", [])])
    store.change("apw", breakdown(deliverable("a", kind="enabler")))
    home = home_of(store)
    assert home["milestones_declared"] is False
    assert [m["id"] for m in home["milestones"]] == ["project"] and home["milestones"][0]["goal"]


# 2.3 The home document and its live updates ---------------------------------------


def homelab(store):
    store.plan([("work", [])])
    store.change("work", breakdown(deliverable("a", kind="enabler"), deliverable("b", ["a"], kind="enabler")))
    store.milestones([milestone("m1", [{"change": "work"}], gate="work/b", goal=True)])


def test_the_home_is_served_and_updates_when_a_story_finishes(store):
    homelab(store)
    fake = FakeSortie()
    fake.plan_states = STATES
    fake.issue("work", kind="epic")
    a = fake.issue("work/a", state="In Progress", runtime={"status": "running"})
    fake.run(a["identifier"], "s1")
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        home = client.get(f"{P}/home").json()
        assert home["health"]["busy"] == 1 and home["health"]["slots"] == 3
        assert home["milestones"][0]["progress"]["stories"] == {"done": 0, "total": 2}
        assert home["timeline"]["here"] == 0
        graph = client.get(f"{P}/graph").json()
        assert graph["home"]["version"] == graph["version"]

        a.update(state="Done", terminal=True, runtime=None)
        fake.end(a["identifier"])
        settle(client)
        after = client.get(f"{P}/home").json()
        assert after["version"] > home["version"]
        assert after["milestones"][0]["progress"]["stories"] == {"done": 1, "total": 2}
        assert after["timeline"]["here"] == 0.5
        assert after["health"]["busy"] == 0
        assert [s["key"] for s in after["critical_path"]["stories"]] == ["work/b"]


def test_the_home_rides_the_event_stream(store):
    homelab(store)
    app = app_for(store, FakeSortie())
    with live_server(app) as base, httpx.Client(base_url=base, timeout=5) as client:
        assert client.post("/api/login", json={"password": PASSWORD}).status_code == 200
        with client.stream("GET", f"{P}/events") as r:
            lines = r.iter_lines()
            assert next(lines) == "event: graph"
            doc = json.loads(next(lines).removeprefix("data: "))
            assert doc["home"]["milestones"][0]["id"] == "m1"
            # A store edit arrives with its home.
            store.tasks("work", "- [ ] 1.1 Enroll. [operator]\n")
            for line in lines:
                if line.startswith("data: ") and json.loads(line[6:])["home"]["not_ready"]:
                    break


def test_sortie_unreachable_keeps_what_the_store_knows(store):
    homelab(store)
    store.tasks("work", "- [x] 1.1 a\n- [ ] 1.2 b\n")
    fake = FakeSortie()
    fake.unreachable = True
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        home = client.get(f"{P}/home").json()
        assert home["health"]["verdict"] == "stuck"
        assert home["health"]["reasons"][0]["code"] == "sortie_unreachable"
        assert home["story_progress"] == "unknown"
        assert home["milestones"][0]["progress"]["agent_tasks"] == {"done": 1, "total": 2}


def test_an_invalid_plan_says_so_and_keeps_the_last_home(store):
    homelab(store)
    fake = FakeSortie()
    app = app_for(store, fake)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        store.milestones([milestone("m1", [{"change": "nope"}])])
        settle(client)
        home = client.get(f"{P}/home").json()
        assert home["plan"]["valid"] is False and home["plan"]["errors"] == 1
        assert home["milestones"][0]["id"] == "m1" and home["milestones"][0]["gate"] == "work/b"


def test_a_failed_sync_shows_as_stuck(store):
    homelab(store)
    fake = FakeSortie()
    app = app_for(store, fake, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        project(app).hub.publish_sync({"state": "failed", "reason": {"code": "x", "message": "tracker down"}})
        home = client.get(f"{P}/home").json()
        assert home["health"]["verdict"] == "stuck"
        assert home["health"]["reasons"][-1]["text"] == "The last sync failed: tracker down"


# A read-only workbench --------------------------------------------------------------


def test_sync_is_on_unless_workbench_sync_is_0(tmp_path):
    env = {"WORKBENCH_HOME": str(tmp_path), "WORKBENCH_SORTIE_URL": "http://s", "WORKBENCH_PASSWORD_HASH": "h",
           "WORKBENCH_SECRET_KEY": "k"}
    assert Config.from_env(env).sync_enabled is True
    assert Config.from_env({**env, "WORKBENCH_SYNC": "1"}).sync_enabled is True
    assert Config.from_env({**env, "WORKBENCH_SYNC": "0"}).sync_enabled is False


def test_with_sync_off_nothing_is_written_to_the_tracker(store):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("b1", covers=["billing#Do b1"]), deliverable("b2", covers=["billing#Do b2"])))
    fake = FakeSortie()
    fake.plan_states = STATES
    fake.issue("billing/b2", state="Todo")
    app = create_app(make_config(store.root, sync_enabled=False), validate=validate_offline, sortie=fake.client())
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert client.post(f"{P}/approve", json={"epic": "billing"}).status_code == 200
        # An approved story released, then edited: a writing workbench pulls it back.
        store.change("billing", breakdown(deliverable("b1", covers=["billing#Do b1"]), deliverable("b2", title="Changed", covers=["billing#Do b2"])))
        settle(client)
        settle(client)
        for path in ("stories/pull-back", "stories/cancel"):
            r = client.post(f"{P}/{path}", json={"key": "billing/b1"})
            assert r.status_code == 409 and r.json()["error"]["code"] == "sync_disabled"
        assert client.get(f"{P}/graph").json()["sync"]["state"] == "disabled"
    writes = [(m, p) for m, p, _ in fake.requests if m == "POST"]
    assert writes == []


def test_the_order_between_changes_ranks_the_change_that_comes_first(store):
    """The pilot proves the route that two later changes wait on in
    plan.yaml: its approval ranks first, and theirs wait on it."""
    store.plan([("route", []), ("later", ["route"]), ("other", ["route"]), ("gate", ["later", "other"])])
    store.change("route", breakdown(deliverable("image", kind="enabler"), deliverable("pilot", ["image"], kind="enabler")))
    store.change("later", breakdown(deliverable("l1", kind="enabler"), deliverable("l2", ["l1"], kind="enabler")))
    store.change("other", breakdown(deliverable("o1", kind="enabler")))
    store.change("gate", breakdown(deliverable("g", ["later/l2", "other/o1"], kind="enabler")))
    store.milestones([milestone("m1", [{"change": c} for c in ("route", "later", "other", "gate")], gate="gate/g")])
    items = items_by_key(home_of(store, done={"route/image"}))
    # later's two drafts are reviewed together, as one row.
    pilot, later, o1 = items["route/pilot"], items["later"], items["other/o1"]
    assert pilot["rank"] == 1 and pilot["slack"] == 0 and pilot["critical"]
    assert pilot["dependents"] == 4
    assert later["slack"] == 1 and o1["slack"] == 1 and later["rank"] > 1 and o1["rank"] > 1


# What each released story is doing ---------------------------------------------------


def released_plan(store, *keys):
    store.plan([("ons", [])])
    store.change("ons", breakdown(*(deliverable(k, kind="enabler") for k in keys)))


def test_scenario_a_retry_keeps_its_place(store):
    """ONS-10, ONS-11 and ONS-13 are released, and ONS-11's session start times out."""
    released_plan(store, "s10", "s11", "s13")
    fake = FakeSortie()
    fake.plan_states = STATES
    fake.issue("ons", kind="epic")
    issues = {i: fake.issue(f"ons/s{i}", identifier=f"ONS-{i}") for i in (10, 11, 13)}
    app = app_for(store, fake, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        rows = client.get(f"{P}/home").json()["released"]
        assert [r["identifier"] for r in rows] == ["ONS-10", "ONS-11", "ONS-13"]
        assert [r["state"] for r in rows] == ["waiting", "waiting", "waiting"]

        issues[10].update(state="In Progress", runtime={"status": "running", "turn_count": 1})
        fake.run("ONS-10", "s1", started_at="2026-11-02T10:00:00Z")
        issues[11].update(state="In Progress", runtime={"status": "retrying", "attempt": 2})
        fake.run("ONS-11", "s2")
        fake.end("ONS-11", retry_attempt=2, error="response_timeout")
        settle(client)
        home = client.get(f"{P}/home").json()
        rows = home["released"]
        assert [r["identifier"] for r in rows] == ["ONS-10", "ONS-11", "ONS-13"]
        assert [r["state"] for r in rows] == ["working", "starting", "waiting"]
        assert rows[0]["turn"] == 1 and rows[0]["started_at"] == 1793613600.0
        assert (rows[1]["attempt"], rows[1]["error"]) == (2, "response_timeout")
        # The slots in use are a figure of their own, not rows of the list.
        assert home["health"]["busy"] == 1 and home["health"]["slots"] == 3


def test_scenario_a_story_in_review(store):
    """ONS-23 opened ortusnova/homelab#97 and Sortie moved it to In Review."""
    released_plan(store, "s22", "s23")
    fake = FakeSortie()
    fake.plan_states = STATES
    fake.issue("ons", kind="epic")
    fake.issue("ons/s22", identifier="ONS-22")
    s23 = fake.issue("ons/s23", identifier="ONS-23", state="In Progress", runtime={"status": "running", "turn_count": 5})
    fake.run("ONS-23", "s1", turn_count=5)
    app = app_for(store, fake, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        before = client.get(f"{P}/home").json()
        assert [r["state"] for r in before["released"]] == ["waiting", "working"]

        fake.end("ONS-23")
        s23.update(state="In Review", runtime=None, pr="ortusnova/homelab#97")
        settle(client)
        home = client.get(f"{P}/home").json()
        assert home["version"] > before["version"]
        row = next(r for r in home["released"] if r["identifier"] == "ONS-23")
        assert row["state"] == "in_review"
        assert row["pr"] == {"ref": "ortusnova/homelab#97", "url": "https://github.com/ortusnova/homelab/pull/97"}

        # Done, it leaves the list; the others keep their place.
        s23.update(state="Done", terminal=True)
        settle(client)
        assert [r["identifier"] for r in client.get(f"{P}/home").json()["released"]] == ["ONS-22"]


def test_a_story_released_later_is_listed_after_the_others(store):
    released_plan(store, "s5", "s10")
    fake = FakeSortie()
    fake.plan_states = STATES
    fake.issue("ons", kind="epic")
    early = fake.issue("ons/s5", identifier="ONS-5", state="Backlog")
    fake.issue("ons/s10", identifier="ONS-10")
    app = app_for(store, fake, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [r["identifier"] for r in client.get(f"{P}/home").json()["released"]] == ["ONS-10"]
        early.update(state="Todo")
        settle(client)
        assert [r["identifier"] for r in client.get(f"{P}/home").json()["released"]] == ["ONS-10", "ONS-5"]
