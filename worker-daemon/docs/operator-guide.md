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
| `run_slots` | no | `1` | How many jobs run at once. |
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

### The control-plane connection

The daemon reaches the control plane only through connections it opens:
check-in, poll, heartbeat and report on `control_plane.url`, and each
job's flows on the URLs its offer names.

- **Until the control plane issues client certificates** (design D18),
  the control plane opens a tunnel to the worker host, and
  `control_plane.url` is the tunnel's loopback end, such as
  `http://127.0.0.1:7690`. The tunnel's listener is the tunnel's, not the
  daemon's. The daemon names itself in `identity_header`.
- **Once it does,** set an `https` URL and `control_plane.tls.cert_file`,
  `key_file` and, for a private CA, `ca_file`. The daemon presents the
  certificate to the control plane and to every flow. The control plane's
  proxy authenticates the certificate and sets the identity header itself.

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
2. It fetches the workspace seed from `{bundles}{run.seed_path}`.
3. It creates the machine `wd-<job>-<attempt>` from the image's pack, with
   the job's CPUs, memory and disk, the job's `share` directory at
   `/run/worker`, and the two sockets mounted with `--mount-socket`. It
   gives no network flag, allowlist or credential binding, so smolvm gives
   the machine no network: `socat` in the guest serves the sockets on guest
   loopback, `127.0.0.1:47801` (egress) and `127.0.0.1:47802` (mcp), and
   that is the guest's only way out.
4. It seeds `/workspace` from the bundle and checks out the issue branch:
   the branch at the bundle's `HEAD`, or, when several are there, the one
   named after the job (`sortie/ONS-7` for `ONS-7`).
5. It runs `run.command` with `run.args` in `/workspace`, with `run.env`
   plus `HTTPS_PROXY`/`HTTP_PROXY` (and lower-case forms) at the egress
   forwarder, `NO_PROXY=127.0.0.1,localhost`, and `WORKER_MCP_URL` at the
   mcp forwarder. The agent's standard input and output travel on the
   `stdio` flow; its standard error goes to the daemon's log when it fails.
6. When the agent exits, it exports the issue branch's new commits as a
   bundle and uploads it on `bundles`, runs the usage command and reports
   on `usage`, deletes the machine, and reports the outcome: `succeeded`
   when the agent exited 0 and the export worked.

When a heartbeat answers `409`, the daemon stops the machine at once,
within the heartbeat interval, and sends no bundle, usage or report for
that attempt. When the daemon is stopped (SIGTERM or SIGINT), it stops
every machine and reports each running attempt `failed`.

### Flows on the wire

Each request carries the flow's job token as `Authorization: Bearer`
(egress: `Proxy-Authorization: Bearer`) and `X-Sortie-Worker-Protocol: 1`.
Paths are under the flow's URL:

| Flow | Request |
|---|---|
| `bundles` | `GET {run.seed_path}`, the seed; `PUT /jobs/{job}/attempts/{n}/result`, the result bundle (`application/x-git-bundle`). No new commits, no upload. |
| `stdio` | WebSocket at `/jobs/{job}/attempts/{n}` (`http`/`https` URLs are dialled as `ws`/`wss`). Each binary or text message is agent input; each agent write is one binary message. |
| `mcp` | A guest request for `/p` goes to `/jobs/{job}/attempts/{n}/p`, streamed both ways. |
| `egress` | The flow is an HTTP proxy: the guest's `CONNECT` tunnels and absolute-form requests are carried to it. |
| `usage` | `POST /jobs/{job}/attempts/{n}`, JSON below. |

### Token usage

After each run the daemon runs the agent kind's usage command in the
machine, in `/workspace`, with the agent's environment. The command prints
one JSON object, `{"input_tokens", "output_tokens"}` and optionally
`"cache_read_tokens"` and `"cache_write_tokens"`. The daemon posts:

```json
{"job": "ONS-7", "attempt": 1, "spend_occurred": true, "amount_known": true,
 "input_tokens": 12000, "output_tokens": 3000, "cache_read_tokens": null,
 "cache_write_tokens": null, "model_requests": 14}
```

`model_requests` counts what the egress forwarder carried. When the
command fails, prints something else, or none is configured, and the run
made model requests, the daemon reports `"spend_occurred": true,
"amount_known": false` with every token field `null`: spend occurred,
amount unknown, never zero (design D14.1). A run that made no model
request reports zero, known.

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
`internal/fakecp`, which serves protocol 1 and the five flows, with
machines from `internal/machine/fakemachine`. The smolvm end-to-end test,
`TestSmolvmEndToEnd`, runs where `/dev/kvm` exists, smolvm is on `PATH`
and `WORKER_DAEMON_E2E_PACK` names a pack with `sh`, `git` and `socat`;
elsewhere it is skipped and says which of the three is missing:

```sh
WORKER_DAEMON_E2E_PACK=/path/base.smolmachine make -C worker-daemon test RUN=TestSmolvmEndToEnd
```
