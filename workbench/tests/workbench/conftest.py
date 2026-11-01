from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Callable

import pytest
from fastapi.testclient import TestClient

from workbench.auth import hash_password
from workbench.config import Config
from tests.conftest import StoreBuilder

PASSWORD = "correct horse"
SORTIE_TOKEN = "sortie-control-token-0123456789"
BROKER_TOKEN = "broker-viewer-token-9876543210"
_HASH = hash_password(PASSWORD)


# A store built by the ``store`` fixture registers as this project.
SLUG = "store"
P = f"/api/projects/{SLUG}"
TRACKER_KEY = "T"


def make_config(store: Path | None, **overrides) -> Config:
    """A config that imports ``store`` as project ``store``, syncing into T."""
    home = overrides.pop("home", None) or ((store.parent if store else Path.cwd()) / "wb-home")
    values = dict(
        home=home, store=store, huly_project=TRACKER_KEY if store else "", sortie_url="http://sortie.test", sortie_token=SORTIE_TOKEN,
        password_hash=_HASH, secret_key="test-secret", frontend_dir=None, debounce_ms=50, poll_seconds=0.2,
    )
    values.update(overrides)
    return Config(**values)


def sign_in(client: TestClient) -> None:
    r = client.post("/api/login", json={"password": PASSWORD})
    assert r.status_code == 200, r.text


@pytest.fixture
def open_registry(tmp_path: Path):
    """Open the registry at tmp_path/wb.db, closing every one opened when the test ends."""
    from workbench.registry import Registry

    opened: list[Registry] = []

    def open_() -> Registry:
        opened.append(Registry(tmp_path / "wb.db"))
        return opened[-1]

    yield open_
    for reg in opened:
        reg.close()


@pytest.fixture
def config(store) -> Config:
    store.plan([])
    return make_config(store.root)


def project(app, slug: str = SLUG):
    """The project's runtime: its plan, hub, sessions and pending syncs."""
    return app.state.projects.runtimes[slug]


def settle(client: TestClient, slug: str = SLUG) -> None:
    """Bring the project up to date now: read the store, then poll Sortie once.

    The app's file watcher and poll loop also do this, in the background and
    at their own pace. Tests that are not about those loops call this
    instead of waiting for them, so their outcome never depends on timing.
    It runs on the app's event loop, as the poll loop does."""
    rt = project(client.app, slug)

    async def run() -> None:
        await asyncio.to_thread(rt.plan.refresh)
        await rt.hub.poll_once()

    client.portal.call(run)


@pytest.fixture
def store(tmp_path: Path) -> StoreBuilder:
    """A throwaway store whose breakdowns' covers get the specs they name."""
    return StoreBuilder(tmp_path / "store", specs_for_covers=True)


@pytest.fixture
def make_store(tmp_path: Path) -> Callable[[str], StoreBuilder]:
    def make(name: str) -> StoreBuilder:
        return StoreBuilder(tmp_path / name, specs_for_covers=True)
    return make
