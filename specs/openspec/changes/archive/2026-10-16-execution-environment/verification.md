# Verification: execution-environment

## Task group 1: remaining smolvm checks

Run on 2026-10-12 with smolvm 1.26.1 on the Sortie host (CachyOS, KVM). Every machine was created for these checks only, labeled `sortie.test=execution-environment`, and deleted by that label afterwards. The packs are the spike's, from `spikes/smolvm/golden/out/` on branch `spike/smolvm`: `base-639f67207fb8f11d.smolmachine` (opencode 1.18.29) and `go-node-ce57b820f0d118fb.smolmachine` (adds Node 24.21.0). The scratch harness is in `/tmp/opencode/ee`.

In the commands below, `S` is a shell function for `smolvm machine exec --name <machine> -- "$@"`.

### Task 1.1: three mounts on one machine

The Sortie binary was built from the fork with `CGO_ENABLED=0 go build ./cmd/sortie` and copied into a host directory of its own. A small Python listener on the host answered each connection with `host-echo:` plus what it read.

```sh
smolvm machine create --name ee-s11 --from base-639f67207fb8f11d.smolmachine \
  --label sortie.test=execution-environment \
  -v /tmp/opencode/ee/s11/control:/run/sortie/control \
  -v /tmp/opencode/ee/s11/opt:/opt/sortie:ro \
  --mount-socket /tmp/opencode/ee/s11/tools.sock:/run/sortie/tools.sock
smolvm machine start --name ee-s11      # "Starting machine 'ee-s11' with 2 mount(s)..." in 0.31 s
```

**The binary runs from the read-only mount.**

```text
$ S /opt/sortie/sortie --version
sortie dev (commit: unknown, built: unknown, go1.27.1-X:nodwarf5, linux/amd64)
rc=0
```

**Guest writes to `/opt/sortie` fail, and the host enforces it.** The guest is root. A remount reports success but changes nothing, and a fresh mount of the same virtiofs tag is read-only too:

```text
$ S sh -c 'echo x > /opt/sortie/planted; touch /opt/sortie/sortie'
sh: 1: cannot create /opt/sortie/planted: Read-only file system
touch: cannot touch '/opt/sortie/sortie': Read-only file system
$ S sh -c 'mount -o remount,rw /opt/sortie; echo rc=$?; echo x > /opt/sortie/planted'
rc=0
sh: 1: cannot create /opt/sortie/planted: Read-only file system
$ S sh -c 'mkdir -p /mnt/x && mount -t virtiofs smolvm1 /mnt/x; echo z > /mnt/x/planted3'
sh: 1: cannot create /mnt/x/planted3: Read-only file system
$ ls /tmp/opencode/ee/s11/opt
sortie
```

**The control link shares writes both ways.**

```text
$ S sh -c 'ls -A /workspace'                  # empty before the link
$ S sh -c 'ln -s /run/sortie/control /workspace/.sortie && echo guest-wrote > /workspace/.sortie/status'
$ cat control/status                           # on the host
guest-wrote
$ echo host-wrote > control/mcp.json; S cat /workspace/.sortie/mcp.json
host-wrote
$ S ln -sf /etc/passwd /workspace/.sortie/evil; readlink control/evil   # host
/etc/passwd
```

Guest files show uid 1000 inside the guest and belong to the host user on the host. A symlink the guest plants appears on the host as a symlink, which is what `workspacekit` refuses.

**The socket reaches the host listener**, tested with `perl` and `IO::Socket::UNIX`, since the base image has no `python3`, `nc` or `socat`:

```text
with listener:        read n=15 data=[host-echo:ping]
listener killed:      connected; wrote=5; read n=undef err=Connection reset by peer
socket file removed:  connected; read n=undef
listener restarted:   read n=15 data=[host-echo:ping]
```

**Result.** All three checks pass. Two details differ from what the design records, and neither changes it:

- With no listener, the guest's connect succeeds, and the first read fails with `ECONNRESET`, not a clean EOF. `mcp-relay` must therefore treat a read error before the first response byte the same as EOF: both mean `tool server unavailable`.
- `machine ls --json` reports `"mounts": 2`. The socket is not counted as a mount, and `machine ls` shows no mount paths. The proof of "exactly three resources" is the `create` argv that Sortie builds, which the unit tests assert.

The guest's `mount` table also lists the `-v` mounts again under `/storage/overlays/<vm>/merged/...`, one of them flagged `rw`. Writes through that path fail with `Read-only file system` too.

### Task 1.2: claude-agent-acp and codex behind a credential binding

There is no Anthropic or OpenAI key on this host, so both agents used OpenRouter's compatible endpoints, with the OpenRouter key bound to `openrouter.ai`. The host variable was exported under the name each agent reads; the guest saw only the placeholder.

```sh
export ANTHROPIC_AUTH_TOKEN="$OPENROUTER_API_KEY" OPENAI_API_KEY="$OPENROUTER_API_KEY"
smolvm machine create --name ee-s12 --from go-node-ce57b820f0d118fb.smolmachine \
  --label sortie.test=execution-environment \
  --allow-host-pattern registry.npmjs.org --allow-host-pattern openrouter.ai \
  --credential anthropic=ANTHROPIC_AUTH_TOKEN@openrouter.ai \
  --credential openai=OPENAI_API_KEY@openrouter.ai
smolvm machine start --name ee-s12
```

The guest environment, with the placeholders shortened:

```text
CURL_CA_BUNDLE=/run/smolvm/ca-bundle.pem
NODE_EXTRA_CA_CERTS=/run/smol/credentials/ca.pem
SSL_CERT_FILE=/run/smolvm/ca-bundle.pem
REQUESTS_CA_BUNDLE=/run/smolvm/ca-bundle.pem
DENO_CERT=/run/smol/credentials/ca.pem
ANTHROPIC_AUTH_TOKEN=<smolvm placeholder>...
OPENAI_API_KEY=<smolvm placeholder>...
```

**claude-agent-acp 0.89.1.** It was installed in the guest with `npm i -g @agentclientprotocol/claude-agent-acp@0.89.1`. That pulls `@anthropic-ai/claude-agent-sdk-linux-x64`, the native Claude Code binary. The ACP driver (`/tmp/opencode/ee/acp_drive.py`) ran it over `machine exec -i`:

```sh
python3 acp_drive.py ee-s12 /workspace/repo "Reply with exactly the word pong. Do not use any tools." \
  --env ANTHROPIC_BASE_URL=https://openrouter.ai/api --env ANTHROPIC_API_KEY= \
  --env ANTHROPIC_MODEL=anthropic/claude-haiku-5.5 --env ANTHROPIC_SMALL_FAST_MODEL=anthropic/claude-haiku-5.5 \
  --env CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 -- claude-agent-acp
```

```text
[  0.22s] initialize: {"protocolVersion": 1, "agentCapabilities": {..., "loadSession": true, ...
[  0.54s] session/new: 599fc062-c81a-4bc9-a653-373510cbe4bc
pong
[  3.79s] prompt result: {"jsonrpc": "2.0", "id": 3, "result": {"stopReason": "end_turn", "usage": {"inputTokens": 2, "outputTokens": 4, ...
[  3.85s] exit: 0
$ smolvm machine egress-events --name ee-s12
No egress denials recorded for 'ee-s12'.
```

**codex-cli 0.154.0.** The host's static binary was copied in with `smolvm machine cp` (251 MB at about 85 MB/s) and run once with `codex exec`:

```sh
S codex exec --skip-git-repo-check -c model_provider=openrouter \
  -c 'model_providers.openrouter={name="openrouter",base_url="https://openrouter.ai/api/v1",env_key="OPENAI_API_KEY",wire_api="responses"}' \
  -m anthropic/claude-haiku-5.5 "Reply with exactly the word pong. Do not run commands."
```

```text
provider: openrouter
...
codex
pong
tokens used
15,267
```

codex also tried `api.github.com`, `chatgpt.com` and `ab.chatgpt.com`. `egress-events` lists those as denied, and the turn succeeded anyway.

