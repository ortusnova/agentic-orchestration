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
| `WORKBENCH_SORTIE_NAME` | No | The name of the host the Sortie at `WORKBENCH_SORTIE_URL` runs on, as the workers' lanes show it (default `sortie`), such as `p1`. |
| `WORKBENCH_SORTIES` | No | Other Sorties whose workers the workbench shows, and to which it sends answers, session grants and session views of the stories they hold: `name=url`, separated by commas, such as `taichi=http://127.0.0.1:7681` for taichi's homelab Sortie through an SSH forward. Lower-case names. Each one's control token is in `WORKBENCH_SORTIE_TOKEN_<NAME>`, or in the file `WORKBENCH_SORTIE_TOKEN_<NAME>_FILE` names, `<NAME>` in capitals with `_` for `-`. The stage-0 kit sets these from the files `$STAGE0_HOME/sorties/<name>.env` and `<name>.token`; with one Sortie on p1 it sets none (the retired `huly-tunnel.sh` wrote them for taichi's homelab Sortie). |
| `WORKBENCH_SORTIE_PROXY_<NAME>` | No | The proxy through which the workbench reaches the brokers of the runs that Sortie holds, which listen on its host's loopback: `socks5://host:port`, such as `socks5://127.0.0.1:7781`, the SOCKS forward in the SSH connection that forwards taichi's Sortie. Without it, the workbench connects to the broker address Sortie gives, which only works on Sortie's own host. |
| `WORKBENCH_NOTIFY_KIND` | No | The operator's channel, where each open question is pushed: `slack`, `discord`, `telegram` or `webhook`. Unset, nothing is pushed. See "Questions on the operator's channel". |
| `WORKBENCH_NOTIFY_CREDENTIAL_FILE` | With a channel | The file that holds the channel's webhook URL, or the Telegram bot's token. The workbench refuses one that anyone but its owner may write. |
| `WORKBENCH_NOTIFY_CHAT_ID` | With `telegram` | The Telegram chat the bot posts to. |
| `WORKBENCH_PUBLIC_URL` | With a channel | Where people reach this workbench, for example `https://workbench.homelab.dev`; each pushed question links to its project's home there. |
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
| `WORKBENCH_SYNC` | No | `0` makes the workbench read-only toward the tracker: no automatic sync, no pull-back of an edited story, and pull-back, cancel and supersede are refused. It also writes and pushes no planning schema upgrade, and validates against the schemas it ships. For a second workbench beside the one that owns a project, for example to check a build against live data, as a ui story's preview does. Default `1`. |
| `WORKBENCH_GITHUB_TOKEN` | For private repositories | The GitHub token the workbench looks up the pull requests a story names in `delivered_by` with (see "A story built outside an agent run"). It needs read access to their pull requests; the token Sortie pushes with will do. Without one, only public repositories can be looked up. |
| `WORKBENCH_GITHUB_API_URL` | No | GitHub's API, default `https://api.github.com`. |
| `WORKBENCH_DELIVERY_RECHECK_SECONDS` | No | How often a named pull request that is not merged yet is looked up again, default `300`, at most `540`. A merged one is never looked up again. |
| `WORKBENCH_PREVIEWS` | No | The record of the ui stories' previews that `workbench preview-sync` keeps; with it, a started ui story shows its preview's address, and reads Merging once its pull request records the click-proofs for its head. Unset: no previews. |
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
5. **Approve.** Only a person with the `approver` role approves; the agent never does. Approvals live in the workbench database, with who approved and when, and never in the breakdown files. Approve a story's exact content: a later edit to it returns it to draft. Open an epic, or select it, and choose **Approve the epic** to approve every story of a change that has none approved yet, except a story that needs a spec. After that, approve story by story: select a story and choose **Approve**, or choose **Approve N draft stories** in the epic's view.
6. **Sync.** There is no sync step. About a second after an approval, a pull-back, a cancel, a supersede, or an edit that changes the approved part of the plan, the workbench syncs the project through Sortie's preview and apply, and asks Sortie to poll at once when a story was released. One sync runs at a time per project; changes during a sync cause one more. Nothing shows it while it works. When the tracker could not be updated, the top bar says **Tracker not updated**; open it for the reason, any failed write, and when it tries again (backoff of at most 60 seconds, and at the next change). **Try now** tries at once. An invalid plan, or a tracker that lacks a needed capability, waits for a fix instead.
7. **Run.** A synced story waits in Sortie's `backlog_state` until it is released. A sync releases it, by moving it to `release_state`, once it and every story it directly depends on are approved; Sortie then dispatches it when its blockers are done, and its running badge appears on the graph within a few seconds. Release is one way: the workbench never moves a released story back on its own, except to pull back a story whose content changed after release. Sortie runs agents only for its configured project (`tracker.project`); other projects are planned and synced, and their top bar says "agents not dispatched".

### The project home

A project opens on its home; the **Graph** tab beside it opens the graph. Everyone signed in sees the same home. From the top:

- **The timeline**: the milestones from `openspec/milestones.yaml` (format: `openspec/schemas/planning/milestones.schema.json`), the goal's tick drawn double, "we are here" at the share of the work that is done, and a red cross for each upcoming blocker that needs a person. Without the file the whole plan is one milestone. Each milestone counts its stories (done when their tracker issue is Done), and the `tasks.md` items its `tasks` setting selects; a line ending in `[metal]` is hardware work and `[operator]` is attended work or a decision, and any other line is agent work. A milestone also counts the stories its stories depend on, directly or through other stories, when no milestone includes them or only a later one does: each counts once, in the earliest milestone that waits on it, and shows under its own change. A milestone may set `done: true` when it was finished outside the tracker.
- **Workers**, as slot lanes in time: one lane per agent slot of each worker host, each job a box from its start to its estimated end, with its story, phase, current step and time, and a line at now. Above the lanes, whether work moves or is stuck (stuck, done, idle now, will idle, healthy) and why, how many slots are busy, and how many released stories are ready to start; under each worker, what it runs next. A stuck slot reads as stuck in its own lane, with why and the action that resolves it there (see [Workers](#workers)). Free slots with nothing ready read as stuck, not idle, when released stories wait on pull requests in review, to merge or to deploy, on questions, or on held budgets: the verdict counts them ("6 stories wait on 5 pull requests in review") and lists each thing waited on, with its stories and who acts on it. The count of what needs a person is the **Needs a person** section's, not the lanes'.
- **The critical path** to the next milestone's gate.
- **Needs a person**: first, in its own **Answer** column, each question an agent asked when it stopped (see [Answer an agent](#answer-an-agent)); then every unit of planned work that cannot run yet and needs someone, in three columns (Approve, Write a spec, Operator tasks), ranked by how soon it holds agents up. **Approve** keeps the approver role check; every other action opens the graph at the item, selected and attached to the planning chat.
- **Milestones**: each one's stories, agent tasks and person tasks, counted apart.

### Story status

Each story shows its plan status on its node and in its details. The graph API names it in `plan_status`:

| Status | Meaning |
| --- | --- |
| Needs a spec (`needs_spec`) | Not approved, covers no requirement (`covers` is empty), and not done. It cannot be approved: the workbench offers no **Approve**, the epic's approval leaves it out, and `POST approve` naming it fails with `no_spec`. This holds for an enabler too. An approval recorded before this rule stands. |
| Draft (`draft`) | Not approved, or edited since its approval. |
| Held (`held`) | Approved, but a story it depends on is not approved yet. The details name those stories. Shown as "held for approval" on the node. |
| Ready (`ready`) | Approved, with every direct blocker approved or merged. The next sync releases it. |
| Released (`released`) | In the tracker's release state, so Sortie may dispatch it. |
| Started (`frozen`) | An agent has claimed it. Its content is frozen. |
| Building (`building`) | A `review: ui` story whose build runs. Its content is frozen. |
| In review (`in_review`) | A `review: ui` story whose review runs: the criteria review, the review on its preview that runs the click-proofs, or a fix run of the review rule; or whose build is over and whose review is next. |
| Waiting for click-proofs (`click_proofs`) | A `review: ui` story whose review passed: Sortie's merge watch holds its pull request and waits for the click-proofs on its preview, recorded for its head commit. Once the preview is up, the node's details link to its address and the pull request (`preview` in the graph API: `url`, `pull`, `pull_url`, `sha`, `click_proofs`). |
| Merging (`merging`) | A `review: ui` story whose pull request records the click-proofs for its head (`preview.click_proofs`): Sortie merges it. |
| Delivered (`delivered`) | It names the pull requests that delivered it, and every one is merged. It reads Done, and counts as done for its epic and as approved and done for the stories that depend on it. |
| Not confirmed (`delivery_unconfirmed`) | It names pull requests, and one is open, closed without merging, or cannot be looked up. It reads "Delivery not confirmed" with each pull request and why, and counts as not done. |
| Re-review (`needs_rereview`) | It needs a person again: a story it depends on was cancelled (`blocker_canceled`), or an agent said the work was already done and its evidence did not check out (`unverified_no_change`). Approve it again, or cancel it. |

A story's `review` field is `human` (the default), `criteria` or `ui`. In the stage-0 kit `human` and `criteria` both go through the criteria gate (design D5): an agent session separate from the build checks each acceptance criterion, and Sortie merges the pull request once that review and CI pass. A criterion the review cannot settle shows as a question on the workbench, and the pull request stays open.

### A UI story

A story with `review: ui` changes an interface. The operator approves that interface as a design prototype before the story is written, and the story's acceptance criteria name the approved prototype; after that no person approves or merges it (spec store `agent-platform/run-supervision`, "A UI is approved as a prototype, and the built story merges after its review"). A UI change with no approved prototype goes to prototyping first, and the operator is asked on the decision board, not on a built pull request. Once it covers a requirement it reads Ready, Held or Released as an approved story does, with no **Approve**: the epic's approval leaves it out, and `POST approve` naming it fails with `ui_story`. The stories that depend on it count it as approved. A sync sends it with no approver, and Sortie releases it. One that covers no requirement needs a spec first.

An agent builds it (Building), the criteria review checks it (In review), and once the review passed Sortie's merge waits for its click-proofs (Waiting for click-proofs). Once CI passes `workbench preview-sync` deploys its pull request as a preview: the branch's workbench, read-only toward the tracker (`WORKBENCH_SYNC=0`), against live data, and the story's details show the address. The kit comments the address and the commit on the pull request, once per head it deploys, and that comment starts the review on the preview (In review again): it clicks every action the story's criteria name there and, when they pass, comments `sortie:click-proofs passed <head sha>` on the pull request. Once that record names the head the story reads Merging, and Sortie merges it; the preview goes away and the story is Done. A fix pushed after the click-proofs needs them again on its own preview. A comment on the pull request still starts a fix run on the branch.

`workbench preview-sync --repo OWNER/REPO --record FILE --deploy COMMAND [--live-url URL] [--first-port 8800] [--handoff-state "In Review"]` is one pass, run on a timer: it reads Sortie's plan graph with `WORKBENCH_SORTIE_URL`, `WORKBENCH_SORTIE_TOKEN` and `WORKBENCH_HULY_PROJECT`, and GitHub with `WORKBENCH_GITHUB_TOKEN`. A ui story's pull request is the one from `sortie/<identifier>`. It runs `COMMAND up IDENTIFIER BRANCH SHA PORT ADDRESS` to deploy and `COMMAND down IDENTIFIER` to remove, and keeps `FILE`, which the live workbench reads as `WORKBENCH_PREVIEWS`. The address is a name beside `--live-url`'s (`https://workbench.example.net` gives `https://workbench-ons-93.example.net/`), or the loopback port without one. The stage-0 kit runs it every minute (`dev/stage0/README.md`).

### A story built outside an agent run

When a story's work was done by hand, or anywhere else, its deliverable names the pull requests that delivered it, each `owner/repo#number`:

```yaml
- id: homelab-image
  delivered_by: [ortusnova/homelab#85]
```

The workbench looks up each one on GitHub with `WORKBENCH_GITHUB_TOKEN`, then again every `WORKBENCH_DELIVERY_RECHECK_SECONDS` until it is merged, and never again after: the registry keeps each merge. Once every one is merged, the story reads Done, with a link to each pull request and its merge commit, and the page updates on its own. Such a story offers no Approve, and no sync creates or releases it: Sortie never runs it, and it blocks nothing in the tracker. Naming the pull request does not change what an approval covers, so it is allowed on a started story too. A store whose breakdown schema predates `delivered_by` gets the current one when the workbench opens the project.

### Change a story after it starts

A started story is frozen: the validator refuses an edit to it or its removal, and the graph's red panel says so. Change scope instead with one of these:

- **Amend.** Add a new story that depends on the started one.
- **Supersede.** Add a new story with `supersedes: <the started story>`. Approve it, select it, and choose **Confirm: replaces …**. The workbench cancels the old story in the tracker and moves its dependents onto the new one.
- **Cancel.** Select the story and choose **Cancel story**. Sortie stops its worker and moves it to `cancel_state`. Every story that depends on it directly goes to Re-review.

**Pull back** (on a released story that has not started) returns it to `backlog_state`, so no agent takes it until the next sync releases it again.

Each action needs the `approver` role, and the workbench records who did it. The routes, under `/api/projects/<slug>/`, are `POST approve` (`{"keys": [...]}` or `{"epic": "<change>"}`), `POST stories/pull-back`, `POST stories/cancel`, and `POST stories/supersede` (each `{"key": "<change>/<id>"}`).

### Answer an agent

An agent that cannot go on without a person stops and states why, and Sortie parks its story. The workbench reads each park from Sortie's state at every poll, and the stop statement whole from Sortie's per-issue response. Each one is listed first on the home, under **Answer**: the story, the whole statement, when it stopped, and an answer box. The story's details in the graph show the same, its card reads "asks a question", and the execution verdict names the question (`<story> asks: …`). While a question is open, the project bar and the page title of the home and the graph show the count ("1 question"), live.

An `approver` answers in the box. The workbench sends the text through Sortie's answer API (`POST /api/v1/<identifier>/answer`, with the person's name as the author); Sortie posts it on the issue and releases the park, and the story's next run resumes with the statement and the answer. The workbench writes nothing to the tracker itself, and records who answered in the story's worker history. A question leaves the list as soon as Sortie no longer reports the park, whether it was answered here or released in the tracker. A planner sees the question but no answer box, and a read-only workbench (`WORKBENCH_SYNC=0`) refuses answers. The route is `POST /api/projects/<slug>/stories/answer` (`{"key": "<change>/<id>", "text": "…"}`); Sortie's `409` for an issue that is not parked comes back as `409`.

#### Questions on other Sorties

More than one Sortie may run a project's stories, such as p1's and taichi's in the stage-0 kit. The workbench reads the parks of every Sortie in `WORKBENCH_SORTIES` too, at the same poll, and lists their questions with its own Sortie's; each one says which Sortie holds it ("on taichi's Sortie"). An answer goes to the answer API of the Sortie that holds the question, never to another. A Sortie that cannot be read keeps the questions it last reported until it answers again.

#### Questions on the operator's channel

With `WORKBENCH_NOTIFY_KIND` set, the workbench pushes each open question to the operator's channel the first time it sees it, within one poll: the story, the Sortie that holds it, the whole statement, and a link to the project's home on `WORKBENCH_PUBLIC_URL`, where it is answered. A question still open 4 hours after its last push is pushed once more ("Still open after 4 hours: …"), and so on every 4 hours. Once it is answered, or released in the tracker, it is not pushed again; a new park of the same story is a new question. What was pushed when is kept in `alerts.json` in `WORKBENCH_HOME`, so a restart pushes nothing twice. A push the channel refuses is logged, without the channel's address, and tried again a minute later. A read-only workbench (`WORKBENCH_SYNC=0`) pushes nothing, so that only the workbench that owns the project does. Without `WORKBENCH_NOTIFY_KIND`, no channel is configured and nothing is pushed.

| Kind | Credential file holds | Also |
| --- | --- | --- |
| `slack` | the incoming webhook URL | |
| `discord` | the channel webhook URL; mentions never ping | |
| `telegram` | the bot's token | `WORKBENCH_NOTIFY_CHAT_ID` |
| `webhook` | the URL that receives a JSON POST: `text`, `event_type` (`question.opened` or `question.open_again`), `project`, `identifier`, `story`, `title`, `statement`, `sortie`, `url` | |

### Watch and steer workers

The context card lists the project's **Workers**: the stories Sortie runs, retries, or holds, with their stage (the workflow rule that runs them), attempt, turn, last message, and who holds the floor. A project that Sortie does not dispatch has none. Choose **Watch** in the list, or **Watch the worker** on a selected running story, to show that worker's session in the chat pane in place of the planning chat. The pane shows the session from its start. **Back to planning** returns to the planning chat, which kept running.

Watching needs the `planner` role. An `approver` can also steer the agent with a message, **Take over** (the broker's `interrupt`) and **Hand back**, answer its permission requests, and **Cancel story**. A planner sees the session and the permission requests, but no way to answer them. The workbench records each command with who sent it, and the pane shows each message with its sender's name.

When the watched run ends and Sortie starts another run of the same story, for a retry or the next stage, the pane follows it and says so. When Sortie runs nothing more for the story, the pane says that the worker ended.

The workbench reads each worker's broker address and viewer token from Sortie with its Sortie token, and relays the broker under the person's session; the browser never gets the token. The routes, under `/api/projects/<slug>/workers/<identifier>/`, are `GET events` (planner), `GET messages` (planner), and `POST commands` (approver; a planner may send only `state`).

### Workers

The **Workers** tab (`#/p/<slug>/workers`, and `GET /api/projects/<slug>/workers`) shows each worker host of every Sortie the workbench reads (`WORKBENCH_SORTIE_URL` and `WORKBENCH_SORTIES`): its capacity and what it uses now (agent slots, CPUs, memory, disk: Sortie serves `capacity` and `used`, and free is their difference), each job with its story, phase (build, review, fix, live proof, deploy), the step the job reports and how long it has run, what waits on the worker and why (a story waiting for a slot or a capability, a pull request waiting for a live proof, a deploy waiting for the host to drain), and each machine on it that no Sortie started, with its name and age. Below them, every open pull request of a story says what it waits on: its review, CI, a conflict, a live proof, a person, or nothing ("ready to merge"). A pull request ready to merge for 10 minutes makes its worker's verdict stuck: nothing merges it. The page, the home's lanes and each story's panel follow the graph's stream, so they change at the poll after Sortie's state does, within about 5 seconds.

Every stuck or waiting lane, and every story it names, has an action:

- **Answer**, on a story stopped with a question, opens its answer box in the lane. The answer goes to the Sortie that holds the park, p1's or taichi's, which runs the story again.
- **More sessions**, on a story its session budget holds, grants it more sessions (4 by default, at most 32) through the Sortie that holds it, which runs it again with no change to its workflow: `POST /api/projects/<slug>/stories/sessions` (`{"key": "<change>/<id>", "sessions": 4}`, approver). A Sortie that has no per-issue grant answers `501 grant_unsupported`.
- **Open**, on a job, opens the graph at the story with its agent's session in the chat pane: live while it runs, where an approver can send it a message, and its last transcript after, from the Sortie that ran it (`GET /api/v1/runs/<dispatch_id>/events`). The link names the session in the URL (`watch=<identifier>`).
- A story's panel shows its **run state**: where it runs or why it waits, what releases it, its pull request and what that waits on, and the same action.

What the workbench reads of each Sortie's `GET /api/v1/state`. Where a Sortie lacks a member, the view goes without that part and says so instead of showing it empty: the Workers view and the home's lanes name the Sortie, the missing field and what cannot be shown without it (`sorties[].missing` and `lanes.missing`, each `{"field", "text"}`), and a worker's row says "does not report" for its use, its unmanaged machines or its jobs' steps (`workers[].known`). The fields it names are `host`, `workers[].declaration.used`, `workers[].declaration.unmanaged`, `running[].step`, `waits` and `pulls`.

| Member | What the view shows |
| --- | --- |
| `config.agent.max_concurrent_agents` | The slots of a Sortie that runs agents on its own host (environment kind `smolvm`). |
| `host` | That host: `name`, `capacity` and `used` (`{"cpu", "mem_gib", "disk_gib"}`), and `unmanaged`, the machines on it no Sortie started, each `{"name", "since", "state"}`. |
| `workers.workers[]` | Under environment kind `remote`, each worker daemon: `id`, `declaration.run_slots`, `declaration.free`, `declaration.used` and `declaration.unmanaged` as the daemon declares them ([worker daemon guide](../../worker-daemon/docs/operator-guide.md#what-the-workbench-sees-of-the-worker)), and `last_check_in`. `workers.leases[]` places each job on its worker; `workers.queue[]` gives the jobs no worker took and why (`waiting`, `job.requires.capabilities`). |
| `running[]` | Each job: `started_at`, `rule_name`, `reaction_kind`, `stage_position`, `held` and `hold_reason`, `broker`, `pull_request`; `worker`, the host it runs on; `estimated_end`, the start plus the median of the rule's recent runs (without it, the box's end is estimated from the phase). Under environment kind `remote` only, `phase` (the daemon's own words: one of build, review, fix, live proof or deploy is drawn as such, any other as build) and `step` with `step_at`, which a worker daemon carries on its heartbeats. A Sortie that runs agents on its own host serves no step, so the view says "not reported by its Sortie" and does not name the field as missing. |
| `parked[]`, `budget_exhausted[]` | The stories stopped for a person or a budget, each in a lane of its worker (`worker`, else the Sortie's host). A park with `reason` `agent_blocked` asks a question; any other reason reads "stopped (reason)", and Answer still releases it. Only `reason` `session_budget` offers **More sessions**, with `used_sessions` and `budget_sessions`; a `token_budget` or `money_budget` links to its story, since only a larger budget in its workflow releases it. |
| `held[]` | In its panel, why any held story waits and what releases it (`hold.reason_text`, `hold.release_text`); from a Sortie without `waits[]`, also a story held back for a slot (`hold.reason` `agent_slots_full`). |
| `waits[]` | What waits on a worker: `{"issue_identifier", "worker", "kind", "reason", "release", "since"}`, `kind` one of `slot`, `capability`, `drain` (no `issue_identifier`; it marks the worker draining). A wait with no `worker` goes to the Sortie's first worker. When a Sortie serves `waits[]`, the view does not also read waits from `held[]`, `workers.queue[]` and `dispatch_paused`. |
| `pulls[]` | Each open pull request of a story: `{"issue_identifier", "number", "repository", "url", "waits_on", "reason", "since", "worker"}`, `waits_on` one of `review`, `ci`, `conflict`, `person`, `none` (Sortie knows no live proof, so it never serves `live_proof`); `worker` is absent under kind `remote`, and the pull request then goes to the Sortie's first worker. Without it, a running review or fix names its `pull_request` as waiting on its review or CI. |
| `dispatch_paused` | From a Sortie without `waits[]`: a deploy that waits for the host to drain. |

Sortie serves the session grant as `POST /api/v1/<identifier>/sessions` (`{"add": n, "by": "…", "reason": "…"}`, `add` from 1 to 64; ortusnova/sortie#32; it answers `{"issue_identifier", "issue_id", "used_sessions", "budget_sessions", "granted_sessions"}`, `budget_sessions` null when `agent.max_sessions` sets none): it raises that issue's session budget by `n` and dispatches it again, and answers `404 issue_not_found` for an issue it does not hold. The workbench offers the grant only to a story the session budget holds, and answers `409 not_budget_held` for any other.

### Projects from before story approval

When a project opens, the workbench brings the planning schemas in its spec store up to the ones it ships. A store whose branch has no upstream gets them in one git commit on that branch ("Update the planning schemas"). A store whose branch tracks an upstream, such as a clone of a GitHub repository, keeps its branch and its files as they are: the workbench commits the schemas onto the upstream branch on a branch of their own, `workbench/planning-schemas-<hash of the files>`, pushes it, and opens one pull request for it with `WORKBENCH_GITHUB_TOKEN`, which then needs to be allowed to create pull requests. A later start with the same schemas finds that branch and pull request and opens none. Until the pull request merges, the workbench validates the plan against the schemas it ships. The workbench also imports each breakdown approved in the old format (`status: approved` with a matching `approved_digest`) as an approval of all its stories, recorded as "Imported approval".

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

`scripts/check/ui.sh planner|approver|local` starts the workbench on `http://127.0.0.1:8799` against a fake Sortie with fake worker brokers (`scripts/check/fake_sortie.py`), with a small seeded plan whose story `billing/b1` runs as `HLAB-1`. `planner` and `approver` sign in through Dex (`scripts/check/dex.yaml`, in a container), as one person with the planner role, or with both roles; `local` uses local mode with the password `check`. Each run starts from a blank state in `~/.cache/plan-workbench-check`, and Ctrl-C stops everything. Drive the worker with `curl -X POST 127.0.0.1:7690/admin/next` (its next run) and `/admin/end` (no more runs); `/admin/park` stops `billing/b2` with a question to answer. With it running, `scripts/check/roles_api.sh planner|approver` checks, through the API, what that role may and may not do.

## Run everything live

`dev/live/live.sh` runs the whole loop on one machine with real agents: the workbench (`http://127.0.0.1:8791`, local mode, password `live`), Sortie under plan control (`127.0.0.1:7679`), `opencode acp` behind `acp-broker` for each worker, a local Gitea (`http://127.0.0.1:3001`, repository `sortie/live-lab`), and the local Huly project `LIVE`. Each setup step checks first, so a second start changes nothing; `--reset` starts over. The kit holds the Sortie workflow (`WORKFLOW.md`), the agents' own configuration home (`agent-config/`), and the OpenSpec change the planning agent breaks down (`store/`).

A run: ask the planning agent to break the change `pricing` into stories, and approve the epic; it syncs on its own. Sortie dispatches the released stories; each worker's change arrives as a pull request in Gitea and its story moves to `In Review`. Merge a pull request in Gitea: within a poll (30 seconds) Sortie moves the story to `Done` and dispatches the stories it unblocked. Small stories take an agent seconds, so `dev/live/take_over.sh <identifier>` waits for that story's worker and takes the floor through the workbench; then choose **Watch**, steer, and **Hand back**.

Needs: Docker, Go, `uv`, `npm`, the `openspec` CLI, and OpenCode signed in to Anthropic. The agents' configuration loads only the Anthropic sign-in plugin, not your own MCP servers or instructions. Ctrl-C stops Sortie and the workbench; `dev/forge/gitea.sh down` removes Gitea and its repositories.
