# Test and lint the deploy kit: the environment files in environments/ and
# their loader (envfile/), live-proofs (live-proofs/), the checks on the
# control host's workflow (workflow/) and the kit's scripts (spec store
# deploy-environments). The root Makefile's test and lint run these too.

SHELL := bash

.PHONY: help test test-live lint check-environments check-latest check-forge-rules

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-20s %s\n", $$1, $$2}'

KIT_TESTS := $(wildcard test-*.sh)
LIVE_TESTS := test-egress-live.sh

test: check-environments ## Run the loader's, live-proofs' and the workflow's tests and the kit's script tests
	cd envfile && go test -race -count=1 ./...
	cd live-proofs && go test -race -count=1 ./...
	cd workflow && go test -race -count=1 ./...
	@set -e; for t in $(filter-out $(LIVE_TESTS),$(KIT_TESTS)); do echo "== $$t"; ./$$t; done

check-environments: ## Check every environments/<env>.yml against the schema, and that no two share a port or a local name
	cd envfile && go run . check ../environments

test-live: ## On the control host: the egress scenarios against the pinned nono, api.anthropic.com and mcp.firecrawl.dev, and an attempt's GitHub egress
	./test-egress-live.sh

check-forge-rules: ## Read the rulesets of each repository a forge_ceiling grants write on, and report those that do not bound a minted token, as recommended; fail only on a ruleset the App bypasses (GitHub API, read only)
	./check-forge-rules.py

lint: ## Lint the loader, live-proofs and the workflow's tests, and shellcheck the kit's scripts
	cd envfile && golangci-lint run ./...
	cd live-proofs && golangci-lint run ./...
	cd workflow && golangci-lint run ./...
	shellcheck -x *.sh

check-latest: ## Fail when a module the loader requires, or nono, is behind its latest stable release (network)
	@cd envfile && out=$$(go list -m -u -f '{{if .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' $$(go mod edit -json | jq -r '.Require[].Path') | grep . || true); \
	if [ -n "$$out" ]; then echo "$$out" >&2; exit 1; fi; echo "envfile: every dependency is the latest"
	@. ./nono-version.sh; latest=$$(nono_latest); \
	if [ -z "$$latest" ]; then echo "could not read nono's latest release" >&2; exit 1; fi; \
	if [ "$$latest" != "$$NONO_VERSION" ]; then echo "nono $$latest is out; the kit pins $$NONO_VERSION (nono-version.sh)" >&2; exit 1; fi; \
	echo "nono: $$NONO_VERSION is the latest release"
