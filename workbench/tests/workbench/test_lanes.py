"""The workers' slot lanes on the home (agent-platform/run-supervision)."""

from __future__ import annotations

from datetime import datetime, timezone

import pytest
from fastapi.testclient import TestClient

from tests.workbench.conftest import P, make_config, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import STATES
from tests.workbench.test_home import homelab
from tests.workbench.test_watch import validate_offline
from workbench.app import create_app
from workbench.config import Config, ConfigError
from workbench.lanes import estimates, worker_lanes

NOW = datetime(2026, 11, 2, 12, 0, tzinfo=timezone.utc).timestamp()


def at(minutes: float) -> str:
    """A time ``minutes`` from NOW, as Sortie writes it."""
    return datetime.fromtimestamp(NOW + minutes * 60, timezone.utc).isoformat().replace("+00:00", "Z")


def run(ident, rule="build", minutes=20, status="succeeded"):
    return {"issue_identifier": ident, "rule_name": rule, "status": status, "started_at": at(-60 - minutes), "completed_at": at(-60)}


def running(ident, started=-10, **extra):
    return {"issue_identifier": ident, "issue_title": f"title {ident}", "rule_name": "build", "started_at": at(started),
            "session_id": f"s-{ident}", "held": False, "hold_reason": "", "holder": "", "turn_count": 3,
            "last_message": "go test ./...", "last_event_at": at(-1), **extra}


def state(slots=3, **lists):
    return {"config": {"agent": {"max_concurrent_agents": slots}, "polling": {"interval_ms": 15000}},
            "started_at": at(-90), **lists}


def lanes(st, runs=None, **kw):
    return worker_lanes("p1", main=True, state=st, error=None, runs=estimates(runs or []), key_of={"T-1": "work/a"}, now=NOW, **kw)


# Lanes and estimates --------------------------------------------------------------


def test_one_lane_per_slot_and_a_job_with_its_estimate():
    w = lanes(state(running=[running("T-1")]), runs=[run("A"), run("B", minutes=30), run("C", minutes=40), run("D", "review", 5)])
    assert w["slots"] == 3 and w["busy"] == 1 and len(w["lanes"]) == 3
    assert w["verdict"] == "working" and w["reasons"] == []
    job = w["lanes"][0]["job"]
    assert job["identifier"] == "T-1" and job["key"] == "work/a" and job["phase"] == "Build"
    assert job["started_at"] == NOW - 600
    assert job["estimate"] == {"seconds": 1800, "basis": "median of 3 past build runs"}
    assert [lane["kind"] for lane in w["lanes"]] == ["job", "free", "free"]


def test_no_estimate_without_enough_history():
    w = lanes(state(running=[running("T-1")]), runs=[run("A"), run("B", status="failed"), run("C")])
    assert w["lanes"][0]["job"]["estimate"] is None


def test_an_estimate_from_every_rule_when_its_own_has_too_few():
    w = lanes(state(running=[running("T-1", rule_name="review")]), runs=[run("A", minutes=10), run("B", minutes=20), run("C", minutes=30)])
    assert w["lanes"][0]["job"]["estimate"]["basis"] == "median of 3 past runs"


def test_a_free_slot_says_why_nothing_runs():
    w = lanes(state())
    assert [lane["reason"] for lane in w["lanes"]] == ["Nothing approved is ready for p1.", "", ""]
    assert w["verdict"] == "idle"
    held = [{"issue_identifier": "T-5", "issue_title": "five", "hold": {"reason": "blocked_by", "blockers": [{"issue_identifier": "T-4"}]}}]
    w = lanes(state(held=held))
    assert w["lanes"][0]["reason"] == "Nothing ready for p1: 1 story waits on other work"
    assert w["later"] == [{"kind": "blocked", "identifier": "T-5", "key": None, "title": "five", "text": "after T-4", "status": None}]


def test_a_free_slot_says_what_runs_next():
    w = lanes(state(slots=2, between_stages=[{"issue_identifier": "T-1", "issue_title": "a", "target_rule": "review"}]),
              runs=[run("A", "review", 10)] * 3)
    first = w["lanes"][0]
    assert first["reason"] == "next: T-1 moves to review"
    assert first["next"][0]["key"] == "work/a" and first["next"][0]["estimate"]["seconds"] == 600
    assert w["lanes"][1]["reason"] == "Nothing approved is ready for p1."


def test_a_paused_dispatch_is_said_in_the_free_slot():
    w = lanes(state(dispatch_paused={"reason_text": "Draining for a deploy"}))
    assert w["lanes"][0]["reason"] == "Draining for a deploy" and w["verdict"] == "paused"


# Each stuck reason reads as stuck in its own lane, with why. -------------------------


