---
# Sortie in the control plane's compose project (control-plane/docs/operator-guide.md).
# Copy this file to $CP_SORTIE_CONFIG_DIR/WORKFLOW.md (default
# $CP_HOME/sortie/WORKFLOW.md) and replace cp.example.org, the Huly project
# and the repository with yours. Secrets stay $VAR references: each is a
# podman secret the compose project hands Sortie as an environment variable.
#
# TLS ends at Pomerium (design D14.4): Sortie listens in plain HTTP on its
# address on the control plane's internal network only, and every flow URL
# below is Pomerium's https URL. A worker host reaches nothing else.
tracker:
  kind: huly
  endpoint: http://huly.cp.internal:8087
  api_key: $SORTIE_HULY_TOKEN
  project: ONS
  active_states: [Todo, In Progress]
  terminal_states: [Done, Canceled]
  in_progress_state: In Progress
  handoff_state: In Review
  release_state: Todo
  backlog_state: Backlog
  cancel_state: Canceled
  done_state: Done

huly:
  workspace: sortie
  epic_type: Epic
  story_type: Story

github:
  api_key: $SORTIE_GITHUB_TOKEN
  project: ortusnova/agentic-orchestration

server:
  control_token: $SORTIE_CONTROL_TOKEN

polling:
  interval_ms: 15000

workspace:
  root: /var/lib/sortie/workspaces

db_path: /var/lib/sortie/sortie.db

agent:
  kind: agent-client-protocol
  command: agent-acp
  max_turns: 20
  max_concurrent_agents: 3
  turn_timeout_ms: 3600000
  stall_timeout_ms: 900000
  read_timeout_ms: 60000

# Pomerium's identity header: Pomerium sets it from the worker's client
# certificate on every worker route and drops any value the worker sent.
worker:
  identity_header: X-Sortie-Worker-Identity

environment:
  kind: remote
  # acp-broker wraps each remote session as it wraps a local one, so the
  # workbench, in this container's network namespace, can watch and steer it.
  host_prefix: [acp-broker, -http, 127.0.0.1:0, -viewer-token-file, /run/secrets/cp-viewer-token, --]
  remote:
    image: stage0.smolmachine
    backend: smolvm
    requires: {cpu: 4, mem_gib: 8, disk_gib: 20}
    # The machine sees only placeholders; nono, behind the egress route,
    # holds each credential and replaces its placeholder on its hosts.
    credentials:
      - {name: anthropic, env: CLAUDE_CODE_OAUTH_TOKEN, hosts: [api.anthropic.com]}
    flows:
      stdio: {url: "https://cp.example.org:8443/stdio"}
      egress: {url: "https://cp.example.org:8443"}
      mcp: {url: "https://cp.example.org:8443/mcp"}
      bundles: {url: "https://cp.example.org:8443/bundles"}
      usage: {url: "https://cp.example.org:8443/usage"}
    repository:
      url: https://github.com/ortusnova/agentic-orchestration.git
      base: main
      branch_prefix: sortie/
      provider: github
      project: ortusnova/agentic-orchestration
---
Work on {{ .issue.identifier }}: {{ .issue.title }}

{{ .issue.description }}
