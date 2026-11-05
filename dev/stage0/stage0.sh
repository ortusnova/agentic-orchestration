#!/usr/bin/env bash
# Stages 0 and 1 of agent-platform-workers (spec store, design D13, D18): one
# persistent Sortie under plan control, on p1, that dispatches every
# repository under the remote kind, with the plan workbench, acp-broker, the
# local Huly and the control plane's front (Pomerium, the egress gate and
# nono; spec store agent-platform/control-plane); and a worker daemon on p1
# and on each worker host, which runs the jobs (spec store
# agent-platform/one-sortie), as systemd user units. GitHub is the forge.
# Rootless throughout: no sudo, no system unit.
#
# Two profiles, chosen by STAGE0_PROFILE on the first run (then kept in
# stage0.env):
#   control (default)  p1: Sortie, the workbench, acp-broker and the local
#                      Huly, for the whole plan; the control plane's front
#                      (design D18), which replaced the stage-1 edge and its
#                      SSH tunnels: Pomerium, which admits only the client
#                      certificates of its CA (worker-host.sh), the egress
#                      gate and nono; and p1's own worker.
#   worker             a worker host such as taichi: the worker daemon only,
#                      which dials p1's Pomerium with the client certificate
#                      worker-host.sh add, on p1, gave it, and homelab's
#                      inputs when the host has homelab's dev host. No
#                      Sortie, no Huly and no credential. A host on the
#                      retired homelab profile moves to it with
#                      STAGE0_PROFILE=worker, which stops its Sortie and
#                      removes its unit.
#
#   dev/stage0/stage0.sh                  set up or update, then (re)start the units
#   dev/stage0/stage0.sh --rebuild-pack   also rebuild the smolvm packs, which are
#                                         rebuilt by themselves when their inputs change
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
# planning sessions and the supervisor.
# Needs (worker): smolvm 1.32.4 or later, Go, git, curl and p1's Pomerium; for
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
# The worker's name: the name of the certificate worker-host.sh add gave it,
# which Pomerium names it by anyway.
STAGE0_WORKER_NAME=$(hostname -s)
# Jobs at once (the daemon's run_slots): unset, as many as this host's CPUs
# and memory hold (worker-config.sh).
STAGE0_WORKER_SLOTS=
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
# p1's own worker's jobs at once: unset, as many as p1's CPUs and memory
# hold (worker-config.sh); each VM takes 4 vCPU and 8 GiB (WORKFLOW.md).
STAGE0_WORKER_SLOTS=
# Sortie's cap on agents at once, on every worker together: unset, a ceiling
# above the workers' slots, which the matcher holds (worker-config.sh).
STAGE0_SORTIE_MAX_AGENTS=
# The control plane's front (README.md "Worker hosts"): Pomerium's public
# URL, which every worker dials and whose host must resolve to p1 on each
# of them; and, on p1's loopback, nono, the live-proof nono (the egress of
# a live-proof job that runs) and the egress gate.
STAGE0_CP_PUBLIC_URL=https://$(hostname -f):8443
STAGE0_NONO_PORT=7690
STAGE0_NONO_LIVE_PROOF_PORT=7691
STAGE0_EGRESS_GATE_PORT=7692
# The operator's channel for every question (README.md): slack, discord, telegram or
# webhook, with its credential in \$STAGE0_HOME/notify-credential, mode 0600; and
# where people reach the workbench, for the link in each question.
STAGE0_NOTIFY_KIND=
STAGE0_NOTIFY_CHAT_ID=
STAGE0_WORKBENCH_PUBLIC_URL=http://127.0.0.1:8793
# The previews of review: ui stories' pull requests (run-previews.sh): the
# live workbench's TLS name, which each preview's name for people sits
# beside (empty: none; the address the review opens is always a name under
# Pomerium's, README.md), the first preview port, and a command to run when
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
STAGE0_PACK=${STAGE0_PACK:-$STAGE0_HOME/stage0.smolmachine}
# Pomerium's public URL, which every worker dials: on p1 it stays as the
# first run that knew it wrote it to stage0.env; a worker host has it from
# worker-host.sh add (control-plane-url), unless its stage0.env says
# otherwise.
WORKER_HOST_CONFIG=$HOME/.config/ortus-nova
# A worker host that the stage-1 edge served gets its certificate from p1's
# stage0.sh, which runs from the same merge (carry_edge_workers in
# retire-edge.sh). The live kit, which the deploy timer runs, waits for it
# (STAGE0_WORKER_CERT_WAIT seconds); a kit run by hand does not.
if [[ $STAGE0_PROFILE == worker && -z ${STAGE0_CP_PUBLIC_URL:-} ]]; then
  cert_wait=${STAGE0_WORKER_CERT_WAIT:-0}
  [[ $ROOT == "$LIVE" ]] && cert_wait=${STAGE0_WORKER_CERT_WAIT:-1800}
  [[ $cert_wait =~ ^[0-9]+$ ]] || die "STAGE0_WORKER_CERT_WAIT is $cert_wait; want seconds"
  # shellcheck source=retire-edge.sh
  . "$KIT/retire-edge.sh"
  wait_for_worker_files "$WORKER_HOST_CONFIG" "$cert_wait" \
    || die "p1 has not given this host its certificate in $cert_wait s ($WORKER_HOST_CONFIG): on p1, run dev/stage0/worker-host.sh add $(hostname -s)"
fi
if [[ -z ${STAGE0_CP_PUBLIC_URL:-} ]]; then
  if [[ $STAGE0_PROFILE == worker ]]; then
    STAGE0_CP_PUBLIC_URL=$(cat "$WORKER_HOST_CONFIG/control-plane-url" 2>/dev/null || true)
    [[ -n $STAGE0_CP_PUBLIC_URL ]] || die "$WORKER_HOST_CONFIG/control-plane-url is missing: on p1, run dev/stage0/worker-host.sh add $(hostname -s)"
  else
    STAGE0_CP_PUBLIC_URL=https://$(hostname -f):8443
    say "Pomerium's public URL is $STAGE0_CP_PUBLIC_URL (STAGE0_CP_PUBLIC_URL in $ENV_FILE); every worker host must resolve its host to p1"
    printf '# Pomerium'"'"'s public URL, which every worker dials (README.md "Worker hosts").\nSTAGE0_CP_PUBLIC_URL=%s\n' "$STAGE0_CP_PUBLIC_URL" >> "$ENV_FILE"
  fi
fi
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
export STAGE0_PROFILE STAGE0_HOME STAGE0_HULY_URL STAGE0_HULY_WORKSPACE STAGE0_HULY_PROJECT STAGE0_SORTIE_PORT \
  STAGE0_PACK STAGE0_REPO_URL STAGE0_REPO_BASE STAGE0_REPO_PROJECT \
  STAGE0_AO_REPO_URL STAGE0_AO_REPO_BASE STAGE0_AO_REPO_PROJECT \
  STAGE0_HOMELAB_REPO_URL STAGE0_HOMELAB_REPO_BASE STAGE0_HOMELAB_REPO_PROJECT STAGE0_CP_PUBLIC_URL

if [[ $STAGE0_PROFILE == worker ]]; then
  # make dev-host installs smolvm in ~/.local/bin, which a non-login shell lacks.
  case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH=$HOME/.local/bin:$PATH ;; esac
  needed=(smolvm go git curl sha256sum)
else
  # opencode: the planning sessions and the supervisor run the host's.
  needed=(smolvm podman go uv npm openspec gh git openssl python3 curl sha256sum tar ssh opencode)
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
# shellcheck source=staged-pack.sh
. "$KIT/staged-pack.sh"
# shellcheck source=notify-channel.sh
. "$KIT/notify-channel.sh"
migrate_max_agents "$ENV_FILE"
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
# shellcheck source=deploy-state.sh
. "$KIT/deploy-state.sh"
UNITS=${STAGE0_UNITS:-$HOME/.config/systemd/user}
# The worker daemon's machines carry the runs: it restarts only when its
# binary, its configuration or its unit changed (worker-build.sh).
# shellcheck source=worker-build.sh
. "$KIT/worker-build.sh"