@pytest.mark.parametrize("lists, code, text", [
    ({"running": [running("T-1", held=True, hold_reason="permission_request", holder="brian")]},
     "held", "held: permission request, by brian"),
    ({"running": [running("T-1", stall_at=at(-2), stall_text="no agent event for 15 min")]},
     "stalled", "no agent event for 15 min"),
    ({"retrying": [{"issue_identifier": "T-1", "attempt": 2, "due_at": at(3), "error": "agent exited 1", "rule_name": "build"}]},
     "retrying", "retrying, attempt 2: agent exited 1"),
    ({"budget_exhausted": [{"issue_identifier": "T-1", "reason": "session_budget", "budget_sessions": 16, "used_sessions": 16,
                            "exhausted_at": at(-30), "rule_name": "review"}]},
     "budget", "used its 16 sessions; the session budget holds it"),
    ({"parked": [{"issue_identifier": "T-1", "reason": "agent_blocked", "statement": "Which repository?", "parked_at": at(-20)}]},
     "question", "asks: Which repository?"),
])
def test_a_stuck_slot_reads_as_stuck_with_why(lists, code, text):
    w = lanes(state(**lists))
    lane = w["lanes"][0]
    assert lane["stuck"] == {"code": code, "text": text}
    assert lane["job"]["identifier"] == "T-1" and lane["job"]["key"] == "work/a"
    assert w["verdict"] == "stuck"
    assert w["reasons"] == [{"code": code, "text": f"T-1 {text}", "identifier": "T-1", "key": "work/a"}]


def test_a_question_takes_its_whole_statement_from_the_issue():
    w = lanes(state(parked=[{"issue_identifier": "T-1", "statement": "Which…"}]), questions={"T-1": {"statement": "Which repository should it use?"}})
    assert w["lanes"][0]["stuck"]["text"] == "asks: Which repository should it use?"


def test_stuck_work_that_does_not_fit_the_free_slots_waits_below():
    parked = [{"issue_identifier": f"T-{i}", "statement": f"q{i}"} for i in (1, 2, 3)]
    w = lanes(state(slots=2, parked=parked))
    assert [lane["kind"] for lane in w["lanes"]] == ["stuck", "stuck"]
    assert w["later"][0] == {"kind": "you", "identifier": "T-3", "key": None, "title": "T-3", "text": "asks: q3", "stuck": True,
                             "code": "question", "budget": None,
                             "question": {"identifier": "T-3", "statement": "q3", "parked_at": None, "reason": None}}
    assert [r["identifier"] for r in w["reasons"]] == ["T-1", "T-2", "T-3"]


def test_running_jobs_keep_their_lanes_and_stuck_work_takes_the_rest():
    w = lanes(state(slots=2, running=[running("T-2"), running("T-1", started=-40)],
                    budget_exhausted=[{"issue_identifier": "T-3", "reason": "token_budget"}]))
    assert [(lane["kind"], lane["job"]["identifier"]) for lane in w["lanes"]] == [("job", "T-1"), ("job", "T-2")]
    assert w["later"][0]["kind"] == "held" and w["later"][0]["text"] == "held: token budget reached"


# Pull requests ---------------------------------------------------------------------


def reaction(ident, kind, number, created=-10, **extra):
    return {"issue_identifier": ident, "issue_title": f"title {ident}", "kind": kind, "created_at": at(created),
            "pull_request": {"number": number, "repository": "o/r", "url": f"https://github.com/o/r/pull/{number}"}, **extra}


def test_each_pull_request_says_what_it_waits_on():
    passing = {"last_check": {"status": "passing"}}
    w = lanes(state(pending_reactions=[
        reaction("T-1", "ci", 1), reaction("T-1", "merge-completion", 1),
        reaction("T-2", "ci", 2, last_check={"status": "failing"}),
        reaction("T-3", "ci", 3, **passing), reaction("T-3", "merge-conflict", 3, last_check={"mergeable": False}),
        reaction("T-4", "ci", 4, **passing), reaction("T-4", "merge", 4),
        reaction("T-5", "ci", 5, **passing), reaction("T-5", "merge-completion", 5),
    ]))
    texts = {i["identifier"]: (i["kind"], i["text"]) for i in w["later"]}
    assert texts == {
        "T-1": ("waiting", "#1 waits on CI"), "T-2": ("waiting", "#2 waits on CI, which fails"),
        "T-3": ("waiting", "#3 waits on a conflict"), "T-4": ("waiting", "#4 waits on its auto-merge"),
        "T-5": ("ready", "#5 waits on nothing: ready to merge"),
    }
    assert w["later"][0]["identifier"] == "T-5" and w["later"][0]["pull"]["url"].endswith("/5")
    assert w["verdict"] == "idle"


