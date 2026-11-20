"""The platform's domain data models.

These Pydantic models are the source of truth. ``schemas/domain.schema.json``
is generated from them, and every other language's types are generated from
that schema.
"""

from .base import SCHEMA_VERSION, DomainModel, Record
from .convo import Convo, ConvoEvent, ConvoLink
from .execution import Worker, WorkstreamVm
from .ids import IdKind, created_at_ms, kind_of, new_id
from .people import Person
from .project import Membership, Project
from .session import Session
from .ticket import Ticket
from .workstream import Epic, Schedule, Workstream, WorkstreamEvent

RECORDS: tuple[type[Record], ...] = (
    Person,
    Project,
    Membership,
    Epic,
    Workstream,
    WorkstreamEvent,
    Ticket,
    Schedule,
    Session,
    WorkstreamVm,
    Worker,
    Convo,
    ConvoLink,
    ConvoEvent,
)
"""Every model stored on its own, with its own ID."""

__all__ = [
    "RECORDS",
    "SCHEMA_VERSION",
    "Convo",
    "ConvoEvent",
    "ConvoLink",
    "DomainModel",
    "Epic",
    "IdKind",
    "Membership",
    "Person",
    "Project",
    "Record",
    "Schedule",
    "Session",
    "Ticket",
    "Worker",
    "Workstream",
    "WorkstreamEvent",
    "WorkstreamVm",
    "created_at_ms",
    "kind_of",
    "new_id",
]
