## 1. This repository

- [x] 1.1 The stage-0 kit's workflow runs every repository under the `remote` kind and lists none closed to dispatch; verify with `TestOneSortieDispatchesEveryRepository` and `TestRequirementsNameWhatAJobNeeds` in `dev/stage0/edge`.
- [x] 1.2 The homelab profile's workflow (`WORKFLOW-homelab.md`), its Huly tunnel and its Sortie unit are gone; a worker host runs only the worker daemon; verify with `dev/stage0/test-worker.sh`.

## 2. Sortie fork (ortusnova/sortie, a separate pull request)

- [ ] 2.1 Validation refuses `environment.kind: smolvm` with an error naming `environment.kind`, `smolvm` and `remote`; verify with a config test.
- [ ] 2.2 Delete `internal/environment/smolvm`, the tool socket and the `/opt/sortie` binary mount, and the SSH-worker check that only the kind needed; verify `go test ./...` and `make lint`.
- [ ] 2.3 Remove the smolvm guide and the kind from the workflow reference; verify `make lint-docs`.