def test_a_pull_request_ready_to_merge_for_an_hour_makes_its_worker_stuck():
    w = lanes(state(pending_reactions=[reaction("T-1", "merge-completion", 7, created=-61)]))
    assert w["verdict"] == "stuck"
    assert w["reasons"][0]["code"] == "ready" and "ready to merge, for over an hour" in w["reasons"][0]["text"]


def test_an_unreachable_worker_says_so():
    w = worker_lanes("taichi", main=False, state=None, error="connection refused", runs=None, key_of={}, now=NOW)
    assert w["verdict"] == "unreachable" and w["lanes"] == []
    assert w["reasons"][0]["text"] == "taichi's Sortie is unreachable: connection refused"


# The home, against the fake Sorties ------------------------------------------------------


def two_sorties(store, fake: FakeSortie, other: FakeSortie, **cfg):
    config = make_config(store.root, sortie_name="p1", **cfg)
    return create_app(config, validate=validate_offline, sortie=fake.client(), sorties={"taichi": other.client()})


def test_the_home_shows_each_sorties_workers_live(store):
    homelab(store)
    fake, other = FakeSortie(), FakeSortie(token="taichi-token")
    fake.plan_states = STATES
    fake.issue("work", kind="epic")
    a = fake.issue("work/a", state="In Progress", runtime={"status": "running"})
    fake.run(a["identifier"], "s1", stage="build", started_at=at(-5), issue_title="a")
    fake.runs = [run("X", minutes=12), run("Y", minutes=14), run("Z", minutes=16)]
    other.max_agents = 2
    other.parked = [{"issue_identifier": "ONS-50", "statement": "Push the merge, please", "parked_at": at(-3)}]
    app = two_sorties(store, fake, other)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        workers = client.get(f"{P}/home").json()["workers"]
        assert [w["name"] for w in workers] == ["p1", "taichi"]
        p1, taichi = workers
        assert p1["main"] and p1["slots"] == 3 and p1["busy"] == 1 and p1["verdict"] == "working"
        job = p1["lanes"][0]["job"]
        assert job["key"] == "work/a" and job["estimate"]["seconds"] == 14 * 60
        assert taichi["slots"] == 2 and taichi["verdict"] == "stuck"
        assert taichi["lanes"][0]["stuck"] == {"code": "question", "text": "asks: Push the merge, please"}

        # Live: the job ends, and taichi stops answering.
        fake.end(a["identifier"])
        other.unreachable = True
        settle(client)
        p1, taichi = client.get(f"{P}/home").json()["workers"]
        assert p1["busy"] == 0 and p1["lanes"][0]["kind"] == "free"
        assert taichi["verdict"] == "unreachable" and "unreachable" in taichi["reasons"][0]["text"]


def test_the_main_sortie_of_another_project_is_not_read(store):
    homelab(store)
    fake = FakeSortie()
    fake.DEFAULT = "OTHER"
    fake.projects["OTHER"] = {"name": "Other", "problems": []}
    with TestClient(create_app(make_config(store.root), validate=validate_offline, sortie=fake.client())) as client:
        sign_in(client)
        settle(client)
        assert client.get(f"{P}/home").json()["workers"] == [{"name": "sortie", "main": True, "dispatched": False}]
        assert not any(path in ("/api/v1/state", "/api/v1/runs") for _, path, _ in fake.requests)


def test_the_other_sorties_come_from_the_environment(tmp_path):
    (tmp_path / "taichi.token").write_text("t0ken\n")
    env = {"WORKBENCH_SORTIE_URL": "http://127.0.0.1:7680", "WORKBENCH_SORTIE_TOKEN": "x", "WORKBENCH_PASSWORD_HASH": "h",
           "WORKBENCH_SECRET_KEY": "k", "WORKBENCH_HOME": str(tmp_path), "WORKBENCH_SORTIE_NAME": "p1",
           "WORKBENCH_SORTIES": "taichi=http://127.0.0.1:7681/", "WORKBENCH_SORTIE_TOKEN_DIR": str(tmp_path)}
    config = Config.from_env(env)
    assert config.sortie_name == "p1"
    assert [(s.name, s.url, s.token) for s in config.other_sorties] == [("taichi", "http://127.0.0.1:7681", "t0ken")]
    assert config.other_sorties[0].broker_proxy == ""
    proxied = Config.from_env({**env, "WORKBENCH_SORTIE_BROKER_PROXIES": "taichi=socks5://127.0.0.1:7781"})
    assert proxied.other_sorties[0].broker_proxy == "socks5://127.0.0.1:7781"
    with pytest.raises(ConfigError, match="no Sortie named nope"):
        Config.from_env({**env, "WORKBENCH_SORTIE_BROKER_PROXIES": "nope=socks5://127.0.0.1:7781"})
    with pytest.raises(ConfigError, match="taken"):
        Config.from_env({**env, "WORKBENCH_SORTIES": "p1=http://x"})
