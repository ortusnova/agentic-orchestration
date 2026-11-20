"""Generate ``schemas/domain.schema.json`` from the models, or check that it is current.

    domain-models write   # regenerate the file
    domain-models check   # exit 1 if the file differs from what the models generate
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

from pydantic.json_schema import models_json_schema

from . import RECORDS, SCHEMA_VERSION

SCHEMA_PATH = Path(__file__).resolve().parents[2] / "schemas" / "domain.schema.json"


def generate() -> str:
    """The schema file's exact text."""
    _, top = models_json_schema([(m, "validation") for m in RECORDS], ref_template="#/$defs/{model}")
    doc = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": f"urn:agentic-orchestration:domain:v{SCHEMA_VERSION}",
        "title": "Domain models",
        "description": "Generated from the domain_models package. Do not edit: change the models and run `make -C domain_models schemas`.",
        "x-schema-version": SCHEMA_VERSION,
        "x-records": [m.__name__ for m in RECORDS],
        "$defs": dict(sorted(top["$defs"].items())),
    }
    return json.dumps(doc, indent=2, ensure_ascii=False) + "\n"


def main(argv: list[str] | None = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    if args == ["write"]:
        SCHEMA_PATH.parent.mkdir(parents=True, exist_ok=True)
        SCHEMA_PATH.write_text(generate())
        print(f"wrote {SCHEMA_PATH}")
        return 0
    if args == ["check"]:
        current = SCHEMA_PATH.read_text() if SCHEMA_PATH.exists() else ""
        if current != generate():
            print(f"{SCHEMA_PATH} is out of date: run `make -C domain_models schemas`", file=sys.stderr)
            return 1
        print("schemas: ok")
        return 0
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
