"""What every domain model shares: strict validation, UTC times, and the schema version."""

from __future__ import annotations

from datetime import datetime, timedelta
from typing import Annotated, Literal

from pydantic import AfterValidator, AwareDatetime, BaseModel, ConfigDict, Field

# The version of the domain schema. Readers refuse a version they do not
# support. Within one version, changes are additive only: a new optional
# field, a new model. Anything else is a new version.
SCHEMA_VERSION = 1


def _utc(value: datetime) -> datetime:
    if value.utcoffset() != timedelta(0):
        raise ValueError("time must be in UTC")
    return value


UtcDatetime = Annotated[AwareDatetime, AfterValidator(_utc)]
"""A point in time. Always timezone-aware and in UTC."""

NonEmptyStr = Annotated[str, Field(min_length=1)]


class DomainModel(BaseModel):
    """The base of every model: unknown fields and wrong types fail validation."""

    model_config = ConfigDict(extra="forbid", strict=True, validate_default=True, use_attribute_docstrings=True)


class Record(DomainModel):
    """A model stored on its own, with its own ID. It carries the schema version it was written in."""

    schema_version: Literal[1] = SCHEMA_VERSION
    created_at: UtcDatetime
    updated_at: UtcDatetime
