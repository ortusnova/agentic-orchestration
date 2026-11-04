## 1. Workbench

- [x] 1.1 Show a started ui story as Building, In review, Waiting for click-proofs or Merging, from Sortie's runs and merge watch and the previews' record, with its preview's address and nothing to approve. Test against the fake Sortie and in the frontend.
- [x] 1.2 `workbench preview-sync`: comment the address and commit once per deployed head, and record whether the pull request records the click-proofs for its head. Test against the fake Sortie and a fake GitHub.
- [x] 1.3 The planning prompt and the store's instructions: a ui story names its approved prototype; a UI change with none goes to prototyping first and the decision board.

## 2. Kit and proof

- [x] 2.1 The workflow's review on the preview, started by the kit's comment, asks the reviewer to click every action the criteria name on the preview and to comment `sortie:click-proofs passed <head sha>`; Sortie merges after it. Checked against Sortie (`test-review-gate.sh`).
- [ ] 2.2 Live: the next UI story merges by itself after its click-proofs record, with no person involved.

## 3. The review machine reaches the preview (ONS-125, design D12)

- [x] 3.1 The preview's address is a name under Pomerium's public URL, `http://<identifier>.preview.<host>/` (`workbench/src/workbench/preview.py` `preview_address`), never `http://127.0.0.1:<port>/`, with or without `STAGE0_WORKBENCH_URL`; a preview recorded at an older address moves to it. Tested in `workbench/tests/workbench/test_preview.py`.
- [x] 3.2 The route: the worker's egress forwarder sends a machine's plain `http://` request as a `CONNECT` on the egress flow with the job's token and identifier; Pomerium's egress route takes it to the egress gate, which admits it by the live job token, serves it from the preview's loopback port (`previews/front-routes`, from `preview.sh`) and never from nono, gives a review job the previews' review key, and records each refusal with the worker, job and host. Tested in `worker-daemon/internal/flows/forward_test.go`, `control-plane/internal/egress/preview_test.go`, `dev/stage0/test-front.sh` (the rendered Pomerium configuration, a revoked worker, the real gate with no token and an ended one) and `dev/stage0/test-preview.sh`.
- [x] 3.2a A revoked worker stays refused by Pomerium on every route and never reaches the gate (operator, 2026-11-04): its refusal is recorded in Pomerium's journal, whose `authorize_log_fields` and `access_log_fields` `cpctl` renders with the host, the job (`headers.X-Sortie-Job`) and the client certificate. Tested in `control-plane/internal/pomerium/pomerium_test.go` and `dev/stage0/test-front.sh`. Evidence from the real Pomerium v0.33.6 with `cpctl`'s configuration, taichi revoked, a `CONNECT` for `ons-7.preview.cp.localtest:80` naming job `ONS-7` (the stand-in gate saw only p1's):
  - `{"service":"authorize","request-id":"b4355305-…","method":"CONNECT","host":"ons-7.preview.cp.localtest:80","headers":{"X-Sortie-Job":"ONS-7"},"allow":false,"allow-why-false":["client-certificate-unauthorized"],"message":"authorize check"}`
  - `{"service":"envoy","method":"CONNECT","authority":"ons-7.preview.cp.localtest:80","request-id":"b4355305-…","response-code":403,"response-code-details":"ext_authz_denied","client-certificate":{"issuer":"CN=Ortus Nova control plane CA","subject":"CN=taichi,OU=worker"},"headers":{"x-sortie-job":"ONS-7"},"message":"http-request"}`
- [x] 3.3 `preview-sync` comments a preview's address only once it answers on the route (`FrontChecker`: TLS to Pomerium with p1's worker certificate, `GET /` and `GET /api/auth/mode` inside the tunnel), and deploys one that does not answer again. Tested in `workbench/tests/workbench/test_preview.py` and `test_preview_front.py`.
- [x] 3.4 The review-on-preview prompt opens the address with the machine's proxy settings, and takes an address that does not answer as a platform fault to stop `blocked` on, with the address and the error, never a request to a person to redeploy. Tested in `dev/stage0/workflow/workflow_test.go`.
- [ ] 3.5 Live: the review run of a UI story on p1's Sortie opens its preview at the commented address from its own machine and runs the click-proofs there, with no person involved. Evidence, recorded once this change is deployed on p1:
  - the preview comment: _pending_
  - the click-proofs comment, or the failing verdict it records: _pending_
  - the gate journal line of the run's requests (`journalctl --user -u ortus-stage0-egress-gate | grep 'msg="preview request"'`): _pending_
