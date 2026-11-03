## 1. Workbench

- [x] 1.1 Show a started ui story as Building, In review, Waiting for click-proofs or Merging, from Sortie's runs and merge watch and the previews' record, with its preview's address and nothing to approve. Test against the fake Sortie and in the frontend.
- [x] 1.2 `workbench preview-sync`: comment the address and commit once per deployed head, and record whether the pull request records the click-proofs for its head. Test against the fake Sortie and a fake GitHub.
- [x] 1.3 The planning prompt and the store's instructions: a ui story names its approved prototype; a UI change with none goes to prototyping first and the decision board.

## 2. Kit and proof

- [x] 2.1 The workflow's review on the preview, started by the kit's comment, asks the reviewer to click every action the criteria name on the preview and to comment `sortie:click-proofs passed <head sha>`; Sortie merges after it. Checked against Sortie (`test-review-gate.sh`).
- [ ] 2.2 Live: the next UI story merges by itself after its click-proofs record, with no person involved.
