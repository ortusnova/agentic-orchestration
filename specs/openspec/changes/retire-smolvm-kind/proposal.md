## Why

Stage 1 of the Ortus Nova spec store's `agent-platform-workers` (design D18)
leaves one Sortie, on p1, that dispatches every repository under the
`remote` kind; each job runs on a worker daemon, on p1 or on taichi
(`agent-platform/one-sortie`). Nothing runs under the co-located `smolvm`
kind any more: taichi's homelab Sortie, its last user, is retired. The kind,
its per-issue host socket for the tools and its read-only Sortie binary
mount are code the fork keeps apart from upstream for nothing.

## What Changes

- `environment.kind` accepts `local` and `remote`. **BREAKING**: a workflow
  with `environment.kind: smolvm` fails validation with an error that names
  `environment.kind`, `smolvm` and `remote`.
- "smolvm excludes SSH workers" is removed: there is no `smolvm` kind.
- Every requirement of `sortie/smolvm-environment` is removed, and so are
  the host-socket requirements of `sortie/guest-tool-server`. A remote job's
  machine is the worker daemon's (`agent-platform/worker-daemon`), and its
  tools come over the `mcp` flow (`agent-platform/remote-runs`).
- This supersedes `sortie-http-guest-tools`, whose deltas change the
  requirements removed here.

## Capabilities

### Modified Capabilities

- `sortie/execution-environment`: "Workflow selects an execution environment kind" accepts `local` and `remote` and refuses `smolvm` naming `remote`; "smolvm excludes SSH workers" is removed.
- `sortie/smolvm-environment`: removed.
- `sortie/guest-tool-server`: the host socket and the relay are removed.

## Impact

- Sortie fork (ortusnova/sortie, a separate pull request): `internal/environment/smolvm` and the tool socket are deleted, and config validation refuses the kind; the workflow reference and the smolvm guide follow.
- This repository: the stage-0 kit's workflow uses the `remote` kind (`dev/stage0/WORKFLOW.md`), and the homelab profile's co-located workflow is deleted.
- Operators: a workflow on the `smolvm` kind moves to `remote` and runs its jobs on a worker daemon.
