"""The slot lanes in time (agent-platform/run-supervision): what each slot
ran before now, and what it is expected to run after.

Past: the runs Sortie's run history holds that ended in the lanes' window,
each on the worker it ran on, packed into the worker's slot rows so that no
two overlap in a row. Sortie's run history does not record the worker of a
run under the remote kind, so the workbench records it as it sees each run
running (``RunWorkers``); a run it never saw on a worker is counted, not
drawn.

Future: the jobs that wait for a slot, in the order they began to wait,
each placed in the slot that frees first on a worker that can take it,
for the median length of its rule's succeeded runs. It is a forecast: it
flows again at every refresh, as slots open. A job no worker can take is
not placed; it waits, with why.
"""

from __future__ import annotations

import json
import logging
import time
from pathlib import Path
from typing import Any, Iterable

log = logging.getLogger(__name__)

# How far back the lanes draw past runs, in seconds: the axis starts 30
# minutes back; a little more keeps a run that crosses the edge.
PAST_SECONDS = 3600.0
# How far ahead the forecast places jobs, in seconds; a job that would start
# later is counted, not placed.
FUTURE_SECONDS = 6 * 3600.0
# As Sortie estimates a running job's end: the median of a rule's newest
# succeeded runs.
MEDIAN_RUNS = 20
# How long a remembered run's worker is kept.
REMEMBER_SECONDS = 2 * 86400.0


def _ts(v: Any) -> float | None:
    from .fleet import ts

    return ts(v)


