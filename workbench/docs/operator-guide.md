# Operator guide: Sortie, the broker, and the workbench on one host

This guide runs the three parts of the planning loop on one machine:

- **Sortie**, which syncs the approved plan into the tracker and dispatches agents.
- **`acp-broker`**, which the workbench starts for each planning session.
- **The plan workbench**, the web app where you plan with an agent and watch the graph.

The tracker here is a local Huly. Any tracker whose Sortie adapter declares the write capabilities works the same way; the workbench only talks to Sortie.

## Prerequisites

- Go, through the toolchain pinned in the Sortie repository.
- `uv`, Node.js 20 or newer with `npm`, and the `openspec` CLI (1.13).
- An ACP agent. The default is OpenCode (`opencode acp`), already signed in to a model provider.
- A local Huly at `http://localhost:8087` (see `docs/huly-adapter-notes.md` in the Sortie repository), with:
  - an account and a login token;
  - a project, for example `HULY`, that Sortie dispatches agents for;
  - the task types `Epic` and `Story` in that project's space type (see "Plan task types" in the Huly adapter notes), and, for plan control, a status `In Review` in each of its task types: Sortie's `handoff_state`, which must be neither active, terminal, nor the backlog state. Projects the workbench creates get the same space type, so they have them too. `dev/huly_project.py <front-url> <workspace> <email> <password> <project-key>` sets all three up, and changes nothing when they are there; `dev/dev.sh` runs it on every start. `dev/huly_clean_check.py` proves it from a clean slate, in a throwaway workspace it deletes afterwards.
- A Sortie built with plan projects (ADR 0034), so one Sortie serves the plan of every workbench project.

Ports used below: Sortie `7678`, workbench `8790`. Each broker picks a free localhost port.

**For development, skip the steps below and run `dev/dev.sh`.** It runs Sortie, the backend (`--reload`), and Vite (hot reload) in one terminal against a blank environment of its own: a workbench home with one project, `store`, whose empty spec store syncs into the Huly project `DEV`, and a Sortie under plan control that dispatches only `DEV` with the mock agent. Approve stories: the workbench syncs them on its own, Sortie releases them, the mock agent works on each for about a minute, and the story's pull request (a placeholder note, since the mock agent changes no code) opens in a local Gitea (`http://127.0.0.1:3001/sortie/dev-lab`, user `sortie`, password `Live-Gitea-Pw1`) while the story waits `In Review`. Merge it there, and within 30 seconds Sortie marks the story `Done` and releases what it unblocks. Projects you create land in the same home. Work persists across runs; `dev/dev.sh --reset` wipes it back to empty, the Gitea repository `dev-lab` included. Ctrl-C stops the three services; Gitea keeps running (`dev/forge/gitea.sh down` removes it). Every setting, the ports included, is in `dev/.env.example`; copy it to `dev/.env` to change one.

## 1. Build Sortie and the broker

From the repository root, build Sortie from the `sortie/` submodule, on our default branch `plan-bench`, which carries plan sync and the execution environments, and the broker from `acp-broker/`:

```sh
(cd sortie && GOWORK=off go build -o ~/.local/bin/sortie ./cmd/sortie)  # Sortie is not in go.work
(cd acp-broker && go build -o ~/.local/bin/acp-broker ./cmd/acp-broker)
```

`~/.local/bin` must be on `PATH`, because the workbench starts `acp-broker` by name. To use another path, set `WORKBENCH_BROKER_COMMAND`.

## 2. Configure and start Sortie

Create a working directory for Sortie, with a `WORKFLOW.md` like this one:

```yaml
---
tracker:
  kind: huly
  endpoint: $SORTIE_HULY_URL
  api_key: $SORTIE_HULY_TOKEN
  project: $SORTIE_HULY_PROJECT
  active_states: [Todo, In Progress]
  terminal_states: [Done, Canceled]
  in_progress_state: In Progress

huly:
  workspace: $SORTIE_HULY_WORKSPACE
  epic_type: Epic
  story_type: Story

server:
  port: 7678
  control_token: $SORTIE_CONTROL_TOKEN

polling:
  interval_ms: 10000

workspace:
  root: /tmp/sortie-plan-workspaces

agent:
  kind: mock
  max_turns: 3
  max_concurrent_agents: 2

mock:
  turn_delay_ms: 20000
---
**{{ .issue.identifier }}**: {{ .issue.title }}
```

The mock agent makes each turn last 20 seconds, so a running agent stays visible on the graph. Replace the agent block with a real agent when you want real work done.

Get a Huly login token, choose a control token, and start Sortie:

```sh
export SORTIE_HULY_URL=http://localhost:8087 SORTIE_HULY_WORKSPACE=sortie SORTIE_HULY_PROJECT=HULY
export SORTIE_HULY_TOKEN=$(curl -s -X POST $SORTIE_HULY_URL/_accounts -H 'Content-Type: application/json' \
  -d '{"method":"login","params":{"email":"you@example.com","password":"..."}}' | jq -r .result.token)
export SORTIE_CONTROL_TOKEN=$(openssl rand -hex 24)
echo "$SORTIE_CONTROL_TOKEN" > ~/.config/plan-workbench-sortie-token   # the workbench reads it below
sortie WORKFLOW.md
```

Check it: `curl -s -H "Authorization: Bearer $SORTIE_CONTROL_TOKEN" localhost:7678/api/v1/plan/capabilities` lists the four write capabilities and `custom_field`.

Sortie dispatches issues in `Todo` or `In Progress`. Synced issues start in `Backlog`, so nothing runs until you move stories to `Todo` in Huly. Sortie still starts only a story whose blockers are done.

## 3. Install and build the workbench

```sh
git clone <plan-workbench repository> && cd plan-workbench
uv sync
(cd frontend && npm ci && npm run build)
```

`uv sync` installs `planlib` and the backend into `.venv`. Started with `uv run`, the workbench puts `.venv/bin` first on `PATH`, so the planning agent it launches can run `planlib validate`.

Choose an operator password and keep its hash:

```sh
uv run workbench hash-password > ~/.config/plan-workbench-password-hash   # type the password at the prompt
```

## 4. Configure and start the workbench

| Variable | Required | Meaning |
|---|---|---|
| `WORKBENCH_HOME` | No | Where the project registry (`workbench.db`) and the spec stores of created projects (`projects/<slug>/`) live. Default: `$XDG_DATA_HOME/plan-workbench`, else `~/.local/share/plan-workbench`. |
| `WORKBENCH_STORE` | No | An existing spec store to register as a project at startup. It is registered once, under a slug made from its directory name; later starts find it by its path. |
| `WORKBENCH_HULY_PROJECT` | With `WORKBENCH_STORE` | The Huly project key that store syncs into. |
| `WORKBENCH_SORTIE_URL` | Yes | Sortie's HTTP address, for example `http://127.0.0.1:7678`. |
| `WORKBENCH_SORTIE_TOKEN` | When Sortie has a control token | Sortie's `server.control_token`. |
| `WORKBENCH_PASSWORD_HASH` | In local mode | Output of `workbench hash-password`. Local mode is the workbench without `WORKBENCH_OIDC_ISSUER`: one shared password signs in a local identity with both roles. It is for development; see "Sign-in" below. |
| `WORKBENCH_OIDC_ISSUER` | For team use | The OpenID Connect issuer URL. Setting it turns on sign-in through the provider and turns off the password. |
| `WORKBENCH_OIDC_CLIENT_ID` | With an issuer | The workbench's client id at the provider. |
| `WORKBENCH_OIDC_CLIENT_SECRET` or `WORKBENCH_OIDC_CLIENT_SECRET_FILE` | With an issuer | The client secret, or a file that holds it. |
| `WORKBENCH_OIDC_REDIRECT_URL` | With an issuer | `<workbench base URL>/api/auth/callback`, exactly as registered at the provider. |
| `WORKBENCH_OIDC_PLANNER_GROUP` | With an issuer | The group whose members get the `planner` role: plan, edit, retry a failed sync, run planning sessions, and manage projects. |
| `WORKBENCH_OIDC_APPROVER_GROUP` | With an issuer | The group whose members get the `approver` role as well: approve, release, pull back, cancel, supersede, and steer. |
| `WORKBENCH_OIDC_ROLES_CLAIM` | No | The ID token claim that lists the person's groups, default `groups`. |
| `WORKBENCH_OIDC_SCOPES` | No | The scopes the sign-in asks for, default `openid profile email`. Dex and Okta return groups only with `groups` added. |
| `WORKBENCH_ALLOW_LOCAL_MODE` | No | `1` lets `workbench serve` run local mode on a non-loopback `--host`, which it refuses otherwise. |
| `WORKBENCH_SECRET_KEY` | Yes | Random string that signs the session cookie. |
| `WORKBENCH_BROKER_COMMAND` | No | Broker command, default `acp-broker`. |
| `WORKBENCH_AGENT_COMMAND` | No | Agent command, default `opencode acp`. |
| `WORKBENCH_AGENT_MODEL` | No | Model a planning session starts with, default `anthropic/claude-opus-5-5`. It is selected through the agent's ACP `model` config option. When the agent does not offer the model, the session ends and the chat shows why. Set it empty to keep the agent's own default. The person can change the model and the thinking level below the message box; the last choice is kept in `preferences.json` in the runtime directory and wins over this setting while the agent offers it. Without a chosen thinking level, the model's own default applies. |
| `WORKBENCH_PROMPT_FILE` | No | Planning prompt, default `prompts/planning-session.md`. |
| `WORKBENCH_COOKIE_SECURE` | No | `1` behind HTTPS. |
| `WORKBENCH_SYNC` | No | `0` makes the workbench read-only toward the tracker: no automatic sync, no pull-back of an edited story, and pull-back, cancel and supersede are refused. For a second workbench beside the one that owns a project, for example to check a build against live data. Default `1`. |
| `WORKBENCH_RUNTIME_DIR` | No | The parent of each project's session runtime directory, `<dir>/<slug>`, which holds the running session's state, broker token, and logs. Default: `$XDG_RUNTIME_DIR/plan-workbench`, else `~/.cache/plan-workbench`. |

```sh
export WORKBENCH_STORE=~/dev/billing-specs WORKBENCH_HULY_PROJECT=HULY   # optional: bring in an existing store
export WORKBENCH_SORTIE_URL=http://127.0.0.1:7678
export WORKBENCH_SORTIE_TOKEN=$(cat ~/.config/plan-workbench-sortie-token)
export WORKBENCH_PASSWORD_HASH=$(cat ~/.config/plan-workbench-password-hash)
export WORKBENCH_SECRET_KEY=$(openssl rand -hex 32)
uv run workbench serve --port 8790
```

The tokens stay in this process. The browser holds only the workbench's own cookie.

To work on the workbench itself, add `--reload`: the backend restarts when its Python code changes. Run `npm run dev` in `frontend/` and open `http://localhost:5173` for hot reload of the frontend; Vite forwards `/api` to the backend on port 8790. Keep `WORKBENCH_SECRET_KEY` fixed so a restart keeps you signed in.

### Sign-in

With `WORKBENCH_OIDC_ISSUER` set, people sign in with the team's identity provider (authorization code flow with PKCE). Register the workbench at the provider as a confidential web client with the redirect URL `<base>/api/auth/callback`. Make the provider put the person's groups into the ID token claim `WORKBENCH_OIDC_ROLES_CLAIM` (Keycloak: a group membership mapper. Authentik: the default `groups` scope. Dex and Okta: add `groups` to `WORKBENCH_OIDC_SCOPES`). Membership in the approver group gives both roles; a person in neither group is refused, with a message saying so. Roles are read at sign-in, so a change at the provider applies at the person's next sign-in.

The browser holds only the workbench's signed session cookie (`HttpOnly`, `SameSite=Strict`, and `Secure` with `WORKBENCH_COOKIE_SECURE=1`), which carries the person's name, email, and roles. No provider token reaches it. **Sign out** ends the session, and then the provider's session when the provider offers an end-session endpoint. After switching from local mode, everyone signs in once more.

## 5. Plan

Open `http://127.0.0.1:8790` and sign in. The first page lists the projects.

