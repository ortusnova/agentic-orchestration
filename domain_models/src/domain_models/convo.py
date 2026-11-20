"""Convos: streams of related events outside the system, and the workstreams they bear on."""

from __future__ import annotations

import enum
from typing import Annotated

from pydantic import Field, model_validator

from .base import DomainModel, NonEmptyStr, Record, UtcDatetime
from .ids import ConvoEventId, ConvoId, ConvoLinkId, ProjectId, RepositoryId, WorkstreamId
from .people import Actor


class ConvoKind(enum.StrEnum):
    PULL_REQUEST = "pull_request"

    TRACKER_ISSUE = "tracker_issue"
    """The comments on a ticket's mirror."""

    CHAT_THREAD = "chat_thread"
    EMAIL_THREAD = "email_thread"

    WORKBENCH = "workbench"
    """A conversation held in the workbench."""


class PullRequestState(enum.StrEnum):
    OPEN = "open"
    MERGED = "merged"
    CLOSED = "closed"


class PullRequestInfo(DomainModel):
    repository_id: RepositoryId
    number: Annotated[int, Field(ge=1)]
    head_branch: NonEmptyStr
    head_sha: NonEmptyStr
    state: PullRequestState
    merged_sha: str | None = None


class Convo(Record):
    id: ConvoId
    project_id: ProjectId
    kind: ConvoKind

    system: NonEmptyStr
    """Where it lives, for example ``github``, ``slack``, ``huly``, ``workbench``."""

    external_id: NonEmptyStr
    """Its stable ID in that system."""

    url: str | None = None
    title: str | None = None

    pull_request: PullRequestInfo | None = None
    """Set exactly for a pull request."""

    @model_validator(mode="after")
    def _pull_request_rules(self) -> Convo:
        if (self.kind is ConvoKind.PULL_REQUEST) != (self.pull_request is not None):
            raise ValueError("pull_request is set exactly for a pull request convo")
        return self


class ConvoLinkReason(enum.StrEnum):
    SOURCE = "source"
    """The convo the workstream came from, and replies to."""

    OUTPUT = "output"
    """The workstream created it, for example its pull request."""

    SUBSCRIBED = "subscribed"
    """Attached later, by a person, the router, or a session."""


class ConvoLink(Record):
    """Attaches one convo to one workstream. Follow-up events in the convo go to every attached workstream."""

    id: ConvoLinkId
    convo_id: ConvoId
    workstream_id: WorkstreamId
    reason: ConvoLinkReason
    linked_by: Actor
    unlinked_at: UtcDatetime | None = None


class ConvoEventKind(enum.StrEnum):
    """PROVISIONAL: grows with the feedback loops (review item 10)."""

    MESSAGE = "message"
    REVIEW_COMMENT = "review_comment"
    REVIEW_VERDICT = "review_verdict"
    CI_RESULT = "ci_result"
    STATE_CHANGE = "state_change"
    LABEL_CHANGE = "label_change"


class ConvoEvent(Record):
    """One event in a convo. Append-only."""

    id: ConvoEventId
    convo_id: ConvoId

    at: UtcDatetime
    """When it happened in its system."""

    kind: ConvoEventKind
    author: Actor

    external_id: NonEmptyStr
    """Its ID in the convo's system, so a second delivery is recognized."""

    body: str = ""
    """Untrusted: whatever its author wrote."""

    url: str | None = None
    in_reply_to: ConvoEventId | None = None
