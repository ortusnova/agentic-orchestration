# Build, test, and lint every component of this repository. Each component
# also has its own Makefile, with build and release targets.

GO_MODULES := acp-broker smol-builder worker-daemon
COMPONENTS := workbench $(GO_MODULES)

.PHONY: help test lint check-latest licenses no-placeholder specs dist

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'

test: ## Run every component's tests, and the stage-0 kit's
	@set -e; for c in $(COMPONENTS); do echo "== $$c: test"; $(MAKE) -s -C $$c test; done
	@echo "== dev/stage0: test"; $(MAKE) -s -C dev/stage0 test

lint: licenses no-placeholder specs ## Lint every component, check the license copies and placeholders, and validate the specs
	@set -e; for c in $(COMPONENTS); do echo "== $$c: lint"; $(MAKE) -s -C $$c lint; done
	@echo "== dev/stage0: lint"; $(MAKE) -s -C dev/stage0 lint

check-latest: ## Fail when a Go component's dependency, or the kit's nono, is behind its latest stable release (network)
	@set -e; for m in $(GO_MODULES); do echo "== $$m: check-latest"; $(MAKE) -s -C $$m check-latest; done
	@echo "== dev/stage0: check-latest"; $(MAKE) -s -C dev/stage0 check-latest

# smolvm rejects every request that carries its credential-placeholder prefix,
# so an agent that reads it in this repository breaks its own session. The
# needle is split so that this file does not spell it either.
PLACEHOLDER_NEEDLE := SMOL_$(strip )PLACEHOLDER_

no-placeholder: ## Fail if a tracked file spells smolvm's credential-placeholder prefix
	@if git grep -n -F "$(PLACEHOLDER_NEEDLE)"; then echo "a tracked file spells smolvm's credential-placeholder prefix; write <smolvm placeholder> instead" >&2; exit 1; fi; echo "no-placeholder: ok"

licenses: ## Check that each component ships the repository's LICENSE unchanged
	@set -e; for c in $(COMPONENTS); do cmp -s LICENSE $$c/LICENSE || { echo "$$c/LICENSE differs from LICENSE"; exit 1; }; done; echo "licenses: ok"

specs: ## Validate the OpenSpec store in specs/
	@cd specs && openspec validate --all --no-interactive > /dev/null && echo "specs: ok"

dist: ## Build the release artifacts: Go archives and the workbench wheel
	@set -e; for m in $(GO_MODULES); do $(MAKE) -s -C $$m dist; done; $(MAKE) -s -C workbench wheel
