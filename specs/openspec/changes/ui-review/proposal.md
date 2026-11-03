## Why

A story whose value is how its screens look and feel cannot be judged from
its spec alone: the operator wants to see it built before saying yes. The
Ortus Nova spec store's `agent-platform/run-supervision` ("A UI story is
built before it is approved, and its merge is the approval", design D8)
adds a third review value for such stories, `ui`; `workbench-platform-faults`,
`operator-alerts`, `stuck-means-waiting` and `worker-activity-view` move to
it while still drafts. Sortie's part (sync keeps `sortie-review:ui`,
releases the story once it covers a requirement with no approver, never
merges it, and an operator's comment on its pull request starts a fix run)
is in the `sortie` submodule.

## What Changes

- A deliverable's `review` may be `ui`. The breakdown and plan graph schemas
  accept it; a store whose schemas predate it gets them through the
  workbench's schema pull request, and is validated against them until it
  merges.
- The workbench offers no Approve for a ui story, and the epic's batch
  leaves it out. Once it covers a requirement it reads as an approved story
  does (ready, held, released), counts as approved for its dependents, and
  syncs as a draft with no approver, which Sortie releases. Started, it
  reads "Building"; once its preview is up, "Ready for you to look", with
  the preview's address and its pull request; merged, it is done.
- `workbench preview-sync` deploys each ui story's pull request whose run is
  over, whose review passed and whose head passed CI, through a deploy
  command, comments the preview's address on the pull request once, deploys
  each new head once it passes CI, and removes the preview when the pull
  request closes. It keeps the record the workbench reads
  (`WORKBENCH_PREVIEWS`).
- A read-only workbench (`WORKBENCH_SYNC=0`) writes and pushes no schema
  upgrade.
- The stage-0 kit runs preview-sync every minute; its `preview.sh` builds
  the branch's workbench and runs it read-only against the live store,
  Sortie and a copy of the live registry, at a name beside the live
  workbench's TLS name. The workflow sends operator comments to a review
  fix run, and passes over the kit's own comment.

## Capabilities

### Modified Capabilities

- `planning/review-gate`: adds "A ui story is built before it is approved".
- `planning/plan-workbench`: adds "A ui story shows its preview".

## Impact

- `workbench/src/planlib` schemas, the store template and the test fixtures; the plan graph schema is Sortie's copy.
- `workbench/src/workbench`: the plan overlay, the sync graph and its digest, approval, the home, the GitHub client, `preview.py`, the CLI, the frontend's status labels and context card.
- `dev/stage0`: `WORKFLOW.md`, `preview.sh`, `run-previews.sh`, `run-workbench.sh`, three units, `stage0.sh`.
