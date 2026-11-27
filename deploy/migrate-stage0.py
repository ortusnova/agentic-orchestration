#!/usr/bin/env python3
"""The file and database steps of migrate-stage0.sh (spec store
deploy-environments, design D7). Standard library only.

  counts DB                      every table's row count, as JSON
  rewrite-db DB OLD NEW [BACKUP] rewrite OLD to NEW in every text value of
                                 DB, in one transaction, after a backup
                                 copy at BACKUP; fails when a count differs
                                 after
  rewrite-links HOME OLD NEW     rewrite OLD to NEW in the git links under
                                 HOME (.git files, worktrees/*/gitdir)
  rewrite-files HOME OLD NEW ASIDE [EXCLUDE...]
                                 rewrite OLD to NEW in every text file and
                                 symlink under HOME, and move every other
                                 file naming OLD to ASIDE, at the same
                                 relative path; prints what it did as JSON
  databases HOME [EXCLUDE...]    every SQLite file under HOME, relative
  diff-counts BEFORE AFTER [TABLE...]
                                 the counts (JSON, by database) that differ,
                                 or with TABLEs those that went down; fails
                                 on any
  scan HOME OLD [EXCLUDE...]     list every file under HOME naming OLD, a
                                 database's dump included; fails on any
  record FILE set KEY JSON | append KEY JSON | get KEY | lines KEY
                                 the migration's record; lines prints each
                                 item of a list on a line, a list's items
                                 joined by a tab

A path is named only when it is whole: OLD followed by a character that
cannot continue a file name. EXCLUDE is a path relative to HOME, skipped
with what it holds. The files git tracks in a work tree under HOME are its
commit's, not state, and are left alone; SQLite files are read through
SQLite, never as bytes.
"""

import json
import mmap
import os
import re
import sqlite3
import subprocess
import sys

SQLITE_MAGIC = b"SQLite format 3\x00"


def pattern(old):
    return re.compile(re.escape(old) + r"(?![A-Za-z0-9_.-])")


def bpattern(old):
    return re.compile(re.escape(old.encode()) + rb"(?![A-Za-z0-9_.-])")


def is_sqlite(path):
    try:
        with open(path, "rb") as f:
            return f.read(16) == SQLITE_MAGIC
    except OSError:
        return False


def sqlite_sidecar(path):
    return path.endswith(("-wal", "-shm", "-journal")) and is_sqlite(path.rsplit("-", 1)[0])


def tables(db):
    return [
        r[0]
        for r in db.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' "
            "AND sql NOT LIKE 'CREATE VIRTUAL%' ORDER BY name"
        )
    ]


def counts(path):
    db = sqlite3.connect(path)
    try:
        return {t: db.execute(f'SELECT count(*) FROM "{t}"').fetchone()[0] for t in tables(db)}
    finally:
        db.close()


def rewrite_db(path, old, new, backup=None):
    db = sqlite3.connect(path, isolation_level=None)
    try:
        if backup:
            dest = sqlite3.connect(backup)
            db.backup(dest)
            dest.close()
        before = {t: db.execute(f'SELECT count(*) FROM "{t}"').fetchone()[0] for t in tables(db)}
        rx = pattern(old)
        changed = 0
        db.execute("BEGIN IMMEDIATE")
        try:
            for t in tables(db):
                for col in [r[1] for r in db.execute(f'PRAGMA table_info("{t}")')]:
                    rows = db.execute(
                        f'SELECT DISTINCT "{col}" FROM "{t}" WHERE typeof("{col}") = \'text\' AND instr("{col}", ?) > 0',
                        (old,),
                    ).fetchall()
                    for (value,) in rows:
                        fixed = rx.sub(new, value)
                        if fixed != value:
                            cur = db.execute(f'UPDATE "{t}" SET "{col}" = ? WHERE "{col}" = ?', (fixed, value))
                            changed += cur.rowcount
            after = {t: db.execute(f'SELECT count(*) FROM "{t}"').fetchone()[0] for t in tables(db)}
            if after != before:
                raise RuntimeError(f"the counts changed in the rewrite: {before} -> {after}")
            db.execute("COMMIT")
        except BaseException:
            db.execute("ROLLBACK")
            raise
        db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        return changed
    finally:
        db.close()


