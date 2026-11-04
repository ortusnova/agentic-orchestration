## Why

The Ortus Nova spec store's `agent-platform/run-supervision` now says "A UI
is approved as a prototype, and the built story merges after its review":
the operator approves a UI on its design prototype before a story is
written, the story names that prototype in its acceptance criteria, and
Sortie merges its pull request once its criteria review, CI and the
click-proofs on its preview pass, with no further approval by a person.
`ui-review` had the operator look at the preview and merge it. Sortie's part
(auto-merge waits for a `sortie:click-proofs passed <head sha>` comment
naming the pull request's head, and asks for it on the story) merged as
sortie#42 and is in the `sortie` submodule.

## What Changes

- The workbench no longer shows a built ui story as "Ready for you to look"
  nor offers anything to approve on it. It shows its state on the way to
  Sortie's merge, from Sortie's runs and merge watch and the previews'
  record: Building, In review, Waiting for click-proofs, Merging, with its
  preview's address once it is up.
- `workbench preview-sync` comments the preview's address and commit on the
  pull request for each head it deploys, and keeps in the record whether the
  pull request records the click-proofs for its head.
- The stage-0 workflow sends the kit's preview comment to a review on the
  preview: its prompt asks the reviewer to click every action the story's
  criteria name on the preview and, when they pass, to comment
  `sortie:click-proofs passed <head sha>` on the pull request. The review
  prompt of a ui story says its click-proofs come after it, and the fix
  prompt no longer says the operator merges.
- The review machine reaches the preview through the control plane's front
  (design D12, ONS-125): the address the kit comments is a name under
  Pomerium's public URL, never p1's loopback; a job's machine on any worker
  opens it through its proxy, on its egress route, with its job token, and
  the egress gate serves it from the preview's loopback port. The kit
  comments it only once the preview answers there, and redeploys one that
  does not.
- The planning session and the store's instructions write a ui story only
  for an approved prototype, named in its criteria; a UI change with none
  goes to prototyping first, and the operator is asked on the decision board.

## Capabilities

### Modified Capabilities

- `planning/review-gate`: "A ui story is built before it is approved" now has
  the prototype as its approval and Sortie's merge after the click-proofs.
- `planning/plan-workbench`: "A ui story shows its preview" now shows its
  state and its preview's address, with nothing to approve.

## Impact

- `workbench/src/workbench`: the plan overlay, the hub, the fleet helpers, the home, the story routes, `preview.py`; the frontend's status labels, card and context card; the store template's schemas and instructions, and the planning prompt.
- `dev/stage0`: `WORKFLOW.md` (no triage on `review_comments`, the click-proofs prompt), `sortie-test/stage0_ui_test.go`, the README; the `sortie` submodule moves to a `plan-bench` holding sortie#42.
