"""Check that every documented attribute in a class body stands apart.

A documented attribute is an assignment followed directly by a string, its
docstring. It must have a blank line before it (unless it opens the class
body) and a blank line after its docstring (unless it closes the body):

    class Shape(enum.StrEnum):
        PLANNED = "planned"
        \"\"\"Implements a story of an approved plan.\"\"\"

        ADHOC = "adhoc"
        \"\"\"A one-off ask.\"\"\"

Ruff has no rule for this, and keeps a blank line it finds, so the two agree.

    python tools/attribute_docstrings.py PATH...         # report, exit 1 on a problem
    python tools/attribute_docstrings.py --fix PATH...   # insert the missing blank lines
"""

from __future__ import annotations

import ast
import sys
from pathlib import Path


def _is_docstring(node: ast.stmt) -> bool:
    return isinstance(node, ast.Expr) and isinstance(node.value, ast.Constant) and isinstance(node.value.value, str)


def missing_blank_lines(source: str) -> list[int]:
    """The 1-based line numbers after which a blank line is missing, in ascending order."""
    lines = source.splitlines()

    def blank(lineno: int) -> bool:
        return not lines[lineno - 1].strip()

    after: set[int] = set()
    for cls in ast.walk(ast.parse(source)):
        if not isinstance(cls, ast.ClassDef):
            continue
        body = cls.body
        for i, stmt in enumerate(body[:-1]):
            if not isinstance(stmt, (ast.Assign, ast.AnnAssign)) or not _is_docstring(body[i + 1]):
                continue
            doc = body[i + 1]
            if i > 0:
                top = stmt.lineno
                while top - 1 > body[i - 1].end_lineno and lines[top - 2].lstrip().startswith("#"):
                    top -= 1
                if not blank(top - 1):
                    after.add(top - 1)
            if i + 2 < len(body) and not blank(doc.end_lineno + 1):
                after.add(doc.end_lineno)
    return sorted(after)


def fix(source: str) -> str:
    lines = source.splitlines(keepends=True)
    for lineno in reversed(missing_blank_lines(source)):
        lines.insert(lineno, "\n")
    return "".join(lines)


def _files(paths: list[str]) -> list[Path]:
    out: list[Path] = []
    for p in map(Path, paths):
        out.extend(sorted(p.rglob("*.py")) if p.is_dir() else [p])
    return out


def main(argv: list[str]) -> int:
    fixing = "--fix" in argv
    paths = [a for a in argv if a != "--fix"]
    if not paths:
        print(__doc__, file=sys.stderr)
        return 2
    problems = 0
    for path in _files(paths):
        source = path.read_text()
        missing = missing_blank_lines(source)
        if not missing:
            continue
        if fixing:
            path.write_text(fix(source))
            print(f"{path}: inserted {len(missing)} blank line(s)")
            continue
        problems += len(missing)
        for lineno in missing:
            print(f"{path}:{lineno + 1}: a documented attribute needs a blank line here")
    return 1 if problems else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