**The key never reached the guest.** The guest searched every process environment and `/root`, `/workspace`, `/tmp`, `/etc`, `/opt` and `/run` for OpenRouter key-shaped strings (`sk-or-v1-[0-9a-f]*`), and printed only a SHA-256 prefix of each match, so the check itself put no key bytes in argv. The host key's prefix is `f8b504d480920a41`. The guest found no match at all:

```text
procdone
filesdone
```

**Result.** Both clients accept the substituted certificate. claude-agent-acp's native binary (Bun) reads `NODE_EXTRA_CA_CERTS`, and codex's Rust client reads `SSL_CERT_FILE`. This matches the design. The guide's support list:

| Agent kind | Under smolvm |
|---|---|
| `agent-client-protocol` with `opencode acp` | Supported (spike, 2026-10-11) |
| `agent-client-protocol` with `claude-agent-acp` | Supported (this check) |
| `codex` | TLS works (this check). The kind's own `app-server` launch was not run in a VM. |
| `claude-code`, `copilot`, `pi` | Not verified |

### Task 1.3: SIGTERM to `smolvm machine exec` in the middle of an opencode turn

```sh
smolvm machine create --name ee-s13 --from base-639f67207fb8f11d.smolmachine \
  --label sortie.test=execution-environment \
  --allow-host-pattern openrouter.ai --allow-host-pattern models.opencode.ai \
  --allow-host-pattern registry.npmjs.org \
  --credential openrouter=OPENROUTER_API_KEY@openrouter.ai
python3 acp_drive.py ee-s13 /workspace/repo "First run the shell command 'sleep 25' with the bash tool. After it finishes, create the file done.txt containing the word done." \
  --kill-after 12 -- opencode acp
```

The driver sent SIGTERM to the `smolvm machine exec` process 12 s after `session/prompt`, while the `sleep 25` tool call was running:

```text
[  1.71s] session/new: ses_ed097006affeBzSwGFL5TCXtoq
[  3.41s] update tool_call: bash
[ 13.71s] SIGTERM -> smolvm exec pid 1358454
[ 13.71s] EOF waiting for session/prompt; exit=-15 stderr tail: []
$ S ps -eo pid,args
    1 /run/smolvm/init container-init
   17 [opencode] <defunct>
```

The CLI died of the signal at once, stdout closed with no `session/prompt` response, and no stderr was written. In the guest, `opencode` was a zombie and its `sleep` was gone.

A new `opencode acp`, started with `session/load` of the same session, replayed:

```text
{"sessionUpdate": "user_message_chunk", "content": {"type": "text", "text": "First run the shell command 'sleep 25' ..."}}
{"sessionUpdate": "tool_call", "title": "sleep 25", "kind": "execute", "status": "pending"}
{"sessionUpdate": "tool_call_update", "status": "in_progress", "kind": "execute", "title": "sleep 25"}
```

A follow-up prompt asked whether the sleep finished and whether `done.txt` existed. The agent checked and answered:

```text
The `sleep 25` was interrupted, not completed, and `done.txt` does not exist, so I have not created it yet.
[  6.67s] prompt result: {"result": {"stopReason": "end_turn", ...}}
```

**Result.** What a stop in the middle of a turn loses:

- the response to the in-flight `session/prompt`, and every update that had not yet been written to stdout;
- the result of the running tool call. Its record stays `in_progress` for good, with no output and no completion.

What survives on the guest disk:

- the session itself, which `session/load` resumes;
- the user prompt and every update up to the tool call's start;
- the work tree, unchanged by the interrupted step.

The resumed agent sees the interrupted call and recovers on its own. This matches the design's "no graceful TERM" risk: the loss is the current step, not the session. A guest-side TERM relay is not needed for opencode.

## Task group 2: smol-builder

Repository `~/dev/smol-builder`, module `github.com/briancappello/agentic-orchestration/smol-builder`. `make test` and `make lint` pass (0 issues).

| Task | Commit | Verified by |
|---|---|---|
| 2.1 | `d9ef6d9` | `go build ./...` and `make test` on the empty set. The five layers are byte-identical to the spike's `golden/` (`diff -r`), so every pin is the same. |
| 2.2 | `d129ae9` | `TestParseRejects`, 24 cases, each asserting the error names the entry: unknown key, unknown or duplicate layer, `base` listed, escaping paths (`..`, absolute, symlinks), missing file, wrong `ARG BASE` / `FROM ${BASE}` header. |
| 2.3 | `95f8687` | `TestResolveOrderIgnoresListing`, `TestResolveChangedLayerFile`, `TestResolveProjectLayerLast`, `TestResolveSharedPrefix`. |
| 2.4 | `2277fed` | Fake-tool unit tests for the command sequence, tag reuse, builder deletion on every failure and on cancel, and an unchanged output on failure. The gated test `SMOL_BUILDER_SMOLVM_TEST=1 go test -run TestIntegration -v ./internal/build/` passed in 33.4 s: the machine created from the pack printed `/usr/local/go/bin/go`, `go version go1.27.2 linux/amd64`, `layers=base+go` and `/workspace`, `pack_sha256` matched the file, and a forced `RUN false` project layer and an injected pack failure each left the previous output intact and no builder machine behind. |
| 2.5 | `2af5a2f` | The README's commands were run as written: build of `examples/go-libpq` (report `layers: [base, go, project]`, `pack_sha256` equal to `sha256sum`), then a machine from it printing `go1.27.2`, `libpq-dev` installed, `base+go`, `/workspace`. `go install` used a temporary `GOBIN`, and `pack prune` ran with `--dry-run` only. |

Findings:

- Packs are not byte-reproducible: the same image packed twice gave different SHA-256s. The image tag identifies the stack; `pack_sha256` identifies one file. That is what Sortie records.
- The base layer carries only opencode as an agent CLI, as the spike did. The stack-definition spec asks for "the supported agent CLIs". claude-agent-acp needs Node, which is in the `node` layer, not the base. See the open questions at the end of this file.
- The base layer's apt packages are not pinned by version, while the spec asks for every downloaded artifact to be pinned. Carried over unchanged from the spike.

## Task group 3: environment configuration and the local kind

Fork branch `feat/execution-environment`, worktree `~/dev/sortie-worktrees/execution-environment`. The baseline on `feat/session-control` was clean: `make test` passed, and `make lint` reported 0 issues for linux, darwin and windows. Every commit below passed `go vet`, the full `make test`, and `make lint`.

| Task | Commit | Verified by |
|---|---|---|
| 3.1 | `d152ddfa` | `TestEnvironmentConfig_Errors` (45 cases, one per error, each naming the dotted setting and the value), `TestValidateFrontMatter_Environment`, and `TestManager_ReloadRetainsOnUnknownEnvironmentKind`: a reload with `environment.kind: firecracker` keeps the previous config and prompt, and `LastLoadError()` names `environment.kind` and `firecracker`. |
| 3.2 | `1f3ee27d` | Unit tests for the `local` kind (`TestCommand`, `TestCommandRuns`, `TestGuestPathIsIdentity`, `TestLifecycleNoOps`, ...) and `TestEnvironmentKindsRegistered`, which resolves both kinds. The registry lives in `internal/environment` (`environment.Registry`), because `internal/config` already imports `internal/registry`. |
| 3.3 | `667bc38a` | Every existing test in `internal/workspace` and `internal/orchestrator` passes with no edit. One recording-environment test per call site: hooks in `Prepare`, `Finish`, `Cleanup`, `CleanupByPath`, `CleanupTerminal`; `env.Prepare` and `env.Remove`; the self-review diff and verification commands; handoff baseline and compare; reaction triage (host temporary files under `local`); the worker-exit cleanup; the sweep. |
| 3.4 | `7ea25009` | The existing handoff-evidence tests pass unchanged. `TestHandoffEvidence_UntrackedFileHashedInEnvironment` changes an untracked file in a directory standing in for the guest and sees the fingerprint change while the host directory stays empty. |
| 3.5 | `43b69e7c` | Lifecycle tests: an unrecorded workspace under `local` is kept with no `after_create` re-run and no `.sortie` created; the same workspace under a fake second kind gets the `local` kind's `Remove`, is prepared again with `after_create`, and is recorded as `{"kind":"fakevm","name":"vm-REC-1"}`. |

