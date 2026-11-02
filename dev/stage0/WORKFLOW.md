---
# Stage 0 of agent-platform-workers (spec store design D13): Sortie under plan
# control on p1, co-located smolvm, GitHub as the forge. dev/stage0/stage0.sh
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

environment:
  kind: smolvm
  host_prefix: [acp-broker, -http, 127.0.0.1:0, -viewer-token-file, "${STAGE0_HOME}/viewer-token", --]
  smolvm:
    pack: ${STAGE0_PACK}
    cpus: 4
    memory_mib: 8192
    storage_gib: 20
    allow_hosts:
      - models.opencode.ai
      - registry.npmjs.org
      - proxy.golang.org
      - sum.golang.org
      # proxy.golang.org serves module zips by redirect to Google Cloud Storage.
      - storage.googleapis.com
    credentials:
      # Read at each launch (Sortie's command source), so a refreshed login
      # reaches the next run without a restart of Sortie.
      - {name: anthropic, env: CLAUDE_CODE_OAUTH_TOKEN, hosts: [api.anthropic.com], command: "${STAGE0_KIT}/anthropic-token.py"}
    pass_env: [IS_SANDBOX]
    # A story runs in the repository its plan node names (the Huly label
    # sortie-repository:<name>), else in sortie. homelab is listed so plan
    # sync accepts homelab stories, and closed: the homelab Sortie runs them.
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
      - name: homelab
        url: ${STAGE0_HOMELAB_REPO_URL}
        base: ${STAGE0_HOMELAB_REPO_BASE}
        provider: github
        project: ${STAGE0_HOMELAB_REPO_PROJECT}
        dispatch: false
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
