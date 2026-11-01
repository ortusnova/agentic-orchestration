.PHONY: help build cross test lint check-latest

NAME := worker-daemon

# The platforms the daemon ships for.
PLATFORMS ?= linux/amd64 linux/arm64 darwin/arm64
VERSION ?= $(shell git describe --tags --match '$(NAME)/v*' --always --dirty 2>/dev/null | sed 's#^$(NAME)/##')
LDFLAGS := -s -w -X main.version=$(VERSION)

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'

build: ## Build bin/worker-daemon for this host
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o bin/$(NAME) ./cmd/$(NAME)

cross: ## Build one binary per platform into bin/, each named NAME_OS_ARCH
	@set -e; for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags='$(LDFLAGS)' -o bin/$(NAME)_$${os}_$${arch} ./cmd/$(NAME); \
	  echo "bin/$(NAME)_$${os}_$${arch}"; \
	done

test: ## Run the tests with the race detector (RUN=TestFoo to pick tests)
	go test -race -count=1 $(if $(RUN),-run '$(RUN)') ./...

check-latest: ## Fail when a Go module is behind its latest release (network)
	WORKER_DAEMON_LATEST_TEST=1 go test -count=1 -run '^TestLatest' -v ./internal/latest/

lint: ## Lint for Linux and macOS
	@for os in linux darwin; do echo "golangci-lint: GOOS=$$os"; GOOS=$$os golangci-lint run ./... || exit 1; done

.PHONY: dist
dist: ## Build a release archive per platform, with LICENSE and README, and SHA256SUMS, into dist/
	rm -rf dist && mkdir -p dist
	@set -e; for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; dir=$(NAME)_$(VERSION)_$${os}_$${arch}; \
	  mkdir -p dist/$$dir; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags='$(LDFLAGS)' -o dist/$$dir/$(NAME) ./cmd/$(NAME); \
	  cp LICENSE README.md dist/$$dir/; \
	  tar -C dist -czf dist/$$dir.tar.gz $$dir; rm -r dist/$$dir; \
	  echo "dist/$$dir.tar.gz"; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS
