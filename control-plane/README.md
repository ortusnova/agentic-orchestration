# control-plane

The control plane as one compose project under rootless podman: Sortie,
acp-broker, the plan workbench, Pomerium, nono and Huly. Worker hosts reach
it only through Pomerium, with a client certificate from the control
plane's CA (spec store `agent-platform/control-plane`, design D18).

- [Operator guide](docs/operator-guide.md): set it up, issue and revoke a
  worker certificate, and where each secret file goes.
- `compose.yaml`: the project. `cp.sh` starts, stops and checks it.
- `images/`: the images built here, tagged with the commit.
- `cmd/cpctl`: the CA, the worker list, and Pomerium's configuration.
- `cmd/cp-egress`: the egress gate behind Pomerium's egress route.
- `cmd/cp-probe`: the health probe the images carry.
- `livetest/`: every scenario, against the running project.

## Develop

Go only, standard library only.

```sh
make test        # the unit tests; the live scenarios skip
make lint        # golangci-lint and shellcheck
make images      # podman build of the images, tagged with the commit
make live-test   # start the project with test certificates and fake credentials, run every scenario, stop it
make pins        # which upstream images are pinned by digest (pin-images.sh pins them)
```

## License

AGPL-3.0-or-later, as the rest of this repository. See `../LICENSE`.
