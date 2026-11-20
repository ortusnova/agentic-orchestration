import json
from xml.etree import ElementTree

from domain_models import erd, legend
from domain_models.schema import DIAGRAMS_PATH, LEGEND_DIR, SCHEMA_PATH, generate, outputs

DOMAIN = erd.Domain(json.loads(generate()))


def test_the_committed_outputs_are_current():
    for path, text in outputs().items():
        assert path.read_text() == text, f"{path.name} is stale: run `make -C domain_models schemas`"
    assert {SCHEMA_PATH, DIAGRAMS_PATH} <= set(outputs())


def test_records_and_value_objects_are_told_apart():
    assert DOMAIN.models["Workstream"].record
    assert not DOMAIN.models["Source"].record
    assert "WorkstreamState" not in DOMAIN.models, "an enum is a type, not a model"


def test_a_record_belongs_to_its_required_single_references():
    assert erd.parents(DOMAIN, "Session") == [("workstream_id", "Workstream")]
    assert ("project_id", "Project") in erd.parents(DOMAIN, "Workstream")
    assert all(field != "epic_id" for field, _ in erd.parents(DOMAIN, "Workstream")), "epic_id is optional"


def test_the_roots_belong_to_nothing():
    roots = [r for r in DOMAIN.records if not erd.parents(DOMAIN, r)]
    assert roots == ["Person", "Project", "Worker"]


def test_references_reach_through_embedded_models():
    refs = DOMAIN.references("Workstream")
    assert ("source.requester", "Person", False) in refs
    assert ("blocked_by", "Workstream", True) in refs


def test_a_reference_to_an_embedded_entity_counts_for_its_record():
    assert ("repositories", "Project", True) in DOMAIN.references("Workstream")


def test_a_record_diagram_embeds_solid_and_refers_dashed():
    diagram = erd.record_diagram(DOMAIN, "Workstream")
    assert '    Workstream ||--|| Source : "source"' in diagram
    assert '    Workstream ||--o| StoryRef : "story"' in diagram
    assert '    Workstream }o..|| Project : "project_id"' in diagram
    assert '    Workstream }o..o{ Workstream : "blocked_by"' in diagram
    assert '    Source }o..o| Person : "requester"' in diagram


def test_a_union_embeds_each_branch():
    diagram = erd.record_diagram(DOMAIN, "WorkstreamEvent")
    for branch in ("PersonActor", "ComponentActor", "SessionActor", "ExternalActor"):
        assert f'    WorkstreamEvent ||--|| {branch} : "actor"' in diagram


def test_record_diagrams_leave_out_the_fields_every_record_has():
    assert DOMAIN.common_record_fields == ["schema_version", "created_at", "updated_at"]
    diagram = erd.record_diagram(DOMAIN, "Session")
    assert "created_at" not in diagram
    assert "WorkstreamId workstream_id FK" in diagram


def test_the_page_renders_every_diagram():
    page = erd.html(json.loads(generate()), "https://example.invalid/mermaid.mjs")
    assert page.count('<pre class="mermaid">') == 1 + len(DOMAIN.records), "the overview, each record"
    assert "did not render" in page


def test_the_legend_comes_first():
    doc = erd.markdown(json.loads(generate()))
    assert doc.index("## Legend") < doc.index("## Records") < doc.index("## Overview")
    page = erd.html(json.loads(generate()), "https://example.invalid/mermaid.mjs")
    assert page.index('id="legend"') < page.index('id="overview"')


def test_every_legend_drawing_is_written_and_shown():
    names = set(legend.files())
    assert {p.name for p in outputs() if p.parent == LEGEND_DIR} == names
    assert {p.name for p in LEGEND_DIR.iterdir()} == names, "a stale drawing is left in schemas/legend/"
    doc = erd.markdown(json.loads(generate()))
    for name in names:
        assert f'<img src="legend/{name}"' in doc


def test_every_legend_drawing_is_valid_svg():
    for name, svg in legend.files().items():
        root = ElementTree.fromstring(svg)
        assert root.tag == "{http://www.w3.org/2000/svg}svg", name


def test_line_ends_use_mermaids_markers():
    files = legend.files()
    assert 'marker-end="url(#oneEnd)"' in files["end-one.svg"]
    assert 'marker-end="url(#zeroOneEnd)"' in files["end-zero-or-one.svg"]
    assert 'marker-end="url(#zeroManyEnd)"' in files["end-zero-or-many.svg"]
    assert 'class="line dashed"' in files["line-refers.svg"]
    assert 'class="line"' in files["line-contains.svg"]


def test_every_diagram_draws_references_dashed_and_containment_solid():
    for line in erd.overview(DOMAIN).splitlines()[1:]:
        assert "--" not in line, "the overview has only references"
