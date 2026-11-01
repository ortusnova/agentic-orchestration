## Why

A person approves a story, then has to open **Sync…**, read a preview and
confirm it before the story reaches the tracker and an agent. The confirmation
guards nothing the approval does not already guard:

- a sync writes only approved stories, and skips drafts;
- it never deletes an issue, never reassigns one, and changes an issue's state
  only to release it (`sortie/plan-sync`, "What a sync never does");
- an edit to a released story withdraws its approval and pulls it back
  (`planning/release-gate`).

So the one consequential effect of a sync, an agent starting, is decided by
**Approve**. The dialog asks the same question twice and adds minutes of delay.
Then the released story waits for Sortie's next poll.

## What Changes

- **The approved plan reaches the tracker on its own.** About a second after
  an approval, a pull-back, a cancel, a supersede, or a change to the spec
  store that alters the validated graph, the workbench syncs the project
  through Sortie's preview and apply, with no confirmation. One sync runs at a
  time per project, and changes during a run cause exactly one more.
- **A release starts work at once.** After a sync that released a story, the
  workbench asks Sortie to poll now (`POST /api/v1/refresh`), so dispatch
  does not wait for the poll interval.
- **The Sync button and dialog go away, and nothing replaces them.** Syncing
  is invisible (operator, 2026-11-01): no sync status, no sync time, and an
  unapproved story reads "Not approved". Only when the tracker could not be
  updated does the top bar say so, with the reason, the next try and a
  retry.
- **Failures are visible and retried.** An invalid plan, an unreachable
  Sortie, a tracker without a needed capability, or a partial failure stops
  the sync and shows why. A failed sync is retried with backoff, and again on
  the next change.
- **BREAKING (workbench UI):** there is no preview step. Sortie's
  preview-and-apply API is unchanged; the workbench calls both steps itself.

## Capabilities

### New Capabilities

### Modified Capabilities

- `planning/plan-workbench`: "Sync with preview" is replaced by "The approved
  plan syncs on its own" and "The tracker is never shown out of date".

## Impact

- `workbench/`: a per-project sync loop in the backend (`sync.py`, `hub.py`),
  a sync-status endpoint and event, the top bar and the removal of
  `SyncDialog.svelte`, `dev/live` and the operator guide.
- Sortie: none. It already has preview, apply and `POST /api/v1/refresh`.
