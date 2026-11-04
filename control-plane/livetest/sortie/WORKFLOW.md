---
# Sortie in the control plane's live tests (livetest/run.sh): the remote
# kind with a file tracker holding ONS-7 and ONS-8. The tests play the
# worker host, through Pomerium, with the certificate cpctl issued p1.
tracker:
  kind: file
  active_states: [Todo]
  terminal_states: [Done]
  handoff_state: In Review
file:
  path: /etc/sortie/issues.json
server:
  control_token: $SORTIE_CONTROL_TOKEN
polling:
  interval_ms: 1000
workspace:
  root: /var/lib/sortie/workspaces
db_path: /var/lib/sortie/sortie.db
agent:
  kind: agent-client-protocol
  command: /bin/sh live-test-agent
  max_turns: 1
  max_concurrent_agents: 2
  read_timeout_ms: 60000
  turn_timeout_ms: 600000
  stall_timeout_ms: 600000
worker:
  identity_header: X-Sortie-Worker-Identity
environment:
  kind: remote
  host_prefix: [acp-broker, -http, 127.0.0.1:0, -viewer-token-file, /run/secrets/cp-viewer-token, --]
  remote:
    image: live-test
    backend: smolvm
    requires: {cpu: 1, mem_gib: 1, disk_gib: 1}
    credentials:
      - {name: anthropic, env: ANTHROPIC_API_KEY, hosts: [model.cp.test]}
    flows:
      stdio: {url: "https://cp.test:8443/stdio"}
      egress: {url: "https://cp.test:8443"}
      mcp: {url: "https://cp.test:8443/mcp"}
      bundles: {url: "https://cp.test:8443/bundles"}
      usage: {url: "https://cp.test:8443/usage"}
    lease_ttl_ms: 600000
---
Work on {{ .issue.identifier }}: {{ .issue.title }}
