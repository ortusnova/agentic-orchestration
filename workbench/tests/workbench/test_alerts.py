"""A person is alerted, wherever the question is held
(agent-platform/run-supervision): the workbench reads the questions of every
Sortie, answers each through the Sortie that holds it, and pushes each open
question to the operator's channel, again every four hours while it stays
open. Every test runs against two fake Sorties, p1's and taichi's."""

from __future__ import annotations

import asyncio
import json
import os
from pathlib import Path

import httpx
import pytest
from fastapi.testclient import TestClient

from tests.workbench.conftest import P, make_config, project, settle, sign_in
from tests.workbench.fake_sortie import FakeSortie
from tests.workbench.test_approvals import billing, status
from tests.workbench.test_watch import validate_offline
from workbench.alerts import REPEAT_SECONDS, RETRY_SECONDS, Channel, cut
from workbench.app import create_app
from workbench.config import Config, ConfigError, SortieSource

STATEMENT = "Which cadence did the operator choose for the Authentik upgrade gate: weekly, or at each release?"
PUBLIC = "https://workbench.homelab.dev"
HOME_LINK = f"{PUBLIC}/#/p/store"
TAICHI_TOKEN = "taichi-control-token-0123456789"
SLACK = "https://hooks.slack.test/services/T0/B0/secret-part"


class Clock:
    def __init__(self, t: float = 1_800_000_000.0):
        self.t = t

    def __call__(self) -> float:
        return self.t


class FakeChannel:
    """The operator's channel: every push it received, as JSON."""

    def __init__(self):
        self.received: list[dict] = []
        self.urls: list[str] = []
        self.status = 200

    def handle(self, request: httpx.Request) -> httpx.Response:
        self.urls.append(str(request.url))
        if self.status >= 300:
            return httpx.Response(self.status, json={"ok": False})
        self.received.append(json.loads(request.content))
        return httpx.Response(200, json={"ok": True})

    def channel(self, kind: str = "slack", secret: str = SLACK, chat_id: str = "") -> Channel:
        return Channel(kind, secret, chat_id, transport=httpx.MockTransport(self.handle))

    def texts(self) -> list[str]:
        return [m.get("text") or m.get("content") for m in self.received]


def two_sorties(store):
    """billing/b1 is T-1 and billing/b2 is T-2, in the project p1's Sortie
    serves; taichi's Sortie runs stories of the same project."""
    billing(store)
    p1, taichi = FakeSortie(), FakeSortie(token=TAICHI_TOKEN)
    p1.issue("billing/b1", state="In Progress")
    p1.issue("billing/b2")
    return p1, taichi


def app_with(store, p1, taichi, channel: FakeChannel, clock: Clock, **cfg):
    config = make_config(store.root, sortie_name="p1", public_url=PUBLIC, **cfg)
    return create_app(config, validate=validate_offline, sortie=p1.client(), sorties={"taichi": taichi.client()},
                      channel=channel.channel(), clock=clock)


def home(app):
    return project(app).hub.home


# Scenario: A question on taichi's Sortie ---------------------------------------------


def test_a_question_on_taichis_sortie_shows_under_needs_a_person_and_reaches_the_channel(store):
    p1, taichi = two_sorties(store)
    channel, clock = FakeChannel(), Clock()
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert home(app)["questions"] == [] and channel.received == []
        taichi.park("T-1", STATEMENT)
        settle(client)  # one refresh
        qs = home(app)["questions"]
        assert [(q["key"], q["identifier"], q["sortie"], q["statement"]) for q in qs] == [("billing/b1", "T-1", "taichi", STATEMENT)]
        needs = home(app)["needs_person"]
        assert needs["questions"] == 1 and needs["nearest"] == "billing/b1"
        assert status(app, "billing/b1")["question"]["sortie"] == "taichi"
        # The channel has it: the question, the story, and the link to where it is answered.
        assert len(channel.received) == 1
        text = channel.texts()[0]
        assert STATEMENT in text and "T-1" in text and "billing/b1" in text and status(app, "billing/b1")["title"] in text
        assert "taichi's Sortie" in text and text.endswith(HOME_LINK)
        assert channel.urls == [SLACK]
        # Within the same refresh it is not pushed twice.
        settle(client)
        assert len(channel.received) == 1


def test_the_answer_goes_through_the_sortie_that_holds_the_question(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT)
    channel, clock = FakeChannel(), Clock()
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        r = client.post(f"{P}/stories/answer", json={"key": "billing/b1", "text": "At each release."})
        assert r.status_code == 200, r.text
        assert taichi.answers == [{"identifier": "T-1", "text": "At each release.", "author": "Local operator"}]
        assert p1.answers == [] and not any(p.endswith("/answer") for _, p, _ in p1.requests)
        # Answered: it leaves the home, and is never pushed again.
        assert home(app)["questions"] == []
        clock.t += 2 * REPEAT_SECONDS
        settle(client)
        assert len(channel.received) == 1


