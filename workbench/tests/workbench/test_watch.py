from __future__ import annotations

import time

from fastapi.testclient import TestClient

from workbench.app import create_app
from tests.conftest import breakdown, deliverable
from tests.workbench.conftest import P, make_config, project, sign_in
from tests.workbench.fake_sortie import FakeSortie


def no_openspec(*_):
    raise AssertionError("openspec should not be called")


def specs_on_disk(root, change):
    """A fake `openspec show`: the change's delta specs as written on disk
    (tests/conftest.py writes them for what a breakdown covers)."""
    specs = root / "openspec" / "changes" / change / "specs"
    deltas = []
    for f in sorted(specs.rglob("spec.md")):
        for line in f.read_text().splitlines():
            if line.startswith("### Requirement: "):
                name = line.removeprefix("### Requirement: ")
                deltas.append({"spec": f.parent.relative_to(specs).as_posix(), "operation": "ADDED",
                               "requirement": {"text": f"It SHALL {name}."}})
    return {"deltas": deltas}


def validate_offline(root, **kw):
    import planlib

    return planlib.validate(root, show=specs_on_disk, **kw)


def wait_for(predicate, timeout=2.0):
    """Only for tests of the file watcher itself; other tests call settle()."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.02)
    return predicate()


def keys(graph):
    return {n["plan_key"] for n in graph["nodes"]}


def watching(app, store) -> None:
    """Return once the watcher reports edits. Its watch starts in the
    background, so an edit made right after startup could come too early.
    Edit a probe file until a refresh follows."""
    rt = project(app)
    assert wait_for(lambda: rt.hub.tracker is not None, timeout=5), "no first poll"
    probe = store.root / "openspec" / ".watch-probe"
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        before = rt.plan.version
        probe.write_text(str(time.monotonic()))
        if wait_for(lambda: rt.plan.version > before, timeout=0.5):
            return
    raise AssertionError("the watcher never reported the probe")


def test_valid_edit_updates_graph_and_cycle_keeps_it(store):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("invoice")))
    # One poll at startup, then none: only the watcher can see the edits.
    cfg = make_config(store.root, debounce_ms=300, poll_seconds=3600)
    app = create_app(cfg, validate=validate_offline, sortie=FakeSortie().client())

    with TestClient(app) as client:
        sign_in(client)
        state = project(app).plan
        assert keys(state.graph) == {"billing", "billing/invoice"}
        watching(app, store)

        start = time.monotonic()
        store.change("billing", breakdown(deliverable("invoice"), deliverable("tax", ["invoice"])))
        assert wait_for(lambda: state.graph and "billing/tax" in keys(state.graph))
        assert time.monotonic() - start < 2.0

        store.change("billing", breakdown(deliverable("invoice", ["tax"]), deliverable("tax", ["invoice"])))
        assert wait_for(lambda: any(e["code"] == "cycle" for e in state.errors))
        assert keys(state.graph) == {"billing", "billing/invoice", "billing/tax"}
        cycle = next(e for e in state.errors if e["code"] == "cycle")
        assert set(cycle["members"]) == {"billing/invoice", "billing/tax"}

        body = client.get(f"{P}/validation").json()
        assert body["valid"] is False and body["has_graph"] is True


def test_broken_store_reports_without_graph(store):
    app = create_app(make_config(store.root), validate=validate_offline)
    with TestClient(app) as client:
        sign_in(client)
        body = client.get(f"{P}/validation").json()
        assert body["has_graph"] is False and body["errors"][0]["code"] == "missing_plan"
