"""Generate ``schemas/`` from the models, or check that it is current.

domain-models write        # regenerate the schema and its diagrams
domain-models check        # exit 1 if either differs from what the models generate
domain-models html PATH    # write a standalone page that renders the diagrams
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

from pydantic.json_schema import models_json_schema

from . import RECORDS, SCHEMA_VERSION, erd, legend

SCHEMAS_DIR = Path(__file__).resolve().parents[2] / "schemas"
SCHEMA_PATH = SCHEMAS_DIR / "domain.schema.json"
DIAGRAMS_PATH = SCHEMAS_DIR / "domain.erd.md"
LEGEND_DIR = SCHEMAS_DIR / "legend"
MERMAID_URL = "https://cdn.jsdelivr.net/npm/mermaid@12.3.0/dist/mermaid.esm.min.mjs"


def generate() -> str:
    """The schema file's exact text."""
    _, top = models_json_schema([(m, "validation") for m in RECORDS], ref_template="#/$defs/{model}")
    doc = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": f"urn:agentic-orchestration:domain:v{SCHEMA_VERSION}",
        "title": "Domain models",
        "description": (
            "Generated from the domain_models package. Do not edit: change the models"
            " and run `make -C domain_models schemas`."
        ),
        "x-schema-version": SCHEMA_VERSION,
        "x-records": [m.__name__ for m in RECORDS],
        "$defs": dict(sorted(top["$defs"].items())),
    }
    return json.dumps(doc, indent=2, ensure_ascii=False) + "\n"


def outputs() -> dict[Path, str]:
    """Every generated file, and its exact text."""
    schema = generate()
    out = {SCHEMA_PATH: schema, DIAGRAMS_PATH: erd.markdown(json.loads(schema))}
    out.update({LEGEND_DIR / name: svg for name, svg in legend.files().items()})
    return out


def main(argv: list[str] | None = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    if args == ["write"]:
        for path, text in outputs().items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
            print(f"wrote {path}")
        return 0
    if args == ["check"]:
        stale = [p for p, text in outputs().items() if (p.read_text() if p.exists() else "") != text]
        for path in stale:
            print(f"{path} is out of date: run `make -C domain_models schemas`", file=sys.stderr)
        if stale:
            return 1
        print("schemas: ok")
        return 0
    if len(args) == 2 and args[0] == "html":
        out = Path(args[1])
        out.write_text(erd.html(json.loads(generate()), MERMAID_URL))
        print(f"wrote {out}")
        return 0
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
