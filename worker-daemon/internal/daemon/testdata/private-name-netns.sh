#!/bin/sh
# Runs TestSmolvmEgressEndToEnd with WORKER_DAEMON_E2E_PRIVATE_NAME in a
# network namespace of its own (pasta --config-net --no-map-gw), whose
# /etc/resolv.conf names a test resolver on 127.0.0.1: dnsmasq answers NAME
# with ADDR, TTL 1 s, and forwards every other name to $UPSTREAM (9.9.9.9 by
# default). The host's own DNS is not changed. ADDR is a private address
# with a listener on $WORKER_DAEMON_E2E_PRIVATE_PORT (443 by default). The
# test's other variables (WORKER_DAEMON_E2E_PACK, _ALLOWED, ...) pass
# through.
#
#   private-name-netns.sh NAME ADDR [go test flags...]
#
# With PRIVATE_LOCAL=1, ADDR is no host of the network but the namespace's
# own: the namespace takes ADDR on its loopback and the test resolver
# listens there too, so the listener is dnsmasq's, on port 53, and
# WORKER_DAEMON_E2E_PRIVATE_PORT is 53. A live proof in a job machine, which
# reaches no private host, runs it so (worker-daemon/live-proofs.json).
#
# It needs pasta (passt), unshare (util-linux) and dnsmasq, and with
# PRIVATE_LOCAL ip (iproute2).
#
# In pasta's user namespace the caller is root with setgroups denied, so:
# dnsmasq runs with --no-daemon, which keeps its user and groups; the
# namespace's /etc/nsswitch.conf is "files dns", so that a host resolver
# such as systemd-resolved does not answer instead of the test resolver;
# and the test runs in a nested user namespace as the caller's own uid and
# gid, because smolvm refuses to start a machine as root there ("chown vm
# data dir for uid drop"). --no-map-gw keeps the host's gateway address
# the real gateway.
set -eu
[ $# -ge 2 ] || { echo "usage: $0 NAME ADDR [go test flags...]" >&2; exit 2; }
NAME=$1 ADDR=$2
shift 2
PKG=$(cd "$(dirname "$0")/.." && pwd)
W=$(mktemp -d /tmp/private-name.XXXXXX)
trap 'rm -rf "$W"' EXIT
echo "$ADDR $NAME" >"$W/hosts"
echo "nameserver 127.0.0.1" >"$W/resolv.conf"
echo "hosts: files dns" >"$W/nsswitch.conf"
HOST_UID=$(id -u) HOST_GID=$(id -g)
LISTEN=127.0.0.1
PRIVATE_LOCAL=${PRIVATE_LOCAL:-}
if [ -n "$PRIVATE_LOCAL" ]; then
  LISTEN=127.0.0.1,$ADDR
  WORKER_DAEMON_E2E_PRIVATE_PORT=53
  export WORKER_DAEMON_E2E_PRIVATE_PORT
fi
export W NAME ADDR PKG UPSTREAM="${UPSTREAM:-9.9.9.9}" HOST_UID HOST_GID LISTEN PRIVATE_LOCAL
# shellcheck disable=SC2016 # the inner shell expands these
pasta --config-net --no-map-gw -- unshare --mount sh -euc '
  mount --bind "$W/resolv.conf" /etc/resolv.conf
  mount --bind "$W/nsswitch.conf" /etc/nsswitch.conf
  if [ -n "$PRIVATE_LOCAL" ]; then ip address add "$ADDR/32" dev lo; fi
  dnsmasq --no-daemon --listen-address="$LISTEN" --bind-interfaces \
    --no-resolv --no-hosts --addn-hosts="$W/hosts" --local-ttl=1 --server="$UPSTREAM" \
    --log-facility="$W/dnsmasq.log" &
  dns=$!
  sleep 1
  cd "$PKG"
  status=0
  WORKER_DAEMON_E2E_PRIVATE_NAME="$NAME" unshare --user --map-user="$HOST_UID" --map-group="$HOST_GID" \
    go test -count=1 -v -timeout 30m -run "^TestSmolvmEgressEndToEnd\$" "$@" . || status=$?
  kill "$dns" 2>/dev/null || true
  exit "$status"
' sh "$@"
