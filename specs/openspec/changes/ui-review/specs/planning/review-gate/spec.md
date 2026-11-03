## ADDED Requirements

### Requirement: A ui story is built before it is approved
A deliverable's `review` MAY be `ui`. A ui story that covers a requirement SHALL need no approval: the workbench SHALL offer no Approve for it, alone or in its epic's batch, and SHALL refuse one. It SHALL count as approved for the stories that depend on it, and a sync SHALL send it with no approver, so that Sortie releases it. A ui story that covers no requirement SHALL need a spec, like any other story. The operator's merge of its pull request SHALL be its approval.

#### Scenario: No Approve
- **WHEN** `worker-activity-view` carries `review: ui` and covers a requirement
- **THEN** it reads ready with no Approve, an approval of it is refused, and a sync releases it with no `approved_by`

#### Scenario: Its dependent
- **WHEN** an approved story depends only on a ui story that covers a requirement
- **THEN** the dependent is ready