def test_questions_on_both_sorties_reach_the_workbench_and_the_channel(store):
    p1, taichi = two_sorties(store)
    p1.park("T-2", "Should b2 keep the old invoice format?", "2026-11-01T10:00:00Z")
    taichi.park("T-1", STATEMENT, "2026-11-01T12:00:00Z")
    channel, clock = FakeChannel(), Clock()
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [(q["identifier"], q["sortie"]) for q in home(app)["questions"]] == [("T-2", "p1"), ("T-1", "taichi")]
        texts = channel.texts()
        assert len(texts) == 2
        assert any("T-2 asks a person on p1's Sortie" in t for t in texts)
        assert any("T-1 asks a person on taichi's Sortie" in t for t in texts)
        # Each answer goes to its own Sortie.
        assert client.post(f"{P}/stories/answer", json={"key": "billing/b2", "text": "Yes."}).status_code == 200
        assert [a["identifier"] for a in p1.answers] == ["T-2"] and taichi.answers == []


# Scenario: Still open ----------------------------------------------------------------


def test_a_question_still_open_four_hours_later_reaches_the_channel_once_more(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT)
    channel, clock = FakeChannel(), Clock()
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert len(channel.received) == 1
        clock.t += REPEAT_SECONDS - 1
        settle(client)
        assert len(channel.received) == 1
        clock.t += 1  # four hours after it was pushed
        settle(client)
        assert len(channel.received) == 2
        again = channel.texts()[1]
        assert again.startswith("Still open after 4 hours: T-1") and STATEMENT in again and again.endswith(HOME_LINK)
        settle(client)
        assert len(channel.received) == 2
        # Released in the tracker: no longer open, so no longer pushed.
        taichi.unpark("T-1")
        settle(client)
        clock.t += 2 * REPEAT_SECONDS
        settle(client)
        assert len(channel.received) == 2


def test_a_new_park_of_the_same_story_is_a_new_question(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT, "2026-11-01T12:00:00Z")
    channel, clock = FakeChannel(), Clock()
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        taichi.park("T-1", "And which day of the week?", "2026-11-01T13:00:00Z")
        settle(client)
        assert len(channel.received) == 2 and "And which day of the week?" in channel.texts()[1]
        assert not channel.texts()[1].startswith("Still open")


def test_a_restart_pushes_nothing_twice(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT)
    channel, clock = FakeChannel(), Clock()
    with TestClient(app_with(store, p1, taichi, channel, clock)) as client:
        sign_in(client)
        settle(client)
    assert len(channel.received) == 1
    clock.t += REPEAT_SECONDS / 2
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert len(channel.received) == 1
        clock.t += REPEAT_SECONDS / 2
        settle(client)
        assert len(channel.received) == 2


def test_a_sortie_that_cannot_be_read_keeps_its_questions(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT)
    channel, clock = FakeChannel(), Clock()
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        taichi.unreachable = True
        settle(client)
        assert [q["identifier"] for q in home(app)["questions"]] == ["T-1"]
        others = project(app).hub.doc["sortie"]["others"]
        assert others == [{"name": "taichi", "reachable": False, "error": others[0]["error"]}] and others[0]["error"]
        taichi.unreachable = False
        settle(client)
        assert project(app).hub.doc["sortie"]["others"] == [{"name": "taichi", "reachable": True, "error": None}]
        # Back again, it is the same question: not pushed a second time.
        assert len(channel.received) == 1


def test_a_refused_push_is_tried_again_a_minute_later(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT)
    channel, clock = FakeChannel(), Clock()
    channel.status = 503
    app = app_with(store, p1, taichi, channel, clock)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        tries = len(channel.urls)
        assert tries >= 1 and channel.received == []
        settle(client)
        assert len(channel.urls) == tries
        channel.status = 200
        clock.t += RETRY_SECONDS
        settle(client)
        assert len(channel.received) == 1 and not channel.texts()[0].startswith("Still open")


def test_a_read_only_workbench_pushes_nothing(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT)
    channel, clock = FakeChannel(), Clock()
    app = app_with(store, p1, taichi, channel, clock, sync_enabled=False)
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [q["sortie"] for q in home(app)["questions"]] == ["taichi"]
        assert channel.received == []


def test_without_a_channel_the_questions_still_show(store):
    p1, taichi = two_sorties(store)
    taichi.park("T-1", STATEMENT)
    app = create_app(make_config(store.root, sortie_name="p1"), validate=validate_offline, sortie=p1.client(),
                     sorties={"taichi": taichi.client()})
    with TestClient(app) as client:
        sign_in(client)
        settle(client)
        assert [q["sortie"] for q in home(app)["questions"]] == ["taichi"]


