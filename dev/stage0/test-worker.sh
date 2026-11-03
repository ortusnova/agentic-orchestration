#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks the kit's worker side with systemctl stubbed (spec store
# agent-platform/one-sortie): the worker daemon's configuration, which the
# daemon itself checks (worker-daemon check), gives every worker the same
# images and a homelab job what the homelab Sortie gave its machines; p1's
# own worker needs no tunnel; a worker host's Sortie and p1's Huly tunnels
# are retired; the retired homelab profile moves only when a person says so;
# and a second run changes nothing.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
say() { echo "say: $*" >> "$tmp/said"; }

mkdir -p "$tmp/bin" "$STAGE0_HOME" "$tmp/units"
printf '#!/bin/sh\necho "systemctl $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/systemctl"
# smolvm too old: a stage0.sh run here stops before it builds or installs anything.
printf '#!/bin/sh\nexit 1\n' > "$tmp/bin/smolvm"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH
daemon=${WORKER_DAEMON_BIN:-}
if [[ -z $daemon ]]; then
  daemon=$tmp/worker-daemon
  (cd "$KIT/../../worker-daemon" && go build -o "$daemon" ./cmd/worker-daemon)
fi
calls() { [[ -f $tmp/calls ]] && tr '\n' ';' < "$tmp/calls"; true; }

# shellcheck source=worker-config.sh
. "$KIT/worker-config.sh"
# shellcheck source=homelab-inputs.sh
. "$KIT/homelab-inputs.sh"
STAGE0_PACK=$STAGE0_HOME/stage0.smolmachine STAGE0_MAX_AGENTS=2
echo cert > "$tmp/ca.crt"
conf=$STAGE0_HOME/worker.json

# A worker without homelab's inputs: the stage-0 pack, with the egress CA,
# and no nested.
render_worker_config "$conf" taichi http://127.0.0.1:7700 "$tmp/ca.crt"
"$daemon" check -config "$conf" > "$tmp/out" 2>&1 && ok "the daemon accepts the configuration" || fail "check: $(cat "$tmp/out")"
grep -q '"stage0.smolmachine": "'"$STAGE0_PACK"'"' "$conf" && ok "the stage-0 pack is stage0.smolmachine, as the workflow names it" || fail "packs: $(cat "$conf")"
grep -q '"nested": false' "$conf" && ok "a worker without homelab's inputs does not declare nested" || fail "nested: $(cat "$conf")"
grep -q '"ORTUS_EGRESS_CA": "'"$tmp/ca.crt"'"' "$conf" && grep -q 'update-ca-certificates' "$conf" \
  && ok "every machine trusts the egress CA's interception" || fail "CA: $(cat "$conf")"
grep -q '"IS_SANDBOX": "1"' "$conf" && ok "the agent runs with IS_SANDBOX, which the remote kind does not pass" || fail "IS_SANDBOX: $(cat "$conf")"
grep -q '"url": "http://127.0.0.1:7700"' "$conf" && ok "the worker dials the edge on its own loopback" || fail "url: $(cat "$conf")"
for host in p1 taichi; do
  if grep -v '"worker_id"' "$conf" | grep -q "\"$host\""; then fail "the configuration names the host $host beyond worker_id"; fi
done

# A worker with homelab's inputs: the homelab pack and what a homelab job needs.
STAGE0_EGRESS_CA=$tmp/ca.crt
mounts="{\"host\": \"$tmp/sources\", \"guest\": \"/home/builder/.cache/homelab-sources\", \"read_only\": true}"
mounts+=", {\"host\": \"$tmp/recorder\", \"guest\": \"/var/log/homelab-recorder\", \"per_job\": true}"
STAGE0_HOMELAB_PACK=$STAGE0_HOME/packs/homelab-0123.smolmachine
STAGE0_HOMELAB_IMAGE_JSON=$(homelab_image_json /opt/krunfw "$mounts")
render_worker_config "$conf" taichi http://127.0.0.1:7700 "$tmp/ca.crt"
"$daemon" check -config "$conf" > "$tmp/out" 2>&1 && ok "the daemon accepts the homelab image" || fail "check homelab: $(cat "$tmp/out")"
grep -q '"homelab.smolmachine": "'"$STAGE0_HOMELAB_PACK"'"' "$conf" && grep -q '"nested": true' "$conf" \
  && ok "a worker with homelab's inputs has the homelab pack and declares nested" || fail "homelab pack: $(cat "$conf")"
[[ $(grep -c '"IS_SANDBOX": "1"' "$conf") == 2 ]] && ok "the homelab image has IS_SANDBOX too" || fail "homelab IS_SANDBOX: $(cat "$conf")"
grep -q '"user": "builder"' "$conf" && grep -q '"lib_dir": "/opt/krunfw"' "$conf" && grep -q '"per_job": true' "$conf" \
  && grep -q '"HOMELAB_VM_AGE_KEY": "'"$STAGE0_HOME"'/inputs/vm-age/keys.txt"' "$conf" \
  && ok "the homelab job gets builder, the guest kernel, the shared folders and the vm key" || fail "homelab image: $(cat "$conf")"
for step in 'sops/age/keys.txt' 'guest-start.sh' 'scripts/vmhost/prepare.sh'; do
  grep -q "$step" "$conf" && ok "its setup does what the hooks did: $step" || fail "no $step in the setup"
done
sum=$(sha256sum < "$conf")
render_worker_config "$conf" taichi http://127.0.0.1:7700 "$tmp/ca.crt"
[[ $(sha256sum < "$conf") == "$sum" ]] && ok "rendering again changes nothing" || fail "a second render differs"
unset STAGE0_HOMELAB_PACK STAGE0_HOMELAB_IMAGE_JSON

# p1's own worker: on the edge at the tunnels' port, with no tunnel.
STAGE0_SORTIE_PORT=7680
# shellcheck source=edge-config.sh
. "$KIT/edge-config.sh"
mkdir -p "$STAGE0_WORKERS"
printf 'WORKER_HOST=brian@taichi\nEDGE_PORT=7701\nWORKER_PORT=7700\n' > "$STAGE0_WORKERS/taichi.env"
register_local_worker p1
touch -d '2000-01-01' "$STAGE0_WORKERS/p1.env"
register_local_worker p1
[[ $(stat -c %Y "$STAGE0_WORKERS/p1.env") == $(date -d 2000-01-01 +%s) ]] && ok "registering p1's worker again changes nothing" || fail "p1.env was rewritten"
[[ $(tunnel_workers | tr '\n' ' ') == "taichi " ]] && ok "p1's worker gets no tunnel" || fail "tunnel workers: $(tunnel_workers)"
render_edge_config "$STAGE0_HOME/edge.json"
edge=${STAGE0_EDGE_BIN:-}
if [[ -z $edge ]]; then
  edge=$tmp/stage0-edge
  (cd "$KIT/edge" && go build -o "$edge" .)
fi
echo control > "$STAGE0_HOME/sortie-token"
echo pass > "$STAGE0_HOME/nono-pass"
[[ $("$edge" -check -config "$STAGE0_HOME/edge.json" | tr '\n' ';') == "p1 127.0.0.1:7700;taichi 127.0.0.1:7701;" ]] \
  && ok "the edge names p1's worker by the port the workflow's flows use" || fail "edge: $(cat "$STAGE0_HOME/edge.json")"
[[ $(edge_port mac) == 7702 ]] && ok "a new worker host gets the next free port" || fail "next port: $(edge_port mac)"

# A worker host's Sortie is stopped and its unit removed, once.
echo unit > "$tmp/units/ortus-stage0-sortie.service"
echo env > "$STAGE0_HOME/profile.env"
rm -f "$tmp/calls"
retire_sortie "$tmp/units"
[[ ! -e $tmp/units/ortus-stage0-sortie.service && ! -e $STAGE0_HOME/profile.env ]] && ok "the worker host's Sortie unit is gone" || fail "the Sortie unit is still there"
[[ $(calls) == "systemctl --user disable -q --now ortus-stage0-sortie.service;systemctl --user daemon-reload;" ]] \
  && ok "and its Sortie is stopped" || fail "calls: $(calls)"
