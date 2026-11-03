#!/usr/bin/env bash
# Run what merged to the kit's main, so no person deploys it: every minute
# (ortus-stage0-deploy.timer), move the live kit ($STAGE0_HOME/kit, the
# worktree stage0.sh runs the units from) to origin/main.
#
#   - A change to the workbench rebuilds it and restarts its unit, at once:
#     restarting the workbench stops no agent, so it never waits for a
#     drain. When the same merge also changes the rest of the kit, only
#     workbench/ moves ahead while the rest waits (recorded in
#     deploy-workbench.at), and the later full apply builds it again.
#   - A change to the rest of the kit (dev/, acp-broker/, smol-builder/) runs
#     stage0.sh again, which restarts Sortie too, so it waits until no agent
#     runs; until then the live kit stays where it is.
#   - A merge to Sortie's plan-bench runs stage0.sh --update-sortie, which
#     rebuilds and restarts Sortie, so it too waits until no agent runs. A
#     Sortie checkout that is not behind plan-bench (a branch being tried)
#     stays where it is.
#   - While either waits, Sortie's dispatch is paused (drain.sh): the runs
#     going finish and no new one starts, so the change waits for those runs
#     only, never behind the queue of stories. Once it is applied, or put
#     back, dispatch resumes. A Sortie without the pause is waited for as
#     before, until it happens to run nothing.
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
# The PATH stage0.sh last ran with (it records it): a systemd unit gets a
# shorter one, without the tools stage0.sh needs (openspec, under pnpm).
if [[ -s $STAGE0_HOME/path ]]; then
  PATH=$(cat "$STAGE0_HOME/path")
  export PATH
fi
failed=$STAGE0_HOME/deploy.failed
sortie_failed=$STAGE0_HOME/deploy-sortie.failed
say() { echo "deploy: $*" >&2; }
# shellcheck source=drain.sh
. "$(dirname "$0")/drain.sh"
resume() { dispatch_resume || say "Sortie did not resume dispatch; trying again in a minute"; }

# workbench_ahead: while the rest of the kit waits for a drain, move only
# workbench/ to the merged commit, rebuild it and restart it, once per
# commit. A failed build puts workbench/ back.
workbench_ahead() {
  [[ -n $workbench && ${STAGE0_PROFILE:-control} == control ]] || return 0
  [[ $(cat "$STAGE0_HOME/deploy-workbench.at" 2>/dev/null) == "$new" ]] && return 0
  say "${new:0:8} changes the workbench: rebuilding it now; the rest of the kit waits"
  git -C "$LIVE" checkout -q "$new" -- workbench
  if (cd "$LIVE/workbench" && uv sync -q) && (cd "$LIVE/workbench/frontend" \
    && { [[ -d node_modules ]] && ! grep -q '^workbench/frontend/package-lock.json$' <<< "$changed" || npm ci --silent; } \
    && npx vite build --logLevel warn); then
    systemctl --user restart ortus-stage0-workbench.service
    echo "$new" > "$STAGE0_HOME/deploy-workbench.at"
  else
    say "the workbench build failed at ${new:0:8}; workbench/ back on ${old:0:8}"
    git -C "$LIVE" checkout -q "$old" -- workbench
    echo "$new" > "$STAGE0_HOME/deploy-workbench.at"
  fi
}

exec 9> "$STAGE0_HOME/stage0.lock"
flock -n 9 || { say "a stage0.sh runs; trying again in a minute"; exit 0; }
export STAGE0_LOCKED=1

git -C "$LIVE" fetch -q origin main
old=$(git -C "$LIVE" rev-parse HEAD)
new=$(git -C "$LIVE" rev-parse origin/main)
[[ $old == "$new" || $(cat "$failed" 2>/dev/null) == "$new" ]] && new=$old

src=${STAGE0_SORTIE_SRC:-}
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
  # Nothing waits any more (a change that failed, say): dispatch again.
  resume
  exit 0
fi

changed=$(git -C "$LIVE" diff --name-only "$old" "$new")
kit=$(grep -E '^(dev|acp-broker|smol-builder)/' <<< "$changed" || true)
workbench=$(grep -E '^workbench/' <<< "$changed" || true)
update_sortie=()
[[ $sortie_old != "$sortie_new" ]] && update_sortie=(--update-sortie)

if [[ -n $kit || ${#update_sortie[@]} -gt 0 ]]; then
  why=()
  [[ -n $kit ]] && why+=("kit ${new:0:8}")
  [[ ${#update_sortie[@]} -gt 0 ]] && why+=("Sortie ${sortie_new:0:8}")
  draining="dispatch paused"
  dispatch_pause "deploy of ${why[*]}" || draining="Sortie did not pause dispatch"
  busy=$(sortie_running) || busy=0
  if (( busy > 0 )); then
    workbench_ahead
    [[ -n $kit ]] && say "${new:0:8} changes the kit; waiting until no agent runs ($busy now, $draining)"
    [[ ${#update_sortie[@]} -gt 0 ]] && say "Sortie ${sortie_new:0:8} merged; waiting until no agent runs ($busy now, $draining)"
    exit 0
  fi
else
  resume
fi

git -C "$LIVE" checkout -q --detach "$new"
back() {
  say "$1 failed at ${new:0:8}; back on ${old:0:8} until another commit merges"
  [[ $old != "$new" ]] && echo "$new" > "$failed"
  git -C "$LIVE" checkout -q --detach "$old"
  if [[ ${#update_sortie[@]} -gt 0 ]]; then
    say "Sortie back on ${sortie_old:0:8} until another plan-bench commit merges"
    echo "$sortie_new" > "$sortie_failed"
    git -C "$src" checkout -q --detach "$sortie_old"
  fi
  resume
  exit 1
}

if [[ -n $kit || ${#update_sortie[@]} -gt 0 ]]; then
  [[ -n $kit ]] && say "${new:0:8} changes the kit: running stage0.sh"
  [[ ${#update_sortie[@]} -gt 0 ]] && say "Sortie ${sortie_new:0:8} merged: running stage0.sh --update-sortie"
  "$LIVE/dev/stage0/stage0.sh" "${update_sortie[@]}" || back "stage0.sh"
  resume
elif [[ -n $workbench && ${STAGE0_PROFILE:-control} == control ]]; then
  say "${new:0:8} changes the workbench: rebuilding it"
  (cd "$LIVE/workbench" && uv sync -q) || back "uv sync"
  (cd "$LIVE/workbench/frontend" \
    && { [[ -d node_modules ]] && ! grep -q '^workbench/frontend/package-lock.json$' <<< "$changed" || npm ci --silent; } \
    && npx vite build --logLevel warn) || back "the frontend build"
  systemctl --user restart ortus-stage0-workbench.service
fi
rm -f "$failed"
[[ ${#update_sortie[@]} -gt 0 ]] && rm -f "$sortie_failed"
say "running ${new:0:8}${update_sortie:+, Sortie ${sortie_new:0:8}}"
