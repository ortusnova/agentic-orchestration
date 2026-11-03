#!/usr/bin/env bash
# Run what merged to the kit's main, so no person deploys it: every minute
# (ortus-stage0-deploy.timer), move the live kit ($STAGE0_HOME/kit, the
# worktree stage0.sh runs the units from) to origin/main. A deploy never
# drains (spec store agent-platform/run-supervision): it pauses no dispatch
# and waits for no run, so a merge applies within the minute, whatever runs.
#
#   - A change to the workbench rebuilds it and restarts its unit.
#   - A change to the rest of the kit (dev/, acp-broker/, smol-builder/,
#     worker-daemon/) runs stage0.sh again, which restarts Sortie at once:
#     Sortie re-adopts each run whose lease a worker still holds and follows
#     it to its end. nono, the edge and the worker daemon carry the runs, so
#     stage0.sh restarts each only when what it runs changed
#     (unit-restart.sh). A worker host (STAGE0_PROFILE=worker) builds no
#     Sortie, follows no plan-bench merge and asks no Sortie anything,
#     whatever Sortie settings its stage0.env kept from before it became a
#     worker.
#   - A merge to Sortie's plan-bench runs stage0.sh --update-sortie, which
#     rebuilds and restarts Sortie. A Sortie checkout that is not behind
#     plan-bench (a branch being tried) stays where it is.
#   - A build that fails puts the previous commit back, and that commit is
#     not tried again until another one merges (journalctl --user -u
#     ortus-stage0-deploy says why).
#   - It runs under stage0.sh's lock: while a stage0.sh runs, it does
#     nothing and tries again a minute later.
set -euo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
LIVE=${STAGE0_LIVE_KIT:-$STAGE0_HOME/kit}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# stage0.sh runs the profile deploy decided on.
export STAGE0_PROFILE=${STAGE0_PROFILE:-control}
# The PATH stage0.sh last ran with (it records it): a systemd unit gets a
# shorter one, without the tools stage0.sh needs (openspec, under pnpm).
if [[ -s $STAGE0_HOME/path ]]; then
  PATH=$(cat "$STAGE0_HOME/path")
  export PATH
fi
failed=$STAGE0_HOME/deploy.failed
sortie_failed=$STAGE0_HOME/deploy-sortie.failed
say() { echo "deploy: $*" >&2; }

# An earlier kit paused Sortie's dispatch while a change waited for a drain,
# and recorded it in dispatch.paused: dispatch again, once.
resume_left_pause() {
  [[ $STAGE0_PROFILE == control && -e $STAGE0_HOME/dispatch.paused ]] || return 0
  if printf 'Authorization: Bearer %s\n' "$(cat "$STAGE0_HOME/sortie-token" 2> /dev/null)" \
    | curl -sf -X POST -H @- "http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/dispatch/resume" > /dev/null; then
    rm -f "$STAGE0_HOME/dispatch.paused"
    say "Sortie dispatches again after the pause an earlier kit left"
  else
    say "Sortie did not resume the pause an earlier kit left; trying again in a minute"
  fi
}

exec 9> "$STAGE0_HOME/stage0.lock"
flock -n 9 || { say "a stage0.sh runs; trying again in a minute"; exit 0; }
export STAGE0_LOCKED=1
rm -f "$STAGE0_HOME/deploy-workbench.at"

git -C "$LIVE" fetch -q origin main
old=$(git -C "$LIVE" rev-parse HEAD)
new=$(git -C "$LIVE" rev-parse origin/main)
[[ $old == "$new" || $(cat "$failed" 2>/dev/null) == "$new" ]] && new=$old

# A worker host builds no Sortie.
src=${STAGE0_SORTIE_SRC:-}
[[ $STAGE0_PROFILE == worker ]] && src=''
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
  exit 0
fi

changed=$(git -C "$LIVE" diff --name-only "$old" "$new")
kit=$(grep -E '^(dev|acp-broker|smol-builder|worker-daemon)/' <<< "$changed" || true)
workbench=$(grep -E '^workbench/' <<< "$changed" || true)
update_sortie=()
[[ $sortie_old != "$sortie_new" ]] && update_sortie=(--update-sortie)

git -C "$LIVE" checkout -q --force --detach "$new"
back() {
  say "$1 failed at ${new:0:8}; back on ${old:0:8} until another commit merges"
  [[ $old != "$new" ]] && echo "$new" > "$failed"
  git -C "$LIVE" checkout -q --force --detach "$old"
  if [[ ${#update_sortie[@]} -gt 0 ]]; then
    say "Sortie back on ${sortie_old:0:8} until another plan-bench commit merges"
    echo "$sortie_new" > "$sortie_failed"
    git -C "$src" checkout -q --detach "$sortie_old"
  fi
  resume_left_pause
  exit 1
}

if [[ -n $kit || ${#update_sortie[@]} -gt 0 ]]; then
  [[ -n $kit ]] && say "${new:0:8} changes the kit: running stage0.sh now, whatever runs"
  [[ ${#update_sortie[@]} -gt 0 ]] && say "Sortie ${sortie_new:0:8} merged: running stage0.sh --update-sortie now, whatever runs"
  "$LIVE/dev/stage0/stage0.sh" "${update_sortie[@]}" || back "stage0.sh"
elif [[ -n $workbench && $STAGE0_PROFILE == control ]]; then
  say "${new:0:8} changes the workbench: rebuilding it"
  (cd "$LIVE/workbench" && uv sync -q) || back "uv sync"
  (cd "$LIVE/workbench/frontend" \
    && { [[ -d node_modules ]] && ! grep -q '^workbench/frontend/package-lock.json$' <<< "$changed" || npm ci --silent; } \
    && npx vite build --logLevel warn) || back "the frontend build"
  systemctl --user restart ortus-stage0-workbench.service
fi
resume_left_pause
rm -f "$failed"
[[ ${#update_sortie[@]} -gt 0 ]] && rm -f "$sortie_failed"
say "running ${new:0:8}${update_sortie:+, Sortie ${sortie_new:0:8}}"
