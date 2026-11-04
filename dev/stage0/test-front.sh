#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks the control plane's front on p1 (front-config.sh), which replaced
# the stage-1 edge (design D18), with podman and cp-egress stubbed: Pomerium
# runs the image the compose project pins, on the host's network, with its
# certificate and key as podman secrets; the egress gate listens on
# loopback, introspects with Sortie's control token, and sends a running
# live proof's egress to the live-proof nono; every flow of the workflow is
# under Pomerium's public URL; and the edge is gone from the kit.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

mkdir -p "$tmp/bin" "$STAGE0_HOME/bin" "$STAGE0_HOME/control-plane/secrets"
# shellcheck disable=SC2016 # expanded by the stubs, not here
printf '#!/bin/sh\necho "podman $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/podman"
# shellcheck disable=SC2016
printf '#!/bin/sh\necho "cp-egress $*" >> "%s/calls"\n' "$tmp" > "$STAGE0_HOME/bin/cp-egress"
chmod +x "$tmp/bin/podman" "$STAGE0_HOME/bin/cp-egress"
export PATH=$tmp/bin:$PATH
printf 'STAGE0_SORTIE_PORT=7680\nSTAGE0_CP_PUBLIC_URL=https://p1.example.org:8443\n' > "$STAGE0_HOME/stage0.env"

# shellcheck source=front-config.sh
(. "$STAGE0_HOME/stage0.env"; . "$KIT/front-config.sh"; pomerium_image) > "$tmp/image"
pinned=$(grep -o 'docker.io/pomerium/pomerium:[^ ]*' "$KIT/../../control-plane/compose.yaml")
[[ $(cat "$tmp/image") == "$pinned" && $pinned == *@sha256:* ]] && ok "p1 runs the Pomerium image the compose project pins by digest" || fail "image: $(cat "$tmp/image") vs $pinned"
# shellcheck source=front-config.sh
[[ $(. "$KIT/front-config.sh"; STAGE0_CP_PUBLIC_URL=https://cp.example.org; cp_public_port) == 443 ]] \
  && [[ $(. "$KIT/front-config.sh"; STAGE0_CP_PUBLIC_URL=https://cp.example.org:9443; cp_public_port) == 9443 ]] \
  && ok "Pomerium listens on the public URL's port" || fail "cp_public_port"

"$KIT/run-pomerium.sh" > "$tmp/out" 2>&1 && fail "Pomerium started without its certificate" || ok "Pomerium needs its certificate and key"
for s in cp-pomerium-cert cp-pomerium-key; do echo "$s" > "$STAGE0_HOME/control-plane/secrets/$s"; done
rm -f "$tmp/calls"
"$KIT/run-pomerium.sh" > "$tmp/out" 2>&1 || fail "run-pomerium.sh: $(cat "$tmp/out")"
grep -q "^podman secret create --replace ortus-stage0-pomerium-key $STAGE0_HOME/control-plane/secrets/cp-pomerium-key$" "$tmp/calls" \
  && grep -q -- "--secret ortus-stage0-pomerium-key,target=cp-pomerium-key" "$tmp/calls" \
  && ok "Pomerium's key enters its container as a podman secret, at the path its configuration names" || fail "secrets: $(cat "$tmp/calls")"
grep -q -- "--network host .* $pinned --config /pomerium/config.yaml$" "$tmp/calls" \
  && grep -q -- "-v $STAGE0_HOME/control-plane/pomerium:/pomerium:ro,z" "$tmp/calls" \
  && ok "Pomerium runs on the host's network with cpctl's configuration" || fail "podman run: $(cat "$tmp/calls")"

rm -f "$tmp/calls"
"$KIT/run-egress-gate.sh" > "$tmp/out" 2>&1 || fail "run-egress-gate.sh: $(cat "$tmp/out")"
want="cp-egress -listen 127.0.0.1:7692 -sortie http://127.0.0.1:7680 -control-token-file $STAGE0_HOME/sortie-token -nono 127.0.0.1:7690 -nono-pass-file $STAGE0_HOME/nono-pass -live-proof-nono 127.0.0.1:7691 -live-proof-jobs-file $STAGE0_HOME/live-proofs/active"
[[ $(cat "$tmp/calls") == "$want" ]] \
  && ok "the egress gate listens on loopback, tunnels through nono, and sends a running live proof's egress to the live-proof nono" \
  || fail "cp-egress: $(cat "$tmp/calls")"
(cd "$KIT/../../control-plane" && go run ./cmd/cp-egress -h > "$tmp/flags" 2>&1 || true)
for f in listen sortie control-token-file nono nono-pass-file live-proof-nono live-proof-jobs-file; do
  grep -q -- "^  -$f " "$tmp/flags" || fail "cp-egress has no -$f flag"
done
ok "cp-egress takes every flag run-egress-gate.sh gives it"

# nono on p1 is the compose project's: the same release, and the same
# profile, whose credentials egress-credentials.sh gives it by name.
# shellcheck source=nono-version.sh
(. "$KIT/nono-version.sh"; grep -q "image: ghcr.io/nolabs-ai/nono:$NONO_VERSION@sha256:" "$KIT/../../control-plane/compose.yaml") \
  && ok "p1 pins the nono release the compose project runs" || fail "nono-version.sh and control-plane/compose.yaml pin different nono releases"
want=$(grep -o 'env://CP_EGRESS_[A-Z_]*' "$KIT/../../control-plane/nono/egress-profile.json" | sed 's|env://||' | sort -u | tr '\n' ' ')
got=$(sed -n 's/^  export \(.*\)$/\1/p' "$KIT/egress-credentials.sh" | tr ' ' '\n' | sort -u | tr '\n' ' ')
[[ -n $want && $got == "$want" ]] && ok "run-nono.sh gives nono every credential the profile reads ($want)" || fail "profile reads '$want', egress-credentials.sh exports '$got'"
grep -q 'PROFILE=.*control-plane/nono' "$KIT/run-nono.sh" && ok "run-nono.sh runs the control plane's egress profile" || fail "run-nono.sh's profile"


# The units: the edge and its tunnels are gone, and the worker daemon
# starts after Pomerium, its door.
[[ ! -e $KIT/units/ortus-stage0-edge.service && ! -e $KIT/units/ortus-stage0-worker-tunnel@.service && ! -e $KIT/edge && ! -e $KIT/edge-config.sh ]] \
  && ok "the edge, its configuration and its tunnel unit are gone" || fail "the edge is still in the kit"
grep -q '^After=.*ortus-stage0-pomerium.service' "$KIT/units/ortus-stage0-worker.service" && ok "p1's worker starts after Pomerium" || fail "worker unit"
grep -q '^ExecStart=@KIT@/run-pomerium.sh$' "$KIT/units/ortus-stage0-pomerium.service" \
  && grep -q '^ExecStart=@KIT@/run-egress-gate.sh$' "$KIT/units/ortus-stage0-egress-gate.service" \
  && ok "Pomerium and the egress gate run as user units" || fail "front units"
exit $failed
