"""A person is alerted, wherever the question is held
(agent-platform/run-supervision): each Sortie's notifications reach the
workbench by Sortie's webhook backend, authenticated by the host's token; the
workbench keeps each one, links it to the story and the question it names,
and shows it under "Needs a person", unread until seen, and unread again
every four hours while its question stays open.

Every body is one Sortie's webhook backend posted
(tests/fixtures/sortie-webhook, which dev/stage0/test-review-gate.sh checks
against Sortie itself), with the story's identifier and times of the test."""

from __future__ import annotations

import json
import logging
from datetime import datetime, timezone
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_alerts import STATEMENT, TAICHI_TOKEN, Clock
from tests.workbench.test_approvals import billing
from tests.workbench.test_watch import validate_offline
from workbench.alerts import REPEAT_SECONDS
from workbench.app import create_app
from workbench.config import Config
from workbench.notifications import PayloadError, RedactTokens, parse

FIXTURES = Path(__file__).resolve().parents[1] / "fixtures" / "sortie-webhook"
PARKED_AT = "2026-11-01T18:35:26Z"
PARKED = datetime.fromisoformat(PARKED_AT.replace("Z", "+00:00")).timestamp()
HOST_TOKENS = {"p1": "p1-notify-token-0123456789", "taichi": "taichi-notify-token-0123456789"}


