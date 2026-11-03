## MODIFIED Requirements

### Requirement: A ui story is built before it is approved
A deliverable's `review` MAY be `ui`. A ui story SHALL be one whose interface the operator approved as a design prototype before the story was written, and its acceptance criteria SHALL name that approved prototype. A ui story that covers a requirement SHALL need no further approval: the workbench SHALL offer no Approve for it, alone or in its epic's batch, and SHALL refuse one. It SHALL count as approved for the stories that depend on it, and a sync SHALL send it with no approver, so that Sortie releases it. A ui story that covers no requirement SHALL need a spec, like any other story. Its pull request SHALL merge, by Sortie, once its criteria review, CI and the click-proofs on its preview pass, with no approval by a person. A UI change that has no approved prototype SHALL go to prototyping first, and the operator SHALL be asked on the decision board, not on a built pull request.

#### Scenario: No Approve
- **WHEN** `worker-activity-view` carries `review: ui` and covers a requirement
- **THEN** it reads ready with no Approve, an approval of it is refused, and a sync releases it with no `approved_by`

#### Scenario: Its dependent
- **WHEN** an approved story depends only on a ui story that covers a requirement
- **THEN** the dependent is ready

#### Scenario: A built UI story
- **WHEN** a ui story built from an approved prototype passes its review, CI and click-proofs
- **THEN** Sortie merges it, and the operator is not asked to approve it

#### Scenario: No prototype
- **WHEN** a planning session would write a story that changes an interface and names no approved prototype
- **THEN** it does not write it as a ui story: the change goes to prototyping first, and the operator is asked on the decision board
