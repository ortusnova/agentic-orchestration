#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks worker-host.sh with ssh and systemctl stubbed: each worker host
# gets its own tunnel and edge port, and the edge's configuration, which
# the edge itself checks (stage0-edge -check), names each worker by its port.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home STAGE0_UNITS=$tmp/units
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

mkdir -p "$tmp/bin" "$STAGE0_HOME/egress-ca"
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\necho "ssh $*" >> "%s/calls"\ncase "$*" in *curl*) echo 404 ;; *cat*) cat > /dev/null ;; esac\n' "$tmp" > "$tmp/bin/ssh"
printf '#!/bin/sh\necho "systemctl $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/systemctl"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH
printf 'STAGE0_SORTIE_PORT=7680\n' > "$STAGE0_HOME/stage0.env"
echo control > "$STAGE0_HOME/sortie-token"
echo pass > "$STAGE0_HOME/nono-pass"
echo cert > "$STAGE0_HOME/egress-ca/ca.crt"
edge=${STAGE0_EDGE_BIN:-}
if [[ -z $edge ]]; then
  edge=$tmp/stage0-edge
  (cd "$KIT/edge" && go build -o "$edge" .)
fi
wh() { "$KIT/worker-host.sh" "$@" > "$tmp/out" 2>&1; }
ports() { "$edge" -check -config "$STAGE0_HOME/edge.json" | tr '\n' ';'; }

wh add brian@taichi || fail "add taichi: $(cat "$tmp/out")"
[[ $(ports) == "taichi 127.0.0.1:7701;" ]] && ok "taichi gets the first edge port, on loopback" || fail "after taichi: $(ports)"
grep -q 'WORKER_HOST=brian@taichi' "$STAGE0_HOME/workers/taichi.env" && ok "taichi is named without its user@" || fail "taichi.env: $(cat "$STAGE0_HOME/workers/taichi.env")"
grep -q 'systemctl --user enable -q ortus-stage0-worker-tunnel@taichi.service' "$tmp/calls" && ok "taichi's tunnel unit is enabled" || fail "calls: $(cat "$tmp/calls")"
grep -q 'ssh -o BatchMode=yes brian@taichi mkdir -p ~/.config/ortus-nova' "$tmp/calls" && ok "taichi gets the egress CA's certificate" || fail "no CA copy: $(cat "$tmp/calls")"

wh add mac.lan mac || fail "add mac: $(cat "$tmp/out")"
wh add brian@taichi || fail "add taichi again: $(cat "$tmp/out")"
[[ $(ports) == "mac 127.0.0.1:7702;taichi 127.0.0.1:7701;" ]] && ok "each worker has its own port, and adding one again keeps it" || fail "after mac: $(ports)"

unit=$STAGE0_UNITS/ortus-stage0-worker-tunnel@.service
# shellcheck disable=SC2016 # systemd expands these, not the shell
grep -q "^EnvironmentFile=$STAGE0_HOME/workers/%i.env$" "$unit" \
  && grep -q -- '-R 127.0.0.1:${WORKER_PORT}:127.0.0.1:${EDGE_PORT} ${WORKER_HOST}$' "$unit" \
  && ok "p1 opens each tunnel, a remote forward between loopbacks" || fail "the tunnel unit: $(cat "$unit")"

wh add p1 && fail "p1 was added as a worker host" || ok "p1 is not a worker host"
wh add 'taichi;rm' && fail "a host with a shell character was added" || ok "a host with a shell character is refused"
wh add taichi 'tai chi' && fail "a name with a space was added" || ok "a name with a space is refused"

wh remove taichi || fail "remove taichi: $(cat "$tmp/out")"
[[ $(ports) == "mac 127.0.0.1:7702;" ]] && ok "a removed worker's port is closed" || fail "after remove: $(ports)"
grep -q 'systemctl --user disable -q --now ortus-stage0-worker-tunnel@taichi.service' "$tmp/calls" && ok "a removed worker's tunnel is stopped" || fail "calls: $(cat "$tmp/calls")"
wh add taichi || fail "add taichi back: $(cat "$tmp/out")"
[[ $(ports) == "mac 127.0.0.1:7702;taichi 127.0.0.1:7701;" ]] && ok "a freed port is used again" || fail "after re-add: $(ports)"

# p1's own worker (stage0.sh) is on the edge, with no tunnel to remove.
printf 'LOCAL=1\nEDGE_PORT=7700\n' > "$STAGE0_HOME/workers/p1.env"
wh list && grep -qx "$(printf 'p1\tlocal\tedge port 7700')" "$tmp/out" && ok "p1's worker is listed as local" || fail "list: $(cat "$tmp/out")"
wh remove p1 && fail "p1's own worker was removed" || ok "p1's own worker is not removed here"
exit $failed
