#!/usr/bin/env bash
# Sortie reads the Claude token at each launch (the workflow's command
# source), so it never needs a restart for it. nono holds the same token for
# the egress flow and reads it only at start, and an OAuth login's
# token lasts hours. So every 10 minutes (ortus-stage0-token.timer): when a
# fresher token exists and nono's expires within 90 minutes, restart both
# nonos (the kit's and the live-proof one) alone, but only while no agent
# runs, so no session's egress is cut off.
# Once nono's token has expired, no egress can work with it, so it restarts
# at once. A `claude setup-token` token never needs this.
set -euo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
systemctl --user is-active -q ortus-stage0-nono.service || exit 0
running=$(cat "$STAGE0_HOME/nono-token-expiry" 2>/dev/null || echo 1)
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
  echo "nono's Claude token has expired; restarting nono onto a fresher one" >&2
else
  echo "restarting nono onto a fresher Claude token" >&2
fi
systemctl --user restart ortus-stage0-nono.service ortus-stage0-nono-live-proof.service
