#!/usr/bin/env bash
# Apply a host setup change, such as a smolvm bump, and run stage0.sh so the
# packs, Sortie and the worker daemon follow it (the homelab pack is named by
# smolvm's version). Like a deploy, it never drains (spec store
# agent-platform/run-supervision): it pauses no dispatch and waits for no
# run. stage0.sh restarts Sortie at once, which re-adopts the runs its
# workers hold, and restarts the worker daemon only when what it runs
# changed (unit-restart.sh); a job the restart ends is retried, as for any
# lost lease.
#
#   dev/stage0/dev-host-apply.sh              worker host with homelab: `make dev-host` in STAGE0_HOMELAB_SRC
#   dev/stage0/dev-host-apply.sh COMMAND...   any host: run COMMAND as the apply
#
# A failed apply, or a failed stage0.sh after it, exits non-zero. It holds
# stage0.sh's lock throughout, so no deploy runs between. Like deploy.sh, it
# writes this host's deploy state (deploy-state.sh): deploying while it
# runs, succeeded once stage0.sh ran and every unit whose configuration
# changed has restarted, failed with its error otherwise.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
LIVE=${STAGE0_LIVE_KIT:-$STAGE0_HOME/kit}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# shellcheck source=deploy-state.sh
. "$KIT/deploy-state.sh"
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
say() { echo "dev-host-apply: $*" >&2; }
die() { say "$*"; exit 1; }
# The commit the live kit runs, and its main, as deploy.sh last fetched it.
commit() { git -C "$LIVE" rev-parse -q --verify HEAD 2> /dev/null || true; }
main() { git -C "$LIVE" rev-parse -q --verify origin/main 2> /dev/null || commit; }
# failed WHY: record the failed apply, and stop.
failed() { deploy_state_write failed "$(commit)" "$(main)" "the dev-host apply failed: $1"; die "$1"; }

apply=("$@")
if (( ${#apply[@]} == 0 )); then
  [[ ${STAGE0_PROFILE:-control} == worker && -n ${STAGE0_HOMELAB_SRC:-} ]] \
    || die "usage: $0 COMMAND... (only a worker host with homelab (STAGE0_HOMELAB_SRC) has a default: make dev-host)"
  apply=(make -C "$STAGE0_HOMELAB_SRC" dev-host)
fi

exec 9> "$STAGE0_HOME/stage0.lock"
if ! flock -n 9; then
  say "waiting for a stage0.sh or a deploy to finish ($STAGE0_HOME/stage0.lock)"
  flock 9
fi
export STAGE0_LOCKED=1

deploy_state_write deploying "$(commit)" "$(main)"
say "applying now, whatever runs: ${apply[*]}"
"${apply[@]}" 9>&- || failed "${apply[*]} failed"
say "running stage0.sh"
log=$STAGE0_HOME/dev-host-apply.err
rc=0
"$KIT/stage0.sh" 9>&- 2> "$log" || rc=$?
cat "$log" >&2
(( rc == 0 )) || failed "stage0.sh failed after the apply: $(sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -v '^[[:space:]]*$' | tail -n 1)"
unsettled=$(units_unsettled | paste -sd, -)
[[ -z $unsettled ]] || failed "stage0.sh's restart of $unsettled did not leave it running"
deploy_state_write succeeded "$(commit)" "$(main)"
say "applied"
