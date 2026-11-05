"""The store's plan as the validator reads it, kept current by a file watcher."""

from __future__ import annotations

import asyncio
import logging
import os
import threading
import time
from pathlib import Path
from typing import Any, Callable

from watchfiles import awatch

import planlib
from planlib import PlanLoadError

log = logging.getLogger(__name__)

Validator = Callable[[Path], planlib.ValidationResult]


class PlanState:
    """The last valid plan graph and the current validation errors.

    An invalid store keeps the last valid graph and reports the errors, so
    the graph never disappears while someone fixes a file.
    """

    def __init__(self, store: Path, validate: Validator = planlib.validate,
                 inputs: Callable[[], tuple[dict[str, str], dict[str, str]]] | None = None):
        self.store = store
        self._validate = validate
        # Approved digests and the frozen set, read at every validation.
        self._inputs = inputs or (lambda: ({}, {}))
        # Planning schemas by kind, read in place of the store's own: the
        # workbench's, while the upgrade that brings them waits to merge.
        self.replaced_schemas: dict[str, str] = {}
        self.graph: dict[str, Any] | None = None
        self.stories: dict[str, dict[str, Any]] = {}
        # The milestones and each change's tasks.md items, from the last valid read.
        self.planning: dict[str, Any] = {"milestones": [], "declared": False, "tasks": {}}
        self.errors: list[dict[str, Any]] = []
        # The last read's warnings, logged each time they change.
        self.warnings: list[str] = []
        self.version = 0
        self.validated_at = 0.0
        self._listeners: list[Callable[[], None]] = []
        # The watcher, the poller and the stale check all refresh, each from
        # its own thread. One refresh at a time, so a slow one that read the
        # files before an edit cannot finish last and undo a newer one.
        self._lock = threading.RLock()
        # The files under openspec/ as the last refresh saw them.
        self._seen: tuple | None = None

    def on_change(self, listener: Callable[[], None]) -> None:
        self._listeners.append(listener)

    def fingerprint(self) -> tuple:
        """The path, modification time and size of every file under openspec/."""
        root = self.store / "openspec"
        out = []
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames.sort()
            for name in sorted(filenames):
                path = os.path.join(dirpath, name)
                try:
                    st = os.stat(path)
                except FileNotFoundError:
                    continue
                out.append((os.path.relpath(path, root), st.st_mtime_ns, st.st_size))
        return tuple(out)

    def stale(self) -> bool:
        """Whether a file changed since the last refresh read the store.

        The watcher can miss events: a file written into a directory just
        created lands before the watch on that directory exists."""
        return self.fingerprint() != self._seen

    def refresh(self) -> None:
        """Validate the store now and keep the result."""
        with self._lock:
            self._refresh()

    def _refresh(self) -> None:
        # Taken before reading: an edit made while validating shows as stale.
        self._seen = self.fingerprint()
        try:
            approvals, frozen = self._inputs()
            kw = {"schemas": planlib.Schemas.load(self.store, self.replaced_schemas)} if self.replaced_schemas else {}
            result = self._validate(self.store, approvals=approvals, frozen=frozen, **kw)
            errors = [e.to_dict() for e in result.errors]
            warnings = [str(w) for w in getattr(result, "warnings", [])]
            if warnings != self.warnings:
                for w in warnings:
                    log.warning("plan: %s", w)
            self.warnings = warnings
            graph = result.graph
            if graph is not None:
                self.stories = result.stories
                self.planning = {"milestones": getattr(result, "milestones", []),
                                 "declared": getattr(result, "milestones_declared", False),
                                 "tasks": getattr(result, "tasks", {})}
        except PlanLoadError as exc:
            errors = [{"code": "store", "message": str(exc), "file": None, "change": None, "deliverable": None, "members": []}]
            graph = None
        except Exception as exc:  # a validator crash must not stop the watcher
            log.exception("plan validation failed")
            errors = [{"code": "internal", "message": str(exc), "file": None, "change": None, "deliverable": None, "members": []}]
            graph = None
        if graph is not None:
            self.graph = graph
        self.errors = errors
        self.version += 1
        self.validated_at = time.time()
        for listener in list(self._listeners):
            listener()

    def snapshot(self) -> dict[str, Any]:
        return {"version": self.version, "valid": not self.errors, "errors": self.errors, "graph": self.graph}


async def watch(state: PlanState, debounce_ms: int, stop: asyncio.Event) -> None:
    """Re-validate whenever a file under the store's openspec/ changes."""
    root = state.store / "openspec"
    async for _changes in awatch(root, debounce=debounce_ms, step=50, stop_event=stop, recursive=True):
        await asyncio.to_thread(state.refresh)
