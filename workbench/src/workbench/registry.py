"""The project registry: which projects exist, in SQLite.

Plans themselves live in each project's spec store, a git repository the
planning agent edits. The registry records only the projects: a slug that
names the project in URLs and on disk, a display name, the tracker project
it syncs into, and where its store is.
"""

from __future__ import annotations

import re
import sqlite3
import threading
import time
from contextlib import contextmanager
from dataclasses import asdict, dataclass
from importlib import resources
from pathlib import Path
from typing import Iterator

SLUG = re.compile(r"^[a-z0-9][a-z0-9-]{0,47}$")
TRACKER_KEY = re.compile(r"^[A-Z][A-Z0-9]{0,4}$")
MAX_NAME = 80


class RegistryError(Exception):
    """A request the registry refuses; ``code`` names the reason for the API."""

    def __init__(self, status: int, code: str, message: str, field: str | None = None):
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message
        self.field = field


@dataclass(frozen=True)
class Project:
    id: int
    slug: str
    name: str
    huly_project: str
    store_path: str
    created_at: int

    def store(self, home: Path) -> Path:
        """The store's directory; a relative path is under the workbench home."""
        p = Path(self.store_path)
        return p if p.is_absolute() else home / p

    def to_dict(self) -> dict:
        return asdict(self)


def check_new(slug: str, name: str, huly_project: str) -> tuple[str, str, str]:
    """Normalize and check the fields of a new project."""
    slug, name, huly_project = slug.strip(), " ".join(name.split()), huly_project.strip()
    if not name:
        raise RegistryError(400, "invalid_name", "a project needs a name", "name")
    if len(name) > MAX_NAME:
        raise RegistryError(400, "invalid_name", f"the name is longer than {MAX_NAME} characters", "name")
    if not SLUG.match(slug) or slug.endswith("-"):
        raise RegistryError(400, "invalid_slug",
                            "the slug is lower-case letters, digits and dashes, starts with a letter or digit, "
                            "and is at most 48 characters", "slug")
    if not TRACKER_KEY.match(huly_project):
        raise RegistryError(400, "invalid_huly_project",
                            "the Huly project key is a capital letter, then up to four capital letters or digits",
                            "huly_project")
    return slug, name, huly_project


def suggest_slug(name: str) -> str:
    s = re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-")
    return s[:48].rstrip("-")


def _migrations() -> list[tuple[int, str]]:
    out = []
    for entry in resources.files("workbench").joinpath("sql").iterdir():
        m = re.match(r"^(\d+)_.*\.sql$", entry.name)
        if m:
            out.append((int(m.group(1)), entry.read_text()))
    return sorted(out)


