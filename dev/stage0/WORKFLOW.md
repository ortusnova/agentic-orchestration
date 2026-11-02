---
# Stage 1 of agent-platform-workers (spec store design D13, D18): the one
# Sortie, under plan control on p1, for every repository, under the remote
# kind; GitHub is the forge. dev/stage0/stage0.sh
# renders the ${STAGE0_*} placeholders; secrets stay $VAR references, read from
# Sortie's environment (dev/stage0/run-sortie.sh), never written here.
tracker:
  kind: huly
  endpoint: ${STAGE0_HULY_URL}
  api_key: $STAGE0_HULY_TOKEN
  project: ${STAGE0_HULY_PROJECT}
  active_states: [Todo, In Progress]
  terminal_states: [Done, Canceled]
  in_progress_state: In Progress
  handoff_state: In Review
  release_state: Todo
  backlog_state: Backlog
  cancel_state: Canceled
  done_state: Done

huly:
  workspace: ${STAGE0_HULY_WORKSPACE}
  epic_type: Epic
  story_type: Story

# A story is done when its pull request merges, whoever merges it.
reactions:
  merge_completion:
    provider: github
    target_state: Done
    poll_interval_ms: 30000

github:
  api_key: $STAGE0_GITHUB_TOKEN

# Comments on the story, so the operator sees in Huly why a run stopped.
notifications:
  - kind: tracker_comment
    events: [session.failed, session.stopped, budget.held]

server:
  port: ${STAGE0_SORTIE_PORT}
  control_token: $STAGE0_SORTIE_TOKEN

polling:
  interval_ms: 15000

workspace:
  root: ${STAGE0_HOME}/workspaces

hooks:
  after_create: |
    if [ -f go.mod ]; then GOTOOLCHAIN=local go mod download; fi
  timeout_ms: 600000

# One ACP launch for both harnesses, both on the operator's Claude
# subscription: the pack's agent-acp runs opencode by default, and Claude
# Code (claude-agent-acp) for a rule whose name starts with "claude", from
# SORTIE_RULE. Both run behind acp-broker on the host, so the workbench can
# watch and steer either.
agent:
  kind: agent-client-protocol
  command: agent-acp
  max_turns: 20
  max_sessions: 16
  max_concurrent_agents: ${STAGE0_MAX_AGENTS}
  turn_timeout_ms: 3600000
  stall_timeout_ms: 900000
  # A fresh VM's agent can take over 15 s to answer at session start; the
  # default 5 s fails every first attempt. Running turns are bounded by the
  # stall and turn timeouts above, not by this.
  read_timeout_ms: 60000

dispatch:
  rules:
    - name: claude-label
      match:
        labels: [claude-code]
    - name: claude-title
      match:
        title: ["[claude]"]

