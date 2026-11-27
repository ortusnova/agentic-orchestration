#!/usr/bin/env bash
# Run what merged to the kit's main, so no person deploys it: every minute
# (ortus-nova-<env>-deploy.timer), move the live kit ($ORTUS_NOVA_HOME/kit, the
# worktree deploy.sh runs the units from) to origin/main. A deploy never
# drains (spec store agent-platform/run-supervision): it pauses no dispatch
# and waits for no run, so a merge applies within the minute, whatever runs.
#
#   - A change to the workbench rebuilds it and restarts its unit.
#   - A change to the rest of the kit (dev/, acp-broker/, control-plane/,
#     smol-builder/, worker-daemon/) runs deploy.sh again, which restarts Sortie at once:
#     Sortie re-adopts each run whose lease a worker still holds and follows
#     it to its end. nono, the egress gate, Pomerium and the worker daemon
#     carry the runs, so deploy.sh restarts each only when what it runs changed
#     (unit-restart.sh). A worker host (ORTUS_NOVA_PROFILE=worker) builds no
#     Sortie, follows no plan-bench merge and asks no Sortie anything,
#     whatever Sortie settings its stage0.env kept from before it became a
#     worker.
#   - A merge to Sortie's plan-bench runs deploy.sh --update-sortie, which
#     rebuilds and restarts Sortie. A Sortie checkout that is not behind
#     plan-bench (a branch being tried) stays where it is.
#   - A build that fails puts the previous commit back, and that commit is
#     not tried again until another one merges (journalctl --user -u
#     ortus-nova-<env>-deploy says why). A kit change whose deploy.sh failed
#     runs the previous commit's deploy.sh again, so what the failed run
#     changed on the host (units, configuration) is the previous commit's.
#   - It runs under deploy.sh's lock: while a deploy.sh runs, it does
#     nothing and tries again a minute later.
#   - It writes this host's deploy state (deploy-state.sh), which Sortie's
#     health check reads: deploying while it runs,
#     succeeded once the new commit runs and every unit whose configuration
#     changed has restarted (unit-restart.sh: the egress gate too, ONS-123),
#     failed with its error when anything above failed, and at each tick
#     the commit that runs beside main, so a commit left behind shows. On
#     p1 it then copies each worker host's deploy state beside it.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
ENV_NAME=${1:?usage: follow.sh ENV}
# shellcheck source=names.sh
. "$KIT/names.sh"
env_load "$ENV_NAME"
LIVE=${ORTUS_NOVA_LIVE_KIT:-$ORTUS_NOVA_HOME/kit}
# shellcheck source=deploy-state.sh
. "$KIT/deploy-state.sh"
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
# deploy.sh runs what the environment file says this host runs.
export ORTUS_NOVA_PROFILE=${ORTUS_NOVA_PROFILE:-control}
# The PATH deploy.sh last ran with (it records it): a systemd unit gets a
# shorter one, without the tools deploy.sh needs (openspec, under pnpm).
if [[ -s $ORTUS_NOVA_HOME/path ]]; then
  PATH=$(cat "$ORTUS_NOVA_HOME/path")
  export PATH
fi
failed=$ORTUS_NOVA_HOME/deploy.failed
sortie_failed=$ORTUS_NOVA_HOME/deploy-sortie.failed
say() { echo "deploy: $*" >&2; }

# An earlier kit paused Sortie's dispatch while a change waited for a drain,
# and recorded it in dispatch.paused: dispatch again, once.
resume_left_pause() {
  [[ $ORTUS_NOVA_PROFILE == control && -e $ORTUS_NOVA_HOME/dispatch.paused ]] || return 0
  if printf 'Authorization: Bearer %s\n' "$(cat "$ORTUS_NOVA_HOME/sortie-token" 2> /dev/null)" \
    | curl -sf -X POST -H @- "http://127.0.0.1:$ORTUS_NOVA_SORTIE_PORT/api/v1/dispatch/resume" > /dev/null; then
    rm -f "$ORTUS_NOVA_HOME/dispatch.paused"
    say "Sortie dispatches again after the pause an earlier kit left"
  else
    say "Sortie did not resume the pause an earlier kit left; trying again in a minute"
  fi
}

exec 9> "$ORTUS_NOVA_HOME/$ORTUS_NOVA_LOCK_FILE"
flock -n 9 || { say "a deploy.sh runs; trying again in a minute"; exit 0; }
export ORTUS_NOVA_LOCKED=1
rm -f "$ORTUS_NOVA_HOME/deploy-workbench.at"
# p1 copies each worker host's deploy state at every tick, whatever this
# one did (deploy-state.sh).
[[ $ORTUS_NOVA_PROFILE == control ]] && trap deploy_state_pull EXIT

git -C "$LIVE" fetch -q origin main || say "could not fetch origin main; checking the main fetched last"
old=$(git -C "$LIVE" rev-parse HEAD)
main=$(git -C "$LIVE" rev-parse origin/main)
new=$main
[[ $old == "$new" || $(cat "$failed" 2>/dev/null) == "$new" ]] && new=$old

