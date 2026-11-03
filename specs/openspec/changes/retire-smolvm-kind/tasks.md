## 1. This repository

- [x] 1.1 The stage-0 kit's workflow runs every repository under the `remote` kind and lists none closed to dispatch; verify with `TestOneSortieDispatchesEveryRepository` and `TestRequirementsNameWhatAJobNeeds` in `dev/stage0/edge`.
- [x] 1.2 The homelab profile's workflow (`WORKFLOW-homelab.md`), its Huly tunnel and its Sortie unit are gone; a worker host runs only the worker daemon; verify with `dev/stage0/test-worker.sh`.
- [x] 1.3 The workflow has no hooks and `environment.remote` only the keys the remote kind takes (no `allow_hosts`, no `pass_env`), and `stage0.sh` fails on a key Sortie calls unknown; verify with `TestTheRemoteKindHasNoHooks` and `TestTheRemoteSectionHasOnlyRemoteKeys` in `dev/stage0/edge`.
- [x] 1.4 A worker host's Sortie stops only once p1's Sortie holds every merge record it holds (`worker-host.sh carry`); verify with `dev/stage0/test-merge-carry.sh`.

## 2. Sortie fork (ortusnova/sortie, a separate pull request)

- [ ] 2.1 Validation refuses `environment.kind: smolvm` with an error naming `environment.kind`, `smolvm` and `remote`; verify with a config test.
- [ ] 2.2 Delete `internal/environment/smolvm`, the tool socket and the `/opt/sortie` binary mount, and the SSH-worker check that only the kind needed; verify `go test ./...` and `make lint`.
- [ ] 2.3 Remove the smolvm guide and the kind from the workflow reference; verify `make lint-docs`.
