#!/usr/bin/env bash
# Stages 0 and 1 of agent-platform-workers (spec store, design D13, D18): one
# persistent Sortie under plan control, on p1, that dispatches every
# repository under the remote kind, with the plan workbench, acp-broker, the
# local Huly and the stage-1 edge; and a worker daemon on p1 and on each
# worker host, which runs the jobs (spec store agent-platform/one-sortie), as
# systemd user units. GitHub is the forge. Rootless throughout: no sudo, no
# system unit.
#
# Two profiles, chosen by STAGE0_PROFILE on the first run (then kept in
# stage0.env):
#   control (default)  p1: Sortie, the workbench, acp-broker and the local
#                      Huly, for the whole plan; the stage-1 edge (design
#                      D18): the edge, nono and one SSH tunnel per worker host
#                      (worker-host.sh); and p1's own worker.
#   worker             a worker host such as taichi: the worker daemon only,
#                      which dials p1's edge through the tunnel p1 opens to it
#                      (worker-host.sh add, on p1), and homelab's inputs when
#                      the host has homelab's dev host. No Sortie, no Huly and
#                      no credential. A host on the retired homelab profile
#                      moves to it with STAGE0_PROFILE=worker, which stops its
#                      Sortie and removes its unit.
#
#   dev/stage0/stage0.sh                  set up or update, then (re)start the units
#   dev/stage0/stage0.sh --rebuild-pack   also rebuild the smolvm packs
#   dev/stage0/stage0.sh --update-sortie  also move the Sortie checkout to origin/plan-bench
#                                         (deploy.sh does this once plan-bench moves)
#
# Every step checks first, so a second run changes nothing but a restart.
# Settings live in $STAGE0_HOME/stage0.env, written on the first run (edit it,
# then run this again). State, tokens and workspaces live in $STAGE0_HOME.
#
# Needs (control): smolvm 1.32.4 or later, podman, Go, uv, npm, the openspec CLI, gh signed in,
# a live Claude subscription login (claude setup-token, opencode or Claude Code), Huly running
# (HULY_COMPOSE_DIR) and the host's opencode signed in to Anthropic for
# planning sessions.
# Needs (worker): smolvm 1.32.4 or later, Go, git, curl and p1's tunnel; for
# homelab jobs, python3 and homelab's dev host (`make dev-host` in homelab).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
KIT=$ROOT/dev/stage0
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
REBUILD_PACK='' UPDATE_SORTIE=''
for arg; do
  case $arg in
    --rebuild-pack) REBUILD_PACK=1 ;;
    --update-sortie) UPDATE_SORTIE=1 ;;
    *) echo "usage: $0 [--rebuild-pack] [--update-sortie]" >&2; exit 2 ;;
  esac
done
say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'stage0.sh: %s\n' "$*" >&2; exit 1; }
secret() { [[ -s $1 ]] || { (umask 077; openssl rand -hex 24 > "$1"); }; }

# One run at a time: a second stage0.sh, or deploy.sh moving the live kit,
# waits for this one. deploy.sh takes the lock itself and runs this under it
# (STAGE0_LOCKED=1); the lock goes with the exec below.
if [[ ${STAGE0_LOCKED:-} != 1 ]]; then
  mkdir -p "$STAGE0_HOME"
  exec 9> "$STAGE0_HOME/stage0.lock"
  if ! flock -n 9; then
    say "Waiting for another stage0.sh or a deploy to finish ($STAGE0_HOME/stage0.lock)"
    flock 9
  fi
  export STAGE0_LOCKED=1
fi

# The live kit is the kit's own detached worktree of origin/main, never the
# checkout this runs from: what runs is what merged, whatever a developer
# has checked out (ortus-stage0-deploy.timer keeps it current, deploy.sh).
# STAGE0_KIT_FROM_CHECKOUT=1 runs this checkout instead, to try a branch.
LIVE=$STAGE0_HOME/kit
if [[ ${STAGE0_KIT_FROM_CHECKOUT:-} != 1 && $ROOT != "$LIVE" ]]; then
  git -C "$ROOT" fetch -q origin main
  if [[ -e $LIVE ]]; then
    git -C "$LIVE" checkout -q --detach origin/main
  else
    git -C "$ROOT" worktree add -q --detach "$LIVE" origin/main
  fi
  say "Running the kit from $LIVE (origin/main at $(git -C "$LIVE" rev-parse --short HEAD))"
  exec "$LIVE/dev/stage0/stage0.sh" "$@"
