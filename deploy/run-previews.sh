#!/usr/bin/env bash
# One pass of the previews of the review: ui stories' pull requests
# (ortus-nova-<env>-previews.timer runs this every minute): `workbench
# preview-sync` deploys each one whose review and CI passed through
# preview.sh, checks that it answers at its address through the control
# plane's front, comments that address and its commit on the pull request
# (which starts the review on the preview), deploys again one that does not
# answer, follows the branch, and removes the preview once the pull request
# closes. It keeps the record the live workbench shows them from,
# $ORTUS_NOVA_HOME/previews.json.
#
# Each preview's address is http://<identifier>.<preview_domain>/, a name
# under Pomerium's public URL (front-config.sh): a job's machine on any
# worker opens it through its HTTP proxy, which reaches the egress gate on
# p1 through Pomerium with the worker's certificate and the job's token.
# The check takes the same route, with p1's own worker's certificate and
# Sortie's control token. People get a name beside the live workbench's TLS
# name too, ORTUS_NOVA_WORKBENCH_URL in stage0.env, when it is set
# (https://workbench.example.net gives https://workbench-ons-93.example.net/),
# served by the proxy in front of it from $ORTUS_NOVA_HOME/previews/routes.
set -euo pipefail
KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=names.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/names.sh"
env_load "${ORTUS_NOVA_ENV:?the unit names the environment (ORTUS_NOVA_ENV)}"
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
if [[ -s $ORTUS_NOVA_HOME/path ]]; then
  PATH=$(cat "$ORTUS_NOVA_HOME/path")
  export PATH
fi
export WORKBENCH_GITHUB_TOKEN WORKBENCH_SORTIE_TOKEN
if [[ -s $ORTUS_NOVA_HOME/github-token ]]; then
  WORKBENCH_GITHUB_TOKEN=$(cat "$ORTUS_NOVA_HOME/github-token")
else
  WORKBENCH_GITHUB_TOKEN=$(gh auth token) || { echo "run-previews.sh: gh has no token: run gh auth login" >&2; exit 1; }
fi
WORKBENCH_SORTIE_TOKEN=$(cat "$ORTUS_NOVA_HOME/sortie-token")
# A stage0.env written before this setting existed gets its default here, as
# deploy.sh gives it.
ORTUS_NOVA_AO_REPO_PROJECT=${ORTUS_NOVA_AO_REPO_PROJECT:-ortusnova/agentic-orchestration}
tls=$ORTUS_NOVA_PREVIEW_CHECK_TLS
exec env WORKBENCH_SORTIE_URL="http://127.0.0.1:$ORTUS_NOVA_SORTIE_PORT" WORKBENCH_HULY_PROJECT="$ORTUS_NOVA_HULY_PROJECT" \
  "$ORTUS_NOVA_KIT_ROOT/workbench/.venv/bin/workbench" preview-sync --repo "$ORTUS_NOVA_AO_REPO_PROJECT" \
  --record "$ORTUS_NOVA_HOME/previews.json" --deploy "$KIT/preview.sh" \
  --front-url "$ORTUS_NOVA_CP_PUBLIC_URL" --front-cert "$tls/worker.crt" --front-key "$tls/worker.key" --front-ca "$tls/ca.crt" \
  --live-url "${ORTUS_NOVA_WORKBENCH_URL:-}" --first-port "${ORTUS_NOVA_PREVIEW_FIRST_PORT:-8800}"
