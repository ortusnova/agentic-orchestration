#!/usr/bin/env bash
# The kit moved to deploy/ (spec store deploy-environments, design D6): the
# follower is deploy/follow.sh. deploy-kit-push removes this.
export ORTUS_NOVA_HOME=${STAGE0_HOME:-}
exec "$(cd "$(dirname "$0")/../.." && pwd)/deploy/follow.sh" dev "$@"