class Registry:
    """Thread-safe access to the registry database."""

    def __init__(self, path: Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        # Re-entrant: the first use opens and migrates the database under it.
        self._lock = threading.RLock()
        self._conn: sqlite3.Connection | None = None

    def __enter__(self) -> "Registry":
        return self

    def __exit__(self, *_exc) -> None:
        self.close()

    @property
    def _db(self) -> sqlite3.Connection:
        """The connection, opened and migrated on first use, so a registry
        that is built but never used, such as an app that never starts,
        holds no database open."""
        with self._lock:
            if self._conn is None:
                conn = self._connect()
                conn.execute("PRAGMA journal_mode=WAL")
                self._conn = conn
                self._migrate()
            return self._conn

    def _connect(self) -> sqlite3.Connection:
        db = sqlite3.connect(self.path, timeout=60, check_same_thread=False, isolation_level=None)
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA foreign_keys=ON")
        return db

    def _migrate(self) -> None:
        with self._lock:
            current = self._db.execute("PRAGMA user_version").fetchone()[0]
            for version, sql in _migrations():
                if version <= current:
                    continue
                self._db.execute("BEGIN IMMEDIATE")
                try:
                    for stmt in _statements(sql):
                        self._db.execute(stmt)
                    self._db.execute(f"PRAGMA user_version={version}")
                    self._db.execute("COMMIT")
                except BaseException:
                    self._db.execute("ROLLBACK")
                    raise

    def close(self) -> None:
        with self._lock:
            if self._conn is not None:
                self._conn.close()
                self._conn = None

    def list(self) -> list[Project]:
        with self._lock:
            rows = self._db.execute("SELECT * FROM projects ORDER BY lower(name), slug").fetchall()
        return [Project(**dict(r)) for r in rows]

    def get(self, slug: str) -> Project | None:
        with self._lock:
            row = self._db.execute("SELECT * FROM projects WHERE slug = ?", (slug,)).fetchone()
        return Project(**dict(row)) if row else None

    def by_store(self, store_path: str) -> Project | None:
        with self._lock:
            row = self._db.execute("SELECT * FROM projects WHERE store_path = ?", (store_path,)).fetchone()
        return Project(**dict(row)) if row else None

    def rename(self, slug: str, name: str) -> Project:
        name = " ".join(name.split())
        if not name or len(name) > MAX_NAME:
            raise RegistryError(400, "invalid_name", f"a name is 1 to {MAX_NAME} characters", "name")
        with self._lock:
            cur = self._db.execute("UPDATE projects SET name = ? WHERE slug = ?", (name, slug))
        if cur.rowcount == 0:
            raise RegistryError(404, "project_not_found", f"no project {slug}")
        return self.get(slug)  # type: ignore[return-value]

    # Approvals and re-reviews, per project.

    def approvals(self, project_id: int) -> dict[str, dict]:
        with self._lock:
            rows = self._db.execute("SELECT * FROM approvals WHERE project_id = ?", (project_id,)).fetchall()
        return {r["plan_key"]: dict(r) for r in rows}

    def approve(self, project_id: int, digests: dict[str, str], approver_id: str, approver_name: str) -> None:
        """Approve each story at its digest, and clear its re-review, in one transaction."""
        now = int(time.time())
        with self._lock:
            self._db.execute("BEGIN IMMEDIATE")
            try:
                for key, digest in digests.items():
                    self._db.execute(
                        "INSERT INTO approvals (project_id, plan_key, digest, approver_id, approver_name, approved_at) "
                        "VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (project_id, plan_key) DO UPDATE SET "
                        "digest = excluded.digest, approver_id = excluded.approver_id, "
                        "approver_name = excluded.approver_name, approved_at = excluded.approved_at",
                        (project_id, key, digest, approver_id, approver_name, now))
                    self._db.execute("DELETE FROM rereviews WHERE project_id = ? AND plan_key = ?", (project_id, key))
                self._db.execute("COMMIT")
            except BaseException:
                self._db.execute("ROLLBACK")
                raise

    def rereviews(self, project_id: int) -> dict[str, dict]:
        with self._lock:
            rows = self._db.execute("SELECT * FROM rereviews WHERE project_id = ?", (project_id,)).fetchall()
        return {r["plan_key"]: dict(r) for r in rows}

    def withdraw(self, project_id: int, keys: list[str], reason: str, ref: str) -> None:
        """Withdraw the approvals of keys and mark them for re-review, in one transaction."""
        now = int(time.time())
        with self._lock:
            self._db.execute("BEGIN IMMEDIATE")
            try:
                for key in keys:
                    self._db.execute("DELETE FROM approvals WHERE project_id = ? AND plan_key = ?", (project_id, key))
                    self._db.execute(
                        "INSERT INTO rereviews (project_id, plan_key, reason, ref, created_at) VALUES (?, ?, ?, ?, ?) "
                        "ON CONFLICT (project_id, plan_key) DO UPDATE SET reason = excluded.reason, ref = excluded.ref, "
                        "created_at = excluded.created_at", (project_id, key, reason, ref, now))
                self._db.execute("COMMIT")
            except BaseException:
                self._db.execute("ROLLBACK")
                raise

    def record_command(self, project_id: int, identifier: str, session_id: str | None, op: str, text: str | None,
                       actor_id: str, actor_name: str) -> dict:
        """Record a command a person sent to a worker."""
        now = time.time()
        with self._lock:
            cur = self._db.execute(
                "INSERT INTO worker_commands (project_id, identifier, session_id, op, text, actor_id, actor_name, created_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?)", (project_id, identifier, session_id, op, text, actor_id, actor_name, now))
        return {"id": cur.lastrowid, "identifier": identifier, "session_id": session_id, "op": op, "text": text,
                "actor_id": actor_id, "actor_name": actor_name, "time": now}

    def commands(self, project_id: int, identifier: str) -> list[dict]:
        """The commands sent to a story's workers, oldest first."""
        with self._lock:
            rows = self._db.execute(
                "SELECT id, identifier, session_id, op, text, actor_id, actor_name, created_at AS time FROM worker_commands "
                "WHERE project_id = ? AND identifier = ? ORDER BY created_at, id", (project_id, identifier)).fetchall()
        return [dict(r) for r in rows]

    # Pull requests seen merged, for every project.

    def merged_pulls(self) -> dict[str, dict]:
        with self._lock:
            rows = self._db.execute("SELECT ref, url, merge_commit, merged_at FROM merged_pulls").fetchall()
        return {r["ref"]: dict(r) for r in rows}

    def record_merged(self, ref: str, url: str, merge_commit: str, merged_at: str) -> None:
        with self._lock:
            self._db.execute(
                "INSERT INTO merged_pulls (ref, url, merge_commit, merged_at, seen_at) VALUES (?, ?, ?, ?, ?) "
                "ON CONFLICT (ref) DO NOTHING", (ref, url, merge_commit, merged_at, int(time.time())))

    # What the Sorties' notifications sent, for every project.

    def record_notification(self, host: str, notification_id: str, identifier: str, event_type: str, payload: str,
                            received_at: float) -> tuple[int, bool]:
        """Keep one notification; Sortie sending the same one again keeps
        the first. The row's id, and whether it is new."""
        with self._lock:
            cur = self._db.execute(
                "INSERT INTO notifications (host, notification_id, identifier, event_type, payload, received_at) "
                "VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (host, notification_id) DO NOTHING",
                (host, notification_id, identifier, event_type, payload, received_at))
            if cur.rowcount:
                return cur.lastrowid, True
            row = self._db.execute("SELECT id FROM notifications WHERE host = ? AND notification_id = ?",
                                   (host, notification_id)).fetchone()
        return row["id"], False

    def notifications(self, identifiers: list[str] | None = None) -> list[dict]:
        """The notifications kept, oldest first; only those naming one of
        ``identifiers`` when it is given."""
        with self._lock:
            if identifiers is None:
                rows = self._db.execute("SELECT * FROM notifications ORDER BY received_at, id").fetchall()
            else:
                rows = []
                # SQLite takes a bounded number of parameters per statement.
                for i in range(0, len(identifiers), 500):
                    part = identifiers[i:i + 500]
                    rows += self._db.execute(
                        f"SELECT * FROM notifications WHERE identifier IN ({','.join('?' * len(part))})", part).fetchall()
                rows.sort(key=lambda r: (r["received_at"], r["id"]))
        return [dict(r) for r in rows]

    def see_notifications(self, ids: list[int], seen_by: str, seen_at: float) -> int:
        """Mark the notifications seen, now; how many there were."""
        with self._lock:
            n = 0
            for i in ids:
                n += self._db.execute("UPDATE notifications SET seen_at = ?, seen_by = ? WHERE id = ?",
                                      (seen_at, seen_by, i)).rowcount
        return n

    def delete(self, slug: str) -> None:
        with self._lock:
            self._db.execute("DELETE FROM projects WHERE slug = ?", (slug,))

    @contextmanager
    def reserve(self, slug: str, name: str, huly_project: str, store_path: str) -> Iterator[Project]:
        """Insert a project in an open transaction, yield it, and commit when
        the block finishes; an exception rolls the insert back. The row holds
        the slug, the tracker key and the store path from the moment it is
        inserted, so a concurrent create of the same values waits, then fails."""
        self._db  # migrate first: the insert runs on its own connection
        db = self._connect()
        try:
            db.execute("BEGIN IMMEDIATE")
            try:
                cur = db.execute(
                    "INSERT INTO projects (slug, name, huly_project, store_path, created_at) VALUES (?, ?, ?, ?, ?)",
                    (slug, name, huly_project, store_path, int(time.time())),
                )
            except sqlite3.IntegrityError as exc:
                db.execute("ROLLBACK")
                raise _conflict(str(exc), slug, huly_project) from exc
            project = Project(cur.lastrowid, slug, name, huly_project, store_path, int(time.time()))
            try:
                yield project
            except BaseException:
                db.execute("ROLLBACK")
                raise
            db.execute("COMMIT")
        finally:
            db.close()


def _conflict(message: str, slug: str, huly_project: str) -> RegistryError:
    if "projects.slug" in message:
        return RegistryError(409, "slug_taken", f"a project already uses the slug {slug}", "slug")
    if "projects.huly_project" in message:
        return RegistryError(409, "huly_project_taken", f"a project already syncs into Huly project {huly_project}", "huly_project")
    if "projects.store_path" in message:
        return RegistryError(409, "store_taken", "a project already uses that store", "slug")
    return RegistryError(400, "invalid_project", message)


def _statements(sql: str) -> list[str]:
    """Split a migration into statements; migrations hold no literal semicolons."""
    lines = [ln for ln in sql.splitlines() if not ln.strip().startswith("--")]
    return [s.strip() for s in "\n".join(lines).split(";") if s.strip()]
