# worker-daemon

`worker-daemon` runs a worker host's jobs for the control plane. It dials
the control plane's worker API (protocol 1), runs each job it takes in a
smolvm machine, with no network of its own unless the offer's
`run.egress` allows hosts by name, and carries the job's flows:
the agent's standard input and output, its model traffic and tool calls,
its workspace in and its commits out as git bundles, its token usage, and,
when the offer names a telemetry flow, the OTLP its machine sends.
It holds no credential but its client certificate.

A machine with a network is denied the private ranges (design D15). A
policy's `private[]` entry is smolvm's `--allow-cidr <cidr>`, never
`--allow-private` (design D16), and is left out of that deny set; it
opens every port of every address of the range. `udp[]` and `private[]`
entries alone give no network.

One static binary per platform: `linux/amd64`, `linux/arm64`,
`darwin/arm64`. Standard library only, no CGo.

```sh
make build          # bin/worker-daemon
make cross          # one binary per platform
make test           # every scenario, against the fake control plane and Sortie's data plane
make lint
worker-daemon version
worker-daemon run -config /etc/worker-daemon/config.json
```

The [operator guide](docs/operator-guide.md) documents the configuration
(control-plane URL, reserve, floor, the client certificate), what a job
does, and the flows on the wire.

| Package | What it is |
|---|---|
| `cmd/worker-daemon` | The command. |
| `internal/daemon` | Check-in, polling within the reserve and floor, leases, the run of a job, the report. |
| `internal/cp` | The worker API client and its TLS. |
| `internal/flows` | The data-plane flows, the guest-facing egress and MCP forwarders, and the OTLP receiver and its spool. |
| `internal/otlp` | OTLP/HTTP export requests, protobuf and JSON: each resource stamped with the attempt's `sortie.*` attributes, every string masked. |
| `internal/machine` | Machines: the smolvm CLI backend, and `fakemachine` for tests. |
| `internal/fakecp` | A fake control plane serving protocol 1 and the flows. |
| `internal/protocol` | The wire types. |
| `internal/secret` | A token type that never prints, and the broker's masking rules for telemetry. |
| `internal/hostinfo` | Memory, disk, KVM and nested virtualization, per OS. |
| `internal/stdio` | The framed stream of the stdio flow (`sortie-stdio/2`, or `sortie-stdio/1`). |
| `internal/egress` | An offer's `run.egress`: its strict check, and the machine's smolvm network (designs D8, D15 and D16). |