1. **Create a project**, or open one. **New project** asks for a name, a slug, and a Huly project key; the slug and key are suggested from the name. The workbench asks Sortie to create the Huly project when it does not exist, or links it as it is when it does, then creates an empty spec store in git. A Huly project that cannot take plan writes, for example because its space type lacks the `Epic` task type, opens with an amber band that says what to fix in Huly. The slug names the project in links and on disk and never changes; **Rename** changes only the name. **Remove** ends the project's session and forgets the project, but keeps its spec store and its Huly project.
2. **Start a planning session** in the chat pane. The workbench starts one broker with the agent in the spec store, and the agent reports what the plan holds. Answer its permission requests in the chat.
3. **Ask for a breakdown**, for example "break the `billing` change into deliverables". The agent writes `breakdown.yaml`, runs `planlib validate`, and fixes what it reports. The graph redraws as the files change. Problems the validator finds appear in the red panel above the graph; select one to select its node.
4. **Point at nodes.** Selecting a node shows its details at the top of the chat and attaches it to your next message. Remove the attachment with its × before sending if you do not mean it.
5. **Approve.** Only a person with the `approver` role approves; the agent never does. Approvals live in the workbench database, with who approved and when, and never in the breakdown files. Approve a story's exact content: a later edit to it returns it to draft. Open an epic, or select it, and choose **Approve the epic** to approve every story of a change that has none approved yet. After that, approve story by story: select a story and choose **Approve**, or choose **Approve N draft stories** in the epic's view.
6. **Sync.** There is no sync step. About a second after an approval, a pull-back, a cancel, a supersede, or an edit that changes the approved part of the plan, the workbench syncs the project through Sortie's preview and apply, and asks Sortie to poll at once when a story was released. One sync runs at a time per project; changes during a sync cause one more. Nothing shows it while it works. When the tracker could not be updated, the top bar says **Tracker not updated**; open it for the reason, any failed write, and when it tries again (backoff of at most 60 seconds, and at the next change). **Try now** tries at once. An invalid plan, or a tracker that lacks a needed capability, waits for a fix instead.
7. **Run.** A synced story waits in Sortie's `backlog_state` until it is released. A sync releases it, by moving it to `release_state`, once it and every story it directly depends on are approved; Sortie then dispatches it when its blockers are done, and its running badge appears on the graph within a few seconds. Release is one way: the workbench never moves a released story back on its own, except to pull back a story whose content changed after release. Sortie runs agents only for its configured project (`tracker.project`); other projects are planned and synced, and their top bar says "agents not dispatched".

### The project home

A project opens on its home; the **Graph** tab beside it opens the graph. Everyone signed in sees the same home. From the top:

