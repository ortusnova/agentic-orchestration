## ADDED Requirements

### Requirement: A ui story shows its preview
A started ui story SHALL read "Building" until its pull request's preview is up, and then "Ready for you to look", with the preview's address and its pull request, until it is done. `workbench preview-sync` SHALL deploy a ui story's pull request only once the story's run is over, its review passed, and CI passed on the head; SHALL comment the preview's address on the pull request once; SHALL deploy each new head of the branch once its run is over and CI passes on it, keeping the last one up until then; and SHALL remove the preview when the pull request closes, merged or not. A preview SHALL be read-only toward the tracker.

#### Scenario: Ready for you to look
- **WHEN** `worker-activity-view`'s review passed and CI passes on its pull request
- **THEN** its preview is deployed, its pull request gets a comment with the address, and the workbench shows it as "Ready for you to look" with that address

#### Scenario: The preview follows the branch
- **WHEN** a fix run adds a commit to the branch
- **THEN** the preview stays on the last head until CI passes on the new one, and then shows the new one

#### Scenario: Merged
- **WHEN** the operator merges the pull request
- **THEN** the preview is removed and the story is done
