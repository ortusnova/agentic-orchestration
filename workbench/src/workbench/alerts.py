"""Every open question, pushed to the operator's channel
(agent-platform/run-supervision: a person is alerted, wherever the question
is held).

The workbench reads the questions of every Sortie it knows. Each one it
pushes when it first sees it open, with the question, the story, the Sortie
that holds it and a link to where it is answered, and again every four hours
while it stays open. A question that is answered, or no longer open, is not
pushed again. What was pushed when is kept in a file of the workbench's
home, so a restart pushes nothing twice.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import time
from pathlib import Path
from typing import Any, Callable
from urllib.parse import quote

import httpx

log = logging.getLogger(__name__)

# An open question is pushed again this long after it was last pushed.
REPEAT_SECONDS = 4 * 3600.0
# A push the channel refused is tried again after this long.
RETRY_SECONDS = 60.0
# The longest text each kind takes; a longer one is cut.
_LIMITS = {"discord": 2000, "telegram": 4096, "slack": 40000, "webhook": 40000}
TELEGRAM_API = "https://api.telegram.org"


class ChannelError(Exception):
    """The channel refused a push, or could not be reached."""


class Channel:
    """The operator's channel: one of the kinds Sortie's notifications have.
    ``secret`` is the webhook URL, or the Telegram bot's token. Neither it nor
    the address is ever logged."""

    def __init__(self, kind: str, secret: str, chat_id: str = "", transport: httpx.AsyncBaseTransport | None = None,
                 timeout: float = 10.0):
        if kind not in _LIMITS:
            raise ValueError(f"unknown channel kind {kind!r}")
        self.kind = kind
        self._secret = secret
        self._chat_id = chat_id
        self._client = httpx.AsyncClient(transport=transport, timeout=timeout)

    async def aclose(self) -> None:
        await self._client.aclose()

    async def send(self, text: str, payload: dict[str, Any]) -> None:
        text = cut(text, _LIMITS[self.kind])
        if self.kind == "slack":
            url, body = self._secret, {"text": text}
        elif self.kind == "discord":
            # A question never pings anyone, whatever its text says.
            url, body = self._secret, {"content": text, "allowed_mentions": {"parse": []}}
        elif self.kind == "telegram":
            url, body = f"{TELEGRAM_API}/bot{self._secret}/sendMessage", {"chat_id": self._chat_id, "text": text}
        else:
            url, body = self._secret, {**payload, "text": text}
        try:
            r = await self._client.post(url, json=body)
        except httpx.HTTPError as exc:
            raise ChannelError(f"{self.kind}: cannot reach the channel ({type(exc).__name__})") from None
        if r.status_code >= 300:
            raise ChannelError(f"{self.kind}: the channel answered HTTP {r.status_code}")


def cut(text: str, limit: int) -> str:
    return text if len(text) <= limit else text[: limit - 1] + "…"


def question_key(q: dict[str, Any]) -> str:
    """One question: a new park of the same story is a new question."""
    return f"{q.get('sortie') or ''}|{q['identifier']}|{q.get('parked_at') or ''}"


def link(public_url: str, slug: str) -> str:
    """The project's home, where the question is listed and answered."""
    return f"{public_url}/#/p/{quote(slug, safe='')}"


def message(q: dict[str, Any], url: str, again: bool) -> str:
    holder = f" on {q['sortie']}'s Sortie" if q.get("sortie") else ""
    head = (f"Still open after {REPEAT_SECONDS / 3600:g} hours: " if again else "") + f"{q['identifier']} asks a person{holder}"
    story = f"Story {q.get('key') or q['identifier']}: {q.get('title') or ''}".rstrip(": ")
    statement = (q.get("statement") or "").strip() or "The agent stopped for a person without saying why."
    return f"{head}\n{story}\n\n{statement}\n\nAnswer it here: {url}"


def payload(q: dict[str, Any], url: str, again: bool, slug: str) -> dict[str, Any]:
    """What a webhook channel receives besides the text."""
    return {"event_type": "question.open_again" if again else "question.opened", "project": slug,
            "identifier": q["identifier"], "story": q.get("key"), "title": q.get("title"),
            "statement": q.get("statement") or "", "sortie": q.get("sortie"), "url": url}


class Alerts:
    """Pushes each project's open questions; ``observe`` takes the whole
    list of one project's open questions at every refresh."""

    def __init__(self, channel: Channel, public_url: str, path: Path | None = None, clock: Callable[[], float] = time.time):
        self.channel = channel
        self.public_url = public_url.rstrip("/")
        self.path = path
        self.clock = clock
        # project slug -> question key -> {"identifier", "sent", "count", "failed"}.
        self._sent: dict[str, dict[str, dict[str, Any]]] = self._load()
        # The poll loop and a person's action may both refresh at once.
        self._lock = asyncio.Lock()

    def _load(self) -> dict[str, dict[str, dict[str, Any]]]:
        if self.path is None or not self.path.exists():
            return {}
        try:
            data = json.loads(self.path.read_text())
        except (OSError, ValueError) as exc:
            log.warning("could not read %s, so open questions may be pushed again: %s", self.path, exc)
            return {}
        return {slug: dict(qs) for slug, qs in data.items() if isinstance(qs, dict)} if isinstance(data, dict) else {}

    def _save(self) -> None:
        if self.path is None:
            return
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self.path.with_suffix(".tmp")
            tmp.write_text(json.dumps(self._sent, indent=1, sort_keys=True))
            os.replace(tmp, self.path)
        except OSError as exc:
            log.warning("could not write %s: %s", self.path, exc)

    def sent(self, slug: str) -> dict[str, dict[str, Any]]:
        """What was pushed of the project's open questions, by question key."""
        return dict(self._sent.get(slug) or {})

    async def observe(self, slug: str, questions: list[dict[str, Any]]) -> None:
        async with self._lock:
            await self._observe(slug, questions)

    async def _observe(self, slug: str, questions: list[dict[str, Any]]) -> None:
        now = self.clock()
        before = self._sent.get(slug) or {}
        # A question no longer open is forgotten: it is never pushed again.
        after: dict[str, dict[str, Any]] = {}
        url = link(self.public_url, slug)
        for q in questions:
            key = question_key(q)
            rec = dict(before.get(key) or {"identifier": q["identifier"], "sent": None, "count": 0, "failed": None})
            after[key] = rec
            due = rec["sent"] is None or now - rec["sent"] >= REPEAT_SECONDS
            if not due or (rec.get("failed") is not None and now - rec["failed"] < RETRY_SECONDS):
                continue
            again = rec["count"] > 0
            try:
                await self.channel.send(message(q, url, again), payload(q, url, again, slug))
            except ChannelError as exc:
                log.warning("project %s: could not push %s's question: %s", slug, q["identifier"], exc)
                rec["failed"] = now
                continue
            rec.update(sent=now, count=rec["count"] + 1, failed=None)
            log.info("project %s: pushed %s's question%s", slug, q["identifier"], " again" if again else "")
        if after != before:
            if after:
                self._sent[slug] = after
            else:
                self._sent.pop(slug, None)
            self._save()
