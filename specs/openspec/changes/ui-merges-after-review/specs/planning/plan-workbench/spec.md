## MODIFIED Requirements

### Requirement: A ui story shows its preview
A started ui story SHALL show its state on the way to Sortie's merge, until it is done: "Building" while its build runs; "In review" while its review, the review on its preview or a fix run of the review rule runs, or when its build is over and its review is next; "Waiting for click-proofs" once its review passed and Sortie's merge waits for the click-proofs on its preview; and "Merging" once its pull request records the click-proofs for its head. Once its preview is up it SHALL show the preview's address and its pull request. It SHALL never read as waiting for the operator, and SHALL offer nothing to approve. `workbench preview-sync` SHALL deploy a ui story's pull request only once the story's run is over, its review passed, and CI passed on the head; SHALL comment the preview's address and commit on the pull request once for each head it deploys; SHALL deploy each new head of the branch once its run is over and CI passes on it, keeping the last one up until then; SHALL record whether the pull request records the click-proofs (`sortie:click-proofs passed <sha>`) for its head, a record for another commit not counting; and SHALL remove the preview when the pull request closes, merged or not. A preview SHALL be read-only toward the tracker.

#### Scenario: Waiting for click-proofs
- **WHEN** `worker-activity-view`'s review passed and CI passes on its pull request
- **THEN** its preview is deployed, its pull request gets a comment with the address and the commit, and the workbench shows it as "Waiting for click-proofs" with that address and nothing to approve

#### Scenario: Merging
- **WHEN** the review on the preview comments `sortie:click-proofs passed <head sha>` on the pull request
- **THEN** the workbench shows the story as "Merging", and Sortie merges it

#### Scenario: The preview follows the branch
- **WHEN** a fix run adds a commit to the branch
- **THEN** the preview stays on the last head until CI passes on the new one, then shows the new one and comments on the pull request again, and a click-proofs record for the earlier commit no longer counts

#### Scenario: Merged
- **WHEN** Sortie merges the pull request
- **THEN** the preview is removed and the story is done
