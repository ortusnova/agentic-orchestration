#!/usr/bin/env bash
# Start nono's proxy, the egress flow of the stage-1 edge (the
# ortus-stage0-nono user unit runs this). It listens on p1's loopback only,
# and only the edge reaches it, with the credential in $STAGE0_HOME/nono-pass.
# It allows the hosts in egress-profile.json and replaces each credential's
# placeholder on requests to its host, intercepting TLS with the kit's
# egress CA ($STAGE0_HOME/egress-ca). The credentials are read here at start
# (egress-credentials.sh) and live only in nono's memory.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
die() { printf 'run-nono.sh: %s\n' "$*" >&2; exit 1; }
export PATH=$STAGE0_HOME/bin:$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin

# shellcheck source=egress-credentials.sh
. "$KIT/egress-credentials.sh"
egress_credentials || die "could not read the egress credentials"
NONO_PROXY_PASS=$(cat "$STAGE0_HOME/nono-pass")
export NONO_PROXY_PASS NONO_NO_UPDATE_CHECK=1
# --silent keeps nono from printing its proxy URL, which holds the pass; -v
# logs each refusal to the journal.
exec nono --silent proxy -v --listen 127.0.0.1 --port "${STAGE0_NONO_PORT:-7690}" \
  --profile "$KIT/egress-profile.json" \
  --proxy-ca-cert "$STAGE0_HOME/egress-ca/ca.crt" --proxy-ca-key "$STAGE0_HOME/egress-ca/ca.key"