Notes:

- `git hash-object --stdin-paths` follows symlinks and stops at the first path it cannot hash, such as a dangling link. The implementation records such a path as unhashable, keeps the IDs already printed, and restarts after it, up to 64 such paths. A symlink retargeted to identical content, and a change of only the executable bit of an untracked file, no longer change the fingerprint.
- `local` writes no `.sortie/environment` record, because `.sortie` must not exist before `after_create` in a local workspace. A missing record means `local`, as the spec says.

## Task group 4: launch carrier

| Task | Commit | Verified by |
|---|---|---|
| 4.1 | `1e6151b0` | Every SSH test in `internal/agent/...` passes with no edit. `RemoteLaunch` over SSH is byte-identical to `BuildSSHLaunch`, preamble included. A fake carrier covers the env split, `AuxiliaryCommand`, connection-failure classification and early exit. `GuestPath` is the identity for local and SSH targets and maps `<host ws>/.sortie/x` to `<guest ws>/.sortie/x` for a carrier. |
| 4.2 | `27cd2beb` | One table test per adapter (opencode, clientprotocol, codex, claude-code, copilot) over local, SSH and carrier targets, asserting whether MCP servers are passed and which path is used. |
| 4.3 | `b1c83814` | `TestSessionLaunch_HostPrefixLocalEqualsPrefixedAgentCommand`, `..._HostPrefixPrecedesCarrier`, `..._HostPrefixPrecedesSSH`, `TestAuxiliaryCommand_NeverRunsBehindHostPrefix`, `..._HostPrefixKeepsTheSessionTokenOnTheHost` (real processes: the prefix sees the token and API URL, the guest side sees neither), and `credentialtest.AssertHostPrefix` for all five agent kinds, both for the session and the credential check. Removing the prefix from `SessionLaunch` makes 29 tests fail. |

Related commits: `9a79e456` removes host-only wrappers that only tests reached, so `make lint-no-tests` passes (eight test files moved to the `workspace.Target` signature, with no assertion change). `257de8a3` fixes a pre-existing 1.3% flake in `TestSessionToken_DoesNotTrimTruncatedText` (40 of 3000 runs failed before, 0 of 3000 after).

Notes:

- Under `local`, a prefix written into `agent.command` today also wraps auxiliary launches and canaries. `host_prefix` does not, as the spec requires. Session launches are byte-identical to the `agent.command` form.
- The carrier passes the adapter's own settings (for example `OPENCODE_CONFIG_CONTENT`) into the guest with `-e`. Decision 3 does not list them. They are agent configuration, not host variables, but they appear in the host process's argv.

## Task group 5: smolvm kind

Gated tests run with `SORTIE_SMOLVM_TEST=1` against the spike's base pack. Without the gate they skip. After every run, `smolvm machine ls` listed only `ag1`.

| Task | Commit | Verified by |
|---|---|---|
| 5.1 | `623f2daf` | A fake smolvm CLI (the test binary re-run as a script) for `TestCheckVersion`, `TestCheckHost_RefusesUnsupportedVersion`, `..._RefusesMissingBinary`, `TestParseMachines_RecordedListing` (the recorded `ls --json`), `TestCLI_WrapsFailures`, `TestCLI_KeepsLastEightKiBOfStderr`. `TestBoot_RefusesUnsupportedSmolvmVersion`: startup exits 1 and stderr names `1.30.0` and `1.26.x`. `TestWorkflowValidate_ReloadRefusesUnsupportedSmolvmVersion`: the reload is rejected and the previous config kept. |
| 5.2 | `9e3ff345` | `TestPrepare_CreatesAndStartsTheVM` asserts the exact `create` argv, with exactly two `-v`, one `--mount-socket`, and none of `--nested`, `--allow-system-mounts`, `--docker-socket`, `-p`, `--net`. Gated `TestIntegration_PrepareRealVM`: four labels, `"mounts": 2`, no network, control directory shared, `/opt/sortie` read-only, restart keeps the guest workspace. |
| 5.3 | `35851643` | `TestCommand_Argv` (the token and API URL never reach the argv), `TestLaunchArgv`, `TestConnectionFailed`. Gated `TestIntegration_HookRunsInTheGuest`: `after_create: uname -n > created-on` leaves the VM hostname in the guest file and no `created-on` on the host; the hook sees `SORTIE_ENVIRONMENT=smolvm` and `SORTIE_WORKSPACE=/workspace`. Gated `TestIntegration_ConnectionFailedOnRealExits`. |
| 5.4 | `88ab1a8f` | Gated `TestIntegration_ControlDirectoryIsShared`: the guest writes `blocked` and the host reads it with `workspace.ReadStatusFile`; a planted link to `/etc/passwd` is refused. Gated `TestIntegration_ExistingSortieEntryFailsPreparation`: a `.sortie` directory or a dangling link fails preparation naming `/workspace/.sortie`, unchanged. |
| 5.5 | `94a44e31`, `92722c10` | Fake-CLI tests for Release, Remove and Sweep selection (running key kept; unlabeled, unmanaged and other-workflow VMs kept; path-traversal labels ignored). Orchestrator tests: Release is the worker's last step for every exit kind; cleanup runs `before_remove`, then Remove, then deletes the directory, using the recorded kind. Gated `TestIntegration_ReleaseRemove` and `TestIntegration_Sweep` (the managed VM is deleted after its workspace goes, and an unlabeled VM stays running). `92722c10` makes dispatches without setup hooks prepare the environment too. |
| 5.6 | `424501a2` | `TestValidateDispatchConfig_AgentCredential*`: a bound variable, a `pass_env` variable, an unset variable, the failing case (error names the variable), rule-routed kinds. |
| 5.7 | `e2b1340b` | Migration `026_run_history_environment_image.sql` (upgrade from v25 and fresh database), `TestHandleState_Environment` and `TestHandleIssueDetail_Environment` with and without an environment, and a fake-CLI test that a rebuilt pack does not change an existing VM's recorded digest. |

Related commits: `d2cce0b7` moves the binary directory to `<state dir>/env-bin`, so it cannot collide with `<state dir>/env/<key>` for a key named `bin`. `2caf2ca8` makes `Prepare` refuse a VM labeled for another workflow instead of replacing it. `94ed3fc4` runs reaction triage with its input and result in the guest's `/run/sortie/triage/<run id>/` (verified tmpfs, mode 700, removed afterwards). `92ae99dd` fixes a pre-existing flake in `TestMaskTruncated_NothingRegisteredMatchesMask`.

Measured smolvm 1.26.1 facts that shaped the code:

- `machine exec` without `-i` or `--stream` holds back all output until the command exits, so non-interactive commands use `--stream`.
- A `--credential` without `--allow-host-pattern` opens the whole network (`curl https://example.com` returned 200). Credential hosts are therefore always added to the allowlist.
- The credential value is read from the environment of `machine start`, not `create` or `exec`.
- `--mount-socket` accepts a host socket path that does not exist yet.
- Machine names: at most 117 characters for a pack VM to start, `[A-Za-z0-9_-]`, no `--`, no trailing `-`. Sortie caps names at 63 characters and appends a hash of the key when it rewrites one.
- Errors before the guest command starts: `Error: vm not found: <name>` (exit 1), `Error: agent operation failed: connect: machine '<name>' is not running...` (exit 1), `Error: io operation failed: connection closed` (exit 1). These are classified as connection failures. A missing guest program or working directory exits 255 and is an agent failure.

## Task group 6: guest tool server

