import json

import pytest
from pydantic import ValidationError

from domain_models import RECORDS, Convo, Project, Session, Ticket, Workstream
from domain_models.ids import IdKind, new_id
from domain_models.schema import SCHEMA_PATH, generate

NOW = "2026-11-20T12:00:00Z"
TIMES = {"created_at": NOW, "updated_at": NOW}
PERSON = new_id(IdKind.PERSON)
PROJECT = new_id(IdKind.PROJECT)
REPO = new_id(IdKind.REPOSITORY)


def load(model, **fields):
    """Validate ``fields`` as JSON, the way every reader receives a record."""
    return model.model_validate_json(json.dumps({**TIMES, **fields}))


def workstream(**over):
    fields = {
        "id": new_id(IdKind.WORKSTREAM),
        "project_id": PROJECT,
        "ticket_id": new_id(IdKind.TICKET),
        "shape": "adhoc",
        "source": {"channel": "chat", "requester": PERSON},
        "title": "Fix the invoice total",
        "goal": "Invoice totals include tax",
        "repositories": [REPO],
        "state": "submitted",
        "approval": {"state": "pending"},
        "owner": PERSON,
    }
    return load(Workstream, **{**fields, **over})


def planned(**over):
    story = {"plan_key": "billing/invoice", "change": "billing", "digest": "sha256:ab"}
    return workstream(shape="planned", story=story, epic_id=new_id(IdKind.EPIC), **over)


def session(**over):
    fields = {
        "id": new_id(IdKind.SESSION),
        "workstream_id": new_id(IdKind.WORKSTREAM),
        "stage": "draft",
        "attempt": 1,
        "state": "queued",
        "agent": {"harness": "opencode", "model": "anthropic/claude-opus-5-5"},
    }
    return load(Session, **{**fields, **over})


def test_the_committed_schema_is_current():
    assert SCHEMA_PATH.read_text() == generate(), "run `make -C domain_models schemas`"


def test_every_record_is_in_the_schema():
    defs = json.loads(generate())["$defs"]
    assert {m.__name__ for m in RECORDS} <= set(defs)


def test_a_record_round_trips_through_json():
    ws = planned()
    assert Workstream.model_validate_json(ws.model_dump_json()) == ws


def test_an_unknown_field_fails():
    with pytest.raises(ValidationError, match="Extra inputs"):
        workstream(colour="blue")


def test_an_unknown_schema_version_fails():
    with pytest.raises(ValidationError, match="schema_version"):
        workstream(schema_version=2)


def test_an_unknown_state_fails():
    with pytest.raises(ValidationError, match="state"):
        workstream(state="paused")


def test_a_time_not_in_utc_fails():
    with pytest.raises(ValidationError, match="UTC"):
        workstream(created_at="2026-11-20T12:00:00+01:00")


def test_a_time_without_a_zone_fails():
    with pytest.raises(ValidationError):
        workstream(created_at="2026-11-20T12:00:00")


def test_an_id_of_the_wrong_kind_fails():
    with pytest.raises(ValidationError, match="project_id"):
        workstream(project_id=new_id(IdKind.WORKSTREAM))


def test_an_external_key_is_not_an_id():
    with pytest.raises(ValidationError, match="ticket_id"):
        workstream(ticket_id="ONS-77")


def test_a_planned_workstream_has_a_story_and_an_epic():
    assert planned().story.plan_key == "billing/invoice"
    with pytest.raises(ValidationError, match="story"):
        workstream(shape="planned", epic_id=new_id(IdKind.EPIC))
    with pytest.raises(ValidationError, match="epic_id"):
        workstream(epic_id=new_id(IdKind.EPIC))


def test_a_scheduled_workstream_comes_from_a_schedule():
    source = {"channel": "schedule", "requester": None, "schedule_id": new_id(IdKind.SCHEDULE)}
    assert workstream(shape="scheduled", source=source, owner=None).owner is None
    with pytest.raises(ValidationError, match="scheduled"):
        workstream(shape="scheduled")
    with pytest.raises(ValidationError, match="requester"):
        workstream(source={"channel": "chat", "requester": None})


