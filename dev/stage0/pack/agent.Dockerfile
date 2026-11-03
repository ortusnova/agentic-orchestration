ARG BASE
FROM ${BASE}
# Claude Code behind ACP, agent-acp and both harnesses' policy files, for root
# (install-harness.sh, shared with the homelab pack).
COPY install-harness.sh install-opencode-plugins.sh agent-acp claude-settings.json opencode.json /opt/stage0-harness/
RUN /opt/stage0-harness/install-harness.sh root /root
# The linters `make lint` runs in Go repositories such as ortusnova/sortie.
ARG GOLANGCI_LINT_VERSION=2.14.0
ARG GOLANGCI_LINT_SHA256=ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab
RUN curl -fsSLo /tmp/gl.tgz "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64.tar.gz" \
 && echo "${GOLANGCI_LINT_SHA256}  /tmp/gl.tgz" | sha256sum -c - \
 && tar -xzf /tmp/gl.tgz -C /usr/local/bin --strip-components=1 "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64/golangci-lint" \
 && rm /tmp/gl.tgz \
 && apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends shellcheck && rm -rf /var/lib/apt/lists/* \
 && golangci-lint --version && shellcheck --version
# Claude Code accepts bypassPermissions as root only with IS_SANDBOX=1 (the worker image's env, worker-config.sh).