| Task | Commit | Verified by |
|---|---|---|
| 6.1 | `7298f4b1` | `TestMCPRelay_ListeningSocket` (with half-close), `TestMCPRelay_SocketClosesImmediately`, `TestMCPRelay_MissingSocket`, `TestMCPRelay_AcceptsButNeverAnswers`. EOF, a connect error, or any read error before the first response byte (which covers the measured `ECONNRESET`) prints `tool server unavailable` and exits 1. A first-byte deadline of 30 s bounds a listener that accepts and never answers. |
| 6.2 | `37627439` | `internal/toolsocket` tests (own identity per socket, restart at the same path including a stale socket file, 0600 mode, kill and reap on close) and orchestrator tests `TestServeToolSocket_TwoIssuesEachGetTheirOwnIdentity`, `TestRecoverToolSockets_ServesTheRecordedIdentityAfterRestart`, `TestToolSocket_RemovedWithTheWorkspace`. After a restart, the identity comes from `<state dir>/env/<key>/tool-session.json`, which the guest cannot reach, never from `.sortie`. |
| 6.3 | `fc80be76` | `TestGenerateMCPConfig_PerEnvironmentKind` (local, SSH, socket environment; no secret, database path, workflow path or host binary path in the guest file). Gated `TestIntegration_GuestCallsSortieToolsOnTheHost`: a static Sortie binary in `/opt/sortie`, a host `mcp-server` behind the socket, and a scripted MCP client in the guest running `/opt/sortie/sortie mcp-relay /run/sortie/tools.sock`; `sortie_status` returned this workspace's status (`turn_number:4, max_turns:17`). With the listener stopped, the relay exited non-zero with `tool server unavailable`, and a new listener at the same path served the running VM again. |

## Task group 7: repository sync

