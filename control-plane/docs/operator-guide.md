# Control plane operator guide

The control plane is one compose project that runs under rootless podman,
as an unprivileged user, on one host. Worker hosts reach it only through
Pomerium, with a client certificate from the control plane's CA. This
guide sets it up, issues and revokes worker certificates, and says where
each secret file goes.

It replaces the stage-1 edge of the stage-0 kit and its SSH tunnels
(spec store `agent-platform-workers`, design D18). The worker daemon and
the runs are unchanged: a worker host gets an `https` control-plane URL and
its certificate instead of a tunnel (see
[Worker hosts](#worker-hosts-move-from-the-edge)).

## What runs

| Service | Image | What it does |
|---|---|---|
| `sortie` | `localhost/ortus-nova/sortie:<commit>`, built here from `sortie/` | The orchestrator. It listens only on its address on the internal network, `10.89.71.10:7680`, in plain HTTP. TLS ends at Pomerium (design D14.4): every flow URL in its workflow is Pomerium's `https` URL. |
| `acp-broker` | `localhost/ortus-nova/acp-broker:<commit>`, built here | Sortie starts one in front of each session (`environment.host_prefix`), in Sortie's container, and the workbench one per planning session, in its own. Both images carry the same broker; the health check runs it in Sortie's container. |
| `workbench` | `localhost/ortus-nova/workbench:<commit>`, built here | The plan workbench, at `http://127.0.0.1:8793` (`CP_WORKBENCH_PORT`). It shares Sortie's network namespace, so it reaches each session's broker on loopback. |
| `pomerium` | `docker.io/pomerium/pomerium`, pinned by digest | The only door for worker hosts, on `CP_PUBLIC_PORT` (8443). See [Pomerium](#pomerium). |
| `egress-gate` | `localhost/ortus-nova/control-plane:<commit>`, built here | Behind Pomerium's egress route: admits each `CONNECT` only with a live egress job token (Sortie's token introspection), logs it with the worker, job and attempt, and tunnels it through nono. |
| `nono` | `ghcr.io/nolabs-ai/nono`, pinned by digest | Holds every platform credential. It allows only the hosts of `nono/egress-profile.json`, which are the hosts of the credentials it holds (every other host a job reaches is in its offer's policy, which the worker allows directly), replaces a job's placeholder with the credential only on requests to that credential's hosts, and logs every request (`podman logs cp-nono`). A guest sees only placeholders. |
| `huly` and `huly-*` | `hardcoreeng/*`, CockroachDB, Redpanda, Elasticsearch, Silo (the MinIO fork huly-selfhost runs, as MinIO left Docker Hub) and nginx, each pinned by digest | The tracker. Its entry, nginx, is at `http://huly.cp.internal:8087` (`CP_HULY_HOST`, `CP_HULY_PORT`), published on `127.0.0.1`. |

Two networks: `ortus-cp-internal` has no route out, and every service is on
it; `ortus-cp-outside` carries the published ports and the egress of Sortie
(the forge) and nono (the model and forge APIs). Huly's stores are on the
internal network only.

The images built here are tagged with the commit they were built from
(`make -C control-plane images`, tag `git rev-parse HEAD`, label
`org.opencontainers.image.revision`). Every other image, and every base the
images built here start from, is pinned by digest; `make -C control-plane
test` fails while one is not (see [Pin the upstream images](#pin-the-upstream-images)).

## Before you start

On the control-plane host, as the user that will run it (never root):

- podman 4.7 or later, rootless (`podman info` says `rootless: true`), and
  podman-compose 1.5 or later (`pipx install podman-compose`), which runs
  each health check as the command it names rather than through `/bin/sh`:
  the images built here have no shell.
- This repository, with the `sortie/` submodule, to build the images
  (`git submodule update --init sortie`), and Go to build `cpctl` if you do
  not run it from the image.
- A DNS name for the control plane that worker hosts resolve, such as
  `cp.example.org`, and one for Huly that the host and people's browsers
  resolve, such as `huly.cp.example.org` (`CP_HULY_HOST`).
- Rootless podman cannot publish a port below 1024 unless
  `net.ipv4.ip_unprivileged_port_start` allows it; the default public port is
  8443.
- `loginctl enable-linger $USER`, so the containers keep running after you
  log out; `systemctl --user enable podman-restart.service` brings them back
  after a reboot.

## Set it up

```sh
make -C control-plane images                                   # tagged with this commit
control-plane/cp.sh cpctl init -public-url https://cp.example.org:8443
```

`cpctl init` makes, in `CP_HOME` (default
`~/.local/share/ortus-nova/control-plane`): the control plane's CA, Pomerium's
server certificate for the public URL's host (add more names with `-name`),
the egress CA, and every secret it can generate. It replaces nothing that
exists, except Pomerium's certificate when the names change. It lists the
secret files you still have to write.

Then write Sortie's workflow: copy `control-plane/sortie/WORKFLOW.example.md`
to `$CP_HOME/sortie/WORKFLOW.md` (`CP_SORTIE_CONFIG_DIR`) and set the public
URL in each flow, the Huly project and the repository. Its secrets stay
`$VAR` references.

Start it:

```sh
control-plane/cp.sh up        # secrets, then the project, then waits until each service is up
control-plane/cp.sh health    # each service, up or down
control-plane/cp.sh down      # stop; volumes and secrets stay
```

`cp.sh health` prints one line per service and fails when one is down:

```text
sortie       up
acp-broker   up
workbench    up
pomerium     up
egress-gate  up
nono         up
huly         up
```

Each line is that container's own health check (`podman healthcheck run`):
Sortie's `/readyz`, the workbench's `/api/auth/mode`, `pomerium health`, the
egress gate's and nono's ports, and Huly's `config.json` through its entry;
acp-broker's is `acp-broker ls` in Sortie's container.

### Huly's first start

Huly needs an account and a workspace before Sortie can log in, and
`cp.sh up` stops while `cp-huly-token` is missing. Start Huly alone with
`control-plane/cp.sh huly`, create the account and the project with
`dev/huly_project.py http://huly.cp.example.org:8087 sortie <email>
<password> ONS`, write the token it prints to
`$CP_HOME/secrets/cp-huly-token`, and run `cp.sh up`.

## Secrets

Every secret is one file in `$CP_HOME/secrets`, mode 0600. At each `cp.sh
up` they become podman secrets (`podman secret create --replace`), which the
compose project hands each container that needs one: as an environment
variable, or as a file under `/run/secrets`. No secret is a value in
`compose.yaml` or a layer of an image; `cpctl secrets` lists them and which
are missing. To change one, replace its file and run `cp.sh up` again. A
file may end in a newline; `cp.sh` drops trailing newlines from each secret.

| File (`$CP_HOME/secrets/...`) | Made by | Goes to |
|---|---|---|
| `cp-sortie-control-token` | `cpctl init` | Sortie (`SORTIE_CONTROL_TOKEN`), the workbench (`WORKBENCH_SORTIE_TOKEN`), the egress gate (file, for token introspection) |
| `cp-nono-pass` | `cpctl init` | nono (`NONO_PROXY_PASS`), the egress gate (file) |
| `cp-workbench-secret` | `cpctl init` | the workbench (`WORKBENCH_SECRET_KEY`) |
| `cp-viewer-token` | `cpctl init` | Sortie (file, the token of each session broker's viewer listener) |
| `cp-huly-secret` | `cpctl init` | every Huly service (`SERVER_SECRET`, the collaborator's `SECRET`) |
| `cp-pomerium-cert`, `cp-pomerium-key` | `cpctl init`, from the CA | Pomerium (files) |
| `cp-egress-ca-cert`, `cp-egress-ca-key` | `cpctl init` | nono (files): it intercepts TLS for the credentials' hosts with this CA. Worker hosts' machines trust its certificate; its key never leaves this host |
| `cp-github-token` | you: a token scoped to the repositories Sortie pushes to | Sortie (`SORTIE_GITHUB_TOKEN`, git and the forge API), the workbench (`WORKBENCH_GITHUB_TOKEN`), nono (`CP_EGRESS_GITHUB`, the `github_api` credential, for `api.github.com` only) |
| `cp-anthropic-token` | you: `claude setup-token` | nono (`CP_EGRESS_ANTHROPIC`) |
| `cp-huly-token` | you: `dev/huly_project.py` prints it | Sortie (`SORTIE_HULY_TOKEN`) |
| `cp-workbench-password-hash` | you: `podman run --rm -i localhost/ortus-nova/workbench:<commit> workbench hash-password` | the workbench (`WORKBENCH_PASSWORD_HASH`) |

The CA's key, `$CP_HOME/ca/ca.key`, is not a podman secret: only `cpctl`
reads it, to issue certificates. Keep `CP_HOME` mode 0700 and back it up.

## Pomerium

`cpctl render` writes Pomerium's configuration, `$CP_HOME/pomerium/config.yaml`,
from the worker list, `$CP_HOME/workers.json`; `cp.sh apply` renders it and
restarts Pomerium onto it.

- **Only the CA's certificates.** `downstream_mtls` with the control plane's
  CA and `enforcement: reject_connection`: a connection without a client
  certificate, or with one another CA issued, is rejected in the TLS
  handshake and reaches no route.
- **One route per flow.** `control` (`/api/v1/workers/`), `stdio`,
  `bundles`, `usage` and `mcp` go to Sortie's worker API and data plane;
  `egress`, every `CONNECT`, goes to the egress gate. Each route allows only
  the fingerprints of the certificates the worker list names for that flow,
  so a worker removed from the list reaches no route.
- **The worker's identity.** On every route Pomerium sets
  `X-Sortie-Worker-Identity`, Sortie's `worker.identity_header`, to the DNS
  name in the client certificate, which is the worker's name, replacing any
  value the worker sent. A worker that sends `X-Sortie-Worker-Identity:
  taichi` with `p1`'s certificate reaches Sortie as `p1`.
- **The MCP route.** A tool call for `/mcp/jobs/{job}/attempts/{attempt}/...`
  goes to Sortie's data plane, `/api/v1/flows/mcp/jobs/{job}/attempts/{attempt}`,
  which looks the request's job token up (`agent-platform/job-tokens`) and
  forwards it only with that attempt's live `mcp` token, to the tool server
  of that attempt's own session, with the session's inner bearer in place
  of the job token. Any other token is refused before it reaches a session.

## Worker certificates

### Issue one

```sh
control-plane/cp.sh worker issue taichi
control-plane/cp.sh apply
```

The name is the worker's identity: a DNS label (lower-case letters, digits,
`-`), the certificate's only DNS name, and the `worker_id` of its daemon.
`issue` writes `$CP_HOME/workers/taichi/worker.crt`, `worker.key` and
`ca.crt`, adds the certificate's fingerprint to the worker list for every
flow (`-flows control,stdio,...` for fewer), renders Pomerium's
configuration, and prints the daemon's settings. `apply` puts it into
effect. Then:

1. Copy the three files to the worker host, for example into
   `~/.config/ortus-nova/` there, mode 0600 for the key, and delete
   `worker.key` here: the key is the one secret a worker host keeps.
2. Copy `$CP_HOME/secrets/cp-egress-ca-cert` to the worker host as the
   egress CA its machines trust (`~/.config/ortus-nova/egress-ca.crt`).
3. Set the daemon's control-plane connection (worker-daemon operator guide)
   and restart it:

   ```json
   "worker_id": "taichi",
   "control_plane": {
     "url": "https://cp.example.org:8443",
     "tls": {"cert_file": "/home/brian/.config/ortus-nova/worker.crt",
             "key_file": "/home/brian/.config/ortus-nova/worker.key",
             "ca_file": "/home/brian/.config/ortus-nova/ca.crt"}
   }
   ```

Issuing a name again gives it a new certificate and drops the old one from
every route at the next `apply`. A certificate is valid for two years;
`cp.sh worker list` shows each one's expiry.

### Revoke one

```sh
control-plane/cp.sh worker revoke taichi
```

`revoke` takes `taichi` off the worker list, deletes what `issue` left for
it in `$CP_HOME/workers`, renders Pomerium's configuration and applies it at
once. From then on Pomerium answers each of `taichi`'s requests 403 on every
route: its next check-in is rejected, and a job it holds can no longer reach
its flows, so its lease expires and Sortie retries the job elsewhere. The
certificate itself stays valid for the CA, so do not put its fingerprint
back; issue a new one if the host returns.

## Pin the upstream images

`compose.yaml` names each upstream image by tag and digest
(`name:tag@sha256:...`). After changing a tag, run on a host that reaches
the registries, with `skopeo`:

```sh
control-plane/pin-images.sh           # the digest each tag has now
make -C control-plane pins            # which images are pinned
```

and commit the result. `make -C control-plane test` fails while an
upstream image, or a base image of `images/*.Dockerfile`, has no digest, and
Dependabot proposes new digests weekly. `make -C control-plane
check-latest` fails when Pomerium or nono has a newer release.

## Worker hosts: move from the edge

The stage-1 edge (the edge and the SSH tunnels on p1) is gone. On p1 the
stage-0 kit runs this front in front of its own Sortie, as user units rather
than the whole compose project (`dev/stage0/README.md`, "Worker hosts"):
Pomerium, the image `compose.yaml` pins, on the host's network; the egress
gate, `cp-egress`; and nono, with `nono/egress-profile.json`. Its CA, worker
list and Pomerium's configuration are `cpctl`'s, in
`$STAGE0_HOME/control-plane` (`cpctl init -address -sortie -egress` routes
Pomerium to Sortie and the gate on loopback). The next
`dev/stage0/stage0.sh` there stops and removes the edge's units
(`ortus-stage0-edge` and each `ortus-stage0-worker-tunnel@<worker>`) and
issues p1's own worker its certificate; `dev/stage0/worker-host.sh add HOST`
issues a worker host's and copies it there, and `remove NAME` revokes it.

For a worker host of a control plane run from this compose project: issue
its certificate as above, point its daemon's `control_plane.url` at
Pomerium with the three TLS files, replace its egress CA certificate with
the control plane's, and restart the daemon. Nothing else on the worker
host changes, and a remote-kind workflow names the flows by Pomerium's URL
instead of the tunnel's loopback end.

## Check it

`make -C control-plane test` runs the unit tests: Pomerium's configuration,
the worker list and certificates, the egress gate, and the compose file's
pins and secrets. `make -C control-plane live-test` (CI runs it) starts the
whole project under rootless podman with test certificates and fake
credentials (`livetest/run.sh`), runs every scenario of
`agent-platform/control-plane` against it through Pomerium as worker hosts
`p1` and `taichi` (`livetest/*_test.go`), and stops it; `CP_LIVE_KEEP=1`
leaves it running.

## Logs

`podman logs cp-<service>`: `cp-sortie`, `cp-workbench`, `cp-pomerium`,
`cp-egress-gate` (each egress tunnel and refusal, with worker, job and
attempt), `cp-nono` (each request it allows or refuses), `cp-huly` and
`cp-huly-*`.
