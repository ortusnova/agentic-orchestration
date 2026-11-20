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

from . import legend

_ID_PATTERN = re.compile(r"^\^([a-z]+)_\[")
_PRIMITIVES = {"string": "str", "integer": "int", "number": "float", "boolean": "bool"}
_NO_DEFAULT = object()

KEY_PREFIX = "\u2060"
"""A word joiner: invisible, and not whitespace to Mermaid. Mermaid lexes ``PK`` and ``FK`` as keys in the
type position, and needs a word there; prefixing it lets the key column hold ``PK``, ``FK``, or nothing."""


def _py_literal(value: Any) -> str:
    """A JSON value as Python writes it, with single quotes so it can sit inside a Mermaid comment."""
    if isinstance(value, str):
        return "'" + value.replace("'", "\\'") + "'"
    if value is None or isinstance(value, bool):
        return repr(value)
    if isinstance(value, list):
        return "[" + ", ".join(_py_literal(v) for v in value) + "]"
    if isinstance(value, dict):
        return "{" + ", ".join(f"{_py_literal(k)}: {_py_literal(v)}" for k, v in value.items()) + "}"
    return repr(value)


@dataclass(frozen=True)
class Field:
    name: str

    type: str
    """The base type: a primitive, an enum, a model, ``<Entity>Id``, or ``A | B`` for a union."""

    python: str
    """The field's full type in Python notation, as the diagrams show it: ``list[PersonId] = []``."""

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
        default = prop.get("default", _NO_DEFAULT) if defaulted else _NO_DEFAULT
        prop, nullable = _strip_optional(prop)
        many = prop.get("type") == "array"
        if many:
            prop = prop["items"]
        optional = nullable or (defaulted and not many)

        def make(type_: str, **kw: Any) -> Field:
            python = f"list[{type_}]" if many else type_
            if nullable:
                python += " | None"
            if default is not _NO_DEFAULT:
                is_model = isinstance(default, dict) and type_ in self._defs and not self.is_enum(type_)
                python += f" = {type_}()" if is_model else f" = {_py_literal(default)}"
            return Field(name, type_, python, many, optional, **kw)

        if (ref := _ref(prop)) is not None:
            return make(ref) if self.is_enum(ref) else make(ref, embeds=(ref,))
        if "oneOf" in prop:
            branches = tuple(r for r in map(_ref, prop["oneOf"]) if r)
            return make(" | ".join(branches), embeds=branches)
        m = _ID_PATTERN.match(prop.get("pattern", ""))
        if m:
            target = self._by_prefix.get(m.group(1))
            if name == "id":
                return make(f"{target}Id", primary_key=True)
            return make(f"{target}Id", ref_entity=target)
        if "const" in prop:
            return make(f"Literal[{_py_literal(prop['const'])}]")
        if prop.get("format") == "date-time":
            return make("datetime")
        return make(_PRIMITIVES.get(prop.get("type", ""), "Any"))

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
    """One field row, in three columns: key, name, Python type.

    Mermaid's columns are type, name, keys, comment. The key goes in the type position and the Python type
    in the comment, so the row reads key | name | type.
    """
    key = "PK" if f.primary_key else "FK" if f.ref_entity else ""
    return f"        {KEY_PREFIX}{key} {f.name} {_quote(f.python)}"


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


DIAGRAM_CONFIG = """---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
"""
"""Front matter on every diagram: the model name at the top of each box is bold. A renderer that ignores
``themeCSS`` still draws the diagram, with the name in normal weight."""


def _diagrams(domain: Domain) -> list[tuple[str, str, str]]:
    """Every diagram, as (title, intro, Mermaid source with its front matter)."""
    return [(title, intro, DIAGRAM_CONFIG + source) for title, intro, source in _plain_diagrams(domain)]


def _plain_diagrams(domain: Domain) -> list[tuple[str, str, str]]:
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


def _legend_markdown() -> list[str]:
    out = ["## Legend", ""]
    for group in legend.groups():
        out += [f"### {group.title}", "", "| Symbol | Name | Meaning |", "|---|---|---|"]
        for e in group.entries:
            out.append(f'| <img src="legend/{e.file}" alt="{e.name}"> | **{e.name}** | {e.meaning} |')
        out.append("")
    return out


def _inline_html(text: str) -> str:
    escaped = text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    return re.sub(r"`([^`]+)`", r"<code>\1</code>", escaped)


def _legend_html() -> str:
    parts = ['<section id="legend"><h2>Legend</h2>']
    for group in legend.groups():
        parts.append(f"<h3>{group.title}</h3><table><tr><th>Symbol</th><th>Name</th><th>Meaning</th></tr>")
        for e in group.entries:
            parts.append(f"<tr><td>{e.svg}</td><td><b>{e.name}</b></td><td>{_inline_html(e.meaning)}</td></tr>")
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
