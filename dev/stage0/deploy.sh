#!/usr/bin/env bash
# Run what merged to the kit's main, so no person deploys it: every minute
# (ortus-stage0-deploy.timer), move the live kit ($STAGE0_HOME/kit, the
# worktree stage0.sh runs the units from) to origin/main.
#
#   - A change to the workbench rebuilds it and restarts its unit.
#   - A change to the rest of the kit (dev/, acp-broker/, smol-builder/) runs
#     stage0.sh again, which restarts Sortie too, so it waits until no agent
#     runs; until then the live kit stays where it is.
#   - A build that fails puts the previous commit back, and that commit is
#     not tried again until another one merges (journalctl --user -u
#     ortus-stage0-deploy says why).
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
say() { echo "deploy: $*" >&2; }

git -C "$LIVE" fetch -q origin main
old=$(git -C "$LIVE" rev-parse HEAD)
new=$(git -C "$LIVE" rev-parse origin/main)
[[ $old == "$new" ]] && exit 0
[[ $(cat "$failed" 2>/dev/null) == "$new" ]] && exit 0

changed=$(git -C "$LIVE" diff --name-only "$old" "$new")
kit=$(grep -E '^(dev|acp-broker|smol-builder)/' <<< "$changed" || true)
workbench=$(grep -E '^workbench/' <<< "$changed" || true)

if [[ -n $kit ]]; then
  busy=$(curl -sf "http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/state" \
    | python3 -c 'import json, sys; print(len(json.load(sys.stdin).get("running", [])))') || busy=0
  if (( busy > 0 )); then
    say "${new:0:8} changes the kit; waiting until no agent runs ($busy now)"
    exit 0
  fi
fi

git -C "$LIVE" checkout -q --detach "$new"
back() {
  say "$1 failed at ${new:0:8}; back on ${old:0:8} until another commit merges"
  echo "$new" > "$failed"
  git -C "$LIVE" checkout -q --detach "$old"
  exit 1
}

if [[ -n $kit ]]; then
  say "${new:0:8} changes the kit: running stage0.sh"
  "$LIVE/dev/stage0/stage0.sh" || back "stage0.sh"
elif [[ -n $workbench && ${STAGE0_PROFILE:-control} == control ]]; then
  say "${new:0:8} changes the workbench: rebuilding it"
  (cd "$LIVE/workbench" && uv sync -q) || back "uv sync"
  (cd "$LIVE/workbench/frontend" \
    && { [[ -d node_modules ]] && ! grep -q '^workbench/frontend/package-lock.json$' <<< "$changed" || npm ci --silent; } \
    && npx vite build --logLevel warn) || back "the frontend build"
  systemctl --user restart ortus-stage0-workbench.service
fi
rm -f "$failed"
say "running ${new:0:8}"