# The pack is built again when its inputs change (the kit's pack/,
# smol-builder's source, which embeds its layers, and smolvm), so a merged
# change to the agent's config reaches the next machine through deploy.sh.
# Not the smol-builder binary: Go stamps it with the kit's commit.
build_pack() {
  local pack_inputs
  pack_inputs=$({
    (cd "$ROOT" && find dev/stage0/pack smol-builder -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
    smolvm --version
  } | sha256sum | cut -c1-16)
  if [[ -n $REBUILD_PACK || ! -s $STAGE0_PACK || $(cat "$STAGE0_PACK.inputs" 2>/dev/null) != "$pack_inputs" ]]; then
    say "Building the pack $STAGE0_PACK ($pack_inputs)"
    smol-builder build --repo "$KIT/pack" --output "$STAGE0_PACK.tmp" > "$STAGE0_HOME/pack-report.json" \
      || { rm -f "$STAGE0_PACK.tmp"; die "the pack did not build: $STAGE0_HOME/pack-report.json"; }
    mv "$STAGE0_PACK.tmp" "$STAGE0_PACK"
    echo "$pack_inputs" > "$STAGE0_PACK.inputs"
  fi
}

if [[ $STAGE0_PROFILE == worker ]]; then
  # A worker host holds no credential (spec store agent-platform/stage-1-edge):
  # its jobs' model and forge requests get theirs from nono on p1.
  for f in github-token claude-oauth-token; do
    [[ -e $STAGE0_HOME/$f ]] && say "$STAGE0_HOME/$f is no longer used on a worker host: delete it, and revoke the token"
  done
  STAGE0_EGRESS_CA=$WORKER_HOST_CONFIG/egress-ca.crt
  # The client certificate from the control plane's CA, its key and that
  # CA (worker-host.sh add, on p1), which Pomerium admits this worker by.
  worker_tls=("$WORKER_HOST_CONFIG/cp-worker.crt" "$WORKER_HOST_CONFIG/cp-worker.key" "$WORKER_HOST_CONFIG/cp-ca.crt")
  for f in "$STAGE0_EGRESS_CA" "${worker_tls[@]}"; do
    [[ -s $f ]] || die "$f is missing: on p1, run dev/stage0/worker-host.sh add $(hostname -s)"
  done
  say "Building worker-daemon and smol-builder into $STAGE0_HOME/bin"
  build_worker_daemon
  (cd "$ROOT/smol-builder" && go build -o "$STAGE0_HOME/bin/smol-builder" ./cmd/smol-builder)
  export PATH=$STAGE0_HOME/bin:$PATH
  build_pack
  # A worker host that offers nested virtualization (taichi) declares nested,
  # with the live-proof pack the nested profile runs in (WORKFLOW.md).
  STAGE0_WORKER_NESTED=false
  if host_nested; then
    STAGE0_WORKER_NESTED=true
    # shellcheck source=live-proof-pack.sh
    . "$KIT/live-proof-pack.sh"
    live_proof_pack
  else
    say "This host offers no nested virtualization: its worker takes no homelab job and no live proof of the nested profile"
  fi
  if [[ -n ${STAGE0_HOMELAB_SRC:-} && -d $STAGE0_HOMELAB_SRC ]]; then
    command -v python3 > /dev/null || die "python3 is not on PATH; homelab's inputs need it"
    # shellcheck source=homelab-inputs.sh
    . "$KIT/homelab-inputs.sh"
    homelab_inputs
  else
    say "No homelab checkout (STAGE0_HOMELAB_SRC): this worker takes no homelab job"
  fi
  render_worker_config "$STAGE0_HOME/worker.json" "$STAGE0_WORKER_NAME" "$STAGE0_CP_PUBLIC_URL" "$STAGE0_EGRESS_CA" "${worker_tls[@]}"
  "$STAGE0_HOME/bin/worker-daemon" check -config "$STAGE0_HOME/worker.json" > /dev/null \
    || die "the worker daemon refused $STAGE0_HOME/worker.json"

  retire_sortie "$UNITS" || die "this host's Sortie keeps running until p1's Sortie holds its merge records (see above)"
  # It runs no Sortie, so its deploy state lists no ortusnova/sortie.
  deploy_sortie_retired
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
  restart_worker
  sleep 2
  systemctl --user is-active -q ortus-stage0-worker.service || die "the worker daemon did not start: journalctl --user -u ortus-stage0-worker"
  # Pomerium answers a listed worker's certificate on its control route;
  # it rejects a connection without one in the handshake (000).
  code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "${worker_tls[2]}" --cert "${worker_tls[0]}" --key "${worker_tls[1]}" \
    "$STAGE0_CP_PUBLIC_URL/api/v1/workers/" || true)
  [[ $code != 000 && $code != 403 && -n $code ]] \
    || say "p1's Pomerium does not admit this worker at $STAGE0_CP_PUBLIC_URL yet (HTTP $code): on p1, run dev/stage0/worker-host.sh add $(hostname -s)"
  say "Ready: worker $STAGE0_WORKER_NAME dials p1's Pomerium at $STAGE0_CP_PUBLIC_URL (journalctl --user -u ortus-stage0-worker)"
  exit 0
