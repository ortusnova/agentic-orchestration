#!/usr/bin/env bash
# Start nono's proxy, the egress flow behind Pomerium's egress route (the
# ortus-nova-<env>-nono user unit runs this). It listens on p1's loopback only,
# and only the egress gate reaches it, with the credential in
# $ORTUS_NOVA_HOME/nono-pass. It allows the hosts in the control plane's egress
# profile (control-plane/nono/egress-profile.json, the compose project's
# too) and replaces each credential's placeholder on requests to its host,
# intercepting TLS with the kit's egress CA ($ORTUS_NOVA_HOME/egress-ca). The
# credentials are read here at start (egress-credentials.sh) and live only
# in nono's memory.
#
# run-nono.sh live-proof (the ortus-nova-<env>-nono-live-proof unit) starts the
# second one, of the live-proof profile: the same credentials and hosts,
# and those of egress-live-proof.json, on ORTUS_NOVA_NONO_LIVE_PROOF_PORT. The
# egress gate sends it only the CONNECTs of a live-proof job that runs
# ($ORTUS_NOVA_HOME/live-proofs/active, README.md "Live proofs").
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
PROFILE=$(cd "$KIT/../control-plane/nono" && pwd)/egress-profile.json
# shellcheck source=names.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/names.sh"
env_load "${ORTUS_NOVA_ENV:?the unit names the environment (ORTUS_NOVA_ENV)}"
die() { printf 'run-nono.sh: %s\n' "$*" >&2; exit 1; }
export PATH=$ORTUS_NOVA_HOME/bin:$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin

profile=$PROFILE
port=${ORTUS_NOVA_NONO_PORT:-7690}
case ${1:-} in
  '') ;;
  live-proof)
    profile=$ORTUS_NOVA_HOME/egress-profile-live-proof.json
    port=${ORTUS_NOVA_NONO_LIVE_PROOF_PORT:-7691}
    live-proofs profile -base "$PROFILE" -extra "$KIT/egress-live-proof.json" -out "$profile" \
      || die "could not render the live-proof profile"
    ;;
  *) die "usage: run-nono.sh [live-proof]" ;;
esac

# shellcheck source=egress-credentials.sh
. "$KIT/egress-credentials.sh"
egress_credentials || die "could not read the egress credentials"
# nono reads its credentials only here; refresh-token.sh restarts both
# nonos onto a fresher Claude token, using this expiry.
if [[ -z ${1:-} ]]; then "$KIT/anthropic-token.py" --expiry > "$ORTUS_NOVA_HOME/nono-token-expiry"; fi
NONO_PROXY_PASS=$(cat "$ORTUS_NOVA_HOME/nono-pass")
export NONO_PROXY_PASS NONO_NO_UPDATE_CHECK=1
# --silent keeps nono from printing its proxy URL, which holds the pass; -v
# logs each refusal to the journal.
exec nono --silent proxy -v --listen 127.0.0.1 --port "$port" \
  --profile "$profile" \
  --proxy-ca-cert "$ORTUS_NOVA_HOME/egress-ca/ca.crt" --proxy-ca-key "$ORTUS_NOVA_HOME/egress-ca/ca.key"