# The channel kinds ---------------------------------------------------------------------


def push(kind: str, secret: str, chat_id: str = "") -> tuple[str, dict]:
    fake = FakeChannel()

    async def send():
        channel = fake.channel(kind, secret, chat_id)
        try:
            await channel.send("T-1 asks a person @everyone", {"identifier": "T-1", "url": HOME_LINK})
        finally:
            await channel.aclose()

    asyncio.run(send())
    return fake.urls[0], fake.received[0]


def test_each_kind_of_channel_gets_the_body_it_takes():
    assert push("slack", SLACK) == (SLACK, {"text": "T-1 asks a person @everyone"})
    url, body = push("discord", "https://discord.test/api/webhooks/1/x")
    assert url == "https://discord.test/api/webhooks/1/x"
    assert body == {"content": "T-1 asks a person @everyone", "allowed_mentions": {"parse": []}}
    url, body = push("telegram", "123:bot-token", "-1001")
    assert url == "https://api.telegram.org/bot123:bot-token/sendMessage"
    assert body == {"chat_id": "-1001", "text": "T-1 asks a person @everyone"}
    url, body = push("webhook", "https://ops.test/hook")
    assert url == "https://ops.test/hook"
    assert body == {"identifier": "T-1", "url": HOME_LINK, "text": "T-1 asks a person @everyone"}
    assert cut("x" * 10, 5) == "xxxx…" and cut("abc", 5) == "abc"


# Configuration ---------------------------------------------------------------------------


def base_env(tmp_path: Path) -> dict[str, str]:
    return {"WORKBENCH_HOME": str(tmp_path / "home"), "WORKBENCH_SORTIE_URL": "http://127.0.0.1:7680",
            "WORKBENCH_PASSWORD_HASH": "h", "WORKBENCH_SECRET_KEY": "k"}


def test_the_other_sorties_and_their_tokens_come_from_the_environment(tmp_path):
    tokens = tmp_path / "sorties"
    tokens.mkdir()
    (tokens / "taichi.token").write_text(TAICHI_TOKEN + "\n")
    env = {**base_env(tmp_path), "WORKBENCH_SORTIE_NAME": "p1", "WORKBENCH_SORTIES": " taichi=http://127.0.0.1:7681/ ",
           "WORKBENCH_SORTIE_TOKEN_DIR": str(tokens)}
    config = Config.from_env(env)
    assert config.sortie_name == "p1"
    assert config.other_sorties == (SortieSource("taichi", "http://127.0.0.1:7681", TAICHI_TOKEN),)
    assert TAICHI_TOKEN not in repr(config)
    assert Config.from_env(base_env(tmp_path)).other_sorties == () and Config.from_env(base_env(tmp_path)).sortie_name == "sortie"
    for bad in ("taichi", "Taichi=http://x", "taichi=ftp://x", "p1=http://x", "a=http://x a=http://y", "nope=http://x"):
        with pytest.raises(ConfigError):
            Config.from_env({**env, "WORKBENCH_SORTIES": bad})


def test_the_channel_credential_is_read_from_a_file_only_its_owner_writes(tmp_path):
    secret = tmp_path / "notify-credential"
    secret.write_text(SLACK + "\n")
    os.chmod(secret, 0o600)
    env = {**base_env(tmp_path), "WORKBENCH_NOTIFY_KIND": "slack", "WORKBENCH_NOTIFY_CREDENTIAL_FILE": str(secret),
           "WORKBENCH_PUBLIC_URL": PUBLIC + "/"}
    config = Config.from_env(env)
    assert (config.notify_kind, config.notify_secret, config.public_url) == ("slack", SLACK, PUBLIC)
    assert SLACK not in repr(config)
    os.chmod(secret, 0o620)
    with pytest.raises(ConfigError, match="writable"):
        Config.from_env(env)
    os.chmod(secret, 0o600)
    for drop in ("WORKBENCH_NOTIFY_CREDENTIAL_FILE", "WORKBENCH_PUBLIC_URL"):
        with pytest.raises(ConfigError):
            Config.from_env({k: v for k, v in env.items() if k != drop})
    with pytest.raises(ConfigError):
        Config.from_env({**env, "WORKBENCH_NOTIFY_KIND": "email"})
    with pytest.raises(ConfigError, match="CHAT_ID"):
        Config.from_env({**env, "WORKBENCH_NOTIFY_KIND": "telegram"})
    assert Config.from_env({**env, "WORKBENCH_NOTIFY_KIND": "telegram", "WORKBENCH_NOTIFY_CHAT_ID": "-1001"}).notify_chat_id == "-1001"
    assert Config.from_env(base_env(tmp_path)).notify_kind == ""
