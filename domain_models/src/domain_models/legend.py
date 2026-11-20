"""The legend of the diagrams: a drawing of each symbol, its name, and what it means.

The line ends are drawn with the marker shapes Mermaid 12 draws them with
(``chunk-*.mjs``: ``only_one``, ``zero_or_one``, ``zero_or_more``), at the
stroke Mermaid renders (2px black, dashes 2px on, 2px off), so a symbol here
looks the same as on a diagram.
"""

from __future__ import annotations

from dataclasses import dataclass
from html import escape

FONT = "Recursive Variable, arial, sans-serif"
INK = "#000"
BOX_FILL = "#fdf4ff"
BOX_STROKE = "#e879f9"
HIGHLIGHT = "#fde68a"

_MARKERS = """<defs>
  <marker id="oneEnd" refX="18" refY="9" markerWidth="18" markerHeight="18" orient="auto"
    markerUnits="userSpaceOnUse"><path d="M3,0 L3,18 M9,0 L9,18"/></marker>
  <marker id="oneStart" refX="0" refY="9" markerWidth="18" markerHeight="18" orient="auto"
    markerUnits="userSpaceOnUse"><path d="M9,0 L9,18 M15,0 L15,18"/></marker>
  <marker id="zeroOneEnd" refX="30" refY="9" markerWidth="30" markerHeight="18" orient="auto"
    markerUnits="userSpaceOnUse"><circle cx="9" cy="9" r="6"/><path d="M21,0 L21,18"/></marker>
  <marker id="zeroManyEnd" refX="39" refY="18" markerWidth="57" markerHeight="36" orient="auto"
    markerUnits="userSpaceOnUse"><circle cx="9" cy="18" r="6"/><path d="M21,18 Q39,0 57,18 Q39,36 21,18"/></marker>
  <marker id="zeroManyStart" refX="18" refY="18" markerWidth="57" markerHeight="36" orient="auto"
    markerUnits="userSpaceOnUse"><circle cx="48" cy="18" r="6"/><path d="M0,18 Q18,0 36,18 Q18,36 0,18"/></marker>
</defs>"""

_STYLE = f"""<style>
  text {{ font-family: {FONT}; font-size: 13px; fill: {INK}; }}
  marker path, marker circle {{ stroke: {INK}; stroke-width: 2; fill: none; }}
  marker circle {{ fill: #fff; }}
  .line {{ stroke: {INK}; stroke-width: 2; fill: none; }}
  .dashed {{ stroke-dasharray: 2 2; }}
  .box {{ fill: {BOX_FILL}; stroke: {BOX_STROKE}; stroke-width: 1; }}
  .hi {{ fill: {HIGHLIGHT}; stroke: {BOX_STROKE}; stroke-width: 1; }}
</style>"""


def _svg(width: int, height: int, body: str) -> str:
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
        f'viewBox="0 0 {width} {height}">\n{_STYLE}\n{_MARKERS}\n{body}\n</svg>\n'
    )


def _box(x: int, y: int, w: int, h: int, label: str, fill: str = BOX_FILL) -> str:
    return (
        f'<rect x="{x}" y="{y}" width="{w}" height="{h}" style="fill:{fill};stroke:{BOX_STROKE}"/>'
        f'<text x="{x + w // 2}" y="{y + h // 2 + 4}" text-anchor="middle">{escape(label)}</text>'
    )


def _connection(*, dashed: bool, start: str | None, end: str | None, left: str = "A", right: str = "B") -> str:
    """Box ``left``, a line, box ``right``, with optional markers at each end."""
    markers = ""
    if start:
        markers += f' marker-start="url(#{start})"'
    if end:
        markers += f' marker-end="url(#{end})"'
    cls = "line dashed" if dashed else "line"
    return _svg(
        260,
        44,
        # Lines first, boxes over them: Mermaid hides the inner half of a marker behind its box.
        f'<path class="{cls}" d="M51,22 L209,22"{markers}/>' + _box(1, 7, 50, 30, left) + _box(209, 7, 50, 30, right),
    )


_COLUMNS = (0, 44, 164, 424)
"""The x of each column of a field row: key, name, type, and the right edge."""


def _cells(y: int, cells: tuple[str, str, str], highlight: int | None = None) -> list[str]:
    out = []
    for i, text in enumerate(cells):
        x, right = _COLUMNS[i] + 1, _COLUMNS[i + 1] + 1
        cls = "hi" if i == highlight else "box"
        out.append(f'<rect class="{cls}" x="{x}" y="{y}" width="{right - x}" height="28"/>')
        if text:
            out.append(f'<text x="{x + 8}" y="{y + 18}">{escape(text)}</text>')
    return out


def _field_row(cells: tuple[str, str, str], highlight: int) -> str:
    """One field row as a diagram draws it: key, name, type, with cell ``highlight`` marked."""
    return _svg(_COLUMNS[-1] + 2, 30, "".join(_cells(1, cells, highlight)))


def _full_box() -> str:
    rows = [("PK", "id", "SessionId"), ("FK", "workstream_id", "WorkstreamId"), ("", "stage", "str")]
    body = [_box(1, 1, _COLUMNS[-1], 28, "Session")]
    for i, row in enumerate(rows):
        body += _cells(29 + i * 28, row)
    return _svg(_COLUMNS[-1] + 2, 30 + 28 * len(rows), "".join(body))


