#!/usr/bin/env bash
# Start the egress gate behind Pomerium's egress route (the
# ortus-nova-<env>-egress-gate user unit runs this): control-plane/'s cp-egress,
# on p1's loopback. It admits a CONNECT only with a live egress job token
# (Sortie's token introspection, with the control token), logs it with the
# worker Pomerium names, the job and the attempt, and tunnels it through
# nono; a live-proof job that runs ($ORTUS_NOVA_HOME/live-proofs/active)
# through the live-proof nono. A CONNECT for a ui story's preview, a name
# under preview_domain on port 80, it serves from the preview's port on
# p1's loopback ($ORTUS_NOVA_HOME/previews/front-routes, which preview.sh
# writes), never through nono, and gives a review job's requests the
# previews' review key; Sortie's control token admits preview-sync's check
# that a preview answers there. Sortie registers each attempt's GitHub
# tokens with it (POST /v1/forge-tokens, the control token): while the
# attempt runs, its CONNECTs for github.com and api.github.com go to a nono
# the gate starts for it, with the kit's nono and egress CA, whose token
# files are in $ORTUS_NOVA_FORGE_TOKENS_DIR (front-config.sh). While Sortie
# cannot answer, as while it restarts, it answers 503, which the worker
# retries.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=names.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/names.sh"
env_load "${ORTUS_NOVA_ENV:?the unit names the environment (ORTUS_NOVA_ENV)}"
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
exec "$ORTUS_NOVA_HOME/bin/cp-egress" -listen "127.0.0.1:$ORTUS_NOVA_EGRESS_GATE_PORT" \
  -sortie "http://127.0.0.1:$ORTUS_NOVA_SORTIE_PORT" -control-token-file "$ORTUS_NOVA_HOME/sortie-token" \
  -nono "127.0.0.1:$ORTUS_NOVA_NONO_PORT" -nono-pass-file "$ORTUS_NOVA_HOME/nono-pass" \
  -live-proof-nono "127.0.0.1:$ORTUS_NOVA_NONO_LIVE_PROOF_PORT" -live-proof-jobs-file "$ORTUS_NOVA_LIVE_PROOFS/active" \
  -preview-domain "$(preview_domain)" -previews-file "$ORTUS_NOVA_PREVIEW_FRONT_ROUTES" \
  -preview-review-key-file "$ORTUS_NOVA_PREVIEW_REVIEW_KEY" \
  -forge-nono "$ORTUS_NOVA_HOME/bin/nono" -forge-ca-cert "$ORTUS_NOVA_HOME/egress-ca/ca.crt" -forge-ca-key "$ORTUS_NOVA_HOME/egress-ca/ca.key" \
  -forge-dir "$ORTUS_NOVA_FORGE_TOKENS_DIR"
