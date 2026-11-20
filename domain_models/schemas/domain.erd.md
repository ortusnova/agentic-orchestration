# Domain model diagrams

## Legend

### Lines

| Symbol | Name | Meaning |
|---|---|---|
| <img src="legend/line-contains.svg" alt="Contains"> | **Contains** | A holds B inside it, in the field the label names. B is a value object: it has no ID and is stored only as part of A. |
| <img src="legend/line-refers.svg" alt="Refers to by ID"> | **Refers to by ID** | A has a field, marked `FK`, that holds the ID of record B. The label names the field. |

### Line ends

| Symbol | Name | Meaning |
|---|---|---|
| <img src="legend/end-one.svg" alt="Exactly one"> | **Exactly one** | Each A has exactly one B. The field is required. |
| <img src="legend/end-zero-or-one.svg" alt="Zero or one"> | **Zero or one** | Each A has at most one B. The field may be null or left out. |
| <img src="legend/end-zero-or-many.svg" alt="Zero or many"> | **Zero or many** | Each A has any number of B: the field is a list, or many A hold the same B. |
| <img src="legend/example.svg" alt="Reading both ends"> | **Reading both ends** | Read each end from the box at the other end. Each `Session` has exactly one `Workstream`, in `workstream_id`. Each `Workstream` has zero or many `Session`s. |

### Boxes

| Symbol | Name | Meaning |
|---|---|---|
| <img src="legend/box-full.svg" alt="Model, in full"> | **Model, in full** | A record or value object with all its fields. Each record has a diagram of its own. |
| <img src="legend/box-name.svg" alt="Other record"> | **Other record** | Drawn by name only, to show a line to it. Its fields are in its own diagram. |
| <img src="legend/box-colours.svg" alt="Box colour"> | **Box colour** | Means nothing. Mermaid picks the colours. |

### Fields

| Symbol | Name | Meaning |
|---|---|---|
| <img src="legend/field-pk.svg" alt="Own ID"> | **Own ID** | The record's own ID. |
| <img src="legend/field-fk.svg" alt="Reference"> | **Reference** | Holds the ID of another record, or a list of them. A dashed line leads to that record. |
| <img src="legend/field-name.svg" alt="Name"> | **Name** | The field's name. Line labels use it. |
| <img src="legend/field-type.svg" alt="Type"> | **Type** | The field's type, written as in Python. `WorkstreamId` is an ID of a `Workstream`: `<prefix>_<ULID>`, with the prefixes in the records table. |
| <img src="legend/field-none.svg" alt="May be null"> | **May be null** | `| None`: the value may be null. With no `=`, the field must still be given. |
| <img src="legend/field-default.svg" alt="Has a default"> | **Has a default** | `= value`: the field may be left out, and then has that value. |
| <img src="legend/field-list.svg" alt="List"> | **List** | A list of the type in brackets. |
| <img src="legend/field-enum.svg" alt="Enum"> | **Enum** | A type with no box of its own. Its values are in the schema. |
| <img src="legend/field-union.svg" alt="One of several"> | **One of several** | The field holds one of the models joined by `|`. Each has a solid line. |
| <img src="legend/field-literal.svg" alt="Fixed value"> | **Fixed value** | Always this value: here, the `kind` that tells the members of a union apart. |
| <img src="legend/field-datetime.svg" alt="Time"> | **Time** | A `datetime` is always in UTC. |

## About this file

Generated from `schemas/domain.schema.json` by `make -C domain_models schemas`. Do not edit.

A **record** is stored on its own and has an ID. Every other model is a **value object**, stored
only inside a record. Every record also has `schema_version`, `created_at`, `updated_at`, which the per-record diagrams leave out.

## Records

