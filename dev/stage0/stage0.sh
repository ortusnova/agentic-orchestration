#!/usr/bin/env bash
# The kit moved to deploy/ (spec store deploy-environments, design D6). A
# host's stage-0 deploy unit runs this path once more; deploy/deploy.sh dev
# rewrites the units onto the new paths. deploy-kit-push removes it.
export ORTUS_NOVA_HOME=${STAGE0_HOME:-} ORTUS_NOVA_LOCKED=${STAGE0_LOCKED:-}
exec "$(cd "$(dirname "$0")/../.." && pwd)/deploy/deploy.sh" dev "$@"
