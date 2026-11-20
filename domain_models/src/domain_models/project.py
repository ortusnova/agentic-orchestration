"""Projects, their repositories, and their members."""

from __future__ import annotations

import enum
from typing import Annotated

from pydantic import Field, model_validator

from .base import DomainModel, NonEmptyStr, Record
from .ids import MembershipId, PersonId, ProjectId, RepositoryId

Slug = Annotated[str, Field(pattern=r"^[a-z0-9][a-z0-9-]{0,62}$")]


class Forge(enum.StrEnum):
    GITHUB = "github"
    GITEA = "gitea"
    GITLAB = "gitlab"


class RepositoryRole(enum.StrEnum):
    CODE = "code"
    """A repository sessions change."""
    SPEC_STORE = "spec_store"
    """The project's OpenSpec store: its plan, changes, and breakdowns."""


class Repository(DomainModel):
    id: RepositoryId
    role: RepositoryRole
    forge: Forge
    owner: NonEmptyStr
    name: NonEmptyStr
    default_branch: NonEmptyStr = "main"


class Project(Record):
    id: ProjectId
    slug: Slug
    name: NonEmptyStr
    repositories: list[Repository]
    """Every repository of the project. Exactly one has the ``spec_store`` role."""

    @model_validator(mode="after")
    def _one_spec_store(self) -> Project:
        stores = [r for r in self.repositories if r.role is RepositoryRole.SPEC_STORE]
        if len(stores) != 1:
            raise ValueError(f"a project has exactly one spec_store repository, not {len(stores)}")
        if len({r.id for r in self.repositories}) != len(self.repositories):
            raise ValueError("repository IDs repeat")
        return self


class ProjectRole(enum.StrEnum):
    """What a member may do in one project. Each role includes the ones before it."""

    VIEWER = "viewer"
    """Sees the project, its workstreams, and their sessions."""
    PLANNER = "planner"
    """Also submits workstreams and plans with the planning session."""
    APPROVER = "approver"
    """Also approves workstreams and steers, takes over, or cancels sessions."""
    ADMIN = "admin"
    """Also manages the project's members, repositories, and settings."""


class Membership(Record):
    """One person's role in one project. A person has at most one membership per project."""

    id: MembershipId
    project_id: ProjectId
    person_id: PersonId
    role: ProjectRole
