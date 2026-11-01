## ADDED Requirements

### Requirement: A delivered story reads as done
The workbench SHALL look up each pull request a story names, on GitHub, with a token from its environment. When every one is merged, the story SHALL read as Done, with a link to each pull request and its merge commit, wherever and however the work was done. It SHALL count as done for its epic's progress and as approved and done for the release gate, so a story that depends on it can be released. It SHALL offer no Approve, and a sync SHALL NOT release it. When a pull request is open, closed without merging, or cannot be looked up, the story SHALL read "Delivery not confirmed", with the pull request and the reason, and SHALL count as not done. The workbench SHALL look a pull request up again at least every ten minutes until it is merged, and never again after.

#### Scenario: Merged by hand
- **WHEN** `homelab-image` names `ortusnova/homelab#85`, which is merged
- **THEN** its node reads Done, links to `ortusnova/homelab#85`, and offers no Approve
- **AND** a sync does not release it

#### Scenario: A dependent is released
- **WHEN** `homelab-pilot` is approved and depends on `taichi-instance` and `smolvm-machine-shape`
- **AND** `taichi-instance` is delivered and `smolvm-machine-shape` is approved
- **THEN** `homelab-pilot` is ready

#### Scenario: Not merged yet
- **WHEN** a story names a pull request that is still open
- **THEN** it reads "Delivery not confirmed", naming the pull request and that it is open
- **AND** it reads Done within ten minutes of the merge, without a reload
