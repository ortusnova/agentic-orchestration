"""What a workstream VM needs, and what a worker offers.

PROVISIONAL: where the manifest is read from, the allowed channels out of a
VM, and how mounts work are open (docs/architecture review items 5 to 7).
"""

from __future__ import annotations

from typing import Annotated

from pydantic import Field

from .base import DomainModel, NonEmptyStr

Port = Annotated[int, Field(ge=1, le=65535)]


class Resources(DomainModel):
    cpus: Annotated[int, Field(ge=1)]
    memory_mib: Annotated[int, Field(ge=1)]
    disk_gib: Annotated[int, Field(ge=1)]
    gpus: Annotated[int, Field(ge=0)] = 0


class NetworkRule(DomainModel):
    """One internet host a VM may reach."""

    host: NonEmptyStr
    """A host name. Never an IP address or a range."""
    ports: list[Port] = [443]


class ResourceMount(DomainModel):
    """A named resource the worker provides, mounted read-only. The project never names a host path."""

    resource: NonEmptyStr
    """For example ``go-module-cache``. Each worker maps the name to its own storage."""
    path: NonEmptyStr
    """Where it appears inside the VM."""


class CapabilityManifest(DomainModel):
    """Everything a workstream VM may do. What it is granted equals this, and nothing more."""

    network: list[NetworkRule] = []
    mounts: list[ResourceMount] = []
    kvm: bool = False
    resources: Resources


class WorkerOffer(DomainModel):
    """What a worker allows the VMs it runs."""

    capacity: Resources
    """The worker's total, after its own reserve."""
    kvm: bool = False
    resources: list[NonEmptyStr] = []
    """The named resources it can mount."""
