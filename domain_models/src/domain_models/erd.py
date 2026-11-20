"""Entity-relationship diagrams of the domain, drawn from the generated JSON Schema.

The diagrams read the schema, not the Python classes, so they show what every
language sees:

- the overview: records only, each joined to the records it belongs to;
- one diagram per record: its fields, the value objects it embeds (solid
  lines), and the records it refers to by ID (dashed lines).
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from typing import Any

_ID_PATTERN = re.compile(r"^\^([a-z]+)_\[")
_PRIMITIVES = {"string": "string", "integer": "int", "number": "float", "boolean": "bool"}


@dataclass(frozen=True)
class Field:
    name: str

    type: str
    """The attribute type shown: a primitive, an enum, a model, or ``<Entity>Id``."""

    many: bool
    optional: bool

    ref_entity: str | None = None
    """The entity an ID field refers to."""

    embeds: tuple[str, ...] = ()
    """The models this field embeds; several for a union."""

    primary_key: bool = False


@dataclass(frozen=True)
class Model:
    name: str
    record: bool
    fields: tuple[Field, ...]


def _strip_optional(prop: dict[str, Any]) -> tuple[dict[str, Any], bool]:
    options = prop.get("anyOf")
    if options and len(options) == 2 and {"type": "null"} in options:
        return next(o for o in options if o != {"type": "null"}), True
    return prop, False


def _ref(prop: dict[str, Any]) -> str | None:
    ref = prop.get("$ref")
    return ref.rsplit("/", 1)[1] if ref else None


class Domain:
    """The models of one generated schema, and how they relate."""

    def __init__(self, schema: dict[str, Any]):
        self._defs: dict[str, Any] = schema["$defs"]
        self._records: list[str] = schema["x-records"]
        self._by_prefix: dict[str, str] = {}
        for name, d in self._defs.items():
            id_prop = (d.get("properties") or {}).get("id", {})
            m = _ID_PATTERN.match(id_prop.get("pattern", ""))
            if m:
                self._by_prefix[m.group(1)] = name
        self.models: dict[str, Model] = {
            name: self._model(name, d) for name, d in self._defs.items() if d.get("type") == "object"
        }

    @property
    def common_record_fields(self) -> list[str]:
        """The fields every record has, in the order of the first record. Not ``id``: its type differs."""
        names = [f.name for f in self.models[self._records[0]].fields if not f.primary_key]
        return [n for n in names if all(n in {f.name for f in self.models[r].fields} for r in self._records)]

    @property
    def records(self) -> list[str]:
        return list(self._records)

    def is_enum(self, name: str) -> bool:
        return "enum" in self._defs.get(name, {})

    def prefix_of(self, entity: str) -> str | None:
        return next((p for p, e in self._by_prefix.items() if e == entity), None)

    def _model(self, name: str, d: dict[str, Any]) -> Model:
        required = set(d.get("required", []))
        fields = tuple(self._field(fname, prop, fname not in required) for fname, prop in d["properties"].items())
        return Model(name=name, record=name in self._records, fields=fields)

    def _field(self, name: str, prop: dict[str, Any], defaulted: bool) -> Field:
        prop, nullable = _strip_optional(prop)
        many = prop.get("type") == "array"
        if many:
            prop = prop["items"]
        optional = nullable or (defaulted and not many)

        if (ref := _ref(prop)) is not None:
            if self.is_enum(ref):
                return Field(name, ref, many, optional)
            return Field(name, ref, many, optional, embeds=(ref,))
        if "oneOf" in prop:
            branches = tuple(r for r in map(_ref, prop["oneOf"]) if r)
            return Field(name, "|".join(branches), many, optional, embeds=branches)
        m = _ID_PATTERN.match(prop.get("pattern", ""))
        if m:
            target = self._by_prefix.get(m.group(1))
            if name == "id":
                return Field(name, f"{target}Id", many, optional, primary_key=True)
            return Field(name, f"{target}Id", many, optional, ref_entity=target)
        if "const" in prop:
            return Field(name, "const", many, optional)
        if prop.get("format") == "date-time":
            return Field(name, "datetime", many, optional)
        return Field(name, _PRIMITIVES.get(prop.get("type", ""), "any"), many, optional)

    def embedded_in(self) -> dict[str, str]:
        """For each value object, the first record that embeds it."""
        out: dict[str, str] = {}
        for record in self._records:
            stack = [record]
            seen = {record}
            while stack:
                for f in self.models[stack.pop()].fields:
                    for target in f.embeds:
                        if target in self._records or target in seen:
                            continue
                        seen.add(target)
                        out.setdefault(target, record)
                        stack.append(target)
        return out

    def references(self, record: str) -> list[tuple[str, str, bool]]:
        """The records ``record`` reaches by ID, through its embedded models too: (field path, record, many).

        A reference to a value object that has its own ID, such as a ``Repository``, counts as one to the
        record that embeds it.
        """
        owner = self.embedded_in()
        refs: list[tuple[str, str, bool]] = []
        self._walk(record, "", False, refs, set())
        return [(path, owner.get(target, target), many) for path, target, many in refs]

    def _walk(self, name: str, path: str, many: bool, refs: list[tuple[str, str, bool]], seen: set[str]) -> None:
        if name in seen:
            return
        for f in self.models[name].fields:
            here = f"{path}{f.name}"
            if f.ref_entity:
                refs.append((here, f.ref_entity, many or f.many))
            for target in f.embeds:
                if target not in self._records:
                    self._walk(target, here + ".", many or f.many, refs, seen | {name})


def _quote(text: str) -> str:
    return '"' + text.replace('"', "'") + '"'


def _card(many: bool, optional: bool) -> str:
    """The marker at the target end of a line: how many targets one source has."""
    if many:
        return "o{"
    return "o|" if optional else "||"


def _attribute(f: Field) -> str:
    type_ = re.sub(r"[^A-Za-z0-9_]", "_", f.type) + ("[]" if f.many else "")
    key = " PK" if f.primary_key else " FK" if f.ref_entity else ""
    note = " " + _quote("optional") if f.optional else ""
    return f"        {type_} {f.name}{key}{note}"


def parents(domain: Domain, record: str) -> list[tuple[str, str]]:
    """The records ``record`` belongs to: its required, single ID fields, as (field, record)."""
    return [
        (f.name, f.ref_entity)
        for f in domain.models[record].fields
        if f.ref_entity and not f.optional and not f.many and f.ref_entity in domain.records
    ]


def overview(domain: Domain) -> str:
    """Records only, each joined to the records it belongs to. Roots are the records that belong to none."""
    lines = ["erDiagram"]
    lines.extend(f"    {record}" for record in domain.records)
    for record in domain.records:
        for field, parent in parents(domain, record):
            lines.append(f"    {record} }}o..|| {parent} : {_quote(field)}")
    return "\n".join(lines)


def record_diagram(domain: Domain, record: str) -> str:
    """One record with every field, the value objects it embeds, and the records it refers to."""
    embedded = [record]
    for name in embedded:
        for f in domain.models[name].fields:
            embedded.extend(t for t in f.embeds if t not in embedded)
    common = set(domain.common_record_fields) if record in domain.records else set()
    lines = ["erDiagram"]
    for name in embedded:
        lines.append(f"    {name} {{")
        lines.extend(_attribute(f) for f in domain.models[name].fields if not (name == record and f.name in common))
        lines.append("    }")
    for name in embedded:
        for f in domain.models[name].fields:
            for target in f.embeds:
                lines.append(f"    {name} ||--{_card(f.many, f.optional)} {target} : {_quote(f.name)}")
            if f.ref_entity:
                lines.append(f"    {name} }}o..{_card(f.many, f.optional)} {f.ref_entity} : {_quote(f.name)}")
    return "\n".join(lines)


def _diagrams(domain: Domain) -> list[tuple[str, str, str]]:
    """Every diagram, as (title, intro, Mermaid source)."""
    out = [
        (
            "Overview",
            "Records only. A line joins a record to a record it belongs to: a required ID field holding one ID."
            " A record with no line out is a root. Every line here is a reference by ID, even where Mermaid"
            " draws one solid at a crossing.",
            overview(domain),
        )
    ]
    for record in domain.records:
        out.append(
            (
                record,
                "Solid lines embed a value object; dashed lines refer to another record by ID.",
                record_diagram(domain, record),
            )
        )
    return out


def _md_list(names: list[str]) -> str:
    return ", ".join(f"`{n}`" for n in names) or "—"


LEGEND_EXAMPLE = """erDiagram
    Session {
        SessionId id PK
        WorkstreamId workstream_id FK
        PersonId held_by FK "optional"
        AgentRef agent
        SessionState state
        Usage usage "optional"
        datetime started_at "optional"
    }
    Workstream {
        PersonId[] participants FK
    }
    AgentRef {
        string harness
        string model
    }
    Usage {
        int input_tokens "optional"
    }
    Session }o..|| Workstream : "workstream_id"
    Session }o..o| Person : "held_by"
    Workstream }o..o{ Person : "participants"
    Session ||--|| AgentRef : "agent"
    Session ||--o| Usage : "usage"
"""
"""A small diagram, cut from the Session and Workstream diagrams, with every kind of line and line end."""

LEGEND_INTRO = "An example, cut from the `Session` and `Workstream` diagrams. It has every kind of line and line end:"

LEGEND: list[tuple[str, list[str], list[tuple[str, str]]]] = [
    (
        "Boxes",
        ["Shape", "Meaning"],
        [
            (
                "Box with a list of fields",
                "A model, drawn in full. In a record's own diagram, the record and every value object it contains.",
            ),
            (
                "Box with a name only",
                "Another record, drawn only to show the line to it. It has its own diagram below.",
            ),
            ("Box colour", "Nothing. Mermaid picks the colours."),
        ],
    ),
    (
        "Lines",
        ["Line", "Meaning"],
        [
            (
                "Solid",
                "Contains: one model holds the other in the field the label names. The other is a value object,"
                " stored only inside its owner, so the owner's end always shows two bars: exactly one owner.",
            ),
            (
                "Dashed",
                "Refers to by ID: the model with the field the label names (marked `FK`) holds the other record's ID.",
            ),
        ],
    ),
    (
        "Line ends",
        ["End (as drawn)", "Meaning"],
        [
            ("Two bars", "exactly one"),
            ("A circle and a bar", "zero or one: the field is optional"),
            ("A circle and a crow's foot", "zero or many: the field is a list, or many records may hold the same ID"),
        ],
    ),
    (
        "Reading a line",
        ["Example", "Reads as"],
        [
            (
                "Dashed from `Session` to `Workstream`, labelled `workstream_id`: two bars at `Workstream`,"
                " a circle and a crow's foot at `Session`",
                "Each session refers to exactly one workstream, in `workstream_id`. A workstream has zero or"
                " many sessions. The end next to a box says how many of that box the box at the other end has.",
            ),
            (
                "Solid from `Session` to `Usage`, labelled `usage`: two bars at `Session`, a circle and a bar"
                " at `Usage`",
                "A session contains zero or one `Usage`, in `usage`. Each `Usage` belongs to exactly one session.",
            ),
            (
                "Dashed from `Workstream` to `Person`, labelled `participants`: a circle and a crow's foot at each end",
                "A workstream refers to zero or many people, in the list `participants`. A person may be a"
                " participant in zero or many workstreams.",
            ),
        ],
    ),
    (
        "Fields",
        ["Column or type", "Meaning"],
        [
            ("First column", "The field's type."),
            ("Second column", "The field's name."),
            ("`PK`", "The record's own ID."),
            ("`FK`", "The ID of another record. A dashed line leads to it."),
            ('`"optional"`', "The field may be absent or null. A list is never marked: it may be empty."),
            ("`SessionId`, `WorkstreamId`, ...", "An ID of that record: `<prefix>_<ULID>`. Prefixes are in the table."),
            ("`Type[]`", "A list of `Type`."),
            ("A capitalised type with no box, such as `SessionState`", "An enum. Its values are in the schema."),
            ("`A_B_C`", "One of several models: `A`, `B`, or `C`. Each has a solid line."),
            ("`datetime`", "A point in time, always in UTC."),
            ("`const`", "A fixed value: the schema version, or the `kind` that tells union members apart."),
            ("`string`, `int`, `float`, `bool`", "As named."),
        ],
    ),
]
"""The legend, as (section, column headings, rows). Cells use Markdown backticks for code."""


def _legend_markdown() -> list[str]:
    example = LEGEND_EXAMPLE.rstrip("\n")
    out = [
        "## Legend",
        "",
        LEGEND_INTRO,
        "",
        "```mermaid",
        example,
        "```",
        "",
    ]
    for title, heads, rows in LEGEND:
        out += [f"### {title}", "", f"| {heads[0]} | {heads[1]} |", "|---|---|"]
        out += [f"| {a.replace('|', chr(92) + '|')} | {b.replace('|', chr(92) + '|')} |" for a, b in rows]
        out.append("")
    return out


def _inline_html(text: str) -> str:
    escaped = text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    return re.sub(r"`([^`]+)`", r"<code>\1</code>", escaped)


def _legend_html() -> str:
    parts = [f'<section id="legend"><h2>Legend</h2><p>{_inline_html(LEGEND_INTRO)}</p>']
    parts.append(f'<pre class="mermaid">\n{LEGEND_EXAMPLE.rstrip()}\n</pre>')
    for title, heads, rows in LEGEND:
        parts.append(f"<h3>{title}</h3><table><tr><th>{heads[0]}</th><th>{heads[1]}</th></tr>")
        parts += [f"<tr><td>{_inline_html(a)}</td><td>{_inline_html(b)}</td></tr>" for a, b in rows]
        parts.append("</table>")
    parts.append("</section>")
    return "\n".join(parts)


def markdown(schema: dict[str, Any]) -> str:
    """The diagram document: a table of the records, the overview, and one diagram per record."""
    domain = Domain(schema)
    rows = []
    for record in domain.records:
        belongs = sorted({p for _, p in parents(domain, record)})
        others = sorted({t for _, t, _ in domain.references(record)} - set(belongs) - {record})
        rows.append(f"| `{record}` | `{domain.prefix_of(record)}_` | {_md_list(belongs)} | {_md_list(others)} |")
    value_objects = sorted(n for n, m in domain.models.items() if not m.record)
    out = [
        "# Domain model diagrams",
        "",
        *_legend_markdown(),
        "## About this file",
        "",
        "Generated from `schemas/domain.schema.json` by `make -C domain_models schemas`. Do not edit.",
        "",
        "A **record** is stored on its own and has an ID. Every other model is a **value object**, stored",
        "only inside a record. Every record also has "
        + _md_list(domain.common_record_fields)
        + ", which the per-record diagrams leave out.",
        "",
        "## Records",
        "",
        "| Record | ID prefix | Belongs to | Also refers to |",
        "|---|---|---|---|",
        *rows,
        "",
        f"Value objects: {_md_list(value_objects)}.",
        "",
    ]
    for i, (title, intro, source) in enumerate(_diagrams(domain)):
        out += [("## " if i == 0 else "### ") + title, "", intro, "", "```mermaid", source, "```", ""]
        if i == 0:
            out += ["## Each record", ""]
    return "\n".join(out)


def html(schema: dict[str, Any], mermaid_url: str) -> str:
    """A standalone page that renders every diagram, for viewing outside GitHub."""
    domain = Domain(schema)
    sections = []
    nav = []
    for i, (title, intro, source) in enumerate(_diagrams(domain)):
        anchor = title.lower()
        nav.append(f'<a href="#{anchor}">{title}</a>')
        tag = "h2" if i == 0 else "h3"
        heading = f"<{tag}>{title}</{tag}><p>{intro}</p>"
        sections.append(f'<section id="{anchor}">{heading}\n<pre class="mermaid">\n{source}\n</pre></section>')
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Domain model diagrams</title>
<style>
  body {{ font-family: system-ui, sans-serif; margin: 24px; }}
  nav {{ position: sticky; top: 0; background: #fff; padding: 8px 0; border-bottom: 1px solid #ddd; }}
  nav a {{ margin-right: 12px; }}
  section {{ margin: 32px 0; }}
  pre.mermaid {{ overflow: auto; }}
  .error {{ color: #b00020; white-space: pre-wrap; }}
  table {{ border-collapse: collapse; margin: 8px 0 16px; }}
  th, td {{ border: 1px solid #ddd; padding: 6px 10px; text-align: left; vertical-align: top; }}
  th {{ background: #f6f6f6; }}
</style>
</head>
<body>
<h1>Domain model diagrams</h1>
<nav><a href="#legend">Legend</a> {" ".join(nav)}</nav>
{_legend_html()}
{chr(10).join(sections)}
<script type="module">
  import mermaid from {json.dumps(mermaid_url)};
  mermaid.initialize({{ startOnLoad: false, maxTextSize: 200000, er: {{ useMaxWidth: false }} }});
  for (const el of document.querySelectorAll("pre.mermaid")) {{
    try {{
      await mermaid.run({{ nodes: [el], suppressErrors: false }});
    }} catch (err) {{
      const p = document.createElement("p");
      p.className = "error";
      p.textContent = "This diagram did not render: " + err.message;
      el.before(p);
    }}
  }}
</script>
</body>
</html>
"""