fi

# The operator's channel (notify-channel.sh): optional; with no
# STAGE0_NOTIFY_KIND the questions show on the workbench only.
STAGE0_NOTIFY_KIND=${STAGE0_NOTIFY_KIND:-}
STAGE0_NOTIFY_ENTRY=$(notify_entry "$STAGE0_NOTIFY_KIND" "${STAGE0_NOTIFY_CHAT_ID:-}") || die "fix the channel in $ENV_FILE"
export STAGE0_NOTIFY_ENTRY
if [[ -n $STAGE0_NOTIFY_KIND ]]; then
  notify_credential "$STAGE0_HOME/notify-credential" || die "the $STAGE0_NOTIFY_KIND channel has no credential"
else
  say "No operator channel of your own: Sortie's notifications go to the workbench only. Set STAGE0_NOTIFY_KIND in $ENV_FILE to add one (README.md)"
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
sortie_built=$(git -C "$STAGE0_SORTIE_SRC" rev-parse HEAD)
say "Building sortie ${sortie_built:0:7}, acp-broker, smol-builder and worker-daemon into $STAGE0_HOME/bin"
(cd "$STAGE0_SORTIE_SRC" && GOWORK=off go build -o "$STAGE0_HOME/bin/sortie" ./cmd/sortie)
(cd "$ROOT/acp-broker" && go build -o "$STAGE0_HOME/bin/acp-broker" ./cmd/acp-broker)
(cd "$ROOT/smol-builder" && go build -o "$STAGE0_HOME/bin/smol-builder" ./cmd/smol-builder)
build_worker_daemon
export PATH=$STAGE0_HOME/bin:$PATH
# shellcheck source=nono-version.sh
. "$KIT/nono-version.sh"
say "Building control-plane's cpctl and egress gate and stage0-live-proofs, and installing nono $NONO_VERSION (pinned by checksum) into $STAGE0_HOME/bin"
(cd "$ROOT/control-plane" && CGO_ENABLED=0 go build -o "$STAGE0_HOME/bin/" ./cmd/cpctl ./cmd/cp-egress)
(cd "$KIT/live-proofs" && go build -o "$STAGE0_HOME/bin/stage0-live-proofs" .)
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
# This host's token for the workbench's notifications (notify-channel.sh):
# Sortie posts with it, the workbench takes it, no person writes it.
notify_host=$(notify_host) || die "fix the host name in $ENV_FILE"
notify_token "$STAGE0_HOME" "$notify_host" || die "could not mint $notify_host's notifications token"
secret "$STAGE0_HOME/viewer-token"
# The supervisor's token and prompt (supervisor.sh), which the workflow's
# supervisor section reads.
# shellcheck source=supervisor.sh
. "$KIT/supervisor.sh"
supervisor_install "$STAGE0_HOME" "$KIT" || die "could not install the supervisor"
# The egress gate reaches nono with this; nono intercepts TLS for the
# credential hosts with the egress CA, whose certificate each worker gets
# (worker-host.sh, worker-config.sh) and whose key never leaves this host.
secret "$STAGE0_HOME/nono-pass"
# The egress gate gives a review job's requests to a ui story's preview this
# key, which signs the review in there (run-egress-gate.sh, run-workbench.sh).
secret "$STAGE0_HOME/preview-review-key"
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
# The control plane's front (front-config.sh): the CA and Pomerium's
# certificate and configuration, and p1's own worker's certificate.
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
[[ -n $(pomerium_image) ]] || die "control-plane/compose.yaml pins no Pomerium image"
front_init || die "could not set up the control plane's CA and Pomerium's configuration in $STAGE0_CP_HOME"
issue_local_worker "$STAGE0_WORKER_NAME" || die "could not issue p1's own worker its certificate"
# Each worker host the stage-1 edge served gets its certificate now, before
# the edge and its tunnels are retired below: a host that cannot be reached
# stops this run with the edge still up.
# shellcheck source=retire-edge.sh
. "$KIT/retire-edge.sh"
carried=$(carry_edge_workers "$STAGE0_HOME/workers" "$KIT/worker-host.sh" add) \
  || die "could not give each worker host of the stage-1 edge its certificate; the edge stays until it can"
