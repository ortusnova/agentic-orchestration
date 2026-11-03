## 1. planlib

- [x] 1.1 Accept `review: ui` in the breakdown and plan graph schemas, the store template and the test fixtures; the plan graph schema equals Sortie's.

## 2. Workbench

- [x] 2.1 No Approve for a ui story, alone or in its epic's batch; read it as approved for its dependents once it covers a requirement; sync it with no approver. Test against the fake Sortie.
- [x] 2.2 Read "Building", then "Ready for you to look" with the preview's address and pull request, from the previews' record.
- [x] 2.3 `workbench preview-sync`: deploy a reviewed, green head, comment its address once, follow the branch, remove the preview when the pull request closes. Test against the fake Sortie and a fake GitHub.
- [x] 2.4 A read-only workbench pushes no schema upgrade.

## 3. Kit and proof

- [x] 3.1 The workflow routes `sortie-review:ui` through the review, never auto-merges it, and starts a fix run on an operator comment; checked against Sortie (`test-review-gate.sh`).
- [x] 3.2 `preview.sh`, `run-previews.sh` and the units; checked by `test-preview.sh`.
- [ ] 3.3 Live on p1: with `worker-activity-view` on `review: ui`, its preview is up at its address, linked from the pull request and the workbench; a comment adds a commit the preview shows; the merge makes it Done. Superseded: the operator no longer merges a ui story, and its live proof is `ui-merges-after-review` 2.2.
