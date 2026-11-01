## 1. Backend

- [x] 1.1 Add a per-project sync task with debounce and one-more-run coalescing (D1); test that three triggers during a run cause exactly one more run.
- [x] 1.2 Trigger it from approve, pull-back, cancel, supersede, and from a store change that alters approved stories (D3); test each trigger and that a draft-only edit does not sync.
- [x] 1.3 Preview then apply in one step; retry once on `stale_preview`; stop on a missing capability or an invalid plan (D2); test each path against the fake Sortie.
- [x] 1.4 Call Sortie's refresh after a sync that released a story (D4); test that a failed refresh leaves the sync successful.
- [x] 1.5 Keep the sync status and last result per project, publish them with the graph, retry failures with backoff of at most 60 seconds, and add a retry route for planners; test the status transitions.

## 2. Frontend

- [x] 2.1 Replace the Sync button and `SyncDialog.svelte` with the sync status in the top bar, and the last result on open; test syncing, synced, failed and retry.
- [x] 2.2 Show nothing about syncing: drafts read "Not approved", and the top bar speaks only when the tracker could not be updated ("The tracker is never shown out of date"); test that syncing, synced and idle show nothing.

## 3. Docs and proof

- [x] 3.1 Update the operator guide and `dev/live` for sync without a dialog.
- [ ] 3.2 Live: approve a story in the stage-0 workbench and see it released, and its worker start, within a few seconds, with no dialog.
