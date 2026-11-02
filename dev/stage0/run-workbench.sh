#!/usr/bin/env bash
# Start the stage-0 plan workbench (the ortus-stage0-workbench user unit runs
# this). Local mode: one password, kept as a hash in $STAGE0_HOME; the
# password itself is in $STAGE0_HOME/workbench-password (0600) for the
# operator. The workbench binds to loopback only. Planning sessions run the
# host's opencode with the live kit's agent configuration (Anthropic sign-in
# plugin only).
set -euo pipefail

STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"

export PATH=$STAGE0_HOME/bin:$HOME/.local/bin:$HOME/.local/share/pnpm/bin:/usr/local/bin:/usr/bin:/bin
# The GitHub token Sortie has (run-sortie.sh), for looking up the pull
# requests a story names in delivered_by: no credential of its own. Exported,
# not passed on the command line, so it stays out of the process list.
export WORKBENCH_GITHUB_TOKEN
if [[ -s $STAGE0_HOME/github-token ]]; then
  WORKBENCH_GITHUB_TOKEN=$(cat "$STAGE0_HOME/github-token")
else
  WORKBENCH_GITHUB_TOKEN=$(gh auth token) || { echo "run-workbench.sh: gh has no token: run gh auth login" >&2; exit 1; }
fi
# Every other Sortie the kit runs, whose workers the home shows: each one
# huly-tunnel.sh reached, as $STAGE0_HOME/sorties/<name>.env (its address on
# this host's loopback) and <name>.token (its control token).
sorties=()
for f in "$STAGE0_HOME"/sorties/*.env; do
  [[ -e $f ]] || continue
  sorties+=("$(basename "$f" .env)=$(sed -n 's/^SORTIE_URL=//p' "$f")")
done
# This host's Sortie, by the name the home gives its worker.
name=$(hostname -s | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-\n' '-')
WB=$STAGE0_KIT_ROOT/workbench
cd "$WB"
exec env WORKBENCH_HOME="$STAGE0_HOME/home" WORKBENCH_STORE="$STAGE0_STORE" WORKBENCH_HULY_PROJECT="$STAGE0_HULY_PROJECT" \
  WORKBENCH_SORTIE_URL="http://127.0.0.1:$STAGE0_SORTIE_PORT" WORKBENCH_SORTIE_TOKEN="$(cat "$STAGE0_HOME/sortie-token")" \
  WORKBENCH_SORTIE_NAME="$name" WORKBENCH_SORTIES="${sorties[*]}" WORKBENCH_SORTIE_TOKEN_DIR="$STAGE0_HOME/sorties" \
  WORKBENCH_PASSWORD_HASH="$(cat "$STAGE0_HOME/workbench-password-hash")" \
  WORKBENCH_SECRET_KEY="$(cat "$STAGE0_HOME/workbench-secret")" \
  WORKBENCH_RUNTIME_DIR="$STAGE0_HOME/sessions" WORKBENCH_FRONTEND_DIR="$WB/frontend/dist" \
  WORKBENCH_AGENT_COMMAND="env XDG_CONFIG_HOME=$STAGE0_KIT_ROOT/dev/live/agent-config opencode acp" \
  "$WB/.venv/bin/workbench" serve --host 127.0.0.1 --port "$STAGE0_WORKBENCH_PORT"
