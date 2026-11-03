ARG BASE
FROM ${BASE}
# Claude Code behind ACP, agent-acp and both harnesses' policy files, for root
# (install-harness.sh, shared with the homelab pack).
COPY install-harness.sh install-opencode-plugins.sh agent-acp claude-settings.json opencode.json /opt/stage0-harness/
RUN /opt/stage0-harness/install-harness.sh root /root
# The linters `make lint` runs in Go repositories such as ortusnova/sortie.
ARG GOLANGCI_LINT_VERSION=2.15.0
ARG GOLANGCI_LINT_SHA256=9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
RUN curl -fsSLo /tmp/gl.tgz "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64.tar.gz" \
 && echo "${GOLANGCI_LINT_SHA256}  /tmp/gl.tgz" | sha256sum -c - \
 && tar -xzf /tmp/gl.tgz -C /usr/local/bin --strip-components=1 "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64/golangci-lint" \
 && rm /tmp/gl.tgz \
 && apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends shellcheck && rm -rf /var/lib/apt/lists/* \
 && golangci-lint --version && shellcheck --version
# Claude Code accepts bypassPermissions as root only with IS_SANDBOX=1 (the worker image's env, worker-config.sh).
