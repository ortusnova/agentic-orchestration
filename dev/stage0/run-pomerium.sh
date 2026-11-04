#!/usr/bin/env bash
# Start Pomerium, the only door of the control plane for every worker (the
# ortus-stage0-pomerium user unit runs this): the image the compose project
# pins by digest, under rootless podman, on the host's network, on the port
# of the public URL. It requires a client certificate from the control
# plane's CA on every connection, admits on each route only the workers
# $STAGE0_HOME/control-plane/workers.json lists, sets Sortie's identity
# header from the certificate, and routes to Sortie and the egress gate on
# p1's loopback ($STAGE0_HOME/control-plane/pomerium/config.yaml, which
# cpctl renders; worker-host.sh restarts this onto a new list). Its
# certificate and key enter the container as podman secrets.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
die() { printf 'run-pomerium.sh: %s\n' "$*" >&2; exit 1; }

image=$(pomerium_image)
[[ -n $image ]] || die "control-plane/compose.yaml pins no Pomerium image"
secrets=$STAGE0_CP_HOME/secrets
for s in cp-pomerium-cert cp-pomerium-key; do
  [[ -s $secrets/$s ]] || die "$secrets/$s is missing: run dev/stage0/stage0.sh"
  podman secret create --replace "ortus-stage0-${s#cp-}" "$secrets/$s" > /dev/null
done
exec podman run --rm --replace --name ortus-stage0-pomerium --network host \
  --secret ortus-stage0-pomerium-cert,target=cp-pomerium-cert \
  --secret ortus-stage0-pomerium-key,target=cp-pomerium-key \
  -v "$STAGE0_CP_HOME/pomerium:/pomerium:ro,z" \
  "$image" --config /pomerium/config.yaml