# A worker host builds no Sortie.
src=${ORTUS_NOVA_SORTIE_SRC:-}
[[ $ORTUS_NOVA_PROFILE == worker ]] && src=''
sortie_old='' sortie_new=''
if [[ -n $src && -d $src ]]; then
  git -C "$src" fetch -q origin plan-bench
  sortie_old=$(git -C "$src" rev-parse HEAD)
  sortie_new=$(git -C "$src" rev-parse origin/plan-bench)
  if [[ $sortie_old == "$sortie_new" || $(cat "$sortie_failed" 2>/dev/null) == "$sortie_new" ]] \
    || ! git -C "$src" merge-base --is-ancestor "$sortie_old" "$sortie_new"; then
    sortie_new=$sortie_old
  fi
fi
if [[ $old == "$new" && $sortie_old == "$sortie_new" ]]; then
  resume_left_pause
  deploy_state_refresh "$old" "$main"
  exit 0
fi
deploy_state_write deploying "$old" "$main"

changed=$(git -C "$LIVE" diff --name-only "$old" "$new")
kit=$(grep -E '^(deploy|dev|acp-broker|control-plane|smol-builder|worker-daemon)/' <<< "$changed" || true)
workbench=$(grep -E '^workbench/' <<< "$changed" || true)
update_sortie=()
[[ $sortie_old != "$sortie_new" ]] && update_sortie=(--update-sortie)

git -C "$LIVE" checkout -q --force --detach "$new"
log=$ORTUS_NOVA_HOME/deploy.err
# kit_run ARGS...: run the live kit's deploy.sh; its errors also go to $log,
# whose last line is the failed deploy's error. A file, not a pipe: a unit
# or container deploy.sh starts must not hold this run open. A commit from
# before the kit moved to deploy/, which a failed deploy goes back to on a
# host under its stage-0 names, has only the stage-0 kit's script (design
# D6; deploy-kit-push removes this).
kit_run() {
  local rc=0
  if [[ -x $LIVE/deploy/deploy.sh ]]; then
    "$LIVE/deploy/deploy.sh" "$ENV_NAME" "$@" 2> "$log" || rc=$?
  else
    STAGE0_HOME=$ORTUS_NOVA_HOME STAGE0_LOCKED=1 "$LIVE/dev/stage0/stage0.sh" "$@" 2> "$log" || rc=$?
  fi
  cat "$log" >&2
  return "$rc"
}
# why: the last line deploy.sh wrote to its errors, without its colours.
why() { sed 's/\x1b\[[0-9;]*m//g' "$log" 2> /dev/null | grep -v '^[[:space:]]*$' | tail -n 1; }

back() {
  local error=$1 cause=${2:-}
  say "$1 failed at ${new:0:8}; back on ${old:0:8} until another commit merges"
  [[ $old != "$new" ]] && echo "$new" > "$failed"
  git -C "$LIVE" checkout -q --force --detach "$old"
  if [[ ${#update_sortie[@]} -gt 0 ]]; then
    say "Sortie back on ${sortie_old:0:8} until another plan-bench commit merges"
    echo "$sortie_new" > "$sortie_failed"
    git -C "$src" checkout -q --detach "$sortie_old"
  fi
  error+=" at ${new:0:12}${update_sortie:+ (Sortie ${sortie_new:0:12})}${cause:+: $cause}"
  if [[ $1 == deploy.sh* && -n $kit ]]; then
    say "running deploy.sh on ${old:0:8} again, to undo what ${new:0:8}'s left"
    if ! kit_run; then
      say "deploy.sh on ${old:0:8} failed too: the host needs a person (journalctl --user -u ${ORTUS_NOVA_UNIT_PREFIX}deploy)"
      error+="; deploy.sh on ${old:0:12} failed too: $(why)"
    fi
  fi
  deploy_state_write failed "$old" "$main" "$error"
  resume_left_pause
  exit 1
}

if [[ -n $kit || ${#update_sortie[@]} -gt 0 ]]; then
  [[ -n $kit ]] && say "${new:0:8} changes the kit: running deploy.sh now, whatever runs"
  [[ ${#update_sortie[@]} -gt 0 ]] && say "Sortie ${sortie_new:0:8} merged: running deploy.sh --update-sortie now, whatever runs"
  kit_run "${update_sortie[@]}" || back "deploy.sh" "$(why)"
elif [[ -n $workbench && $ORTUS_NOVA_PROFILE == control ]]; then
  say "${new:0:8} changes the workbench: rebuilding it"
  (cd "$LIVE/workbench" && uv sync -q) || back "uv sync"
  (cd "$LIVE/workbench/frontend" \
    && { [[ -d node_modules ]] && ! grep -q '^workbench/frontend/package-lock.json$' <<< "$changed" || npm ci --silent; } \
    && npx vite build --logLevel warn) || back "the frontend build"
  systemctl --user restart "${ORTUS_NOVA_UNIT_PREFIX}workbench.service" || back "the workbench's restart"
fi
# Applied only once every unit whose configuration changed has restarted
# and runs (unit-restart.sh), the egress gate among them (ONS-123).
unsettled=$(units_unsettled | paste -sd, -)
[[ -z $unsettled ]] || back "deploy.sh's restart of $unsettled" "it did not run after its restart"
resume_left_pause
rm -f "$failed"
[[ ${#update_sortie[@]} -gt 0 ]] && rm -f "$sortie_failed"
deploy_state_write succeeded "$new" "$main"
say "running ${new:0:8}${update_sortie:+, Sortie ${sortie_new:0:8}}"
