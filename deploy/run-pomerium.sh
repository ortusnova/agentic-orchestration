#!/usr/bin/env bash
# Start Pomerium, the only door of the control plane for every worker (the
# pomerium user unit runs this): the image the compose project
# pins by digest, under rootless podman, on the host's network, on the port
# of the public URL. It requires a client certificate from the control
# plane's CA on every connection, admits on each route only the workers
# $ORTUS_NOVA_HOME/control-plane/workers.json lists, sets Sortie's identity
# header from the certificate, and routes to Sortie and the egress gate on
# p1's loopback ($ORTUS_NOVA_HOME/control-plane/pomerium/config.yaml, which
# cpctl renders; worker-host.sh restarts this onto a new list). Its
# certificate and key enter the container as podman secrets.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=names.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/names.sh"
env_load "${ORTUS_NOVA_ENV:?the unit names the environment (ORTUS_NOVA_ENV)}"
# shellcheck source=front-config.sh
. "$KIT/front-config.sh"
die() { printf 'run-pomerium.sh: %s\n' "$*" >&2; exit 1; }

image=$(pomerium_image)
[[ -n $image ]] || die "control-plane/compose.yaml pins no Pomerium image"
secrets=$ORTUS_NOVA_CP_HOME/secrets
for s in cp-pomerium-cert cp-pomerium-key; do
  [[ -s $secrets/$s ]] || die "$secrets/$s is missing: run deploy/deploy.sh"
  podman secret create --replace "$ORTUS_NOVA_PODMAN_PREFIX${s#cp-}" "$secrets/$s" > /dev/null
done
exec podman run --rm --replace --name "${ORTUS_NOVA_PODMAN_PREFIX}pomerium" --network host \
  --secret "${ORTUS_NOVA_PODMAN_PREFIX}pomerium-cert,target=cp-pomerium-cert" \
  --secret "${ORTUS_NOVA_PODMAN_PREFIX}pomerium-key,target=cp-pomerium-key" \
  -v "$ORTUS_NOVA_CP_HOME/pomerium:/pomerium:ro,z" \
  "$image" --config /pomerium/config.yaml
