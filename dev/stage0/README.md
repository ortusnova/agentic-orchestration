# Stage 0 kit

Stages 0 and 1 of the Ortus Nova spec store's `agent-platform-workers`
(design D13, D15, D18). On p1 it runs the one Sortie, under plan control,
which dispatches every repository under the `remote` kind, with the plan
workbench, acp-broker and the local Huly; the control plane's front, which
replaced the stage-1 edge and its SSH tunnels: Pomerium, the egress gate and
nono ([below](#worker-hosts-p1)); and p1's own worker daemon. On each worker
host, taichi first, it runs only a worker daemon
([below](#worker-hosts-taichi)). Where a job runs is the matcher's choice,
from what the job requires (spec store `agent-platform/one-sortie`). GitHub
is the forge. Everything is rootless: systemd user units and podman
containers.

```sh
dev/stage0/stage0.sh                  # set up or update, then (re)start the units
dev/stage0/stage0.sh --rebuild-pack   # also rebuild the smolvm packs (they are rebuilt by themselves when their inputs change)
dev/stage0/stage0.sh --update-sortie  # also move the Sortie checkout to origin/plan-bench (the timer does this by itself)
STAGE0_PROFILE=worker dev/stage0/stage0.sh   # on a worker host: the worker daemon only
dev/stage0/test-smolvm-version.sh     # check the smolvm version check against fake binaries
dev/stage0/test-store-sync.sh         # check the store's fast-forward against throwaway repositories
dev/stage0/test-deploy.sh             # check what each kind of merge does to the live kit, with no drain
dev/stage0/test-unit-restart.sh       # check that nono, the egress gate, Pomerium and the worker daemon restart only when what they run changed
dev/stage0/test-worker-restart.sh     # check that a test-only change under worker-daemon/ leaves the daemon running and a code change restarts it
dev/stage0/test-worker.sh             # check the workers' configuration and the retired Sortie
dev/stage0/test-front.sh              # check Pomerium's and the egress gate's units on p1, with stubs
dev/stage0/test-worker-host.sh        # check a worker host's certificate, its issue and its revocation
dev/stage0/test-retire-edge.sh        # check that the stage-1 edge's units and tunnels are retired
dev/stage0/test-merge-carry.sh        # check the carry of a worker host's Sortie's merge records into p1's
dev/stage0/dev-host-apply.sh [CMD...] # apply a setup change at once (homelab: make dev-host), then run stage0.sh
dev/stage0/test-review-gate.sh        # check the review gate, the fix runs and the operator's channel against Sortie (the sortie/ submodule, or SORTIE_SRC)
dev/stage0/test-notify-channel.sh     # check the operator's channel, with stubs
dev/stage0/test-preview.sh            # check the deploy, move and removal of a ui story's preview against a throwaway repository
dev/stage0/test-supervisor.sh         # check the supervisor's token, prompt, pause and status, with stubs
dev/stage0/supervisor.sh status       # the supervisor's status, budget, open questions and recent actions with their evidence
dev/stage0/supervisor.sh pause [WHY]  # pause the supervisor, until resume, across restarts
dev/stage0/supervisor.sh resume       # resume it; a session starts at once for what waited
dev/stage0/worker-host.sh add HOST    # give a worker host its client certificate from the control plane's CA
dev/stage0/worker-host.sh remove NAME # revoke it: Pomerium refuses the worker on every route
dev/stage0/worker-host.sh carry HOST  # before a worker host's Sortie stops: its merge records into p1's Sortie
make -C dev/stage0 test               # stage0-live-proofs' and the workflow's tests and every test-*.sh but the live one
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
| p1's worker | unit `ortus-stage0-worker`, `$STAGE0_HOME/bin/worker-daemon` built from `worker-daemon/`, configured by `$STAGE0_HOME/worker.json` (rendered by `worker-config.sh`); it dials Pomerium at `STAGE0_CP_PUBLIC_URL` with its own client certificate (`$STAGE0_HOME/control-plane/workers/p1`), as every worker host does, and Pomerium names it `p1` |
| Live kit | `$STAGE0_HOME/kit`, the kit's own detached worktree of `origin/main`: `stage0.sh` runs from it, whichever checkout you start it from, and the units run it. `ortus-stage0-deploy.timer` moves it to each merge within a minute (`deploy.sh`): a workbench change is rebuilt and the workbench restarted; a change to the rest of the kit, `worker-daemon/` included, runs `stage0.sh` again at once, and so does a merge to Sortie's `plan-bench` (`stage0.sh --update-sortie`; a Sortie checkout that is not behind `plan-bench` stays put). No change drains the host (see [A deploy never drains](#a-deploy-never-drains)); a failed build goes back to the running commit. One `stage0.sh` or deploy runs at a time (`$STAGE0_HOME/stage0.lock`): a second `stage0.sh` waits, and the timer tries again a minute later. `STAGE0_KIT_FROM_CHECKOUT=1 dev/stage0/stage0.sh` runs your checkout instead, to try a branch, and stops the timer |
| Previews | unit `ortus-stage0-previews.timer` every minute, and one `ortus-stage0-preview@<identifier>` per ui story's pull request; see [Previews of ui stories](#previews-of-ui-stories) |
| Live proofs | unit `ortus-stage0-live-proofs.timer` every minute (`run-live-proofs.sh`, `$STAGE0_HOME/bin/stage0-live-proofs` built from `live-proofs/`), and the live-proof nono, unit `ortus-stage0-nono-live-proof`; see [Live proofs](#live-proofs) |
| Spec store | `STAGE0_STORE`; `ortus-stage0-store.timer` fast-forwards it to its upstream every minute (`store-sync.sh`), and leaves it alone, saying why in its journal, when that could lose work: tracked changes, local commits, a detached HEAD |
| Control plane's front | units `ortus-stage0-pomerium`, `ortus-stage0-egress-gate`, `ortus-stage0-nono` and `ortus-stage0-nono-live-proof`; see [Worker hosts](#worker-hosts-p1) |
| Supervisor | one agent session at a time beside the stories, run by Sortie on p1 with the host's opencode: prompt `supervisor.md`, installed as `$STAGE0_HOME/sortie/supervisor.md`, token `supervisor-token`; see [The supervisor](#the-supervisor) |
| Logs | `journalctl --user -u ortus-stage0-sortie` (and `-workbench`, `-store`, `-deploy`, `-pomerium`, `-egress-gate`, `-nono`, `-nono-live-proof`, `-live-proofs`, `-worker`, `-previews`, `-preview@<identifier>`) |

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
  commit. It records its verdict on each criterion in
  `.sortie/criteria.json` (`met`, `not-met`, `live-proof` or `unsettled`,
  with evidence), which the worker returns to Sortie with the branch. Only
  when every criterion is `met` does Sortie's `auto_merge` squash the pull
  request, once CI passes, and merge completion move the story to `Done`:
  no person is involved. A criterion `not-met`, or no verdict, keeps the
  pull request open and parks the story with those criteria as the
  supervisor's question. A criterion it cannot settle, such as one that
  conflicts with a design rule, is `unsettled` and stops it `blocked`: the
  pull request stays open, and Sortie parks the story with the review's
  question, which the workbench shows. Branch protection that requires an approving review would keep
  every pull request from merging; the kit's repositories must not require
  one. Sortie merges a `review: human` story after a review only from the
  Sortie commit that lets its gate do so (`planReviewGate`); with an older
  Sortie a person still merges those. A `review: ui` story
  (`sortie-review:ui`) is merged by Sortie too, with no person involved,
  once its review and CI pass and the review on its preview
  ([below](#previews-of-ui-stories)) records its click-proofs for the head
  (sortie#42). A criterion that needs a live run its machine cannot do is
  not settled by the review but by a live-proof job, and the pull request
  merges only once that job proves it ([below](#live-proofs)).
  `test-review-gate.sh` checks all of this against
  Sortie's own loader, exit and merge code: it needs the `sortie/`
  submodule, or `SORTIE_SRC` naming a Sortie checkout. Without either it
  skips, and with `STAGE0_TEST_NO_SKIP=1`, as in CI, it fails instead.

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
  run with the fix prompt. The triage passes over a click-proofs record and
  a live-proof job's result ([below](#live-proofs)).
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
  (`websearch.provider` in `pack/opencode.json`), so `control-plane/nono/egress-profile.json`
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
- **Parallel agents.** A worker's run slots follow its host: as many of
  the smallest job (4 CPUs, 8 GiB) as its CPUs and memory hold above the
  reserve (2 CPUs, 8 GiB), at least one, which the worker daemon reads at
  each check-in (p1 5, taichi 7 in 2026-11). `STAGE0_WORKER_SLOTS` in a
  host's `stage0.env` sets them instead. A slot only lets a job be offered:
  Sortie places a job only on a worker with a free slot and the CPU, memory
  and disk the job requires free now, and free memory is the host's
  `MemAvailable`, so what a model server or a sandbox on the host takes is
  not offered. Sortie's own cap, `agent.max_concurrent_agents`, is only a
  ceiling above every worker's slots together: 16 unless
  `STAGE0_SORTIE_MAX_AGENTS` in p1's `stage0.env` says otherwise
  (`worker-config.sh`, `sortie_agent_cap`). All agents share one Claude
  subscription and its rate limits.
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
  up. Once the preview answers at its address through the control plane's
  front (below), the pull request gets a comment with the address and the
  commit, and the live workbench shows the address on the story, from
  `$STAGE0_HOME/previews.json`. A preview that does not answer there gets
  no comment and is deployed again on every pass until it does: it is never
  left to a person.
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

The address the kit comments, and the review opens, is a name under
Pomerium's public URL, on plain http:
`http://<identifier>.preview.<host of STAGE0_CP_PUBLIC_URL>/`
(`https://p1.example.org:8443` gives
`http://ons-93.preview.p1.example.org/`). It is never
`http://127.0.0.1:<port>/`, which is p1's loopback and nothing on a job's
machine, with or without `STAGE0_WORKBENCH_URL`. No DNS name is needed:
the review runs in a job's machine on any worker (p1's or taichi's), and
its route is the one its egress already takes
([Worker hosts (p1)](#worker-hosts-p1), "Previews"):

1. The machine's browser, with the machine's proxy settings (`HTTP_PROXY`,
   the worker's egress forwarder), asks for the address.
2. The forwarder sends it as a `CONNECT <name>:80` on the egress flow, with
   the job's token and its identifier (`X-Sortie-Job`), through Pomerium
   with the worker's certificate.
3. The egress gate admits it by the job's live token, never sends it to
   nono, and serves it from the preview's port on p1's loopback, by the
   `NAME PORT` lines of `$STAGE0_HOME/previews/front-routes`, which
   `preview.sh` writes. The preview sees its own name as the host, so its
   pages and API calls work under it.

The preview's port is never opened on p1's network. The gate gives a
review job's requests the previews' review key
(`$STAGE0_HOME/preview-review-key`, which `stage0.sh` makes) and its job,
and the preview (`WORKBENCH_REVIEW_KEY_FILE`, taken only with
`WORKBENCH_SYNC=0`) signs it in as `Review of <job>` with both roles, so
it can click every action with no login. `preview-sync` checks a preview
on the same route before it comments: TLS to Pomerium with p1's own
worker's certificate (`$STAGE0_HOME/control-plane/workers/p1`), a
`CONNECT` with Sortie's control token, which the gate admits for a
preview's name only and gives no review key, and `GET /` and
`GET /api/auth/mode` inside, which must answer 200. The review-on-preview
prompt says that an address which does not answer is a platform fault: the
review stops `blocked` with the address and the error, and never asks a
person to redeploy the preview.

People get a name too, beside the live workbench's TLS name,
`STAGE0_WORKBENCH_URL` in `stage0.env`, when it is set:
`https://workbench.example.net` gives
`https://workbench-ons-93.example.net/`, so a wildcard certificate for the
live name's domain covers it; the comment names it after the address. The
kit does not serve TLS itself: each change rewrites
`$STAGE0_HOME/previews/routes`, one `HOST PORT` line per preview with such
a name, for the proxy in front of the live workbench, and runs
`STAGE0_PREVIEW_ROUTES_HOOK` (for example, that proxy's reload). Without
`STAGE0_WORKBENCH_URL`, as on p1, a preview has no name for people, and
its address is still the one under Pomerium's.

A preview runs the branch's code on this host with the kit's Sortie and
GitHub tokens; `WORKBENCH_SYNC=0` keeps the workbench from writing to the
tracker, and the branch has passed its criteria review and CI first.

## The supervisor

Sortie runs one supervisor agent session at a time for what its rules
cannot settle (spec store `agent-platform/run-supervision`, design D9): a
parked story whose pull request already merged, a question whose premise
the forge contradicts, a pull request nobody moves, a failure in no fault
class. The workflow's `supervisor` section starts it on a park, on a
stopped failure in no class, on a pull request reaction with no progress
for an hour, on a free slot with nothing to dispatch while stories are
held, and on a sweep every 30 minutes. It runs on p1, in a slot of its own,
never one of a story's, with the host's opencode (the stories' model and
the Anthropic sign-in plugin, from `supervisor-opencode.json`) and `gh`, within 96
sessions and 3,000,000 tokens a UTC day.

- **Its prompt** is `supervisor.md`. `stage0.sh` installs it beside the
  rendered workflow, and Sortie reads it at each session's start, so a
  merged change to it reaches the next session once the deploy has run
  `stage0.sh`. It states the rules: check the forge before believing a
  claim; merge only what passed its review and CI and whose live criteria
  are proven; never rewrite a pushed commit or edit a started story; never
  merge a UI story before the operator approves it; ask a person for
  credentials, keys, approvals and decisions, one question per decision;
  record evidence with every action.
- **Its token** is `supervisor-token`, which `stage0.sh` mints once.
  Sortie accepts it only on the supervisor's routes (answer, release,
  grant sessions, record a merge, start a fix run, draft, merge, file an
  issue, request a live proof, hold, ask a person); every other control
  route answers it `403`, and a request without `evidence` is refused.
- **What it did.** `dev/stage0/supervisor.sh status` prints its status,
  its budget, its running or last session, the questions it raised that
  are still open, and its 50 newest actions, each with its result, pull
  request, merge commit and evidence. They come from Sortie's state
  (`curl -s http://127.0.0.1:7680/api/v1/state`: `supervisor`,
  `supervisor_actions`, and `platform_faults` of class
  `supervisor_question`). Each session's start and end, with its trigger,
  outcome and tokens, is in `journalctl --user -u ortus-stage0-sortie`
  (`supervisor session started`, `supervisor session ended`).
- **Its questions.** For a credential, a key, an approval, a decision, or
  an action it cannot check, it raises a question naming the action, where
  to take it, the stories it holds and what it checked. The stories stay
  out of dispatch until you take the action and mark it done:
  `POST /api/v1/platform-faults/<id>/action-done` with the control token,
  after which Sortie checks it again and releases them.
- **Pause it.** `dev/stage0/supervisor.sh pause [why]` stops its running
  session and starts no new one; the triggers wait. `dev/stage0/supervisor.sh
  resume` starts a session at once for what waited. Sortie forgets a pause
  when it restarts, as on each deploy, so the kit records yours in
  `$STAGE0_HOME/supervisor.paused` and the Sortie unit pauses it again after
  each start, until you resume it. Pausing dispatch does not pause the
  supervisor, and pausing the supervisor does not pause dispatch.

## Live proofs

Some acceptance criteria need a live run that the story's own machine
cannot do: a host its egress refuses, a sandbox, another repository (spec
store `agent-platform/run-supervision`, "A live criterion a story's machine
cannot run becomes a live-proof job"). For example, homelab's full vm chain
needs Let's Encrypt staging, which `control-plane/nono/egress-profile.json` does not allow. The
review records such a criterion `live-proof` in `.sortie/criteria.json`
and names it after `live proof needed:`; Sortie then does not merge the
pull request and comments the criterion on the story. The supervisor
requests a live proof, and merges the pull request
(`POST /api/v1/<identifier>/merge`) once it is proven at its head:
`POST /api/v1/<identifier>/live-proof`, with the criterion and why the
machine could not run it as its `evidence`. Every minute
`ortus-stage0-live-proofs.timer` runs `run-live-proofs.sh`, one pass of
`stage0-live-proofs sync` (`live-proofs/`) against Sortie and GitHub:

- **The request.** Each new `live_proof` action in Sortie's
  `supervisor_actions` opens a live proof of its pull request. From then
  on, the head gets the commit status `live-proof`, pending until the
  criterion is proven. Sortie's `auto_merge` requires CI, and Sortie reads
  CI as every status and check run of the head, so the pull request is not
  merged.
- **The job.** The pass asks Sortie for a fix run of the story
  (`POST /api/v1/<identifier>/fix`, with the control token). The message
  opens with `<!-- live-proof -->` and holds the pull request, the branch
  and the criterion. While the story's own run still goes, Sortie refuses
  the fix run, and the pass asks again a minute later. The fix run runs on
  the story's branch, in the story's repository, on a worker whose profile
  allows it: a homelab job only on a worker that declares `nested`, which is
  taichi. The workflow's fix prompt runs it as a live proof. It follows the
  repository's `AGENTS.md`: for homelab, `sandbox create --wait`, the
  cheapest proof that runs the changed code, and an open issue for every
  failure whose cause is unknown. It pushes its fixes as new commits, never
  rewriting one.
- **A parked story.** Sortie refuses a fix run of a parked story, so it
  starts the job itself when the supervisor requests the live proof of a
  parked story: it finds the open pull request of the story's branch in
  the repository the story routes to, runs the job on its head with the
  same marker, and keeps the park. The pass then asks for no fix run; it
  takes the job as started at the request. A parked story with no open
  pull request on its branch gets `409 no_pull_request` and no job. The
  supervisor then decides on the result: merge (Sortie's merge finds a
  parked story's pull request the same way), answer, or ask a person.
- **The egress.** While the job runs, its identifier is in
  `$STAGE0_HOME/live-proofs/active`, and the egress gate sends its CONNECTs to the
  live-proof nono (`ortus-stage0-nono-live-proof`, `run-nono.sh live-proof`,
  on `STAGE0_NONO_LIVE_PROOF_PORT`, default 7691). That nono runs the kit's
  profile, with its credentials, plus the hosts in `egress-live-proof.json`
  (`stage0-live-proofs profile` renders it into
  `$STAGE0_HOME/egress-profile-live-proof.json`). Add a host a live chain
  needs to that file, not to `control-plane/nono/egress-profile.json`. The
  egress gate's journal records each live-proof tunnel with `profile=live-proof`.
- **The result.** The job comments its result on the pull request through
  GitHub's API, with `SORTIE_GITHUB_TOKEN`, the placeholder of the
  `github_api` credential that nono holds. The comment's first line is
  `<!-- live-proof-result -->`, followed by `Result: proven` or
  `Result: unproven`, `Commit: <sha>`, the criterion, with a proven result
  the line `sortie:live-proof proven <sha>`, and the step table. Sortie's
  supervisor merge reads that line: it refuses to merge a story whose
  review left a criterion to a live proof until the line names the head
  (`409 live_proof_unproven`).
  The pass reads only the kit's own account's comments written since the
  request, and the newest sets the status:
  - proven at the head: `success`, and the supervisor merges once CI passes
    (Sortie checks the `sortie:live-proof proven` line itself);
  - unproven: `failure`;
  - proven at an older commit: `pending` again, until the supervisor
    requests a new proof.

  A job with no result after 12 hours loses the live-proof egress.
  The review comment triage passes over the result comment.
- **The end.** When the pull request closes, the live proof ends. A new
  request replaces the old one and starts a new job.

The supervisor of record may also run a live proof by hand in a taichi
sandbox (design D9) and post the same comment with the kit's account. A
result posted before the job starts settles the proof, and the pass starts
no job. `stage0-live-proofs` keeps its record in
`$STAGE0_HOME/live-proofs/record.json`.
`go test ./...` in `live-proofs/` checks the request, the job, the comment
and the status against a fake Sortie and a fake GitHub. `test-review-gate.sh`
checks the job's prompt, its placement on taichi by Sortie's matcher, and
Sortie's merge gate on the status, with Sortie's GitHub adapter against a
fake forge. `control-plane/internal/egress` checks the live-proof egress.

## A deploy never drains

A deploy restarts Sortie at once and applies the kit's change at once (spec
store `agent-platform/run-supervision`, "A Sortie restart interrupts no run,
and a deploy never drains"): it pauses no dispatch and waits for no run.
Sortie, on start, re-adopts each run whose lease a worker still holds and
follows it to its end, its handoff, review and merge included; a run whose
worker stopped meanwhile loses its lease and is retried, as for any lost
lease. On a busy p1 running 2 stories with 9 waiting, a kit merge applies
within the minute, the 2 go on, and the 9 are dispatched as slots free.

nono, the egress gate, Pomerium and the worker daemon carry the runs (their egress, their
stream, their machines), so `stage0.sh` restarts each only when what it runs
changed, or when it is not running (`unit-restart.sh`; the digest each
started with is in `$STAGE0_HOME/started/`): nono for its binary, scripts,
profile, egress CA, pass, `github-token`, `stage0.env` or unit (the
live-proof nono also for `egress-live-proof.json` and `live-proofs/`); the egress
gate for its source, its script, `stage0.env`, the control token or unit;
Pomerium for its image, its configuration (a new worker list), its
certificate, its script or unit; the worker daemon for its binary,
`worker.json` or unit (`worker-build.sh` builds it with `-trimpath
-buildvcs=false`, so the same code builds the same bytes and a test or
a document under `worker-daemon/` restarts nothing; a daemon an earlier
kit started from the source being built keeps running, and its record
moves to the binary). A change to nono's or the edge's ends the jobs
that unit carries, and Sortie retries them. A restart of the worker
daemon ends none: the unit stops the daemon only (`KillMode=process`),
which leaves its jobs' machines running, and its next start adopts them
and Sortie keeps their runs (worker-daemon's operator guide, "A restart
of the daemon ends no job either"). A credential `gh auth token` gives nono
changes none of these: `systemctl --user restart ortus-stage0-nono` picks
it up.

- **Deploys.** `deploy.sh` runs `stage0.sh` within the minute of a kit or
  Sortie merge, whatever runs, on p1 and on a worker host alike.
  `journalctl --user -u ortus-stage0-deploy` shows each one: the commit it
  applies, then `running <commit>`, or why it went back.
- **Host setup changes**, such as a smolvm bump. Run
  `dev/stage0/dev-host-apply.sh` on the host instead of applying the change
  by hand: it runs the apply, then `stage0.sh` so that the pack and Sortie
  follow the new smolvm. On a worker host with homelab (taichi) the apply is
  homelab's `make dev-host` in `STAGE0_HOMELAB_SRC`; elsewhere, name it:
  `dev/stage0/dev-host-apply.sh <command>`. It holds stage0.sh's lock
  throughout, so no deploy runs in between.

An earlier kit paused Sortie's dispatch while a change waited, and recorded
it in `$STAGE0_HOME/dispatch.paused`; `deploy.sh` resumes such a pause once
(`POST /api/v1/dispatch/resume`, with the control token) and forgets it.

## Credentials

`run-sortie.sh` reads each credential at start and keeps it in memory. A
job's machine sees only Sortie's placeholders, which nono on p1 replaces
([below](#where-the-credentials-live)).

| Credential | Source |
| --- | --- |
| GitHub | `github-token` in the stage-0 home, else `gh auth token`. `run-workbench.sh` reads the same one, so the workbench looks up the pull requests a story names in `delivered_by` with no credential of its own; restart the workbench unit after changing it. nono holds it for the jobs: a job's `SORTIE_GITHUB_TOKEN` is only the `github_api` placeholder, for GitHub's API, which a live-proof job comments its result with ([Live proofs](#live-proofs)) |
| Claude subscription, for both harnesses | `claude-oauth-token` (from `claude setup-token`); else the freshest live OAuth login: opencode's (`~/.local/share/opencode/opencode.db`) or Claude Code's (`~/.claude/.credentials.json`). `anthropic-token.py` picks it. Sortie hands it to no job: nono holds it and puts it in each job's egress to `api.anthropic.com` (see Egress below); `refresh-token.sh` restarts nono onto a fresher login |
| Huly | a login with the dev account in `stage0.env` |
| The workbench's notifications | `notify-tokens/<host>.token` in the stage-0 home, mode 0600, minted by `stage0.sh`: Sortie posts its notifications to the workbench with it, and the workbench refuses any other; no operator credential. See [below](#the-operators-channel) |
| The operator's channel | `notify-credential` in the stage-0 home, mode 0600, written by the operator only; optional, see [below](#the-operators-channel) |
| The supervisor's token | `supervisor-token` in the stage-0 home, mode 0600, minted by `stage0.sh` (`supervisor.sh`); Sortie accepts it only on the supervisor's routes and hands it to the supervisor session only |

Git reaches GitHub over HTTPS through a credential helper in the kit's own
`gitconfig` (`GIT_CONFIG_GLOBAL`). It includes `~/.gitconfig` and holds no
secret.

## Worker hosts (p1)

Every worker, p1's own and each worker host's, reaches Sortie only
through the control plane's front on p1: Pomerium, with a client
certificate from the control plane's CA (spec store
`agent-platform/control-plane`, design D18). It replaced the stage-1 edge
and its SSH tunnels; the worker daemon and the runs are unchanged, and only
its configuration names Pomerium's URL and its certificate. Only the control
profile runs it. The CA, the worker list and Pomerium's configuration are
`control-plane/`'s (`cpctl`, the [control plane operator
guide](../../control-plane/docs/operator-guide.md)), in
`$STAGE0_HOME/control-plane`; p1 runs Pomerium, the egress gate and nono as
user units in front of its own Sortie rather than the whole compose project.

| Part | Where |
| --- | --- |
| Pomerium | unit `ortus-stage0-pomerium` (`run-pomerium.sh`): the image `control-plane/compose.yaml` pins by digest, under rootless podman on the host's network, on the port of `STAGE0_CP_PUBLIC_URL` (default `https://<p1's FQDN>:8443`, written to `stage0.env` on the first run; its host must resolve to p1 on every worker host, and p1's firewall must admit the port from each of them). Its configuration, `$STAGE0_HOME/control-plane/pomerium/config.yaml`, is rendered by `cpctl` from the worker list; its certificate and key enter its container as podman secrets |
| Egress gate | unit `ortus-stage0-egress-gate` (`run-egress-gate.sh`), `$STAGE0_HOME/bin/cp-egress` built from `control-plane/cmd/cp-egress`, on `127.0.0.1:7692` (`STAGE0_EGRESS_GATE_PORT`), behind Pomerium's egress route |
| nono | unit `ortus-stage0-nono`, `nono proxy` on `127.0.0.1:7690` (`STAGE0_NONO_PORT`), with the control plane's `control-plane/nono/egress-profile.json`, the compose project's too. nono is pinned by the SHA-256 of its release archive in `nono-version.sh`; `stage0.sh` installs it into `$STAGE0_HOME/bin` and refuses any other archive. `make check-latest` fails once a newer stable release is out |
| Live-proof nono | unit `ortus-stage0-nono-live-proof` on `127.0.0.1:7691` (`STAGE0_NONO_LIVE_PROOF_PORT`); see [Live proofs](#live-proofs) |
| Workers | `$STAGE0_HOME/control-plane/workers.json`, each worker's certificate fingerprint and routes; `dev/stage0/worker-host.sh list` |

What a worker's request meets:

- **Only the CA's certificates.** Pomerium requires a client certificate
  from the control plane's CA on every connection and rejects any other
  in the TLS handshake (`downstream_mtls`, `enforcement:
  reject_connection`). Each flow (control, stdio, egress, MCP, bundles,
  usage) is its own route, which allows only the certificates the worker
  list names.
- **Identity.** On every route Pomerium sets `X-Sortie-Worker-Identity`
  (Sortie's `worker.identity_header`) to the certificate's DNS name, the
  worker's name, and drops any value the request brings, so taichi's
  daemon that says `p1` reaches Sortie as `taichi`.
- **Sortie** listens on p1's loopback only (`127.0.0.1:7680`), in plain
  HTTP: TLS ends at Pomerium (design D14.4). Sortie checks each flow's job
  token itself. While it restarts, Pomerium answers a worker `502` or
  `503`, which the worker daemon retries, keeping its machine; a `401`
  would tell it the job is gone.
- **Egress.** A `CONNECT` is the egress flow; its job token is the
  `Proxy-Authorization` (Bearer, or Basic as in
  `HTTPS_PROXY=https://job:<token>@<Pomerium>`). The egress gate asks
  Sortie's `POST /api/v1/tokens/introspect` (with the control token) about
  it and admits it only when it is live and granted for egress; anything
  else gets `407` and never reaches nono. While Sortie cannot answer, as
  while it restarts, the gate answers `503`, which the worker retries. An
  admitted `CONNECT` goes through nono, which allows only the hosts in
  `control-plane/nono/egress-profile.json`, the one list of the hosts a
  job of any repository may reach, with the forge's and those of the
  credentials nono holds (`workflow/profile_test.go` checks it holds each
  credential `WORKFLOW.md` binds). The remote kind takes no `allow_hosts`,
  so the workflow has none; add a host a chain needs to that profile. For
  a credential's host nono intercepts TLS with the kit's egress CA and
  replaces the placeholder Sortie gives the machine
  (`sortie-credential-<name>`) with the credential. A refusal is recorded
  in the gate's journal with the worker, job, attempt and host:
  `msg="egress refused" worker=taichi job=ONS-7 attempt=1 host=pastebin.com:443 status=403`.
  A live-proof job that runs goes through the live-proof nono instead.
- **Previews.** A `CONNECT` for a name under `preview.<Pomerium's host>`
  (`front-config.sh` `preview_domain`) on port 80 is a ui story's preview
  ([Previews of ui stories](#previews-of-ui-stories)): the same egress
  route, the same certificate check and the same job token, from a machine
  on any worker. The gate serves it from the preview's loopback port
  (`$STAGE0_HOME/previews/front-routes`) and never sends it to nono, so
  `egress-profile.json` lists no preview. A plain `http://` request a
  machine's browser sends to its proxy goes as a `CONNECT` too, since
  Pomerium's egress route takes only `CONNECT`. A worker whose certificate
  is revoked (`worker-host.sh remove`) reaches no route, and Pomerium
  refuses it in the TLS handshake or with `403` (its journal,
  `ortus-stage0-pomerium`); a `CONNECT` with no token or an ended one gets
  `407`, and the gate's journal records it with the worker, the job the
  forwarder names, and the host:
  `msg="preview refused" worker=taichi job=ONS-7 host=ons-7.preview.p1.example.org:80 status=407 reason="no live egress token"`;
  an admitted one,
  `msg="preview request" worker=taichi job=ONS-7 attempt=1 host=ons-7.preview.p1.example.org:80 port=8800 method=GET path=/ status=200`.

The workflow names the flows by Pomerium's URL:
`${STAGE0_CP_PUBLIC_URL}/<flow>` for stdio, mcp, bundles and usage, and
`${STAGE0_CP_PUBLIC_URL}` for egress.

`stage0.sh` stops and removes the stage-1 edge's units on a host that still
has them (`retire-edge.sh`): `ortus-stage0-edge` and each
`ortus-stage0-worker-tunnel@<worker>`. Before that, it gives each worker
host the edge served (`$STAGE0_HOME/workers/<worker>.env` with its SSH
destination) its certificate with `worker-host.sh add`, unless the worker
list names it already; a host it cannot reach stops the run with the edge
still up. On the worker host, the deploy timer's `stage0.sh` from the same
merge waits for the files `add` copies, up to `STAGE0_WORKER_CERT_WAIT`
seconds (default 1800; a `stage0.sh` run by hand does not wait), then dials
Pomerium. The edge's state (`edge.json`, `workers/`) stays, unread, so a
revert to the edge finds its worker hosts again.

### Add a worker host

On p1, once `stage0.sh` has run:

```sh
dev/stage0/worker-host.sh add brian@taichi        # the worker is named taichi
dev/stage0/worker-host.sh add mini.lan mac-mini   # or name it yourself
dev/stage0/worker-host.sh list
```

`add` needs `ssh -o BatchMode=yes HOST true` to work from p1. It issues the
worker a client certificate from the control plane's CA for its name (a DNS
label), lists it on every route, and copies to `~/.config/ortus-nova/` on
the host the certificate (`cp-worker.crt`), its key (`cp-worker.key`, mode
0600, which p1 then deletes: it is the one secret a worker host keeps), the
CA (`cp-ca.crt`), the egress CA's certificate (`egress-ca.crt`; its
machines trust nono's interception with it, and its key stays on p1) and
Pomerium's URL (`control-plane-url`). It restarts Pomerium onto the new
list; Sortie is not restarted. Then, on the host, `STAGE0_PROFILE=worker
dev/stage0/stage0.sh` points its daemon at Pomerium with the certificate.
Adding the same name again gives it a new certificate and drops the old one.
p1's own worker gets its certificate from `stage0.sh`, in
`$STAGE0_HOME/control-plane/workers/p1`.

### Revoke a worker host

```sh
dev/stage0/worker-host.sh remove taichi
```

`remove` takes `taichi` off the worker list and restarts Pomerium onto it at
once: from then on Pomerium answers each of `taichi`'s requests `403` on
every route, so its next check-in is rejected, and a job it holds can no
longer reach its flows, so its lease expires and Sortie retries the job
elsewhere. Add it again to give it a new certificate.

### Where the secrets live

All on p1 but a worker's own key; no credential is stored on a worker host.

| What | Where |
| --- | --- |
| Model (`anthropic`, `api.anthropic.com`) | the Claude subscription token Sortie uses (see Credentials above), read by `run-nono.sh` at start into nono's environment (`egress-credentials.sh`); nono reads it only at start, so `ortus-stage0-token.timer` restarts both nonos alone onto a fresher one while no agent runs, or at once when its token has expired |
| Forge (`github_api`, `api.github.com`; `github_git`, `github.com` for git over HTTPS) | `github-token` in the stage-0 home, else `gh auth token`, read the same way |
| Sortie's control token, for introspection | `sortie-token` in the stage-0 home |
| The egress gate's credential to nono | `nono-pass` in the stage-0 home |
| Egress CA | `egress-ca/ca.key` (mode 0600) and `egress-ca/ca.crt` in the stage-0 home |
| The control plane's CA | `control-plane/ca/ca.key` (mode 0600) and `ca.crt` in the stage-0 home; only `cpctl` reads the key |
| Pomerium's certificate and key | `control-plane/secrets/cp-pomerium-cert` and `cp-pomerium-key` in the stage-0 home, which `run-pomerium.sh` makes podman secrets. `cpctl init` also writes the compose project's other generated secrets there; p1 uses none of them |
| A worker's certificate and key | on the worker host, `~/.config/ortus-nova/cp-worker.crt` and `cp-worker.key` (mode 0600); p1's own in `control-plane/workers/p1` in the stage-0 home |

`make -C dev/stage0 test` checks Pomerium's and the egress gate's units
(`test-front.sh`) and the certificates of the workers
(`test-worker-host.sh`); `make -C control-plane test` runs the egress
gate's scenarios against a fake Sortie, with a stand-in for nono, and
`make -C control-plane live-test` every scenario of
`agent-platform/control-plane` against the real Pomerium. `make -C
dev/stage0 test-live` runs the nono scenarios on p1 through the egress
gate against the pinned nono, the credentials on p1, `api.anthropic.com`
and `mcp.firecrawl.dev`, on ports of its own.

## The operator's channel

Alerts go through Sortie's notifications (spec store
`agent-platform/run-supervision`, "A person is alerted, wherever the
question is held"), and their default destination is the workbench, by
Sortie's `webhook` backend. It needs no operator credential: `stage0.sh`
mints this host's token into `$STAGE0_HOME/notify-tokens/<host>.token`
(mode 0600; `<host>` is `STAGE0_SORTIE_NAME`, else the host's short name in
lower case, the name the workbench gives its Sortie), `run-sortie.sh` reads
it into Sortie's environment only, as `SORTIE_WORKBENCH_NOTIFY_URL`
(`http://127.0.0.1:<workbench port>/api/notifications/<host>/<token>`), and
`run-workbench.sh` points the workbench at the directory
(`WORKBENCH_NOTIFY_TOKENS`). The workflow's `notifications` list it as:

```yaml
  - kind: webhook
    url: $SORTIE_WORKBENCH_NOTIFY_URL
    events: [session.stopped, session.failed, session.escalated, escalation.ci_failure, escalation.review_comments, escalation.bot_review, escalation.merge_conflicts, escalation.auto_merge, escalation.merge_completion, budget.held]
```

The workbench keeps each notification, links it to the story and the open
question it names, and shows it under **Needs a person** (**From Sortie**),
unread until a person marks it seen, and unread again every 4 hours while
its question stays open (`workbench/docs/operator-guide.md`, "Sortie's
notifications"). It refuses a post with a wrong or missing token.

Every host has its own token. p1's Sortie posts to the workbench on p1's
loopback. Since `agent-platform/one-sortie` the kit runs no Sortie on a
worker host: a story that runs on taichi's worker parks on p1's Sortie, and
its notification takes that way. So a worker host gets no token: the stage-1
edge's tunnel that forwarded p1's workbench to it is gone with the edge, and
`worker-host.sh remove` deletes a token an earlier `add` minted for it.

Any other destination is the operator's choice and one more entry in the
same list, after the workbench's: any kind Sortie's notifications have
(discord, slack, telegram, or a webhook such as a mobile push service). It
is optional: with no `STAGE0_NOTIFY_KIND`, the default, Sortie's
notifications go to the workbench only and `stage0.sh`, `run-sortie.sh` and
the workbench start without an operator credential.

| Where | What it sends |
| --- | --- |
| The workflow, to the workbench, through Sortie's notifications (always) | a run that stops for a person or fails (`session.stopped`, `session.failed`), a question, permission request or failed criteria check (`session.escalated`), every escalation (`escalation.*`), and a budget hold (`budget.held`) |
| The workflow, to the operator's channel, through Sortie's notifications (`notify-channel.sh` renders the entry) | the moment a run stops for a person (`session.stopped`: an agent's park, or a review that cannot settle a criterion, with its question), a question or permission request the broker escalates and a failed criteria check (`session.escalated`), a budget hold (`budget.held`), a merge that needs a person (`escalation.auto_merge`), and a pull request that still fails CI or conflicts with its base after its fix runs (`escalation.ci_failure`, `escalation.merge_conflicts`) |
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
Sortie. To check it, park a story that runs on taichi: within one minute
Sortie's notification of it is under **Needs a person** (**From Sortie**,
unread) on the workbench, and its question under **Answer**; the channel,
when one is set, has it with the link. `journalctl --user -u
ortus-stage0-workbench` logs `notification from p1: session.stopped
<identifier>`.

## Worker hosts (taichi)

A worker host runs only a worker daemon (`worker-daemon/`, its
[operator guide](../../worker-daemon/docs/operator-guide.md)). It runs no
Sortie, polls no tracker and holds no credential: its jobs' model and forge
requests get theirs from nono on p1. taichi's homelab Sortie, the second
Sortie of stage 0, is retired: p1's Sortie dispatches homelab too.

```sh
# On p1, once: the host's client certificate, and the egress CA's certificate.
dev/stage0/worker-host.sh add brian@taichi
# On the host: the first run writes stage0.env with the profile.
STAGE0_PROFILE=worker dev/stage0/stage0.sh
dev/stage0/stage0.sh            # later runs read it from stage0.env
```

| Part | Where |
| --- | --- |
| Worker | unit `ortus-stage0-worker`, `$STAGE0_HOME/bin/worker-daemon` built from `worker-daemon/`, configured by `$STAGE0_HOME/worker.json` (`worker-config.sh`), state in `$STAGE0_HOME/worker`. It dials p1's Pomerium at `control-plane-url` with its client certificate (`cp-worker.crt`, `cp-worker.key`, `cp-ca.crt` in `~/.config/ortus-nova`, from `worker-host.sh add`), and Pomerium names it by that certificate |
| Live kit | as on p1, `$STAGE0_HOME/kit` follows `origin/main` (`ortus-stage0-deploy.timer`); a change to the kit runs `stage0.sh` (the worker profile) at once, which restarts the worker daemon only when what it runs changed. A worker host builds no Sortie, follows no `plan-bench` merge and asks no Sortie to pause or resume, whatever Sortie or Huly settings its `stage0.env` kept from the retired homelab profile; nothing on it reads them |
| Packs | `stage0.smolmachine` from `pack/`, as on p1, for sortie and agentic-orchestration jobs; and, with homelab's dev host, `$STAGE0_HOME/packs/homelab-<hash>.smolmachine` (below) |
| Egress CA | `~/.config/ortus-nova/egress-ca.crt`, from `worker-host.sh add`. Each machine's setup puts it in the guest's trust store and `NODE_EXTRA_CA_CERTS`, so nono's interception of the credential hosts is trusted |
| Previews | nothing of its own: a review job on taichi opens a ui story's preview at the commented `http://<identifier>.preview.<Pomerium's host>/` through its machine's proxy, on the egress route to p1's Pomerium with taichi's certificate and the job's token, as a job on p1's worker does ([Previews of ui stories](#previews-of-ui-stories)); no preview port on p1 is open to taichi |

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
| Shared folders | read-only, `~/.cache/homelab-sources`; read-write, `~/.cache/homelab-vmhost`, homelab's uv cache and its Go cache (one folder); per job, `~/.local/state/homelab/sortie-recorder/<job>`, the flight recorder's folder, which stays after the machine is gone; read-only, `~/.local/state/homelab/open-issues` at the same path in the guest (`/home/builder/.local/state/homelab/open-issues`), the open-issue evidence `python3 -m homelab.open_issues capture` keeps on the host, so a job reads the evidence a row of homelab's `docs/open-issues.md` names. A job's own captures go to its recorder folder (`HOMELAB_STATE_HOME=/var/log/homelab-recorder`); the setup gives `~/.local/state` back to `builder`, since the read-only mount leaves its parents to root. Six, beside the daemon's share: on taichi a machine with 8 started (smolvm 1.32.4, nested, 2026-11-04), so one stays spare |
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
systemctl --user disable --now ortus-stage0-sortie ortus-stage0-workbench ortus-stage0-pomerium ortus-stage0-egress-gate \
  ortus-stage0-nono ortus-stage0-nono-live-proof ortus-stage0-worker
smolvm machine ls   # delete each wd-* machine: smolvm machine delete --name <name>
rm ~/.config/systemd/user/ortus-stage0-*.service && systemctl --user daemon-reload
rm -rf ~/.local/share/ortus-nova/stage0   # state, tokens, workspaces and the pack
```