| Record | ID prefix | Belongs to | Also refers to |
|---|---|---|---|
| `Person` | `usr_` | — | — |
| `Project` | `prj_` | — | — |
| `Membership` | `mbr_` | `Person`, `Project` | — |
| `Epic` | `ep_` | `Project` | `Ticket` |
| `Workstream` | `ws_` | `Project`, `Ticket` | `Convo`, `Epic`, `Person`, `Schedule`, `Session`, `WorkstreamVm` |
| `WorkstreamEvent` | `wse_` | `Workstream` | `Convo`, `Person`, `Session` |
| `Ticket` | `tk_` | `Project` | `Epic`, `Workstream` |
| `Schedule` | `sch_` | `Person`, `Project` | — |
| `Session` | `ses_` | `Workstream` | `Person`, `Worker`, `WorkstreamVm` |
| `WorkstreamVm` | `vm_` | `Worker`, `Workstream` | — |
| `Worker` | `wkr_` | — | — |
| `Convo` | `cv_` | `Project` | — |
| `ConvoLink` | `cvl_` | `Convo`, `Workstream` | `Person`, `Session` |
| `ConvoEvent` | `cve_` | `Convo` | `Person`, `Session` |

Value objects: `AgentRef`, `Approval`, `CapabilityManifest`, `ComponentActor`, `ExternalActor`, `ExternalIdentity`, `ImageRef`, `NetworkRule`, `PersonActor`, `PullRequestInfo`, `Repository`, `ResourceMount`, `Resources`, `SessionActor`, `Source`, `StoryRef`, `TrackerMirror`, `Usage`, `WorkerOffer`.

## Overview

Records only. A line joins a record to a record it belongs to: a required ID field holding one ID. A record with no line out is a root. Every line here is a reference by ID, even where Mermaid draws one solid at a crossing.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Person
    Project
    Membership
    Epic
    Workstream
    WorkstreamEvent
    Ticket
    Schedule
    Session
    WorkstreamVm
    Worker
    Convo
    ConvoLink
    ConvoEvent
    Membership }o..|| Project : "project_id"
    Membership }o..|| Person : "person_id"
    Epic }o..|| Project : "project_id"
    Workstream }o..|| Project : "project_id"
    Workstream }o..|| Ticket : "ticket_id"
    WorkstreamEvent }o..|| Workstream : "workstream_id"
    Ticket }o..|| Project : "project_id"
    Schedule }o..|| Project : "project_id"
    Schedule }o..|| Person : "owner"
    Session }o..|| Workstream : "workstream_id"
    WorkstreamVm }o..|| Workstream : "workstream_id"
    WorkstreamVm }o..|| Worker : "worker_id"
    Convo }o..|| Project : "project_id"
    ConvoLink }o..|| Convo : "convo_id"
    ConvoLink }o..|| Workstream : "workstream_id"
    ConvoEvent }o..|| Convo : "convo_id"
```

## Each record

### Person

A human who uses the system: plans, approves, steers, or asks for work.

Used to attribute every action, to grant roles in projects through memberships, and to recognise the same person in other systems through their external identities.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Person {
        ⁠PK id "PersonId"
        ⁠ display_name "str"
        ⁠ email "str | None = None"
        ⁠ identities "list[ExternalIdentity] = []"
    }
    ExternalIdentity {
        ⁠ system "str"
        ⁠ subject "str"
        ⁠ handle "str | None = None"
    }
    Person ||--o{ ExternalIdentity : "identities"
```

### Project

A body of work: its repositories, its spec store, and its members.

