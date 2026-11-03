#!/usr/bin/env bash
# One pass of the previews of the review: ui stories' pull requests
# (ortus-stage0-previews.timer runs this every minute): `workbench
# preview-sync` deploys each one whose review and CI passed through
# preview.sh, comments its address and commit on the pull request (which
# starts the review on the preview), follows the branch,
# and removes the preview once the pull request closes. It keeps the record
# the live workbench shows them from, $STAGE0_HOME/previews.json.
#
# Each preview's address is a name beside the live workbench's TLS name,
# STAGE0_WORKBENCH_URL in stage0.env (https://workbench.example.net gives
# https://workbench-ons-93.example.net/), served by the same proxy from
# $STAGE0_HOME/previews/routes; without it, the preview's loopback port.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
if [[ -s $STAGE0_HOME/path ]]; then
  PATH=$(cat "$STAGE0_HOME/path")
  export PATH
fi
export WORKBENCH_GITHUB_TOKEN WORKBENCH_SORTIE_TOKEN
if [[ -s $STAGE0_HOME/github-token ]]; then
  WORKBENCH_GITHUB_TOKEN=$(cat "$STAGE0_HOME/github-token")
else
  WORKBENCH_GITHUB_TOKEN=$(gh auth token) || { echo "run-previews.sh: gh has no token: run gh auth login" >&2; exit 1; }
fi
WORKBENCH_SORTIE_TOKEN=$(cat "$STAGE0_HOME/sortie-token")
# A stage0.env written before this setting existed gets its default here, as
# stage0.sh gives it.
STAGE0_AO_REPO_PROJECT=${STAGE0_AO_REPO_PROJECT:-ortusnova/agentic-orchestration}
exec env WORKBENCH_SORTIE_URL="http://127.0.0.1:$STAGE0_SORTIE_PORT" WORKBENCH_HULY_PROJECT="$STAGE0_HULY_PROJECT" \
  "$STAGE0_KIT_ROOT/workbench/.venv/bin/workbench" preview-sync --repo "$STAGE0_AO_REPO_PROJECT" \
  --record "$STAGE0_HOME/previews.json" --deploy "$KIT/preview.sh" --live-url "${STAGE0_WORKBENCH_URL:-}" \
  --first-port "${STAGE0_PREVIEW_FIRST_PORT:-8800}"