for name in $carried; do say "Gave worker host $name its certificate: it dials Pomerium from its next stage0.sh"; done
# Sortie's health check and merge-and-prove read each host's deploy state
# (deploy-state.sh): p1's own, and a copy of each worker host's, which
# deploy.sh fetches over SSH every minute. A worker host listed before
# worker-host.sh add recorded its SSH destination is reached by its name.
for name in $(front_workers); do
  [[ $name == "$STAGE0_WORKER_NAME" || -s $STAGE0_HOME/deploy-hosts/$name ]] && continue
  deploy_host_add "$name" "$name"
  say "p1 reads worker host $name's deploy state over ssh $name ($STAGE0_HOME/deploy-hosts/$name names another destination)"
done
STAGE0_DEPLOY_HOSTS=$(deploy_hosts_yaml)
export STAGE0_DEPLOY_HOSTS
mkdir -p "$STAGE0_LIVE_PROOFS"

build_pack
STAGE0_EGRESS_CA=$STAGE0_HOME/egress-ca/ca.crt
# p1's worker never declares nested: only a worker host does (taichi), so a
# homelab job and a live proof of the nested profile never run on p1.
STAGE0_WORKER_NESTED=false
if [[ -n ${STAGE0_HOMELAB_SRC:-} && -d $STAGE0_HOMELAB_SRC ]]; then
  say "p1's worker declares no nested, so it takes no homelab job: STAGE0_HOMELAB_SRC is not used here; homelab runs on taichi"
fi
# p1's own worker dials Pomerium as every worker host does.
local_tls=$STAGE0_CP_HOME/workers/$STAGE0_WORKER_NAME
render_worker_config "$STAGE0_HOME/worker.json" "$STAGE0_WORKER_NAME" "$STAGE0_CP_PUBLIC_URL" "$STAGE0_EGRESS_CA" \
  "$local_tls/worker.crt" "$local_tls/worker.key" "$local_tls/ca.crt"
"$STAGE0_HOME/bin/worker-daemon" check -config "$STAGE0_HOME/worker.json" > /dev/null \
  || die "the worker daemon refused $STAGE0_HOME/worker.json"

say "Rendering Sortie's workflow"
STAGE0_SORTIE_MAX_AGENTS=$(sortie_agent_cap) || die "the Sortie-wide agent cap is wrong ($ENV_FILE)"
export STAGE0_SORTIE_MAX_AGENTS
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
  ortus-stage0-nono.service ortus-stage0-nono-live-proof.service ortus-stage0-live-proofs.service ortus-stage0-live-proofs.timer
  ortus-stage0-egress-gate.service ortus-stage0-pomerium.service ortus-stage0-worker.service)
# The live kit follows origin/main on its own; a kit run from a checkout
# (STAGE0_KIT_FROM_CHECKOUT=1) stays where it is.
deploy=''
[[ $ROOT == "$LIVE" ]] && deploy=1 && units+=(ortus-stage0-deploy.service ortus-stage0-deploy.timer)
for unit in "${units[@]}"; do
  sed -e "s|@KIT@|$KIT|g" -e "s|@STAGE0_HOME@|$STAGE0_HOME|g" "$KIT/units/$unit" > "$UNITS/$unit"
done
# The stage-1 edge and its tunnels are gone: every worker reaches Sortie
# through Pomerium (retire-edge.sh, design D18).
for unit in $(retire_edge "$UNITS"); do say "Retired $unit: every worker reaches Sortie through Pomerium now"; done
systemctl --user daemon-reload
if [[ $deploy ]]; then
  systemctl --user enable -q --now ortus-stage0-deploy.timer
else
  systemctl --user disable -q --now ortus-stage0-deploy.timer 2>/dev/null || true
fi
systemctl --user enable -q ortus-stage0-sortie.service ortus-stage0-workbench.service ortus-stage0-nono.service \
  ortus-stage0-nono-live-proof.service ortus-stage0-egress-gate.service ortus-stage0-pomerium.service ortus-stage0-worker.service
