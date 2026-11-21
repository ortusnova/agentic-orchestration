#!/usr/bin/env bash
# Development mode for the plan workbench, from a blank slate.
#
#   dev/dev.sh           start (the first run creates an empty environment)
#   dev/dev.sh --reset   wipe the environment back to empty, then start
#
# The environment is yours alone: a workbench home with one project, "store",
# whose empty spec store syncs into the Huly project DEV, and a Sortie under
# plan control that dispatches only DEV with the mock agent. Approve stories
# and sync: Sortie releases them, the mock agent "works" on each, and its
# pull request opens in a local Gitea (dev/forge/gitea.sh, repository
# sortie/$FORGE_REPO, default dev-lab, on http://127.0.0.1:3001) while the story waits In Review.
# Merge the pull request in Gitea and Sortie marks the story Done and releases
# what it unblocks. Projects you create in the workbench live in the same home
# and get Huly projects of their own; Sortie plans and syncs them but runs
# agents for DEV only. Work persists across runs until --reset.
#
# Runs Sortie, the backend (reloads on Python edits), and Vite (hot reload)
# in this terminal; Ctrl-C stops them. A planning session keeps running
# across restarts; end it from the chat pane, or with --reset.
#
# Settings come from the environment, or from dev/.env if it exists (see
# dev/.env.example for every setting and its default).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
if [[ -f $ROOT/dev/.env ]]; then set -a; source "$ROOT/dev/.env"; set +a; fi
# shellcheck source=stage0/huly-restart-policy.sh
. "$ROOT/dev/stage0/huly-restart-policy.sh"
WB=$ROOT/workbench
SORTIE_SRC=${SORTIE_SRC:-$ROOT/sortie}
BROKER_SRC=${BROKER_SRC:-$ROOT/acp-broker}
DEV_BIN=${DEV_BIN:-$ROOT/.bin}
DEV_HOME=${DEV_HOME:-$ROOT/.dev}
STORE=$DEV_HOME/store
WB_HOME=$DEV_HOME/home
RUN_DIR=$DEV_HOME/sortie
RUNTIME=$DEV_HOME/sessions
HULY_URL=${HULY_URL:-http://localhost:8087}
HULY_COMPOSE_DIR=${HULY_COMPOSE_DIR:-$HOME/dev/huly-selfhost}
HULY_WORKSPACE=${HULY_WORKSPACE:-sortie}
HULY_PROJECT=${HULY_PROJECT:-DEV}
HULY_EMAIL=${HULY_EMAIL:-sortie@example.com}
HULY_PASSWORD=${HULY_PASSWORD:-sortie-dev-pass}
FORGE_REPO=${FORGE_REPO:-dev-lab}
DEV_AGENT=${DEV_AGENT:-mock}
TOKEN_FILE=${SORTIE_TOKEN_FILE:-$HOME/.config/plan-workbench-sortie-token}
HASH_FILE=${PASSWORD_HASH_FILE:-$HOME/.config/plan-workbench-password-hash}
SORTIE_PORT=${SORTIE_PORT:-7678}
WB_PORT=${WB_PORT:-8790}
VITE_PORT=${VITE_PORT:-5173}
RESET=
[[ ${1:-} == --reset ]] && RESET=1
[[ $# -gt 0 && -z $RESET ]] && { echo "usage: $0 [--reset]" >&2; exit 2; }

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'dev.sh: %s\n' "$*" >&2; exit 1; }

for port in "$SORTIE_PORT" "$WB_PORT" "$VITE_PORT"; do
  ss -ltn | grep -q ":$port " && die "port $port is in use; stop whatever runs there first"
done

mkdir -p "$(dirname "$TOKEN_FILE")" "$(dirname "$HASH_FILE")"
[[ -s $TOKEN_FILE ]] || { openssl rand -hex 24 > "$TOKEN_FILE"; chmod 600 "$TOKEN_FILE"; }
if [[ ! -s $HASH_FILE ]]; then
  say "Choose the password you will sign in to the workbench with"
  (cd "$WB" && uv run workbench hash-password) > "$HASH_FILE"
  chmod 600 "$HASH_FILE"
fi

end_sessions() { # end the planning sessions still running under $RUNTIME
  local f pid
  for f in "$RUNTIME"/state.json "$RUNTIME"/*/state.json; do
    [[ -f $f ]] || continue
    pid=$(jq -r '.driver_pid // empty' "$f")
    [[ -n $pid ]] && kill -TERM "$pid" 2>/dev/null && sleep 2 || true
  done
}

if [[ -n $RESET ]]; then
  say "Resetting the dev environment"
  end_sessions
  rm -rf "$STORE" "$RUN_DIR" "$RUNTIME" "$WB_HOME" "$DEV_HOME/workspaces"
  # The dev repository only: Gitea and the live run's repository stay.
  if [[ -s $DEV_HOME/gitea-token ]]; then
    curl -sf -o /dev/null -X DELETE -H "Authorization: token $(cat "$DEV_HOME/gitea-token")" \
      "http://127.0.0.1:${GITEA_PORT:-3001}/api/v1/repos/sortie/$FORGE_REPO" || true
  fi
elif [[ -f $RUNTIME/state.json ]]; then
  # Sessions now live in one runtime directory per project, under $RUNTIME.
  say "Ending the planning session from before projects (its runtime layout changed)"
  end_sessions
  find "$RUNTIME" -maxdepth 1 -type f -delete
  rm -rf "$RUNTIME/registry"
fi

say "Installing workbench dependencies"
(cd "$WB" && uv sync -q)
[[ -d $WB/frontend/node_modules ]] || (cd "$WB/frontend" && npm install --silent)

if [[ ! -d $STORE/openspec ]]; then
  say "Creating an empty spec store in $STORE"
  (cd "$WB" && uv run python -c 'import sys; from workbench.scaffold import scaffold_store; scaffold_store(sys.argv[1], "dev")' "$STORE")
fi
mkdir -p "$RUN_DIR"

say "Building sortie and acp-broker into $DEV_BIN"
[[ -f $SORTIE_SRC/go.mod ]] || die "no Sortie source in $SORTIE_SRC; run: git submodule update --init"
mkdir -p "$DEV_BIN"
# Sortie is not in go.work: build it from its own go.mod alone.
(cd "$SORTIE_SRC" && GOWORK=off go build -o "$DEV_BIN/sortie" ./cmd/sortie)
(cd "$BROKER_SRC" && go build -o "$DEV_BIN/acp-broker" ./cmd/acp-broker)
# The workbench and Sortie start acp-broker by name.
export PATH=$DEV_BIN:$PATH

# Every Huly container runs, not just its front end (huly-restart-policy.sh).
if [[ -f $HULY_COMPOSE_DIR/compose.yml ]]; then
  CONTAINERS_REGISTRIES_CONF=$HULY_COMPOSE_DIR/.podman-registries.conf huly_ensure "$HULY_COMPOSE_DIR" docker compose \
    || die "could not start Huly in $HULY_COMPOSE_DIR"
fi
for _ in $(seq 60); do curl -sf -o /dev/null "$HULY_URL/config.json" && break; sleep 2; done
HULY_TOKEN=$(python3 "$ROOT/dev/huly_project.py" "$HULY_URL" "$HULY_WORKSPACE" "$HULY_EMAIL" "$HULY_PASSWORD" \
  "$HULY_PROJECT" ${RESET:+--reset}) || die "could not prepare Huly project $HULY_PROJECT at $HULY_URL"

say "Preparing the local Gitea"
eval "$(GITEA_HOME=$DEV_HOME GITEA_REPO=$FORGE_REPO "$ROOT/dev/forge/gitea.sh" up)"
case $DEV_AGENT in
mock) AGENT_KIND=mock AGENT_COMMAND= ;;
acp)
  # Workers' brokers serve viewers over HTTP, so the workbench can watch them.
  [[ -s $DEV_HOME/viewer-token ]] || { openssl rand -hex 24 > "$DEV_HOME/viewer-token"; chmod 600 "$DEV_HOME/viewer-token"; }
  AGENT_KIND=agent-client-protocol
  AGENT_COMMAND="acp-broker -http 127.0.0.1:0 -viewer-token-file $DEV_HOME/viewer-token -- python3 $ROOT/dev/mock_acp_agent.py --turn-seconds 20"
  ;;
*) die "DEV_AGENT is mock or acp, not $DEV_AGENT" ;;
esac
DEV_AGENT_KIND=$AGENT_KIND DEV_AGENT_COMMAND=$AGENT_COMMAND \
DEV_HOME=$DEV_HOME DEV_HULY_URL=$HULY_URL DEV_HULY_TOKEN=$HULY_TOKEN DEV_HULY_PROJECT=$HULY_PROJECT \
DEV_HULY_WORKSPACE=$HULY_WORKSPACE DEV_SORTIE_PORT=$SORTIE_PORT DEV_SORTIE_TOKEN=$(cat "$TOKEN_FILE") \
DEV_FORGE_URL=$FORGE_URL DEV_FORGE_PROJECT=$FORGE_PROJECT DEV_FORGE_TOKEN=$FORGE_TOKEN \
  python3 "$ROOT/dev/forge/render.py" "$ROOT/dev/WORKFLOW.md" "$RUN_DIR/WORKFLOW.md"
(cd "$RUN_DIR" && sortie validate WORKFLOW.md >/dev/null) || die "Sortie refused the dev workflow; run: cd $RUN_DIR && sortie validate WORKFLOW.md"

PGIDS=()
start() { # name dir command...; each runs in its own process group so Ctrl-C stops it whole
  local name=$1 dir=$2; shift 2
  (cd "$dir" && exec setsid "$@") > >(sed -u "s/^/[$name] /") 2>&1 &
  PGIDS+=("$!")
}
stop() {
  trap - INT TERM EXIT
  say "Stopping (a running planning session keeps going; end it in the chat pane)"
  for pg in "${PGIDS[@]}"; do kill -TERM -- "-$pg" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap stop INT TERM EXIT

say "Starting Sortie, the workbench backend, and Vite"
# Hooks see SORTIE_* variables only: the forge coordinates go to them that way.
SORTIE_FORGE_KIT=$ROOT/dev/forge SORTIE_FORGE_REMOTE=$FORGE_URL/$FORGE_PROJECT.git SORTIE_FORGE_URL=$FORGE_URL \
SORTIE_FORGE_PROJECT=$FORGE_PROJECT SORTIE_FORGE_TOKEN=$FORGE_TOKEN SORTIE_FORGE_PLACEHOLDER=1 \
  start sortie "$RUN_DIR" sortie WORKFLOW.md

WORKBENCH_HOME=$WB_HOME WORKBENCH_STORE=$STORE WORKBENCH_HULY_PROJECT=$HULY_PROJECT WORKBENCH_SORTIE_URL=http://127.0.0.1:$SORTIE_PORT \
WORKBENCH_SORTIE_TOKEN=$(cat "$TOKEN_FILE") WORKBENCH_PASSWORD_HASH=$(cat "$HASH_FILE") \
WORKBENCH_SECRET_KEY=dev-secret WORKBENCH_RUNTIME_DIR=$RUNTIME \
  start backend "$WB" uv run workbench serve --port "$WB_PORT" --reload

WORKBENCH_BACKEND=http://127.0.0.1:$WB_PORT start vite "$WB/frontend" npm run dev -- --host 127.0.0.1 --port "$VITE_PORT" --strictPort

ready=
for _ in $(seq 90); do
  # The backend first (asking through Vite before it is up logs proxy errors),
  # then Vite and its proxy: 401 from /api/me means both answer.
  [[ $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$WB_PORT/api/me) == 401 ]] &&
    [[ $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$VITE_PORT/api/me) == 401 ]] && { ready=1; break; }
  sleep 1
done
[[ -n $ready ]] || die "the workbench did not come up; see the [backend] and [vite] lines above"
say "Ready: open http://127.0.0.1:$VITE_PORT   (Huly project $HULY_PROJECT: $HULY_URL/workbench/$HULY_WORKSPACE/tracker; pull requests: $FORGE_URL/$FORGE_PROJECT/pulls, sortie / Live-Gitea-Pw1)   Ctrl-C to stop"
wait
