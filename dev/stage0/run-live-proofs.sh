#!/usr/bin/env bash
# One pass of the live proofs (ortus-stage0-live-proofs.timer runs this every
# minute; README.md "Live proofs"): stage0-live-proofs reads each live-proof
# request the supervisor made of Sortie, holds its pull request's merge with
# the live-proof status, starts the live-proof job as a fix run on the
# story's branch, lists the jobs that run for the egress gate's live-proof egress,
# and sets the status from the job's result comment. It keeps its record in
# $STAGE0_HOME/live-proofs.
set -euo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
if [[ -s $STAGE0_HOME/path ]]; then
  PATH=$(cat "$STAGE0_HOME/path")
  export PATH
fi
export STAGE0_LIVE_PROOFS_GITHUB_TOKEN STAGE0_LIVE_PROOFS_SORTIE_TOKEN
if [[ -s $STAGE0_HOME/github-token ]]; then
  STAGE0_LIVE_PROOFS_GITHUB_TOKEN=$(cat "$STAGE0_HOME/github-token")
else
  STAGE0_LIVE_PROOFS_GITHUB_TOKEN=$(gh auth token) || { echo "run-live-proofs.sh: gh has no token: run gh auth login" >&2; exit 1; }
fi
STAGE0_LIVE_PROOFS_SORTIE_TOKEN=$(cat "$STAGE0_HOME/sortie-token")
exec "$STAGE0_HOME/bin/stage0-live-proofs" sync -dir "$STAGE0_HOME/live-proofs" -sortie "http://127.0.0.1:$STAGE0_SORTIE_PORT"
