#!/usr/bin/env bash
# Start the stage-0 plan workbench (the ortus-stage0-workbench user unit runs
# this). Local mode: one password, kept as a hash in $STAGE0_HOME; the
# password itself is in $STAGE0_HOME/workbench-password (0600) for the
# operator. The workbench binds to loopback only. Planning sessions run the
# host's opencode with the live kit's agent configuration (Anthropic sign-in
# plugin only). It shows each review: ui story's preview from the record
# run-previews.sh keeps.
#
#   run-workbench.sh IDENTIFIER   the preview of a ui story's pull request
#                                 (ortus-stage0-preview@IDENTIFIER, preview.sh):
#                                 that branch's workbench, read-only toward the
#                                 tracker (WORKBENCH_SYNC=0), against the live
#                                 store and Sortie, with its own home and port;
#                                 a review job signs in there by the previews'
#                                 review key, which the egress gate gives it
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
# The other Sorties whose workers the workbench shows (agent-platform/run-supervision):
# each one as $STAGE0_HOME/sorties/<name>.env (its address on this host's
# loopback, and the SOCKS forward that reaches its agents' brokers on its
# host's loopback) and <name>.token (its control token). With one Sortie on
# p1 there is none: stage0.sh deletes those the retired huly-tunnel.sh left.
export WORKBENCH_SORTIE_NAME=${STAGE0_SORTIE_NAME:-$(hostname -s | tr '[:upper:]' '[:lower:]')}
peers=()
for f in "$STAGE0_HOME"/sorties/*.env; do
  [[ -e $f ]] || continue
  name=$(basename "$f" .env)
  url=$(sed -n 's/^SORTIE_URL=//p' "$f")
  [[ -n $url && -s $STAGE0_HOME/sorties/$name.token ]] || continue
  peers+=("$name=$url")
  var=$(tr 'a-z-' 'A-Z_' <<< "$name")
  export "WORKBENCH_SORTIE_TOKEN_${var}_FILE=$STAGE0_HOME/sorties/$name.token"
  proxy=$(sed -n 's/^SORTIE_BROKER_PROXY=//p' "$f")
  [[ -z $proxy ]] || export "WORKBENCH_SORTIE_PROXY_${var}=$proxy"
done
WORKBENCH_SORTIES=$(IFS=,; echo "${peers[*]}")
export WORKBENCH_SORTIES
# The operator's channel (notify-channel.sh): the workbench pushes each open
# question there with a link to the home on STAGE0_WORKBENCH_PUBLIC_URL, and
# again every 4 hours while it stays open. No kind, no channel.
notify=()
if [[ -n ${STAGE0_NOTIFY_KIND:-} ]]; then
  notify=(WORKBENCH_NOTIFY_KIND="$STAGE0_NOTIFY_KIND" WORKBENCH_NOTIFY_CHAT_ID="${STAGE0_NOTIFY_CHAT_ID:-}"
    WORKBENCH_NOTIFY_CREDENTIAL_FILE="$STAGE0_HOME/notify-credential")
fi
# The notifications each host's Sortie posts (notify-channel.sh): the
# workbench takes those with the host's token in this directory.
export WORKBENCH_NOTIFY_TOKENS=$STAGE0_HOME/notify-tokens
WB=$STAGE0_KIT_ROOT/workbench
home=$STAGE0_HOME/home sessions=$STAGE0_HOME/sessions port=$STAGE0_WORKBENCH_PORT
mode=(WORKBENCH_PREVIEWS="$STAGE0_HOME/previews.json")
if (( $# )); then
  [[ $1 =~ ^[A-Za-z0-9]+-[0-9]+$ ]] || { echo "run-workbench.sh: not a tracker identifier: $1" >&2; exit 2; }
  preview=$STAGE0_HOME/previews/$1
  # shellcheck source=/dev/null
  . "$preview/preview.env"
  WB=$preview/src/workbench home=$preview/home sessions=$preview/sessions port=$PREVIEW_PORT
  # A review job's requests carry the previews' review key, which the
  # egress gate sets (run-egress-gate.sh): they are its review's, signed in.
  mode=(WORKBENCH_SYNC=0 WORKBENCH_REVIEW_KEY_FILE="$STAGE0_HOME/preview-review-key")
fi
cd "$WB"
exec env WORKBENCH_HOME="$home" WORKBENCH_STORE="$STAGE0_STORE" WORKBENCH_HULY_PROJECT="$STAGE0_HULY_PROJECT" \
  WORKBENCH_SORTIE_URL="http://127.0.0.1:$STAGE0_SORTIE_PORT" WORKBENCH_SORTIE_TOKEN="$(cat "$STAGE0_HOME/sortie-token")" \
  WORKBENCH_PUBLIC_URL="${STAGE0_WORKBENCH_PUBLIC_URL:-http://127.0.0.1:$STAGE0_WORKBENCH_PORT}" "${notify[@]}" \
  WORKBENCH_PASSWORD_HASH="$(cat "$STAGE0_HOME/workbench-password-hash")" \
  WORKBENCH_SECRET_KEY="$(cat "$STAGE0_HOME/workbench-secret")" \
  WORKBENCH_RUNTIME_DIR="$sessions" WORKBENCH_FRONTEND_DIR="$WB/frontend/dist" \
  WORKBENCH_AGENT_COMMAND="env XDG_CONFIG_HOME=$STAGE0_KIT_ROOT/dev/live/agent-config opencode acp" \
  "${mode[@]}" "$WB/.venv/bin/workbench" serve --host 127.0.0.1 --port "$port"
