#!/usr/bin/env bash
# Apply a host setup change, such as a smolvm bump, under no running agent,
# run stage0.sh so the packs, Sortie and the worker daemon follow it (the
# homelab pack is named by smolvm's version), and take work again. The queue
# of stories waits; the change waits for the running runs only.
#
#   On p1 (control): pause Sortie's dispatch (drain.sh), wait for the runs
#   going to finish, apply, run stage0.sh, and dispatch again.
#   On a worker host: no Sortie runs here. Wait until the worker daemon runs
#   no job, stop it so it takes none during the apply, apply, and run
#   stage0.sh, which starts it again. A job p1's Sortie gives it in the
#   moment between the last check and the stop fails and is retried there.
#
#   dev/stage0/dev-host-apply.sh              worker host with homelab: `make dev-host` in STAGE0_HOMELAB_SRC
#   dev/stage0/dev-host-apply.sh COMMAND...   any host: run COMMAND as the apply
#
# Dispatch resumes, and the worker daemon starts again, whether the apply
# succeeds or fails; a failure exits non-zero. It holds stage0.sh's lock
# throughout, so no deploy runs between. STAGE0_DRAIN_POLL (seconds, default
# 15) is how often it looks.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# shellcheck source=drain.sh
. "$KIT/drain.sh"
say() { echo "dev-host-apply: $*" >&2; }
die() { say "$*"; exit 1; }
worker=''
[[ ${STAGE0_PROFILE:-control} == worker ]] && worker=1

apply=("$@")
if (( ${#apply[@]} == 0 )); then
  [[ $worker && -n ${STAGE0_HOMELAB_SRC:-} ]] \
    || die "usage: $0 COMMAND... (only a worker host with homelab (STAGE0_HOMELAB_SRC) has a default: make dev-host)"
  apply=(make -C "$STAGE0_HOMELAB_SRC" dev-host)
fi

exec 9> "$STAGE0_HOME/stage0.lock"
if ! flock -n 9; then
  say "waiting for a stage0.sh or a deploy to finish ($STAGE0_HOME/stage0.lock)"
  flock 9
fi
export STAGE0_LOCKED=1

if [[ $worker ]]; then
  # shellcheck source=worker-config.sh
  . "$KIT/worker-config.sh"
  trap 'systemctl --user start ortus-stage0-worker.service || say "the worker daemon did not start again: systemctl --user start ortus-stage0-worker"' EXIT
  while busy=$(worker_jobs) && (( busy > 0 )); do
    say "waiting for the worker daemon's $busy job(s) to finish"
    sleep "${STAGE0_DRAIN_POLL:-15}"
  done
  systemctl --user stop ortus-stage0-worker.service
  say "the worker daemon runs no job; stopped it for the apply"
else
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
fi

say "applying: ${apply[*]}"
"${apply[@]}" 9>&- || die "the apply failed: ${apply[*]}"
say "running stage0.sh"
"$KIT/stage0.sh" 9>&- || die "stage0.sh failed after the apply"
say "applied; work resumes"