def test_a_workstream_cannot_block_itself():
    ws = workstream()
    with pytest.raises(ValidationError, match="block itself"):
        workstream(id=ws.id, blocked_by=[ws.id])


def test_a_closed_workstream_has_closed_at():
    assert workstream(state="done", closed_at=NOW).closed_at is not None
    with pytest.raises(ValidationError, match="closed_at"):
        workstream(state="cancelled")


def test_a_decided_approval_names_who_and_when():
    assert workstream(approval={"state": "approved", "by": PERSON, "at": NOW}).approval.by == PERSON
    with pytest.raises(ValidationError, match="decided"):
        workstream(approval={"state": "approved"})
    with pytest.raises(ValidationError, match="decided"):
        workstream(approval={"state": "pending", "by": PERSON})


def test_a_ticket_belongs_to_one_workstream_or_one_epic():
    base = {"id": new_id(IdKind.TICKET), "project_id": PROJECT, "title": "Fix it"}
    assert load(Ticket, **base, workstream_id=new_id(IdKind.WORKSTREAM))
    with pytest.raises(ValidationError, match="exactly one"):
        load(Ticket, **base)
    with pytest.raises(ValidationError, match="exactly one"):
        load(Ticket, **base, workstream_id=new_id(IdKind.WORKSTREAM), epic_id=new_id(IdKind.EPIC))


def test_a_ticket_has_one_mirror_per_tracker_instance():
    mirror = {"tracker": "huly", "instance": "https://huly.example/sortie", "external_id": "65f0", "origin": "imported"}
    base = {"id": new_id(IdKind.TICKET), "project_id": PROJECT, "title": "Fix it", "workstream_id": new_id(IdKind.WORKSTREAM)}
    assert load(Ticket, **base, mirrors=[mirror]).mirrors[0].external_id == "65f0"
    with pytest.raises(ValidationError, match="one mirror"):
        load(Ticket, **base, mirrors=[mirror, {**mirror, "external_id": "65f1"}])


def test_a_project_has_one_spec_store():
    repo = {"id": REPO, "role": "code", "forge": "github", "owner": "ortusnova", "name": "app"}
    store = {"id": new_id(IdKind.REPOSITORY), "role": "spec_store", "forge": "github", "owner": "ortusnova", "name": "specs"}
    assert load(Project, id=PROJECT, slug="app", name="App", repositories=[repo, store])
    with pytest.raises(ValidationError, match="spec_store"):
        load(Project, id=PROJECT, slug="app", name="App", repositories=[repo])


def test_a_session_ends_with_an_outcome():
    assert session(state="ended", started_at=NOW, ended_at=NOW, outcome="completed").outcome == "completed"
    with pytest.raises(ValidationError, match="outcome"):
        session(state="ended", ended_at=NOW)
    with pytest.raises(ValidationError, match="outcome"):
        session(outcome="failed")


def test_a_held_session_names_who_holds_it():
    assert session(state="held", started_at=NOW, held_by=PERSON).held_by == PERSON
    with pytest.raises(ValidationError, match="held_by"):
        session(state="held", started_at=NOW)


def test_attempts_start_at_one():
    with pytest.raises(ValidationError, match="attempt"):
        session(attempt=0)


def test_a_pull_request_convo_carries_its_pull_request():
    base = {"id": new_id(IdKind.CONVO), "project_id": PROJECT, "system": "github", "external_id": "ortusnova/app#12"}
    pr = {"repository_id": REPO, "number": 12, "head_branch": "ws/fix", "head_sha": "abc123", "state": "open"}
    assert load(Convo, **base, kind="pull_request", pull_request=pr).pull_request.number == 12
    with pytest.raises(ValidationError, match="pull_request"):
        load(Convo, **base, kind="pull_request")
    with pytest.raises(ValidationError, match="pull_request"):
        load(Convo, **base, kind="chat_thread", pull_request=pr)
