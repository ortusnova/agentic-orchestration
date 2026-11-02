#!/usr/bin/env bash
# Sortie hands smolvm the Claude token from its own environment, read once at
# start, and an OAuth login's token lasts hours. So every 10 minutes
# (ortus-stage0-token.timer): when a fresher token exists and the running one
# expires within 90 minutes, restart Sortie, and nono, which holds the same
# token for the edge's egress flow, but only while no agent runs, so no
# session is cut off. Once the running token has expired, no session can
# work with it (each one fails and retries, and counts as running), so it
# restarts at once. A `claude setup-token` token never needs this.
set -euo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
systemctl --user is-active -q ortus-stage0-sortie.service || exit 0
running=$(cat "$STAGE0_HOME/token-expiry" 2>/dev/null || echo 1)
[[ $running == 0 ]] && exit 0
fresh=$("$KIT/anthropic-token.py" --expiry) || { echo "no live Claude token on this host" >&2; exit 0; }
now=$(( $(date +%s) * 1000 ))
(( fresh == 0 || fresh > running )) || exit 0
(( running - now < 90 * 60 * 1000 )) || exit 0
busy=$(curl -sf "http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/state" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("running", [])))') || exit 0
if (( busy > 0 && running > now )); then
  echo "Claude token expires soon, but $busy agent(s) run; retrying later" >&2
  exit 0
fi
if (( running <= now )); then
  echo "Sortie's Claude token has expired; restarting it onto a fresher one ($busy agent(s) fail with it)" >&2
else
  echo "restarting Sortie onto a fresher Claude token" >&2
fi
systemctl --user restart ortus-stage0-sortie.service
if systemctl --user is-active -q ortus-stage0-nono.service; then
  systemctl --user restart ortus-stage0-nono.service
fi