| Task | Commit | Verified by |
|---|---|---|
| 7.1 | `d152ddfa` (with 3.1) | `internal/config/environment_test.go`: `url` and `base` required, `branch_prefix` default `sortie/`, `max_bundle_mib` default 512 and positive, `project` required with `provider` naming `environment.smolvm.repository.project`. Nothing was missing, so 7.1 has no commit of its own. |
| 7.2 | `2b0fd588` | `TestFetchUpstream_MirrorsBranchesAndPrunesOnlyThem` (`refs/sortie/*` survives a prune), `TestLock_SerializesOperations`, `TestLock_WaitsForTheLockFileOfAnotherProcess`, and `TestFetchBundle_RunsNoFilterOrHookOnTheHost`: the test's global config defines a `filter.evil` driver, hooks, an fsmonitor and `fsckObjects=false`; the upstream commits `* filter=evil` and a hook-shaped file; fetch, import and bundle on the mirror leave no marker, while a control clone with the same config does. Every mirror command also sets `fetch.fsckObjects` and `receive.fsckObjects`, because each one set in a config file overrides `transfer.fsckObjects`. |
| 7.3 | `402de664` | Fake-environment `TestSeed_NewIssueBranchAtBase` asserts the guest command sequence. Gated `TestIntegration_SeedsTheRepositoryBeforeAfterCreate`: a new key's `after_create` sees `sortie/<key>` with HEAD equal to `origin/main` and the upstream `main`; an existing upstream branch is checked out at its tip; the guest bundle is gone. The guest `origin` URL has its credentials removed. |
| 7.4 | `b04fed63` | `TestRefresh_UpdatesOnlyRemoteTrackingRefs`, `TestRefresh_UpstreamFailureSendsTheMirrorsLastState`. Gated `TestIntegration_RefreshesOriginBeforeTheNextRun`: across a VM stop and a second preparation, the new upstream `main` is `origin/main`, and a local commit and an uncommitted file are unchanged. |
| 7.5 | `91367f4e` | `TestExport_RefusesABundleNamingAnotherRef`, `..._RefusesAnOversizedBundle`, `..._MissingBranchExportsNothing`, `..._UnchangedBranchImportsNothing`, each comparing the mirror's `for-each-ref` before and after; `..._ImportsCommittedWorkOnly`. |
| 7.6 | `21871148` | Against a local upstream: `TestExport_FirstPush`, `..._RebaseReplacesSortiesOwnPush`, `..._HumanCommitAfterSortiesPushFails` (`branch moved upstream`, upstream left at the human's commit), `..._HumanCreatedTheBranchBeforeSortiesFirstPush`. Gated `TestIntegration_ExportsAndPushesTheIssueBranch` runs export and push through a real VM. |
| 7.7 | `b82e5888` | `TestRunWorkerAttempt_ExportPrecedesTheHandoff`, `..._FailedExportFailsTheRun` (no transition, retry scheduled, workspace kept, failed row carries the error), `..._NoExportWithoutARepository`, `..._NoExportForACancelledRun`. Migration 027 adds `run_history.exported_commit`. |
| 7.8 | `5cd8689c` | `TestHandleWorkerExit_ReactionsIgnoreAForgedSCMFileUnderARepository` (all eight reaction seeds act on the recorded PR 42, not the forged 999), `TestRecoverPendingReactions_IgnoresAForgedSCMFileUnderARepository` (includes auto-merge), `TestFreshRunSeed_...`, `TestSweepWorkspaces_AgePassIgnoresAForgedPushTimeUnderARepository`, `TestEvaluateHandoffEvidence_...`. Forcing the source back to `.sortie/scm.json` makes all six forged-file tests fail. |

Related commit: `5f6ca01c` adds `/.sortie` to the seeded repository's `.git/info/exclude`. Without it, `git add -A` would commit the control-directory link, and every later seed of that branch would fail on the committed `.sortie`.

## Task group 8: pull requests

| Task | Commit | Verified by |
|---|---|---|
| 8.1 | `59023090` | `httptest` `TestEnsurePullRequest` in the GitHub, GitLab and Gitea adapters: found (returned unchanged, `Created` false), created, a lost create race (GitHub 422, GitLab and Gitea 409) answered by re-reading the list, and the scope error reported as `ErrSCMAuth` naming `repo` / "Pull requests: write", `api`, or `write:repository`. |
| 8.2 | `03488533` | `TestCheckRepositoryProvider` (mismatch names `environment.smolvm.repository.provider` and the reaction's provider setting; an adapter that cannot open pull requests is refused through `registry.SCMMeta.OpensPullRequests`), `TestRun_RepositoryProviderMismatchStopsStartup`, `TestRun_ExportAloneBuildsTheSCMAdapter`, `TestWorkflowValidate_ReloadRefusesARepositoryProviderMismatch`. |
| 8.3 | `158d069c` | `TestPullRequestTextFor` (suggested title and body, absent file, oversized file, control characters, title cut to 256 runes, malformed JSON, a link refused), `TestDefaultPullRequestText` (`ENG-42: Calculator adds wrong`), `TestExportIssueBranch_EnsuresThePullRequest` (the record stores `pr_number`, owner and repo; a failed call fails the export and is retried on the next run). |

Related commit: `6c356877` adds `ExportResult.UpToDate`, so a record write that failed after a successful push is repaired by the next run instead of being lost.

## Task group 9: documentation

| Task | Commit | Verified by |
|---|---|---|
| 9.1 | `ceb68e63`, `f8b087ed`, `388f5738` | A parity script (`/tmp/opencode/ee/parity.py`) found all 20 keys, 4 credential keys, 32 error fields and 39 error messages from the config code, the host check, the repository-provider check and the dispatch preflight in `docs/workflow-reference.md`. `make lint-docs` reports 0 errors. |
| 9.2 | `e892d833` | `docs/smolvm-environment-guide.md`. Its example workflow, with only the pack path replaced by a real pack, passes `sortie validate --format json` with `{"valid":true,"errors":[],"warnings":[]}`, including the smolvm version check and the agent-credential preflight. The claude-agent-acp variant also validates. |

Fixes found while checking the docs against the code, each with a test that fails before the fix:

- `aa7c9472` (security): under smolvm, hooks, triage and git inspection inherited every host `SORTIE_*` variable, and the carrier forwarded them into the guest with `-e`. That could put `SORTIE_TRACKER_API_KEY` in the guest. Now nothing is inherited when the environment runs elsewhere; `local` is unchanged.
- `a1598285`: `after_run` in a VM received the host path in `SORTIE_SELF_REVIEW_SUMMARY_PATH`; it now receives the guest path.
- `48630f01`: the provider-mismatch error named `label_commands.provider` instead of `reactions.label_commands.provider`.
- `0b9d0bf7`: `sortie validate` warned `unknown_key` for a provider block named only by a reaction or by `repository.provider`.

Accepted ADRs made stale by this change, not edited: 0009 (the MCP stdio sidecar: under smolvm the agent starts `mcp-relay`, and Sortie starts the tool server on the host) and 0018 (retention by age: under smolvm with a repository, `pushed_at` comes from `.sortie-host/scm.json`).

## Task group 10: end-to-end check

### Task 10.1: end-to-end check

Run on 2026-10-12 against the Sortie fork, branch `feat/execution-environment` at `650efbf0`, and smol-builder at `2af5a2f`. The harness is in `/tmp/opencode/ee/e2e`. None of it is committed.

**Deviation from the task text, chosen during implementation and pending the user's confirmation.** No GitHub repository was used, and nothing was pushed to the internet:

- The upstream is a local bare repository, `/tmp/opencode/ee/e2e/upstream.git`. `environment.smolvm.repository.url` is that plain path. The host fetches and pushes it with its own `git`.
- `repository.provider: github` with `project: e2e/calc`. The top-level `github:` block points `endpoint` at a fake GitHub REST server (`fakegh.py`, Python standard library, `127.0.0.1:7811`). The token is the fake value `fake-e2e-token`.
- The fake server serves only the two calls `EnsurePullRequest` makes: `GET /repos/{owner}/{repo}/pulls?state=open&head=owner:branch` and `POST /repos/{owner}/{repo}/pulls`. It logs every request as one JSON line, and answers anything else with 404. In the export-only configuration, Sortie made no other GitHub call: no scope check and no `GET /user`. The log below holds every request it received.

The first attempt, at `388f5738`, found a defect: the ACP session was created with the host workspace path as `cwd`. The fix and its codex counterpart are listed under "Fork commits" at the end. Every run below used the fixed binary.

#### Setup

The upstream repository, on `main`:

```text
go.mod             module example.com/calc, go 1.22
calc.go            func Add(a, b int) int { return a - b }
calc_test.go       TestAdd: Add(2, 3) must be 5 (fails)
smol-builder.toml  layers = ["go"]
```

The pack, built from that repository:

```sh
cd ~/dev/smol-builder && make build
bin/smol-builder build --repo /tmp/opencode/ee/e2e/src --output /tmp/opencode/ee/e2e/calc.smolmachine > pack-report.json
```

```json
{"layers":["base","go"],"image":"localhost/smol-builder/chain:bbac8014d8f16e8c","reused":["base","go"],"pack":"/tmp/opencode/ee/e2e/calc.smolmachine","pack_sha256":"183813ef9612b9c290a6e2eebde93f5b5dd0da8650cd6041608643da35edd93e","pack_bytes":292742911,"timings":{"layers":{"base":0,"go":0},"save":6.6,"builder":1.1,"pack":12.4,"total":21.5}}
```

The binaries, from the fork worktree:

```sh
CGO_ENABLED=0 go build -o /tmp/opencode/ee/e2e/sortie ./cmd/sortie
CGO_ENABLED=0 go build -o /tmp/opencode/ee/e2e/acp-broker ./acp-broker/cmd/acp-broker
```

`issues.json` holds one issue: `CALC-1`, "Calculator adds wrong", in state `To Do`.

`WORKFLOW.md`, abridged. The `before_run` and `after_run` hooks only record evidence into `.sortie/e2e-hooks.txt`; they are shown in full in the harness.

```yaml
tracker: {kind: file, active_states: [To Do], handoff_state: Review, terminal_states: [Done]}
file: {path: issues.json}
polling: {interval_ms: 2000}
workspace: {root: /tmp/opencode/ee/e2e/ws}
hooks:
  timeout_ms: 180000
  after_create: |
    go version > /tmp/after-create.txt
    git rev-parse --abbrev-ref HEAD >> /tmp/after-create.txt
    git config user.name "Sortie agent"
    git config user.email agent@e2e.invalid
  before_run: |   # branch, origin/main, HEAD, git branch -a
  after_run: |    # git log, go test, git remote -v, credential checks,
                  # SORTIE_* names, key-shaped strings, curl https://github.com
agent:
  kind: agent-client-protocol
  command: opencode acp
  max_turns: 1
environment:
  kind: smolvm
  host_prefix: [/tmp/opencode/ee/e2e/acp-broker, -registry-dir, /tmp/opencode/ee/e2e/broker, --]
  smolvm:
    pack: /tmp/opencode/ee/e2e/calc.smolmachine
    cpus: 2
    memory_mib: 4096
    allow_hosts: [openrouter.ai, models.opencode.ai, registry.npmjs.org]
    credentials:
      - {name: openrouter, env: OPENROUTER_API_KEY, hosts: [openrouter.ai]}
    repository:
      url: /tmp/opencode/ee/e2e/upstream.git
      base: main
      provider: github
      project: e2e/calc
github:
  api_key: $SORTIE_GITHUB_TOKEN     # fake-e2e-token
  endpoint: http://127.0.0.1:7811
server:
  port: 7820
```

The pack's opencode configuration selects `openrouter/anthropic/claude-haiku-5.5`. The `after_create` hook sets a git identity, because the base layer has none.

The prompt of run 1 tells the agent to fix `calc.go`, run `go test ./...`, commit on the current branch, write `{"title":"Fix Add()","body":"Adds a passing test run."}` to `.sortie/pr.json`, call `sortie_status` once and quote its result, and write `needs-human-review` to `.sortie/status`. The prompt of run 2 tells it to print `origin/main` and `.sortie/scm.json`, rebase onto `origin/main`, run the tests, call `sortie_status`, and write `needs-human-review`.

`watch.py` runs beside Sortie for each run. It records the following:

- `/api/v1/state` while the run is active.
- The work session's `acp-broker` process on the host: its argv and the names in its environment. It keeps the session token value in memory only.
- A dump of every guest process environment, through `smolvm machine exec`. It compares that dump on the host with the exact bytes of the OpenRouter key and of the session token, and records only booleans and SHA-256 prefixes.
- `smolvm machine ls --json` during and after the run.

#### Commands

```sh
cd /tmp/opencode/ee/e2e
python3 fakegh.py 7811 fakegh.log & echo $! > fakegh.pid
SORTIE_GITHUB_TOKEN=fake-e2e-token ./sortie --log-level debug --log-format json WORKFLOW.md > sortie-run1.log 2>&1 & echo $! > sortie.pid
python3 watch.py run1                      # waits for the run to end and the VM to stop
kill "$(cat sortie.pid)"                   # the file tracker keeps the Review handoff in memory only

# Between the runs: a maintainer commit on main, and a forged SCM file written in the guest
git -C src commit -m "Add a README" && git -C src push upstream.git main
smolvm machine start --name sortie-CALC-1
smolvm machine exec --name sortie-CALC-1 -w /workspace -- sh -c \
  'printf %s "{\"branch\":\"sortie/CALC-1\",\"sha\":\"0000000000000000000000000000000000000000\",\"pr_number\":999,\"owner\":\"evil\",\"repo\":\"other\"}" > .sortie/scm.json'
smolvm machine stop --name sortie-CALC-1

SORTIE_GITHUB_TOKEN=fake-e2e-token ./sortie ... WORKFLOW.md > sortie-run2.log 2>&1 & echo $! > sortie.pid
python3 watch.py run2
kill "$(cat sortie.pid)"

sed -i 's/"To Do"/"Done"/' issues.json     # move CALC-1 to Done
SORTIE_GITHUB_TOKEN=fake-e2e-token ./sortie ... WORKFLOW.md > sortie-done.log 2>&1 & echo $! > sortie.pid
```

The file tracker keeps a transition in memory and never writes the file. So the handoff to `Review` lasts only while one Sortie process runs. Restarting Sortie is how run 2 is dispatched again, from `To Do` in the file. With the issue `Done` in the file, the restart hands it to the startup terminal cleanup.

Twice, between runs, the stopped VM was started by hand to read evidence. First, `opencode export <session>` printed each run's transcript. Second, the forged file was written. Each time the VM was stopped again before Sortie next ran.

The create argv is not in Sortie's log. A separate probe captured it. The probe used a copy of the workflow in `argv/`, with its own state directory and issue `CALC-2`, and `environment.smolvm.binary` set to a wrapper that logs every argv and then runs `smolvm`. Its `after_create` exits 1, so no agent starts and no model is called.

#### Output: the first attempt, at `388f5738`

The credential verification session failed at once, and opencode's log in the guest showed why:

```text
WARN worker run failed, scheduling retry  error="agent session start: agent: credential_unverified: the agent runtime did not complete a credential verification request: session/prompt error -32603: Internal error: OpenCode service failure: {\"service\":\"session\"}"
# /root/.local/share/opencode/log/opencode.log in the guest
message="creating instance" directory=/workspace
message="creating instance" directory=/tmp/opencode/ee/e2e/ws/CALC-1
```

`session/new` carried the host workspace path as `cwd`, and that path does not exist in the VM. The same `opencode acp`, driven by hand with `cwd: /workspace` in the same VM, answered `pong`. After the fix, both sessions name `/workspace`.

#### Output: run 1

`watch-run1.log`, with long lines cut:

```text
[11:22:12] host key sha256 prefix f8b504d480920a41
[11:22:12] CALC-1 running; environment: {"kind": "smolvm", "name": "sortie-CALC-1"}
[11:22:23] broker pid 703208 argv: /tmp/opencode/ee/e2e/acp-broker -registry-dir /tmp/opencode/ee/e2e/broker -- smolvm machine exec --name sortie-CALC-1 -i -w /workspace -e SORTIE_ISSUE_ID=1 -e SORTIE_ISSUE_IDENTIFIER=CALC-1 ... -e SORTIE_SESSION_PURPOSE=work -e SORTIE_WORKSPACE=/workspace -e SORTIE_ENVIRONMENT=smolvm -- opencode acp
[11:22:23] broker env SORTIE_* names: SORTIE_API_URL SORTIE_ATTEMPT SORTIE_CHAIN_ID SORTIE_DISPATCH_ID SORTIE_GITHUB_TOKEN SORTIE_ISSUE_ID SORTIE_ISSUE_IDENTIFIER SORTIE_RULE SORTIE_SESSION_PURPOSE SORTIE_SESSION_TOKEN SORTIE_WORKSPACE
[11:22:23] broker has SORTIE_SESSION_TOKEN: True length 44 | SORTIE_API_URL: http://127.0.0.1:7820
[11:22:23] guest opencode processes: 113 opencode acp
[11:22:23] guest environ dump bytes: 2054 processes: 3
[11:22:23] guest environ contains the OpenRouter key: False
[11:22:23] guest environ contains the session token value: False
[11:22:23] guest environ mentions SORTIE_SESSION_TOKEN: False | SORTIE_API_URL: False
[11:22:23] agent env: HOME=/root NODE_EXTRA_CA_CERTS=/run/smol/credentials/ca.pem OPENROUTER_API_KEY=<smolvm placeholder>... (len 60) SORTIE_ATTEMPT SORTIE_CHAIN_ID SORTIE_DISPATCH_ID SORTIE_ENVIRONMENT=smolvm SORTIE_ISSUE_ID SORTIE_ISSUE_IDENTIFIER=CALC-1 SORTIE_RULE SORTIE_SESSION_PURPOSE SORTIE_STACK_LAYERS SORTIE_WORKSPACE=/workspace SSL_CERT_FILE=/run/smolvm/ca-bundle.pem
[11:22:23] key-shaped strings in guest environs (sha256 prefixes): none
[11:22:23] ls --json during the run: {"name": "sortie-CALC-1", "state": "running", "network": true, "mounts": 2, "labels": {"sortie.managed": "1", "sortie.pack": "183813ef...edd93e", "sortie.workflow": "3ae3b5b25b17", "sortie.workspace": "CALC-1"}, "cpus": 2, "memory_mib": 4096}
[11:23:01] run ended
[11:23:01] ls --json after the run: {"name": "sortie-CALC-1", "state": "stopped", ...}
```

Sortie's log (INFO records, issue fields removed):

```text
11:22:12.310 issue dispatched                      agent_kind=agent-client-protocol
11:22:13.162 created VM                            vm=sortie-CALC-1
11:22:13.162 seeding repository
11:22:13.528 running hook                          hook=after_create
11:22:13.607 running hook                          hook=before_run
11:22:23.129 agent credential verified             duration_ms=9316
11:22:27.369 agent session started                 session_id=ses_ecedce302ffejnVKGfjPfgYjCA
11:22:30.967 tool call completed                   tool=execute duration_ms=464
11:22:33.294 tool call completed                   tool=other duration_ms=80        # sortie_status, through the relay
11:22:53.201 tool call completed                   tool=execute duration_ms=20873   # sed, go test, git commit, pr.json
11:22:56.529 tool call completed                   tool=execute duration_ms=62      # .sortie/status
11:22:59.142 agent signaled status, exiting worker status=needs-human-review turns_completed=1
11:22:59.204 running hook                          hook=after_run
11:23:00.304 issue branch exported and pushed      branch=sortie/CALC-1 commit=b6403758d9f61e47741458dd1be05a9dce283e1f
11:23:00.307 pull request ensured for the issue branch  pr_number=1 url=https://github.example/e2e/calc/pull/1 created=true
11:23:00.307 worker exiting                        exit_kind=normal turns_completed=1
11:23:00.642 stopped VM                            vm=sortie-CALC-1
11:23:00.643 handoff transition succeeded, releasing claim  handoff_state=Review
```

`.sortie/e2e-hooks.txt`, written in the guest by the hooks:

```text
== before_run 17:22:13
branch: sortie/CALC-1
origin/main: c655e8b Add calc with a failing test
HEAD: c655e8b Add calc with a failing test
* sortie/CALC-1
  remotes/origin/main
== after_run 17:22:59
branch: sortie/CALC-1
b640375 Fix Add() to return sum of inputs
c655e8b Add calc with a failing test
-- status
-- go test
ok  	example.com/calc	(cached)
-- remote
origin	/tmp/opencode/ee/e2e/upstream.git (fetch)
origin	/tmp/opencode/ee/e2e/upstream.git (push)
-- credential config
none
-- credential files
ls: cannot access '/root/.git-credentials': No such file or directory
ls: cannot access '/root/.ssh': No such file or directory
ls: cannot access '/root/.config/gh': No such file or directory
-- git credential env
none
-- SORTIE_* names in the hook
SORTIE_ATTEMPT SORTIE_ENVIRONMENT SORTIE_ISSUE_ID SORTIE_ISSUE_IDENTIFIER SORTIE_SELF_REVIEW_STATUS SORTIE_STACK_LAYERS SORTIE_WORKSPACE
-- OPENROUTER_API_KEY prefix: <smolvm placeholder>
-- key-shaped strings (sha256 prefixes)
procdone
filesdone
-- github.com from the guest
curl: (6) Could not resolve host: github.com
curl exit=6
```

The guest's `git log` shows the agent's commit, by the identity `after_create` set:

```text
$ git -C upstream.git show -s --format='%H %an <%ae>%n%s' sortie/CALC-1
b6403758d9f61e47741458dd1be05a9dce283e1f Sortie agent <agent@e2e.invalid>
Fix Add() to return sum of inputs
 calc.go | 2 +-
```

`fakegh.log` after run 1:

```text
{"method": "GET", "path": "/repos/e2e/calc/pulls?head=e2e%3Asortie%2FCALC-1&per_page=100&state=open", "auth": "Bearer fake-e2e-token", "body": null, "status": 200, "returned": [], "ts": "11:23:00"}
{"method": "POST", "path": "/repos/e2e/calc/pulls", "auth": "Bearer fake-e2e-token", "body": {"title": "Fix Add()", "head": "sortie/CALC-1", "base": "main", "body": "Adds a passing test run."}, "status": 201, "created": 1, "ts": "11:23:00"}
```

The trusted record `ws/CALC-1/.sortie-host/scm.json`, mode 0600 in a 0700 directory:

```json
{"branch":"sortie/CALC-1","sha":"b6403758d9f61e47741458dd1be05a9dce283e1f","pushed_at":"2026-10-12T17:23:00Z","pr_number":1,"owner":"e2e","repo":"calc"}
```

The agent's tool calls and final message, from `opencode export` in the guest:

```text
TOOL bash {"command": "cat calc.go; cat calc_test.go; git status; git branch --show-current"} -> ... On branch sortie/CALC-1 ...
TOOL bash {"command": "sed -i 's/return a - b }/return a + b }/' calc.go && cat calc.go && go test ./... && git add calc.go && git commit -qm \"Fix Add() to return sum of inputs\" && ... > .sortie/pr.json ..."} -> ok  example.com/calc 0.002s ... b640375 Fix Add() to return sum of inputs
TOOL sortie-tools_sortie_status {} -> {"data":{"turn_number":1,"max_turns":1,"turns_remaining":0,"attempt":null,"session_duration_seconds":5.768,"tokens":{...null...},"tokens_measured":false},"success":true}
TOOL bash {"command": "... printf '%s\\n' \"needs-human-review\" \"Fixed Add() in calc.go ...\" > .sortie/status ..."}
TEXT ... - **Status tool result:** `{"data":{"turn_number":1,"max_turns":1,"turns_remaining":0,"attempt":null,"session_duration_seconds":5.768,...,"tokens_measured":false},"success":true}`
```

`smolvm machine egress-events --name sortie-CALC-1` after run 1:

```text
TIMESTAMP                      OP        DESTINATION
2026-10-12T17:22:13Z           resolve   container
2026-10-12T17:22:13Z           resolve   container
2026-10-12T17:23:00Z           resolve   github.com
2026-10-12T17:23:00Z           resolve   github.com
```

The `github.com` denials are the hook's `curl`. The `container` lookups happen at VM start, before any hook or agent runs.

#### Output: run 2

Before run 2, the upstream got a maintainer commit on `main`, `aef11c5 Add a README`. The guest wrote the forged file `/workspace/.sortie/scm.json`, which is the shared `.sortie`: `{"branch":"sortie/CALC-1","sha":"0000…","pr_number":999,"owner":"evil","repo":"other"}`.

`watch-run2.log` repeats every finding of run 1: the token is present in the broker (length 44) and absent from the guest, the key is absent from the guest, and no key-shaped string is in any guest environment. The VM was `running` during the run and `stopped` after it.

`.sortie/e2e-hooks.txt`, from run 2's `before_run` and `after_run`:

```text
== before_run 17:24:25
branch: sortie/CALC-1
origin/main: aef11c5 Add a README
HEAD: b640375 Fix Add() to return sum of inputs
* sortie/CALC-1
  remotes/origin/main
  remotes/origin/sortie/CALC-1
== after_run 17:24:42
branch: sortie/CALC-1
95c2ca4 Fix Add() to return sum of inputs
aef11c5 Add a README
c655e8b Add calc with a failing test
```

The agent's tool calls, from `opencode export`:

```text
TOOL bash {"command": "git log --oneline -1 origin/main; cat .sortie/scm.json"} -> aef11c5 Add a README
  {"branch":"sortie/CALC-1","sha":"0000000000000000000000000000000000000000","pr_number":999,"owner":"evil","repo":"other"}
TOOL bash {"command": "git rebase origin/main && go test ./..."} -> Successfully rebased and updated refs/heads/sortie/CALC-1. ok example.com/calc (cached)
TOOL sortie-tools_sortie_status {} -> {"data":{"turn_number":1,"max_turns":1,"turns_remaining":0,"attempt":null,"session_duration_seconds":4.243,...},"success":true}
TOOL bash {"command": "mkdir -p .sortie && echo \"needs-human-review\" > .sortie/status && cat .sortie/status"}
```

Sortie's log:

```text
11:24:25.006 tool sockets listening again           workspaces=["CALC-1"]
11:24:25.019 issue dispatched
11:24:25.575 running hook                          hook=before_run        # no "created VM", no after_create
11:24:30.665 agent credential verified             duration_ms=4924
11:24:42.525 agent signaled status, exiting worker status=needs-human-review turns_completed=1
11:24:42.540 running hook                          hook=after_run
11:24:43.246 issue branch exported and pushed      branch=sortie/CALC-1 commit=95c2ca4325344f307dc0a4c17018b66028fd1d02
11:24:43.248 pull request ensured for the issue branch  pr_number=1 url=https://github.example/e2e/calc/pull/1 created=false
11:24:43.415 handoff transition succeeded, releasing claim  handoff_state=Review
```

The upstream, the fake server, and the two SCM files after run 2:

```text
$ git -C upstream.git log --oneline --graph --all --decorate
* 95c2ca4 (sortie/CALC-1) Fix Add() to return sum of inputs
* aef11c5 (HEAD -> main) Add a README
* c655e8b Add calc with a failing test

$ tail -1 fakegh.log          # one GET, no second POST
{"method": "GET", "path": "/repos/e2e/calc/pulls?head=e2e%3Asortie%2FCALC-1&per_page=100&state=open", "auth": "Bearer fake-e2e-token", "body": null, "status": 200, "returned": [1], "ts": "11:24:43"}

$ cat ws/CALC-1/.sortie-host/scm.json
{"branch":"sortie/CALC-1","sha":"95c2ca4325344f307dc0a4c17018b66028fd1d02","pushed_at":"2026-10-12T17:24:43Z","pr_number":1,"owner":"e2e","repo":"calc"}
$ cat ws/CALC-1/.sortie/scm.json
{"branch":"sortie/CALC-1","sha":"0000000000000000000000000000000000000000","pr_number":999,"owner":"evil","repo":"other"}
```

`/api/v1/state` during run 2 (`state-run2-running.json`, abridged), and after it:

```text
{"issue_identifier": "CALC-1", "state": "To Do", "environment": {"kind": "smolvm", "name": "sortie-CALC-1"}, ...}
{"counts": {"running": 0, "retrying": 0, "budget_exhausted": 0}, "running": [], "retrying": []}
```

#### Output: run history

```text
$ sqlite3 -header .sortie.db 'select id,identifier,attempt,started_at,completed_at,status,error,turns_completed,environment_image_sha256,exported_commit from run_history'
id|identifier|attempt|started_at|completed_at|status|error|turns_completed|environment_image_sha256|exported_commit
1|CALC-1|1|2026-10-12T17:22:12Z|2026-10-12T17:23:00Z|succeeded||1|183813ef9612b9c290a6e2eebde93f5b5dd0da8650cd6041608643da35edd93e|b6403758d9f61e47741458dd1be05a9dce283e1f
2|CALC-1|1|2026-10-12T17:24:25Z|2026-10-12T17:24:43Z|succeeded||1|183813ef9612b9c290a6e2eebde93f5b5dd0da8650cd6041608643da35edd93e|95c2ca4325344f307dc0a4c17018b66028fd1d02
```

`/metrics` after run 1: `sortie_handoff_transitions_total{result="success"} 1`.

#### Output: Done

```text
$ smolvm machine ls             # before the restart
sortie-CALC-1 stopped          2   4096 MiB       2       0   20 GiB   10 GiB  -
$ sed -i 's/"To Do"/"Done"/' issues.json; ./sortie ... WORKFLOW.md
11:25:11.592 INFO deleted VM          vm=sortie-CALC-1 workspace=/tmp/opencode/ee/e2e/ws/CALC-1
11:25:11.592 INFO workspace cleaned   identifier=CALC-1
gone after 1s
$ smolvm machine ls --json | jq -r '.[].name'
ag1
$ ls -A ws/ env/
(both empty)
```

The issue's tool-socket directory, `env/CALC-1`, went with the workspace. The mirror `repos/58fcfbb062ae8f8b.git` stays, as designed.

#### Output: the create argv (probe)

```text
machine create --name sortie-CALC-2 --from /tmp/opencode/ee/e2e/calc.smolmachine --cpus 2 --mem 4096 --label sortie.managed=1 --label sortie.workspace=CALC-2 --label sortie.pack=183813ef9612b9c290a6e2eebde93f5b5dd0da8650cd6041608643da35edd93e --label sortie.workflow=d4fff01438f9 --allow-host-pattern openrouter.ai --allow-host-pattern models.opencode.ai --allow-host-pattern registry.npmjs.org --credential openrouter=OPENROUTER_API_KEY@openrouter.ai -v /tmp/opencode/ee/e2e/argv/ws/CALC-2/.sortie:/run/sortie/control -v /tmp/opencode/ee/e2e/argv/env-bin:/opt/sortie:ro --mount-socket /tmp/opencode/ee/e2e/argv/env/CALC-2/tools.sock:/run/sortie/tools.sock
machine start --name sortie-CALC-2
machine exec --name sortie-CALC-2 -i -w /workspace -e SORTIE_WORKSPACE=/workspace -e SORTIE_ENVIRONMENT=smolvm -- sh -c umask 077 && mkdir -p "$1" && cat > "$1/in.bundle" sh /run/sortie/git
machine exec --name sortie-CALC-2 --stream -w /workspace ... -- git clone --quiet --no-checkout --origin origin -- /run/sortie/git/in.bundle /workspace
machine exec --name sortie-CALC-2 --stream -w /workspace ... -- sh -c exclude="$(git rev-parse --git-path info/exclude)" && ... printf '%s\n' /.sortie >> "$exclude"
machine exec --name sortie-CALC-2 --stream -w /workspace ... -- git checkout --quiet --no-track -b sortie/CALC-2 origin/main
machine exec --name sortie-CALC-2 --stream -w /workspace ... -- git remote set-url origin /tmp/opencode/ee/e2e/upstream.git
machine exec --name sortie-CALC-2 --stream -w /workspace ... -- rm -f -- /run/sortie/git/in.bundle
machine exec --name sortie-CALC-2 --stream -w /workspace -e SORTIE_ISSUE_ID=2 ... -- sh -c echo "probe: stop before any agent launch" >&2; exit 1
machine stop --name sortie-CALC-2
```

The failed `after_create` removed the probe's host workspace and left the stopped VM. A Sortie restart with `CALC-2` in `Done` logged `sweep: deleted leftover VM vm=sortie-CALC-2`.

#### Result

- **Seeded on the issue branch, and no git host in the allowlist.** In the first run, `before_run` in the guest shows `branch: sortie/CALC-1` at `origin/main` (`c655e8b`). The create argv allows only `openrouter.ai`, `models.opencode.ai` and `registry.npmjs.org`, and the credential binding's host is `openrouter.ai`. The guest's `origin` is the plain upstream path, with no userinfo. A `curl https://github.com` from the guest failed to resolve, and `egress-events` lists `github.com` as denied.
- **The agent fixed the test and committed.** The guest's `git log` shows `b640375 Fix Add() to return sum of inputs`, by the git identity that `after_create` set. In `after_run`, `go test ./...` reports `ok`.
- **Export, push, and pull request.** Sortie pushed `sortie/CALC-1` at `b6403758` to the upstream. It then listed the open pull requests for the branch and created one: `POST /repos/e2e/calc/pulls` with title `Fix Add()` and body `Adds a passing test run.`, which the agent wrote in `.sortie/pr.json`. The trusted record `.sortie-host/scm.json` holds the branch, the SHA, the push time, and `pr_number` 1.
- **Second run.** In the guest, `before_run` shows `origin/main: aef11c5 Add a README` and `remotes/origin/sortie/CALC-1`. `after_create` did not run again. The agent rebased onto `origin/main`. Sortie replaced its own earlier push with a lease, now `95c2ca4` on top of `aef11c5`. It then found pull request 1 (`GET` returned `[1]`) and created none. The guest wrote a forged `.sortie/scm.json` with `pr_number: 999`, owner `evil`, repo `other`. The agent saw that forged file, but the trusted record still names pull request 1 in `e2e/calc`, now with SHA `95c2ca4`.
- **`sortie_status` from the VM.** In both runs the agent called `sortie-tools_sortie_status`, through `/opt/sortie/sortie mcp-relay /run/sortie/tools.sock`. It got `{"turn_number":1,"max_turns":1,"turns_remaining":0,...}` and quoted the result in its final message. After the Sortie restart before run 2, the log shows `tool sockets listening again workspaces=["CALC-1"]`.
- **Handoff evidence sees the commit.** Both runs passed the evidence check under the default `observed` policy. Both were recorded `succeeded` and moved to `Review`, with `sortie_handoff_transitions_total{result="success"}`. A run with no work would have been withheld and recorded `failed`. Here the verdict is work observed, because the trusted record names the pushed commit. That record is checked before the guest-side commit comparison, and Sortie logs no verdict when work is observed. Run history records `exported_commit` `b6403758…` and `95c2ca43…`, and `environment_image_sha256` equal to the pack's `pack_sha256`.
- **No secret in the guest.**
  - *The API key.* The guest saw only `OPENROUTER_API_KEY=<smolvm placeholder>…` (60 characters). No guest process environment held the key's bytes. No `sk-or-v1-` string was in any guest environment, or in any file under `/root`, `/workspace`, `/tmp`, `/etc`, `/opt` and `/run` (`procdone` and `filesdone` with no prefix before them; the host key's prefix is `f8b504d480920a41`).
  - *The session token.* The broker on the host had `SORTIE_SESSION_TOKEN` (44 bytes) and `SORTIE_API_URL`. No guest process environment held the token's value or either name, and neither did the hooks.
  - *Git credentials.* The guest has none:
    - no `credential.*`, `url.*` or `http.*` git configuration;
    - no `~/.git-credentials`, `~/.ssh` or `~/.config/gh`;
    - none of `SSH_AUTH_SOCK`, `GIT_ASKPASS`, `GH_TOKEN`, `GITHUB_TOKEN` or `GIT_SSH_COMMAND`;
    - an `origin` URL with no userinfo.
  - *The GitHub token.* `SORTIE_GITHUB_TOKEN` is in Sortie's environment and the broker's, but not in the guest agent's environment or the hooks' `SORTIE_*` names.
- **VM lifecycle.** The VM was `running` during each run and `stopped` within a second after it (`stopped VM`). When the issue was `Done`, the startup terminal cleanup deleted the VM and the host workspace. `smolvm machine ls` then listed only `ag1`.

Observations, not failures:

- **The file tracker needs a restart to dispatch again.** Its transitions are kept in memory, so the handoff to `Review` lasts for the life of one Sortie process. A second dispatch, and the move to `Done`, need a restart. So the `Done` cleanup was the startup terminal cleanup, not reconciliation in a running process.
- **No token usage is measured for opencode in the VM.** Both rows have `tokens_measured` 0, and `sortie_status` reports `tokens_measured: false`. This check did not determine whether a local opencode session behaves the same.
- **`egress-events` lists two denied `resolve container` lookups at every VM start,** before any hook or agent runs. They are smolvm's own lookups and had no effect.

#### Fork commits

| Commit | Fix | Verified by |
|---|---|---|
| `f68b6e96` | `fix(clientprotocol)`: `session/new`, `session/load` and `session/resume` name the guest workspace (`LaunchTarget.GuestPath`) as `cwd` in an execution environment. Before this, every ACP session under smolvm failed, credential verification included. | `TestSessionCwdByLaunchTarget`: local and carrier, for a work session and for credential verification. The carrier cases fail before the fix with the host path. |
| `650efbf0` | `fix(codex)`: `thread/start` `cwd`, `turn/start` `cwd`, and the sandbox's `writableRoots` name the guest workspace. This is the same defect class; codex in a VM was not run. | `TestStartThread_CwdIsTheGuestWorkspaceInAnEnvironment`, `TestRunTurn_CwdAndWritableRootAreTheGuestWorkspaceInAnEnvironment`. Both fail before the fix. |

`go vet`, the full `make test`, and `make lint` (0 issues on linux, darwin and windows) passed after both commits.

#### Cost

Two work turns, one per run, on `anthropic/claude-haiku-5.5`. Each run also sent one credential verification prompt (`Say exactly: SORTIE_CREDENTIAL_OK`), and opencode makes its own title call on a small model. One more one-word prompt (`pong`) was sent by hand while diagnosing the first attempt. The first attempt failed before any model call. The probe made none.
