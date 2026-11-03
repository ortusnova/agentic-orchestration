#!/usr/bin/env bash
# On p1: give a homelab-profile host (dev/stage0/stage0.sh, STAGE0_PROFILE=
# homelab) p1's Huly on its own loopback, through a remote SSH forward kept
# up by a user unit; and give p1's workbench that host's Sortie on p1's
# loopback, through a local forward in the same connection, so the workbench
# reads the questions it holds and answers them; and a SOCKS forward to that
# host's loopback, so the workbench watches and steers the agents it runs
# through their brokers (spec store agent-platform/run-supervision). Rootless. Separate from stage0.sh, so
# enabling it does not restart p1's Sortie; it restarts the workbench once it
# has the host's Sortie token.
#
#   dev/stage0/huly-tunnel.sh HOST          install, enable and start the tunnel to HOST
#   dev/stage0/huly-tunnel.sh --remove HOST stop and remove it
#
# HOST is an SSH destination that works without a password prompt
# (`ssh -o BatchMode=yes HOST true`). The Sortie's name, as the workbench
# shows it, is HOST's host name without user or domain: brian@taichi.lan is
# taichi. Run it again once stage0.sh has run on HOST, so the workbench gets
# that Sortie's control token.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
UNIT=ortus-stage0-huly-tunnel@.service
UNITS=$HOME/.config/systemd/user
# The homelab profile's defaults on HOST (stage0.sh).
REMOTE_HOME=.local/share/ortus-nova/stage0
REMOTE_SORTIE_PORT=7680
# p1's loopback ports for the homelab hosts' Sorties, one each, from here up;
# and for the SOCKS forwards that reach their agents' brokers, which listen on
# each host's loopback, one each, from SOCKS_FIRST_PORT up.
FIRST_PORT=7681
SOCKS_FIRST_PORT=7781
die() { printf 'huly-tunnel.sh: %s\n' "$*" >&2; exit 1; }

remove=''
if [[ ${1:-} == --remove ]]; then remove=1; shift; fi
[[ $# == 1 ]] || die "usage: $0 [--remove] HOST"
host=$1
[[ $host =~ ^[A-Za-z0-9._@-]+$ ]] || die "HOST must be a plain SSH destination: $host"
instance="ortus-stage0-huly-tunnel@$host.service"
name=${host##*@}
name=${name%%.*}
name=$(tr '[:upper:]' '[:lower:]' <<< "$name")
[[ $name =~ ^[a-z0-9][a-z0-9-]*$ ]] || die "HOST's name $name must be letters, digits and dashes"
tunnel=$STAGE0_HOME/tunnels/$host.env
sortie=$STAGE0_HOME/sorties/$name

restart_workbench() {
  systemctl --user try-restart ortus-stage0-workbench.service 2>/dev/null || true
}

if [[ -n $remove ]]; then
  systemctl --user disable -q --now "$instance" 2>/dev/null || true
  # The template stays while another host still uses it.
  if ! systemctl --user list-units --all --no-legend 'ortus-stage0-huly-tunnel@*' | grep -q .; then
    rm -f "$UNITS/$UNIT"
  fi
  systemctl --user daemon-reload
  rm -f "$tunnel" "$sortie.env" "$sortie.token"
  restart_workbench
  echo "removed the Huly tunnel to $host, and $name's Sortie from the workbench"
  exit 0
fi

ssh -o BatchMode=yes -o ConnectTimeout=10 "$host" true || die "ssh $host does not work without a prompt"
mkdir -p "$UNITS" "$STAGE0_HOME/tunnels" "$STAGE0_HOME/sorties"
chmod 700 "$STAGE0_HOME/sorties"
# The same port for the same host on every run; the next free one for a new host.
port=$(sed -n 's/^SORTIE_LOCAL_PORT=//p' "$tunnel" 2>/dev/null || true)
if [[ -z $port ]]; then
  port=$FIRST_PORT
  while grep -qsx "SORTIE_LOCAL_PORT=$port" "$STAGE0_HOME"/tunnels/*.env; do port=$((port + 1)); done
fi
socks=$(sed -n 's/^SORTIE_SOCKS_PORT=//p' "$tunnel" 2>/dev/null || true)
if [[ -z $socks ]]; then
  socks=$SOCKS_FIRST_PORT
  while grep -qsx "SORTIE_SOCKS_PORT=$socks" "$STAGE0_HOME"/tunnels/*.env; do socks=$((socks + 1)); done
fi
remote_port=$(ssh -o BatchMode=yes "$host" "sed -n 's/^STAGE0_SORTIE_PORT=//p' $REMOTE_HOME/stage0.env 2>/dev/null" || true)
remote_port=${remote_port:-$REMOTE_SORTIE_PORT}
[[ $remote_port =~ ^[0-9]+$ ]] || die "$host's STAGE0_SORTIE_PORT is not a port: $remote_port"
printf 'SORTIE_LOCAL_PORT=%s\nSORTIE_FORWARD=127.0.0.1:%s:127.0.0.1:%s\nSORTIE_SOCKS_PORT=%s\nSORTIE_SOCKS=127.0.0.1:%s\n' \
  "$port" "$port" "$remote_port" "$socks" "$socks" > "$tunnel"
sed -e "s|@STAGE0_HOME@|$STAGE0_HOME|g" "$KIT/units/$UNIT" > "$UNITS/$UNIT"
systemctl --user daemon-reload
systemctl --user enable -q "$instance"
systemctl --user restart "$instance"
huly=''
for _ in $(seq 30); do
  if ssh -o BatchMode=yes "$host" curl -sf -o /dev/null http://127.0.0.1:8087/config.json; then huly=1; break; fi
  sleep 1
done
[[ -n $huly ]] || die "the tunnel is up but Huly does not answer on $host: journalctl --user -u $instance"
echo "Huly answers on $host at http://127.0.0.1:8087"

# The workbench reads and answers the questions of the host's Sortie with its
# control token, which stage0.sh creates there.
if ! (umask 077; ssh -o BatchMode=yes "$host" cat "$REMOTE_HOME/sortie-token" > "$sortie.token.new" 2>/dev/null) \
    || [[ ! -s $sortie.token.new ]]; then
  rm -f "$sortie.token.new"
  echo "$host has no Sortie token yet: run dev/stage0/stage0.sh there, then $0 $host again"
  exit 0
fi
mv "$sortie.token.new" "$sortie.token"
printf 'SORTIE_URL=http://127.0.0.1:%s\nSORTIE_BROKER_PROXY=socks5://127.0.0.1:%s\n' "$port" "$socks" > "$sortie.env"
restart_workbench
echo "$name's Sortie is at http://127.0.0.1:$port, its agents' brokers through socks5://127.0.0.1:$socks: the workbench answers, grants and steers there"
