# Domain model diagrams

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

Records only. A line joins a record to a record it belongs to: a required ID field holding one ID. A record with no line out is a root.

```mermaid
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
    Membership }o--|| Project : "project_id"
    Membership }o--|| Person : "person_id"
    Epic }o--|| Project : "project_id"
    Workstream }o--|| Project : "project_id"
    Workstream }o--|| Ticket : "ticket_id"
    WorkstreamEvent }o--|| Workstream : "workstream_id"
    Ticket }o--|| Project : "project_id"
    Schedule }o--|| Project : "project_id"
    Schedule }o--|| Person : "owner"
    Session }o--|| Workstream : "workstream_id"
    WorkstreamVm }o--|| Workstream : "workstream_id"
    WorkstreamVm }o--|| Worker : "worker_id"
    Convo }o--|| Project : "project_id"
    ConvoLink }o--|| Convo : "convo_id"
    ConvoLink }o--|| Workstream : "workstream_id"
    ConvoEvent }o--|| Convo : "convo_id"
```

## Each record

### Person

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Person {
        PersonId id PK
        string display_name
        string email "optional"
        ExternalIdentity[] identities
    }
    ExternalIdentity {
        string system
        string subject
        string handle "optional"
    }
    Person ||--o{ ExternalIdentity : "identities"
```

### Project

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Project {
        ProjectId id PK
        string slug
        string name
        Repository[] repositories
    }
    Repository {
        RepositoryId id PK
        RepositoryRole role
        Forge forge
        string owner
        string name
        string default_branch "optional"
    }
    Project ||--o{ Repository : "repositories"
```

### Membership

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Membership {
        MembershipId id PK
        ProjectId project_id FK
        PersonId person_id FK
        ProjectRole role
    }
    Membership }o..|| Project : "project_id"
    Membership }o..|| Person : "person_id"
```

### Epic

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Epic {
        EpicId id PK
        ProjectId project_id FK
        string change
        string title
        EpicState state
        TicketId ticket_id FK "optional"
    }
    Epic }o..|| Project : "project_id"
    Epic }o..o| Ticket : "ticket_id"
```

### Workstream

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Workstream {
        WorkstreamId id PK
        ProjectId project_id FK
        EpicId epic_id FK "optional"
        TicketId ticket_id FK
        WorkstreamShape shape
        Source source
        StoryRef story "optional"
        string title
        string goal
        RepositoryId[] repositories FK
        WorkstreamState state
        Approval approval
        int priority "optional"
        datetime due_at "optional"
        WorkstreamId[] blocked_by FK
        PersonId owner FK "optional"
        PersonId[] participants FK
        WorkstreamVmId vm_id FK "optional"
        SessionId current_session_id FK "optional"
        datetime closed_at "optional"
    }
    Source {
        SourceChannel channel
        PersonId requester FK "optional"
        ScheduleId schedule_id FK "optional"
        ConvoId reply_to FK "optional"
        string origin_url "optional"
    }
    StoryRef {
        string plan_key
        string change
        string digest
        string[] covers
    }
    Approval {
        ApprovalState state
        PersonId by FK "optional"
        datetime at "optional"
        string digest "optional"
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

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    WorkstreamEvent {
        WorkstreamEventId id PK
        WorkstreamId workstream_id FK
        datetime at
        PersonActor_ComponentActor_SessionActor_ExternalActor actor
        WorkstreamEventKind kind
        string summary
        SessionId session_id FK "optional"
        ConvoId convo_id FK "optional"
    }
    PersonActor {
        const kind "optional"
        PersonId person_id FK
    }
    ComponentActor {
        const kind "optional"
        Component component
    }
    SessionActor {
        const kind "optional"
        SessionId session_id FK
    }
    ExternalActor {
        const kind "optional"
        string system
        string subject
        string handle "optional"
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

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Ticket {
        TicketId id PK
        ProjectId project_id FK
        WorkstreamId workstream_id FK "optional"
        EpicId epic_id FK "optional"
        string title
        string description "optional"
        string[] labels
        TrackerMirror[] mirrors
    }
    TrackerMirror {
        TrackerKind tracker
        string instance
        string external_id
        string external_key "optional"
        string url "optional"
        MirrorOrigin origin
        datetime synced_at "optional"
    }
    Ticket }o..|| Project : "project_id"
    Ticket }o..o| Workstream : "workstream_id"
    Ticket }o..o| Epic : "epic_id"
    Ticket ||--o{ TrackerMirror : "mirrors"
```

### Schedule

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Schedule {
        ScheduleId id PK
        ProjectId project_id FK
        string title
        string cron
        string goal
        RepositoryId[] repositories FK
        PersonId owner FK
        bool enabled "optional"
    }
    Schedule }o..|| Project : "project_id"
    Schedule }o..o{ Repository : "repositories"
    Schedule }o..|| Person : "owner"
```

### Session

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Session {
        SessionId id PK
        WorkstreamId workstream_id FK
        string stage
        int attempt
        SessionState state
        AgentRef agent
        WorkstreamVmId vm_id FK "optional"
        WorkerId worker_id FK "optional"
        PersonId held_by FK "optional"
        datetime started_at "optional"
        datetime ended_at "optional"
        SessionOutcome outcome "optional"
        string statement "optional"
        int turns "optional"
        Usage usage "optional"
    }
    AgentRef {
        string harness
        string model
    }
    Usage {
        int input_tokens "optional"
        int output_tokens "optional"
        int cache_read_tokens "optional"
        int cache_write_tokens "optional"
        float cost_usd "optional"
    }
    Session }o..|| Workstream : "workstream_id"
    Session ||--|| AgentRef : "agent"
    Session }o..o| WorkstreamVm : "vm_id"
    Session }o..o| Worker : "worker_id"
    Session }o..o| Person : "held_by"
    Session ||--o| Usage : "usage"
