#!/usr/bin/env python3
"""Carry a retired Sortie's merge records into p1's Sortie.

Sortie decides a planned blocker from its own records, the rows of its
database's plan_satisfaction table (spec store sortie/blocker-satisfaction):
a story that merged under taichi's Sortie satisfies its dependents on p1
only once p1's Sortie holds its row. worker-host.sh carry runs this on both
hosts; the worker profile's retire_sortie (worker-config.sh) runs it before
it stops a worker host's Sortie.

  merges.py dump DB             the rows of DB, as JSON (none when DB or its table is missing)
  merges.py load DB             add the rows on stdin to DB, keeping any DB holds already;
                                print the keys of the rows on stdin, which DB now holds
  merges.py missing DB CARRIED  print each row of DB, "issue_id kind", whose key the
                                JSON list in the file CARRIED (from load) does not hold

A row's key is (issue_id, kind), the table's primary key. Only Python's
standard library: this runs over SSH on a worker host.
"""

import json
import os
import sqlite3
import sys

TABLE = "plan_satisfaction"


def connect(path, write=False):
    mode = "rw" if write else "ro"
    return sqlite3.connect(f"file:{path}?mode={mode}", uri=True, timeout=30)


def has_table(db):
    return db.execute("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (TABLE,)).fetchone() is not None


def rows(path):
    if not os.path.exists(path):
        return []
    db = connect(path)
    try:
        if not has_table(db):
            return []
        db.row_factory = sqlite3.Row
        return [dict(r) for r in db.execute(f"SELECT * FROM {TABLE} ORDER BY issue_id, kind")]
    finally:
        db.close()


def key(row):
    return [row["issue_id"], row["kind"]]


def load(path, incoming):
    db = connect(path, write=True)
    try:
        if not has_table(db):
            sys.exit(f"merges.py: {path} has no {TABLE} table: is plan control on in this Sortie?")
        columns = [r[1] for r in db.execute(f"PRAGMA table_info({TABLE})")]
        with db:
            for row in incoming:
                cols = [c for c in columns if c in row]
                db.execute(
                    f"INSERT OR IGNORE INTO {TABLE} ({', '.join(cols)}) VALUES ({', '.join('?' for _ in cols)})",
                    [row[c] for c in cols],
                )
        held = {tuple(r) for r in db.execute(f"SELECT issue_id, kind FROM {TABLE}")}
    finally:
        db.close()
    keys = [key(r) for r in incoming]
    lost = [k for k in keys if tuple(k) not in held]
    if lost:
        sys.exit(f"merges.py: {path} does not hold {lost} after the load")
    return keys


def missing(path, carried_path):
    carried = set()
    if os.path.exists(carried_path):
        with open(carried_path) as f:
            carried = {tuple(k) for k in json.load(f)}
    return [r for r in rows(path) if tuple(key(r)) not in carried]


def main(argv):
    if len(argv) == 3 and argv[1] == "dump":
        json.dump(rows(argv[2]), sys.stdout)
        print()
    elif len(argv) == 3 and argv[1] == "load":
        json.dump(load(argv[2], json.load(sys.stdin)), sys.stdout)
        print()
    elif len(argv) == 4 and argv[1] == "missing":
        for r in missing(argv[2], argv[3]):
            print(r["issue_id"], r["kind"])
    else:
        sys.exit(__doc__.split("\n\n")[2])


if __name__ == "__main__":
    main(sys.argv)
