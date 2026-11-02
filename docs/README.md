# Documentation

How the parts fit:

```
  people ──► workbench ──► Sortie ──► acp-broker ──► agent (ACP)
     │          │            │             ▲
     │          └─► tracker ◄┘             │
     └──────────── watch and steer ────────┘
```

The workbench writes the approved plan into the tracker through Sortie.
Sortie runs an agent session for each released story, with `acp-broker`
in front of the agent. People watch and steer those sessions from the
workbench, which reaches each broker over HTTP.

| Guide | What it covers |
|---|---|
| [Operator guide](../workbench/docs/operator-guide.md) | Run the whole system: Huly, Sortie, the broker, and the workbench, with every setting. |
| [ACP broker guide](../acp-broker/docs/guide.md) | The broker's flags, viewers, use with Sortie, and telemetry. |
| [ACP broker notes](../acp-broker/docs/notes.md) | The upstream ACP work the broker depends on, and what each agent does. |
| [smol-builder](../smol-builder/README.md) | Build the smolvm packs agents run in. |
| [Control plane operator guide](../control-plane/docs/operator-guide.md) | Run the control plane as one compose project under rootless podman: Sortie, acp-broker, the workbench, Pomerium, nono and Huly; issue and revoke worker certificates; where each secret file goes. |
| [Worker daemon operator guide](../worker-daemon/docs/operator-guide.md) | Run a worker host: the daemon's configuration (control-plane URL, reserve, floor, client certificate), what a job does, and its flows. |
| [Sortie](../sortie/docs/README.md) | The orchestrator: our fork's documentation. |
| [Specs](../specs/openspec/specs) | The requirements of every component, as OpenSpec specs. |
| [Development setup](../dev/.env.example) | `dev/dev.sh` and its settings. |
| [Stage 0 kit](../dev/stage0/README.md) | A persistent Sortie, workbench, broker and Huly on one host, with GitHub and smolvm. |