Everything else belongs to a project. It scopes who may see and do what, and which repositories sessions may change.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Project {
        ⁠PK id "ProjectId"
        ⁠ slug "str"
        ⁠ name "str"
        ⁠ repositories "list[Repository]"
    }
    Repository {
        ⁠PK id "RepositoryId"
        ⁠ role "RepositoryRole"
        ⁠ forge "Forge"
        ⁠ owner "str"
        ⁠ name "str"
        ⁠ default_branch "str = 'main'"
    }
    Project ||--o{ Repository : "repositories"
```

### Membership

One person's role in one project.

Used to decide what that person may see and do in the project. A person has at most one membership per project.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Membership {
        ⁠PK id "MembershipId"
        ⁠FK project_id "ProjectId"
        ⁠FK person_id "PersonId"
        ⁠ role "ProjectRole"
    }
    Membership }o..|| Project : "project_id"
    Membership }o..|| Person : "person_id"
```

### Epic

The workstreams that come from one plan DAG: the breakdown of one OpenSpec change.

Used to follow the progress of a plan as a whole. An epic is never executed itself: it is done when all its workstreams are.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Epic {
        ⁠PK id "EpicId"
        ⁠FK project_id "ProjectId"
        ⁠ change "str"
        ⁠ title "str"
        ⁠ state "EpicState"
        ⁠FK ticket_id "TicketId | None = None"
    }
    Epic }o..|| Project : "project_id"
    Epic }o..o| Ticket : "ticket_id"
```

### Workstream

One unit of work, from the moment it is asked for until it is done or cancelled.

A planned story, an ad-hoc ask, or a scheduled run. The central record: sessions run for it, its ticket shows it in trackers, its convos carry the discussion about it, and its events record everything that happened to it.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Workstream {
        ⁠PK id "WorkstreamId"
        ⁠FK project_id "ProjectId"
        ⁠FK epic_id "EpicId | None = None"
        ⁠FK ticket_id "TicketId"
        ⁠ shape "WorkstreamShape"
        ⁠ source "Source"
        ⁠ story "StoryRef | None = None"
        ⁠ title "str"
        ⁠ goal "str"
        ⁠FK repositories "list[RepositoryId]"
        ⁠ state "WorkstreamState"
        ⁠ approval "Approval"
        ⁠ priority "int = 2"
        ⁠ due_at "datetime | None = None"
        ⁠FK blocked_by "list[WorkstreamId] = []"
        ⁠FK owner "PersonId | None"
        ⁠FK participants "list[PersonId] = []"
        ⁠FK vm_id "WorkstreamVmId | None = None"
        ⁠FK current_session_id "SessionId | None = None"
        ⁠ closed_at "datetime | None = None"
    }
    Source {
        ⁠ channel "SourceChannel"
        ⁠FK requester "PersonId | None"
        ⁠FK schedule_id "ScheduleId | None = None"
        ⁠FK reply_to "ConvoId | None = None"
        ⁠ origin_url "str | None = None"
    }
    StoryRef {
        ⁠ plan_key "str"
        ⁠ change "str"
        ⁠ digest "str"
        ⁠ covers "list[str] = []"
    }
    Approval {
        ⁠ state "ApprovalState"
        ⁠FK by "PersonId | None = None"
        ⁠ at "datetime | None = None"
        ⁠ digest "str | None = None"
    }
    Workstream }o..|| Project : "project_id"
    Workstream }o..o| Epic : "epic_id"
    Workstream }o..|| Ticket : "ticket_id"
    Workstream ||--|| Source : "source"
    Workstream ||--o| StoryRef : "story"
    Workstream }o..o{ Repository : "repositories"
    Workstream ||--|| Approval : "approval"
    Workstream }o..o{ Workstream : "blocked_by"
    Workstream }o..o| Person : "owner"
    Workstream }o..o{ Person : "participants"
    Workstream }o..o| WorkstreamVm : "vm_id"
    Workstream }o..o| Session : "current_session_id"
    Source }o..o| Person : "requester"
    Source }o..o| Schedule : "schedule_id"
    Source }o..o| Convo : "reply_to"
    Approval }o..o| Person : "by"
```

### WorkstreamEvent

One entry in a workstream's history.

