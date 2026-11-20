"""Typed identifiers: a kind prefix, an underscore, and a ULID.

``ws_01JD3Z8Q6V4C7M2N9P0R5S8T1W`` is a workstream. The prefix says what an
ID names, so a reader, a log line, or a model can tell an ID's kind without
looking it up. The ULID sorts by creation time to the millisecond.

The system's own IDs are always of this form. A key an external system
assigns, such as a tracker's ``ONS-77``, is never an ID: it is an external
reference on the object it names.
"""

from __future__ import annotations

import enum
import os
import time
from typing import Annotated

from pydantic import StringConstraints

_CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
_ULID_PATTERN = "[0-9A-HJKMNP-TV-Z]{26}"


class IdKind(enum.StrEnum):
    """Every kind of ID, and its prefix."""

    PERSON = "usr"
    PROJECT = "prj"
    REPOSITORY = "repo"
    MEMBERSHIP = "mbr"
    EPIC = "ep"
    WORKSTREAM = "ws"
    WORKSTREAM_EVENT = "wse"
    TICKET = "tk"
    SCHEDULE = "sch"
    SESSION = "ses"
    WORKSTREAM_VM = "vm"
    WORKER = "wkr"
    CONVO = "cv"
    CONVO_EVENT = "cve"
    CONVO_LINK = "cvl"


def _ulid(now_ms: int | None = None) -> str:
    ms = int(time.time() * 1000) if now_ms is None else now_ms
    if not 0 <= ms < 1 << 48:
        raise ValueError("timestamp out of range for a ULID")
    value = (ms << 80) | int.from_bytes(os.urandom(10), "big")
    out = []
    for _ in range(26):
        out.append(_CROCKFORD[value & 31])
        value >>= 5
    return "".join(reversed(out))


def new_id(kind: IdKind, *, now_ms: int | None = None) -> str:
    """A new ID of ``kind``. ``now_ms`` fixes the time part, for tests."""
    return f"{kind.value}_{_ulid(now_ms)}"


def kind_of(value: str) -> IdKind:
    """The kind of ID ``value`` is. Raises ``ValueError`` for anything that is not an ID."""
    prefix, sep, rest = value.partition("_")
    if not sep or len(rest) != 26 or any(c not in _CROCKFORD for c in rest):
        raise ValueError(f"not an ID: {value!r}")
    try:
        return IdKind(prefix)
    except ValueError:
        raise ValueError(f"unknown ID prefix {prefix!r} in {value!r}") from None


def created_at_ms(value: str) -> int:
    """The creation time, in Unix milliseconds, encoded in ID ``value``."""
    kind_of(value)
    ms = 0
    for c in value.partition("_")[2][:10]:
        ms = ms * 32 + _CROCKFORD.index(c)
    return ms


def _id_type(kind: IdKind):
    return Annotated[str, StringConstraints(pattern=f"^{kind.value}_{_ULID_PATTERN}$")]


PersonId = _id_type(IdKind.PERSON)
ProjectId = _id_type(IdKind.PROJECT)
RepositoryId = _id_type(IdKind.REPOSITORY)
MembershipId = _id_type(IdKind.MEMBERSHIP)
EpicId = _id_type(IdKind.EPIC)
WorkstreamId = _id_type(IdKind.WORKSTREAM)
WorkstreamEventId = _id_type(IdKind.WORKSTREAM_EVENT)
TicketId = _id_type(IdKind.TICKET)
ScheduleId = _id_type(IdKind.SCHEDULE)
SessionId = _id_type(IdKind.SESSION)
WorkstreamVmId = _id_type(IdKind.WORKSTREAM_VM)
WorkerId = _id_type(IdKind.WORKER)
ConvoId = _id_type(IdKind.CONVO)
ConvoEventId = _id_type(IdKind.CONVO_EVENT)
ConvoLinkId = _id_type(IdKind.CONVO_LINK)
