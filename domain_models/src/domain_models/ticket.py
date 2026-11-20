"""Tickets, and their mirrors in external trackers."""

from __future__ import annotations

import enum

from pydantic import model_validator

from .base import DomainModel, NonEmptyStr, Record, UtcDatetime
from .ids import EpicId, ProjectId, TicketId, WorkstreamId


class TrackerKind(enum.StrEnum):
    HULY = "huly"
    JIRA = "jira"
    LINEAR = "linear"
    GITHUB = "github"
    GITEA = "gitea"
    GITLAB = "gitlab"


class MirrorOrigin(enum.StrEnum):
    IMPORTED = "imported"
    """The ticket was read from the tracker: the tracker had it first."""
    EXPORTED = "exported"
    """The system created the ticket in the tracker."""


class TrackerMirror(DomainModel):
    """A ticket's copy in one external tracker. PROVISIONAL: sync and conflict rules are open (review item 3b)."""

    tracker: TrackerKind
    instance: NonEmptyStr
    """Which installation, for example ``https://huly.example`` and its workspace."""
    external_id: NonEmptyStr
    """The tracker's stable ID for the issue."""
    external_key: str | None = None
    """The key people see, for example ``ONS-77``."""
    url: str | None = None
    origin: MirrorOrigin
    synced_at: UtcDatetime | None = None
    """When the last sync in either direction finished."""


class Ticket(Record):
    """The tracker record of a workstream or an epic, kept in the system's own database.

    A ticket has no state of its own: its state is its subject's. A mirror
    maps that state to the tracker's states.
    """

    id: TicketId
    project_id: ProjectId
    workstream_id: WorkstreamId | None = None
    epic_id: EpicId | None = None
    title: NonEmptyStr
    description: str = ""
    labels: list[NonEmptyStr] = []
    mirrors: list[TrackerMirror] = []

    @model_validator(mode="after")
    def _one_subject(self) -> Ticket:
        if (self.workstream_id is None) == (self.epic_id is None):
            raise ValueError("a ticket belongs to exactly one workstream or one epic")
        seen = {(m.tracker, m.instance) for m in self.mirrors}
        if len(seen) != len(self.mirrors):
            raise ValueError("a ticket has at most one mirror per tracker instance")
        return self
