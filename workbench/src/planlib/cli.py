"""planlib command line: `planlib validate` and `planlib graph`."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .errors import PlanLoadError
from .validate import validate

EXIT_OK, EXIT_INVALID, EXIT_UNUSABLE = 0, 1, 2


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="planlib", description="Validate an OpenSpec store's plan.")
    sub = parser.add_subparsers(dest="command", required=True)
    for name, help_text in (
        ("validate", "check plan.yaml and every breakdown.yaml; list every problem"),
        ("graph", "print the validated plan graph as JSON; print nothing but errors when invalid"),
    ):
        p = sub.add_parser(name, help=help_text)
        p.add_argument("--store", default=".", help="spec store root (default: current directory)")
        p.add_argument("--json", action="store_true", help="print errors as JSON")
        if name == "graph":
            p.add_argument("-o", "--output", help="write the graph to this file instead of stdout")
    args = parser.parse_args(argv)

    try:
        result = validate(Path(args.store))
    except PlanLoadError as exc:
        print(f"planlib: {exc}", file=sys.stderr)
        return EXIT_UNUSABLE

    if result.errors:
        if args.json:
            json.dump({"ok": False, "errors": [e.to_dict() for e in result.errors]}, sys.stdout, indent=2)
            print()
        else:
            for e in result.errors:
                print(e)
            print(f"planlib: {len(result.errors)} problem(s) found", file=sys.stderr)
        return EXIT_INVALID

    if args.command == "validate":
        nodes = result.graph["nodes"]
        epics = sum(1 for n in nodes if n["kind"] == "epic")
        summary = f"plan is valid: {epics} epic(s), {len(nodes) - epics} story(ies)"
        if result.milestones_declared:
            summary += f", {len(result.milestones)} milestone(s)"
        if args.json:
            json.dump({"ok": True, "errors": [], "summary": summary}, sys.stdout)
            print()
        else:
            print(summary)
        return EXIT_OK

    text = json.dumps(result.graph, indent=2) + "\n"
    if args.output:
        Path(args.output).write_text(text)
    else:
        sys.stdout.write(text)
    return EXIT_OK


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
