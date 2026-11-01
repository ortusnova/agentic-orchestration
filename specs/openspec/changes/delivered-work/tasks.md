## 1. planlib

- [x] 1.1 Declare `delivered_by` in the breakdown schema, the store template and the test fixture; refuse any other form, naming the deliverable and the value.
- [x] 1.2 Leave `delivered_by` out of the story digest; carry it beside the graph for the workbench.

## 2. Workbench

- [x] 2.1 Look up each named pull request on GitHub with the configured token, every recheck until merged, never after; keep merges in the registry. Test against a fake GitHub.
- [x] 2.2 Read Done or "Delivery not confirmed"; count Done for the epic's progress and the release gate; no Approve; no sync creates or releases it.
- [x] 2.3 Show each pull request and its merge commit, and the reason when one does not confirm the delivery.

## 3. Kit and proof

- [x] 3.1 The stage-0 workbench gets the GitHub token Sortie has.
- [ ] 3.2 Live on p1: with `homelab-image` naming `ortusnova/homelab#85`, the workbench shows it Done, linked, with no Approve.
