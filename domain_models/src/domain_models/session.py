"""Sessions: one agent conversation with one purpose, run in a workstream's VM."""

from __future__ import annotations

import enum
from typing import Annotated

from pydantic import Field, model_validator

from .base import DomainModel, NonEmptyStr, Record, UtcDatetime
from .ids import PersonId, SessionId, WorkerId, WorkstreamId, WorkstreamVmId

StageName = Annotated[str, Field(pattern=r"^[a-z0-9][a-z0-9_-]{0,62}$")]


class AgentRef(DomainModel):
    harness: NonEmptyStr
    """The ACP agent, for example ``opencode`` or ``claude-code``."""

    model: NonEmptyStr
    """The model it runs, for example ``anthropic/claude-opus-5-5``."""


class SessionState(enum.StrEnum):
    QUEUED = "queued"
    STARTING = "starting"
    RUNNING = "running"

    HELD = "held"
    """A person holds the floor; the session's clock is stopped."""

    ENDED = "ended"


class SessionOutcome(enum.StrEnum):
    COMPLETED = "completed"
    """The stage's work is done."""

    NO_CHANGE = "no_change"
    """The agent claims nothing needed changing, with evidence."""

    BLOCKED = "blocked"
    """The agent cannot proceed without a person."""

    FAILED = "failed"
    TIMED_OUT = "timed_out"
    STALLED = "stalled"
    CANCELLED = "cancelled"


class Usage(DomainModel):
    input_tokens: Annotated[int, Field(ge=0)] = 0
    output_tokens: Annotated[int, Field(ge=0)] = 0
    cache_read_tokens: Annotated[int, Field(ge=0)] = 0
    cache_write_tokens: Annotated[int, Field(ge=0)] = 0
    cost_usd: Annotated[float, Field(ge=0)] | None = None


class Session(Record):
    id: SessionId
    workstream_id: WorkstreamId

    stage: StageName
    """The session's purpose, for example ``draft``, ``review``, ``address-review``."""

    attempt: Annotated[int, Field(ge=1)]
    """1 for the first session of this stage in the workstream; a retry adds one."""

    state: SessionState
    agent: AgentRef
    vm_id: WorkstreamVmId | None = None
    worker_id: WorkerId | None = None
    held_by: PersonId | None = None
    started_at: UtcDatetime | None = None
    ended_at: UtcDatetime | None = None
    outcome: SessionOutcome | None = None

    statement: str | None = None
    """The agent's own account of why it stopped, after secret masking."""

    turns: Annotated[int, Field(ge=0)] = 0
    usage: Usage = Usage()

    @model_validator(mode="after")
    def _ended_rules(self) -> Session:
        ended = self.state is SessionState.ENDED
        if ended != (self.outcome is not None) or ended != (self.ended_at is not None):
            raise ValueError("outcome and ended_at are set exactly when the session ended")
        if (self.state is SessionState.HELD) != (self.held_by is not None):
            raise ValueError("held_by is set exactly when the session is held")
        if self.state in (SessionState.RUNNING, SessionState.HELD) and self.started_at is None:
            raise ValueError("a running or held session has started_at")
        return self
