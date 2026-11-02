#!/usr/bin/env bash
# On p1: add a worker host to the stage-1 edge, or remove one. Each worker
# host gets its own SSH tunnel, which p1 opens, and its own edge port, and
# the edge names the worker by that port (README.md, the stage-1 edge).
#
#   dev/stage0/worker-host.sh add HOST [NAME]  open the tunnel to HOST; the worker is NAME
#                                              (default: HOST without its user@)
#   dev/stage0/worker-host.sh remove NAME      close it and forget the worker
#   dev/stage0/worker-host.sh list             each worker, its host and its edge port
#
# HOST is an SSH destination that works without a prompt
# (`ssh -o BatchMode=yes HOST true`). Run stage0.sh first: it sets up the
# edge and nono. Nothing here restarts Sortie; the edge restarts, so each
# tunnel's requests in flight are cut once.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
UNITS=${STAGE0_UNITS:-$HOME/.config/systemd/user}
TEMPLATE=ortus-stage0-worker-tunnel@.service
die() { printf 'worker-host.sh: %s\n' "$*" >&2; exit 1; }
usage() { die "usage: $0 add HOST [NAME] | remove NAME | list"; }

[[ -s $STAGE0_HOME/stage0.env ]] || die "$STAGE0_HOME/stage0.env is missing: run dev/stage0/stage0.sh first"
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# shellcheck source=edge-config.sh
. "$KIT/edge-config.sh"
mkdir -p "$STAGE0_WORKERS"

# apply: the edge's configuration and units follow $STAGE0_WORKERS.
apply() {
  render_edge_config "$STAGE0_HOME/edge.json"
  mkdir -p "$UNITS"
  sed -e "s|@STAGE0_HOME@|$STAGE0_HOME|g" "$KIT/units/$TEMPLATE" > "$UNITS/$TEMPLATE"
  systemctl --user daemon-reload
  if systemctl --user is-enabled -q ortus-stage0-edge.service 2>/dev/null; then
    systemctl --user restart ortus-stage0-edge.service
  fi
}

cmd=${1:-}
[[ $# -gt 0 ]] && shift
case $cmd in
  add)
    [[ $# == 1 || $# == 2 ]] || usage
    host=$1
    name=${2:-${host#*@}}
    [[ $host =~ ^[A-Za-z0-9._@-]+$ ]] || die "HOST must be a plain SSH destination: $host"
    [[ $name =~ $EDGE_WORKER_NAME ]] || die "NAME must be letters, digits, '.', '_' or '-': $name"
    [[ $name == p1 ]] && die "p1 is the control plane, not a worker host"
    port=''
    if [[ -e $STAGE0_WORKERS/$name.env ]]; then
      port=$(worker_setting "$name" EDGE_PORT)
    else
      port=$STAGE0_EDGE_FIRST_PORT
      while grep -qx "EDGE_PORT=$port" "$STAGE0_WORKERS"/*.env 2>/dev/null; do port=$((port + 1)); done
    fi
    ssh -o BatchMode=yes -o ConnectTimeout=10 "$host" true || die "ssh $host does not work without a prompt"
    # The egress CA's certificate, so the worker's machines trust nono's
    # interception. A certificate, not a credential: its key stays on p1.
    ssh -o BatchMode=yes "$host" 'mkdir -p ~/.config/ortus-nova && cat > ~/.config/ortus-nova/egress-ca.crt' \
      < "$STAGE0_HOME/egress-ca/ca.crt" || die "could not copy the egress CA's certificate to $host"
    (umask 077; printf 'WORKER_HOST=%s\nEDGE_PORT=%s\nWORKER_PORT=%s\n' "$host" "$port" "$STAGE0_EDGE_WORKER_PORT" \
      > "$STAGE0_WORKERS/$name.env")
    apply
    instance=ortus-stage0-worker-tunnel@$name.service
    systemctl --user enable -q "$instance"
    systemctl --user restart "$instance"
    for _ in $(seq 30); do
      code=$(ssh -o BatchMode=yes "$host" curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$STAGE0_EDGE_WORKER_PORT/" || true)
      if [[ $code != 000 && -n $code ]]; then
        echo "worker $name: $host reaches p1's edge at http://127.0.0.1:$STAGE0_EDGE_WORKER_PORT (edge port $port)"
        exit 0
      fi
      sleep 1
    done
    die "the tunnel is up but the edge does not answer on $host: journalctl --user -u $instance -u ortus-stage0-edge"
    ;;
  remove)
    [[ $# == 1 ]] || usage
    name=$1
    [[ $name =~ $EDGE_WORKER_NAME && -e $STAGE0_WORKERS/$name.env ]] || die "no worker host $name"
    systemctl --user disable -q --now "ortus-stage0-worker-tunnel@$name.service" 2>/dev/null || true
    rm -f "$STAGE0_WORKERS/$name.env"
    apply
    echo "removed worker host $name"
    ;;
  list)
    [[ $# == 0 ]] || usage
    for name in $(worker_names); do
      printf '%s\t%s\tedge port %s\n' "$name" "$(worker_setting "$name" WORKER_HOST)" "$(worker_setting "$name" EDGE_PORT)"
    done
    ;;
  *) usage ;;
esac
