# Test and lint the stage-0 kit: stage0-live-proofs (live-proofs/), the
# checks on p1's workflow (workflow/) and the kit's scripts. The root
# Makefile's test and lint run these too.

SHELL := bash

.PHONY: help test test-live lint check-latest

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'

KIT_TESTS := $(wildcard test-*.sh)
LIVE_TESTS := test-egress-live.sh

test: ## Run stage0-live-proofs' and the workflow's tests and the kit's script tests
	cd live-proofs && go test -race -count=1 ./...
	cd workflow && go test -race -count=1 ./...
	@set -e; for t in $(filter-out $(LIVE_TESTS),$(KIT_TESTS)); do echo "== $$t"; ./$$t; done

test-live: ## On p1: the egress scenarios against the pinned nono, api.anthropic.com and mcp.firecrawl.dev
	./test-egress-live.sh

lint: ## Lint stage0-live-proofs and the workflow's tests, and shellcheck the kit's scripts
	cd live-proofs && golangci-lint run ./...
	cd workflow && golangci-lint run ./...
	shellcheck -x *.sh

check-latest: ## Fail when nono has a stable release newer than the kit's pin (network)
	@. ./nono-version.sh; latest=$$(nono_latest); \
	if [ -z "$$latest" ]; then echo "could not read nono's latest release" >&2; exit 1; fi; \
	if [ "$$latest" != "$$NONO_VERSION" ]; then echo "nono $$latest is out; the kit pins $$NONO_VERSION (nono-version.sh)" >&2; exit 1; fi; \
	echo "nono: $$NONO_VERSION is the latest release"
