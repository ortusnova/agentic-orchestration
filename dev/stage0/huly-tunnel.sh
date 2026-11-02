#!/usr/bin/env bash
# On p1: give a homelab-profile host (dev/stage0/stage0.sh, STAGE0_PROFILE=
# homelab) p1's Huly on its own loopback, through a remote SSH forward kept
# up by a user unit. Rootless. Separate from stage0.sh, so enabling it does
# not restart p1's Sortie or workbench.
#
#   dev/stage0/huly-tunnel.sh HOST          install, enable and start the tunnel to HOST
#   dev/stage0/huly-tunnel.sh --remove HOST stop and remove it
#
# HOST is an SSH destination that works without a password prompt
# (`ssh -o BatchMode=yes HOST true`).
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
UNIT=ortus-stage0-huly-tunnel@.service
UNITS=$HOME/.config/systemd/user
die() { printf 'huly-tunnel.sh: %s\n' "$*" >&2; exit 1; }

remove=''
if [[ ${1:-} == --remove ]]; then remove=1; shift; fi
[[ $# == 1 ]] || die "usage: $0 [--remove] HOST"
host=$1
[[ $host =~ ^[A-Za-z0-9._@-]+$ ]] || die "HOST must be a plain SSH destination: $host"
instance="ortus-stage0-huly-tunnel@$host.service"

if [[ -n $remove ]]; then
  systemctl --user disable -q --now "$instance" 2>/dev/null || true
  # The template stays while another host still uses it.
  if ! systemctl --user list-units --all --no-legend 'ortus-stage0-huly-tunnel@*' | grep -q .; then
    rm -f "$UNITS/$UNIT"
  fi
  systemctl --user daemon-reload
  echo "removed the Huly tunnel to $host"
  exit 0
fi

ssh -o BatchMode=yes -o ConnectTimeout=10 "$host" true || die "ssh $host does not work without a prompt"
mkdir -p "$UNITS"
install -m 0644 "$KIT/units/$UNIT" "$UNITS/$UNIT"
systemctl --user daemon-reload
systemctl --user enable -q "$instance"
systemctl --user restart "$instance"
for _ in $(seq 30); do
  ssh -o BatchMode=yes "$host" curl -sf -o /dev/null http://127.0.0.1:8087/config.json && {
    echo "Huly answers on $host at http://127.0.0.1:8087"
    exit 0
  }
  sleep 1
done
die "the tunnel is up but Huly does not answer on $host: journalctl --user -u $instance"
