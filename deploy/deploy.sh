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
# Two profiles, chosen by ORTUS_NOVA_PROFILE on the first run (then kept in
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
#                      ORTUS_NOVA_PROFILE=worker, which stops its Sortie and
#                      removes its unit.
#
#   deploy/deploy.sh ENV                  on the control host: deploy this checkout's
#                                         commit to every host of ENV (push.sh)
#   deploy/deploy.sh ENV --commit SHA     deploy SHA
#   deploy/deploy.sh ENV --check          each host's commit and status beside the target
#   deploy/deploy.sh ENV --this-host      one host's step, from its live kit (bootstrap.sh
#                                         runs it); with --commit, refuses another commit
#   ... --rebuild-pack                    also rebuild the smolvm packs, which are
#                                         rebuilt by themselves when their inputs change
#   ... --update-sortie                   also move the Sortie checkout to origin/plan-bench
#
# Every step checks first, so a second run changes nothing but a restart.
# Settings live in $ORTUS_NOVA_HOME/stage0.env, written on the first run (edit it,
# then run this again). State, tokens and workspaces live in $ORTUS_NOVA_HOME.
#
# Needs (control): smolvm 1.32.4 or later, podman, Go, uv, npm, the openspec CLI, gh signed in,
# a live Claude subscription login (claude setup-token, opencode or Claude Code), Huly running
# (HULY_COMPOSE_DIR) and the host's opencode signed in to Anthropic for
# planning sessions and the supervisor.
# Needs (worker): smolvm 1.32.4 or later, Go, git, curl and p1's Pomerium; for
# homelab jobs, python3 and homelab's dev host (`make dev-host` in homelab).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
KIT=$ROOT/deploy
say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'deploy.sh: %s\n' "$*" >&2; exit 1; }
secret() { [[ -s $1 ]] || { (umask 077; openssl rand -hex 24 > "$1"); }; }
usage() { echo "usage: $0 ENV [--commit COMMIT] [--check | --this-host] [--rebuild-pack] [--update-sortie]" >&2; exit 2; }
[[ $# -gt 0 && $1 != -* ]] || usage
ENV_NAME=$1
shift
REBUILD_PACK='' UPDATE_SORTIE='' THIS_HOST_ONLY='' CHECK='' COMMIT='' PASS=()
while (( $# )); do
  case $1 in
    --rebuild-pack) REBUILD_PACK=1 PASS+=("$1") ;;
    --update-sortie) UPDATE_SORTIE=1 PASS+=("$1") ;;
    --this-host) THIS_HOST_ONLY=1 ;;
    --check) CHECK=1 ;;
    --commit) [[ $# -gt 1 ]] || usage; COMMIT=$2; shift ;;
    *) usage ;;
  esac
  shift
done
[[ -z $CHECK || -z $THIS_HOST_ONLY ]] || usage
# shellcheck source=names.sh
. "$KIT/names.sh"

# The environment (design D2, D3): its file, read through the loader only;
# this host is the one hostname -s names, and must be one of its hosts.
# ORTUS_NOVA_ENVIRONMENTS and ORTUS_NOVA_ENVFILE, for the tests, are another
# directory of environment files and a built loader.
ENVIRONMENTS=${ORTUS_NOVA_ENVIRONMENTS:-$KIT/environments}
ENVFILE=${ORTUS_NOVA_ENVFILE:-}
if [[ -z $ENVFILE ]]; then
  ENVFILE=${XDG_CACHE_HOME:-$HOME/.cache}/ortus-nova/envfile
  mkdir -p "${ENVFILE%/*}"
  (cd "$KIT/envfile" && go build -o "$ENVFILE" .) || die "could not build the environment loader (deploy/envfile)"
fi
THIS_HOST=$(hostname -s | tr '[:upper:]' '[:lower:]')
values=$("$ENVFILE" values -host "$THIS_HOST" "$ENVIRONMENTS/$ENV_NAME.yml") || die "$ENVIRONMENTS/$ENV_NAME.yml: see above"
eval "$values"
for pair in $ORTUS_NOVA_ENV_HOST_SSH; do
  dest=${pair#*=}
  getent hosts "${dest#*@}" > /dev/null \
    || die "the host ${pair%%=*} of environment $ENV_NAME ($ORTUS_NOVA_ENV_FILE) does not resolve on $THIS_HOST (${dest#*@}): a typo, or a retired host"
done
[[ " $ORTUS_NOVA_ENV_HOST_RUNS " == *" worker "* ]] \
  || die "$THIS_HOST runs control without worker in $ORTUS_NOVA_ENV_FILE: the kit runs a worker on its control host"
env_names "$ENV_NAME"
export ORTUS_NOVA_HOME
WANT_PROFILE=worker
[[ " $ORTUS_NOVA_ENV_HOST_RUNS " == *" control "* ]] && WANT_PROFILE=control
# What a stage-0 install ran must be what the environment file says it
# runs (design D6); checked before anything is written.
if [[ -n $ORTUS_NOVA_STAGE0 ]]; then
  settings_profile=$(env_settings; echo "${ORTUS_NOVA_PROFILE:-control}")
  [[ $settings_profile == homelab || $settings_profile == "$WANT_PROFILE" ]] \
    || die "$ORTUS_NOVA_HOME/$ORTUS_NOVA_SETTINGS_FILE says $(setting PROFILE)=$settings_profile, and $ORTUS_NOVA_ENV_FILE says $THIS_HOST runs $ORTUS_NOVA_ENV_HOST_RUNS ($WANT_PROFILE)"
fi
[[ -n $ORTUS_NOVA_STAGE0 ]] \
  && say "This host still has its stage-0 names ($ORTUS_NOVA_HOME); deploy/deploy.sh $ENV_NAME --migrate-stage0 on the control host migrates it"
# shellcheck source=deploy-state.sh
. "$KIT/deploy-state.sh"

# The control host deploys the environment (design D3): one commit, to
# itself first and then to each other host, each by its own --this-host.
if [[ -z $THIS_HOST_ONLY ]]; then
  [[ " $ORTUS_NOVA_ENV_HOST_RUNS " == *" control "* ]] \
    || die "$THIS_HOST does not run control in environment $ENV_NAME: run deploy/deploy.sh $ENV_NAME on $ORTUS_NOVA_ENV_CONTROL_HOST"
  # shellcheck source=push.sh
  . "$KIT/push.sh"
  COMMIT=$(push_commit "$COMMIT") || die "the commit to deploy: see above"
  if [[ -n $CHECK ]]; then
    push_check "$COMMIT"
    exit
  fi
  push_deploy "$COMMIT" "${PASS[@]}"
  exit
fi

# One step at a time on a host: a second one, or the dev-host apply, waits
# for this one. The apply takes the lock itself and runs this under it
# (ORTUS_NOVA_LOCKED=1).
if [[ ${ORTUS_NOVA_LOCKED:-} != 1 ]]; then
  mkdir -p "$ORTUS_NOVA_HOME"
  exec 9> "$ORTUS_NOVA_HOME/$ORTUS_NOVA_LOCK_FILE"
  if ! flock -n 9; then
    say "Waiting for another deploy.sh or a deploy to finish ($ORTUS_NOVA_HOME/$ORTUS_NOVA_LOCK_FILE)"
    flock 9
  fi
  export ORTUS_NOVA_LOCKED=1
fi

# A host's step (--this-host) runs from its live kit, which bootstrap.sh
# moved to the commit of the push; what runs is what the push chose,
# whatever a developer has checked out. ORTUS_NOVA_KIT_FROM_CHECKOUT=1 runs
# this checkout instead, to try a branch on one host.
LIVE=$ORTUS_NOVA_HOME/kit
if [[ ${ORTUS_NOVA_KIT_FROM_CHECKOUT:-} != 1 && $ROOT != "$LIVE" ]]; then
  die "--this-host runs from the live kit ($LIVE): run deploy/deploy.sh $ENV_NAME on $ORTUS_NOVA_ENV_CONTROL_HOST, which deploys every host"
fi
HEAD_COMMIT=$(git -C "$ROOT" rev-parse -q --verify HEAD 2> /dev/null || true)
[[ -z $COMMIT || $COMMIT == "$HEAD_COMMIT" ]] || die "the kit at $ROOT holds ${HEAD_COMMIT:0:12}, not ${COMMIT:0:12}"
deploy_state_write deploying "$HEAD_COMMIT" "${COMMIT:-$HEAD_COMMIT}"
# deploy_applied: the deploy applied once every unit whose configuration
# changed has restarted and runs (unit-restart.sh), the egress gate among
# them (ONS-123); the host's deploy state says succeeded then.
deploy_applied() {
  local unsettled
  unsettled=$(units_unsettled | paste -sd, -)
  [[ -z $unsettled ]] || die "$unsettled did not run after its restart"
  deploy_state_write succeeded "$HEAD_COMMIT" "$HEAD_COMMIT"
}
# retire_deploy_timer: no host follows main by itself any more (design D3):
# the deploy timer and service an earlier kit installed go, under either
# set of names.
retire_deploy_timer() {
  local part
  for part in deploy.timer deploy.service; do
    [[ -e $UNITS/$(unit "$part") ]] || continue
    systemctl --user disable -q --now "$(unit "$part")" 2> /dev/null || true
    rm -f "$UNITS/$(unit "$part")"
    say "Retired $(unit "$part"): the control host deploys this host now"
  done
}

mkdir -p "$ORTUS_NOVA_HOME"/bin
chmod 700 "$ORTUS_NOVA_HOME"
# The environment's values, for the scripts the units run (names.sh
# env_values), and the record of which environment this home is.
printf '%s\n' "$values" > "$ORTUS_NOVA_HOME/environment.env"
# The units' scripts run with the PATH of the last run, recorded here.
printf '%s\n' "$PATH" > "$ORTUS_NOVA_HOME/path"
ENV_FILE=$ORTUS_NOVA_HOME/$ORTUS_NOVA_SETTINGS_FILE
# The homelab profile, a second Sortie on taichi, is retired (spec store
# agent-platform/one-sortie): its host becomes a worker once p1's Sortie
# dispatches homelab. The move stops that Sortie; the environment file says
# the host is a worker.
if [[ -s $ENV_FILE ]] && grep -qx "$(setting PROFILE)=homelab" "$ENV_FILE"; then
  [[ $WANT_PROFILE == worker ]] \
    || die "the homelab profile is retired, and $ORTUS_NOVA_ENV_FILE says $THIS_HOST runs control"
  say "Moving $ENV_FILE from the homelab profile to the worker profile"
  sed -i "s/^$(setting PROFILE)=homelab\$/$(setting PROFILE)=worker/" "$ENV_FILE"
fi
if [[ ! -s $ENV_FILE && $WANT_PROFILE == worker ]]; then
  say "Writing the default worker settings to $ENV_FILE"
  (umask 077; cat > "$ENV_FILE" <<EOF
# This worker host's settings in environment $ENV_NAME (deploy/deploy.sh). Run deploy.sh again after an edit.
ORTUS_NOVA_KIT_ROOT=$ROOT
# Jobs at once (the daemon's run_slots): unset, as many as this host's CPUs
# and memory hold (worker-config.sh).
ORTUS_NOVA_WORKER_SLOTS=
# The operator's homelab checkout, for homelab jobs: the kit adds its own
# detached worktree of origin/master under \$ORTUS_NOVA_HOME/homelab and builds the
# homelab pack from it. Without it, this worker takes no homelab job.
ORTUS_NOVA_HOMELAB_SRC=$HOME/dev/homelab
ORTUS_NOVA_HOMELAB_REPO_BASE=master
EOF
  )
fi
if [[ ! -s $ENV_FILE ]]; then
  say "Writing the default settings to $ENV_FILE"
  (umask 077; cat > "$ENV_FILE" <<EOF
# This control host's settings in environment $ENV_NAME (deploy/deploy.sh). Run deploy.sh again after an edit.
# Ports, the tracker and the local name are the environment's ($ORTUS_NOVA_ENV_FILE).
ORTUS_NOVA_KIT_ROOT=$ROOT
ORTUS_NOVA_SORTIE_SRC=$HOME/dev/sortie-worktrees/$ENV_NAME
ORTUS_NOVA_STORE=$HOME/dev/ortus-nova-specs
ORTUS_NOVA_HULY_EMAIL=sortie@example.com
ORTUS_NOVA_HULY_PASSWORD=sortie-dev-pass
ORTUS_NOVA_REPO_URL=https://github.com/ortusnova/sortie.git
ORTUS_NOVA_REPO_BASE=plan-bench
ORTUS_NOVA_REPO_PROJECT=ortusnova/sortie
# Other repositories (WORKFLOW.md), each dispatched here too.
ORTUS_NOVA_AO_REPO_URL=https://github.com/ortusnova/agentic-orchestration.git
ORTUS_NOVA_AO_REPO_BASE=main
ORTUS_NOVA_AO_REPO_PROJECT=ortusnova/agentic-orchestration
ORTUS_NOVA_HOMELAB_REPO_URL=https://github.com/ortusnova/homelab.git
ORTUS_NOVA_HOMELAB_REPO_BASE=master
ORTUS_NOVA_HOMELAB_REPO_PROJECT=ortusnova/homelab
# This host's own worker's jobs at once: unset, as many as its CPUs and memory
# hold (worker-config.sh); each VM takes 4 vCPU and 8 GiB (WORKFLOW.md).
ORTUS_NOVA_WORKER_SLOTS=
# Sortie's cap on agents at once, on every worker together: unset, a ceiling
# above the workers' slots, which the matcher holds (worker-config.sh).
ORTUS_NOVA_SORTIE_MAX_AGENTS=
# The control plane's front (README.md "Worker hosts"): Pomerium's public
# URL, which every worker dials and whose host must resolve to this host on
# each of them.
ORTUS_NOVA_CP_PUBLIC_URL=https://$(hostname -f):$ORTUS_NOVA_ENV_FRONT_PORT
# The operator's channel for every question (README.md): slack, discord, telegram or
# webhook, with its credential in \$ORTUS_NOVA_HOME/notify-credential, mode 0600; and
# where people reach the workbench, for the link in each question.
ORTUS_NOVA_NOTIFY_KIND=
ORTUS_NOVA_NOTIFY_CHAT_ID=
ORTUS_NOVA_WORKBENCH_PUBLIC_URL=http://127.0.0.1:$ORTUS_NOVA_ENV_WORKBENCH_PORT
# The previews of review: ui stories' pull requests (run-previews.sh): the
# live workbench's TLS name, which each preview's name for people sits
# beside (empty: none; the address the review opens is always a name under
# Pomerium's, README.md), and a command to run when
# \$ORTUS_NOVA_HOME/previews/routes changes, such as a proxy reload.
ORTUS_NOVA_WORKBENCH_URL=
ORTUS_NOVA_PREVIEW_ROUTES_HOOK=
EOF
  )
fi
env_settings
env_values
ORTUS_NOVA_PROFILE=$WANT_PROFILE
ORTUS_NOVA_PACK=${ORTUS_NOVA_PACK:-$ORTUS_NOVA_HOME/$ORTUS_NOVA_PACK_FILE}
# Pomerium's public URL, which every worker dials: on p1 it stays as the
# first run that knew it wrote it to stage0.env; a worker host has it from
# worker-host.sh add (control-plane-url), unless its stage0.env says
# otherwise.
WORKER_HOST_CONFIG=$HOME/$ORTUS_NOVA_WORKER_HOST_CONFIG
# A worker host that the stage-1 edge served gets its certificate from p1's
# deploy.sh, which runs from the same merge (carry_edge_workers in
# retire-edge.sh). The live kit, which the deploy timer runs, waits for it
# (ORTUS_NOVA_WORKER_CERT_WAIT seconds); a kit run by hand does not.
if [[ $ORTUS_NOVA_PROFILE == worker && -z ${ORTUS_NOVA_CP_PUBLIC_URL:-} ]]; then
  cert_wait=${ORTUS_NOVA_WORKER_CERT_WAIT:-0}
  [[ $ROOT == "$LIVE" ]] && cert_wait=${ORTUS_NOVA_WORKER_CERT_WAIT:-1800}
  [[ $cert_wait =~ ^[0-9]+$ ]] || die "ORTUS_NOVA_WORKER_CERT_WAIT is $cert_wait; want seconds"
  # shellcheck source=retire-edge.sh
  . "$KIT/retire-edge.sh"
  wait_for_worker_files "$WORKER_HOST_CONFIG" "$cert_wait" \
    || die "p1 has not given this host its certificate in $cert_wait s ($WORKER_HOST_CONFIG): on $ORTUS_NOVA_ENV_CONTROL_HOST, run deploy/worker-host.sh $ENV_NAME add $THIS_HOST"
fi
if [[ -z ${ORTUS_NOVA_CP_PUBLIC_URL:-} ]]; then
  if [[ $ORTUS_NOVA_PROFILE == worker ]]; then
    ORTUS_NOVA_CP_PUBLIC_URL=$(cat "$WORKER_HOST_CONFIG/control-plane-url" 2>/dev/null || true)
    [[ -n $ORTUS_NOVA_CP_PUBLIC_URL ]] || die "$WORKER_HOST_CONFIG/control-plane-url is missing: on $ORTUS_NOVA_ENV_CONTROL_HOST, run deploy/worker-host.sh $ENV_NAME add $THIS_HOST"
  else
    ORTUS_NOVA_CP_PUBLIC_URL=https://$(hostname -f):$ORTUS_NOVA_ENV_FRONT_PORT
    say "Pomerium's public URL is $ORTUS_NOVA_CP_PUBLIC_URL ($(setting CP_PUBLIC_URL) in $ENV_FILE); every worker host must resolve its host to $THIS_HOST"
    printf '# Pomerium'"'"'s public URL, which every worker dials (README.md "Worker hosts").\n%s=%s\n' "$(setting CP_PUBLIC_URL)" "$ORTUS_NOVA_CP_PUBLIC_URL" >> "$ENV_FILE"
  fi
fi
# The worker's name is the host's machine id in the environment file
# (design D2), the name of the certificate worker-host.sh add gave it.
ORTUS_NOVA_WORKER_NAME=$ORTUS_NOVA_ENV_HOST
ORTUS_NOVA_AO_REPO_URL=${ORTUS_NOVA_AO_REPO_URL:-https://github.com/ortusnova/agentic-orchestration.git}
ORTUS_NOVA_AO_REPO_BASE=${ORTUS_NOVA_AO_REPO_BASE:-main}
ORTUS_NOVA_AO_REPO_PROJECT=${ORTUS_NOVA_AO_REPO_PROJECT:-ortusnova/agentic-orchestration}
ORTUS_NOVA_HOMELAB_REPO_URL=${ORTUS_NOVA_HOMELAB_REPO_URL:-https://github.com/ortusnova/homelab.git}
ORTUS_NOVA_HOMELAB_REPO_BASE=${ORTUS_NOVA_HOMELAB_REPO_BASE:-master}
ORTUS_NOVA_HOMELAB_REPO_PROJECT=${ORTUS_NOVA_HOMELAB_REPO_PROJECT:-ortusnova/homelab}
if [[ $ORTUS_NOVA_KIT_ROOT != "$ROOT" ]]; then
  say "The units now run the kit from $ROOT, not $ORTUS_NOVA_KIT_ROOT ($ENV_FILE)"
  sed -i "s|^$(setting KIT_ROOT)=.*|$(setting KIT_ROOT)=$ROOT|" "$ENV_FILE"
  ORTUS_NOVA_KIT_ROOT=$ROOT
fi
export ORTUS_NOVA_PROFILE ORTUS_NOVA_HOME ORTUS_NOVA_HULY_URL ORTUS_NOVA_HULY_WORKSPACE ORTUS_NOVA_HULY_PROJECT ORTUS_NOVA_SORTIE_PORT \
  ORTUS_NOVA_PACK ORTUS_NOVA_REPO_URL ORTUS_NOVA_REPO_BASE ORTUS_NOVA_REPO_PROJECT \
  ORTUS_NOVA_AO_REPO_URL ORTUS_NOVA_AO_REPO_BASE ORTUS_NOVA_AO_REPO_PROJECT \
  ORTUS_NOVA_HOMELAB_REPO_URL ORTUS_NOVA_HOMELAB_REPO_BASE ORTUS_NOVA_HOMELAB_REPO_PROJECT ORTUS_NOVA_CP_PUBLIC_URL

if [[ $ORTUS_NOVA_PROFILE == worker ]]; then
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
# shellcheck source=local-name.sh
. "$KIT/local-name.sh"
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
# shellcheck source=logs.sh
. "$KIT/logs.sh"
UNITS=${ORTUS_NOVA_UNITS:-$HOME/.config/systemd/user}
# The worker daemon's machines carry the runs: it restarts only when its
# binary, its configuration or its unit changed (worker-build.sh).
# shellcheck source=worker-build.sh
. "$KIT/worker-build.sh"

# The pack is built again when its inputs change (the kit's pack/,
# smol-builder's source, which embeds its layers, and smolvm), so a merged
# change to the agent's config reaches the next machine with the next push.
# Not the smol-builder binary: Go stamps it with the kit's commit.
build_pack() {
  local pack_inputs
  pack_inputs=$({
    (cd "$ROOT" && find deploy/pack smol-builder -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
    smolvm --version
  } | sha256sum | cut -c1-16)
  if [[ -n $REBUILD_PACK || ! -s $ORTUS_NOVA_PACK || $(cat "$ORTUS_NOVA_PACK.inputs" 2>/dev/null) != "$pack_inputs" ]]; then
    say "Building the pack $ORTUS_NOVA_PACK ($pack_inputs)"
    smol-builder build --repo "$KIT/pack" --output "$ORTUS_NOVA_PACK.tmp" > "$ORTUS_NOVA_HOME/pack-report.json" \
      || { rm -f "$ORTUS_NOVA_PACK.tmp"; die "the pack did not build: $ORTUS_NOVA_HOME/pack-report.json"; }
    mv "$ORTUS_NOVA_PACK.tmp" "$ORTUS_NOVA_PACK"
    echo "$pack_inputs" > "$ORTUS_NOVA_PACK.inputs"
  fi
}

if [[ $ORTUS_NOVA_PROFILE == worker ]]; then
  # A worker host holds no credential (spec store agent-platform/stage-1-edge):
  # its jobs' model and forge requests get theirs from nono on p1.
  for f in github-token claude-oauth-token; do
    [[ -e $ORTUS_NOVA_HOME/$f ]] && say "$ORTUS_NOVA_HOME/$f is no longer used on a worker host: delete it, and revoke the token"
  done
  ORTUS_NOVA_EGRESS_CA=$WORKER_HOST_CONFIG/egress-ca.crt
  # The client certificate from the control plane's CA, its key and that
  # CA (worker-host.sh add, on p1), which Pomerium admits this worker by.
  worker_tls=("$WORKER_HOST_CONFIG/cp-worker.crt" "$WORKER_HOST_CONFIG/cp-worker.key" "$WORKER_HOST_CONFIG/cp-ca.crt")
  for f in "$ORTUS_NOVA_EGRESS_CA" "${worker_tls[@]}"; do
    [[ -s $f ]] || die "$f is missing: on $ORTUS_NOVA_ENV_CONTROL_HOST, run deploy/worker-host.sh $ENV_NAME add $THIS_HOST"
  done
  say "Building worker-daemon and smol-builder into $ORTUS_NOVA_HOME/bin"
  build_worker_daemon
  (cd "$ROOT/smol-builder" && go build -o "$ORTUS_NOVA_HOME/bin/smol-builder" ./cmd/smol-builder)
  export PATH=$ORTUS_NOVA_HOME/bin:$PATH
  build_pack
  # A worker host that offers nested virtualization (taichi) declares nested,
  # with the live-proof pack the nested profile runs in (WORKFLOW.md).
  ORTUS_NOVA_WORKER_NESTED=false
  if host_nested; then
    ORTUS_NOVA_WORKER_NESTED=true
    # shellcheck source=live-proof-pack.sh
    . "$KIT/live-proof-pack.sh"
    live_proof_pack
  else
    say "This host offers no nested virtualization: its worker takes no homelab job and no live proof of the nested profile"
  fi
  if [[ -n ${ORTUS_NOVA_HOMELAB_SRC:-} && -d $ORTUS_NOVA_HOMELAB_SRC ]]; then
    command -v python3 > /dev/null || die "python3 is not on PATH; homelab's inputs need it"
    # shellcheck source=homelab-inputs.sh
    . "$KIT/homelab-inputs.sh"
    homelab_inputs
  else
    say "No homelab checkout (ORTUS_NOVA_HOMELAB_SRC): this worker takes no homelab job"
  fi
  render_worker_config "$ORTUS_NOVA_HOME/worker.json" "$ORTUS_NOVA_WORKER_NAME" "$ORTUS_NOVA_CP_PUBLIC_URL" "$ORTUS_NOVA_EGRESS_CA" "${worker_tls[@]}"
  "$ORTUS_NOVA_HOME/bin/worker-daemon" check -config "$ORTUS_NOVA_HOME/worker.json" > /dev/null \
    || die "the worker daemon refused $ORTUS_NOVA_HOME/worker.json"

  retire_sortie "$UNITS" || die "this host's Sortie keeps running until p1's Sortie holds its merge records (see above)"
  # It runs no Sortie, so its deploy state lists no ortusnova/sortie.
  deploy_sortie_retired
  say "Installing the user units"
  mkdir -p "$UNITS"
  render_unit "$KIT" worker.service > "$UNITS/$(unit worker.service)"
  retire_deploy_timer
  systemctl --user daemon-reload
  systemctl --user enable -q "${ORTUS_NOVA_UNIT_PREFIX}worker.service"
  # Every stage-0 unit's journal, into rotated files (logs.sh).
  logs_install "$UNITS" || die "the log files' units did not start: systemctl --user status ${ORTUS_NOVA_UNIT_PREFIX}logs"
  restart_worker
  sleep 2
  systemctl --user is-active -q "${ORTUS_NOVA_UNIT_PREFIX}worker.service" || die "the worker daemon did not start: journalctl --user -u ${ORTUS_NOVA_UNIT_PREFIX}worker"
  # Pomerium answers a listed worker's certificate on its control route;
  # it rejects a connection without one in the handshake (000).
  code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "${worker_tls[2]}" --cert "${worker_tls[0]}" --key "${worker_tls[1]}" \
    "$ORTUS_NOVA_CP_PUBLIC_URL/api/v1/workers/" || true)
  [[ $code != 000 && $code != 403 && -n $code ]] \
    || say "p1's Pomerium does not admit this worker at $ORTUS_NOVA_CP_PUBLIC_URL yet (HTTP $code): on $ORTUS_NOVA_ENV_CONTROL_HOST, run deploy/worker-host.sh $ENV_NAME add $THIS_HOST"
  deploy_applied
  say "Ready: worker $ORTUS_NOVA_WORKER_NAME dials p1's Pomerium at $ORTUS_NOVA_CP_PUBLIC_URL (journalctl --user -u ${ORTUS_NOVA_UNIT_PREFIX}worker)"
  exit 0
fi

# The operator's channel (notify-channel.sh): optional; with no
# ORTUS_NOVA_NOTIFY_KIND the questions show on the workbench only.
ORTUS_NOVA_NOTIFY_KIND=${ORTUS_NOVA_NOTIFY_KIND:-}
ORTUS_NOVA_NOTIFY_ENTRY=$(notify_entry "$ORTUS_NOVA_NOTIFY_KIND" "${ORTUS_NOVA_NOTIFY_CHAT_ID:-}") || die "fix the channel in $ENV_FILE"
export ORTUS_NOVA_NOTIFY_ENTRY
if [[ -n $ORTUS_NOVA_NOTIFY_KIND ]]; then
  notify_credential "$ORTUS_NOVA_HOME/notify-credential" || die "the $ORTUS_NOVA_NOTIFY_KIND channel has no credential"
else
  say "No operator channel of your own: Sortie's notifications go to the workbench only. Set ORTUS_NOVA_NOTIFY_KIND in $ENV_FILE to add one (README.md)"
fi
mkdir -p "$ORTUS_NOVA_HOME"/{sortie,workspaces,home,sessions}
gh auth token >/dev/null || die "gh has no token: run gh auth login"
ORTUS_NOVA_HOME=$ORTUS_NOVA_HOME "$KIT/anthropic-token.py" >/dev/null \
  || die "no live Claude subscription token: run claude setup-token into $ORTUS_NOVA_HOME/claude-oauth-token"

if [[ ! -d $ORTUS_NOVA_SORTIE_SRC ]]; then
  say "Creating the Sortie checkout $ORTUS_NOVA_SORTIE_SRC (ortusnova/sortie plan-bench)"
  git clone -q --branch plan-bench https://github.com/ortusnova/sortie.git "$ORTUS_NOVA_SORTIE_SRC"
fi
if [[ -n $UPDATE_SORTIE ]]; then
  say "Moving $ORTUS_NOVA_SORTIE_SRC to origin/plan-bench"
  git -C "$ORTUS_NOVA_SORTIE_SRC" fetch -q origin plan-bench
  git -C "$ORTUS_NOVA_SORTIE_SRC" checkout -q --detach origin/plan-bench
fi
sortie_built=$(git -C "$ORTUS_NOVA_SORTIE_SRC" rev-parse HEAD)
say "Building sortie ${sortie_built:0:7}, acp-broker, smol-builder and worker-daemon into $ORTUS_NOVA_HOME/bin"
(cd "$ORTUS_NOVA_SORTIE_SRC" && GOWORK=off go build -o "$ORTUS_NOVA_HOME/bin/sortie" ./cmd/sortie)
(cd "$ROOT/acp-broker" && go build -o "$ORTUS_NOVA_HOME/bin/acp-broker" ./cmd/acp-broker)
(cd "$ROOT/smol-builder" && go build -o "$ORTUS_NOVA_HOME/bin/smol-builder" ./cmd/smol-builder)
build_worker_daemon
export PATH=$ORTUS_NOVA_HOME/bin:$PATH
# shellcheck source=nono-version.sh
. "$KIT/nono-version.sh"
say "Building control-plane's cpctl and egress gate and live-proofs, and installing nono $NONO_VERSION (pinned by checksum) into $ORTUS_NOVA_HOME/bin"
(cd "$ROOT/control-plane" && CGO_ENABLED=0 go build -o "$ORTUS_NOVA_HOME/bin/" ./cmd/cpctl ./cmd/cp-egress)
(cd "$KIT/live-proofs" && go build -o "$ORTUS_NOVA_HOME/bin/live-proofs" .)
nono_install "$ORTUS_NOVA_HOME/bin" || die "could not install nono $NONO_VERSION"

say "Installing workbench dependencies and building the frontend"
(cd "$ROOT/workbench" && uv sync -q)
(cd "$ROOT/workbench/frontend" && { [[ -d node_modules ]] || npm install --silent; } && npx vite build --logLevel warn)

HULY_DIR=${HULY_COMPOSE_DIR:-$HOME/dev/huly-selfhost}
# Every Huly container runs and restarts unless stopped, the policy in the
# compose files themselves, so a `compose up` by anyone keeps it; a front
# end that answers is not enough (huly-restart-policy.sh).
if [[ -f $HULY_DIR/compose.yml ]]; then
  huly_ensure "$HULY_DIR" || die "could not start Huly in $HULY_DIR"
elif ! curl -sf -o /dev/null "$ORTUS_NOVA_HULY_URL/config.json"; then
  die "Huly is not running and $HULY_DIR holds no compose project; set HULY_COMPOSE_DIR"
fi
for _ in $(seq 60); do curl -sf -o /dev/null "$ORTUS_NOVA_HULY_URL/config.json" && break; sleep 2; done
# podman honors the restart policy after a reboot only through this user unit.
systemctl --user is-enabled -q podman-restart.service || { say "Enabling podman-restart.service"; systemctl --user enable -q podman-restart.service; }
say "Preparing Huly project $ORTUS_NOVA_HULY_PROJECT"
python3 "$ROOT/dev/huly_project.py" "$ORTUS_NOVA_HULY_URL" "$ORTUS_NOVA_HULY_WORKSPACE" "$ORTUS_NOVA_HULY_EMAIL" \
  "$ORTUS_NOVA_HULY_PASSWORD" "$ORTUS_NOVA_HULY_PROJECT" >/dev/null || die "could not prepare Huly project $ORTUS_NOVA_HULY_PROJECT"

secret "$ORTUS_NOVA_HOME/workbench-secret"
if [[ ! -s $ORTUS_NOVA_HOME/workbench-password-hash ]]; then
  secret "$ORTUS_NOVA_HOME/workbench-password"
  (cd "$ROOT/workbench" && umask 077 && uv run python -c 'import sys
from workbench.auth import hash_password
print(hash_password(open(sys.argv[1]).read().strip()))' "$ORTUS_NOVA_HOME/workbench-password" > "$ORTUS_NOVA_HOME/workbench-password-hash")
fi
secret "$ORTUS_NOVA_HOME/sortie-token"
# This host's token for the workbench's notifications (notify-channel.sh):
# Sortie posts with it, the workbench takes it, no person writes it.
notify_host=$(notify_host) || die "fix the host name in $ENV_FILE"
notify_token "$ORTUS_NOVA_HOME" "$notify_host" || die "could not mint $notify_host's notifications token"
secret "$ORTUS_NOVA_HOME/viewer-token"
# The supervisor's token and prompt (supervisor.sh), which the workflow's
# supervisor section reads.
# shellcheck source=supervisor.sh
. "$KIT/supervisor.sh"
supervisor_install "$ORTUS_NOVA_HOME" "$KIT" || die "could not install the supervisor"
# The egress gate reaches nono with this; nono intercepts TLS for the
# credential hosts with the egress CA, whose certificate each worker gets
# (worker-host.sh, worker-config.sh) and whose key never leaves this host.
secret "$ORTUS_NOVA_HOME/nono-pass"
# The egress gate gives a review job's requests to a ui story's preview this
# key, which signs the review in there (run-egress-gate.sh, run-workbench.sh).
secret "$ORTUS_NOVA_HOME/preview-review-key"
if [[ ! -s $ORTUS_NOVA_HOME/egress-ca/ca.key ]]; then
  say "Creating the egress CA in $ORTUS_NOVA_HOME/egress-ca"
  mkdir -p "$ORTUS_NOVA_HOME/egress-ca"
  (umask 077 && cd "$ORTUS_NOVA_HOME/egress-ca" \
    && openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out ca.key 2>/dev/null \
    && openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=Ortus Nova stage-0 egress CA" \
      -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign,digitalSignature" \
      -out ca.crt) || die "could not create the egress CA"
  chmod 644 "$ORTUS_NOVA_HOME/egress-ca/ca.crt"
fi
# The control plane's front (front-config.sh): the CA and Pomerium's
# certificate and configuration, and p1's own worker's certificate.
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
[[ -n $(pomerium_image) ]] || die "control-plane/compose.yaml pins no Pomerium image"
front_init || die "could not set up the control plane's CA and Pomerium's configuration in $ORTUS_NOVA_CP_HOME"
issue_local_worker "$ORTUS_NOVA_WORKER_NAME" || die "could not issue p1's own worker its certificate"
# Each worker host the stage-1 edge served gets its certificate now, before
# the edge and its tunnels are retired below: a host that cannot be reached
# stops this run with the edge still up.
# shellcheck source=retire-edge.sh
. "$KIT/retire-edge.sh"
carried=$(carry_edge_workers "$ORTUS_NOVA_HOME/workers" "$KIT/worker-host.sh" "$ENV_NAME" add) \
  || die "could not give each worker host of the stage-1 edge its certificate; the edge stays until it can"
for name in $carried; do say "Gave worker host $name its certificate: it dials Pomerium from its next deploy.sh"; done
# Sortie's health check reads this host's deploy state (deploy-state.sh);
# the push keeps each other host's beside it (push.sh). The minute copy's
# list of hosts is gone.
rm -rf "$ORTUS_NOVA_HOME/deploy-hosts"
mkdir -p "$ORTUS_NOVA_LIVE_PROOFS"

build_pack
ORTUS_NOVA_EGRESS_CA=$ORTUS_NOVA_HOME/egress-ca/ca.crt
# p1's worker never declares nested: only a worker host does (taichi), so a
# homelab job and a live proof of the nested profile never run on p1.
ORTUS_NOVA_WORKER_NESTED=false
if [[ -n ${ORTUS_NOVA_HOMELAB_SRC:-} && -d $ORTUS_NOVA_HOMELAB_SRC ]]; then
  say "p1's worker declares no nested, so it takes no homelab job: ORTUS_NOVA_HOMELAB_SRC is not used here; homelab runs on taichi"
fi
# p1's own worker dials Pomerium as every worker host does.
local_tls=$ORTUS_NOVA_CP_HOME/workers/$ORTUS_NOVA_WORKER_NAME
render_worker_config "$ORTUS_NOVA_HOME/worker.json" "$ORTUS_NOVA_WORKER_NAME" "$ORTUS_NOVA_CP_PUBLIC_URL" "$ORTUS_NOVA_EGRESS_CA" \
  "$local_tls/worker.crt" "$local_tls/worker.key" "$local_tls/ca.crt"
"$ORTUS_NOVA_HOME/bin/worker-daemon" check -config "$ORTUS_NOVA_HOME/worker.json" > /dev/null \
  || die "the worker daemon refused $ORTUS_NOVA_HOME/worker.json"

say "Rendering Sortie's workflow"
# shellcheck source=forge-app.sh
. "$KIT/forge-app.sh"
forge_app_key_ok "$ORTUS_NOVA_GITHUB_APP_KEY" || die "Sortie would not start"
export ORTUS_NOVA_EGRESS_GATE_PORT
ORTUS_NOVA_SORTIE_MAX_AGENTS=$(sortie_agent_cap) || die "the Sortie-wide agent cap is wrong ($ENV_FILE)"
export ORTUS_NOVA_SORTIE_MAX_AGENTS
ORTUS_NOVA_KIT=$KIT python3 "$ROOT/dev/forge/render.py" "$KIT/WORKFLOW.md" "$ORTUS_NOVA_HOME/sortie/WORKFLOW.md"
validated=$("$KIT/run-sortie.sh" validate 2>&1) || die "Sortie refused the workflow ($KIT/run-sortie.sh validate): $validated"
# A key Sortie does not know is one it ignores: the workflow would not do
# what it says, as when the remote kind ignored every repository but one.
if unknown=$(grep -iE 'unknown_[a-z_]*key' <<< "$validated"); then
  die "Sortie ignores a key of the workflow ($KIT/run-sortie.sh validate): $unknown"
fi

say "Installing the user units"
retire_huly_tunnels "$UNITS"
mkdir -p "$UNITS"
parts=(sortie.service workbench.service token.service token.timer store.service store.timer
  preview@.service previews.service previews.timer nono.service nono-live-proof.service live-proofs.service live-proofs.timer
  egress-gate.service pomerium.service worker.service)
for part in "${parts[@]}"; do
  render_unit "$KIT" "$part" > "$UNITS/$(unit "$part")"
done
# The stage-1 edge and its tunnels are gone: every worker reaches Sortie
# through Pomerium (retire-edge.sh, design D18).
for unit in $(retire_edge "$UNITS"); do say "Retired $unit: every worker reaches Sortie through Pomerium now"; done
retire_deploy_timer
systemctl --user daemon-reload
systemctl --user enable -q "${ORTUS_NOVA_UNIT_PREFIX}sortie.service" "${ORTUS_NOVA_UNIT_PREFIX}workbench.service" "${ORTUS_NOVA_UNIT_PREFIX}nono.service" \
  "${ORTUS_NOVA_UNIT_PREFIX}nono-live-proof.service" "${ORTUS_NOVA_UNIT_PREFIX}egress-gate.service" "${ORTUS_NOVA_UNIT_PREFIX}pomerium.service" "${ORTUS_NOVA_UNIT_PREFIX}worker.service"
# Every stage-0 unit's journal, into rotated files (logs.sh).
logs_install "$UNITS" || die "the log files' units did not start: systemctl --user status ${ORTUS_NOVA_UNIT_PREFIX}logs"
systemctl --user enable -q --now "${ORTUS_NOVA_UNIT_PREFIX}token.timer"
# The store follows its upstream from now on; no person pulls it.
systemctl --user enable -q --now "${ORTUS_NOVA_UNIT_PREFIX}store.timer"
# The ui stories' previews follow their pull requests (run-previews.sh).
systemctl --user enable -q --now "${ORTUS_NOVA_UNIT_PREFIX}previews.timer"
# The supervisor's live-proof requests become jobs (run-live-proofs.sh).
systemctl --user enable -q --now "${ORTUS_NOVA_UNIT_PREFIX}live-proofs.timer"
# Sortie restarts at once, whatever runs: it re-adopts each run whose lease a
# worker still holds (spec store agent-platform/run-supervision); while it
# restarts, Pomerium and the egress gate answer the workers 503, which they
# retry. nono, the egress gate, Pomerium and the worker daemon carry the
# runs, so each restarts only when what it runs changed (unit-restart.sh): a
# kit change that leaves them as they are cuts no run's egress, stream or
# machine.
systemctl --user restart "${ORTUS_NOVA_UNIT_PREFIX}sortie.service"
# The deploy state lists ortusnova/sortie at the commit of the Sortie that
# runs now (deploy-state.sh).
deploy_sortie_ran "$sortie_built"
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$ORTUS_NOVA_SORTIE_PORT/readyz" && break; sleep 1; done
nono_inputs=("$ORTUS_NOVA_HOME/bin/nono" "$KIT/run-nono.sh" "$KIT/egress-credentials.sh" "$KIT/anthropic-token.py"
  "$ROOT/control-plane/nono/egress-profile.json" "$ORTUS_NOVA_HOME/egress-ca" "$ORTUS_NOVA_HOME/nono-pass" "$ENV_FILE")
restart_changed "${ORTUS_NOVA_UNIT_PREFIX}nono.service" "${nono_inputs[@]}" "$UNITS/${ORTUS_NOVA_UNIT_PREFIX}nono.service"
# The live-proof nono also runs live-proofs (its source, not its
# binary, which Go stamps with the kit's commit) on egress-live-proof.json.
restart_changed "${ORTUS_NOVA_UNIT_PREFIX}nono-live-proof.service" "${nono_inputs[@]}" "$KIT/egress-live-proof.json" \
  "$KIT/live-proofs" "$UNITS/${ORTUS_NOVA_UNIT_PREFIX}nono-live-proof.service"
# The egress gate and Pomerium: their source or image, their configuration
# and their units, not the binaries, which Go stamps with the kit's commit.
# The gate's are everything it is built from and every file it is started
# with (run-egress-gate.sh), so no deploy leaves it on an earlier
# configuration (ONS-123).
restart_changed "${ORTUS_NOVA_UNIT_PREFIX}egress-gate.service" "$ROOT/control-plane/internal/egress" "$ROOT/control-plane/cmd/cp-egress" \
  "$ROOT/control-plane/go.mod" "$KIT/run-egress-gate.sh" "$KIT/front-config.sh" "$ENV_FILE" \
  "$ORTUS_NOVA_HOME/sortie-token" "$ORTUS_NOVA_HOME/nono-pass" "$ORTUS_NOVA_PREVIEW_REVIEW_KEY" "$UNITS/${ORTUS_NOVA_UNIT_PREFIX}egress-gate.service"
restart_changed "${ORTUS_NOVA_UNIT_PREFIX}pomerium.service" "$(pomerium_image)" "$ORTUS_NOVA_CP_HOME/pomerium" "$ORTUS_NOVA_CP_HOME/secrets/cp-pomerium-cert" \
  "$KIT/run-pomerium.sh" "$KIT/front-config.sh" "$UNITS/${ORTUS_NOVA_UNIT_PREFIX}pomerium.service"
restart_worker
systemctl --user restart "${ORTUS_NOVA_UNIT_PREFIX}workbench.service"
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$ORTUS_NOVA_WORKBENCH_PORT/api/auth/mode" && break; sleep 1; done
curl -sf -o /dev/null "http://127.0.0.1:$ORTUS_NOVA_WORKBENCH_PORT/api/auth/mode" || die "the workbench did not start: journalctl --user -u ${ORTUS_NOVA_UNIT_PREFIX}workbench"
# Its local name, for homelab's workstation-setup.sh (local-name.sh).
workbench_name=${ORTUS_NOVA_WORKBENCH_LOCAL_NAME:-workbench.homelab.dev}
if [[ $(write_local_name "$HOME/$ORTUS_NOVA_LOCAL_NAMES_REL" "$workbench_name" "$ORTUS_NOVA_WORKBENCH_PORT") == changed ]]; then
  say "Declared $workbench_name -> 127.0.0.1:$ORTUS_NOVA_WORKBENCH_PORT in ~/$ORTUS_NOVA_LOCAL_NAMES_REL; run \`sudo ./workstation-setup.sh\` in homelab to name it"
fi
for unit in "${ORTUS_NOVA_UNIT_PREFIX}nono" "${ORTUS_NOVA_UNIT_PREFIX}nono-live-proof" "${ORTUS_NOVA_UNIT_PREFIX}egress-gate" "${ORTUS_NOVA_UNIT_PREFIX}pomerium" ${ORTUS_NOVA_UNIT_PREFIX}worker; do
  systemctl --user is-active -q "$unit.service" || die "$unit did not start: journalctl --user -u $unit"
done
deploy_applied
say "Ready: workbench http://127.0.0.1:$ORTUS_NOVA_WORKBENCH_PORT (password in $ORTUS_NOVA_HOME/workbench-password), Sortie 127.0.0.1:$ORTUS_NOVA_SORTIE_PORT, Huly $ORTUS_NOVA_HULY_URL/workbench/$ORTUS_NOVA_HULY_WORKSPACE/tracker, Pomerium $ORTUS_NOVA_CP_PUBLIC_URL for $(front_workers | wc -l) worker(s), p1's own and each worker host's (deploy/worker-host.sh list)"
