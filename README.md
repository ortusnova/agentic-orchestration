# Agentic orchestration

A platform for running autonomous coding agents against tracker tickets,
and for the people who plan, watch, steer, and review that work.

## Components

| Directory | What it is | Language |
|---|---|---|
| `workbench/` | The plan workbench and `planlib`: plan a project as epics and stories, validate the plan, approve it, and sync it to the tracker. Watch and steer the agents that work on it. | Python, Svelte |
| `acp-broker/` | Sits between an orchestrator and an Agent Client Protocol (ACP) agent. One session, two inputs: the autonomous driver and people, who can watch, steer, take over, hand back, and answer permission requests. | Go |
| `smol-builder/` | Builds the smolvm machine images that agents run in. | Go |
| `worker-daemon/` | Runs on a worker host: dials the control plane's worker API, runs each job in a smolvm machine, and carries its flows. Linux and macOS. | Go |
| `specs/` | The OpenSpec store: the requirements of every component, and the record of each change. | Markdown |
| `docs/` | Documentation for the whole system. | Markdown |
| `dev/` | The development setup: runs every component locally against a local tracker and forge, and the stage-0 kit with its stage-1 edge. | Shell, Python, Go |
| `sortie/` | Git submodule: our fork of [Sortie](https://github.com/sortie-ai/sortie), the orchestrator. It is licensed separately, under Apache-2.0. | Go |

See [docs/](docs/README.md) for how the parts fit and every guide.

## Develop

```sh
git clone --recurse-submodules https://github.com/briancappello/agentic-orchestration.git
make test        # every component's tests
make lint        # linters, the license copies, and the specs
make check-latest   # every Go component's dependencies against their latest stable releases
dev/dev.sh       # the whole system locally; settings in dev/.env.example
make dist        # release archives of the Go tools and the workbench wheel
```

Needs Go, `uv`, `npm`, the `openspec` CLI, and Docker for the local Huly
and Gitea. Sortie is not in `go.work`: build it in `sortie/` with
`GOWORK=off`, from its own `go.mod`.

## License

Everything in this repository is licensed under the GNU Affero General
Public License, version 3 or later (AGPL-3.0-or-later). See `LICENSE`.

The `sortie/` submodule is a separate project, with its own license
(Apache-2.0).