fi

mkdir -p "$STAGE0_HOME"/bin
chmod 700 "$STAGE0_HOME"
# deploy.sh runs this again from a systemd unit, whose PATH lacks the tools
# below; it uses the PATH of the last run, recorded here.
printf '%s\n' "$PATH" > "$STAGE0_HOME/path"
ENV_FILE=$STAGE0_HOME/stage0.env
# The homelab profile, a second Sortie on taichi, is retired (spec store
# agent-platform/one-sortie): its host becomes a worker once p1's Sortie
# dispatches homelab. The move stops that Sortie, so a person makes it.
if [[ -s $ENV_FILE ]] && grep -qx 'STAGE0_PROFILE=homelab' "$ENV_FILE"; then
  [[ ${STAGE0_PROFILE:-} == worker ]] \
    || die "the homelab profile is retired: p1's Sortie dispatches homelab now. Once p1 runs this kit's main, move this host to a worker, which stops its Sortie: STAGE0_PROFILE=worker $0"
  say "Moving $ENV_FILE from the homelab profile to the worker profile"
  sed -i 's/^STAGE0_PROFILE=homelab$/STAGE0_PROFILE=worker/' "$ENV_FILE"
fi
if [[ ! -s $ENV_FILE && ${STAGE0_PROFILE:-control} == worker ]]; then
  say "Writing the default worker-profile settings to $ENV_FILE"
  (umask 077; cat > "$ENV_FILE" <<EOF
# Stage-0 settings, worker profile (dev/stage0/stage0.sh). Run stage0.sh again after an edit.
STAGE0_PROFILE=worker
STAGE0_KIT_ROOT=$ROOT
# The worker's name; p1's edge names it by its tunnel anyway (worker-host.sh).
STAGE0_WORKER_NAME=$(hostname -s)
# Jobs at once (the daemon's run_slots).
STAGE0_MAX_AGENTS=2
# The operator's homelab checkout, for homelab jobs: the kit adds its own
# detached worktree of origin/master under \$STAGE0_HOME/homelab and builds the
# homelab pack from it. Without it, this worker takes no homelab job.
STAGE0_HOMELAB_SRC=$HOME/dev/homelab
STAGE0_HOMELAB_REPO_BASE=master
EOF
  )
fi
if [[ ! -s $ENV_FILE ]]; then
  say "Writing the default settings to $ENV_FILE"
  (umask 077; cat > "$ENV_FILE" <<EOF
# Stage-0 settings (dev/stage0/stage0.sh). Run stage0.sh again after an edit.
STAGE0_KIT_ROOT=$ROOT
STAGE0_SORTIE_SRC=$HOME/dev/sortie-worktrees/stage0
STAGE0_STORE=$HOME/dev/ortus-nova-specs
STAGE0_SORTIE_PORT=7680
STAGE0_WORKBENCH_PORT=8793
STAGE0_HULY_URL=http://localhost:8087
STAGE0_HULY_WORKSPACE=sortie
STAGE0_HULY_EMAIL=sortie@example.com
STAGE0_HULY_PASSWORD=sortie-dev-pass
STAGE0_HULY_PROJECT=ONS
STAGE0_REPO_URL=https://github.com/ortusnova/sortie.git
STAGE0_REPO_BASE=plan-bench
STAGE0_REPO_PROJECT=ortusnova/sortie
# Other repositories (WORKFLOW.md), each dispatched here too.
STAGE0_AO_REPO_URL=https://github.com/ortusnova/agentic-orchestration.git
STAGE0_AO_REPO_BASE=main
STAGE0_AO_REPO_PROJECT=ortusnova/agentic-orchestration
STAGE0_HOMELAB_REPO_URL=https://github.com/ortusnova/homelab.git
STAGE0_HOMELAB_REPO_BASE=master
STAGE0_HOMELAB_REPO_PROJECT=ortusnova/homelab
STAGE0_PACK=$STAGE0_HOME/stage0.smolmachine
# Agents at once; each VM takes 4 vCPU and 8 GiB (WORKFLOW.md).
STAGE0_MAX_AGENTS=3
# The stage-1 edge (README.md): nono's port on p1, and the port each worker
# host's tunnel ends on, on that host's loopback.
STAGE0_NONO_PORT=7690
STAGE0_EDGE_WORKER_PORT=7700
# The previews of review: ui stories' pull requests (run-previews.sh): the
# live workbench's TLS name, which each preview's name sits beside (empty:
# loopback ports only), the first preview port, and a command to run when
# \$STAGE0_HOME/previews/routes changes, such as a proxy reload.
STAGE0_WORKBENCH_URL=
STAGE0_PREVIEW_FIRST_PORT=8800
STAGE0_PREVIEW_ROUTES_HOOK=
EOF
  )
