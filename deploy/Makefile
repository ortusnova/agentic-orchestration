# Test and lint the deploy kit: the environment files in environments/ and
# their loader, envfile/ (spec store deploy-environments). The root
# Makefile's test and lint run these too.

SHELL := bash

.PHONY: help test lint check-environments check-latest

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-20s %s\n", $$1, $$2}'

test: check-environments ## Run the loader's tests and check every environment file
	cd envfile && go test -race -count=1 ./...

check-environments: ## Check every environments/<env>.yml against the schema, and that no two share a port or a local name
	cd envfile && go run . check ../environments

lint: ## Lint the loader
	cd envfile && golangci-lint run ./...

check-latest: ## Fail when a module the loader requires is behind its latest stable release (network)
	@cd envfile && out=$$(go list -m -u -f '{{if .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' $$(go mod edit -json | jq -r '.Require[].Path') | grep . || true); \
	if [ -n "$$out" ]; then echo "$$out" >&2; exit 1; fi; echo "envfile: every dependency is the latest"
