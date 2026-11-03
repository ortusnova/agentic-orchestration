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
# stage0.sh's lock throughout, so no deploy runs between.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
say() { echo "dev-host-apply: $*" >&2; }
die() { say "$*"; exit 1; }

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

say "applying now, whatever runs: ${apply[*]}"
"${apply[@]}" 9>&- || die "the apply failed: ${apply[*]}"
say "running stage0.sh"
"$KIT/stage0.sh" 9>&- || die "stage0.sh failed after the apply"
say "applied"