fi
# shellcheck source=/dev/null
. "$ENV_FILE"
# A stage0.env written before a setting existed gets its default here.
STAGE0_PROFILE=${STAGE0_PROFILE:-control}
case $STAGE0_PROFILE in control|worker) ;; *) die "STAGE0_PROFILE is $STAGE0_PROFILE; want control or worker" ;; esac
STAGE0_MAX_AGENTS=${STAGE0_MAX_AGENTS:-3}
STAGE0_PACK=${STAGE0_PACK:-$STAGE0_HOME/stage0.smolmachine}
STAGE0_EDGE_WORKER_PORT=${STAGE0_EDGE_WORKER_PORT:-7700}
STAGE0_WORKER_NAME=${STAGE0_WORKER_NAME:-$(hostname -s)}
[[ $STAGE0_PROFILE == control ]] && STAGE0_WORKER_NAME=p1
STAGE0_AO_REPO_URL=${STAGE0_AO_REPO_URL:-https://github.com/ortusnova/agentic-orchestration.git}
STAGE0_AO_REPO_BASE=${STAGE0_AO_REPO_BASE:-main}
STAGE0_AO_REPO_PROJECT=${STAGE0_AO_REPO_PROJECT:-ortusnova/agentic-orchestration}
STAGE0_HOMELAB_REPO_URL=${STAGE0_HOMELAB_REPO_URL:-https://github.com/ortusnova/homelab.git}
STAGE0_HOMELAB_REPO_BASE=${STAGE0_HOMELAB_REPO_BASE:-master}
STAGE0_HOMELAB_REPO_PROJECT=${STAGE0_HOMELAB_REPO_PROJECT:-ortusnova/homelab}
if [[ $STAGE0_KIT_ROOT != "$ROOT" ]]; then
  say "The units now run the kit from $ROOT, not $STAGE0_KIT_ROOT ($ENV_FILE)"
  sed -i "s|^STAGE0_KIT_ROOT=.*|STAGE0_KIT_ROOT=$ROOT|" "$ENV_FILE"
  STAGE0_KIT_ROOT=$ROOT
fi
export STAGE0_PROFILE STAGE0_HOME STAGE0_HULY_URL STAGE0_HULY_WORKSPACE STAGE0_HULY_PROJECT STAGE0_SORTIE_PORT STAGE0_MAX_AGENTS \
  STAGE0_PACK STAGE0_REPO_URL STAGE0_REPO_BASE STAGE0_REPO_PROJECT \
  STAGE0_AO_REPO_URL STAGE0_AO_REPO_BASE STAGE0_AO_REPO_PROJECT \
  STAGE0_HOMELAB_REPO_URL STAGE0_HOMELAB_REPO_BASE STAGE0_HOMELAB_REPO_PROJECT STAGE0_EDGE_WORKER_PORT

if [[ $STAGE0_PROFILE == worker ]]; then
  # make dev-host installs smolvm in ~/.local/bin, which a non-login shell lacks.
  case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH=$HOME/.local/bin:$PATH ;; esac
  needed=(smolvm go git curl sha256sum)
else
  needed=(smolvm podman go uv npm openspec gh git openssl python3 curl sha256sum tar ssh)
fi
for cmd in "${needed[@]}"; do
  command -v "$cmd" >/dev/null || die "$cmd is not on PATH"
done
# shellcheck source=smolvm-version.sh
. "$KIT/smolvm-version.sh"
# shellcheck source=huly-restart-policy.sh
. "$KIT/huly-restart-policy.sh"
smolvm_version_ok > /dev/null || die "the kit needs smolvm $SMOLVM_MIN or later; found: $(smolvm --version 2>&1 | head -1)"
# shellcheck source=worker-config.sh
. "$KIT/worker-config.sh"
UNITS=${STAGE0_UNITS:-$HOME/.config/systemd/user}

if [[ $STAGE0_PROFILE == worker ]]; then
  # A worker host holds no credential (spec store agent-platform/stage-1-edge):
  # its jobs' model and forge requests get theirs from nono on p1.
  for f in github-token claude-oauth-token; do
    [[ -e $STAGE0_HOME/$f ]] && say "$STAGE0_HOME/$f is no longer used on a worker host: delete it, and revoke the token"
  done
  STAGE0_EGRESS_CA=$HOME/.config/ortus-nova/egress-ca.crt
  [[ -s $STAGE0_EGRESS_CA ]] || die "$STAGE0_EGRESS_CA is missing: on p1, run dev/stage0/worker-host.sh add $(hostname -s)"
  say "Building worker-daemon and smol-builder into $STAGE0_HOME/bin"
  (cd "$ROOT/worker-daemon" && CGO_ENABLED=0 go build -o "$STAGE0_HOME/bin/worker-daemon" ./cmd/worker-daemon)
  (cd "$ROOT/smol-builder" && go build -o "$STAGE0_HOME/bin/smol-builder" ./cmd/smol-builder)
  export PATH=$STAGE0_HOME/bin:$PATH
  if [[ -n $REBUILD_PACK || ! -s $STAGE0_PACK ]]; then
    say "Building the pack $STAGE0_PACK"
    smol-builder build --repo "$KIT/pack" --output "$STAGE0_PACK" > "$STAGE0_HOME/pack-report.json"
  fi
  if [[ -n ${STAGE0_HOMELAB_SRC:-} && -d $STAGE0_HOMELAB_SRC ]]; then
    command -v python3 > /dev/null || die "python3 is not on PATH; homelab's inputs need it"
    # shellcheck source=homelab-inputs.sh
    . "$KIT/homelab-inputs.sh"
    homelab_inputs
  else
    say "No homelab checkout (STAGE0_HOMELAB_SRC): this worker takes no homelab job"
  fi
  render_worker_config "$STAGE0_HOME/worker.json" "$STAGE0_WORKER_NAME" "http://127.0.0.1:$STAGE0_EDGE_WORKER_PORT" "$STAGE0_EGRESS_CA"
  "$STAGE0_HOME/bin/worker-daemon" check -config "$STAGE0_HOME/worker.json" > /dev/null \
    || die "the worker daemon refused $STAGE0_HOME/worker.json"

  retire_sortie "$UNITS"
  say "Installing the user units"
  mkdir -p "$UNITS"
  units=(ortus-stage0-worker.service)
  deploy=''
  [[ $ROOT == "$LIVE" ]] && deploy=1 && units+=(ortus-stage0-deploy.service ortus-stage0-deploy.timer)
  for unit in "${units[@]}"; do
    sed -e "s|@KIT@|$KIT|g" -e "s|@STAGE0_HOME@|$STAGE0_HOME|g" "$KIT/units/$unit" > "$UNITS/$unit"
  done
  systemctl --user daemon-reload
  if [[ $deploy ]]; then
    systemctl --user enable -q --now ortus-stage0-deploy.timer
  else
    systemctl --user disable -q --now ortus-stage0-deploy.timer 2>/dev/null || true
  fi
  systemctl --user enable -q ortus-stage0-worker.service
  systemctl --user restart ortus-stage0-worker.service
  sleep 2
  systemctl --user is-active -q ortus-stage0-worker.service || die "the worker daemon did not start: journalctl --user -u ortus-stage0-worker"
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$STAGE0_EDGE_WORKER_PORT/" || true)
  [[ $code != 000 && -n $code ]] \
    || say "p1's edge does not answer at http://127.0.0.1:$STAGE0_EDGE_WORKER_PORT yet: on p1, run dev/stage0/worker-host.sh add $(hostname -s)"
  say "Ready: worker $STAGE0_WORKER_NAME dials p1's edge at http://127.0.0.1:$STAGE0_EDGE_WORKER_PORT (journalctl --user -u ortus-stage0-worker)"
  exit 0
fi

mkdir -p "$STAGE0_HOME"/{sortie,workspaces,home,sessions}
gh auth token >/dev/null || die "gh has no token: run gh auth login"
STAGE0_HOME=$STAGE0_HOME "$KIT/anthropic-token.py" >/dev/null \
  || die "no live Claude subscription token: run claude setup-token into $STAGE0_HOME/claude-oauth-token"

if [[ ! -d $STAGE0_SORTIE_SRC ]]; then
  say "Creating the Sortie checkout $STAGE0_SORTIE_SRC (ortusnova/sortie plan-bench)"
  git clone -q --branch plan-bench https://github.com/ortusnova/sortie.git "$STAGE0_SORTIE_SRC"
fi
if [[ -n $UPDATE_SORTIE ]]; then
  say "Moving $STAGE0_SORTIE_SRC to origin/plan-bench"
  git -C "$STAGE0_SORTIE_SRC" fetch -q origin plan-bench
  git -C "$STAGE0_SORTIE_SRC" checkout -q --detach origin/plan-bench
fi
say "Building sortie $(git -C "$STAGE0_SORTIE_SRC" rev-parse --short HEAD), acp-broker, smol-builder and worker-daemon into $STAGE0_HOME/bin"
(cd "$STAGE0_SORTIE_SRC" && GOWORK=off go build -o "$STAGE0_HOME/bin/sortie" ./cmd/sortie)
(cd "$ROOT/acp-broker" && go build -o "$STAGE0_HOME/bin/acp-broker" ./cmd/acp-broker)
(cd "$ROOT/smol-builder" && go build -o "$STAGE0_HOME/bin/smol-builder" ./cmd/smol-builder)
(cd "$ROOT/worker-daemon" && CGO_ENABLED=0 go build -o "$STAGE0_HOME/bin/worker-daemon" ./cmd/worker-daemon)
export PATH=$STAGE0_HOME/bin:$PATH
# shellcheck source=nono-version.sh
. "$KIT/nono-version.sh"
say "Building the stage-1 edge and installing nono $NONO_VERSION (pinned by checksum) into $STAGE0_HOME/bin"
(cd "$KIT/edge" && go build -o "$STAGE0_HOME/bin/stage0-edge" .)
nono_install "$STAGE0_HOME/bin" || die "could not install nono $NONO_VERSION"

say "Installing workbench dependencies and building the frontend"
(cd "$ROOT/workbench" && uv sync -q)
(cd "$ROOT/workbench/frontend" && { [[ -d node_modules ]] || npm install --silent; } && npx vite build --logLevel warn)

HULY_DIR=${HULY_COMPOSE_DIR:-$HOME/dev/huly-selfhost}
if ! curl -sf -o /dev/null "$STAGE0_HULY_URL/config.json"; then
  [[ -d $HULY_DIR ]] || die "Huly is not running and HULY_COMPOSE_DIR is not set"
  say "Starting Huly"
  # The containers outlive this run; they must not hold its lock.
  (cd "$HULY_DIR" && podman compose up -d >/dev/null 9>&-)
  for _ in $(seq 60); do curl -sf -o /dev/null "$STAGE0_HULY_URL/config.json" && break; sleep 2; done
fi
# Every Huly container must restart unless stopped, and podman honors that
# after a reboot only through this user unit. huly-selfhost leaves redpanda
# without a policy (huly-restart-policy.sh).
for name in $(huly_restart_policies "$HULY_DIR"); do say "Huly container $name now restarts unless stopped"; done
systemctl --user is-enabled -q podman-restart.service || { say "Enabling podman-restart.service"; systemctl --user enable -q podman-restart.service; }
say "Preparing Huly project $STAGE0_HULY_PROJECT"
python3 "$ROOT/dev/huly_project.py" "$STAGE0_HULY_URL" "$STAGE0_HULY_WORKSPACE" "$STAGE0_HULY_EMAIL" \
  "$STAGE0_HULY_PASSWORD" "$STAGE0_HULY_PROJECT" >/dev/null || die "could not prepare Huly project $STAGE0_HULY_PROJECT"

secret "$STAGE0_HOME/workbench-secret"
if [[ ! -s $STAGE0_HOME/workbench-password-hash ]]; then
  secret "$STAGE0_HOME/workbench-password"
  (cd "$ROOT/workbench" && umask 077 && uv run python -c 'import sys
from workbench.auth import hash_password
print(hash_password(open(sys.argv[1]).read().strip()))' "$STAGE0_HOME/workbench-password" > "$STAGE0_HOME/workbench-password-hash")
fi
secret "$STAGE0_HOME/sortie-token"
secret "$STAGE0_HOME/viewer-token"
# The edge reaches nono with this; nono intercepts TLS for the credential
# hosts with the egress CA, whose certificate each worker gets (worker-host.sh,
# worker-config.sh) and whose key never leaves this host.
secret "$STAGE0_HOME/nono-pass"
if [[ ! -s $STAGE0_HOME/egress-ca/ca.key ]]; then
  say "Creating the egress CA in $STAGE0_HOME/egress-ca"
  mkdir -p "$STAGE0_HOME/egress-ca"
  (umask 077 && cd "$STAGE0_HOME/egress-ca" \
    && openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out ca.key 2>/dev/null \
    && openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=Ortus Nova stage-0 egress CA" \
      -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign,digitalSignature" \
      -out ca.crt) || die "could not create the egress CA"
  chmod 644 "$STAGE0_HOME/egress-ca/ca.crt"
fi
# shellcheck source=edge-config.sh
. "$KIT/edge-config.sh"
# p1's own worker: its edge port is on p1's loopback, with no tunnel.
register_local_worker "$STAGE0_WORKER_NAME"
render_edge_config "$STAGE0_HOME/edge.json"
"$STAGE0_HOME/bin/stage0-edge" -check -config "$STAGE0_HOME/edge.json" >/dev/null || die "the edge refused $STAGE0_HOME/edge.json"

if [[ -n $REBUILD_PACK || ! -s $STAGE0_PACK ]]; then
  say "Building the pack $STAGE0_PACK"
  smol-builder build --repo "$KIT/pack" --output "$STAGE0_PACK" > "$STAGE0_HOME/pack-report.json"
fi
STAGE0_EGRESS_CA=$STAGE0_HOME/egress-ca/ca.crt
# p1 takes homelab jobs only with homelab's dev host here (STAGE0_HOMELAB_SRC).
if [[ -n ${STAGE0_HOMELAB_SRC:-} && -d $STAGE0_HOMELAB_SRC ]]; then
  # shellcheck source=homelab-inputs.sh
  . "$KIT/homelab-inputs.sh"
  homelab_inputs
fi
render_worker_config "$STAGE0_HOME/worker.json" "$STAGE0_WORKER_NAME" "http://127.0.0.1:$STAGE0_EDGE_WORKER_PORT" "$STAGE0_EGRESS_CA"
"$STAGE0_HOME/bin/worker-daemon" check -config "$STAGE0_HOME/worker.json" > /dev/null \
  || die "the worker daemon refused $STAGE0_HOME/worker.json"

say "Rendering Sortie's workflow"
STAGE0_KIT=$KIT python3 "$ROOT/dev/forge/render.py" "$KIT/WORKFLOW.md" "$STAGE0_HOME/sortie/WORKFLOW.md"
validated=$("$KIT/run-sortie.sh" validate 2>&1) || die "Sortie refused the workflow ($KIT/run-sortie.sh validate): $validated"
# A key Sortie does not know is one it ignores: the workflow would not do
# what it says, as when the remote kind ignored every repository but one.
if unknown=$(grep -iE 'unknown_[a-z_]*key' <<< "$validated"); then
  die "Sortie ignores a key of the workflow ($KIT/run-sortie.sh validate): $unknown"
fi

say "Installing the user units"
retire_huly_tunnels "$UNITS"
mkdir -p "$UNITS"
units=(ortus-stage0-sortie.service ortus-stage0-workbench.service ortus-stage0-token.service ortus-stage0-token.timer
  ortus-stage0-store.service ortus-stage0-store.timer
  ortus-stage0-preview@.service ortus-stage0-previews.service ortus-stage0-previews.timer
  ortus-stage0-nono.service ortus-stage0-edge.service ortus-stage0-worker-tunnel@.service ortus-stage0-worker.service)
# The live kit follows origin/main on its own; a kit run from a checkout
# (STAGE0_KIT_FROM_CHECKOUT=1) stays where it is.
deploy=''
[[ $ROOT == "$LIVE" ]] && deploy=1 && units+=(ortus-stage0-deploy.service ortus-stage0-deploy.timer)
for unit in "${units[@]}"; do
  sed -e "s|@KIT@|$KIT|g" -e "s|@STAGE0_HOME@|$STAGE0_HOME|g" "$KIT/units/$unit" > "$UNITS/$unit"
done
systemctl --user daemon-reload
if [[ $deploy ]]; then
  systemctl --user enable -q --now ortus-stage0-deploy.timer
else
  systemctl --user disable -q --now ortus-stage0-deploy.timer 2>/dev/null || true
fi
systemctl --user enable -q ortus-stage0-sortie.service ortus-stage0-workbench.service ortus-stage0-nono.service \
  ortus-stage0-edge.service ortus-stage0-worker.service
systemctl --user enable -q --now ortus-stage0-token.timer
# The store follows its upstream from now on; no person pulls it.
systemctl --user enable -q --now ortus-stage0-store.timer
# The ui stories' previews follow their pull requests (run-previews.sh).
systemctl --user enable -q --now ortus-stage0-previews.timer
systemctl --user restart ortus-stage0-sortie.service
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_SORTIE_PORT/readyz" && break; sleep 1; done
systemctl --user restart ortus-stage0-nono.service ortus-stage0-edge.service
for name in $(tunnel_workers); do systemctl --user enable -q --now "ortus-stage0-worker-tunnel@$name.service"; done
systemctl --user restart ortus-stage0-worker.service
systemctl --user restart ortus-stage0-workbench.service
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_WORKBENCH_PORT/api/auth/mode" && break; sleep 1; done
curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_WORKBENCH_PORT/api/auth/mode" || die "the workbench did not start: journalctl --user -u ortus-stage0-workbench"
for unit in ortus-stage0-nono ortus-stage0-edge ortus-stage0-worker; do
  systemctl --user is-active -q "$unit.service" || die "$unit did not start: journalctl --user -u $unit"
done
say "Ready: workbench http://127.0.0.1:$STAGE0_WORKBENCH_PORT (password in $STAGE0_HOME/workbench-password), Sortie 127.0.0.1:$STAGE0_SORTIE_PORT, Huly $STAGE0_HULY_URL/workbench/$STAGE0_HULY_WORKSPACE/tracker, edge for $(worker_names | wc -l) worker(s), p1's own and $(tunnel_workers | wc -l) worker host(s) (dev/stage0/worker-host.sh list)"
