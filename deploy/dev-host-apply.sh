#!/usr/bin/env bash
# Apply a host setup change, such as a smolvm bump, and run deploy.sh so the
# packs, Sortie and the worker daemon follow it (the homelab pack is named by
# smolvm's version). Like a deploy, it never drains (spec store
# agent-platform/run-supervision): it pauses no dispatch and waits for no
# run. deploy.sh restarts Sortie at once, which re-adopts the runs its
# workers hold, and restarts the worker daemon only when what it runs
# changed (unit-restart.sh); a job the restart ends is retried, as for any
# lost lease.
#
#   deploy/dev-host-apply.sh ENV              worker host with homelab: `make dev-host` in ORTUS_NOVA_HOMELAB_SRC
#   deploy/dev-host-apply.sh ENV COMMAND...   any host: run COMMAND as the apply
#
# A failed apply, or a failed deploy.sh after it, exits non-zero. It holds
# deploy.sh's lock throughout, so no deploy runs between. Like a host's step of a push, it
# writes this host's deploy state (deploy-state.sh): deploying while it
# runs, succeeded once deploy.sh ran and every unit whose configuration
# changed has restarted, failed with its error otherwise.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
[[ $# -gt 0 ]] || { echo "usage: $0 ENV [COMMAND...]" >&2; exit 2; }
ENV_NAME=$1
shift
# shellcheck source=names.sh
. "$KIT/names.sh"
env_load "$ENV_NAME"
LIVE=${ORTUS_NOVA_LIVE_KIT:-$ORTUS_NOVA_HOME/kit}
# shellcheck source=deploy-state.sh
. "$KIT/deploy-state.sh"
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
say() { echo "dev-host-apply: $*" >&2; }
die() { say "$*"; exit 1; }
# The commit the live kit runs, and its main, as the last fetch left it.
commit() { git -C "$LIVE" rev-parse -q --verify HEAD 2> /dev/null || true; }
main() { git -C "$LIVE" rev-parse -q --verify origin/main 2> /dev/null || commit; }
# failed WHY: record the failed apply, and stop.
failed() { deploy_state_write failed "$(commit)" "$(main)" "the dev-host apply failed: $1"; die "$1"; }

apply=("$@")
if (( ${#apply[@]} == 0 )); then
  [[ ${ORTUS_NOVA_PROFILE:-control} == worker && -n ${ORTUS_NOVA_HOMELAB_SRC:-} ]] \
    || die "usage: $0 ENV COMMAND... (only a worker host with homelab (ORTUS_NOVA_HOMELAB_SRC) has a default: make dev-host)"
  apply=(make -C "$ORTUS_NOVA_HOMELAB_SRC" dev-host)
fi

exec 9> "$ORTUS_NOVA_HOME/$ORTUS_NOVA_LOCK_FILE"
if ! flock -n 9; then
  say "waiting for a deploy.sh or a deploy to finish ($ORTUS_NOVA_HOME/$ORTUS_NOVA_LOCK_FILE)"
  flock 9
fi
export ORTUS_NOVA_LOCKED=1

deploy_state_write deploying "$(commit)" "$(main)"
say "applying now, whatever runs: ${apply[*]}"
"${apply[@]}" 9>&- || failed "${apply[*]} failed"
say "running deploy.sh"
log=$ORTUS_NOVA_HOME/dev-host-apply.err
rc=0
"$KIT/deploy.sh" "$ENV_NAME" --this-host 9>&- 2> "$log" || rc=$?
cat "$log" >&2
(( rc == 0 )) || failed "deploy.sh failed after the apply: $(sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -v '^[[:space:]]*$' | tail -n 1)"
unsettled=$(units_unsettled | paste -sd, -)
[[ -z $unsettled ]] || failed "deploy.sh's restart of $unsettled did not leave it running"
deploy_state_write succeeded "$(commit)" "$(main)"
say "applied"
