# Stage 0 kit

Stages 0 and 1 of the Ortus Nova spec store's `agent-platform-workers`
(design D13, D15, D18). On p1 it runs the one Sortie, under plan control,
which dispatches every repository under the `remote` kind, with the plan
workbench, acp-broker and the local Huly; the stage-1 edge: the edge, nono
and one SSH tunnel per worker host ([below](#the-stage-1-edge-p1)); and p1's
own worker daemon. On each worker host, taichi first, it runs only a worker
daemon ([below](#worker-hosts-taichi)). Where a job runs is the matcher's
choice, from what the job requires (spec store `agent-platform/one-sortie`).
GitHub is the forge. Everything is rootless: systemd user units and Huly's
podman containers.

```sh
dev/stage0/stage0.sh                  # set up or update, then (re)start the units
dev/stage0/stage0.sh --rebuild-pack   # also rebuild the smolvm packs (they are rebuilt by themselves when their inputs change)
dev/stage0/stage0.sh --update-sortie  # also move the Sortie checkout to origin/plan-bench (the timer does this by itself)
STAGE0_PROFILE=worker dev/stage0/stage0.sh   # on a worker host: the worker daemon only
dev/stage0/test-smolvm-version.sh     # check the smolvm version check against fake binaries
dev/stage0/test-store-sync.sh         # check the store's fast-forward against throwaway repositories
dev/stage0/test-deploy.sh             # check what each kind of merge does to the live kit, and the drain
dev/stage0/test-worker.sh             # check the workers' configuration and the retired Sortie
dev/stage0/test-merge-carry.sh        # check the carry of a worker host's Sortie's merge records into p1's
dev/stage0/dev-host-apply.sh [CMD...] # drain this host, apply a setup change (homelab: make dev-host), run stage0.sh, dispatch again
dev/stage0/test-review-gate.sh        # check the review gate, the fix runs and the operator's channel against Sortie (the sortie/ submodule, or SORTIE_SRC)
dev/stage0/test-notify-channel.sh     # check the operator's channel, with stubs
dev/stage0/test-preview.sh            # check the deploy, move and removal of a ui story's preview against a throwaway repository
dev/stage0/worker-host.sh add HOST    # give a worker host its tunnel and identity (the stage-1 edge)
dev/stage0/worker-host.sh carry HOST  # before a worker host's Sortie stops: its merge records into p1's Sortie
make -C dev/stage0 test               # the edge's tests and every test-*.sh but the live one
make -C dev/stage0 test-live          # on p1: the egress scenarios against the pinned nono
```

The kit needs smolvm 1.32.4 or later (`SMOLVM_MIN` in `smolvm-version.sh`); `stage0.sh` refuses an older one.

| Part | Where |
| --- | --- |
| Settings | `~/.local/share/ortus-nova/stage0/stage0.env`, written on the first run |
| Sortie | unit `ortus-stage0-sortie`, `127.0.0.1:7680`, workflow rendered from `WORKFLOW.md`: the `remote` kind, every repository |
| Workbench | unit `ortus-stage0-workbench`, `http://127.0.0.1:8793`, local mode; the password is in `workbench-password` |
| Huly | `http://localhost:8087`, workspace `sortie`, project `ONS`; `podman-restart.service` brings it back after a reboot |
| Pack | `stage0.smolmachine`, from `pack/`: Go, Node, opencode and Claude Code (`claude-agent-acp`); each worker builds its own |
| p1's worker | unit `ortus-stage0-worker`, `$STAGE0_HOME/bin/worker-daemon` built from `worker-daemon/`, configured by `$STAGE0_HOME/worker.json` (rendered by `worker-config.sh`); it dials the edge at `127.0.0.1:7700` on p1 with no tunnel, and the edge names it `p1` |
| Live kit | `$STAGE0_HOME/kit`, the kit's own detached worktree of `origin/main`: `stage0.sh` runs from it, whichever checkout you start it from, and the units run it. `ortus-stage0-deploy.timer` moves it to each merge within a minute (`deploy.sh`): a workbench change is rebuilt and the workbench restarted; a change to the rest of the kit, `worker-daemon/` included, runs `stage0.sh` again once no agent runs, and so does a merge to Sortie's `plan-bench` (`stage0.sh --update-sortie`; a Sortie checkout that is not behind `plan-bench` stays put). Such a change drains the host first (see [Drain before a change](#drain-before-a-change)); a failed build goes back to the running commit. One `stage0.sh` or deploy runs at a time (`$STAGE0_HOME/stage0.lock`): a second `stage0.sh` waits, and the timer tries again a minute later. `STAGE0_KIT_FROM_CHECKOUT=1 dev/stage0/stage0.sh` runs your checkout instead, to try a branch, and stops the timer |
| Previews | unit `ortus-stage0-previews.timer` every minute, and one `ortus-stage0-preview@<identifier>` per ui story's pull request; see [Previews of ui stories](#previews-of-ui-stories) |
| Spec store | `STAGE0_STORE`; `ortus-stage0-store.timer` fast-forwards it to its upstream every minute (`store-sync.sh`), and leaves it alone, saying why in its journal, when that could lose work: tracked changes, local commits, a detached HEAD |
| Stage-1 edge | units `ortus-stage0-edge`, `ortus-stage0-nono` and `ortus-stage0-worker-tunnel@<worker>`; see [The stage-1 edge](#the-stage-1-edge-p1) |
| Logs | `journalctl --user -u ortus-stage0-sortie` (and `-workbench`, `-store`, `-deploy`, `-edge`, `-nono`, `-worker`, `-worker-tunnel@<worker>`, `-previews`, `-preview@<identifier>`) |

## How a story runs

The workbench registers the spec store (`STAGE0_STORE`), which follows its
upstream on its own, so a merged plan change reaches it within a minute. About a second
after a change or an approval it syncs the plan on its own, and an approved story whose
blockers are approved is released to `Todo`; the workbench then asks Sortie
to poll at once. Sortie offers the story's job to the workers; the matcher
gives it to a worker whose declaration meets what the job requires (see
Repositories below), whichever host it is on. That worker creates a machine
from its pack, seeds it from the bundle Sortie serves, and runs the agent,
whose session Sortie drives over the stdio flow behind acp-broker on p1. The
worker returns the issue branch as a bundle; Sortie pushes it and opens the
pull request. When the pull request merges, Sortie moves the story to
`Done`. A homelab story released after a Sortie story merged is dispatched
by the same Sortie, like any other.

- **Review and merge.** Every story's pull request goes through the
  criteria gate, whatever its `review` value (`human`, `criteria` or
  `ui`; spec store `agent-platform/run-supervision`, design D5). The build rule
  (`build`, `claude-label` or `claude-title`) hops to the `review` rule
  (`gate: criteria`, stage label `sortie-stage:review`), a new run in a new
  agent session. The review checks each acceptance criterion with evidence,
  may add fix commits, which Sortie pushes, and never rewrites a pushed
  commit. When it passes, Sortie's `auto_merge` squashes the pull request
  once CI passes, and merge completion moves the story to `Done`: no person
  is involved. A criterion it cannot settle, such as one that conflicts
  with a design rule, stops it `blocked`: the pull request stays open, and
  Sortie parks the story with the review's question, which the workbench
  shows. Branch protection that requires an approving review would keep
  every pull request from merging; the kit's repositories must not require
  one. Sortie merges a `review: human` story after a review only from the
  Sortie commit that lets its gate do so (`planReviewGate`); with an older
  Sortie a person still merges those. A `review: ui` story
  (`sortie-review:ui`) is merged by Sortie too, with no person involved,
  once its review and CI pass and the review on its preview
  ([below](#previews-of-ui-stories)) records its click-proofs for the head
  (sortie#42). `test-review-gate.sh` checks all of this against
  Sortie's own loader, exit and merge code: it needs the `sortie/`
  submodule, or `SORTIE_SRC` naming a Sortie checkout.

- **CI failures and merge conflicts.** The workflow turns on Sortie's
  `ci_failure` and `merge_conflicts` reactions (spec store
  `agent-platform/run-supervision`). A pull request whose CI fails, or that
  conflicts with its base after another merge, gets a fix run on the
  `review` rule with the workflow's fix prompt: it merges `origin/<base>`
  in, never rebases or rewrites a pushed commit, fixes the cause on the
  branch, including a CI or toolchain setting the change exposed (an old Go
  is raised to the latest stable release), and checks the criteria again.
  A later turn of the run gets only a short continuation: the earlier
  commits are in the workspace and `origin/<branch>` moves only after the
  run, so the agent does not redo the fix. Sortie pushes its commits, and `auto_merge` merges once CI passes. A pull
  request that still fails after `max_retries` fix runs (2 for CI, 1 for a
  conflict) is a stopped failure: Sortie escalates it, comments on the
  story, and runs no further fix until a new commit lands or a person
  answers it. Neither watch has a window. The CI reaction reads each pull
  request's checks from the pull request's own repository, so a `sortie` or
  `homelab` pull request on p1 gets CI fix runs as an `agentic-orchestration`
  one does; `github.project` only names the CI provider's configured
  repository.
  `test-review-gate.sh` checks the fix runs too.

- **Comments on a pull request.** On p1 `review_comments` is on: a comment
  on a ui story's pull request that no bot wrote, apart from a click-proofs
  record, or a review that requests changes on any story, starts a run on
  the review rule with the comments (at most 5 per story, a minute after the
  last comment). The kit's own comment with a preview's address
  (`<!-- workbench-preview -->`, which GitHub does not mark as a bot's
  because the kit posts it with the operator's token) starts the review on
  the preview, with its click-proofs prompt; any other comment starts a fix
  run with the fix prompt.
- **Harness.** Every story runs opencode (`anthropic/claude-opus-5-5`),
  unless its title holds `[claude]` or it has the Huly label `claude-code`;
  then its build runs Claude Code. The review always runs opencode.
  `pack/agent-acp` picks one from `SORTIE_RULE`. Both use the operator's
  Claude subscription. No other model provider is configured.
- **opencode 2.** The pack runs opencode 2.2.1 (smol-builder's base layer)
  with the Claude sign-in plugin 2.0.0-next.6. opencode 2 keeps credentials
  in its own store, so `pack/agent-acp` imports the placeholder login with
  `opencode auth import` before each start.
- **No permission prompts.** The VM is the sandbox, so neither harness asks
  before acting: opencode's `permissions` allow everything except `webfetch`
  (`pack/opencode.json`), and Claude Code runs with `bypassPermissions`
  (`pack/claude-settings.json`). Sortie refuses consent on unattended runs,
  so a prompt would hold the run until acp-broker's 10-minute timeout.
- **Web search.** opencode's `websearch` uses Firecrawl's keyless MCP
  (`websearch.provider` in `pack/opencode.json`), so `egress-profile.json`
  allows `mcp.firecrawl.dev` and no credential is bound. With no provider set,
  opencode asks the user to pick one on the first search, and Sortie
  refuses that prompt.
- **What the agent reads.** The VM has no copy of the spec store and no
  network beyond the workflow's hosts, so the prompt carries everything:
  the story's Huly description holds its notes, its acceptance criteria,
  and the text and scenarios of each requirement it covers. When a person
  releases a parked story, the first turn of the next run also holds the
  agent's stop statement and every comment posted since the park.
- **Session start.** `agent.read_timeout_ms` is 60 s: a fresh VM's agent
  can take over 15 s to answer at session start, past Sortie's 5 s
  default. Turns are bounded by `stall_timeout_ms` and `turn_timeout_ms`.
- **Parallel agents.** `STAGE0_MAX_AGENTS` in p1's `stage0.env` (default 3)
  caps agents at once, on every worker together; on a worker it is the
  daemon's run slots. All of them share one Claude subscription and its rate
  limits.
- **Repositories.** A story runs in the repository its plan node names
  (plan sync keeps the Huly label `sortie-repository:<name>`), else in
  `sortie`, the default. The workflow lists three, all dispatched by p1's
  Sortie; no other Sortie polls the project. Each names what its job
  requires, never a host:

  | Name | Settings | Default | Image | Requires |
  | --- | --- | --- | --- | --- |
  | `sortie` | `STAGE0_REPO_*` | yes | `stage0.smolmachine` | 4 CPUs, 8 GiB, 20 GiB of disk |
  | `agentic-orchestration` | `STAGE0_AO_REPO_*` | no | `stage0.smolmachine` | 4 CPUs, 8 GiB, 20 GiB of disk |
  | `homelab` | `STAGE0_HOMELAB_REPO_*` | no | `homelab.smolmachine` | `nested`, 8 CPUs, 20 GiB, 60 GiB of disk |

  A worker declares `nested` only when it has homelab's inputs, so any
  worker may take a sortie story and any worker with homelab's dev host a
  homelab one. Plan sync refuses a story naming a repository the workflow
  does not list. A `stage0.env` written before these settings
  existed keeps working: `stage0.sh` fills in each missing one with its
  default. To change one, set it in `stage0.env` and run `stage0.sh` again.

## Previews of ui stories

A `review: ui` story changes an interface: the operator approves it as a
design prototype before the story is written, its acceptance criteria name
that prototype, and after that no person approves or merges it (spec store
`agent-platform/run-supervision`, "A UI is approved as a prototype, and the
built story merges after its review"). Sortie merges its pull request once
its review and CI pass and the pull request records the click-proofs on its
preview for its head, a comment line `sortie:click-proofs passed <head sha>`
(sortie#42). Every minute `ortus-stage0-previews.timer` runs
`run-previews.sh`, one pass of `workbench preview-sync` against Sortie and
GitHub (`STAGE0_AO_REPO_PROJECT`):

- A ui story whose run is over, that waits in `In Review` (its review
  passed), and whose pull request (`sortie/<identifier>`) passes CI on its
  head gets a preview: `preview.sh up` checks that head out in
  `$STAGE0_HOME/previews/<identifier>/src`, builds its workbench, copies
  the live registry into its own home, and starts
  `ortus-stage0-preview@<identifier>`: `run-workbench.sh <identifier>`,
  the branch's workbench with `WORKBENCH_SYNC=0`, against the live store
  and Sortie, on `127.0.0.1:<port>` from `STAGE0_PREVIEW_FIRST_PORT` (8800)
  up. The pull request gets a comment with the address and the commit, and
  the live workbench shows the address on the story, from
  `$STAGE0_HOME/previews.json`.
- That comment starts the review on the preview (`review_comments`, with
  the workflow's click-proofs prompt): a run on the review rule that clicks
  every action the story's criteria name on the preview and, when they
  pass, comments the click-proofs record for the head on the pull request
  through GitHub's API. Sortie then merges it. Until then the workbench
  shows the story as waiting for click-proofs, and once the record names the
  head, as merging.
- The preview follows the branch: a fix run's new head is deployed once its
  run is over and CI passes on it, and gets its own comment and review; a
  record for an earlier commit does not count. Until then the last one stays
  up.
- When the pull request closes, merged or not, `preview.sh down` stops and
  removes it. A merge makes the story Done, and the change deploys as any
  workbench merge does.

The address is a name beside the live workbench's TLS name,
`STAGE0_WORKBENCH_URL` in `stage0.env`: `https://workbench.example.net`
gives `https://workbench-ons-93.example.net/`, so a wildcard certificate
for the live name's domain covers it. The kit does not serve TLS itself:
each change rewrites `$STAGE0_HOME/previews/routes`, one `HOST PORT` line
per preview, for the proxy in front of the live workbench, and runs
`STAGE0_PREVIEW_ROUTES_HOOK` (for example, that proxy's reload). Without
`STAGE0_WORKBENCH_URL` the address is the preview's loopback port.

The review on the preview runs in a job's machine, so it reaches the preview
only through the job's egress: `egress-profile.json` must allow the
preview's name, and the preview must let the review sign in. The kit sets up
neither by itself.

A preview runs the branch's code on this host with the kit's Sortie and
GitHub tokens; `WORKBENCH_SYNC=0` keeps the workbench from writing to the
tracker, and the branch has passed its criteria review and CI first.

## Drain before a change

A change that must not land under a running agent drains the host first
(spec store `agent-platform/run-supervision`, "A pending deploy drains its
host"): Sortie stops dispatching new runs, the runs already going finish,
the change applies, and Sortie dispatches again. The change waits for the
runs that were going when it arrived, never behind the queue of stories.
On a busy p1 running 2 stories with 9 waiting, a kit merge starts no
third run, applies when the 2 finish, and then the 9 are dispatched. A
worker host runs no Sortie to pause: there the change waits until its
worker daemon runs no job.

- **Deploys.** `deploy.sh` pauses dispatch each minute a kit or Sortie
  change waits, and resumes it once `stage0.sh` has run, or once the change
  has failed and been put back.
- **Host setup changes**, such as a smolvm bump. Run
  `dev/stage0/dev-host-apply.sh` on the host instead of applying the change
  by hand. On p1 it pauses dispatch, waits for the running runs (every
  `STAGE0_DRAIN_POLL` seconds, default 15), runs the apply, runs
  `stage0.sh` so that the pack and Sortie follow the new smolvm, and
  resumes dispatch, also when a step fails. On a worker host it waits until
  the worker daemon runs no job, stops the daemon for the apply, runs
  `stage0.sh`, and starts it again, also when a step fails. On a worker host
  with homelab (taichi) the apply is homelab's `make dev-host` in
  `STAGE0_HOMELAB_SRC`; elsewhere, name it:
  `dev/stage0/dev-host-apply.sh <command>`. It holds stage0.sh's lock
  throughout, so no deploy runs in between.

`drain.sh` asks Sortie for this with its control token:

| Call | What Sortie does |
| --- | --- |
| `POST /api/v1/dispatch/pause` | starts no new run, and no retry; the running ones go on |
| `POST /api/v1/dispatch/resume` | dispatches again |
| `GET /api/v1/state` | `running` lists the runs still going |

`$STAGE0_HOME/dispatch.paused` records that the kit paused dispatch, and
why, until the resume. A Sortie without the pause (it answers with an
error) is waited for as before: the change applies only when that Sortie
happens to have no run going, and `journalctl --user -u ortus-stage0-deploy`
says `Sortie did not pause dispatch`.

## Credentials

`run-sortie.sh` reads each credential at start and keeps it in memory. A
job's machine sees only Sortie's placeholders, which nono on p1 replaces
([below](#where-the-credentials-live)).

| Credential | Source |
| --- | --- |
| GitHub | `github-token` in the stage-0 home, else `gh auth token`. `run-workbench.sh` reads the same one, so the workbench looks up the pull requests a story names in `delivered_by` with no credential of its own; restart the workbench unit after changing it |
| Claude subscription, for both harnesses | `claude-oauth-token` (from `claude setup-token`); else the freshest live OAuth login: opencode's (`~/.local/share/opencode/opencode.db`) or Claude Code's (`~/.claude/.credentials.json`). `anthropic-token.py` picks it. Sortie hands it to no job: nono holds it and puts it in each job's egress to `api.anthropic.com` (see Egress below); `refresh-token.sh` restarts nono onto a fresher login |
| Huly | a login with the dev account in `stage0.env` |
| The operator's channel | `notify-credential` in the stage-0 home, mode 0600, written by the operator only; see [below](#the-operators-channel) |

Git reaches GitHub over HTTPS through a credential helper in the kit's own
`gitconfig` (`GIT_CONFIG_GLOBAL`). It includes `~/.gitconfig` and holds no
secret.

## The operator's channel

Every question for a person reaches the workbench, wherever the story
runs (p1 or a worker host such as taichi), and can reach the operator's
channel (spec store `agent-platform/run-supervision`, "A
person is alerted, wherever the question is held"). The channel and its
credential are the operator's choice: any kind Sortie's notifications have.
The channel is optional: with no `STAGE0_NOTIFY_KIND`, the default, no
channel is configured and `stage0.sh`, `run-sortie.sh` and the workbench
start without one.

| Where | What it sends |
| --- | --- |
| The workflow, through Sortie's notifications (`notify-channel.sh` renders the entry) | the moment a run stops for a person (`session.stopped`: an agent's park, or a review that cannot settle a criterion, with its question), a question or permission request the broker escalates and a failed criteria check (`session.escalated`), a budget hold (`budget.held`), a merge that needs a person (`escalation.auto_merge`), and a pull request that still fails CI or conflicts with its base after its fix runs (`escalation.ci_failure`, `escalation.merge_conflicts`) |
| The workbench on p1, which reads the questions of p1's Sortie and of each other Sortie in `WORKBENCH_SORTIES` | each open question within one poll, with the story, the Sortie that holds it, and a link to the project's home on `STAGE0_WORKBENCH_PUBLIC_URL`, where it is answered; then again every 4 hours while it stays open, and never once it is answered or released |

To set the channel, on p1 (a worker host runs no Sortie and needs none):

1. Choose the kind in `$STAGE0_HOME/stage0.env`:

   ```sh
   STAGE0_NOTIFY_KIND=slack        # slack, discord, telegram or webhook
   STAGE0_NOTIFY_CHAT_ID=          # telegram only: the chat id, or @channel
   STAGE0_WORKBENCH_PUBLIC_URL=https://workbench.homelab.dev   # the links in each question
   ```

2. Write the credential, and nothing else, into `$STAGE0_HOME/notify-credential`
   yourself; no script writes it:

   ```sh
   (umask 077; printf '%s\n' 'https://hooks.slack.com/services/…' > ~/.local/share/ortus-nova/stage0/notify-credential)
   ```

   | Kind | The file holds |
   | --- | --- |
   | `slack` | the incoming webhook URL |
   | `discord` | the channel webhook URL |
   | `telegram` | the bot's token (and `STAGE0_NOTIFY_CHAT_ID` names the chat) |
   | `webhook` | the URL that receives Sortie's and the workbench's JSON |

3. Run `dev/stage0/stage0.sh` again. It renders the entry into the
   workflow and refuses a credential file that is missing, not yours, or
   not mode 0600; `run-sortie.sh` and the workbench refuse it too. Sortie
   reads it into its own environment only (`SORTIE_NOTIFY_SECRET`); the
   workbench reads the file itself. With no `STAGE0_NOTIFY_KIND`, the
   questions show on the workbench only, and `stage0.sh` says so.

Each question names the Sortie that holds it, and an answer goes to that
Sortie. To check it, park a story that runs on taichi: within one refresh it
is first under **Needs a person** on the workbench, and the channel, when
one is set, has it with the link.

## The stage-1 edge (p1)

How the control plane reaches worker hosts until Pomerium and the CA in
control-plane-images replace it (spec store `agent-platform-workers`,
design D18; no worker change then). Only the control profile runs it.

| Part | Where |
| --- | --- |
| Tunnels | unit `ortus-stage0-worker-tunnel@<worker>` per worker host: p1 opens `ssh -R 127.0.0.1:7700:127.0.0.1:<edge port>` to the host and keeps it up. On the worker host the edge is `http://127.0.0.1:7700` (`STAGE0_EDGE_WORKER_PORT`); nothing listens on the LAN, and the host gets no SSH access to p1 |
| Edge | unit `ortus-stage0-edge`, `$STAGE0_HOME/bin/stage0-edge` built from `edge/`, configured by `$STAGE0_HOME/edge.json` (rendered by `edge-config.sh`). One port per worker on p1's loopback: `127.0.0.1:7700` for p1's own worker, which needs no tunnel, and one per worker host from `127.0.0.1:7701` up, and nothing else |
| nono | unit `ortus-stage0-nono`, `nono proxy` on `127.0.0.1:7690` (`STAGE0_NONO_PORT`), with `egress-profile.json`. nono is pinned by the SHA-256 of its release archive in `nono-version.sh`; `stage0.sh` installs it into `$STAGE0_HOME/bin` and refuses any other archive. `make check-latest` fails once a newer stable release is out |
| Worker hosts | `$STAGE0_HOME/workers/<worker>.env`: the SSH destination and the edge port, or `LOCAL=1` for p1's own worker (`stage0.sh` writes it); `dev/stage0/worker-host.sh list` |

What the edge does with a request on a worker's port:

- **Identity.** The port names the worker. The edge sets
  `X-Sortie-Worker-Identity` (Sortie's `worker.identity_header`) to that
  worker and drops any value the request brings, so taichi's daemon that
  says `p1` reaches Sortie as `taichi`.
- **Worker API.** `/api/v1/workers/...` goes to Sortie. No other Sortie
  route is reachable, not token introspection, not `/api/v1/state`.
- **Path flows.** `/stdio/...`, `/mcp/...`, `/bundles/...` and `/usage/...`
  go to the service of each flow (`STAGE0_EDGE_<FLOW>_URL` in `stage0.env`,
  Sortie by default), with the flow's prefix removed. The edge first asks
  Sortie's `POST /api/v1/tokens/introspect` (with the control token) about
  the request's `Authorization: Bearer` job token, and admits it only when
  the token is live and granted for that flow; anything else gets `401`
  and never reaches the service.
- **Egress.** A `CONNECT` is the egress flow; its job token is the
  `Proxy-Authorization` (Bearer, or Basic as in
  `HTTPS_PROXY=http://job:<token>@127.0.0.1:7700`). Once admitted, the
  edge tunnels it through nono, which allows only the hosts in
  `egress-profile.json`, the one list of the hosts a job of any repository
  may reach, with the forge's and those of the credentials nono holds
  (`edge/profile_test.go` checks it holds each credential `WORKFLOW.md`
  binds). The remote kind takes no `allow_hosts`, so the workflow has none;
  add a host a chain needs to `egress-profile.json`. For a credential's host nono intercepts TLS with the
  kit's egress CA and replaces the placeholder Sortie gives the machine
  (`sortie-credential-<name>`) with the credential. A refusal is recorded in
  the edge's journal with the worker, job, attempt and host:
  `msg="egress refused" worker=taichi job=ONS-7 attempt=1 host=pastebin.com:443 status=403`.

A remote-kind workflow names the flows as the worker host sees them:
`http://127.0.0.1:7700/<flow>` for stdio, mcp, bundles and usage, and
`http://127.0.0.1:7700` for egress.

### Add a worker host

On p1, once `stage0.sh` has run:

```sh
dev/stage0/worker-host.sh add brian@taichi        # the worker is named taichi
dev/stage0/worker-host.sh add mini.lan mac-mini   # or name it yourself
dev/stage0/worker-host.sh list
dev/stage0/worker-host.sh remove taichi
```

`add` needs `ssh -o BatchMode=yes HOST true` to work from p1. It gives the
host the next free edge port, writes `$STAGE0_HOME/workers/<worker>.env`,
copies the egress CA's certificate to `~/.config/ortus-nova/egress-ca.crt`
on the host (its machines trust nono's interception with it; the key stays
on p1), enables and starts the host's tunnel unit, restarts the edge with the
new port, and checks that the host reaches the edge at
`http://127.0.0.1:7700`. The worker's name is the identity Sortie sees;
adding the same name again keeps its port. Sortie itself is not restarted.

### Where the credentials live

All on p1; none is stored on a worker host, which holds only the CA's
certificate.

| What | Where |
| --- | --- |
| Model (`anthropic`, `api.anthropic.com`) | the Claude subscription token Sortie uses (see Credentials above), read by `run-nono.sh` at start into nono's environment (`egress-credentials.sh`); nono reads it only at start, so `ortus-stage0-token.timer` restarts nono alone onto a fresher one while no agent runs, or at once when its token has expired |
| Forge (`github_api`, `api.github.com`; `github_git`, `github.com` for git over HTTPS) | `github-token` in the stage-0 home, else `gh auth token`, read the same way |
| Sortie's control token, for introspection | `sortie-token` in the stage-0 home |
| The edge's credential to nono | `nono-pass` in the stage-0 home |
| Egress CA | `egress-ca/ca.key` (mode 0600) and `egress-ca/ca.crt` in the stage-0 home |

`make -C dev/stage0 test` runs every scenario against a fake Sortie, with
a stand-in for nono; `make -C dev/stage0 test-live` runs the two nono
scenarios on p1 against the pinned nono, the credential on p1 and
`api.anthropic.com`, on ports of its own.

## Worker hosts (taichi)

A worker host runs only a worker daemon (`worker-daemon/`, its
[operator guide](../../worker-daemon/docs/operator-guide.md)). It runs no
Sortie, polls no tracker and holds no credential: its jobs' model and forge
requests get theirs from nono on p1. taichi's homelab Sortie, the second
Sortie of stage 0, is retired: p1's Sortie dispatches homelab too.

```sh
# On p1, once: the host's tunnel and identity, and the egress CA's certificate.
dev/stage0/worker-host.sh add brian@taichi
# On the host: the first run writes stage0.env with the profile.
STAGE0_PROFILE=worker dev/stage0/stage0.sh
dev/stage0/stage0.sh            # later runs read it from stage0.env
```

| Part | Where |
| --- | --- |
| Worker | unit `ortus-stage0-worker`, `$STAGE0_HOME/bin/worker-daemon` built from `worker-daemon/`, configured by `$STAGE0_HOME/worker.json` (`worker-config.sh`), state in `$STAGE0_HOME/worker`. It dials p1's edge at `http://127.0.0.1:7700`, the end of p1's tunnel, and the edge names it by that tunnel |
| Live kit | as on p1, `$STAGE0_HOME/kit` follows `origin/main` (`ortus-stage0-deploy.timer`); a change to the kit runs `stage0.sh` (the worker profile) once the daemon runs no job. A worker host builds no Sortie, follows no `plan-bench` merge and asks no Sortie to pause or resume, whatever Sortie or Huly settings its `stage0.env` kept from the retired homelab profile; nothing on it reads them |
| Packs | `stage0.smolmachine` from `pack/`, as on p1, for sortie and agentic-orchestration jobs; and, with homelab's dev host, `$STAGE0_HOME/packs/homelab-<hash>.smolmachine` (below) |
| Egress CA | `~/.config/ortus-nova/egress-ca.crt`, from `worker-host.sh add`. Each machine's setup puts it in the guest's trust store and `NODE_EXTRA_CA_CERTS`, so nono's interception of the credential hosts is trusted |

p1's workbench shows taichi's workers beside p1's, as workers of p1's
Sortie, the only one (`agent-platform/run-supervision`); the brokers of
their agents run on p1. The forwards to taichi's own Sortie and its brokers
that `huly-tunnel.sh` opened while taichi ran one are retired with it:
`stage0.sh` on p1 closes the tunnels and deletes the files they left for the
workbench (`$STAGE0_HOME/tunnels/` and `sorties/`). p1's Sortie is named
after p1's host name; set `STAGE0_SORTIE_NAME` in `stage0.env` to name it
otherwise.

### homelab's inputs

With homelab's dev host (`make dev-host` in homelab) and its checkout at
`STAGE0_HOMELAB_SRC` (default `~/dev/homelab`), `stage0.sh` gives the
worker the homelab image (`homelab-inputs.sh`) and the worker declares
`nested`. Without them it declares no `nested`, so the matcher gives it no
homelab job. What a homelab job gets is what the homelab Sortie gave its
machines (spec store `run-homelab-through-sortie`, design D5-D12):

| Part | Where |
| --- | --- |
| homelab | the kit's own detached worktree of `origin/master` in `$STAGE0_HOME/homelab`, added from `STAGE0_HOMELAB_SRC`; every `stage0.sh` run moves it to the latest `origin/master` |
| Pack | `$STAGE0_HOME/packs/homelab-<hash>.smolmachine`, from `homelab-pack/`: homelab's sandbox image scripts plus the harness (`pack/install-harness.sh`), for homelab's guest user `builder`, created with the host user's uid and gid so it can write the shared folders. The daemon runs its commands as `builder` (`smolvm.images`); a pack does not carry its image's `USER`. The hash covers the staged inputs, smol-builder and the smolvm version, so a change to any of them builds a new pack. While no job runs, the packs it replaced are deleted |
| Machine | what the job requires (WORKFLOW.md): 8 vCPU, 20 GiB, a 60 GiB disk, nested KVM; and homelab's guest kernel (`lib_dir`, `SMOLVM_LIB_DIR`) |
| Shared folders | read-only, `~/.cache/homelab-sources`; read-write, `~/.cache/homelab-vmhost`, homelab's uv cache and its Go cache (one folder); per job, `~/.local/state/homelab/sortie-recorder/<job>`, the flight recorder's folder, which stays after the machine is gone. Five, beside the daemon's share: smolvm's ceiling on taichi |
| vm key | copied alone into `$STAGE0_HOME/inputs/vm-age`; reaches every command in the machine as `HOMELAB_VM_AGE_KEY` (`env_files`), never logged |
| Setup | what the homelab workflow's hooks did, before the agent starts: the vm key where sops looks, `guest-start.sh` (the flight recorder, and swap at `/storage/swapfile`), and `scripts/vmhost/prepare.sh` |

The recorder folders stay until you delete them:
`rm -rf ~/.local/state/homelab/sortie-recorder/<job>` once a job's
evidence is no longer needed.

### From the homelab profile

A host on the retired homelab profile keeps its Sortie until a person moves
it: `stage0.sh`, the deploy timer's too, refuses the homelab profile and
says how. Once p1 runs this kit's `main` (its Sortie lists homelab open to
dispatch, and p1 has closed the Huly tunnel taichi's Sortie polled through,
`ortus-stage0-huly-tunnel@taichi`, so that Sortie dispatches nothing more)
and `worker-host.sh add` has run for the host:

```sh
dev/stage0/worker-host.sh carry brian@taichi   # on p1: taichi's merge records into p1's Sortie
STAGE0_PROFILE=worker dev/stage0/stage0.sh     # on taichi
```

Sortie lets a planned story start only from its own record that each
blocker merged (spec store `sortie/blocker-satisfaction`), the rows of its
`plan_satisfaction` table, and p1's Sortie has none for the stories that
merged under taichi's. `carry` reads every row of taichi's Sortie's
database (`$STAGE0_HOME/sortie/.sortie.db` there) over SSH, adds each one
p1's Sortie lacks to its database (`merges.py`), keeping p1's own, and
leaves the list it carried on taichi (`merges-carried.json`). A second
carry changes nothing; `test-merge-carry.sh` checks all of this.

`stage0.sh` on taichi then rewrites `stage0.env` to the worker profile and,
only once p1's Sortie holds every record taichi's holds, stops taichi's
Sortie, removes its unit, and starts the worker daemon. While a record is
missing, it says how many, leaves the Sortie running and fails: carry and
run it again. The homelab Sortie's database and workspaces stay in
`$STAGE0_HOME`; delete each `sortie-*` machine it left with
`smolvm machine delete --name <name>`. Its two credential files,
`github-token` and `claude-oauth-token`, are no longer read: delete them
and revoke the tokens.

To remove a worker host: on the host, `systemctl --user disable --now
ortus-stage0-worker ortus-stage0-deploy.timer`, then
`git -C ~/dev/homelab worktree remove ~/.local/share/ortus-nova/stage0/homelab`
and `rm -rf ~/.local/share/ortus-nova/stage0`. On p1:
`dev/stage0/worker-host.sh remove <worker>`.

## Remove it

```sh
for w in $(dev/stage0/worker-host.sh list | grep -v '	local	' | cut -f1); do dev/stage0/worker-host.sh remove "$w"; done
systemctl --user disable --now ortus-stage0-sortie ortus-stage0-workbench ortus-stage0-edge ortus-stage0-nono ortus-stage0-worker
smolvm machine ls   # delete each wd-* machine: smolvm machine delete --name <name>
rm ~/.config/systemd/user/ortus-stage0-*.service && systemctl --user daemon-reload
rm -rf ~/.local/share/ortus-nova/stage0   # state, tokens, workspaces and the pack
```
