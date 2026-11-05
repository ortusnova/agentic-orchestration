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
| `state_dir` | yes | | Absolute. Holds one directory per running job (`jobs/<job>-<attempt>`, mode 0700): the job's record (`job.json`, without its tokens), the workspace seed, the exported bundle and the forwarders' sockets. Removed when the job ends; kept across a restart of the daemon, which adopts the job. Keep the path short: a Unix socket path is limited to 104 bytes on macOS. |
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

1. It serves the job's forwarders on Unix sockets in the job's
   directory: **egress**, an HTTP proxy, **mcp**, the tool server, and,
   when the offer names a `telemetry` flow, **telemetry**, the OTLP
   receiver (see [Telemetry](#telemetry)).
2. It fetches the workspace seed from `{bundles}{run.seed_path}`, which
   is `/jobs/{job}/attempts/{attempt}/seed`.
3. It creates the machine `wd-<job>-<attempt>` from the image's pack, with
   the job's CPUs, memory and disk, the job's `share` directory at
   `/run/worker`, the sockets mounted with `--mount-socket`, and the
   image's host inputs, if this host configures any. It gives no
   credential binding. `socat` in the guest serves the sockets on guest
   loopback, `127.0.0.1:47801` (egress), `127.0.0.1:47802` (mcp) and,
   with a `telemetry` flow, `127.0.0.1:4318` (telemetry). When
   the offer has no `run.egress`, the machine gets no network flag or
   allowlist, so smolvm gives it no network and the forwards are its only
   way out. When it has one, the machine also gets the network of
   [The machine's network](#the-machines-network). A `run.egress` that
   does not parse gets no machine: the attempt is reported `failed`, once,
   with the error, which names the entry.
4. It seeds `/workspace` from the bundle and checks out the issue branch:
   the branch at the bundle's `HEAD`, or, when several are there, the one
   named after the job (`sortie/ONS-7` for `ONS-7`). It adds `/.sortie` to
   the repository's `.git/info/exclude`, so the agent's control directory
   is never committed. Then it runs the image's `setup` scripts.
5. It runs `run.command` with `run.args` in `/workspace`, with `run.env`
   plus `HTTPS_PROXY`/`HTTP_PROXY` (and lower-case forms) at the egress
   forwarder, `NO_PROXY=127.0.0.1,localhost` (and `no_proxy`), followed by
   the hosts the machine's network allows directly, `WORKER_MCP_URL` at the
   mcp forwarder, and, with a `telemetry` flow, the OTLP variables of
   [Telemetry](#telemetry). The agent's standard input, output and error
   travel on the `stdio` flow; its standard error also goes to the
   daemon's log when it fails. When the control plane closes the agent's input (Sortie does
   when it stops the session), the agent's standard input closes.
6. When the agent exits, it sends the exit status, the stream's last
   frame. Then it runs the usage command and reports on `usage`, ships
   the telemetry spool, for at most 30 seconds, returns the result on
   `bundles` (the issue branch's bundle and the agent's
   `.sortie/pr.json`, `.sortie/status` and `.sortie/criteria.json`, each
   when the agent wrote it), keeps the machine's egress refusals when it
   has a network, deletes the machine, and reports the outcome, with the
   attempt's telemetry counts when it has a `telemetry` flow: `succeeded`
   when the agent exited 0 and the result was returned. The result always
   goes before the report. Usage and telemetry go before the result,
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
retried at the next beat. The lease decides how long the daemon waits
for the stream: it dials the stream again while Sortie answers `404`,
`409`, `502`, `503` or `504`, or cannot be reached, for as long as its
heartbeats renew the job's lease, however long Sortie takes to serve the
stream again. Usage and the result are sent again on the same answers
for at most one lease (`lease_ms`) each, and the report while Sortie
answers a `5xx` or cannot be reached, until the lease would have run
out. A stream refused `401` ends the attempt as failed, with no usage or
result. A job is never held forever: a `410` releases it (below), a
`409` heartbeat ends it, and when no heartbeat has been accepted for
longer than the lease, so the lease can no longer be renewed, the daemon
stops the machine, deletes it and reports the attempt `failed`, once.

A job the control plane no longer wants is released at once. When a
heartbeat, a report or any flow request, the `stdio` dial included,
answers `410` (`job_gone`: Sortie cancelled or finished the attempt, or
no longer knows it after its restart), the daemon logs `the control
plane no longer wants the job` with Sortie's reason, stops the agent and
the machine, deletes the machine, frees what the job reserved at its
next check-in, and sends no result, usage or report for that attempt.

When a heartbeat answers `409`, the daemon stops the machine at once,
within the heartbeat interval, and sends no result, usage or report for
that attempt.

A restart of the daemon ends no job either. When the daemon is stopped
(SIGTERM or SIGINT), it stops each job's agent and leaves the job to its
next start: the machine keeps running, nothing is reported, and the
job's directory under `state_dir` keeps its record (`job.json`: the
offer without its tokens, and the seed once the agent may run). The
stdio stream breaks, and Sortie keeps the run and waits for it. At its
next start, before its first check-in, the daemon adopts each job it
left, and logs `jobs left by the last start` with how many it
`adopted` and how many it did not (`not_adopted`):

- It takes the job's offer again from Sortie with
  `POST /api/v1/workers/jobs/{job}/attempts/{attempt}/resume`, which
  hands it new flow tokens, since it keeps none at rest. Then it renews
  the lease, dials the `stdio` stream again and starts the agent again in
  the same machine and workspace, and the job goes on to its result and
  its report, once.
- A job Sortie no longer wants (`410`) or holds for another worker
  (`409`) is released: its machine is deleted and nothing is reported. A
  Sortie that cannot resume (`404`), a job whose machine is gone, as
  after a host reboot, and a job whose agent had not started yet are
  reported `failed`, their machines deleted.
- A machine labelled `worker-daemon.worker=<worker_id>` and
  `worker-daemon.job` that no job directory names, as when `state_dir`
  was removed, is deleted. A machine of another worker or of no daemon
  is left alone.

The machines must outlive the daemon's process: under systemd, run it
with `KillMode=process` (see [Run as a service](#run-as-a-service)), or
stopping the unit kills the machines with it.

### The machine's network

An offer's `run.egress` (`agent-platform/job-egress-policy`) holds the
harness's policy and the repository's, each in the format of
`.sortie/egress.yaml` without its `version` (`hosts`, `udp`, `private`,
each list empty, never `null`, when it has no entries), where Sortie read
the repository's, and the `credential_hosts` that stay on the `egress`
flow:

```json
{"source": {"repository": "homelab", "branch": "main", "commit": "4b825dc…", "file": ".sortie/egress.yaml"},
 "harness": {"hosts": [{"host": "models.opencode.ai", "reason": "…"}], "udp": [], "private": []},
 "repository": {
   "hosts": [{"host": "*.githubusercontent.com", "reason": "…"}],
   "udp": [{"cidr": "0.0.0.0/0", "port": "123", "reason": "NTP"}],
   "private": [{"cidr": "192.168.122.0/24", "reason": "…"}]},
 "credential_hosts": ["api.anthropic.com"]}
```

The daemon checks it strictly, by the policy's schema: an unknown member,
a `version` other than `1` when one is sent, a host that is not a
lower-case DNS name or `*.` and a name of two labels or more (never `*`,
`*.com`, a port or an address), an empty `reason`, a CIDR with host bits
set, a port outside 1-65535 (a string, `N` or `N-M`, or a number), or a
`private` entry outside the private ranges (`10.0.0.0/8`,
`172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`), or overlapping loopback
or link-local, refuses the job, as above, naming the entry. A valid one
maps to smolvm's flags (designs D8, D15 and D16):

| `run.egress` | smolvm flag |
|---|---|
| present | `--net-backend virtio-net --block-private` |
| present: design D15's ranges, less the `private` entries | `--deny-cidr <cidr>` for each of `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`, `169.254.0.0/16`, `127.0.0.0/8`, `fc00::/7`, `fe80::/10`, `::1/128`; a range that holds a `private` entry is split into the largest CIDRs that hold none of its addresses |
| each host of `harness.hosts` and `repository.hosts`, once | `--allow-host-pattern <host>` (an exact name or `*.domain`) |
| each `udp` entry | `--allow-udp <cidr>:<port>` (`<port>` is `N` or `N-M`) |
| each `private` entry | `--allow-cidr <cidr>`, every port of the range, and the range left out of the deny set; never `--allow-private` (design D16) |
| a host that is, or is covered by or covers, a host of `credential_hosts` or of `run.credentials` | nothing: it stays on the `egress` flow |
| absent | no network flag: no network |

smolvm lets a machine reach every address it resolved for an allowed
name, private or not, so without the deny set a declared name whose
answer is a LAN address would reach the LAN (DNS rebinding); smolvm
checks `--deny-cidr` before anything is allowed. A `private` entry of
`192.168.1.0/30`, for example, gives `--allow-cidr 192.168.1.0/30` and,
for `192.168.0.0/16`, `--deny-cidr 192.168.0.0/24`, `192.168.1.4/30`,
`192.168.1.8/29`, … `192.168.64.0/18`, `192.168.128.0/17`. Loopback and
link-local are always denied whole. The job's log names the deny set
(`egress: private ranges denied cidrs=…`). A `private` entry opens every
port of every address of its range, whether or not a name
resolves to it, so declare the smallest range the job needs.

A `private` entry is `--allow-cidr`, never `--allow-private` (design
D16): `--allow-private` reopens no address beside the patterns, as
measured below, so an entry given by it would open nothing. The daemon
never uses `--allow-host`, which fixes a host's addresses when the
machine starts, `--allow-private`, a bare `--net`, or any flag that
widens egress. The
socket forwards stay as they are. A policy with no host allowed
directly, every host a credential host, gives no network, even with
`udp` or `private` entries: smolvm filters by name only beside a host
pattern, and `--block-private` without one leaves the internet open.

`NO_PROXY` and `no_proxy` hold `127.0.0.1,localhost` and then each host
allowed directly, a pattern `*.d` written `.d`, so that HTTP clients go
direct to those and send every other host, credential hosts first among
them, to the `egress` forward, where nono puts the credential in place of
its placeholder. A VM nested in the machine, as homelab's libvirt seed,
leaves through the machine's own interface, under the same flags, with no
list of its own.

The job's log names each allowed host with its source (`egress: host
allowed host=… source=harness`, `repository`, or both), each UDP and
private entry, and each host withheld as a credential host.

Before it deletes a machine with a network, after a job or when a
restarted daemon cannot adopt it, the daemon reads its refusals with
`smolvm machine egress-events --name <machine> --json` and keeps them in
`state_dir/refusals/<job>/<attempt>.json`: `{"job", "attempt",
"machine", "read_at", "events"}`, `events` as smolvm prints them. The
file stays after the machine and its job directory are gone; remove it
when you no longer need it. A failure to read them is logged and does not
keep the machine.

#### smolvm's filters, as measured

Measured on smolvm 1.32.4, the version the kit pins, on taichi, on
2026-11-04, with `--net-backend virtio-net` and machines from homelab's
pack. A host's answer was changed with a test resolver: smolvm uses the
host's resolver, so the machines ran in a network namespace of their own
(`pasta --config-net`, with `/etc/resolv.conf` bound to a resolver on
`127.0.0.1` that answers the test names from a file, TTL 1 s, and forwards
the rest), and taichi's own DNS was not changed. Each probe is a TCP
connection from the machine, by name or by address.

| Question | Result |
|---|---|
| Does `--allow-host` fix a host's addresses at machine start? | No. smolvm asks the resolver for it when the machine is created and started, but when the answer changed (`9.9.9.9` to `149.112.112.112`) the machine resolved the new address and reached it within 5 s, as with a pattern. The daemon still never passes it (design D8). |
| Does `--allow-host-pattern` with an exact name follow an address change in a running machine? | Yes. When the answer changed (`1.1.1.1` to `8.8.8.8`), the machine resolved the new address and reached it within 5 s, and again 65 s later; `8.8.8.8`, refused by address before, was reached by address after. An address the machine resolved for an allowed name stays allowed for the machine's life: the old one was still reached after the change, and after the answer changed back. |
| What does `--block-private` do beside the patterns? | Nothing that the patterns do not already do. With one or more patterns, every address the machine has not resolved for an allowed name is refused, private or public, with or without `--block-private` (the LAN, host loopback at `100.96.0.1`, `169.254.169.254`, `10.0.0.1`, `1.1.1.1`). A pattern whose answer is a private address (`192.168.1.202`) is reached even with `--block-private`. `--allow-private 192.168.1.0/24` did not reopen an address of that range that no allowed name resolved to, so the daemon gives a policy's `private` entries as `--allow-cidr` instead (design D16). Without any pattern, `--block-private` (with `--allow-udp` only) left the public internet and the LAN open and refused only `169.254.169.254`, so the daemon gives no network to a policy that allows no host directly. |
| Is `--deny-cidr` checked before an allow (design D15)? | Yes. A machine `--from` the stage-0 pack with `--deny-cidr` for each range of design D15, `--allow-host-pattern example.com`, `--allow-host-pattern p1.test` (answered with p1's LAN address, `192.168.1.165`) and `--allow-cidr 192.168.1.165/32` was refused `p1.test` and `192.168.1.165` on port 22, where p1 listens. With every range denied, the guest's resolver still answered `example.com`, and the machine reached it on port 443. |
| Does a job machine made by the daemon refuse a declared name that answers a private address (design D15)? | Yes. `TestSmolvmEgressEndToEnd` under `private-name-netns.sh p1.test 192.168.1.165`, with `WORKER_DAEMON_E2E_PRIVATE_PORT=22` and the stage-0 pack, passed: the daemon created the machine `--from` the pack with `--deny-cidr` for each range of design D15, the machine reached `ghcr.io` on port 443, was refused `pastebin.com`, and was refused `p1.test` and `192.168.1.165` on port 22, and `state_dir/refusals` held a `connect` record of `192.168.1.165:22` for each. As a control, the same run with the `--deny-cidr` flags removed from the daemon's `machine create` (by a wrapper around smolvm) failed: the machine reached `p1.test` and `192.168.1.165`, with `--block-private` still given. So `machine create --from` keeps `--deny-cidr`, and it is the deny set that refuses the answer. |
| Does a job machine made by the daemon reach a declared `private` range by address, and only that range (design D16)? | Yes. `TestSmolvmEgressEndToEnd` with `WORKER_DAEMON_E2E_PRIVATE_CIDR=192.168.1.164/30`, `WORKER_DAEMON_E2E_PRIVATE_ADDR=192.168.1.165` (p1, which no name resolves to), `WORKER_DAEMON_E2E_PRIVATE_OUTSIDE_ADDR=192.168.1.1` (the router), `WORKER_DAEMON_E2E_PRIVATE_PORT=22` and the stage-0 pack, passed on taichi (2026-11-04): the daemon created the machine with `--allow-cidr 192.168.1.164/30`, no `--allow-private`, and the deny set split around the range (`192.168.1.160/30`, `192.168.1.168/29`, … beside `192.168.0.0/24`, `192.168.1.0/25`, …); the machine reached `ghcr.io` on port 443, was refused `pastebin.com`, reached `192.168.1.165` on port 22, and was refused `192.168.1.1` on port 22, and `state_dir/refusals` held a `connect` record of `192.168.1.1:22` and none of `192.168.1.165`. As a control, the same run with `--allow-cidr` removed from the daemon's `machine create` (by a wrapper around smolvm) failed: `192.168.1.165` was refused. So it is `--allow-cidr` that reopens the range; leaving it out of the deny set alone does not. |

`--allow-udp` was not measured: smolvm answers every UDP query to port 53
itself, so a DNS probe does not show it.

### Flows on the wire

The daemon speaks `agent-platform/worker-api#Data-plane flows on the
wire`, the contract Sortie's data plane serves. Each flow request carries
`X-Sortie-Worker-Protocol: 1` and its flow's job token, as
`Authorization: Bearer`, or `Proxy-Authorization: Bearer` on the `egress`
proxy. Paths are under the flow's URL:

| Flow | Request |
|---|---|
| `bundles` | `GET /jobs/{job}/attempts/{attempt}/seed` answers the seed as `application/x-git-bundle` (the daemon uses `run.seed_path`, which is this path). `POST /jobs/{job}/attempts/{attempt}/result` takes `multipart/form-data` with the part `bundle` (the issue branch) and the optional parts `pr` (`.sortie/pr.json`, at most 64 KiB), `status` (`.sortie/status`, at most 4 KiB) and `criteria` (`.sortie/criteria.json`, a criteria review's verdict per criterion, at most 64 KiB); `bundle` is left out when the machine has no issue branch. |
| `stdio` | `GET /jobs/{job}/attempts/{attempt}` with `Connection: Upgrade` and `Upgrade: sortie-stdio/2` opens a framed stream (answer `101`); a control plane that answers `426` is dialled again with `sortie-stdio/1`. On `sortie-stdio/2` the daemon answers each `S` frame, which Sortie sends after a turn, with an `F` frame: the agent's `.sortie/pr.json`, `.sortie/status` and `.sortie/criteria.json` as they are, so Sortie ends the run after the turn that wrote a status or a criteria verdict. A `ws` or `wss` flow URL is dialled as `http` or `https`. |
| `usage` | `POST /jobs/{job}/attempts/{attempt}` with `{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "model", "measured"}`, the attempt's totals so far; `"measured": false` says that spend occurred and its amount is unknown. |
| `egress` | An HTTP proxy that takes `CONNECT`; the daemon carries the guest's `CONNECT` tunnels to it, and each absolute-form request, such as a browser's for an `http://` address, inside a `CONNECT` tunnel to its host (port 80 unless it names one). Each `CONNECT` carries the job's egress token and `X-Sortie-Job`, the job's identifier, which the control plane logs beside a refusal. |
| `mcp` | `/jobs/{job}/attempts/{attempt}/<path>` reaches the tool server of that attempt's own session, streamed both ways: a guest request for `/<path>` goes there. |
| `telemetry` | Optional: only an offer that names it gets the OTLP receiver. `POST /jobs/{job}/attempts/{attempt}/v1/{signal}`, `{signal}` one of `traces`, `logs` and `metrics`, takes one OTLP/HTTP export request as the machine sent it, `application/x-protobuf` or `application/json` (uncompressed), with its resources stamped and its strings masked (see [Telemetry](#telemetry)). Any `2xx` takes it. `400`, `413`, `415` and `422` refuse it for good: it is dropped and counted. `410` releases the job, as on every flow. Any other answer, or none, is tried again, the oldest request first. |

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

### Telemetry

When an offer names a `telemetry` flow (`agent-platform/session-telemetry`,
design D3), the job's machine gets an OTLP/HTTP receiver that takes no
credential, and the daemon ships what it receives to Sortie on that flow.
Without the flow nothing listens: the machine gets no telemetry socket
and the agent no OTLP variable. The offer's `run.telemetry` says how:

```json
"telemetry": {"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
              "spool_bytes": 67108864, "dispatch_id": "d-42"}
```

| Member | Default | Meaning |
|---|---|---|
| `traceparent` | none | The W3C trace context of the attempt's span, given to the agent as `TRACEPARENT`, so a source that takes it nests its spans under the attempt. |
| `spool_bytes` | `67108864` (64 MiB) | The bound of the attempt's spool on the host. |
| `dispatch_id` | `run.env`'s `SORTIE_DISPATCH_ID` | The attempt's `sortie.dispatch.id`. |

**The receiver.** It is a forward like egress and mcp: `socat` serves
`127.0.0.1:4318`, OTLP/HTTP's own port, on the guest's loopback, through
the socket `otlp.sock` in the job's directory (mode 0600), which only that
attempt's machine is given. It takes `POST /v1/traces`, `/v1/logs` and
`/v1/metrics`, in `application/x-protobuf` or `application/json`, plain
or `Content-Encoding: gzip`, and ignores any `Authorization` the guest
sends. It answers `200` at once (an empty message, or `{}` in JSON),
whatever the flow does, so a slow or refusing flow never holds up the
agent; a body that does not parse is answered `400`, another media type
`415`, and a request larger than the spool `413`, each dropped and
counted.

**The agent's environment** gets, beside the variables above:

| Variable | Value |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://127.0.0.1:4318` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` |
| `OTEL_RESOURCE_ATTRIBUTES` | `run.env`'s value, if any, then `sortie.issue.identifier=<job.issue.identifier>,sortie.dispatch.id=<dispatch>,sortie.attempt=<attempt>`, each value percent-encoded |
| `TRACEPARENT` | `run.telemetry.traceparent`, when the offer has one |

These replace the same names in `run.env`. Setup scripts and the usage
command get them too.

**What leaves.** For each request the daemon:

1. sets `sortie.issue.identifier`, `sortie.dispatch.id` and
   `sortie.attempt` on every resource of the request, as string
   attributes, over any value the guest gave them, and adds a resource
   where the request has none, so every span, log record and metric of
   the attempt carries the attempt it came from;
2. masks every flow token the daemon holds for the job (the tokens a
   resumed job is given, after a restart of the daemon) in every string of the request, by the broker's
   rules (`acp-broker/telemetry-masking`): each occurrence becomes
   `[redacted]`, a value shorter than 8 bytes is not masked, the token of
   `Bearer <token>` is masked on its own, and overlapping occurrences
   become one marker. A length-delimited protobuf field the daemon does
   not know as a message is masked as text, so no field carries a token
   out;
3. spools it, rewritten, in the daemon's memory on the host, within
   `spool_bytes`; a request that does not fit drops the oldest first.

A shipper sends the spool on the flow, the oldest request first, one at
a time, each with the job's telemetry token and
`X-Sortie-Worker-Protocol: 1`, each try bounded by 10 seconds. While the
flow refuses, as when the control plane is down for a minute, the spool
stays within its bound and the shipper tries again, waiting up to 2
seconds between tries; it ships the spool once the flow answers.

**The bound.** After the agent exits and usage is reported, the daemon
waits for the spool to ship, for at most 30 seconds, then returns the
result whatever is left: a refusing flow never holds the agent, and
never holds the result longer than that. When the attempt ends, what is
still spooled is dropped. The report counts it all:

```json
{"outcome": "succeeded", "reason": "…", "machine": "deleted",
 "telemetry": {"received": 120, "shipped": 117, "dropped": 3, "dropped_bytes": 81234}}
```

`dropped` counts what the full spool dropped, what the flow refused for
good, what the receiver refused, and what was still spooled at the end;
a report without a `telemetry` flow has no `telemetry` member. The spool
is kept in memory only: a daemon that stops, leaving the job for its next
start, drops it unshipped, and the adopted job starts a new one.

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
# The jobs' machines run in the unit's control group: a stop signals the
# daemon only, which leaves the machines running for its next start.
KillMode=process

[Install]
WantedBy=default.target
```

On macOS, a launchd agent with `ProgramArguments` `worker-daemon run
-config ~/Library/Application Support/worker-daemon/config.json` and
`KeepAlive` does the same.

## Test

`make -C worker-daemon test` runs every scenario of
`agent-platform/worker-daemon`, and those of
`agent-platform/session-telemetry` the daemon serves, against the fake
control plane in `internal/fakecp`, which serves protocol 1, the five
flows and a job's telemetry flow on the wire as Sortie's data plane does,
with machines from `internal/machine/fakemachine`. The telemetry tests
(`make -C worker-daemon test RUN=Telemetry`) send OTLP to the receiver
as the guest's SDK does, and cover each signal, the stamping, a token in
a span's attribute, a refusing flow then a recovering one, a full spool,
the flush before the result and its bound, and a job with no telemetry
flow. The smolvm end-to-end test,
`TestSmolvmEndToEnd`, runs where `/dev/kvm` exists, smolvm is on `PATH`
and `WORKER_DAEMON_E2E_PACK` names a pack with `sh`, `git` and `socat`;
elsewhere it is skipped and says which of the three is missing:

```sh
WORKER_DAEMON_E2E_PACK=/path/base.smolmachine make -C worker-daemon test RUN=TestSmolvmEndToEnd
```

`TestSmolvmTelemetryEndToEnd`, with the same needs, runs one job whose
offer names a `telemetry` flow: the agent sends one OTLP/HTTP JSON export
of each signal to `127.0.0.1:4318` in the guest, and each must reach the
flow stamped with the attempt's `sortie.*` attributes before the result.

`TestSmolvmEgressEndToEnd` runs one job whose `run.egress` declares
`WORKER_DAEMON_E2E_ALLOWED` (default `ghcr.io`): the machine must reach
it on port 443 and be refused `WORKER_DAEMON_E2E_DENIED` (default
`pastebin.com`), and the refusal must be in `state_dir/refusals` after
the machine is deleted. It also needs nested KVM and the network, and is
skipped, saying why, without them. With `WORKER_DAEMON_E2E_NESTED_SCRIPT`,
a shell script run in the machine that boots a VM nested in it (libvirt,
as homelab's seed) and prints `nested allowed: reached` or `refused`, then
`nested denied: …`, for `$ALLOWED` and `$DENIED`, the job requires
`nested` and the nested VM must do the same; give homelab's pack as
`WORKER_DAEMON_E2E_PACK`:

```sh
WORKER_DAEMON_E2E_PACK=/path/homelab.smolmachine WORKER_DAEMON_E2E_ALLOWED=deb.debian.org \
  WORKER_DAEMON_E2E_NESTED_SCRIPT=$PWD/worker-daemon/internal/daemon/testdata/nested-egress.sh \
  make -C worker-daemon test RUN=TestSmolvmEgressEndToEnd
```

`internal/daemon/testdata/nested-egress.sh` is that script for homelab's
pack: it starts libvirt as the pack's `guest-start.sh` does, takes
Debian's netboot kernel and initrd from `deb.debian.org` (so
`WORKER_DAEMON_E2E_ALLOWED` must be that host), and boots them on
libvirt's default network with a probe of its own as the initrd's first
program. The nested VM resolves names through libvirt's dnsmasq, which
asks the machine's resolver (design D9). The end-to-end test runs the
daemon under a worker id and a job id of its own, so on a worker host it
neither deletes the live worker's machines as strays nor names a machine
as one of its jobs.

With `WORKER_DAEMON_E2E_PRIVATE_NAME`, a name whose answer is a private
address with a listener on `WORKER_DAEMON_E2E_PRIVATE_PORT` (default
`443`), the policy declares that name too, and the machine must be
refused it by name and by address, with both refusals in
`state_dir/refusals`, while `WORKER_DAEMON_E2E_ALLOWED` is still reached
(design D15). The test resolves the name itself and fails if its answer
is not a private address or nothing listens there. Give the name that
answer without changing the host's DNS:
`internal/daemon/testdata/private-name-netns.sh NAME ADDR` runs the test
in a network namespace of its own (`pasta --config-net --no-map-gw`)
whose `/etc/resolv.conf` names dnsmasq on `127.0.0.1`, which answers
`NAME` with `ADDR` and forwards the rest to `$UPSTREAM` (default
`9.9.9.9`), and whose `/etc/nsswitch.conf` is `hosts: files dns`, so that
systemd-resolved does not answer instead. The test runs in a nested user
namespace as the caller's uid, since smolvm will not start a machine as
pasta's root. It needs pasta, unshare and dnsmasq; the other variables
pass through, and the machine is deleted by name after the run. On
taichi, with p1's SSH port as the listener:

```sh
WORKER_DAEMON_E2E_PACK=/path/base.smolmachine WORKER_DAEMON_E2E_PRIVATE_PORT=22 \
  worker-daemon/internal/daemon/testdata/private-name-netns.sh p1.test 192.168.1.165
```

With `WORKER_DAEMON_E2E_PRIVATE_CIDR`, a private range, and
`WORKER_DAEMON_E2E_PRIVATE_ADDR`, an address in it with a listener on
`WORKER_DAEMON_E2E_PRIVATE_PORT` that no name resolves to, the policy
declares the range as a `private` entry, and the machine must reach the
address and be refused a private address outside the range, with that
refusal, and none of the address in the range, in `state_dir/refusals`
(design D16). The address outside is
`WORKER_DAEMON_E2E_PRIVATE_OUTSIDE_ADDR`, or else the answer of
`WORKER_DAEMON_E2E_PRIVATE_NAME`; it needs a listener on the same port.
The test fails if an address is not where it should be or nothing listens
there. On taichi, with two LAN hosts' SSH ports as the listeners:

```sh
WORKER_DAEMON_E2E_PACK=/path/base.smolmachine WORKER_DAEMON_E2E_PRIVATE_PORT=22 \
  WORKER_DAEMON_E2E_PRIVATE_CIDR=192.168.1.164/30 WORKER_DAEMON_E2E_PRIVATE_ADDR=192.168.1.165 \
  WORKER_DAEMON_E2E_PRIVATE_OUTSIDE_ADDR=<another LAN host> \
  make -C worker-daemon test RUN=TestSmolvmEgressEndToEnd
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