rm -f "$tmp/calls"; retire_sortie "$tmp/units"
[[ -z $(calls) ]] && ok "retiring it again changes nothing" || fail "calls again: $(calls)"

# p1 keeps no Huly tunnel: no worker host polls Huly.
mkdir -p "$tmp/units/default.target.wants"
echo unit > "$tmp/units/ortus-stage0-huly-tunnel@.service"
ln -s ../ortus-stage0-huly-tunnel@.service "$tmp/units/default.target.wants/ortus-stage0-huly-tunnel@taichi.service"
mkdir -p "$STAGE0_HOME/tunnels" "$STAGE0_HOME/sorties"
echo SORTIE_LOCAL_PORT=7681 > "$STAGE0_HOME/tunnels/taichi.env"
echo SORTIE_URL=http://127.0.0.1:7681 > "$STAGE0_HOME/sorties/taichi.env"
echo token > "$STAGE0_HOME/sorties/taichi.token"
rm -f "$tmp/calls"; retire_huly_tunnels "$tmp/units"
[[ ! -e $tmp/units/ortus-stage0-huly-tunnel@.service && ! -L $tmp/units/default.target.wants/ortus-stage0-huly-tunnel@taichi.service ]] \
  && grep -q 'disable -q --now ortus-stage0-huly-tunnel@taichi.service' "$tmp/calls" \
  && ok "p1's Huly tunnel to taichi is closed and its unit gone" || fail "huly tunnel: $(calls)"
[[ ! -e $STAGE0_HOME/tunnels/taichi.env && ! -e $STAGE0_HOME/sorties/taichi.env && ! -e $STAGE0_HOME/sorties/taichi.token ]] \
  && ok "and the workbench no longer reads taichi's Sortie" || fail "taichi's Sortie files are still there"
rm -f "$tmp/calls"; retire_huly_tunnels "$tmp/units"
[[ -z $(calls) ]] && ok "closing them again changes nothing" || fail "calls again: $(calls)"

# The homelab profile is retired: stage0.sh refuses it until a person moves
# the host to a worker.
printf 'STAGE0_PROFILE=homelab\nSTAGE0_KIT_ROOT=%s\n' "$KIT/../.." > "$STAGE0_HOME/stage0.env"
rc=0
env -u STAGE0_PROFILE HOME="$tmp/fakehome" STAGE0_UNITS="$tmp/units" STAGE0_LOCKED=1 STAGE0_KIT_FROM_CHECKOUT=1 "$KIT/stage0.sh" > "$tmp/out" 2>&1 || rc=$?
(( rc != 0 )) && grep -q 'homelab profile is retired' "$tmp/out" && grep -q 'STAGE0_PROFILE=worker' "$tmp/out" \
  && grep -qx 'STAGE0_PROFILE=homelab' "$STAGE0_HOME/stage0.env" \
  && ok "stage0.sh refuses the homelab profile and says how to move, changing nothing" || fail "homelab profile: exit $rc, $(cat "$tmp/out")"
rc=0
STAGE0_PROFILE=worker HOME="$tmp/fakehome" STAGE0_UNITS="$tmp/units" STAGE0_LOCKED=1 STAGE0_KIT_FROM_CHECKOUT=1 "$KIT/stage0.sh" > "$tmp/out" 2>&1 || rc=$?
grep -qx 'STAGE0_PROFILE=worker' "$STAGE0_HOME/stage0.env" && ok "STAGE0_PROFILE=worker moves the host to the worker profile" \
  || fail "the move: exit $rc, $(cat "$tmp/out"), $(cat "$STAGE0_HOME/stage0.env")"
exit $failed
