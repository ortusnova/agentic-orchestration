from __future__ import annotations

import json
import subprocess
import sys

from planlib import Schemas, validate
from planlib.cli import main
from tests.conftest import breakdown, deliverable


def no_openspec(*_):
    raise AssertionError("openspec should not be called")


def two_changes(store):
    store.plan([("accounts-api", []), ("billing", ["accounts-api"]), ("search", [])])
    store.change("accounts-api", breakdown(deliverable("accounts-close")))
    store.change(
        "billing",
        breakdown(
            deliverable("invoice", ["accounts-api/accounts-close"], notes="Use the ledger."),
            deliverable("tax", ["invoice"], kind="enabler"),
            deliverable("report", ["tax", "invoice"]),
        ),
    )
    store.change("search")
    store.change("drafty", breakdown(deliverable("x")))


def by_key(graph):
    return {n["plan_key"]: n for n in graph["nodes"]}


def test_graph_matches_schema_and_holds_nodes_and_edges(store):
    two_changes(store)
    r = validate(store.root, show=no_openspec)
    assert r.errors == []
    g = r.graph
    assert Schemas.load(store.root).check("plan-graph", g, file="g") == []

    nodes = by_key(g)
    assert set(nodes) == {
        "accounts-api", "billing", "search",
        "accounts-api/accounts-close", "billing/invoice", "billing/tax", "billing/report",
    }
    assert nodes["billing"]["kind"] == "epic" and nodes["billing/tax"]["deliverable_kind"] == "enabler"
    assert nodes["search"]["approval"] == "unplanned"
    assert nodes["billing/invoice"]["approval"] == "draft"
    assert "Use the ledger." in nodes["billing/invoice"]["body"]
    assert "## Acceptance criteria" in nodes["billing/invoice"]["body"]
    assert {"child": "billing/tax", "parent": "billing"} in g["parents"]
    assert {"blocker": "accounts-api", "blocked": "billing"} in g["blocks"]
    assert {"blocker": "accounts-api/accounts-close", "blocked": "billing/invoice"} in g["blocks"]
    assert g["unplanned_changes"] == ["drafty"]
    assert g["source_digest"].startswith("sha256:")


def test_depth_is_longest_chain_within_view(store):
    two_changes(store)
    nodes = by_key(validate(store.root, show=no_openspec).graph)
    assert nodes["accounts-api"]["depth"] == 0
    assert nodes["billing"]["depth"] == 1
    assert nodes["search"]["depth"] == 0
    # The cross-epic blocker does not push invoice right inside billing's view.
    assert nodes["billing/invoice"]["depth"] == 0
    assert nodes["billing/tax"]["depth"] == 1
    assert nodes["billing/report"]["depth"] == 2


def test_approval_state_flows_into_nodes(store):
    two_changes(store)
    stories = validate(store.root, show=no_openspec).stories
    approvals = {k: v["digest"] for k, v in stories.items() if k.startswith("billing/")}
    nodes = by_key(validate(store.root, show=no_openspec, approvals=approvals).graph)
    assert nodes["billing"]["approval"] == "approved"
    assert nodes["billing/report"]["approval"] == "approved"
    assert nodes["accounts-api"]["approval"] == "draft"


def test_source_digest_changes_with_files(store):
    two_changes(store)
    first = validate(store.root, show=no_openspec).graph["source_digest"]
    assert validate(store.root, show=no_openspec).graph["source_digest"] == first
    store.change("accounts-api", breakdown(deliverable("accounts-close", title="Close accounts")))
    assert validate(store.root, show=no_openspec).graph["source_digest"] != first


def test_invalid_store_outputs_no_graph(store):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("a", ["b"]), deliverable("b", ["a"])))
    r = validate(store.root, show=no_openspec)
    assert r.graph is None and r.errors


def test_cli_validate_ok(store, capsys):
    two_changes(store)
    assert main(["validate", "--store", str(store.root)]) == 0
    assert "plan is valid: 3 epic(s), 4 story(ies)" in capsys.readouterr().out


def test_cli_lists_every_error_and_exits_non_zero(store, capsys):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("a"), deliverable("a"), deliverable("b", ["zzz"])))
    assert main(["validate", "--store", str(store.root)]) == 1
    out = capsys.readouterr().out
    assert "duplicate_id" in out and "dangling_ref" in out


