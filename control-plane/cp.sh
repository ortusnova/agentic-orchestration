#!/usr/bin/env bash
# The control plane's compose project under rootless podman
# (docs/operator-guide.md). Run it as the unprivileged user that owns the
# project; it refuses to run as root.
#
#   cp.sh up                  create the podman secrets from $CP_HOME/secrets, start the project, wait for health
#   cp.sh huly                start Huly alone, for its first account and project
#   cp.sh down                stop and remove the containers (volumes and secrets stay)
#   cp.sh health              each service, up or down, by name; fails when one is down
#   cp.sh apply               render Pomerium's configuration from the worker list and restart Pomerium onto it
#   cp.sh worker issue NAME   issue a worker certificate (then apply)
#   cp.sh worker revoke NAME  take a worker off the list and apply at once
#   cp.sh worker list         the listed workers
#   cp.sh cpctl ARGS...       run cpctl (init, secrets, render, ...)
#
# Settings, from the environment:
#   CP_HOME               the control-plane directory (default ~/.local/share/ortus-nova/control-plane)
#   CP_TAG                the commit the images were built from (default: this checkout's HEAD)
#   CP_SORTIE_CONFIG_DIR  the directory of Sortie's WORKFLOW.md (default $CP_HOME/sortie)
#   CP_NONO_DIR           the directory of nono's egress-profile.json (default nono/ here)
#   CP_COMPOSE_FILES      extra compose files, space-separated, after compose.yaml
#   CP_PUBLIC_PORT, CP_PUBLIC_BIND, CP_WORKBENCH_PORT, CP_HULY_HOST, CP_HULY_PORT, CP_HULY_BIND: see compose.yaml
set -euo pipefail

CP=$(cd "$(dirname "$0")" && pwd)
die() { printf 'cp.sh: %s\n' "$*" >&2; exit 1; }
say() { printf 'cp.sh: %s\n' "$*" >&2; }

[[ $(id -u) != 0 ]] || die "run the control plane as an unprivileged user, not root"
command -v podman > /dev/null || die "podman is not installed"

export CP_HOME=${CP_HOME:-$HOME/.local/share/ortus-nova/control-plane}
export CP_TAG=${CP_TAG:-$(git -C "$CP" rev-parse HEAD)}
export CP_SORTIE_CONFIG_DIR=${CP_SORTIE_CONFIG_DIR:-$CP_HOME/sortie}
export CP_NONO_DIR=${CP_NONO_DIR:-$CP/nono}
# Pomerium's published port is the port of the public URL cpctl init recorded.
if [[ -z ${CP_PUBLIC_PORT:-} && -s $CP_HOME/settings.json ]]; then
  url=$(sed -n 's/.*"public_url": *"https:\/\/\([^"/]*\)".*/\1/p' "$CP_HOME/settings.json")
  [[ $url == *:* ]] && CP_PUBLIC_PORT=${url##*:} || CP_PUBLIC_PORT=443
  export CP_PUBLIC_PORT
fi

# The services the health check reports, in order; acp-broker runs in
# Sortie's container, one per session, so its check runs it there.
SERVICES=(sortie acp-broker workbench pomerium egress-gate nono huly)

container_of() {
  case $1 in
    acp-broker) echo cp-sortie ;;
    *) echo "cp-$1" ;;
  esac
}

# podman-compose before 1.5 runs every health check through /bin/sh, which
# the images built here do not have.
compose_version() { podman-compose --version 2> /dev/null | sed -n 's/^podman-compose version \([0-9.]*\).*/\1/p'; }
compose_version_ok() {
  local v major minor
  v=$(compose_version)
  IFS=. read -r major minor _ <<< "$v"
  [[ -n $major ]] && ((major > 1 || (major == 1 && ${minor:-0} >= 5)))
}

compose() {
  local files=(-f "$CP/compose.yaml") f
  for f in ${CP_COMPOSE_FILES:-}; do files+=(-f "$f"); done
  command -v podman-compose > /dev/null || die "podman-compose is not installed"
  compose_version_ok || die "podman-compose $(compose_version) is too old: the health checks need 1.5 or later (pipx install podman-compose)"
  podman-compose -p ortus-cp "${files[@]}" "$@"
}

# cpctl runs from the control-plane image, as this user, on $CP_HOME; set
# CPCTL to a cpctl binary to use that instead.
cpctl() {
  if [[ -n ${CPCTL:-} ]]; then
    "$CPCTL" -home "$CP_HOME" "$@"
    return
  fi
  mkdir -p "$CP_HOME"
  podman run --rm --network none --userns keep-id --user "$(id -u):$(id -g)" \
    -v "$CP_HOME:$CP_HOME:z" "localhost/ortus-nova/control-plane:$CP_TAG" cpctl -home "$CP_HOME" "$@"
}

