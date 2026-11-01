## Why

Some stories are built outside an agent run: by hand, or by a Sortie that
another workbench does not watch. The Ortus Nova spec store's
`run-homelab-through-sortie/homelab-image` was merged by hand as
`ortusnova/homelab#85`, yet the workbench shows it as a draft to approve, and
its dependents wait on it forever. The plan has to say which pull request
delivered a story, and the workbench has to read Done from it
(the spec store's `agent-platform/delivered-work`).

## What Changes

- A deliverable may name `delivered_by`: one or more pull requests, each
  `owner/repo#number`. The breakdown schema declares it; any other form fails
  validation, naming the deliverable and the value. It is not part of what an
  approval covers, so naming the pull request neither returns a story to
  draft nor counts as an edit to a started one.
- The workbench looks up each named pull request on GitHub with
  `WORKBENCH_GITHUB_TOKEN`, again every five minutes until it is merged, and
  never again after; the registry keeps each merge.
- When every one is merged, the story reads Done with a link to each pull
  request and its merge commit. It counts as done for its epic and as
  approved and done for the release gate. It offers no Approve, and a sync
  neither creates nor releases it, and sends no relation from it.
- Otherwise it reads "Delivery not confirmed", with each pull request and
  why, and counts as not done.
- The stage-0 kit hands the workbench the GitHub token Sortie already has.

## Capabilities

### Modified Capabilities

- `planning/breakdown-artifact`: adds "A story names the pull request that delivered it".
- `planning/plan-workbench`: adds "A delivered story reads as done".

## Impact

- `workbench/src/planlib` (schema message, approval digest, the stories beside the graph), the breakdown schema the workbench ships and its test fixture.
- `workbench/src/workbench`: a GitHub client and a per-project delivery watch, the plan overlay, the sync graph, the home's progress, approval, the registry (`merged_pulls`), and the frontend's node status and context card.
- `dev/stage0/run-workbench.sh`.
