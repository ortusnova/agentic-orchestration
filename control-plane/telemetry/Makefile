# Test and lint the telemetry store, the compose project ortus-telemetry
# (README.md). The control plane's test and lint run test and lint here;
# CI runs live-test.

SHELL := bash

.PHONY: help test lint live-test pins

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'

test: ## Check the configuration, the mask, init and the retention, without podman
	./test-config.sh

lint: ## Shellcheck the scripts and compile the retention
	shellcheck -x telemetry.sh test-config.sh test-live.sh
	python3 -c 'import ast, sys; ast.parse(open("retention.py").read())'

live-test: ## Start the project under rootless podman, send, read, back up, restore and read again, then remove it
	./test-live.sh

pins: ## List each image of the compose project and whether it is pinned by digest
	COMPOSE_FILE=compose.yaml ../pin-images.sh --check