def _median(xs: list[float]) -> float | None:
    if not xs:
        return None
    xs = sorted(xs)
    n = len(xs)
    return xs[n // 2] if n % 2 else (xs[n // 2 - 1] + xs[n // 2]) / 2


def rule_medians(runs: Iterable[dict[str, Any]]) -> dict[str, tuple[float, int]]:
    """Per rule, the median length of its newest succeeded runs in seconds,
    and how many runs it is the median of."""
    by_rule: dict[str, list[tuple[float, float]]] = {}
    for r in runs:
        if r.get("status") != "succeeded":
            continue
        start, end = _ts(r.get("started_at")), _ts(r.get("completed_at"))
        if start is None or end is None or end <= start:
            continue
        by_rule.setdefault(r.get("rule_name") or "", []).append((end, end - start))
    out = {}
    for rule, xs in by_rule.items():
        newest = [d for _, d in sorted(xs, reverse=True)[:MEDIAN_RUNS]]
        out[rule] = (_median(newest) or 0.0, len(newest))
    return out


class RunWorkers:
    """Which worker each run ran on, by Sortie and dispatch id, as the
    workbench saw it running; kept in a file so a restart keeps it."""

    def __init__(self, path: Path | None = None, clock=time.time):
        self.path = path
        self.clock = clock
        self.seen: dict[str, dict[str, Any]] = {}
        if path is not None and path.exists():
            try:
                data = json.loads(path.read_text())
                self.seen = {k: v for k, v in data.items() if isinstance(v, dict) and v.get("worker")}
            except (OSError, ValueError):
                log.warning("could not read %s; the past runs start empty", path)

    @staticmethod
    def _key(sortie: str, dispatch: str) -> str:
        return f"{sortie}/{dispatch}"

    def observe(self, sortie: str, state: dict[str, Any] | None) -> None:
        if not state:
            return
        now = self.clock()
        leases = {x.get("job"): x for x in ((state.get("workers") or {}).get("leases") or []) if isinstance(x, dict)}
        host = (state.get("host") or {}).get("name") if isinstance(state.get("host"), dict) else None
        changed = False
        for r in state.get("running") or []:
            if not isinstance(r, dict) or not r.get("dispatch_id"):
                continue
            # A job Sortie counts as running that no worker took has no
            # worker yet: it ran nowhere.
            w = r.get("worker") or (leases.get(r.get("issue_identifier")) or {}).get("worker") or host
            if not w:
                continue
            key = self._key(sortie, r["dispatch_id"])
            if (self.seen.get(key) or {}).get("worker") != w:
                changed = True
            self.seen[key] = {"worker": w, "at": now}
        horizon = now - REMEMBER_SECONDS
        stale = [k for k, v in self.seen.items() if (v.get("at") or 0) < horizon]
        for k in stale:
            del self.seen[k]
        if (changed or stale) and self.path is not None:
            try:
                self.path.parent.mkdir(parents=True, exist_ok=True)
                tmp = self.path.with_suffix(".tmp")
                tmp.write_text(json.dumps(self.seen))
                tmp.replace(self.path)
            except OSError:
                log.warning("could not keep the runs' workers in %s", self.path)

    def worker_of(self, sortie: str, run: dict[str, Any]) -> str | None:
        if run.get("ssh_host"):
            return run["ssh_host"]
        return (self.seen.get(self._key(sortie, run.get("dispatch_id") or "")) or {}).get("worker")


class RunHistory:
    """Each Sortie's recent runs and its rules' medians, read at most every
    ``every`` seconds (the medians every ``medians_every``)."""

    def __init__(self, workers: RunWorkers | None = None, every: float = 30.0, medians_every: float = 300.0,
                 clock=time.time):
        self.workers = workers or RunWorkers()
        self.every = every
        self.medians_every = medians_every
        self.clock = clock
        self.recent: dict[str, list[dict[str, Any]]] = {}
        self.medians: dict[str, dict[str, tuple[float, int]]] = {}
        self._read: dict[str, float] = {}
        self._medians_read: dict[str, float] = {}

    async def refresh(self, name: str, sortie: Any) -> None:
        """Read the runs of one Sortie when due; a Sortie that serves no run
        history keeps what it gave last."""
        from datetime import datetime, timezone

        from .sortie import SortieError

        now = self.clock()
        if now - self._read.get(name, -1e18) >= self.every:
            self._read[name] = now
            since = datetime.fromtimestamp(now - PAST_SECONDS - 6 * 3600, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
            try:
                body = await sortie.runs(since=since, limit=200)
                self.recent[name] = [r for r in body.get("runs") or [] if isinstance(r, dict)]
            except SortieError as exc:
                log.info("%s's Sortie served no run history: %s", name, exc.message)
        if now - self._medians_read.get(name, -1e18) >= self.medians_every:
            self._medians_read[name] = now
            try:
                body = await sortie.runs(status="succeeded", limit=200)
                self.medians[name] = rule_medians(r for r in body.get("runs") or [] if isinstance(r, dict))
            except SortieError as exc:
                log.info("%s's Sortie served no succeeded runs: %s", name, exc.message)


def _interval_free(row: list[tuple[float, float]], start: float, end: float) -> bool:
    return all(end <= a or start >= b for a, b in row)


def pack_past(worker: dict[str, Any], runs: list[dict[str, Any]], now: float) -> int:
    """Place a worker's past runs in its slot rows, each in the first row
    where it overlaps nothing: the row's running job, from its start on,
    nor a run placed before it. Returns how many found no row."""
    lanes = [lane for lane in worker["lanes"] if not lane.get("extra")] or worker["lanes"]
    rows: list[list[tuple[float, float]]] = []
    for lane in lanes:
        lane.setdefault("past", [])
        j = lane.get("job")
        rows.append([(j["started_at"], float("inf"))] if j else [])
    hidden = 0
    for r in sorted(runs, key=lambda r: (r["started_at"], r["ended_at"], r["identifier"] or "")):
        for lane, row in zip(lanes, rows):
            if _interval_free(row, r["started_at"], r["ended_at"]):
                row.append((r["started_at"], r["ended_at"]))
                lane["past"].append(r)
                break
        else:
            hidden += 1
    return hidden


def past_runs(sortie: str, runs: list[dict[str, Any]], workers: list[dict[str, Any]], run_workers: RunWorkers,
              stories: Any, now: float, running: set[str]) -> int:
    """Put each Sortie's runs that ended in the window on their workers'
    lanes. Returns how many runs ended there on no worker the workbench
    knows."""
    from .fleet import PHASE_TEXT, phase_of

    mine = {w["name"]: w for w in workers if w["sortie"] == sortie}
    single = next(iter(mine)) if len(mine) == 1 and not any(w.get("remote") for w in mine.values()) else None
    by_worker: dict[str, list[dict[str, Any]]] = {}
    unknown = 0
    for r in runs:
        ident = r.get("issue_identifier")
        start, end = _ts(r.get("started_at")), _ts(r.get("completed_at"))
        if not stories.ours(ident) or start is None or end is None or end < now - PAST_SECONDS or end > now:
            continue
        if r.get("dispatch_id") and r["dispatch_id"] in running:
            continue
        name = run_workers.worker_of(sortie, r) or single
        if name not in mine:
            unknown += 1
            continue
        phase = phase_of(r)
        by_worker.setdefault(name, []).append({
            **stories.ref(ident, r.get("issue_title")), "started_at": start, "ended_at": max(end, start),
            "status": r.get("status") or None, "rule": r.get("rule_name") or None, "phase": phase,
            "phase_text": PHASE_TEXT[phase], "error": r.get("error") or None,
        })
    for name, w in mine.items():
        for lane in w["lanes"]:
            lane.setdefault("past", [])
        w["past_hidden"] = pack_past(w, by_worker.get(name, []), now)
    return unknown


def project(sortie: str, queue: list[dict[str, Any]], workers: list[dict[str, Any]], estimate,
            now: float) -> list[dict[str, Any]]:
    """Place the jobs that wait for a slot, in order, each in the slot that
    frees first on a worker that can take it, for its estimated length.
    ``queue`` items name the workers that can take them (``eligible``, None
    for any); ``estimate(item)`` gives (seconds, basis). Returns the jobs
    no slot can take: none of their workers is here, or all of them drain
    for a deploy, or the job waits for a capability no worker has."""
    mine = [w for w in workers if w["sortie"] == sortie]
    slots: list[dict[str, Any]] = []
    for wi, w in enumerate(mine):
        w["later"] = 0
        for lane in w["lanes"]:
            lane["next"] = []
            if lane.get("extra") or w.get("draining") or w.get("gone"):
                continue
            j = lane.get("job")
            slots.append({"worker": w, "wi": wi, "lane": lane, "free": max(now, j["ends_at"]) if j else now})
    waiting = []
    for item in queue:
        names = item.get("eligible")
        fit = [s for s in slots if names is None or s["worker"]["name"] in names]
        ref = {k: item.get(k) for k in ("identifier", "key", "label", "title")}
        if item.get("kind") != "slot" or not fit:
            draining = [w["name"] for w in mine if (names is None or w["name"] in names) and w.get("draining")]
            reason = (f"{' and '.join(draining)} {'drains' if len(draining) == 1 else 'drain'} for a deploy and "
                      f"{'takes' if len(draining) == 1 else 'take'} no new job") if item.get("kind") == "slot" and draining \
                else item.get("reason") or "no worker can take it"
            waiting.append({**ref, "sortie": sortie, "kind": item.get("kind") or "slot", "reason": reason,
                            "release": item.get("release"), "since": item.get("since")})
            continue
        s = min(fit, key=lambda s: (s["free"], s["wi"], s["lane"]["slot"]))
        if s["free"] - now > FUTURE_SECONDS:
            s["worker"]["later"] += 1
            continue
        seconds, basis = estimate(item)
        s["lane"]["next"].append({**ref, "starts_at": s["free"], "ends_at": s["free"] + seconds, "estimate": seconds,
                                  "basis": basis, "rule": item.get("rule"), "since": item.get("since")})
        s["free"] += seconds
    return waiting
