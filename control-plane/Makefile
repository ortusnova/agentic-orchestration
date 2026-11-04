# Build, test and lint the control plane: its tools (cpctl, the egress gate,
# cp-probe), the compose project and its images. The root Makefile's test and
# lint run these too.

SHELL := bash

.PHONY: help build test lint check-latest images live-test pins

# The commit the images are built from, and their tag.
TAG ?= $(shell git rev-parse HEAD)
ROOT := $(abspath ..)
IMAGES := sortie acp-broker workbench control-plane

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'

build: ## Build bin/cpctl, bin/cp-egress and bin/cp-probe for this host
	CGO_ENABLED=0 go build -o bin/ ./cmd/cpctl ./cmd/cp-egress ./cmd/cp-probe

test: ## Run the tests (the live scenarios skip; live-test runs them)
	go test -race -count=1 $(if $(RUN),-run '$(RUN)') ./...

lint: ## Lint the Go code and shellcheck the scripts
	golangci-lint run ./...
	shellcheck -x cp.sh pin-images.sh livetest/run.sh

pins: ## List each upstream image of the compose project and whether it is pinned by digest
	./pin-images.sh --check

check-latest: ## Fail when Pomerium or nono has a stable release newer than the compose project's (network)
	@set -e; for pair in pomerium/pomerium:docker.io/pomerium/pomerium nolabs-ai/nono:ghcr.io/nolabs-ai/nono; do \
	  repo=$${pair%%:*}; image=$${pair#*:}; \
	  latest=$$(curl -fsSL "https://api.github.com/repos/$$repo/releases/latest" | sed -n 's/^ *"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p'); \
	  [[ -n $$latest ]] || { echo "could not read $$repo's latest release" >&2; exit 1; }; \
	  pinned=$$(sed -n "s#^ *image: *$$image:v\{0,1\}\([^@ ]*\).*#\1#p" compose.yaml); \
	  if [[ $$latest != "$$pinned" ]]; then echo "$$repo $$latest is out; compose.yaml runs $$pinned" >&2; exit 1; fi; \
	  echo "$$repo: $$pinned is the latest release"; \
	done

images: ## Build the images built here, tagged with the commit (TAG), with podman
	@set -e; for img in $(IMAGES); do \
	  echo "== localhost/ortus-nova/$$img:$(TAG)"; \
	  podman build -f images/$$img.Dockerfile --build-arg REVISION=$(TAG) -t localhost/ortus-nova/$$img:$(TAG) $(ROOT); \
	done

live-test: ## Start the compose project under rootless podman with test certificates and fake credentials, run every scenario, stop it
	CP_TAG=$(TAG) ./livetest/run.sh