A state change, an approval, a session starting or ending, a steer, an escalation, or a decision. Used to show people what happened and why, and to give later sessions that context. Append-only: never changed or deleted.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    WorkstreamEvent {
        ⁠PK id "WorkstreamEventId"
        ⁠FK workstream_id "WorkstreamId"
        ⁠ at "datetime"
        ⁠ actor "PersonActor | ComponentActor | SessionActor | ExternalActor"
        ⁠ kind "WorkstreamEventKind"
        ⁠ summary "str"
        ⁠FK session_id "SessionId | None = None"
        ⁠FK convo_id "ConvoId | None = None"
    }
    PersonActor {
        ⁠ kind "Literal['person'] = 'person'"
        ⁠FK person_id "PersonId"
    }
    ComponentActor {
        ⁠ kind "Literal['component'] = 'component'"
        ⁠ component "Component"
    }
    SessionActor {
        ⁠ kind "Literal['session'] = 'session'"
        ⁠FK session_id "SessionId"
    }
    ExternalActor {
        ⁠ kind "Literal['external'] = 'external'"
        ⁠ system "str"
        ⁠ subject "str"
        ⁠ handle "str | None = None"
    }
    WorkstreamEvent }o..|| Workstream : "workstream_id"
    WorkstreamEvent ||--|| PersonActor : "actor"
    WorkstreamEvent ||--|| ComponentActor : "actor"
    WorkstreamEvent ||--|| SessionActor : "actor"
    WorkstreamEvent ||--|| ExternalActor : "actor"
    WorkstreamEvent }o..o| Session : "session_id"
    WorkstreamEvent }o..o| Convo : "convo_id"
    PersonActor }o..|| Person : "person_id"
    SessionActor }o..|| Session : "session_id"
```

### Ticket

The tracker record of a workstream or an epic, kept in the system's own database.

People find and discuss work through it, and it is mirrored to external trackers (Huly, Jira, Linear) in either direction. A ticket has no state of its own: its state is its subject's, and each mirror maps that state to its tracker's states.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Ticket {
        ⁠PK id "TicketId"
        ⁠FK project_id "ProjectId"
        ⁠FK workstream_id "WorkstreamId | None = None"
        ⁠FK epic_id "EpicId | None = None"
        ⁠ title "str"
        ⁠ description "str = ''"
        ⁠ labels "list[str] = []"
        ⁠ mirrors "list[TrackerMirror] = []"
    }
    TrackerMirror {
        ⁠ tracker "TrackerKind"
        ⁠ instance "str"
        ⁠ external_id "str"
        ⁠ external_key "str | None = None"
        ⁠ url "str | None = None"
        ⁠ origin "MirrorOrigin"
        ⁠ synced_at "datetime | None = None"
    }
    Ticket }o..|| Project : "project_id"
    Ticket }o..o| Workstream : "workstream_id"
    Ticket }o..o| Epic : "epic_id"
    Ticket ||--o{ TrackerMirror : "mirrors"
```

### Schedule

A recurring request: each time it fires, it creates a scheduled workstream with its goal.

Used for cron-like work, such as dependency updates or nightly checks. PROVISIONAL: cron ownership is open (review item 13).

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Schedule {
        ⁠PK id "ScheduleId"
        ⁠FK project_id "ProjectId"
        ⁠ title "str"
        ⁠ cron "str"
        ⁠ goal "str"
        ⁠FK repositories "list[RepositoryId]"
        ⁠FK owner "PersonId"
        ⁠ enabled "bool = True"
    }
    Schedule }o..|| Project : "project_id"
    Schedule }o..o{ Repository : "repositories"
    Schedule }o..|| Person : "owner"