# Every story runs on a worker (worker-daemon), p1's or a worker host's,
# chosen by Sortie's matcher from what its job requires, never from a host
# name (spec store agent-platform/one-sortie). A worker reaches p1's edge at
# 127.0.0.1:${STAGE0_EDGE_WORKER_PORT} on its own loopback: p1's worker
# directly, a worker host's through the tunnel p1 opens to it. The flows go
# through the edge; egress is nono on p1, which holds the credentials and
# allows only the hosts below (egress-profile.json). acp-broker on p1 carries
# each session, so the workbench can watch and steer it.
environment:
  kind: remote
  host_prefix: [acp-broker, -http, 127.0.0.1:0, -viewer-token-file, "${STAGE0_HOME}/viewer-token", --]
  remote:
    backend: smolvm
    # The stage-0 pack; each worker maps the name to its file
    # (worker-config.sh).
    image: stage0.smolmachine
    requires: {cpu: 4, mem_gib: 8, disk_gib: 20}
    flows:
      stdio: {url: "http://127.0.0.1:${STAGE0_EDGE_WORKER_PORT}/stdio"}
      egress: {url: "http://127.0.0.1:${STAGE0_EDGE_WORKER_PORT}"}
      mcp: {url: "http://127.0.0.1:${STAGE0_EDGE_WORKER_PORT}/mcp"}
      bundles: {url: "http://127.0.0.1:${STAGE0_EDGE_WORKER_PORT}/bundles"}
      usage: {url: "http://127.0.0.1:${STAGE0_EDGE_WORKER_PORT}/usage"}
    # Every repository's hosts: nono allows a job only these and the hosts of
    # the credentials it holds. homelab's are a first list from the chain's
    # pins; the pilot replaces it with what a chain needed (homelab-pilot).
    allow_hosts:
      - models.opencode.ai
      - registry.npmjs.org
      - proxy.golang.org
      - sum.golang.org
      # proxy.golang.org serves module zips by redirect to Google Cloud Storage.
      - storage.googleapis.com
      - codeload.github.com
      - "*.githubusercontent.com"
      - ghcr.io
      - registry-1.docker.io
      - auth.docker.io
      - production.cloudflare.docker.com
      # Docker Hub's blob CDN redirects here too (ONS-22, 2026-11-01).
      - production.cloudfront.docker.com
      - quay.io
      - "*.quay.io"
      - registry.k8s.io
      - "*.pkg.dev"
      - cdimage.debian.org
      - deb.debian.org
      - security.debian.org
      - pypi.org
      - files.pythonhosted.org
      - charts.jetstack.io
      - charts.longhorn.io
      - cloudnative-pg.github.io
      - kubernetes.github.io
    credentials:
      - {name: anthropic, env: CLAUDE_CODE_OAUTH_TOKEN, hosts: [api.anthropic.com]}
    pass_env: [IS_SANDBOX]
    # A story runs in the repository its plan node names (the Huly label
    # sortie-repository:<name>), else in sortie. Every one is dispatched here:
    # no other Sortie polls the project.
    repositories:
      - name: sortie
        url: ${STAGE0_REPO_URL}
        base: ${STAGE0_REPO_BASE}
        provider: github
        project: ${STAGE0_REPO_PROJECT}
      - name: agentic-orchestration
        url: ${STAGE0_AO_REPO_URL}
        base: ${STAGE0_AO_REPO_BASE}
        provider: github
        project: ${STAGE0_AO_REPO_PROJECT}
      # What a homelab job needs, which replaces the homelab Sortie's smolvm
      # settings: nested KVM for the seed and Talos VMs, the sandbox's 8 vCPU
      # and 20 GiB, and a 200 GiB disk. The homelab pack, its user builder,
      # its guest kernel, its shared folders, the vm key and what the hooks
      # did are the worker's (homelab-inputs.sh): a worker with homelab's
      # dev host declares nested, any other does not.
      - name: homelab
        url: ${STAGE0_HOMELAB_REPO_URL}
        base: ${STAGE0_HOMELAB_REPO_BASE}
        provider: github
        project: ${STAGE0_HOMELAB_REPO_PROJECT}
        image: homelab.smolmachine
        requires: {capabilities: [nested], cpu: 8, mem_gib: 20, disk_gib: 200}
    default_repository: sortie
---
You are working on story **{{ .issue.identifier }}**: {{ .issue.title }}

{{ if .issue.description }}{{ .issue.description }}{{ end }}
{{- if .park }}

A person released this story ({{ .park.gesture }}). You stopped earlier{{ if .park.statement }}: {{ .park.statement }}{{ else }}.{{ end }}
Comments posted since you stopped, oldest first; answer them before you go on:
{{ range .park.comments }}
- {{ .author }} ({{ .created_at }}): {{ .body }}
{{- else }}
(none)
{{- end }}
{{- end }}

The repository is checked out in the current directory, on the story's
branch. Read its `AGENTS.md` first and follow it. Change the code to meet the
story's acceptance criteria, add or update tests, and run the repository's
checks for what you changed until they pass. Keep the change focused on this
story.

The story's spec, if it has one, is in the description above: implement every
requirement it covers and give each scenario a test. Finish the whole story
across as many turns as it takes. Stop early, with `needs-human-review` and
the reason, only when you are blocked on something only a person can decide
or provide; work that is merely large or still missing is not a reason.

Commit your work with git, in conventional commits, with no AI attribution.
Do not push and do not open a pull request: Sortie does both after the run.
Only committed work leaves this machine. Before you finish, write
`.sortie/pr.json` with a JSON object holding a short `title` and a `body`
that says what changed and how you proved it.
