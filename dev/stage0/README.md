# Stage 0 kit

Stage 0 of the Ortus Nova spec store's `agent-platform-workers` (design D13,
D15) on one Linux host. It runs a persistent Sortie under plan control with
co-located smolvm, the plan workbench, acp-broker and the local Huly. GitHub
is the forge. Everything is rootless: two systemd user units and Huly's
podman containers.

```sh
dev/stage0/stage0.sh                  # set up or update, then (re)start the units
dev/stage0/stage0.sh --rebuild-pack   # also rebuild the smolvm pack
dev/stage0/stage0.sh --update-sortie  # also move the Sortie checkout to origin/plan-bench
dev/stage0/test-smolvm-version.sh     # check the smolvm version check against fake binaries
```

The kit needs smolvm 1.32.4 or later (`SMOLVM_MIN` in `smolvm-version.sh`); `stage0.sh` refuses an older one.

| Part | Where |
| --- | --- |
| Settings | `~/.local/share/ortus-nova/stage0/stage0.env`, written on the first run |
| Sortie | unit `ortus-stage0-sortie`, `127.0.0.1:7680`, workflow rendered from `WORKFLOW.md` |
| Workbench | unit `ortus-stage0-workbench`, `http://127.0.0.1:8793`, local mode; the password is in `workbench-password` |
| Huly | `http://localhost:8087`, workspace `sortie`, project `ONS`; `podman-restart.service` brings it back after a reboot |
| Pack | `stage0.smolmachine`, from `pack/`: Go, Node, opencode and Claude Code (`claude-agent-acp`) |
| Logs | `journalctl --user -u ortus-stage0-sortie` (and `-workbench`) |

## How a story runs

The workbench registers the spec store (`STAGE0_STORE`). About a second
after an approval it syncs the plan on its own, and an approved story whose
blockers are approved is released to `Todo`; the workbench then asks Sortie
to poll at once. Sortie creates a VM from the pack and
seeds it with the story's repository (see below). It runs the agent behind
acp-broker, then pushes the issue branch and opens the pull request. When
the pull request merges, Sortie moves the story to `Done`.

- **Harness.** Every story runs opencode (`anthropic/claude-opus-5-5`),
  unless its title holds `[claude]` or it has the Huly label `claude-code`;
  then it runs Claude Code.
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

## Credentials

`run-sortie.sh` reads each credential at start and keeps it in memory. The
VMs see only smolvm placeholders.

| Credential | Source |
| --- | --- |
| GitHub | `github-token` in the stage-0 home, else `gh auth token` |
| Claude subscription, for both harnesses | `claude-oauth-token` (from `claude setup-token`); else the freshest live OAuth login: opencode's (`~/.local/share/opencode/opencode.db`) or Claude Code's (`~/.claude/.credentials.json`). `anthropic-token.py` picks it. A login token lasts hours, so `ortus-stage0-token.timer` restarts Sortie onto a fresher one while no agent runs |
| Huly | a login with the dev account in `stage0.env` |

Git reaches GitHub over HTTPS through a credential helper in the kit's own
`gitconfig` (`GIT_CONFIG_GLOBAL`). It includes `~/.gitconfig` and holds no
secret.

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
| Pack | `$STAGE0_HOME/packs/homelab-<hash>.smolmachine`, from `homelab-pack/`: homelab's sandbox image scripts plus the harness (`pack/install-harness.sh`), for `brian`. Its machines run as root until Sortie can create them with `--user brian` (the spec store's `smolvm-machine-shape` story), so no homelab story runs before that. The hash covers the staged inputs, smol-builder and the smolvm version, so a change to any of them builds a new pack. A pack no Sortie machine was created from is deleted |
| Machines | 8 vCPU, 20 GiB, a 200 GiB disk at `/workspace`, nested KVM, `virtio-net`, homelab's guest kernel (`SMOLVM_LIB_DIR`), at most `STAGE0_MAX_AGENTS` (2) at once |
| Host inputs | read-only: `~/.cache/homelab-sources`, and the vm age key copied alone into `$STAGE0_HOME/inputs/vm-age`; read-write: `~/.cache/homelab-vmhost` and homelab's uv and Go caches; per workspace: `~/.local/state/homelab/sortie-recorder/<issue>`, the flight recorder's folder, which Sortie keeps after the machine is gone |

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
