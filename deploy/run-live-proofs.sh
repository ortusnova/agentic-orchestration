#!/usr/bin/env bash
# One pass of the live proofs (ortus-nova-dev-live-proofs.timer runs this every
# minute; README.md "Live proofs"): live-proofs reads each live-proof
# request the supervisor made of Sortie, holds its pull request's merge with
# the live-proof status, starts the live-proof job as a fix run on the
# story's branch, lists the jobs that run for the egress gate's live-proof egress,
# and sets the status from the job's result comment. It keeps its record in
# $ORTUS_NOVA_HOME/live-proofs.
set -euo pipefail
# shellcheck source=names.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/names.sh"
env_load "${ORTUS_NOVA_ENV:?the unit names the environment (ORTUS_NOVA_ENV)}"
if [[ -s $ORTUS_NOVA_HOME/path ]]; then
  PATH=$(cat "$ORTUS_NOVA_HOME/path")
  export PATH
fi
export ORTUS_NOVA_LIVE_PROOFS_GITHUB_TOKEN ORTUS_NOVA_LIVE_PROOFS_SORTIE_TOKEN
if [[ -s $ORTUS_NOVA_HOME/github-token ]]; then
  ORTUS_NOVA_LIVE_PROOFS_GITHUB_TOKEN=$(cat "$ORTUS_NOVA_HOME/github-token")
else
  ORTUS_NOVA_LIVE_PROOFS_GITHUB_TOKEN=$(gh auth token) || { echo "run-live-proofs.sh: gh has no token: run gh auth login" >&2; exit 1; }
fi
ORTUS_NOVA_LIVE_PROOFS_SORTIE_TOKEN=$(cat "$ORTUS_NOVA_HOME/sortie-token")
exec "$ORTUS_NOVA_HOME/bin/live-proofs" sync -dir "$ORTUS_NOVA_HOME/live-proofs" -sortie "http://127.0.0.1:$ORTUS_NOVA_SORTIE_PORT"
