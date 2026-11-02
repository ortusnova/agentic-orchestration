# Stage 0 kit

Stage 0 of the Ortus Nova spec store's `agent-platform-workers` (design D13,
D15) on one Linux host. It runs a persistent Sortie under plan control with
co-located smolvm, the plan workbench, acp-broker and the local Huly. GitHub
is the forge. Worker hosts reach the control plane through Pomerium in
`control-plane/` ([below](#worker-hosts-p1)).
Everything is rootless: systemd user units and Huly's podman containers.

```sh
dev/stage0/stage0.sh                  # set up or update, then (re)start the units
dev/stage0/stage0.sh --rebuild-pack   # also rebuild the smolvm pack
dev/stage0/stage0.sh --update-sortie  # also move the Sortie checkout to origin/plan-bench (the timer does this by itself)
dev/stage0/test-smolvm-version.sh     # check the smolvm version check against fake binaries
dev/stage0/test-store-sync.sh         # check the store's fast-forward against throwaway repositories
dev/stage0/test-deploy.sh             # check what each kind of merge does to the live kit, and the drain
dev/stage0/dev-host-apply.sh [CMD...] # drain this host, apply a setup change (homelab: make dev-host), run stage0.sh, dispatch again
dev/stage0/test-review-gate.sh        # check the review gate and the fix runs against Sortie (the sortie/ submodule, or SORTIE_SRC)
make -C dev/stage0 test               # every test-*.sh
```

The kit needs smolvm 1.32.4 or later (`SMOLVM_MIN` in `smolvm-version.sh`); `stage0.sh` refuses an older one.

| Part | Where |
| --- | --- |
| Settings | `~/.local/share/ortus-nova/stage0/stage0.env`, written on the first run |
| Sortie | unit `ortus-stage0-sortie`, `127.0.0.1:7680`, workflow rendered from `WORKFLOW.md` |
| Workbench | unit `ortus-stage0-workbench`, `http://127.0.0.1:8793`, local mode; the password is in `workbench-password` |
| Huly | `http://localhost:8087`, workspace `sortie`, project `ONS`; `podman-restart.service` brings it back after a reboot |
| Pack | `stage0.smolmachine`, from `pack/`: Go, Node, opencode and Claude Code (`claude-agent-acp`) |
| Live kit | `$STAGE0_HOME/kit`, the kit's own detached worktree of `origin/main`: `stage0.sh` runs from it, whichever checkout you start it from, and the units run it. `ortus-stage0-deploy.timer` moves it to each merge within a minute (`deploy.sh`): a workbench change is rebuilt and the workbench restarted; a change to the rest of the kit runs `stage0.sh` again once no agent runs, and so does a merge to Sortie's `plan-bench` (`stage0.sh --update-sortie`; a Sortie checkout that is not behind `plan-bench` stays put). Such a change drains the host first (see [Drain before a change](#drain-before-a-change)); a failed build goes back to the running commit. One `stage0.sh` or deploy runs at a time (`$STAGE0_HOME/stage0.lock`): a second `stage0.sh` waits, and the timer tries again a minute later. `STAGE0_KIT_FROM_CHECKOUT=1 dev/stage0/stage0.sh` runs your checkout instead, to try a branch, and stops the timer |
| Spec store | `STAGE0_STORE`; `ortus-stage0-store.timer` fast-forwards it to its upstream every minute (`store-sync.sh`), and leaves it alone, saying why in its journal, when that could lose work: tracked changes, local commits, a detached HEAD |
| Logs | `journalctl --user -u ortus-stage0-sortie` (and `-workbench`, `-store`, `-deploy`) |

## How a story runs

The workbench registers the spec store (`STAGE0_STORE`), which follows its
upstream on its own, so a merged plan change reaches it within a minute. About a second
after a change or an approval it syncs the plan on its own, and an approved story whose
blockers are approved is released to `Todo`; the workbench then asks Sortie
to poll at once. Sortie creates a VM from the pack and
seeds it with the story's repository (see below). It runs the agent behind
acp-broker, then pushes the issue branch and opens the pull request. When
the pull request merges, Sortie moves the story to `Done`.

- **Review and merge.** Every story's pull request goes through the
  criteria gate, whatever its `review` value (spec store
  `agent-platform/run-supervision`, design D5). The build rule
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
  Sortie a person still merges those. `test-review-gate.sh` checks all of
  this against Sortie's own loader, exit and merge code: it needs the
  `sortie/` submodule, or `SORTIE_SRC` naming a Sortie checkout.

- **CI failures and merge conflicts.** Both workflows turn on Sortie's
  `ci_failure` and `merge_conflicts` reactions (spec store
  `agent-platform/run-supervision`). A pull request whose CI fails, or that
  conflicts with its base after another merge, gets a fix run on the
  `review` rule with the workflows' fix prompt: it merges `origin/<base>`
  in, never rebases or rewrites a pushed commit, fixes the cause on the
  branch, including a CI or toolchain setting the change exposed (an old Go
  is raised to the latest stable release), and checks the criteria again.
  Sortie pushes its commits, and `auto_merge` merges once CI passes. A pull
  request that still fails after `max_retries` fix runs (2 for CI, 1 for a
  conflict) is a stopped failure: Sortie escalates it, comments on the
  story, and runs no further fix until a new commit lands or a person
  answers it. Neither watch has a window. Sortie's CI provider reads the
  checks of one repository, `github.project`: on p1 that is
  `agentic-orchestration`, so a `sortie` pull request gets no CI fix run
  (its CI reads fail and back off, and its failing CI only holds its
  merge) until Sortie reads each pull request's own repository; the homelab
  profile reads `homelab`.
  `test-review-gate.sh` checks the fix runs too.

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
- **What the agent reads.** The VM has no copy of the spec store and no
  network beyond the workflow's hosts, so the prompt carries everything:
  the story's Huly description holds its notes, its acceptance criteria,
  and the text and scenarios of each requirement it covers. When a person
  releases a parked story, the first turn of the next run also holds the
  agent's stop statement and every comment posted since the park.
- **Session start.** `agent.read_timeout_ms` is 60 s: a fresh VM's agent
  can take over 15 s to answer at session start, past Sortie's 5 s
  default. Turns are bounded by `stall_timeout_ms` and `turn_timeout_ms`.
- **Parallel agents.** `STAGE0_MAX_AGENTS` in `stage0.env` (default 3) caps
  agents at once; each VM takes 4 vCPU and 8 GiB. All of them share one
  Claude subscription and its rate limits.
- **Repositories.** A story runs in the repository its plan node names
  (plan sync keeps the Huly label `sortie-repository:<name>`), else in
  `sortie`, the default. The workflow lists three:

  | Name | Settings | Default | Dispatched here |
  | --- | --- | --- | --- |
  | `sortie` | `STAGE0_REPO_*` | yes | yes |
  | `agentic-orchestration` | `STAGE0_AO_REPO_*` | no | yes |
  | `homelab` | `STAGE0_HOMELAB_REPO_*` | no | no: the homelab Sortie runs these stories |

  `homelab` is listed only so that plan sync accepts its stories; this
  Sortie leaves them alone. Plan sync refuses a story naming a repository
  the workflow does not list. A `stage0.env` written before these settings
  existed keeps working: `stage0.sh` fills in each missing one with its
  default. To change one, set it in `stage0.env` and run `stage0.sh` again.

## Drain before a change

A change that must not land under a running agent drains the host first
(spec store `agent-platform/run-supervision`, "A pending deploy drains its
host"): Sortie stops dispatching new runs, the runs already going finish,
the change applies, and Sortie dispatches again. The change waits for the
runs that were going when it arrived, never behind the queue of stories.
On a busy taichi running 2 stories with 9 waiting, a kit merge starts no
third run, applies when the 2 finish, and then the 9 are dispatched.

- **Deploys.** `deploy.sh` pauses dispatch each minute a kit or Sortie
  change waits, and resumes it once `stage0.sh` has run, or once the change
  has failed and been put back.
- **Host setup changes**, such as a smolvm bump. Run
  `dev/stage0/dev-host-apply.sh` on the host instead of applying the change
  by hand. It pauses dispatch, waits for the running runs (every
  `STAGE0_DRAIN_POLL` seconds, default 15), runs the apply, runs
  `stage0.sh` so that the pack and Sortie follow the new smolvm, and
  resumes dispatch, also when a step fails. On taichi the apply is
  homelab's `make dev-host` in `STAGE0_HOMELAB_SRC`; elsewhere, name it:
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

`run-sortie.sh` reads each credential at start and keeps it in memory. The
VMs see only smolvm placeholders.

| Credential | Source |
| --- | --- |
| GitHub | `github-token` in the stage-0 home, else `gh auth token`. `run-workbench.sh` reads the same one, so the workbench looks up the pull requests a story names in `delivered_by` with no credential of its own; restart the workbench unit after changing it |
| Claude subscription, for both harnesses | `claude-oauth-token` (from `claude setup-token`); else the freshest live OAuth login: opencode's (`~/.local/share/opencode/opencode.db`) or Claude Code's (`~/.claude/.credentials.json`). `anthropic-token.py` picks it. Sortie reads it at each launch (the workflows' `command` source), so a refreshed login reaches the next run with no restart. The homelab profile reads only `claude-oauth-token` |
| Huly | a login with the dev account in `stage0.env` |

Git reaches GitHub over HTTPS through a credential helper in the kit's own
`gitconfig` (`GIT_CONFIG_GLOBAL`). It includes `~/.gitconfig` and holds no
secret.

## Worker hosts (p1)

Worker hosts no longer reach p1 through the stage-1 edge: the control
plane's compose project and its Pomerium replaced the edge, nono and the
SSH tunnels (spec store `agent-platform-workers`, design D18; see the
[control plane operator guide](../../control-plane/docs/operator-guide.md)).
`stage0.sh` stops and removes their units on a host that still has them
(`retire-edge.sh`): `ortus-stage0-edge`, `ortus-stage0-nono`,
`ortus-stage0-token.timer` and each `ortus-stage0-worker-tunnel@<worker>`.
The edge's state in the stage-0 home (`edge.json`, `workers/`, `nono-pass`,
`egress-ca/`) is no longer read; delete it once the control plane runs.
Each worker host moves to the control plane with a client certificate and
an `https` control-plane URL; its daemon and its runs are unchanged.

## The homelab profile (taichi)

A second Sortie on a homelab dev host runs only homelab stories (spec store
`run-homelab-through-sortie`, design D5-D12). It polls the same Huly project
as p1's Sortie. p1's Sortie lists `homelab` closed to dispatch; this one
lists only `homelab`, with no default, so every other story stays p1's.

```sh
# On p1, once: Huly on the homelab host's loopback, through a remote SSH forward.
dev/stage0/huly-tunnel.sh taichi
# On taichi, once homelab's `make dev-host` has run there:
STAGE0_PROFILE=homelab dev/stage0/stage0.sh   # the first run writes stage0.env with the profile
dev/stage0/stage0.sh                          # later runs read it from stage0.env
```

| Part | Where |
| --- | --- |
| Sortie | unit `ortus-stage0-sortie` on taichi, `127.0.0.1:7680`, workflow rendered from `WORKFLOW-homelab.md` |
| Huly | p1's, at `http://127.0.0.1:8087` on taichi through unit `ortus-stage0-huly-tunnel@taichi` on p1. Nothing listens on the LAN, and taichi gets no SSH access to p1. While p1 sleeps, taichi's Sortie cannot reach the tracker and retries |
| homelab | the kit's own detached worktree of `origin/master` in `$STAGE0_HOME/homelab`, added from `STAGE0_HOMELAB_SRC` (default `~/dev/homelab`); every `stage0.sh` run moves it to the latest `origin/master` |
| Pack | `$STAGE0_HOME/packs/homelab-<hash>.smolmachine`, from `homelab-pack/`: homelab's sandbox image scripts plus the harness (`pack/install-harness.sh`), for homelab's guest user `builder`, created with the host user's uid and gid so it can write the shared folders. The workflow creates its machines with `user: builder` (the spec store's `smolvm-machine-shape` story); a pack does not carry its image's `USER`. The hash covers the staged inputs, smol-builder and the smolvm version, so a change to any of them builds a new pack. A pack no Sortie machine was created from is deleted |
| Machines | 8 vCPU, 20 GiB, a 200 GiB disk at `/workspace`, nested KVM, `virtio-net`, homelab's guest kernel (`SMOLVM_LIB_DIR`), at most `STAGE0_MAX_AGENTS` (2) at once |
| Host inputs | Five shared folders, smolvm's ceiling on taichi beside Sortie's own: read-only, `~/.cache/homelab-sources`; read-write, `~/.cache/homelab-vmhost`, homelab's uv cache and its Go cache (one folder); per workspace, `~/.local/state/homelab/sortie-recorder/<issue>`, the flight recorder's folder, which Sortie keeps after the machine is gone. The vm age key, copied alone into `$STAGE0_HOME/inputs/vm-age`, reaches the machines as `HOMELAB_VM_AGE_KEY` (`pass_env`), and `after_create` writes it where sops looks |

Credentials: exactly two files in the stage-0 home on taichi, mode 0600,
and no fallback. `stage0.sh` and `run-sortie.sh` refuse to start without
them.

| File | What |
| --- | --- |
| `github-token` | a fine-grained token limited to `ortusnova/homelab`: Contents and Pull requests, read and write |
| `claude-oauth-token` | a `claude setup-token` token; `anthropic-token.py --setup-token-only` reads only it |

The recorder folders stay until you delete them:
`rm -rf ~/.local/state/homelab/sortie-recorder/<issue>` once an issue's
evidence is no longer needed.

To remove the profile: on taichi, `systemctl --user disable --now
ortus-stage0-sortie`, delete each `sortie-*` machine, then
`git -C ~/dev/homelab worktree remove ~/.local/share/ortus-nova/stage0/homelab`
and `rm -rf ~/.local/share/ortus-nova/stage0`. On p1:
`dev/stage0/huly-tunnel.sh --remove taichi`.

## Remove it

```sh
systemctl --user disable --now ortus-stage0-sortie ortus-stage0-workbench
smolvm machine ls   # delete each sortie-* VM: smolvm machine delete --name <name>
rm ~/.config/systemd/user/ortus-stage0-*.service && systemctl --user daemon-reload
rm -rf ~/.local/share/ortus-nova/stage0   # state, tokens, workspaces and the pack
```
