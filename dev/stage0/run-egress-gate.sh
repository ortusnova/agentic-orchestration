#!/usr/bin/env bash
# Start the egress gate behind Pomerium's egress route (the
# ortus-stage0-egress-gate user unit runs this): control-plane/'s cp-egress,
# on p1's loopback. It admits a CONNECT only with a live egress job token
# (Sortie's token introspection, with the control token), logs it with the
# worker Pomerium names, the job and the attempt, and tunnels it through
# nono; a live-proof job that runs ($STAGE0_HOME/live-proofs/active)
# through the live-proof nono. A CONNECT for a ui story's preview, a name
# under preview_domain on port 80, it serves from the preview's port on
# p1's loopback ($STAGE0_HOME/previews/front-routes, which preview.sh
# writes), never through nono, and gives a review job's requests the
# previews' review key; Sortie's control token admits preview-sync's check
# that a preview answers there. While Sortie cannot answer, as while it
# restarts, it answers 503, which the worker retries.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
exec "$STAGE0_HOME/bin/cp-egress" -listen "127.0.0.1:$STAGE0_EGRESS_GATE_PORT" \
  -sortie "http://127.0.0.1:$STAGE0_SORTIE_PORT" -control-token-file "$STAGE0_HOME/sortie-token" \
  -nono "127.0.0.1:$STAGE0_NONO_PORT" -nono-pass-file "$STAGE0_HOME/nono-pass" \
  -live-proof-nono "127.0.0.1:$STAGE0_NONO_LIVE_PROOF_PORT" -live-proof-jobs-file "$STAGE0_LIVE_PROOFS/active" \
  -preview-domain "$(preview_domain)" -previews-file "$STAGE0_PREVIEW_FRONT_ROUTES" \
  -preview-review-key-file "$STAGE0_PREVIEW_REVIEW_KEY"
