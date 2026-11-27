# shellcheck shell=bash
# shellcheck disable=SC2034 # the scripts that source it use these
# Every name the kit installs, by environment (spec store
# deploy-environments, design D4, D6): the one place a unit, path, setting
# prefix, podman name, machine label or local-names file is spelled. No
# script spells one; each asks env_names. Sourced by every script of the kit.
#
#   env_names ENV  set the names of ENV on this host
#   env_load ENV   env_names, then the environment's values
#                  ($ORTUS_NOVA_HOME/environment.env, the loader's output the
#                  last deploy.sh wrote) and this host's settings
#
# A host installed under the stage-0 names keeps them until it is migrated
# (D6): when it has ~/.local/share/ortus-nova/stage0/stage0.env, no install
# of ENV, and ENV is the environment whose file sets stage0_names (as the
# loader says, or as the stage-0 home's environment.env recorded),
# env_names gives the stage-0 names. Everything else is the same code path.
#
# ORTUS_NOVA_HOME, when set (a unit sets it; so do the tests), is the
# state directory; the names follow what it holds: stage0.env and no
# host.env is a stage-0 install.

ORTUS_NOVA_ENV_PATTERN='^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'

env_names() {
  local env=$1 root=$HOME/.local/share/ortus-nova
  [[ $env =~ $ORTUS_NOVA_ENV_PATTERN ]] || { echo "names.sh: '$env' is not an environment name (a DNS label)" >&2; return 1; }
  ORTUS_NOVA_ENV=$env ORTUS_NOVA_STAGE0=''
  if [[ -n ${ORTUS_NOVA_HOME:-} ]]; then
    [[ -e $ORTUS_NOVA_HOME/stage0.env && ! -e $ORTUS_NOVA_HOME/host.env ]] && ORTUS_NOVA_STAGE0=1
  elif [[ ! -e $root/$env/host.env && -e $root/stage0/stage0.env ]]; then
    if [[ ${ORTUS_NOVA_ENV_STAGE0_NAMES:-} == true ]] || grep -qx "ORTUS_NOVA_ENV=$env" "$root/stage0/environment.env" 2> /dev/null; then
      ORTUS_NOVA_STAGE0=1
    fi
  fi
  if [[ -n $ORTUS_NOVA_STAGE0 ]]; then
    ORTUS_NOVA_HOME=${ORTUS_NOVA_HOME:-$root/stage0}
    ORTUS_NOVA_SETTINGS_FILE=stage0.env
    ORTUS_NOVA_SETTINGS_PREFIX=STAGE0_
    ORTUS_NOVA_UNIT_PREFIX=ortus-stage0-
    ORTUS_NOVA_PODMAN_PREFIX=ortus-stage0-
    ORTUS_NOVA_LOG_DIR=$HOME/.local/state/ortus-nova/logs
    ORTUS_NOVA_WORKER_HOST_CONFIG=.config/ortus-nova
    ORTUS_NOVA_WORKER_HOST_HOME=.local/share/ortus-nova/stage0
    ORTUS_NOVA_LOCK_FILE=stage0.lock
    ORTUS_NOVA_LOCAL_NAME_FILE=ortus-stage0-workbench
    ORTUS_NOVA_PACK_FILE=stage0.smolmachine
    ORTUS_NOVA_MACHINE_PREFIX=wd-
    ORTUS_NOVA_MACHINE_LABEL_KEY=worker-daemon.worker
    ORTUS_NOVA_MACHINE_LABEL_ENV=''
  else
    ORTUS_NOVA_HOME=${ORTUS_NOVA_HOME:-$root/$env}
    ORTUS_NOVA_SETTINGS_FILE=host.env
    ORTUS_NOVA_SETTINGS_PREFIX=ORTUS_NOVA_
    ORTUS_NOVA_UNIT_PREFIX=ortus-nova-$env-
    ORTUS_NOVA_PODMAN_PREFIX=ortus-nova-$env-
    ORTUS_NOVA_LOG_DIR=$HOME/.local/state/ortus-nova/$env/logs
    ORTUS_NOVA_WORKER_HOST_CONFIG=.config/ortus-nova/$env
    ORTUS_NOVA_WORKER_HOST_HOME=.local/share/ortus-nova/$env
    ORTUS_NOVA_LOCK_FILE=deploy.lock
    ORTUS_NOVA_LOCAL_NAME_FILE=ortus-nova-$env-workbench
    ORTUS_NOVA_PACK_FILE=agent.smolmachine
    ORTUS_NOVA_MACHINE_PREFIX=wd-$env-
    ORTUS_NOVA_MACHINE_LABEL_KEY=ortus-nova.worker
    ORTUS_NOVA_MACHINE_LABEL_ENV=$env/
  fi
  # A worker host's stage-0 home, which the control host's copy of its
  # deploy state falls back to while that host is not migrated.
  ORTUS_NOVA_STAGE0_WORKER_HOST_HOME=.local/share/ortus-nova/stage0
  # The unit templates, units/ortus-nova-<part>, rendered as
  # $ORTUS_NOVA_UNIT_PREFIX<part>.
  ORTUS_NOVA_UNIT_TEMPLATE_PREFIX=ortus-nova-
  export ORTUS_NOVA_ENV ORTUS_NOVA_HOME ORTUS_NOVA_STAGE0
}

