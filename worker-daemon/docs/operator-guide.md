# Worker daemon operator guide

`worker-daemon` runs on a worker host (taichi, a Mac, later a homelab
node) and runs the control plane's jobs there. It speaks worker API
protocol 1 (`agent-platform/worker-api`), the API Sortie serves under
`environment.kind: remote` ([Sortie's workflow reference, "Kind
`remote`"](../../sortie/docs/workflow-reference.md#kind-remote)).

- It **dials out only.** It opens every connection to the control plane
  itself and listens on nothing but its guest-facing forwarders, which are
  Unix sockets in each job's directory that only that job's machine is
  given.
- It **holds no credential but its certificate.** It has no model key, no
  forge or tracker token and no deploy key. A job's flow tokens arrive with
  its offer, stay in memory, are added to that flow's requests by the
  daemon, and are never given to the guest, written to disk, or logged.
- It **keeps the host's reserve and floor.** It declares free only what is
  left above them, polls only while that holds its smallest job, and
  refuses a job that would go below either.

## Install

Release archives hold one static binary per platform: `linux/amd64`,
`linux/arm64` and `darwin/arm64`. From a checkout:

```sh
make -C worker-daemon build     # bin/worker-daemon for this host
make -C worker-daemon cross     # bin/worker-daemon_<os>_<arch> for all three
worker-daemon version           # prints the version and "worker API protocol 1"
```

The host needs:

- smolvm 1.32.4 or newer on `PATH` (or `smolvm.binary`), with KVM on Linux
  (`/dev/kvm` readable and writable by the daemon's user) or
  Hypervisor.framework on an Apple silicon Mac.
- A smolvm pack for each image the control plane names (`run.image`), with
  `sh`, `git` and `socat` in it. smol-builder's `base` layer has all three.
  The daemon does not pull images: `smolvm.packs` maps each image reference
  to a pack file on this host.

## Configure

`worker-daemon run -config FILE` reads a JSON file
(default `/etc/worker-daemon/config.json`); `worker-daemon check -config
FILE` validates one and exits. Unknown keys are refused, so a typo fails
the start instead of being ignored.

```json
{
  "worker_id": "taichi",
  "control_plane": {
    "url": "http://127.0.0.1:7690",
    "identity_header": "X-Sortie-Worker-Identity",
    "tls": { "cert_file": "", "key_file": "", "ca_file": "", "server_name": "" }
  },
  "run_slots": 2,
  "reserve": { "mem_gib": 16, "cpu": 2 },
  "floor": { "disk_gib": 50 },
  "smallest_job": { "cpu": 2, "mem_gib": 8, "disk_gib": 20 },
  "state_dir": "/var/lib/worker-daemon",
  "disk_path": "/var/lib/smolvm",
  "smolvm": {
    "binary": "smolvm",
    "packs": { "ghcr.io/ortusnova/agent:1": "/var/lib/worker-daemon/packs/agent.smolmachine" },
    "pack_dir": "/var/lib/worker-daemon/packs",
    "user": "builder"
  },
  "usage_commands": { "opencode": ["agent-usage", "opencode"], "*": ["agent-usage"] },
  "log_level": "info"
}
```

| Key | Required | Default | Meaning |
|---|---|---|---|
| `worker_id` | yes | | The worker's name, sent in the identity header on every worker API request. Letters, digits, `.`, `_`, `-`. |
| `control_plane.url` | yes | | The control plane's base URL, `http` or `https`, without user information. The daemon dials it and nothing dials the daemon. See [The control-plane connection](#the-control-plane-connection). |
| `control_plane.identity_header` | no | `X-Sortie-Worker-Identity` | The header that names the worker; Sortie's `worker.identity_header`. |
| `control_plane.tls.cert_file`, `key_file` | no | | The worker's client certificate and key (PEM), once the control plane issues one. Both or neither; they need an `https` URL. The key is the one secret the worker keeps. |
| `control_plane.tls.ca_file` | no | system roots | The CA that signs the control plane's (and the flows') server certificates. |
| `control_plane.tls.server_name` | no | the URL's host | The name to verify the server certificate against. |
| `run_slots` | no | from the host | How many jobs run at once. Unset or `0`: as many `smallest_job`s as the host's CPUs and memory hold above `reserve`, at least one. See [Run slots](#run-slots). |
| `reserve.mem_gib` | yes | | Memory, in GiB, the host keeps for itself. `free.mem_gib` is what the host has available less this, less what running jobs were promised. |
| `reserve.cpu` | no | `0` | CPUs the host keeps for itself. |
| `floor.disk_gib` | yes | | Disk, in GiB, kept free on `disk_path`'s file system. `free.disk_gib` is what is available there less this, less running jobs' disks. |
| `smallest_job.cpu`, `mem_gib`, `disk_gib` | no | `0` | What the smallest job this worker should take needs. The daemon does not poll while less than this is free. |
| `state_dir` | yes | | Absolute. Holds one directory per running job (`jobs/<job>-<attempt>`, mode 0700): the workspace seed, the exported bundle and the forwarders' sockets. Removed when the job ends. Keep the path short: a Unix socket path is limited to 104 bytes on macOS. |
| `disk_path` | no | `state_dir` | Where free disk is measured: the file system smolvm keeps machines on. |
| `smolvm.binary` | no | `smolvm` | The smolvm program. |
| `smolvm.packs` | no | | Maps an image reference (`run.image`) to an absolute pack path. |
| `smolvm.pack_dir` | no | | Also resolves an image reference ending in `.smolmachine` to the file of that base name here. |
| `smolvm.user` | no | the pack's | The user commands run as in the machine; it is given `/workspace`. |
| `smolvm.nested` | no | what the host offers | `false` keeps the worker from declaring nested virtualization, so it gets no job that requires `nested`: for a host without the inputs of the images that need it. |
| `smolvm.images` | no | | Maps an image reference to what this host gives each of its jobs beyond the pack. See [Host inputs of an image](#host-inputs-of-an-image). |
| `usage_commands` | no | | Maps an agent kind, the base name of `run.command`, to the command run in the machine after each run to read its token usage; `*` covers every other kind. See [Token usage](#token-usage). |
| `log_level` | no | `info` | `debug`, `info`, `warn` or `error`. Logs go to standard error and never carry a token. |

### Reserve, floor and the smallest job

Each check-in declares `free.cpu`, `free.mem_gib` and `free.disk_gib` as
what the host has available now less the reserve or floor, less what the
running jobs were promised, rounded down and never below zero. With
`reserve.mem_gib: 16` and 20 GiB available, the daemon declares
`free.mem_gib: 4`. A machine takes its memory and disk as it uses them, so
counting a running job's whole share can count part of it twice; that errs
toward keeping the reserve.

The daemon polls only while a run slot is free and what it would declare
holds `smallest_job`. When an offered job's `requires` exceeds what is free
(the control plane counted with an older check-in), the daemon does not
start it: it reports the attempt `failed`, machine `deleted`, with the
reason, and Sortie retries it.

### Run slots

Without `run_slots`, each check-in declares
`floor(min((CPUs - reserve.cpu) / smallest_job.cpu, (MemTotal - reserve.mem_gib) / smallest_job.mem_gib))`
slots, at least one, from the host as it is then: a host that gains or
loses CPUs or memory declares its new slots at its next check-in, with no
change to this file. A dimension whose `smallest_job` figure is `0` does
not bound it; with neither, the worker runs one job. A slot only lets a
job be offered: each job still needs its own `requires` free, and free
memory is the host's `MemAvailable` (macOS: `vm_stat`) now, so memory
that anything else on the host takes, such as a model server or a
sandbox, is not offered to a job.

### Host inputs of an image

Some jobs need what only their host has: a homelab job needs homelab's
caches, a folder for its flight recorder, the vm age key and homelab's
guest kernel. The control plane names none of it; it names the image, and
`smolvm.images` says what a job of that image gets on this host:

```json
"images": {
  "homelab.smolmachine": {
    "user": "builder",
    "lib_dir": "/home/brian/.cache/homelab/libkrunfw",
    "mounts": [
      { "host": "/home/brian/.cache/homelab-sources", "guest": "/home/builder/.cache/homelab-sources", "read_only": true },
      { "host": "/home/brian/.local/state/homelab/sortie-recorder", "guest": "/var/log/homelab-recorder", "per_job": true }
    ],
    "env": { "NODE_EXTRA_CA_CERTS": "/usr/local/share/ca-certificates/ortus-egress.crt" },
    "env_files": { "HOMELAB_VM_AGE_KEY": "/home/brian/.local/share/ortus-nova/stage0/inputs/vm-age/keys.txt" },
    "setup": ["sudo /opt/homelab-sandbox/guest-start.sh \"$(id -u)\""]
  }
}
```

| Key | Meaning |
|---|---|
| `user` | The user commands run as for this image, instead of `smolvm.user`. |
| `lib_dir` | `SMOLVM_LIB_DIR` for every smolvm call on the job's machine: the guest kernel it boots. |
| `mounts` | Host folders shared with the machine, at `guest`, read-only with `read_only`. With `per_job`, the job gets `host/<job>`, created when it starts and kept after it ends. A guest path may not overlap `/workspace` or `/run/worker`. |
| `env` | Plain values, no secret, in the same environment, such as `NODE_EXTRA_CA_CERTS`. |
| `env_files` | Each variable's value is the content of its file, less the final newline, read when the job starts. It is in the environment of every command the daemon runs in the machine (setup, the agent, the usage command) and never logged. |
| `setup` | Shell scripts run in order in `/workspace`, with the agent's environment, after the workspace is seeded and before the agent starts. One that fails fails the attempt, with its standard error in the reason. |

A job whose `requires.capabilities` holds `nested` gets nested
virtualization (`smolvm machine create --nested`); the control plane
offers it only to a worker that declares `nested`.

### The control-plane connection

The daemon reaches the control plane only through connections it opens:
check-in, poll, heartbeat and report on `control_plane.url`, and each
job's flows on the URLs its offer names.

- **The control plane** (design D18) issues each worker host a client
  certificate (`control-plane/cp.sh worker issue <worker>`; see the
  [control plane operator guide](../../control-plane/docs/operator-guide.md)).
  Set `control_plane.url` to Pomerium's `https` URL, such as
  `https://cp.example.org:8443`, and `control_plane.tls.cert_file`,
  `key_file` and `ca_file` to the three files it issued. The daemon
  presents the certificate to the control plane and to every flow.
  Pomerium admits only certificates from the control plane's CA that its
  worker list names, and sets the identity header from the certificate,
  over the one the daemon sends; `worker_id` is the certificate's name.
- **Without a certificate**, `control_plane.url` is a plain `http` URL
  and the daemon names itself in `identity_header`. The stage-1 edge
  that served such hosts through SSH tunnels is gone; this serves only a
  control plane on the same trusted host, such as a test.

The daemon never uses `HTTP_PROXY` or `HTTPS_PROXY` from its own
environment.

## What a job does

The daemon follows protocol 1: it checks in at start and every
`heartbeat_ms` (a third of the lease), polls while it has room, renews each
lease every `heartbeat_ms`, and reports each attempt once. Every request
carries `X-Sortie-Worker-Protocol: 1`. For an offer whose `run.backend` is
`smolvm`:

1. It serves the job's two forwarders on Unix sockets in the job's
   directory: **egress**, an HTTP proxy, and **mcp**, the tool server.
2. It fetches the workspace seed from `{bundles}{run.seed_path}`, which
   is `/jobs/{job}/attempts/{attempt}/seed`.
3. It creates the machine `wd-<job>-<attempt>` from the image's pack, with
   the job's CPUs, memory and disk, the job's `share` directory at
   `/run/worker`, the two sockets mounted with `--mount-socket`, and the
   image's host inputs, if this host configures any. It
   gives no network flag, allowlist or credential binding, so smolvm gives
   the machine no network: `socat` in the guest serves the sockets on guest
   loopback, `127.0.0.1:47801` (egress) and `127.0.0.1:47802` (mcp), and
   that is the guest's only way out.
4. It seeds `/workspace` from the bundle and checks out the issue branch:
   the branch at the bundle's `HEAD`, or, when several are there, the one
   named after the job (`sortie/ONS-7` for `ONS-7`). It adds `/.sortie` to
   the repository's `.git/info/exclude`, so the agent's control directory
   is never committed. Then it runs the image's `setup` scripts.
5. It runs `run.command` with `run.args` in `/workspace`, with `run.env`
   plus `HTTPS_PROXY`/`HTTP_PROXY` (and lower-case forms) at the egress
   forwarder, `NO_PROXY=127.0.0.1,localhost`, and `WORKER_MCP_URL` at the
   mcp forwarder. The agent's standard input, output and error travel on
   the `stdio` flow; its standard error also goes to the daemon's log when
   it fails. When the control plane closes the agent's input (Sortie does
   when it stops the session), the agent's standard input closes.
6. When the agent exits, it sends the exit status, the stream's last
   frame. Then it runs the usage command and reports on `usage`, returns
   the result on `bundles` (the issue branch's bundle and the agent's
   `.sortie/pr.json`, `.sortie/status` and `.sortie/criteria.json`, each
   when the agent wrote it), deletes the machine, and reports the outcome:
   `succeeded` when the agent exited 0 and the result was returned. The
   result always goes before the report. Usage goes before the result,
   because once Sortie has the result it finishes the attempt and closes
   its flows.

A restart of Sortie ends no job. When the `stdio` stream breaks before
the control plane closed the agent's input, as it does when Sortie
stops, the daemon keeps the machine, stops the agent, and dials the
stream again until Sortie serves it; it then starts the agent again in
the same `/workspace`, and the restarted Sortie resumes the agent's
session there (Sortie's `docs/workflow-reference.md`, "A restart keeps
the remote runs"). A failed heartbeat that is not a `409` or `410`,
such as a `503` while the restarted Sortie still adopts its runs, is
retried at the next beat. The stream is dialled again, and usage and
the result sent again, while Sortie answers `404`, `502`, `503` or `504`,
or cannot be reached, for at most one lease (`lease_ms`) each, and the
report while Sortie answers a `5xx` or cannot be reached, until the lease
would have run out. A stream refused `401`, or not served again within
the lease, ends the attempt as failed, with no usage or result. A job is
never held forever: when no heartbeat has been accepted for longer than
the lease, Sortie can no longer hold the job, and the daemon stops the
machine, deletes it and reports nothing.

A job the control plane no longer wants is released at once. When a
heartbeat, a report or any flow request, the `stdio` dial included,
answers `410` (`job_gone`: Sortie cancelled or finished the attempt, or
no longer knows it after its restart), the daemon logs `the control
plane no longer wants the job` with Sortie's reason, stops the agent and
the machine, deletes the machine, frees what the job reserved at its
next check-in, and sends no result, usage or report for that attempt.

When a heartbeat answers `409`, the daemon stops the machine at once,
within the heartbeat interval, and sends no result, usage or report for
that attempt. When the daemon is stopped (SIGTERM or SIGINT), it stops
every machine and reports each running attempt `failed`.

### Flows on the wire

The daemon speaks `agent-platform/worker-api#Data-plane flows on the
wire`, the contract Sortie's data plane serves. Each flow request carries
`X-Sortie-Worker-Protocol: 1` and its flow's job token, as
`Authorization: Bearer`, or `Proxy-Authorization: Bearer` on the `egress`
proxy. Paths are under the flow's URL:

| Flow | Request |
|---|---|
| `bundles` | `GET /jobs/{job}/attempts/{attempt}/seed` answers the seed as `application/x-git-bundle` (the daemon uses `run.seed_path`, which is this path). `POST /jobs/{job}/attempts/{attempt}/result` takes `multipart/form-data` with the part `bundle` (the issue branch) and the optional parts `pr` (`.sortie/pr.json`, at most 64 KiB), `status` (`.sortie/status`, at most 4 KiB) and `criteria` (`.sortie/criteria.json`, a criteria review's verdict per criterion, at most 64 KiB); `bundle` is left out when the machine has no issue branch. |
| `stdio` | `GET /jobs/{job}/attempts/{attempt}` with `Connection: Upgrade` and `Upgrade: sortie-stdio/1` opens a framed stream (answer `101`). A `ws` or `wss` flow URL is dialled as `http` or `https`. |
| `usage` | `POST /jobs/{job}/attempts/{attempt}` with `{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "model", "measured"}`, the attempt's totals so far; `"measured": false` says that spend occurred and its amount is unknown. |
| `egress` | An HTTP proxy; the daemon carries the guest's `CONNECT` tunnels and absolute-form requests to it. |
| `mcp` | `/jobs/{job}/attempts/{attempt}/<path>` reaches the tool server of that attempt's own session, streamed both ways: a guest request for `/<path>` goes there. |

A `stdio` frame is one type byte, a four-byte big-endian payload length
of at most 1 MiB, and the payload. The control plane sends `I` (the
agent's standard input) and `C` (input closed); the worker sends `O`
(standard output), `E` (standard error) and last `X` (the agent exited,
the payload its exit status as a four-byte big-endian signed integer). A
stream that ends before `X` is broken. The daemon splits a longer write
into frames of at most 1 MiB, and closes the stream after `X`.

The `bundle` part is a bundle of `refs/heads/<issue branch>` only, which
leaves out the seed's commits; a branch with nothing new is bundled as its
tip alone. The daemon returns the result before it reports the attempt,
and it returns one even when the agent failed.

### Token usage

After each run the daemon runs the agent kind's usage command in the
machine, in `/workspace`, with the agent's environment. The command prints
one JSON object, `{"input_tokens", "output_tokens"}` and optionally
`"cache_read_tokens"`, `"cache_write_tokens"` and `"model"`. The daemon
posts the attempt's totals:

```json
{"input_tokens": 12000, "output_tokens": 3000, "cache_read_tokens": 0,
 "cache_write_tokens": 0, "model": "claude-sonnet-4-5", "measured": true}
```

When the command fails, prints something else, or none is configured,
and the run made model requests (the egress forwarder counts what it
carried), the daemon posts `"measured": false`, with zero counts: spend
occurred, amount unknown, never a zero spend (design D14.1). Sortie
records it as an unmeasured turn. A run that made no model request posts
zero, `"measured": true`.

### What the workbench sees of the worker

The workbench shows each worker host, what runs on it and what waits on
it (`agent-platform/run-supervision`). The daemon tells the control plane
three things for it, in members a control plane that does not know them
ignores, as Sortie ignores every unknown member:

| Where | Member | What |
|---|---|---|
| check-in | `used` | `{"cpu", "mem_gib", "disk_gib"}`: what the running jobs were promised. `free` plus `used` is what the worker can give jobs. |
| check-in | `unmanaged` | `[{"name", "since", "state"}]`: each machine on the host that no Sortie started, that is with neither the daemon's `worker-daemon.job` label nor the `sortie.managed` label of a Sortie's smolvm environment, such as a sandbox made by hand. `since` is when smolvm says it was created, else when the daemon first saw it; `state` is smolvm's (`running`, `stopped`), or absent. The daemon lists the host's machines (`smolvm machine ls --json`) at most every 30 seconds, at a check-in, and keeps the last list when one fails. |
| heartbeat | `step`, `phase`, `step_at` | The step the job reports, the phase it names if any, and when the job wrote it. The body is `{}` until the job reports a step. |

A job reports its step by writing it to the file `SORTIE_STEP_FILE`
names, `/run/worker/step` in the machine: a line of text, the step, such
as `seed-vm-recreate`, or a JSON object `{"step": "seed-vm-recreate",
"phase": "live_proof"}`. The last non-empty line counts; it is kept to
200 printable characters. A vm chain appends each step as it starts it:

```sh
[ -n "${SORTIE_STEP_FILE:-}" ] && echo "seed-vm-recreate" >> "$SORTIE_STEP_FILE"
```

The daemon reads the file on the host, in the job's `share` directory, at
each heartbeat, and never runs anything in the machine for it.

## Run as a service

systemd, as a user that may use `/dev/kvm` and smolvm:

```ini
[Unit]
Description=worker-daemon
After=network-online.target

[Service]
ExecStart=/usr/local/bin/worker-daemon run -config /etc/worker-daemon/config.json
Restart=on-failure
TimeoutStopSec=150

[Install]
WantedBy=default.target
```

On macOS, a launchd agent with `ProgramArguments` `worker-daemon run
-config ~/Library/Application Support/worker-daemon/config.json` and
`KeepAlive` does the same.

## Test

`make -C worker-daemon test` runs every scenario of
`agent-platform/worker-daemon` against the fake control plane in
`internal/fakecp`, which serves protocol 1 and the five flows on the
wire as Sortie's data plane does, with machines from
`internal/machine/fakemachine`. The smolvm end-to-end test,
`TestSmolvmEndToEnd`, runs where `/dev/kvm` exists, smolvm is on `PATH`
and `WORKER_DAEMON_E2E_PACK` names a pack with `sh`, `git` and `socat`;
elsewhere it is skipped and says which of the three is missing:

```sh
WORKER_DAEMON_E2E_PACK=/path/base.smolmachine make -C worker-daemon test RUN=TestSmolvmEndToEnd
```

Three end-to-end tests run the daemon against the real Sortie data plane:
`TestOneAttemptOverTheWire`, `TestUnknownAmountOverTheWire` and
`TestACancelledJobIsReleasedOverTheWire`. They build Sortie from the
`sortie/` submodule (plan-bench) and the control plane's egress gate from
`control-plane/cmd/cp-egress`, and run Sortie under `environment.kind:
remote` with a file tracker and a local forge (a bare repository and a
fake Gitea API). The daemon dials a stand-in for Pomerium with a client
certificate, and its egress route goes through the egress gate to a fake
nono. One attempt of `ONS-7` runs on fake machines, with an agent
that speaks the Agent Client Protocol. It ends in a pull request on the
local forge, and Sortie records the usage the daemon posted, measured or
`"measured": false`. Without the submodule they are skipped; set
`WORKER_DAEMON_SORTIE_SRC` to use another Sortie checkout, and
`WORKER_DAEMON_E2E_NO_SKIP=1` to fail instead:

```sh
git submodule update --init sortie
make -C worker-daemon test RUN=OverTheWire
```