def test_cli_graph_prints_nothing_but_errors_when_invalid(store, capsys):
    store.plan([("billing", [])])
    store.change("billing", breakdown(deliverable("a", ["a"])))
    assert main(["graph", "--store", str(store.root), "--json"]) == 1
    payload = json.loads(capsys.readouterr().out)
    assert payload["ok"] is False and payload["errors"][0]["code"] == "cycle"
    assert "nodes" not in payload


def test_cli_graph_writes_schema_valid_json(store, tmp_path):
    two_changes(store)
    out = tmp_path / "graph.json"
    assert main(["graph", "--store", str(store.root), "-o", str(out)]) == 0
    g = json.loads(out.read_text())
    assert Schemas.load(store.root).check("plan-graph", g, file="g") == []


def test_cli_unusable_store_exits_2(tmp_path, capsys):
    assert main(["validate", "--store", str(tmp_path)]) == 2
    assert "not found" in capsys.readouterr().err


def test_console_script_runs(store):
    two_changes(store)
    proc = subprocess.run(
        [sys.executable, "-m", "planlib.cli", "validate", "--store", str(store.root)],
        capture_output=True, text=True,
    )
    assert proc.returncode == 0, proc.stderr


def test_graph_version_follows_the_store_schema(store):
    """A store created before plan graph version 2 keeps its version 1
    schema, and gets version 1 graphs that its schema accepts."""
    two_changes(store)
    assert validate(store.root, show=no_openspec).graph["schema_version"] == 2

    schema = store.root / "openspec/schemas/planning/plan-graph.schema.json"
    doc = json.loads(schema.read_text())
    doc["properties"]["schema_version"] = {"const": 1}
    for field in ("release", "review", "approved_by", "approved_at"):
        doc["$defs"]["node"]["properties"].pop(field)
    schema.write_text(json.dumps(doc))
    r = validate(store.root, show=no_openspec)
    assert r.errors == [] and r.graph["schema_version"] == 1


def test_each_story_carries_its_resolved_repository(store):
    store.plan([("workers", []), ("plain", [])])
    store.change("workers", breakdown(deliverable("wiring"), deliverable("daemon", repository="agentic-orchestration"),
                                      repository="sortie"))
    store.change("plain", breakdown(deliverable("docs")))
    r = validate(store.root, show=no_openspec)
    assert r.errors == []
    assert Schemas.load(store.root).check("plan-graph", r.graph, file="g") == []
    nodes = by_key(r.graph)
    assert nodes["workers/wiring"]["repository"] == "sortie"
    assert nodes["workers/daemon"]["repository"] == "agentic-orchestration"
    assert "repository" not in nodes["plain/docs"]
    assert "repository" not in nodes["workers"]


def test_a_version_1_graph_carries_no_repository(store):
    store.plan([("workers", [])])
    store.change("workers", breakdown(deliverable("wiring"), repository="sortie"))
    schema = store.root / "openspec/schemas/planning/plan-graph.schema.json"
    doc = json.loads(schema.read_text())
    doc["properties"]["schema_version"] = {"const": 1}
    for field in ("release", "review", "approved_by", "approved_at", "repository"):
        doc["$defs"]["node"]["properties"].pop(field)
    schema.write_text(json.dumps(doc))
    r = validate(store.root, show=no_openspec)
    assert r.errors == [] and r.graph["schema_version"] == 1
    assert all("repository" not in n for n in r.graph["nodes"])


def test_a_story_approval_covers_its_resolved_repository(store):
    store.plan([("workers", [])])
    store.change("workers", breakdown(deliverable("wiring"), repository="sortie"))
    before = validate(store.root, show=no_openspec).stories["workers/wiring"]["digest"]
    store.change("workers", breakdown(deliverable("wiring"), repository="homelab"))
    after = validate(store.root, show=no_openspec).stories["workers/wiring"]["digest"]
    assert before != after
    store.change("workers", breakdown(deliverable("wiring")))
    plain = validate(store.root, show=no_openspec).stories["workers/wiring"]["digest"]
    from planlib import story_digest
    assert plain == story_digest(deliverable("wiring"))


def test_a_repository_name_must_be_kebab_case(store):
    store.plan([("workers", [])])
    store.change("workers", breakdown(deliverable("wiring", repository="Not OK")))
    r = validate(store.root, show=no_openspec)
    assert r.graph is None and any("repository" in str(e) for e in r.errors)