# unit PART: the installed name of the kit's unit PART (sortie.service).
unit() { printf '%s%s\n' "$ORTUS_NOVA_UNIT_PREFIX" "$1"; }

# render_unit KIT PART: the unit PART from KIT/units, rendered for this
# environment, on standard output; it is installed as $(unit PART).
render_unit() {
  local kit=$1 part=$2
  sed -e "s|@KIT@|$kit|g" -e "s|@ORTUS_NOVA_HOME@|$ORTUS_NOVA_HOME|g" -e "s|@ORTUS_NOVA_ENV@|$ORTUS_NOVA_ENV|g" \
    -e "s|@UNIT_PREFIX@|$ORTUS_NOVA_UNIT_PREFIX|g" -e "s|@PODMAN_PREFIX@|$ORTUS_NOVA_PODMAN_PREFIX|g" -e "s|@LOG_DIR@|$ORTUS_NOVA_LOG_DIR|g" \
    "$kit/units/$ORTUS_NOVA_UNIT_TEMPLATE_PREFIX$part"
}

# env_settings: source this host's settings file, whatever its prefix, as
# ORTUS_NOVA_* settings.
env_settings() {
  local f=$ORTUS_NOVA_HOME/$ORTUS_NOVA_SETTINGS_FILE
  [[ -s $f ]] || return 0
  if [[ $ORTUS_NOVA_SETTINGS_PREFIX == ORTUS_NOVA_ ]]; then
    # shellcheck source=/dev/null
    . "$f"
  else
    # shellcheck source=/dev/null
    . <(sed "s/\\b$ORTUS_NOVA_SETTINGS_PREFIX/ORTUS_NOVA_/g" "$f")
  fi
}

# setting NAME: NAME as this host's settings file spells it (KIT_ROOT ->
# ORTUS_NOVA_KIT_ROOT or STAGE0_KIT_ROOT), for a script that edits the file.
setting() { printf '%s%s\n' "$ORTUS_NOVA_SETTINGS_PREFIX" "$1"; }

# env_values: the environment's values, as the last deploy.sh wrote them,
# over this host's settings: the environment file holds them now (design
# D2), whatever an older settings file says.
env_values() {
  local f=$ORTUS_NOVA_HOME/environment.env
  # shellcheck source=/dev/null
  [[ -s $f ]] && . "$f"
  [[ -n ${ORTUS_NOVA_ENV_SORTIE_PORT:-} ]] || return 0
  ORTUS_NOVA_SORTIE_PORT=$ORTUS_NOVA_ENV_SORTIE_PORT
  ORTUS_NOVA_WORKBENCH_PORT=$ORTUS_NOVA_ENV_WORKBENCH_PORT
  ORTUS_NOVA_NONO_PORT=$ORTUS_NOVA_ENV_NONO_PORT
  ORTUS_NOVA_NONO_LIVE_PROOF_PORT=$ORTUS_NOVA_ENV_NONO_LIVE_PROOF_PORT
  ORTUS_NOVA_EGRESS_GATE_PORT=$ORTUS_NOVA_ENV_EGRESS_GATE_PORT
  ORTUS_NOVA_PREVIEW_FIRST_PORT=$ORTUS_NOVA_ENV_PREVIEW_FIRST_PORT
  ORTUS_NOVA_HULY_URL=$ORTUS_NOVA_ENV_TRACKER_URL
  ORTUS_NOVA_HULY_WORKSPACE=$ORTUS_NOVA_ENV_TRACKER_WORKSPACE
  ORTUS_NOVA_HULY_PROJECT=$ORTUS_NOVA_ENV_TRACKER_PROJECT
  ORTUS_NOVA_WORKBENCH_LOCAL_NAME=${ORTUS_NOVA_ENV_WORKBENCH_LOCAL_NAME:-}
  # What the host runs (design D3): control and its own worker, or a worker.
  if [[ -n ${ORTUS_NOVA_ENV_HOST_RUNS:-} ]]; then
    ORTUS_NOVA_PROFILE=worker
    [[ " $ORTUS_NOVA_ENV_HOST_RUNS " == *" control "* ]] && ORTUS_NOVA_PROFILE=control
  fi
  return 0
}

# env_file_load ENV HOST: ENV's values for HOST, read from its environment
# file through the loader (design D2), into this shell; refuses a file the
# loader refuses or a HOST it does not declare. The loader is built once
# into the user's cache. ORTUS_NOVA_ENVIRONMENTS and ORTUS_NOVA_ENVFILE, for
# the tests, are another directory of environment files and a built loader.
env_file_load() {
  local kit dir loader values
  kit=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  dir=${ORTUS_NOVA_ENVIRONMENTS:-$kit/environments}
  loader=${ORTUS_NOVA_ENVFILE:-}
  if [[ -z $loader ]]; then
    loader=${XDG_CACHE_HOME:-$HOME/.cache}/ortus-nova/envfile
    mkdir -p "${loader%/*}"
    (cd "$kit/envfile" && go build -o "$loader" .) || { echo "names.sh: could not build the environment loader (deploy/envfile)" >&2; return 1; }
  fi
  values=$("$loader" values -host "$2" "$dir/$1.yml") || return 1
  eval "$values"
  ORTUS_NOVA_ENV_VALUES=$values
}

env_load() {
  env_names "$1" || return 1
  env_settings
  env_values
  ORTUS_NOVA_ENV=$1
  export ORTUS_NOVA_ENV ORTUS_NOVA_HOME
}