def dump_names(path, old):
    rx = pattern(old)
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        return any(rx.search(line) for line in db.iterdump())
    finally:
        db.close()


def tracked(home, excluded):
    """The files git tracks in each work tree under HOME, relative to HOME."""
    out = set()
    for root, dirs, files in os.walk(home):
        rel = os.path.relpath(root, home)
        dirs[:] = [d for d in dirs if os.path.normpath(os.path.join(rel, d)) not in excluded]
        if ".git" in dirs or ".git" in files:
            dirs[:] = [d for d in dirs if d != ".git"]
            res = subprocess.run(["git", "-C", root, "ls-files", "-z"], capture_output=True)
            if res.returncode == 0:
                for name in res.stdout.split(b"\0"):
                    if name:
                        out.add(os.path.normpath(os.path.join(rel, os.fsdecode(name))))
    return out


def is_git_dir(path):
    return os.path.isfile(os.path.join(path, "HEAD")) and os.path.isdir(os.path.join(path, "objects")) and os.path.isdir(
        os.path.join(path, "refs")
    )


def git_links(home):
    """The files that link a work tree and its repository: each .git file,
    and each worktrees/*/gitdir and commondir in a repository."""
    for root, dirs, files in os.walk(home):
        if ".git" in files:
            yield os.path.join(root, ".git")
        if os.path.basename(os.path.dirname(root)) == "worktrees" and is_git_dir(os.path.dirname(os.path.dirname(root))):
            for f in ("gitdir", "commondir"):
                if f in files:
                    yield os.path.join(root, f)


def walk(home, excluded):
    """Every file and symlink under HOME, relative, but EXCLUDE, git
    repositories (their links are git_links'), and tracked files."""
    excluded = {os.path.normpath(e) for e in excluded}
    skip = tracked(home, excluded)
    for root, dirs, files in os.walk(home):
        rel = os.path.relpath(root, home)
        keep = []
        for d in dirs:
            r = os.path.normpath(os.path.join(rel, d))
            if r in excluded or d == ".git" or is_git_dir(os.path.join(root, d)):
                continue
            if os.path.islink(os.path.join(root, d)):
                files.append(d)
                continue
            keep.append(d)
        dirs[:] = keep
        for f in files:
            r = os.path.normpath(os.path.join(rel, f))
            if r in excluded or r in skip or f == ".git":
                continue
            yield r


def names(path, rx):
    if os.path.getsize(path) == 0:
        return False
    with open(path, "rb") as f, mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ) as m:
        return rx.search(m) is not None


def rewrite_files(home, old, new, aside, excluded):
    brx = bpattern(old)
    done = {"rewritten": [], "relinked": [], "aside": []}
    for rel in list(walk(home, excluded)):
        path = os.path.join(home, rel)
        if os.path.islink(path):
            target = os.readlink(path)
            fixed = pattern(old).sub(new, target)
            if fixed != target:
                os.unlink(path)
                os.symlink(fixed, path)
                done["relinked"].append(rel)
            continue
        if not os.path.isfile(path) or is_sqlite(path) or sqlite_sidecar(path) or not names(path, brx):
            continue
        with open(path, "rb") as f:
            data = f.read()
        if b"\0" in data:
            dest = os.path.join(aside, rel)
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            os.replace(path, dest)
            done["aside"].append(rel)
            continue
        st = os.stat(path)
        tmp = path + ".migrate-tmp"
        with open(tmp, "wb") as f:
            f.write(brx.sub(new.encode(), data))
        os.chmod(tmp, st.st_mode & 0o7777)
        os.replace(tmp, path)
        done["rewritten"].append(rel)
    return done