```

### WorkstreamVm

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    WorkstreamVm {
        WorkstreamVmId id PK
        WorkstreamId workstream_id FK
        WorkerId worker_id FK
        ImageRef image
        VmState state
        CapabilityManifest declared
        CapabilityManifest granted "optional"
        datetime removed_at "optional"
    }
    ImageRef {
        string name
        string digest
    }
    CapabilityManifest {
        NetworkRule[] network
        ResourceMount[] mounts
        bool kvm "optional"
        Resources resources
    }
    NetworkRule {
        string host
        int[] ports
    }
    ResourceMount {
        string resource
        string path
    }
    Resources {
        int cpus
        int memory_mib
        int disk_gib
        int gpus "optional"
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

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Worker {
        WorkerId id PK
        string name
        WorkerState state
        WorkerOffer offer
        datetime last_seen_at "optional"
    }
    WorkerOffer {
        Resources capacity
        bool kvm "optional"
        string[] resources
    }
    Resources {
        int cpus
        int memory_mib
        int disk_gib
        int gpus "optional"
    }
    Worker ||--|| WorkerOffer : "offer"
    WorkerOffer ||--|| Resources : "capacity"
```

### Convo

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    Convo {
        ConvoId id PK
        ProjectId project_id FK
        ConvoKind kind
        string system
        string external_id
        string url "optional"
        string title "optional"
        PullRequestInfo pull_request "optional"
    }
    PullRequestInfo {
        RepositoryId repository_id FK
        int number
        string head_branch
        string head_sha
        PullRequestState state
        string merged_sha "optional"
    }
    Convo }o..|| Project : "project_id"
    Convo ||--o| PullRequestInfo : "pull_request"
    PullRequestInfo }o..|| Repository : "repository_id"
```

### ConvoLink

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    ConvoLink {
        ConvoLinkId id PK
        ConvoId convo_id FK
        WorkstreamId workstream_id FK
        ConvoLinkReason reason
        PersonActor_ComponentActor_SessionActor_ExternalActor linked_by
        datetime unlinked_at "optional"
    }
    PersonActor {
        const kind "optional"
        PersonId person_id FK
    }
    ComponentActor {
        const kind "optional"
        Component component
    }
    SessionActor {
        const kind "optional"
        SessionId session_id FK
    }
    ExternalActor {
        const kind "optional"
        string system
        string subject
        string handle "optional"
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

Solid lines embed a value object; dashed lines refer to another record by ID.

```mermaid
erDiagram
    ConvoEvent {
        ConvoEventId id PK
        ConvoId convo_id FK
        datetime at
        ConvoEventKind kind
        PersonActor_ComponentActor_SessionActor_ExternalActor author
        string external_id
        string body "optional"
        string url "optional"
        ConvoEventId in_reply_to FK "optional"
    }
    PersonActor {
        const kind "optional"
        PersonId person_id FK
    }
    ComponentActor {
        const kind "optional"
        Component component
    }
    SessionActor {
        const kind "optional"
        SessionId session_id FK
    }
    ExternalActor {
        const kind "optional"
        string system
        string subject
        string handle "optional"
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
