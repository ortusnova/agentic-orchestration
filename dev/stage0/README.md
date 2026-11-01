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

## Remove it

```sh
systemctl --user disable --now ortus-stage0-sortie ortus-stage0-workbench
smolvm machine ls   # delete each sortie-* VM: smolvm machine delete --name <name>
rm ~/.config/systemd/user/ortus-stage0-*.service && systemctl --user daemon-reload
rm -rf ~/.local/share/ortus-nova/stage0   # state, tokens, workspaces and the pack
```
