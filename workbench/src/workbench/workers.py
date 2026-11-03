"""The workers Sortie runs for a project's stories: what the graph shows of
them, and the relay that lets a person watch and steer one.

The relay asks Sortie for a worker's broker on every command and stream
(re)connect, and never hands the broker's address or token to the browser.
"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import AsyncIterator, Callable
from typing import Any

import httpx

from .sortie import ProjectSortie, SortieError

log = logging.getLogger(__name__)

# How often the stream asks Sortie whether the story runs again.
FOLLOW_POLL_SECONDS = 2.0


def worker_fields(state: dict[str, Any]) -> dict[str, dict[str, Any]]:
    """The fields each worker adds to its story's runtime, by tracker identifier."""
    out: dict[str, dict[str, Any]] = {}
    for r in state.get("running") or []:
        out[r["issue_identifier"]] = {
            "stage": r.get("rule_name") or None,
            "session_id": r.get("session_id") or None,
            "held": bool(r.get("held")),
            "holder": r.get("holder") or None,
            "watchable": bool((r.get("broker") or {}).get("http_url")),
        }
    for r in state.get("retrying") or []:
        out.setdefault(r["issue_identifier"], {"stage": None, "session_id": None, "held": False, "holder": None, "watchable": False})
    return out


class WorkerError(Exception):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{code}: {message}")
        self.status = status
        self.code = code
        self.message = message


def _sse(event: str, data: Any, id: str | None = None) -> bytes:
    head = f"id: {id}\n" if id is not None else ""
    return f"{head}event: {event}\ndata: {json.dumps(data, separators=(',', ':'))}\n\n".encode()


class WorkerRelay:
    """Relays the brokers of one project's workers."""

    def __init__(self, sortie: ProjectSortie, transport: httpx.AsyncBaseTransport | None = None,
                 follow_poll: float = FOLLOW_POLL_SECONDS, sortie_for: Callable[[str], ProjectSortie] | None = None):
        self.sortie = sortie
        # The Sortie that runs a story: this project's own, or another one.
        self.sortie_for = sortie_for or (lambda _identifier: sortie)
        self.follow_poll = follow_poll
        self._http = httpx.AsyncClient(transport=transport, timeout=httpx.Timeout(30.0, read=None))

    async def aclose(self) -> None:
        await self._http.aclose()

    async def _broker(self, identifier: str) -> tuple[str, str]:
        try:
            b = await self.sortie_for(identifier).broker(identifier)
        except SortieError as exc:
            if exc.status == 404:
                raise WorkerError(409, "no_worker", f"{identifier} has no running worker that can be watched") from exc
            raise WorkerError(502, "sortie_error", exc.message) from exc
        return b["http_url"].rstrip("/"), b["viewer_token"]

    async def command(self, identifier: str, cmd: dict[str, Any]) -> tuple[int, Any, str | None]:
        """Send one command to the worker's broker. Returns the broker's
        status and body, and the run it went to."""
        run = await self._run(identifier)
        url, token = await self._broker(identifier)
        try:
            r = await self._http.post(f"{url}/commands", json=cmd, headers={"Authorization": f"Bearer {token}"})
        except httpx.HTTPError as exc:
            raise WorkerError(502, "broker_unreachable", f"the worker's broker did not answer: {exc}") from exc
        try:
            body = r.json()
        except ValueError:
            body = {"error": r.text}
        return r.status_code, body, (run or {}).get("session_id")

    async def _run(self, identifier: str) -> dict[str, Any] | None:
        """The story's current run: ``{"session_id", "stage", "watchable"}``,
        ``{"retrying": True}`` while Sortie waits to retry, or None."""
        try:
            info = await self.sortie_for(identifier).issue(identifier)
        except SortieError as exc:
            if exc.status == 404:
                return None
            raise WorkerError(502, "sortie_error", exc.message) from exc
        running = info.get("running")
        if running:
            return {"session_id": running.get("session_id"), "stage": running.get("rule_name") or None,
                    "watchable": bool((running.get("broker") or {}).get("http_url"))}
        if info.get("retry"):
            return {"retrying": True, "attempt": info["retry"].get("attempt")}
        return None

    async def last_transcript(self, identifier: str) -> dict[str, Any] | None:
        """The events Sortie kept of the story's newest run: all of them
        when it serves them, else the newest it lists with the issue."""
        sortie = self.sortie_for(identifier)
        try:
            info = await sortie.issue(identifier)
        except SortieError:
            return None
        runs = ((info.get("history") or {}).get("runs") or []) if isinstance(info.get("history"), dict) else []
        dispatch = next((r.get("dispatch_id") for r in runs if isinstance(r, dict) and r.get("dispatch_id")), None)
        events = info.get("recent_events") or []
        if dispatch:
            try:
                events = (await sortie.run_events(dispatch)).get("events") or events
            except SortieError as exc:
                log.info("sortie keeps no events of %s's run %s: %s", identifier, dispatch, exc.message)
        if not events:
            return None
        return {"identifier": identifier, "dispatch_id": dispatch, "sortie": sortie.name,
                "events": [{"seq": e.get("seq"), "at": e.get("at"), "event": e.get("event"), "text": e.get("text") or e.get("message") or "",
                            "tool_line": e.get("tool_line")} for e in events if isinstance(e, dict)]}

    async def events(self, identifier: str, last_event_id: str | None,
                     connected: Callable[[], Any] | None = None) -> AsyncIterator[bytes]:
        """The worker's events, following the story from run to run (design D3)."""
        seen: str | None = None
        first = True
        while True:
            if connected is not None and not await connected():
                return
            run = await self._run(identifier)
            if run is None:
                if seen is None:
                    # Nothing runs now: show the agent's last session instead.
                    last = await self.last_transcript(identifier)
                    if last is not None:
                        yield _sse("transcript", last)
                yield _sse("ended", {"identifier": identifier})
                return
            if run.get("retrying") or not run.get("watchable"):
                await asyncio.sleep(self.follow_poll)
                continue
            if run["session_id"] == seen:
                # The broker closed but Sortie still lists the run: give it time to end.
                await asyncio.sleep(self.follow_poll)
                continue
            try:
                url, token = await self._broker(identifier)
            except WorkerError:
                await asyncio.sleep(self.follow_poll)
                continue
            follow = seen is not None
            seen = run["session_id"]
            # id 0 resets the browser's Last-Event-ID, so a reconnect asks the
            # new broker for its whole replay.
            yield _sse("run", {"identifier": identifier, "session_id": seen, "stage": run["stage"], "follow": follow},
                       id=None if first else "0")
            headers = {"Authorization": f"Bearer {token}", "Accept": "text/event-stream"}
            if first and last_event_id:
                headers["Last-Event-ID"] = last_event_id
            first = False
            try:
                async with self._http.stream("GET", f"{url}/events", headers=headers) as r:
                    if r.status_code != 200:
                        log.warning("broker of %s answered %s", identifier, r.status_code)
                    else:
                        async for chunk in r.aiter_bytes():
                            yield chunk
            except httpx.HTTPError as exc:
                log.info("broker of %s closed: %s", identifier, exc)