```

### Session

One agent conversation with one purpose, run in its workstream's VM.

For example, drafting a change, reviewing it, or addressing review comments. The unit a worker runs and acp-broker relays; a retry is a new session. Records who held it, how it ended, and what it cost.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Session {
        ⁠PK id "SessionId"
        ⁠FK workstream_id "WorkstreamId"
        ⁠ stage "str"
        ⁠ attempt "int"
        ⁠ state "SessionState"
        ⁠ agent "AgentRef"
        ⁠FK vm_id "WorkstreamVmId | None = None"
        ⁠FK worker_id "WorkerId | None = None"
        ⁠FK held_by "PersonId | None = None"
        ⁠ started_at "datetime | None = None"
        ⁠ ended_at "datetime | None = None"
        ⁠ outcome "SessionOutcome | None = None"
        ⁠ statement "str | None = None"
        ⁠ turns "int = 0"
        ⁠ usage "Usage = Usage()"
    }
    AgentRef {
        ⁠ harness "str"
        ⁠ model "str"
    }
    Usage {
        ⁠ input_tokens "int = 0"
        ⁠ output_tokens "int = 0"
        ⁠ cache_read_tokens "int = 0"
        ⁠ cache_write_tokens "int = 0"
        ⁠ cost_usd "float | None = None"
    }
    Session }o..|| Workstream : "workstream_id"
    Session ||--|| AgentRef : "agent"
    Session }o..o| WorkstreamVm : "vm_id"
    Session }o..o| Worker : "worker_id"
    Session }o..o| Person : "held_by"
    Session ||--o| Usage : "usage"
```

### WorkstreamVm

The one VM a workstream's sessions run in, from its first session until the workstream closes.

Records which worker runs it, the image it started from, and the capabilities it asked for and was granted. Untrusted: everything the agent runs is inside it.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    WorkstreamVm {
        ⁠PK id "WorkstreamVmId"
        ⁠FK workstream_id "WorkstreamId"
        ⁠FK worker_id "WorkerId"
        ⁠ image "ImageRef"
        ⁠ state "VmState"
        ⁠ declared "CapabilityManifest"
        ⁠ granted "CapabilityManifest | None = None"
        ⁠ removed_at "datetime | None = None"
    }
    ImageRef {
        ⁠ name "str"
        ⁠ digest "str"
    }
    CapabilityManifest {
        ⁠ network "list[NetworkRule] = []"
        ⁠ mounts "list[ResourceMount] = []"
        ⁠ kvm "bool = False"
        ⁠ resources "Resources"
    }
    NetworkRule {
        ⁠ host "str"
        ⁠ ports "list[int] = [443]"
    }
    ResourceMount {
        ⁠ resource "str"
        ⁠ path "str"
    }
    Resources {
        ⁠ cpus "int"
        ⁠ memory_mib "int"
        ⁠ disk_gib "int"
        ⁠ gpus "int = 0"
    }
    WorkstreamVm }o..|| Workstream : "workstream_id"
    WorkstreamVm }o..|| Worker : "worker_id"
    WorkstreamVm ||--|| ImageRef : "image"
    WorkstreamVm ||--|| CapabilityManifest : "declared"
    WorkstreamVm ||--o| CapabilityManifest : "granted"
    CapabilityManifest ||--o{ NetworkRule : "network"
    CapabilityManifest ||--o{ ResourceMount : "mounts"
    CapabilityManifest ||--|| Resources : "resources"
```

### Worker

A trusted machine that runs workstream VMs.

Used to place each VM on a worker that offers every capability and resource the VM needs, and to know which workers are online.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Worker {
        ⁠PK id "WorkerId"
        ⁠ name "str"
        ⁠ state "WorkerState"
        ⁠ offer "WorkerOffer"
        ⁠ last_seen_at "datetime | None = None"
    }
    WorkerOffer {
        ⁠ capacity "Resources"
        ⁠ kvm "bool = False"
        ⁠ resources "list[str] = []"
    }
    Resources {
        ⁠ cpus "int"
        ⁠ memory_mib "int"
        ⁠ disk_gib "int"
        ⁠ gpus "int = 0"
    }
    Worker ||--|| WorkerOffer : "offer"
    WorkerOffer ||--|| Resources : "capacity"
```

### Convo

A conversation outside the system that bears on one or more workstreams.

A pull request, a chat thread, an email chain, or the comments on a tracker issue. Used to route follow-up events to the workstreams attached to it, and to send replies back where they were asked.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    Convo {
        ⁠PK id "ConvoId"
        ⁠FK project_id "ProjectId"
        ⁠ kind "ConvoKind"
        ⁠ system "str"
        ⁠ external_id "str"
        ⁠ url "str | None = None"
        ⁠ title "str | None = None"
        ⁠ pull_request "PullRequestInfo | None = None"
    }
    PullRequestInfo {
        ⁠FK repository_id "RepositoryId"
        ⁠ number "int"
        ⁠ head_branch "str"
        ⁠ head_sha "str"
        ⁠ state "PullRequestState"
        ⁠ merged_sha "str | None = None"
    }
    Convo }o..|| Project : "project_id"
    Convo ||--o| PullRequestInfo : "pull_request"
    PullRequestInfo }o..|| Repository : "repository_id"
