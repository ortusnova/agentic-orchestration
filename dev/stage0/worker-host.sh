#!/usr/bin/env bash
# On p1: add a worker host to the control plane's front, or remove one
# (README.md "Worker hosts"). Each worker host gets a client certificate from
# the control plane's CA, and Pomerium names the worker to Sortie from it;
# no tunnel, no port of its own (design D18).
#
#   dev/stage0/worker-host.sh add HOST [NAME]  issue NAME a certificate and copy it to HOST;
#                                              the worker is NAME (default: HOST without its
#                                              user@ and domain); p1 then copies HOST's
#                                              deploy state over SSH (deploy-state.sh)
#   dev/stage0/worker-host.sh remove NAME      revoke NAME's certificate: Pomerium refuses it on
#                                              every route from then on
#   dev/stage0/worker-host.sh list             each listed worker, its expiry and its routes
#   dev/stage0/worker-host.sh carry HOST       copy every merge record of HOST's Sortie
#                                              into p1's, before HOST's Sortie stops
#
# HOST is an SSH destination that works without a prompt
# (`ssh -o BatchMode=yes HOST true`). Run stage0.sh first: it sets up the
# CA and Pomerium. Nothing here restarts Sortie; Pomerium restarts onto the
# new worker list, so each worker's requests in flight are cut once and
# its daemon dials again.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# A worker host's STAGE0_HOME, as its own shell sees it (the default there).
WORKER_HOST_HOME=${STAGE0_WORKER_HOST_HOME:-.local/share/ortus-nova/stage0}
die() { printf 'worker-host.sh: %s\n' "$*" >&2; exit 1; }
usage() { die "usage: $0 add HOST [NAME] | remove NAME | list | carry HOST"; }

[[ -s $STAGE0_HOME/stage0.env ]] || die "$STAGE0_HOME/stage0.env is missing: run dev/stage0/stage0.sh first"
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
# shellcheck source=deploy-state.sh
. "$KIT/deploy-state.sh"

# front_ready: stage0.sh has set up the CA and cpctl.
front_ready() {
  [[ -x $STAGE0_HOME/bin/cpctl && -s $STAGE0_CP_HOME/settings.json ]] \
    || die "the control plane's front is not set up: run dev/stage0/stage0.sh first"
}

# apply: Pomerium follows the worker list cpctl rendered.
apply() {
  if systemctl --user is-enabled -q ortus-stage0-pomerium.service 2>/dev/null; then
    systemctl --user restart ortus-stage0-pomerium.service
  fi
}

cmd=${1:-}
[[ $# -gt 0 ]] && shift
case $cmd in
  add)
    [[ $# == 1 || $# == 2 ]] || usage
    host=$1
    name=${2:-${host#*@}}
    name=${name%%.*}
    [[ $host =~ ^[A-Za-z0-9._@-]+$ ]] || die "HOST must be a plain SSH destination: $host"
    [[ $name =~ $FRONT_WORKER_NAME ]] || die "NAME must be a DNS label (lower-case letters, digits, '-'): $name"
    [[ $name == p1 ]] && die "p1 is the control plane: stage0.sh issues its own worker's certificate"
    front_ready
    ssh -o BatchMode=yes -o ConnectTimeout=10 "$host" true || die "ssh $host does not work without a prompt"
    cpctl worker issue "$name" > /dev/null || die "could not issue $name a certificate"
    dir=$STAGE0_CP_HOME/workers/$name
    # The certificate and its key, the CA that signs Pomerium's, the egress
    # CA's certificate, so the worker's machines trust nono's interception
    # (its key stays on p1), and Pomerium's URL. The key is the one secret
    # a worker host keeps; it leaves p1 only here, and p1 deletes its copy.
    ssh -o BatchMode=yes "$host" "mkdir -p -m 700 $STAGE0_WORKER_HOST_CONFIG" || die "could not reach $host"
    copy() { # copy FILE NAME MODE
      ssh -o BatchMode=yes "$host" "umask 077 && cat > $STAGE0_WORKER_HOST_CONFIG/$2.tmp && chmod $3 $STAGE0_WORKER_HOST_CONFIG/$2.tmp && mv $STAGE0_WORKER_HOST_CONFIG/$2.tmp $STAGE0_WORKER_HOST_CONFIG/$2" < "$1" \
        || die "could not copy $2 to $host"
    }
    copy "$dir/worker.crt" "${STAGE0_WORKER_TLS_FILES[0]}" 644
    copy "$dir/worker.key" "${STAGE0_WORKER_TLS_FILES[1]}" 600
    copy "$dir/ca.crt" "${STAGE0_WORKER_TLS_FILES[2]}" 644
    copy "$STAGE0_HOME/egress-ca/ca.crt" egress-ca.crt 644
    copy <(printf '%s\n' "$STAGE0_CP_PUBLIC_URL") control-plane-url 644
    rm -f "$dir/worker.key"
    # p1 copies its deploy state over the same SSH (deploy-state.sh), for
    # Sortie's merge-and-prove; the workflow names it from the next stage0.sh.
    deploy_host_add "$name" "$host"
    apply
    echo "worker $name: $host holds its certificate (~/$STAGE0_WORKER_HOST_CONFIG); on $host, run STAGE0_PROFILE=worker dev/stage0/stage0.sh to dial $STAGE0_CP_PUBLIC_URL"
    ;;
  remove)
    [[ $# == 1 ]] || usage
    name=$1
    [[ $name =~ $FRONT_WORKER_NAME ]] || die "no worker $name"
    [[ $name == p1 ]] && die "p1 is p1's own worker, which stage0.sh runs"
    front_ready
    cpctl worker revoke "$name" > /dev/null || die "no worker $name"
    # A token the stage-1 edge's add minted for its notifications.
    rm -f "$STAGE0_HOME/notify-tokens/$name.token"
    deploy_host_remove "$name"
    apply
    echo "revoked worker $name: Pomerium refuses its certificate on every route"
    ;;
  carry)
    # Before a worker host's Sortie stops (its move from the homelab
    # profile), p1's Sortie must hold every merge record it holds, or a
    # story blocked by a story that merged there waits on p1 for ever
    # (awaiting_merge_record). Run it again after a merge there; the host's
    # stage0.sh refuses to stop its Sortie until no record is left behind.
    [[ $# == 1 ]] || usage
    host=$1
    [[ $host =~ ^[A-Za-z0-9._@-]+$ ]] || die "HOST must be a plain SSH destination: $host"
    db=$STAGE0_HOME/sortie/.sortie.db
    [[ -s $db ]] || die "p1's Sortie has no database at $db"
    rows=$(ssh -o BatchMode=yes "$host" python3 - dump "$WORKER_HOST_HOME/sortie/.sortie.db" < "$KIT/merges.py") \
      || die "could not read the merge records of $host's Sortie"
    carried=$(python3 "$KIT/merges.py" load "$db" <<< "$rows") || die "could not add $host's merge records to p1's Sortie"
    ssh -o BatchMode=yes "$host" "mkdir -p $WORKER_HOST_HOME && cat > $WORKER_HOST_HOME/merges-carried.json" <<< "$carried" \
      || die "could not tell $host which merge records p1's Sortie holds"
    n=$(python3 -c 'import json, sys; print(len(json.load(sys.stdin)))' <<< "$carried")
    echo "p1's Sortie holds the $n merge record(s) of $host's Sortie; on $host, STAGE0_PROFILE=worker dev/stage0/stage0.sh may stop it now"
    ;;
  list)
    [[ $# == 0 ]] || usage
    front_ready
    cpctl worker list
    ;;
  *) usage ;;
esac