systemctl --user enable -q --now ortus-stage0-token.timer
# The store follows its upstream from now on; no person pulls it.
systemctl --user enable -q --now ortus-stage0-store.timer
# The ui stories' previews follow their pull requests (run-previews.sh).
systemctl --user enable -q --now ortus-stage0-previews.timer
# The supervisor's live-proof requests become jobs (run-live-proofs.sh).
systemctl --user enable -q --now ortus-stage0-live-proofs.timer
# Sortie restarts at once, whatever runs: it re-adopts each run whose lease a
# worker still holds (spec store agent-platform/run-supervision); while it
# restarts, Pomerium and the egress gate answer the workers 503, which they
# retry. nono, the egress gate, Pomerium and the worker daemon carry the
# runs, so each restarts only when what it runs changed (unit-restart.sh): a
# kit change that leaves them as they are cuts no run's egress, stream or
# machine.
systemctl --user restart ortus-stage0-sortie.service
# The deploy state lists ortusnova/sortie at the commit of the Sortie that
# runs now (deploy-state.sh).
deploy_sortie_ran "$sortie_built"
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_SORTIE_PORT/readyz" && break; sleep 1; done
nono_inputs=("$STAGE0_HOME/bin/nono" "$KIT/run-nono.sh" "$KIT/egress-credentials.sh" "$KIT/anthropic-token.py"
  "$ROOT/control-plane/nono/egress-profile.json" "$STAGE0_HOME/egress-ca" "$STAGE0_HOME/nono-pass" "$STAGE0_HOME/github-token" "$ENV_FILE")
restart_changed ortus-stage0-nono.service "${nono_inputs[@]}" "$UNITS/ortus-stage0-nono.service"
# The live-proof nono also runs stage0-live-proofs (its source, not its
# binary, which Go stamps with the kit's commit) on egress-live-proof.json.
restart_changed ortus-stage0-nono-live-proof.service "${nono_inputs[@]}" "$KIT/egress-live-proof.json" \
  "$KIT/live-proofs" "$UNITS/ortus-stage0-nono-live-proof.service"
# The egress gate and Pomerium: their source or image, their configuration
# and their units, not the binaries, which Go stamps with the kit's commit.
# The gate's are everything it is built from and every file it is started
# with (run-egress-gate.sh), so no deploy leaves it on an earlier
# configuration (ONS-123).
restart_changed ortus-stage0-egress-gate.service "$ROOT/control-plane/internal/egress" "$ROOT/control-plane/cmd/cp-egress" \
  "$ROOT/control-plane/go.mod" "$KIT/run-egress-gate.sh" "$KIT/front-config.sh" "$ENV_FILE" \
  "$STAGE0_HOME/sortie-token" "$STAGE0_HOME/nono-pass" "$STAGE0_PREVIEW_REVIEW_KEY" "$UNITS/ortus-stage0-egress-gate.service"
restart_changed ortus-stage0-pomerium.service "$(pomerium_image)" "$STAGE0_CP_HOME/pomerium" "$STAGE0_CP_HOME/secrets/cp-pomerium-cert" \
  "$KIT/run-pomerium.sh" "$KIT/front-config.sh" "$UNITS/ortus-stage0-pomerium.service"
restart_worker
systemctl --user restart ortus-stage0-workbench.service
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_WORKBENCH_PORT/api/auth/mode" && break; sleep 1; done
curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_WORKBENCH_PORT/api/auth/mode" || die "the workbench did not start: journalctl --user -u ortus-stage0-workbench"
for unit in ortus-stage0-nono ortus-stage0-nono-live-proof ortus-stage0-egress-gate ortus-stage0-pomerium ortus-stage0-worker; do
  systemctl --user is-active -q "$unit.service" || die "$unit did not start: journalctl --user -u $unit"
done
say "Ready: workbench http://127.0.0.1:$STAGE0_WORKBENCH_PORT (password in $STAGE0_HOME/workbench-password), Sortie 127.0.0.1:$STAGE0_SORTIE_PORT, Huly $STAGE0_HULY_URL/workbench/$STAGE0_HULY_WORKSPACE/tracker, Pomerium $STAGE0_CP_PUBLIC_URL for $(front_workers | wc -l) worker(s), p1's own and each worker host's (dev/stage0/worker-host.sh list)"
