"""People, and who did something."""

from __future__ import annotations

import enum
from typing import Annotated, Literal

from pydantic import Field

from .base import DomainModel, NonEmptyStr, Record
from .ids import PersonId, SessionId


class ExternalIdentity(DomainModel):
    """One account a person holds in another system, so events from that system can be attributed to them."""

    system: NonEmptyStr
    """The system, for example ``oidc:https://idp.example``, ``github``, ``slack``, ``huly``."""

    subject: NonEmptyStr
    """The account's stable identifier in that system, not its display name."""

    handle: str | None = None
    """The name people see, for example a GitHub login. Informational; it may change."""


class Person(Record):
    id: PersonId
    display_name: NonEmptyStr
    email: str | None = None
    identities: list[ExternalIdentity] = []


class Component(enum.StrEnum):
    """The parts of the system that act on their own."""

    WORKBENCH = "workbench"
    ROUTER = "router"
    WORKER = "worker"
    WATCHDOG = "watchdog"
    TRACKER_SYNC = "tracker_sync"
    SCHEDULER = "scheduler"


class PersonActor(DomainModel):
    kind: Literal["person"] = "person"
    person_id: PersonId


class ComponentActor(DomainModel):
    kind: Literal["component"] = "component"
    component: Component


class SessionActor(DomainModel):
    kind: Literal["session"] = "session"
    session_id: SessionId


class ExternalActor(DomainModel):
    """Someone in another system the system cannot attribute to a person it knows."""

    kind: Literal["external"] = "external"
    system: NonEmptyStr
    subject: NonEmptyStr
    handle: str | None = None


Actor = Annotated[PersonActor | ComponentActor | SessionActor | ExternalActor, Field(discriminator="kind")]
"""Who did something: a person, a component, an agent session, or an unknown external account."""
