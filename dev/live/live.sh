#!/usr/bin/env bash
# The live run: the plan workbench, Sortie under plan control, real agents,
# a local Gitea, and the local Huly, end to end.
#
#   dev/live/live.sh           start (the first run sets everything up)
#   dev/live/live.sh --reset   wipe the run's state, Gitea, and the Huly project's issues, then start
#
# Sets up, each step idempotent:
#   - Huly project LIVE in workspace "sortie", with its space type ready for
#     plan control (dev/huly_project.py);
#   - Gitea on 127.0.0.1:3001 with the repository sortie/live-lab (dev/forge/gitea.sh);
#   - a spec store holding the OpenSpec change "pricing" (store/), which the
#     planning agent breaks into stories;
#   - Sortie on 127.0.0.1:7679, from WORKFLOW.md, running `opencode acp`
#     behind acp-broker, with the agent configuration in agent-config/;
#   - the workbench on http://127.0.0.1:8791, local mode, password "live".
# Then: plan and approve in the workbench (approved stories sync on their own); watch and steer the workers;
# review and merge their pull requests in Gitea; Sortie moves merged stories
# to Done and releases what they unblock. Ctrl-C stops Sortie and the
# workbench; Gitea keeps running (dev/forge/gitea.sh down removes it).
#
# Needs: Docker (Huly in HULY_COMPOSE_DIR, or already running), Go, uv,
# npm, the openspec CLI, and opencode signed in to Anthropic.
# Settings: LIVE_HOME (~/.cache/plan-workbench-live), SORTIE_SRC (sortie/), BROKER_SRC (acp-broker/),
# HULY_COMPOSE_DIR (~/dev/huly-selfhost),
# HULY_URL, HULY_WORKSPACE, HULY_EMAIL, HULY_PASSWORD, LIVE_PROJECT (LIVE).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=../stage0/huly-restart-policy.sh
. "$ROOT/dev/stage0/huly-restart-policy.sh"
WB=$ROOT/workbench
KIT=$ROOT/dev/live
FORGE=$ROOT/dev/forge
export LIVE_HOME=${LIVE_HOME:-$HOME/.cache/plan-workbench-live}
SORTIE_SRC=${SORTIE_SRC:-$ROOT/sortie}
BROKER_SRC=${BROKER_SRC:-$ROOT/acp-broker}
HULY_COMPOSE_DIR=${HULY_COMPOSE_DIR:-$HOME/dev/huly-selfhost}
HULY_URL=${HULY_URL:-http://localhost:8087}
HULY_WORKSPACE=${HULY_WORKSPACE:-sortie}
HULY_EMAIL=${HULY_EMAIL:-sortie@example.com}
HULY_PASSWORD=${HULY_PASSWORD:-sortie-dev-pass}
LIVE_PROJECT=${LIVE_PROJECT:-LIVE}
SORTIE_PORT=7679
WB_PORT=8791
RESET=
[[ ${1:-} == --reset ]] && RESET=1
[[ $# -gt 0 && -z $RESET ]] && { echo "usage: $0 [--reset]" >&2; exit 2; }

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'live.sh: %s\n' "$*" >&2; exit 1; }
for port in $SORTIE_PORT $WB_PORT; do ss -ltn | grep -q ":$port " && die "port $port is in use"; done

if [[ -n $RESET ]]; then
  say "Resetting the live run"
  GITEA_HOME=$LIVE_HOME "$FORGE/gitea.sh" down
  rm -rf "$LIVE_HOME"
fi
mkdir -p "$LIVE_HOME/bin" "$LIVE_HOME/sortie"

say "Building sortie and acp-broker into $LIVE_HOME/bin"
(cd "$SORTIE_SRC" && GOWORK=off go build -o "$LIVE_HOME/bin/sortie" ./cmd/sortie) && (cd "$BROKER_SRC" && go build -o "$LIVE_HOME/bin/acp-broker" ./cmd/acp-broker)
export PATH=$LIVE_HOME/bin:$PATH
say "Installing workbench dependencies and building the frontend"
(cd "$WB" && uv sync -q)
(cd "$WB/frontend" && { [[ -d node_modules ]] || npm install --silent; } && npx vite build --logLevel warn)

# Every Huly container runs, not just its front end (huly-restart-policy.sh).
if [[ -f $HULY_COMPOSE_DIR/compose.yml ]]; then
  huly_ensure "$HULY_COMPOSE_DIR" docker compose || die "could not start Huly in $HULY_COMPOSE_DIR"
fi
for _ in $(seq 60); do curl -sf -o /dev/null "$HULY_URL/config.json" && break; sleep 2; done
say "Preparing Huly project $LIVE_PROJECT"
HULY_TOKEN=$(python3 "$ROOT/dev/huly_project.py" "$HULY_URL" "$HULY_WORKSPACE" "$HULY_EMAIL" "$HULY_PASSWORD" \
  "$LIVE_PROJECT" ${RESET:+--reset}) || die "could not prepare Huly project $LIVE_PROJECT"

say "Preparing Gitea"
eval "$(GITEA_HOME=$LIVE_HOME GITEA_REPO=live-lab "$FORGE/gitea.sh" up)"

for f in sortie-token viewer-token; do
  [[ -s $LIVE_HOME/$f ]] || { openssl rand -hex 24 > "$LIVE_HOME/$f"; chmod 600 "$LIVE_HOME/$f"; }
done
rm -rf "$LIVE_HOME/agent-config" && cp -r "$KIT/agent-config" "$LIVE_HOME/agent-config"

if [[ ! -d $LIVE_HOME/store/openspec ]]; then
  say "Creating the spec store with the change \"pricing\""
  (cd "$WB" && uv run python - "$LIVE_HOME/store" "$KIT/store" <<'EOF'
import shutil, sys
from pathlib import Path
from workbench.scaffold import _git, scaffold_store
store, seed = Path(sys.argv[1]), Path(sys.argv[2])
scaffold_store(store, "live")
shutil.copytree(seed / "changes", store / "openspec" / "changes", dirs_exist_ok=True)
(store / "openspec" / "changes" / "pricing" / ".openspec.yaml").write_text("schema: spec-driven-planned\n")
(store / "openspec" / "plan.yaml").write_text("schema_version: 1\nchanges:\n  - name: pricing\n    depends_on: []\n")
_git(store, "add", "-A")
_git(store, "-c", "user.name=live", "-c", "user.email=live@localhost", "commit", "-qm", "Add the change pricing")
EOF
  )
fi

say "Rendering Sortie's workflow"
LIVE_HULY_URL=$HULY_URL LIVE_HULY_TOKEN=$HULY_TOKEN LIVE_HULY_PROJECT=$LIVE_PROJECT LIVE_HULY_WORKSPACE=$HULY_WORKSPACE \
LIVE_SORTIE_PORT=$SORTIE_PORT LIVE_SORTIE_TOKEN=$(cat "$LIVE_HOME/sortie-token") \
LIVE_FORGE_URL=$FORGE_URL LIVE_FORGE_PROJECT=$FORGE_PROJECT LIVE_FORGE_TOKEN=$FORGE_TOKEN \
  python3 "$FORGE/render.py" "$KIT/WORKFLOW.md" "$LIVE_HOME/sortie/WORKFLOW.md"
(cd "$LIVE_HOME/sortie" && sortie validate WORKFLOW.md) || die "Sortie refused the workflow"

PIDS=()
stop() {
  trap - INT TERM EXIT
  say "Stopping Sortie and the workbench (Gitea keeps running)"
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap stop INT TERM EXIT
prefix() { sed -u "s/^/[$1] /"; }

say "Starting Sortie on 127.0.0.1:$SORTIE_PORT"
# Hooks see SORTIE_* variables only: the forge coordinates go to them that way.
(cd "$LIVE_HOME/sortie" && exec env SORTIE_FORGE_KIT="$FORGE" SORTIE_FORGE_REMOTE="$FORGE_URL/$FORGE_PROJECT.git" \
  SORTIE_FORGE_URL="$FORGE_URL" SORTIE_FORGE_PROJECT="$FORGE_PROJECT" SORTIE_FORGE_TOKEN="$FORGE_TOKEN" \
  sortie WORKFLOW.md) > >(prefix sortie) 2>&1 &
PIDS+=("$!")

say "Starting the workbench on http://127.0.0.1:$WB_PORT"
(cd "$WB" && exec env WORKBENCH_HOME="$LIVE_HOME/home" WORKBENCH_STORE="$LIVE_HOME/store" WORKBENCH_HULY_PROJECT="$LIVE_PROJECT" \
  WORKBENCH_SORTIE_URL="http://127.0.0.1:$SORTIE_PORT" WORKBENCH_SORTIE_TOKEN="$(cat "$LIVE_HOME/sortie-token")" \
  WORKBENCH_PASSWORD_HASH="$(uv run python -c 'from workbench.auth import hash_password; print(hash_password("live"))')" \
  WORKBENCH_SECRET_KEY=live WORKBENCH_RUNTIME_DIR="$LIVE_HOME/sessions" WORKBENCH_FRONTEND_DIR="$WB/frontend/dist" \
  WORKBENCH_AGENT_COMMAND="env XDG_CONFIG_HOME=$LIVE_HOME/agent-config opencode acp" PATH="$PATH" \
  uv run workbench serve --port "$WB_PORT") > >(prefix workbench) 2>&1 &
PIDS+=("$!")

for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$WB_PORT/api/auth/mode" && break; sleep 1; done
say "Ready: workbench http://127.0.0.1:$WB_PORT (password \"live\"), Gitea $FORGE_URL/$FORGE_PROJECT (sortie / Live-Gitea-Pw1), Huly $HULY_URL/workbench/$HULY_WORKSPACE/tracker   Ctrl-C to stop"
wait