def _example() -> str:
    body = (
        '<path class="line dashed" d="M91,36 L289,36" marker-start="url(#zeroManyStart)" marker-end="url(#oneEnd)"/>'
        + '<text x="190" y="15" text-anchor="middle">workstream_id</text>'
        + _box(1, 21, 90, 30, "Session")
        + _box(289, 21, 110, 30, "Workstream")
    )
    return _svg(400, 60, body)


@dataclass(frozen=True)
class Entry:
    file: str
    """The drawing's file name under ``schemas/legend/``."""

    svg: str
    name: str

    meaning: str
    """One or two sentences. Backticks mark code."""


@dataclass(frozen=True)
class Group:
    title: str
    entries: tuple[Entry, ...]


def groups() -> tuple[Group, ...]:
    return (
        Group(
            "Lines",
            (
                Entry(
                    "line-contains.svg",
                    _connection(dashed=False, start=None, end=None),
                    "Contains",
                    "A holds B inside it, in the field the label names. B is a value object: it has no ID and is"
                    " stored only as part of A.",
                ),
                Entry(
                    "line-refers.svg",
                    _connection(dashed=True, start=None, end=None),
                    "Refers to by ID",
                    "A has a field, marked `FK`, that holds the ID of record B. The label names the field.",
                ),
            ),
        ),
        Group(
            "Line ends",
            (
                Entry(
                    "end-one.svg",
                    _connection(dashed=False, start=None, end="oneEnd"),
                    "Exactly one",
                    "Each A has exactly one B. The field is required.",
                ),
                Entry(
                    "end-zero-or-one.svg",
                    _connection(dashed=False, start=None, end="zeroOneEnd"),
                    "Zero or one",
                    "Each A has at most one B. The field may be null or left out.",
                ),
                Entry(
                    "end-zero-or-many.svg",
                    _connection(dashed=False, start=None, end="zeroManyEnd"),
                    "Zero or many",
                    "Each A has any number of B: the field is a list, or many A hold the same B.",
                ),
                Entry(
                    "example.svg",
                    _example(),
                    "Reading both ends",
                    "Read each end from the box at the other end. Each `Session` has exactly one `Workstream`,"
                    " in `workstream_id`. Each `Workstream` has zero or many `Session`s.",
                ),
            ),
        ),
        Group(
            "Boxes",
            (
                Entry(
                    "box-full.svg",
                    _full_box(),
                    "Model, in full",
                    "A record or value object with all its fields. Each record has a diagram of its own.",
                ),
                Entry(
                    "box-name.svg",
                    _svg(112, 32, _box(1, 1, 110, 30, "Workstream")),
                    "Other record",
                    "Drawn by name only, to show a line to it. Its fields are in its own diagram.",
                ),
                Entry(
                    "box-colours.svg",
                    _svg(234, 32, _box(1, 1, 110, 30, "Session") + _box(123, 1, 110, 30, "Usage", "#ecfeff")),
                    "Box colour",
                    "Means nothing. Mermaid picks the colours.",
                ),
            ),
        ),
        Group(
            "Fields",
            (
                Entry(
                    "field-pk.svg",
                    _field_row(("PK", "id", "SessionId"), 0),
                    "Own ID",
                    "The record's own ID.",
                ),
                Entry(
                    "field-fk.svg",
                    _field_row(("FK", "workstream_id", "WorkstreamId"), 0),
                    "Reference",
                    "Holds the ID of another record, or a list of them. A dashed line leads to that record.",
                ),
                Entry(
                    "field-name.svg",
                    _field_row(("FK", "workstream_id", "WorkstreamId"), 1),
                    "Name",
                    "The field's name. Line labels use it.",
                ),
                Entry(
                    "field-type.svg",
                    _field_row(("FK", "workstream_id", "WorkstreamId"), 2),
                    "Type",
                    "The field's type, written as in Python. `WorkstreamId` is an ID of a `Workstream`:"
                    " `<prefix>_<ULID>`, with the prefixes in the records table.",
                ),
                Entry(
                    "field-none.svg",
                    _field_row(("FK", "owner", "PersonId | None"), 2),
                    "May be null",
                    "`| None`: the value may be null. With no `=`, the field must still be given.",
                ),
                Entry(
                    "field-default.svg",
                    _field_row(("", "priority", "int = 2"), 2),
                    "Has a default",
                    "`= value`: the field may be left out, and then has that value.",
                ),
                Entry(
                    "field-list.svg",
                    _field_row(("FK", "participants", "list[PersonId] = []"), 2),
                    "List",
                    "A list of the type in brackets.",
                ),
                Entry(
                    "field-enum.svg",
                    _field_row(("", "state", "SessionState"), 2),
                    "Enum",
                    "A type with no box of its own. Its values are in the schema.",
                ),
                Entry(
                    "field-union.svg",
                    _field_row(("", "actor", "PersonActor | ComponentActor | ..."), 2),
                    "One of several",
                    "The field holds one of the models joined by `|`. Each has a solid line.",
                ),
                Entry(
                    "field-literal.svg",
                    _field_row(("", "kind", "Literal['person'] = 'person'"), 2),
                    "Fixed value",
                    "Always this value: here, the `kind` that tells the members of a union apart.",
                ),
                Entry(
                    "field-datetime.svg",
                    _field_row(("", "started_at", "datetime | None = None"), 2),
                    "Time",
                    "A `datetime` is always in UTC.",
                ),
            ),
        ),
    )


def files() -> dict[str, str]:
    """Every drawing, by file name."""
    return {e.file: e.svg for g in groups() for e in g.entries}
