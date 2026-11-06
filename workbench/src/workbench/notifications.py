"""What every Sortie's notifications send the workbench
(agent-platform/run-supervision: a person is alerted, wherever the question
is held).

Each Sortie's workflow lists the workbench as a destination of its
notifications, with Sortie's ``webhook`` backend (Sortie
internal/notify/webhook): it posts one JSON object per notification to a URL
that names its host and carries that host's token, since the backend sends
no header of its own. The workbench keeps each notification, links it to the
story it names and to that story's open question, and shows it under "Needs
a person", unread until a person has seen it. A notification of a question
still open is unread again every four hours after it was sent, as the
operator's channel is pushed again (workbench.alerts), until the question is
answered or no longer open.
"""

from __future__ import annotations

import hmac
import json
import logging
import math
import re
from datetime import datetime
from pathlib import Path
from typing import Any

from .alerts import REPEAT_SECONDS
from .config import SORTIE_NAME

log = logging.getLogger(__name__)

# The largest body the endpoint takes. Sortie cuts what it renders long
# before this.
MAX_BODY = 256 * 1024
# A notification sent this long before the park Sortie reports is still of
# that park: the two clocks are the same host's, but not the same moment.
PARK_SLACK = 60.0
# The fields of Sortie's webhook body (Sortie internal/notify/webhook,
# wirePayload) that are text, when present.
_TEXT = ("notification_id", "timestamp", "source", "issue_id", "identifier", "dispatch_id", "session_id", "agent",
         "severity", "title", "body", "category", "event_type", "agent_text")
_ESCALATION = ("kind", "rule", "url")
_STAGE = ("source_rule", "target_rule", "chain_id", "reason")
# A token in the path of the endpoint, as the access log would print it.
_TOKEN_PATH = re.compile(r"^(/api/notifications/[^/?#]+/)[^/?#]+")


class PayloadError(ValueError):
    """The body is not a notification of Sortie's webhook backend."""


def parse(raw: bytes) -> dict[str, Any]:
    """The notification in a body Sortie's webhook backend posted."""
    try:
        body = json.loads(raw)
    except ValueError:
        raise PayloadError("the body is not JSON") from None
    if not isinstance(body, dict):
        raise PayloadError("the body is not a JSON object")
    for key in _TEXT:
        if body.get(key) is not None and not isinstance(body[key], str):
            raise PayloadError(f"{key} is not a string")
    if not (body.get("notification_id") or "").strip():
        raise PayloadError("notification_id is missing")
    attempt = body.get("attempt")
    if attempt is not None and (isinstance(attempt, bool) or not isinstance(attempt, int)):
        raise PayloadError("attempt is not a number")
    for key, fields in (("escalation", _ESCALATION), ("stage", _STAGE)):
        part = body.get(key)
        if part is None:
            continue
        if not isinstance(part, dict) or any(part.get(f) is not None and not isinstance(part[f], str) for f in fields):
            raise PayloadError(f"{key} is not an object of strings")
    return body


class HostTokens:
    """The token of each host whose Sortie may post to the workbench: the
    file ``<host>.token`` in one directory, which only its owner may read.
    Read at each request, so a host the kit adds needs no restart."""

    def __init__(self, directory: Path | None):
        self.directory = directory

    def check(self, host: str, token: str | None) -> bool:
        if self.directory is None or not token or not SORTIE_NAME.fullmatch(host):
            return False
        path = self.directory / f"{host}.token"
        try:
            if path.stat().st_mode & 0o077:
                log.warning("%s can be read by others than its owner, so it is refused: chmod 600 it", path)
                return False
            want = path.read_text().strip()
        except OSError:
            return False
        return bool(want) and hmac.compare_digest(want.encode(), token.encode())


class RedactTokens(logging.Filter):
    """Keeps a host's token out of the access log, which prints each path."""

    def filter(self, record: logging.LogRecord) -> bool:
        if isinstance(record.args, tuple) and any(isinstance(a, str) and a.startswith("/api/notifications/") for a in record.args):
            record.args = tuple(_TOKEN_PATH.sub(r"\1<token>", a) if isinstance(a, str) else a for a in record.args)
        return True


def redact_access_log() -> None:
    access = logging.getLogger("uvicorn.access")
    if not any(isinstance(f, RedactTokens) for f in access.filters):
        access.addFilter(RedactTokens())


def _time(value: Any) -> float | None:
    if isinstance(value, str) and value.strip():
        try:
            return datetime.fromisoformat(value.strip().replace("Z", "+00:00")).timestamp()
        except ValueError:
            return None
    return None


def _story(n: dict[str, Any]) -> dict[str, Any]:
    key = n["plan_key"]
    return {"key": key, "label": key.split("/", 1)[-1], "change": n.get("change") or key.split("/", 1)[0],
            "title": n.get("title") or key, "url": (n.get("tracker") or {}).get("url")}


def project_notifications(rows: list[dict[str, Any]], nodes: list[dict[str, Any]], questions: dict[str, dict[str, Any]],
                          now: float, shown_only: bool = True) -> list[dict[str, Any]]:
    """The notifications that name a story of the project, each linked to
    that story and to its open question, unread ones first, then newest
    first. ``shown_only`` keeps those "Needs a person" shows: every unread
    one, and every one of a question still open."""
    by_ident = {n["tracker"]["identifier"]: n for n in nodes if n.get("kind") == "story" and n.get("tracker")}
    out: list[dict[str, Any]] = []
    for row in rows:
        n = by_ident.get(row["identifier"])
        if n is None:
            continue
        try:
            p = json.loads(row["payload"])
        except ValueError:
            continue
        sent = _time(p.get("timestamp")) or row["received_at"]
        q = questions.get(row["identifier"])
        # Of this park, not of an earlier one of the same story.
        of_question = q is not None and (q.get("parked_at") is None or sent >= q["parked_at"] - PARK_SLACK)
        esc = p.get("escalation") if isinstance(p.get("escalation"), dict) else None
        out.append({
            "id": row["id"], "host": row["host"], "notification_id": row["notification_id"],
            "event_type": p.get("event_type") or "agent.message", "severity": p.get("severity") or "",
            "category": p.get("category") or "", "title": p.get("title") or "", "body": p.get("body") or "",
            "agent_text": p.get("agent_text") or "", "identifier": row["identifier"], "session_id": p.get("session_id") or "",
            "attempt": p.get("attempt"), "agent": p.get("agent") or "", "source": p.get("source") or "",
            "escalation": {k: esc.get(k) or "" for k in _ESCALATION} if esc else None,
            "sent_at": sent, "received_at": row["received_at"], "seen_at": row["seen_at"], "seen_by": row["seen_by"] or None,
            "story": _story(n),
            "question": {"identifier": row["identifier"], "sortie": q.get("sortie"), "parked_at": q.get("parked_at")} if of_question else None,
            "unread": row["seen_at"] is None, "again": False,
        })
    # The newest notification of each open question is the one pushed again.
    newest: dict[str, dict[str, Any]] = {}
    for item in out:
        if item["question"] is not None:
            newest[item["identifier"]] = item
    for item in newest.values():
        repeats = math.floor((now - item["sent_at"]) / REPEAT_SECONDS)
        if repeats < 1:
            continue
        item["again"] = True
        due = item["sent_at"] + repeats * REPEAT_SECONDS
        if item["seen_at"] is not None and item["seen_at"] < due:
            item["unread"] = True
    if shown_only:
        out = [i for i in out if i["unread"] or i["question"] is not None]
    out.sort(key=lambda i: (not i["unread"], -i["sent_at"], -i["id"]))
    return out