```

### ConvoLink

Attaches one convo to one workstream, and says why.

The convo started the workstream, the workstream created it, or it was attached later. Follow-up events in the convo go to every workstream attached to it.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    ConvoLink {
        ⁠PK id "ConvoLinkId"
        ⁠FK convo_id "ConvoId"
        ⁠FK workstream_id "WorkstreamId"
        ⁠ reason "ConvoLinkReason"
        ⁠ linked_by "PersonActor | ComponentActor | SessionActor | ExternalActor"
        ⁠ unlinked_at "datetime | None = None"
    }
    PersonActor {
        ⁠ kind "Literal['person'] = 'person'"
        ⁠FK person_id "PersonId"
    }
    ComponentActor {
        ⁠ kind "Literal['component'] = 'component'"
        ⁠ component "Component"
    }
    SessionActor {
        ⁠ kind "Literal['session'] = 'session'"
        ⁠FK session_id "SessionId"
    }
    ExternalActor {
        ⁠ kind "Literal['external'] = 'external'"
        ⁠ system "str"
        ⁠ subject "str"
        ⁠ handle "str | None = None"
    }
    ConvoLink }o..|| Convo : "convo_id"
    ConvoLink }o..|| Workstream : "workstream_id"
    ConvoLink ||--|| PersonActor : "linked_by"
    ConvoLink ||--|| ComponentActor : "linked_by"
    ConvoLink ||--|| SessionActor : "linked_by"
    ConvoLink ||--|| ExternalActor : "linked_by"
    PersonActor }o..|| Person : "person_id"
    SessionActor }o..|| Session : "session_id"
```

### ConvoEvent

One event in a convo: a message, a review comment or verdict, a CI result, a state or label change.

Used to start and inform the sessions of the workstreams attached to the convo. Append-only. Its body is untrusted.

```mermaid
---
config:
  themeCSS: "g.label.name * { font-weight: bold; }"
---
erDiagram
    ConvoEvent {
        ⁠PK id "ConvoEventId"
        ⁠FK convo_id "ConvoId"
        ⁠ at "datetime"
        ⁠ kind "ConvoEventKind"
        ⁠ author "PersonActor | ComponentActor | SessionActor | ExternalActor"
        ⁠ external_id "str"
        ⁠ body "str = ''"
        ⁠ url "str | None = None"
        ⁠FK in_reply_to "ConvoEventId | None = None"
    }
    PersonActor {
        ⁠ kind "Literal['person'] = 'person'"
        ⁠FK person_id "PersonId"
    }
    ComponentActor {
        ⁠ kind "Literal['component'] = 'component'"
        ⁠ component "Component"
    }
    SessionActor {
        ⁠ kind "Literal['session'] = 'session'"
        ⁠FK session_id "SessionId"
    }
    ExternalActor {
        ⁠ kind "Literal['external'] = 'external'"
        ⁠ system "str"
        ⁠ subject "str"
        ⁠ handle "str | None = None"
    }
    ConvoEvent }o..|| Convo : "convo_id"
    ConvoEvent ||--|| PersonActor : "author"
    ConvoEvent ||--|| ComponentActor : "author"
    ConvoEvent ||--|| SessionActor : "author"
    ConvoEvent ||--|| ExternalActor : "author"
    ConvoEvent }o..o| ConvoEvent : "in_reply_to"
    PersonActor }o..|| Person : "person_id"
    SessionActor }o..|| Session : "session_id"
```
