"""Workstreams, the epics that group them, and the schedules that create them."""

from __future__ import annotations

import enum
from typing import Annotated

from pydantic import Field, model_validator

from .base import DomainModel, NonEmptyStr, Record, UtcDatetime
from .ids import (
    ConvoId,
    EpicId,
    PersonId,
    ProjectId,
    RepositoryId,
    ScheduleId,
    SessionId,
    TicketId,
    WorkstreamEventId,
    WorkstreamId,
    WorkstreamVmId,
)
from .people import Actor


class WorkstreamShape(enum.StrEnum):
    PLANNED = "planned"
    """Implements a story of an approved plan."""

    ADHOC = "adhoc"
    """A one-off ask: a fix, an investigation."""

    SCHEDULED = "scheduled"
    """Created by a schedule."""


class SourceChannel(enum.StrEnum):
    WORKBENCH = "workbench"
    TRACKER = "tracker"
    FORGE = "forge"
    CHAT = "chat"
    EMAIL = "email"
    SCHEDULE = "schedule"
    API = "api"


class Source(DomainModel):
    """Where a workstream came from, and where its replies go."""

    channel: SourceChannel

    requester: PersonId | None
    """Who asked. None only for a scheduled workstream."""

    schedule_id: ScheduleId | None = None
    """The schedule that created it, for a scheduled workstream."""

    reply_to: ConvoId | None = None
    """The convo replies go to. None when the requester reads replies in the workbench."""

    origin_url: str | None = None
    """A link to the message, issue, or comment that started it."""

    @model_validator(mode="after")
    def _scheduled_or_requested(self) -> Source:
        if (self.channel is SourceChannel.SCHEDULE) != (self.schedule_id is not None):
            raise ValueError("schedule_id is set exactly when the channel is schedule")
        if self.requester is None and self.channel is not SourceChannel.SCHEDULE:
            raise ValueError("only a scheduled workstream has no requester")
        return self


class StoryRef(DomainModel):
    """The plan node a planned workstream implements. The story itself lives in the spec store."""

    plan_key: Annotated[str, Field(pattern=r"^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$")]
    """The story's plan key, ``<change>/<deliverable>``."""

    change: NonEmptyStr
    """The OpenSpec change the story belongs to."""

    digest: NonEmptyStr
    """The story digest (planlib) of the version this workstream implements."""

    covers: list[NonEmptyStr] = []
    """The requirements the story covers."""


class ApprovalState(enum.StrEnum):
    NOT_REQUIRED = "not_required"
    PENDING = "pending"
    APPROVED = "approved"
    REJECTED = "rejected"


class Approval(DomainModel):
    """PROVISIONAL: who approves what is open (review item 8)."""

    state: ApprovalState
    by: PersonId | None = None
    at: UtcDatetime | None = None

    digest: str | None = None
    """What the approval covers: for a planned workstream, the story digest approved."""

    @model_validator(mode="after")
    def _decided_by_someone(self) -> Approval:
        decided = self.state in (ApprovalState.APPROVED, ApprovalState.REJECTED)
        if decided != (self.by is not None) or decided != (self.at is not None):
            raise ValueError("by and at are set exactly when the approval is decided")
        return self


class WorkstreamState(enum.StrEnum):
    """PROVISIONAL: the lifecycle is open (review item 4)."""

    SUBMITTED = "submitted"
    AWAITING_APPROVAL = "awaiting_approval"

    QUEUED = "queued"
    """Approved; waits for its blockers, a worker, or a session to start."""

    ACTIVE = "active"
    """A session runs, or the next one is due."""

    AWAITING_PERSON = "awaiting_person"
    """Stopped until a person acts: a question, a park, a review."""

    DONE = "done"
    CANCELLED = "cancelled"