# check_one SERVICE: whether it answers its health check.
check_one() {
  local svc=$1 ctr
  ctr=$(container_of "$svc")
  case $svc in
    acp-broker) podman exec "$ctr" /usr/local/bin/acp-broker ls > /dev/null 2>&1 ;;
    *) podman healthcheck run "$ctr" > /dev/null 2>&1 ;;
  esac
}

health() {
  local svc failed=0
  for svc in "${SERVICES[@]}"; do
    if check_one "$svc"; then
      printf '%-12s up\n' "$svc"
    else
      printf '%-12s down\n' "$svc"
      failed=1
    fi
  done
  return $failed
}

# wait_healthy SECONDS [SERVICE...]: until every service is up.
wait_healthy() {
  local timeout=$1 svc pending
  local deadline=$((SECONDS + timeout))
  shift
  local want=("$@")
  [[ ${#want[@]} -gt 0 ]] || want=("${SERVICES[@]}")
  while :; do
    pending=()
    for svc in "${want[@]}"; do check_one "$svc" || pending+=("$svc"); done
    [[ ${#pending[@]} -eq 0 ]] && return 0
    if ((SECONDS >= deadline)); then
      say "not up after $timeout s: ${pending[*]}"
      return 1
    fi
    sleep 5
  done
}

# secrets [partial]: one podman secret per file in $CP_HOME/secrets,
# replaced at each start, so a changed file reaches the containers when
# they start again. cp-github-basic is derived: the forge token as git's
# Basic credential. Without partial, every file must be there. A file's
# trailing newlines are not part of its secret: a podman secret given to a
# container as an environment variable keeps them, and a credential nono
# injects or a password it checks must not end in one.
secrets() {
  local missing name state
  if [[ ${1:-} != partial ]]; then
    missing=$(cpctl secrets -missing 2> /dev/null || true)
    [[ -z $missing ]] || die "secret files are missing (docs/operator-guide.md, Secrets):
$missing"
  fi
  while read -r name state _; do
    [[ $state == present ]] || continue
    printf '%s' "$(< "$CP_HOME/secrets/$name")" | podman secret create --replace "$name" - > /dev/null
  done < <(cpctl secrets)
  if [[ -s $CP_HOME/secrets/cp-github-token ]]; then
    printf 'x-access-token:%s' "$(tr -d '\n' < "$CP_HOME/secrets/cp-github-token")" \
      | podman secret create --replace cp-github-basic - > /dev/null
  fi
}

up() {
  [[ -s $CP_HOME/settings.json ]] || die "$CP_HOME is not set up: run cp.sh cpctl init -public-url https://HOST:8443"
  [[ -s $CP_SORTIE_CONFIG_DIR/WORKFLOW.md ]] || die "$CP_SORTIE_CONFIG_DIR/WORKFLOW.md is missing: copy sortie/WORKFLOW.example.md there and edit it"
  # Sortie's user in its container reads it; it holds no secret, only $VAR references.
  chmod -R a+rX "$CP_SORTIE_CONFIG_DIR"
  local img
  for img in sortie acp-broker workbench control-plane; do
    podman image exists "localhost/ortus-nova/$img:$CP_TAG" || die "image localhost/ortus-nova/$img:$CP_TAG is missing: make -C control-plane images"
  done
  cpctl render > /dev/null
  secrets
  say "starting the project (images $CP_TAG)"
  compose up -d
  wait_healthy "${CP_UP_TIMEOUT:-600}"
  health
}

# huly: start Huly alone, before Sortie can log in to it (docs/operator-guide.md,
# Huly's first start).
huly() {
  secrets partial
  compose up -d huly-cockroach huly-redpanda huly-minio huly-elastic huly-stats huly-rekoni huly-account huly-workspace huly-transactor huly-fulltext huly-collaborator huly-front huly
  wait_healthy "${CP_UP_TIMEOUT:-600}" huly
}

apply() {
  cpctl render > /dev/null
  podman restart cp-pomerium > /dev/null
  wait_healthy 120 pomerium
  say "Pomerium runs the worker list in $CP_HOME/workers.json"
}

cmd=${1:-}
[[ $# -gt 0 ]] && shift
case $cmd in
  up) up ;;
  huly) huly ;;
  down) compose down ;;
  health) health ;;
  apply) apply ;;
  worker)
    case ${1:-} in
      revoke) cpctl worker "$@" && apply ;;
      *) cpctl worker "$@" ;;
    esac
    ;;
  cpctl) cpctl "$@" ;;
  *) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2; exit 2 ;;
esac
