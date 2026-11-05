"""A preview's review signs in by the previews' review key, which the
control plane's egress gate sets on every request of a review job to a
preview (agent-platform/run-supervision, "A UI is approved as a prototype,
and the built story merges after its review", scenario The review machine
reaches the preview): its page and API calls succeed with no password."""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from workbench.app import create_app
from workbench.auth import REVIEW_JOB_HEADER, REVIEW_KEY_HEADER
from workbench.config import Config, ConfigError
from tests.workbench.conftest import make_config

KEY = "the-previews-review-key"


def test_the_review_key_signs_the_review_in(tmp_path):
    client = TestClient(create_app(make_config(None, home=tmp_path / "home", review_key=KEY, sync_enabled=False)))

    r = client.get("/api/me", headers={REVIEW_KEY_HEADER: KEY, REVIEW_JOB_HEADER: "ONS-125"})

    assert r.status_code == 200
    assert r.json()["identity"]["name"] == "Review of ONS-125"
    assert client.get("/api/projects", headers={REVIEW_KEY_HEADER: KEY}).status_code == 200


@pytest.mark.parametrize("headers", [{}, {REVIEW_KEY_HEADER: "made-up"}, {REVIEW_KEY_HEADER: ""}])
def test_without_the_review_key_a_request_signs_in_as_before(tmp_path, headers):
    client = TestClient(create_app(make_config(None, home=tmp_path / "home", review_key=KEY, sync_enabled=False)))

    assert client.get("/api/me", headers=headers).status_code == 401


def test_a_workbench_without_a_review_key_takes_none(tmp_path):
    client = TestClient(create_app(make_config(None, home=tmp_path / "home")))

    assert client.get("/api/me", headers={REVIEW_KEY_HEADER: ""}).status_code == 401
    assert client.get("/api/me", headers={REVIEW_KEY_HEADER: KEY}).status_code == 401


def test_only_a_read_only_workbench_takes_a_review_key(tmp_path):
    key = tmp_path / "review-key"
    key.write_text(KEY + "\n")
    env = {"WORKBENCH_SORTIE_URL": "http://sortie", "WORKBENCH_SECRET_KEY": "k", "WORKBENCH_PASSWORD_HASH": "h",
           "WORKBENCH_HOME": str(tmp_path / "home"), "WORKBENCH_REVIEW_KEY_FILE": str(key)}

    with pytest.raises(ConfigError, match="WORKBENCH_SYNC=0"):
        Config.from_env(env)
    assert Config.from_env({**env, "WORKBENCH_SYNC": "0"}).review_key == KEY
    assert KEY not in repr(Config.from_env({**env, "WORKBENCH_SYNC": "0"}))
    key.write_text("")
    with pytest.raises(ConfigError, match="empty"):
        Config.from_env({**env, "WORKBENCH_SYNC": "0"})