class Workstream(Record):
    id: WorkstreamId
    project_id: ProjectId

    epic_id: EpicId | None = None
    """The epic of a planned workstream. None for the others."""

    ticket_id: TicketId
    shape: WorkstreamShape
    source: Source

    story: StoryRef | None = None
    """Set exactly for a planned workstream."""

    title: NonEmptyStr

    goal: NonEmptyStr
    """What done means, in words. For a planned workstream, from its story."""

    repositories: Annotated[list[RepositoryId], Field(min_length=1)]
    """The project repositories its sessions change."""

    state: WorkstreamState
    approval: Approval

    priority: Annotated[int, Field(ge=0, le=4)] = 2
    """0 is the most urgent."""

    due_at: UtcDatetime | None = None

    blocked_by: list[WorkstreamId] = []
    """Workstreams that must be done before this one starts."""

    owner: PersonId | None
    """Who answers for it. None only for a scheduled workstream without an owner."""

    participants: list[PersonId] = []
    """Everyone who acted on it, the owner and requester included."""

    vm_id: WorkstreamVmId | None = None
    """Its VM, from the first session until the VM is removed."""

    current_session_id: SessionId | None = None
    closed_at: UtcDatetime | None = None

    @model_validator(mode="after")
    def _shape_rules(self) -> Workstream:
        planned = self.shape is WorkstreamShape.PLANNED
        if planned != (self.story is not None):
            raise ValueError("story is set exactly for a planned workstream")
        if planned != (self.epic_id is not None):
            raise ValueError("epic_id is set exactly for a planned workstream")
        if (self.shape is WorkstreamShape.SCHEDULED) != (self.source.channel is SourceChannel.SCHEDULE):
            raise ValueError("a scheduled workstream comes from a schedule, and only it does")
        if self.id in self.blocked_by:
            raise ValueError("a workstream cannot block itself")
        closed = self.state in (WorkstreamState.DONE, WorkstreamState.CANCELLED)
        if closed != (self.closed_at is not None):
            raise ValueError("closed_at is set exactly when the workstream is done or cancelled")
        return self


class WorkstreamEventKind(enum.StrEnum):
    """PROVISIONAL: grows with the lifecycle (review item 4)."""

    SUBMITTED = "submitted"
    STATE_CHANGED = "state_changed"
    APPROVED = "approved"
    REJECTED = "rejected"
    SESSION_STARTED = "session_started"
    SESSION_ENDED = "session_ended"
    STEERED = "steered"
    ESCALATED = "escalated"
    WATCHDOG_ACTION = "watchdog_action"
    CONVO_LINKED = "convo_linked"
    COMMENTED = "commented"

    DECISION = "decision"
    """A decision about the work, recorded so later sessions and people see it."""


class WorkstreamEvent(Record):
    """One entry in a workstream's history. Append-only: never changed or deleted."""

    id: WorkstreamEventId
    workstream_id: WorkstreamId
    at: UtcDatetime
    actor: Actor
    kind: WorkstreamEventKind

    summary: NonEmptyStr
    """One line a person can read."""

    session_id: SessionId | None = None
    convo_id: ConvoId | None = None


class EpicState(enum.StrEnum):
    OPEN = "open"
    DONE = "done"
    CANCELLED = "cancelled"


class Epic(Record):
    """Ties together the workstreams of one plan DAG. Never executed itself."""

    id: EpicId
    project_id: ProjectId

    change: NonEmptyStr
    """The OpenSpec change whose breakdown the epic's stories come from."""

    title: NonEmptyStr
    state: EpicState

    ticket_id: TicketId | None = None
    """Its ticket, when epics are mirrored to a tracker."""


class Schedule(Record):
    """Creates a scheduled workstream each time it fires. PROVISIONAL: cron ownership is open (review item 13)."""

    id: ScheduleId
    project_id: ProjectId
    title: NonEmptyStr

    cron: NonEmptyStr
    """A five-field cron expression, evaluated in UTC."""

    goal: NonEmptyStr
    repositories: Annotated[list[RepositoryId], Field(min_length=1)]
    owner: PersonId
    enabled: bool = True
