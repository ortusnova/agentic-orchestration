"""Worker hosts, and the workstream VMs they run."""

from __future__ import annotations

import enum

from pydantic import model_validator

from .base import DomainModel, NonEmptyStr, Record, UtcDatetime
from .capability import CapabilityManifest, WorkerOffer
from .ids import WorkerId, WorkstreamId, WorkstreamVmId


class WorkerState(enum.StrEnum):
    ONLINE = "online"

    DRAINING = "draining"
    """Runs the VMs it has; takes no new ones."""

    OFFLINE = "offline"


class Worker(Record):
    id: WorkerId
    name: NonEmptyStr
    state: WorkerState
    offer: WorkerOffer
    last_seen_at: UtcDatetime | None = None


class ImageRef(DomainModel):
    name: NonEmptyStr

    digest: NonEmptyStr
    """The content digest the VM started from. A name alone is never enough."""


class VmState(enum.StrEnum):
    PENDING = "pending"
    """Placed on a worker, not started."""

    RUNNING = "running"

    STOPPED = "stopped"
    """Holds its disk and no CPU or RAM, while the workstream waits."""

    REMOVED = "removed"
    FAILED = "failed"


class WorkstreamVm(Record):
    """The one VM a workstream's sessions run in. Untrusted."""

    id: WorkstreamVmId
    workstream_id: WorkstreamId
    worker_id: WorkerId
    image: ImageRef
    state: VmState

    declared: CapabilityManifest
    """What the project's manifest asks for."""

    granted: CapabilityManifest | None = None
    """What the worker actually applied, as it reported. It must equal ``declared``."""

    removed_at: UtcDatetime | None = None

    @model_validator(mode="after")
    def _removed_rules(self) -> WorkstreamVm:
        if (self.state is VmState.REMOVED) != (self.removed_at is not None):
            raise ValueError("removed_at is set exactly when the VM is removed")
        return self