def sortie_body(event: str, identifier: str = "T-1", at: float = PARKED + 2, notification_id: str | None = None, **fields) -> dict:
    """What Sortie's webhook backend posts for the event, of the story."""
    body = json.loads((FIXTURES / f"{event.replace('.', '_')}.json").read_text())
    stamp = datetime.fromtimestamp(at, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    body.update(identifier=identifier, issue_id=f"id-{identifier}", timestamp=stamp,
                notification_id=notification_id or f"{event}-{identifier}-{int(at)}", **fields)
    return body


def tokens(tmp_path: Path) -> Path:
    d = tmp_path / "notify-tokens"
    d.mkdir()
    for host, token in HOST_TOKENS.items():
        (d / f"{host}.token").write_text(token + "\n")
        (d / f"{host}.token").chmod(0o600)
    return d


def setup(store, tmp_path, clock):
    """billing/b1 is T-1 and billing/b2 is T-2; taichi's Sortie runs stories
    of the same project as p1's."""
    billing(store)
    p1, taichi = FakeSortie(), FakeSortie(token=TAICHI_TOKEN)
    p1.issue("billing/b1", state="In Progress")
    p1.issue("billing/b2")
    config = make_config(store.root, sortie_name="p1", notify_tokens_dir=tokens(tmp_path))
    app = create_app(config, validate=validate_offline, sortie=p1.client(), peers={"taichi": taichi.client()}, clock=clock)
    return app, p1, taichi


def post(client: TestClient, body: dict | bytes, host: str = "taichi", token: str | None = None):
    token = HOST_TOKENS[host] if token is None else token
    path = f"/api/notifications/{host}/{token}" if token else f"/api/notifications/{host}"
    if isinstance(body, bytes):
        return client.post(path, content=body, headers={"content-type": "application/json"})
    return client.post(path, json=body)


def home(app):
    return project(app).hub.home


def shown(app):
    return home(app)["notifications"]


# Scenario: A question on taichi's Sortie ---------------------------------------------


def test_a_question_on_taichis_sortie_shows_under_needs_a_person_within_one_refresh(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert shown(app) == [] and home(app)["needs_person"]["unread"] == 0
        taichi.park("T-1", STATEMENT, PARKED_AT)
        r = post(client, sortie_body("session.stopped", agent_text=STATEMENT))
        assert r.status_code == 202, r.text
        # Shown at once, before the next poll reads the park.
        [n] = shown(app)
        assert n["unread"] and n["event_type"] == "session.stopped" and n["host"] == "taichi"
        assert n["agent_text"] == STATEMENT and n["identifier"] == "T-1"
        assert n["story"]["key"] == "billing/b1" and n["story"]["title"]
        assert home(app)["needs_person"]["unread"] == 1
        settle(client)  # one refresh: the park, and its question, are linked
        [n] = shown(app)
        assert n["question"] == {"identifier": "T-1", "sortie": "taichi", "parked_at": PARKED}
        assert [q["identifier"] for q in home(app)["questions"]] == ["T-1"]
        # The same, in the graph's own event stream.
        assert project(app).hub.doc["home"]["notifications"][0]["id"] == n["id"]


def test_a_notification_is_unread_until_a_person_has_seen_it(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        nid = post(client, sortie_body("session.stopped", agent_text=STATEMENT)).json()["id"]
        settle(client)
        assert shown(app)[0]["unread"]
        r = client.post(f"{P}/notifications/seen", json={"ids": [nid]})
        assert r.status_code == 200 and r.json()["seen"] == 1
        [n] = shown(app)
        # Seen, it stays while its question is open, no longer unread.
        assert not n["unread"] and n["seen_by"] == "Local operator" and n["seen_at"] == clock.t
        assert home(app)["needs_person"]["unread"] == 0
        assert r.json()["home"]["notifications"][0]["unread"] is False


def test_a_notification_of_a_run_that_is_not_a_question_leaves_once_seen(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        nid = post(client, sortie_body("escalation.ci_failure", "T-2"), host="p1").json()["id"]
        [n] = shown(app)
        assert n["unread"] and n["question"] is None and n["story"]["key"] == "billing/b2" and "CI is still failing" in n["body"]
        client.post(f"{P}/notifications/seen", json={"ids": [nid]})
        assert shown(app) == []
        # Kept, all the same.
        [kept] = client.get(f"{P}/notifications").json()["notifications"]
        assert kept["id"] == nid and not kept["unread"] and kept["event_type"] == "escalation.ci_failure"


def test_each_kind_of_sortie_notification_is_kept_with_what_it_says(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        for f in sorted(FIXTURES.glob("*.json")):
            event = json.loads(f.read_text())["event_type"]
            assert post(client, sortie_body(event, "T-2"), host="p1").status_code == 202
        got = {n["event_type"]: n for n in client.get(f"{P}/notifications").json()["notifications"]}
        assert set(got) == {"session.stopped", "session.escalated", "escalation.ci_failure", "escalation.merge_conflicts"}
        assert got["session.escalated"]["escalation"] == {"kind": "criteria_failed", "rule": "review", "url": ""}
        assert all(n["story"]["key"] == "billing/b2" and n["host"] == "p1" for n in got.values())


def test_a_notification_of_an_earlier_park_is_not_of_the_question_open_now(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        post(client, sortie_body("session.stopped", at=PARKED - 3600))
        [n] = shown(app)
        assert n["question"] is None


def test_a_story_of_no_project_is_kept_and_shown_nowhere(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert post(client, sortie_body("session.stopped", "OTHER-9")).status_code == 202
        assert shown(app) == [] and client.get(f"{P}/notifications").json()["notifications"] == []
        assert [r["identifier"] for r in app.state.projects.registry.notifications()] == ["OTHER-9"]


# Scenario: Still open ----------------------------------------------------------------


def test_a_question_still_open_four_hours_later_is_unread_once_more(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        nid = post(client, sortie_body("session.stopped", agent_text=STATEMENT)).json()["id"]
        client.post(f"{P}/notifications/seen", json={"ids": [nid]})
        clock.t = PARKED + 2 + REPEAT_SECONDS - 1
        settle(client)
        assert not shown(app)[0]["unread"]
        clock.t += 1  # four hours after Sortie sent it
        settle(client)
        [n] = shown(app)
        assert n["unread"] and n["again"] and home(app)["needs_person"]["unread"] == 1
        # Seen again: read until the next four hours.
        client.post(f"{P}/notifications/seen", json={"ids": [nid]})
        assert not shown(app)[0]["unread"]
        clock.t += REPEAT_SECONDS
        settle(client)
        assert shown(app)[0]["unread"]
        # Answered: no longer open, so never unread again, and no longer shown once seen.
        assert client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "At each release."}).status_code == 200
        client.post(f"{P}/notifications/seen", json={"ids": [nid]})
        clock.t += 2 * REPEAT_SECONDS
        settle(client)
        assert shown(app) == []


def test_only_the_newest_notification_of_a_question_is_unread_again(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        a = post(client, sortie_body("session.stopped", at=PARKED + 1)).json()["id"]
        b = post(client, sortie_body("session.escalated", at=PARKED + 2)).json()["id"]
        client.post(f"{P}/notifications/seen", json={"ids": [a, b]})
        clock.t = PARKED + 2 + REPEAT_SECONDS
        settle(client)
        [n] = shown(app)
        assert (n["id"], n["unread"], n["again"], n["earlier"], n["ids"]) == (b, True, True, 1, [a, b])
        kept = {k["id"]: k["unread"] for k in client.get(f"{P}/notifications").json()["notifications"]}
        assert kept == {a: False, b: True}


def test_a_released_question_is_not_unread_again(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        nid = post(client, sortie_body("session.stopped")).json()["id"]
        client.post(f"{P}/notifications/seen", json={"ids": [nid]})
        taichi.unpark("T-1")
        clock.t += 3 * REPEAT_SECONDS
        settle(client)
        assert shown(app) == []


def test_a_question_the_supervisor_holds_is_not_unread_again(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        taichi.hold("T-1", "proves the live criterion by hand", "the run on taichi")
        settle(client)
        nid = post(client, sortie_body("session.stopped")).json()["id"]
        [n] = shown(app)
        # Under the supervisor's lease the question waits on no person.
        assert n["unread"] and n["question"] is None
        client.post(f"{P}/notifications/seen", json={"ids": [nid]})
        clock.t += 3 * REPEAT_SECONDS
        settle(client)
        assert shown(app) == []


# Scenario: Six notifications for one story (agent-platform/supervisor-autonomy) ------


def kept(client) -> dict[int, dict]:
    return {n["id"]: n for n in client.get(f"{P}/notifications").json()["notifications"]}


def test_six_notifications_for_one_story_show_as_one_the_latest(store, tmp_path):
    """ONS-129 had six notifications on 2026-11-05: the agent stopped, a
    review escalated, CI failed twice, its branch conflicted, and it stopped
    again. "Needs a person" shows one entry for it, the latest."""
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        events = ["session.stopped", "session.escalated", "escalation.ci_failure", "escalation.ci_failure",
                  "escalation.merge_conflicts", "session.stopped"]
        ids = [post(client, sortie_body(e, at=PARKED + 1 + i, notification_id=f"n{i}")).json()["id"]
               for i, e in enumerate(events)]
        settle(client)
        [n] = shown(app)
        assert n["id"] == ids[-1] and n["event_type"] == "session.stopped" and n["sent_at"] == PARKED + 6
        assert n["earlier"] == 5 and n["ids"] == ids and n["unread"]
        assert n["question"] == {"identifier": "T-1", "sortie": "taichi", "parked_at": PARKED}
        assert home(app)["needs_person"]["unread"] == 1
        # Every one is kept all the same.
        assert sorted(kept(client)) == ids
        # The count is of stories, not of webhooks.
        post(client, sortie_body("escalation.ci_failure", "T-2", at=PARKED + 9), host="p1")
        assert [s["identifier"] for s in shown(app)] == ["T-2", "T-1"]
        assert home(app)["needs_person"]["unread"] == 2
        # Seen as one: the entry's ids are all of the story's.
        assert client.post(f"{P}/notifications/seen", json={"ids": n["ids"]}).json()["seen"] == 6
        [t1] = [s for s in shown(app) if s["identifier"] == "T-1"]
        assert not t1["unread"] and home(app)["needs_person"]["unread"] == 1


def test_one_unread_notification_makes_the_storys_entry_unread(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        a = post(client, sortie_body("session.stopped", at=PARKED + 1)).json()["id"]
        b = post(client, sortie_body("session.escalated", at=PARKED + 2)).json()["id"]
        client.post(f"{P}/notifications/seen", json={"ids": [b]})
        [n] = shown(app)
        assert (n["id"], n["unread"], n["earlier"], n["ids"]) == (b, True, 1, [a, b])


# Scenario: A story that merged, and the other ways a story moves on -------------------


def test_a_parked_story_unparked_and_merged_has_its_notifications_cleared_unread_or_not(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        a = post(client, sortie_body("session.stopped", at=PARKED + 1)).json()["id"]
        b = post(client, sortie_body("escalation.ci_failure", at=PARKED + 2)).json()["id"]
        client.post(f"{P}/notifications/seen", json={"ids": [a]})
        assert len(shown(app)) == 1
        taichi.unpark("T-1")
        p1.nodes[0].update(state="Done", terminal=True)
        clock.t += 60
        settle(client)
        assert shown(app) == [] and home(app)["needs_person"]["unread"] == 0
        got = kept(client)
        # Seen by the workbench, with the reason; the one a person saw keeps who saw it.
        assert got[b]["cleared_reason"] == "merged" and got[b]["seen_by"] == "workbench" and got[b]["seen_at"] == clock.t
        assert got[a]["cleared_reason"] == "merged" and got[a]["seen_by"] == "Local operator"
        assert got[a]["cleared_at"] == got[b]["cleared_at"] == clock.t
        # Unread again never, however long its park would have lasted.
        clock.t += 3 * REPEAT_SECONDS
        settle(client)
        assert shown(app) == []


def test_a_story_whose_pull_request_merged_has_its_notifications_cleared(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        nid = post(client, sortie_body("escalation.ci_failure", "T-2"), host="p1").json()["id"]
        assert shown(app)[0]["unread"]
        p1.nodes[1].update(state="Done", terminal=True)
        settle(client)
        assert shown(app) == [] and kept(client)[nid]["cleared_reason"] == "merged"


def test_sorties_merged_notification_clears_the_storys_notifications(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        a = post(client, sortie_body("escalation.ci_failure", "T-2"), host="p1").json()["id"]
        clock.t += 5
        b = post(client, sortie_body("escalation.ci_failure", "T-2", at=PARKED + 7, event_type="auto_merge.merged"),
                 host="p1").json()["id"]
        assert shown(app) == []
        got = kept(client)
        assert got[a]["cleared_reason"] == got[b]["cleared_reason"] == "merged"


def test_a_park_that_ended_clears_the_storys_notifications(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        nid = post(client, sortie_body("session.stopped")).json()["id"]
        # Never seen, and still cleared once the park ends.
        taichi.unpark("T-1")
        settle(client)
        assert shown(app) == []
        assert kept(client)[nid]["cleared_reason"] == "unparked" and kept(client)[nid]["seen_by"] == "workbench"


def test_an_answered_question_clears_the_storys_notifications(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        nid = post(client, sortie_body("session.stopped")).json()["id"]
        assert client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "At each release."}).status_code == 200
        settle(client)
        assert shown(app) == [] and kept(client)[nid]["cleared_reason"] == "unparked"


def test_a_cancelled_story_has_its_notifications_cleared(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    p1.plan_states = {"release": "Todo", "backlog": "Backlog", "cancel": "Canceled", "done": "Done"}
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        nid = post(client, sortie_body("escalation.merge_conflicts", "T-2"), host="p1").json()["id"]
        assert shown(app)
        p1.nodes[1].update(state="Canceled", terminal=True)
        settle(client)
        assert shown(app) == [] and kept(client)[nid]["cleared_reason"] == "cancelled"


def test_a_closed_question_clears_the_storys_notifications(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        a = post(client, sortie_body("session.stopped")).json()["id"]
        clock.t += 5
        b = post(client, sortie_body("session.stopped", at=PARKED + 20, event_type="question.closed",
                                     title="acme/web#1: question.closed")).json()["id"]
        # Cleared at once, the park still listed or not, and the closing one with them.
        assert shown(app) == [] and home(app)["needs_person"]["unread"] == 0
        got = kept(client)
        assert got[a]["cleared_reason"] == got[b]["cleared_reason"] == "question_closed"
        assert got[a]["seen_by"] == "workbench"


def test_a_notification_after_a_clear_shows_again(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        post(client, sortie_body("escalation.ci_failure", "T-2", notification_id="first"), host="p1")
        p1.nodes[1].update(state="Done", terminal=True)
        clock.t += 60
        settle(client)
        assert shown(app) == []
        clock.t += 60
        later = post(client, sortie_body("escalation.ci_failure", "T-2", at=clock.t,
                                         event_type="escalation.merge_completion"), host="p1").json()["id"]
        settle(client)
        [n] = shown(app)
        # Only itself: the cleared ones are not among its earlier ones.
        assert n["id"] == later and n["unread"] and n["earlier"] == 0 and n["ids"] == [later]
        assert home(app)["needs_person"]["unread"] == 1
        # And after a closed question, too.
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        post(client, sortie_body("session.stopped", at=clock.t, notification_id="closed", event_type="question.closed"))
        clock.t += 60
        again = post(client, sortie_body("session.stopped", at=clock.t, notification_id="again")).json()["id"]
        assert [s["id"] for s in shown(app) if s["identifier"] == "T-1"] == [again]


def test_a_park_that_cannot_be_read_clears_nothing(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        p1.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        nid = post(client, sortie_body("session.stopped"), host="p1").json()["id"]
        # The state is down: the park is no longer listed, but not ended.
        p1.state_down = True
        settle(client)
        assert [n["id"] for n in shown(app)] == [nid]
        assert kept(client)[nid]["cleared_at"] is None
        p1.state_down = False
        settle(client)
        assert shown(app)[0]["question"] is not None


def test_the_state_a_story_was_last_seen_in_outlasts_a_restart(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        sign_in(client)
        taichi.park("T-1", STATEMENT, PARKED_AT)
        settle(client)
        nid = post(client, sortie_body("escalation.ci_failure")).json()["id"]
    taichi.unpark("T-1")
    config = make_config(store.root, sortie_name="p1", notify_tokens_dir=tmp_path / "notify-tokens")
    again = create_app(config, validate=validate_offline, sortie=p1.client(), peers={"taichi": taichi.client()}, clock=clock)
    with TestClient(again) as client:
        sign_in(client)
        settle(client)
        # The park ended while the workbench was down: its notifications clear.
        assert shown(again) == [] and kept(client)[nid]["cleared_reason"] == "unparked"


def test_what_an_earlier_park_raised_clears_when_the_workbench_first_sees_the_story(store, tmp_path):
    """Notifications kept before the workbench knew the stories' states, as
    on its first start with this change: those of a park, of a story with no
    park now, clear; the others stay until a person sees them."""
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    registry = app.state.projects.registry
    registry.record_notification("p1", "old-stop", "T-1", "session.stopped", json.dumps(sortie_body("session.stopped", at=PARKED - 3600)), PARKED - 3600)
    registry.record_notification("p1", "old-ci", "T-2", "escalation.ci_failure",
                                 json.dumps(sortie_body("escalation.ci_failure", "T-2", at=PARKED - 3600)), PARKED - 3600)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [n["identifier"] for n in shown(app)] == ["T-2"]
        assert {n["identifier"]: n["cleared_reason"] for n in kept(client).values()} == {"T-1": "unparked", "T-2": None}


# The token ---------------------------------------------------------------------------


def test_a_wrong_or_missing_token_is_refused_and_nothing_is_kept(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        settle(client)
        body = sortie_body("session.stopped")
        assert post(client, body, token="wrong").status_code == 401
        assert post(client, body, token="").status_code == 401  # no token at all
        # One host's token is not another's.
        assert post(client, body, host="taichi", token=HOST_TOKENS["p1"]).status_code == 401
        assert client.post(f"/api/notifications/nowhere/{HOST_TOKENS['p1']}", json=body).status_code == 401
        assert client.post("/api/notifications/../p1.token/x", json=body).status_code in (401, 404)
        assert app.state.projects.registry.notifications() == []
        assert shown(app) == []
        # The right one is taken, with no signed-in person.
        assert post(client, body).status_code == 202


def test_a_token_others_can_read_is_refused(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    (tmp_path / "notify-tokens" / "taichi.token").chmod(0o644)
    with TestClient(app) as client:
        assert post(client, sortie_body("session.stopped")).status_code == 401


def test_with_no_tokens_the_endpoint_takes_nothing(store):
    billing(store)
    app = create_app(make_config(store.root), validate=validate_offline, sortie=FakeSortie().client())
    with TestClient(app) as client:
        assert post(client, sortie_body("session.stopped"), host="p1").status_code == 401


def test_the_token_stays_out_of_the_access_log():
    record = logging.LogRecord("uvicorn.access", logging.INFO, "", 0, '%s - "%s %s HTTP/%s" %d',
                               ("127.0.0.1:5", "POST", f"/api/notifications/taichi/{HOST_TOKENS['taichi']}", "1.1", 202), None)
    RedactTokens().filter(record)
    assert HOST_TOKENS["taichi"] not in record.getMessage() and "/api/notifications/taichi/<token>" in record.getMessage()


def test_the_tokens_directory_is_read_from_the_environment(tmp_path):
    env = {"WORKBENCH_SORTIE_URL": "http://sortie.test", "WORKBENCH_PASSWORD_HASH": "x", "WORKBENCH_SECRET_KEY": "k",
           "WORKBENCH_HOME": str(tmp_path / "home")}
    assert Config.from_env(env).notify_tokens_dir is None
    assert Config.from_env({**env, "WORKBENCH_NOTIFY_TOKENS": str(tmp_path / "t")}).notify_tokens_dir == tmp_path / "t"


# The body ----------------------------------------------------------------------------


def test_sortie_sending_the_same_notification_again_keeps_one(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        settle(client)
        body = sortie_body("session.stopped", "T-2")
        first, again = post(client, body, host="p1").json(), post(client, body, host="p1").json()
        assert first["new"] and not again["new"] and first["id"] == again["id"]
        assert len(app.state.projects.registry.notifications()) == 1


@pytest.mark.parametrize("raw, why", [
    (b"not json", "not JSON"),
    (b"[1]", "not a JSON object"),
    (json.dumps({"identifier": "T-1"}).encode(), "notification_id"),
    (json.dumps({"notification_id": "n", "title": 5}).encode(), "title"),
    (json.dumps({"notification_id": "n", "attempt": "2"}).encode(), "attempt"),
    (json.dumps({"notification_id": "n", "escalation": {"kind": 1}}).encode(), "escalation"),
])
def test_a_body_that_is_not_sorties_is_refused(store, tmp_path, raw, why):
    with pytest.raises(PayloadError, match=why):
        parse(raw)
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        r = post(client, raw)
        assert r.status_code == 400 and r.json()["error"]["code"] == "invalid_notification"


def test_every_fixture_is_a_body_the_endpoint_takes():
    for f in FIXTURES.glob("*.json"):
        body = parse(f.read_bytes())
        assert body["notification_id"] and body["event_type"]


def test_a_body_too_large_is_refused(store, tmp_path):
    clock = Clock(PARKED + 10)
    app, p1, taichi = setup(store, tmp_path, clock)
    with TestClient(app) as client:
        r = post(client, sortie_body("session.stopped", body="x" * 300_000))
        assert r.status_code == 413
