## REMOVED Requirements

### Requirement: Sync with preview
**Reason**: The confirmation repeats the approval, which is already the person's decision to start work. A sync writes only approved stories, deletes nothing, and changes an issue's state only to release it.
**Migration**: Approving, pulling back, cancelling or superseding a story syncs the plan within about a second. Nothing shows a sync; the top bar says so only when the tracker could not be updated ("The tracker is never shown out of date").

## ADDED Requirements

### Requirement: The approved plan syncs on its own
The workbench SHALL sync a project's plan to the tracker, through Sortie's preview and apply, without asking for confirmation. It SHALL start a sync no more than 2 seconds after any of these: an approval, a pull-back, a cancel, a confirmed supersede, or a change to the project's validated graph. It SHALL run at most one sync of a project at a time. A trigger during a sync SHALL cause exactly one more sync after it. The graph it sends SHALL carry, for each story, its approval, `release`, `review`, `approved_by`, and `approved_at`. While the plan is invalid, the workbench SHALL NOT sync it. When Sortie reports a missing tracker capability, it SHALL NOT apply.

#### Scenario: Approval starts the work
- **WHEN** an approver approves story `billing/b1`, whose blockers are all approved or merged
- **THEN** within 2 seconds the workbench syncs, and `billing/b1` is released
- **AND** no dialog asks the person to confirm

#### Scenario: Several approvals in a row
- **WHEN** an approver approves three stories, one after another, while a sync runs
- **THEN** the workbench runs one more sync after the running one, and that sync carries all three approvals

#### Scenario: An invalid plan
- **WHEN** the planning agent leaves the plan invalid
- **THEN** no sync runs until the plan is valid again
- **AND** the top bar says the tracker is not updated because the plan is invalid

### Requirement: A release asks Sortie to poll at once
After a sync that released at least one story, the workbench SHALL ask Sortie to poll its tracker at once (`POST /api/v1/refresh`, with Sortie's control token). A failure of that request SHALL NOT fail the sync; the story then starts at Sortie's next poll.

#### Scenario: A released story starts without waiting for the poll
- **WHEN** a sync releases `billing/b1`
- **THEN** the workbench calls Sortie's refresh once
- **AND** Sortie dispatches `billing/b1` on that poll, not on the next timed poll

### Requirement: The tracker is never shown out of date
Syncing SHALL be invisible. The workbench SHALL NOT show a sync status, a sync time, or the word "sync" while the tracker matches the plan, or while a sync runs. A story that is not approved SHALL read "Not approved", never "Not synced". When a sync fails, the top bar SHALL say that the tracker is not updated, and why, in words a person acts on; opening it SHALL show the failed write, if any, and when the next try runs. A failed sync SHALL be retried with backoff of at most 60 seconds, and at the next trigger. A person with the `planner` role SHALL be able to retry a failed sync at once. The notice SHALL disappear when a sync succeeds. A read-only workbench SHALL say it is read-only.

#### Scenario: Sortie is unreachable
- **WHEN** a sync fails because Sortie does not answer
- **THEN** the top bar says the tracker is not updated, because Sortie is unreachable
- **AND** the workbench retries within 60 seconds, and the notice disappears once a sync succeeds

#### Scenario: Nothing to say
- **WHEN** the last sync succeeded, or a sync is running, or none has run yet
- **THEN** the top bar shows nothing about the tracker

#### Scenario: A story nobody approved
- **WHEN** `billing/tax` is a draft
- **THEN** its node reads "Not approved"