def rewrite_links(home, old, new):
    rx = pattern(old)
    done = []
    for path in list(git_links(home)):
        with open(path) as f:
            text = f.read()
        fixed = rx.sub(new, text)
        if fixed != text:
            with open(path, "w") as f:
                f.write(fixed)
            done.append(os.path.relpath(path, home))
    return done


def scan(home, old, excluded):
    brx = bpattern(old)
    hits = []
    for rel in walk(home, excluded):
        path = os.path.join(home, rel)
        if os.path.islink(path):
            if pattern(old).search(os.readlink(path)):
                hits.append(rel)
            continue
        if not os.path.isfile(path) or sqlite_sidecar(path):
            continue
        if is_sqlite(path):
            if dump_names(path, old):
                hits.append(rel + " (its dump)")
        elif names(path, brx):
            hits.append(rel)
    excluded = {os.path.normpath(e) for e in excluded}
    for path in git_links(home):
        rel = os.path.relpath(path, home)
        if not any(rel == e or rel.startswith(e + os.sep) for e in excluded):
            with open(path) as f:
                if pattern(old).search(f.read()):
                    hits.append(rel)
    return hits


def record(path, op, key, value=None):
    data = {}
    if os.path.exists(path):
        with open(path) as f:
            data = json.load(f)
    if op == "get":
        v = data.get(key)
        print(v if isinstance(v, str) else json.dumps(v))
        return
    if op == "lines":
        for item in data.get(key) or []:
            print("\t".join(item) if isinstance(item, list) else item)
        return
    v = json.loads(value)
    if op == "set":
        data[key] = v
    elif op == "append":
        data.setdefault(key, []).append(v)
    else:
        raise SystemExit(f"record: unknown operation {op}")
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2, sort_keys=True)
        f.write("\n")
    os.replace(tmp, path)


def main(argv):
    if not argv:
        raise SystemExit(__doc__)
    cmd, args = argv[0], argv[1:]
    if cmd == "counts":
        print(json.dumps(counts(args[0]), sort_keys=True))
    elif cmd == "rewrite-db":
        print(rewrite_db(args[0], args[1], args[2], args[3] if len(args) > 3 else None))
    elif cmd == "rewrite-links":
        print(json.dumps(rewrite_links(*args[:3])))
    elif cmd == "rewrite-files":
        print(json.dumps(rewrite_files(args[0], args[1], args[2], args[3], args[4:])))
    elif cmd == "databases":
        for rel in walk(args[0], args[1:]):
            path = os.path.join(args[0], rel)
            if not os.path.islink(path) and os.path.isfile(path) and is_sqlite(path):
                print(rel)
    elif cmd == "diff-counts":
        before, after, only = json.loads(args[0]), json.loads(args[1]), args[2:]
        out = []
        for db in sorted(set(before) | set(after)):
            b, a = before.get(db, {}), after.get(db, {})
            for t in sorted(set(b) | set(a)):
                if only:
                    if t in only and a.get(t, 0) < b.get(t, 0):
                        out.append(f"{db} {t}: {b.get(t, 0)} -> {a.get(t, 0)}")
                elif b.get(t) != a.get(t):
                    out.append(f"{db} {t}: {b.get(t)} -> {a.get(t)}")
        print("; ".join(out))
        return 1 if out else 0
    elif cmd == "scan":
        hits = scan(args[0], args[1], args[2:])
        for h in hits:
            print(h)
        return 1 if hits else 0
    elif cmd == "record":
        record(args[0], args[1], args[2], args[3] if len(args) > 3 else None)
    else:
        raise SystemExit(f"migrate-stage0.py: unknown command {cmd}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (OSError, sqlite3.Error, RuntimeError) as e:
        print(f"migrate-stage0.py: {e}", file=sys.stderr)
        sys.exit(1)
