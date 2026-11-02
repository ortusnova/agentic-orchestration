#!/usr/bin/env bash
# Apply a host setup change, such as a smolvm bump, under no running agent:
# pause this host's Sortie's dispatch (drain.sh), wait for the runs going to
# finish, apply the change, run stage0.sh so the pack and Sortie follow it
# (the homelab pack is named by smolvm's version), and dispatch again. The
# queue of stories waits; the change waits for the running runs only.
#
#   dev/stage0/dev-host-apply.sh              homelab profile: `make dev-host` in STAGE0_HOMELAB_SRC
#   dev/stage0/dev-host-apply.sh COMMAND...   any profile: run COMMAND as the apply
#
# Dispatch resumes whether the apply succeeds or fails; a failure exits
# non-zero. It holds stage0.sh's lock throughout, so no deploy runs between.
# STAGE0_DRAIN_POLL (seconds, default 15) is how often it asks Sortie.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# shellcheck source=drain.sh
. "$KIT/drain.sh"
say() { echo "dev-host-apply: $*" >&2; }
die() { say "$*"; exit 1; }

apply=("$@")
if (( ${#apply[@]} == 0 )); then
  [[ ${STAGE0_PROFILE:-control} == homelab ]] || die "usage: $0 COMMAND... (only the homelab profile has a default: make dev-host)"
  apply=(make -C "${STAGE0_HOMELAB_SRC:-$HOME/dev/homelab}" dev-host)
fi

exec 9> "$STAGE0_HOME/stage0.lock"
if ! flock -n 9; then
  say "waiting for a stage0.sh or a deploy to finish ($STAGE0_HOME/stage0.lock)"
  flock 9
fi
export STAGE0_LOCKED=1

trap 'dispatch_resume || say "Sortie did not resume dispatch: POST /api/v1/dispatch/resume, or the next deploy run does"' EXIT
if dispatch_pause "dev-host apply: ${apply[*]}"; then
  say "dispatch paused"
else
  say "Sortie did not pause dispatch; waiting until it happens to run nothing"
fi
while busy=$(sortie_running) && (( busy > 0 )); do
  say "waiting for $busy run(s) to finish"
  sleep "${STAGE0_DRAIN_POLL:-15}"
done

say "applying: ${apply[*]}"
"${apply[@]}" 9>&- || die "the apply failed: ${apply[*]}"
say "running stage0.sh"
"$KIT/stage0.sh" 9>&- || die "stage0.sh failed after the apply"
say "applied; dispatch resumes"
