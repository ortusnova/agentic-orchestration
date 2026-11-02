# Test and lint the stage-0 kit's scripts. The root Makefile's test and lint
# run these too.

SHELL := bash

.PHONY: help test lint

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'

KIT_TESTS := $(wildcard test-*.sh)

test: ## Run the kit's script tests
	@set -e; for t in $(KIT_TESTS); do echo "== $$t"; ./$$t; done

lint: ## Shellcheck the kit's scripts
	shellcheck -x *.sh
