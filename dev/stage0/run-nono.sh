#!/usr/bin/env bash
# Start nono's proxy, the egress flow of the stage-1 edge (the
# ortus-stage0-nono user unit runs this). It listens on p1's loopback only,
# and only the edge reaches it, with the credential in $STAGE0_HOME/nono-pass.
# It allows the hosts in egress-profile.json and replaces each credential's
# placeholder on requests to its host, intercepting TLS with the kit's
# egress CA ($STAGE0_HOME/egress-ca). The credentials are read here at start
# (egress-credentials.sh) and live only in nono's memory.
#
# run-nono.sh live-proof (the ortus-stage0-nono-live-proof unit) starts the
# second one, of the live-proof profile: the same credentials and hosts,
# and those of egress-live-proof.json, on STAGE0_NONO_LIVE_PROOF_PORT. The
# edge sends it only the CONNECTs of a live-proof job that runs
# ($STAGE0_HOME/live-proofs/active, README.md "Live proofs").
#
# run-nono.sh homelab (the ortus-stage0-nono-homelab unit) starts the third,
# of homelab jobs: the same credentials, and only the hosts homelab declares
# in its egress-hosts.yaml ($STAGE0_HOME/egress-profile-homelab.json, which
# stage0.sh renders, homelab-egress.sh), on STAGE0_NONO_HOMELAB_PORT. The
# edge sends it the CONNECTs of every homelab job.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
die() { printf 'run-nono.sh: %s\n' "$*" >&2; exit 1; }
export PATH=$STAGE0_HOME/bin:$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin

profile=$KIT/egress-profile.json
port=${STAGE0_NONO_PORT:-7690}
case ${1:-} in
  '') ;;
  live-proof)
    profile=$STAGE0_HOME/egress-profile-live-proof.json
    port=${STAGE0_NONO_LIVE_PROOF_PORT:-7691}
    stage0-live-proofs profile -base "$KIT/egress-profile.json" -extra "$KIT/egress-live-proof.json" -out "$profile" \
      || die "could not render the live-proof profile"
    ;;
  homelab)
    profile=$STAGE0_HOME/egress-profile-homelab.json
    port=${STAGE0_NONO_HOMELAB_PORT:-7692}
    [[ -s $profile ]] || die "$profile is missing: stage0.sh renders it from homelab's egress-hosts.yaml"
    ;;
  *) die "usage: run-nono.sh [live-proof|homelab]" ;;
esac

# shellcheck source=egress-credentials.sh
. "$KIT/egress-credentials.sh"
egress_credentials || die "could not read the egress credentials"
# nono reads its credentials only here; refresh-token.sh restarts both
# nonos onto a fresher Claude token, using this expiry.
if [[ -z ${1:-} ]]; then "$KIT/anthropic-token.py" --expiry > "$STAGE0_HOME/nono-token-expiry"; fi
NONO_PROXY_PASS=$(cat "$STAGE0_HOME/nono-pass")
export NONO_PROXY_PASS NONO_NO_UPDATE_CHECK=1
# --silent keeps nono from printing its proxy URL, which holds the pass; -v
# logs each refusal to the journal.
exec nono --silent proxy -v --listen 127.0.0.1 --port "$port" \
  --profile "$profile" \
  --proxy-ca-cert "$STAGE0_HOME/egress-ca/ca.crt" --proxy-ca-key "$STAGE0_HOME/egress-ca/ca.key"