- **The timeline**: the milestones from `openspec/milestones.yaml` (format: `openspec/schemas/planning/milestones.schema.json`), the goal's tick drawn double, "we are here" at the share of the work that is done, and a red cross for each upcoming blocker that needs a person. Without the file the whole plan is one milestone. Each milestone counts its stories (done when their tracker issue is Done), and the `tasks.md` items its `tasks` setting selects; a line ending in `[metal]` is hardware work and `[operator]` is attended work or a decision, and any other line is agent work. A milestone may set `done: true` when it was finished outside the tracker.
- **Execution health**: whether the agents will stay busy (stuck, done, idle now, will idle, healthy) and why, the agent slots (Sortie's `agent.max_concurrent_agents`), and the runway of released stories not yet started. Their history covers the hours since the workbench started.
- **The critical path** to the next milestone's gate.
- **Needs a person**: every unit of planned work that cannot run yet and needs someone, in three columns (Approve, Write a spec, Operator tasks), ranked by how soon it holds agents up. **Approve** keeps the approver role check; every other action opens the graph at the item, selected and attached to the planning chat.
- **Milestones**: each one's stories, agent tasks and person tasks, counted apart.

### Story status

Each story shows its plan status on its node and in its details. The graph API names it in `plan_status`:

| Status | Meaning |
| --- | --- |
| Draft (`draft`) | Not approved, or edited since its approval. |
| Held (`held`) | Approved, but a story it depends on is not approved yet. The details name those stories. Shown as "held for approval" on the node. |
| Ready (`ready`) | Approved, with every direct blocker approved or merged. The next sync releases it. |
| Released (`released`) | In the tracker's release state, so Sortie may dispatch it. |
| Started (`frozen`) | An agent has claimed it. Its content is frozen. |
| Re-review (`needs_rereview`) | It needs a person again: a story it depends on was cancelled (`blocker_canceled`), or an agent said the work was already done and its evidence did not check out (`unverified_no_change`). Approve it again, or cancel it. |

A story's `review` field says who reviews its pull request: `human` (the default; a person merges it in the forge) or `criteria` (a separate agent checks the acceptance criteria, and the pull request merges on its own).

### Change a story after it starts

A started story is frozen: the validator refuses an edit to it or its removal, and the graph's red panel says so. Change scope instead with one of these:

- **Amend.** Add a new story that depends on the started one.
- **Supersede.** Add a new story with `supersedes: <the started story>`. Approve it, select it, and choose **Confirm: replaces …**. The workbench cancels the old story in the tracker and moves its dependents onto the new one.
- **Cancel.** Select the story and choose **Cancel story**. Sortie stops its worker and moves it to `cancel_state`. Every story that depends on it directly goes to Re-review.

**Pull back** (on a released story that has not started) returns it to `backlog_state`, so no agent takes it until the next sync releases it again.

Each action needs the `approver` role, and the workbench records who did it. The routes, under `/api/projects/<slug>/`, are `POST approve` (`{"keys": [...]}` or `{"epic": "<change>"}`), `POST stories/pull-back`, `POST stories/cancel`, and `POST stories/supersede` (each `{"key": "<change>/<id>"}`).

### Watch and steer workers

The context card lists the project's **Workers**: the stories Sortie runs, retries, or holds, with their stage (the workflow rule that runs them), attempt, turn, last message, and who holds the floor. A project that Sortie does not dispatch has none. Choose **Watch** in the list, or **Watch the worker** on a selected running story, to show that worker's session in the chat pane in place of the planning chat. The pane shows the session from its start. **Back to planning** returns to the planning chat, which kept running.

Watching needs the `planner` role. An `approver` can also steer the agent with a message, **Take over** (the broker's `interrupt`) and **Hand back**, answer its permission requests, and **Cancel story**. A planner sees the session and the permission requests, but no way to answer them. The workbench records each command with who sent it, and the pane shows each message with its sender's name.

When the watched run ends and Sortie starts another run of the same story, for a retry or the next stage, the pane follows it and says so. When Sortie runs nothing more for the story, the pane says that the worker ended.

The workbench reads each worker's broker address and viewer token from Sortie with its Sortie token, and relays the broker under the person's session; the browser never gets the token. The routes, under `/api/projects/<slug>/workers/<identifier>/`, are `GET events` (planner), `GET messages` (planner), and `POST commands` (approver; a planner may send only `state`).

### Projects from before story approval

When a project opens, the workbench updates the planning schemas in its spec store to the ones it ships, in one git commit ("Update the planning schemas"), and imports each breakdown approved in the old format (`status: approved` with a matching `approved_digest`) as an approval of all its stories, recorded as "Imported approval".

Each project has its own planning session, and sessions of different projects run at the same time. A planning session runs apart from the backend: a detached driver process holds the broker, and the backend finds it again through the runtime directory. So a page reload, another tab, a backend restart, or a `--reload` after a code change reattaches to the same session with its history. Only **End session** in the chat pane stops it.

## Stop

Choose **End session** in the chat pane first: stopping the backend leaves the planning session and its agent running, and the next backend start reattaches to it. Then stop the workbench and Sortie with Ctrl-C.

## Troubleshooting

- **Creating a project fails with `tracker_projects_unsupported`.** Sortie's tracker cannot create projects, or Sortie predates plan projects.
- **"Sortie unreachable" in the top bar.** Check `WORKBENCH_SORTIE_URL` and the token: `curl -H "Authorization: Bearer $WORKBENCH_SORTIE_TOKEN" $WORKBENCH_SORTIE_URL/api/v1/plan/graph`.
- **The session does not start.** `acp-broker` is not on `PATH`, or the agent command fails. The runtime directory, named in the workbench log, holds `driver.log`, the broker's `<key>.stderr`, and `state.json`, whose `error` field says what failed.
- **A session is still running after the backend stopped.** That is by design. Start the backend and choose **End session**, or send SIGTERM to the `driver_pid` in the runtime directory's `state.json`.
- **`planlib: ... planning schema not found`.** The store has no `openspec/schemas/planning/`.
- **Tracker not updated, with `stale_preview`.** The tracker changed between the preview and the apply twice in a row. The workbench tries again on its own; **Try now** tries at once.
- **A story stays Ready after a sync.** Sortie is not under plan control, or its `release_state` is not a state of the tracker project. Sortie's plan graph reports `plan_states`; with none, nothing is released.
- **A running story has no Watch.** Its broker has not reported its address to Sortie yet, or it runs in a version of `acp-broker` that does not report. Sortie's `GET /api/v1/state` shows `broker: null` for it.
- **`frozen` in the red panel.** The agent edited or removed a started story. Undo the edit, and amend or supersede instead.

## How the workbench calls OpenSpec

The workbench validates the whole plan at every approval, every edit, and every story start, and that check runs `openspec show <change> --json` to cross-check each change's requirement names against OpenSpec's own reading. Two defaults keep it fast, and both apply to every install, with nothing to configure:

- **OpenSpec's usage telemetry is off.** OpenSpec's CLI sends telemetry on every command and waits for the send before it exits: about 0.2 to 0.4 seconds of each call, and a network request per plan check. planlib sets `OPENSPEC_TELEMETRY=0` and `DO_NOT_TRACK=1` for each OpenSpec call it makes, and the workbench does the same for the planning agent's session. To turn telemetry back on, set either variable to another value in the workbench's environment; an explicit value is kept.
- **`openspec show` output is cached** in the workbench process, by the bytes of the change's files, the store's `openspec/config.yaml`, and the installed `openspec` executable. An approval changes no file, so it makes no OpenSpec call; an edit re-runs OpenSpec only for the change it touched. A failed call is never cached.

`scripts/check/openspec_timing.sh [store]` measures both on this machine: `openspec show` with telemetry on and off, and `planlib.validate` cold, warm (as at an approval), and after a spec edit. A warm check should take milliseconds; if it does not, a default regressed. `tests/planlib/test_openspec_speed.py` guards both defaults.

## Check the UI without Huly

`scripts/check/ui.sh planner|approver|local` starts the workbench on `http://127.0.0.1:8799` against a fake Sortie with fake worker brokers (`scripts/check/fake_sortie.py`), with a small seeded plan whose story `billing/b1` runs as `HLAB-1`. `planner` and `approver` sign in through Dex (`scripts/check/dex.yaml`, in a container), as one person with the planner role, or with both roles; `local` uses local mode with the password `check`. Each run starts from a blank state in `~/.cache/plan-workbench-check`, and Ctrl-C stops everything. Drive the worker with `curl -X POST 127.0.0.1:7690/admin/next` (its next run) and `/admin/end` (no more runs). With it running, `scripts/check/roles_api.sh planner|approver` checks, through the API, what that role may and may not do.

## Run everything live

`dev/live/live.sh` runs the whole loop on one machine with real agents: the workbench (`http://127.0.0.1:8791`, local mode, password `live`), Sortie under plan control (`127.0.0.1:7679`), `opencode acp` behind `acp-broker` for each worker, a local Gitea (`http://127.0.0.1:3001`, repository `sortie/live-lab`), and the local Huly project `LIVE`. Each setup step checks first, so a second start changes nothing; `--reset` starts over. The kit holds the Sortie workflow (`WORKFLOW.md`), the agents' own configuration home (`agent-config/`), and the OpenSpec change the planning agent breaks down (`store/`).

A run: ask the planning agent to break the change `pricing` into stories, and approve the epic; it syncs on its own. Sortie dispatches the released stories; each worker's change arrives as a pull request in Gitea and its story moves to `In Review`. Merge a pull request in Gitea: within a poll (30 seconds) Sortie moves the story to `Done` and dispatches the stories it unblocked. Small stories take an agent seconds, so `dev/live/take_over.sh <identifier>` waits for that story's worker and takes the floor through the workbench; then choose **Watch**, steer, and **Hand back**.

Needs: Docker, Go, `uv`, `npm`, the `openspec` CLI, and OpenCode signed in to Anthropic. The agents' configuration loads only the Anthropic sign-in plugin, not your own MCP servers or instructions. Ctrl-C stops Sortie and the workbench; `dev/forge/gitea.sh down` removes Gitea and its repositories.
