#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks the kit's worker side with systemctl stubbed (spec store
# agent-platform/one-sortie): the worker daemon's configuration, which the
# daemon itself checks (worker-daemon check), gives every worker the same
# images and a homelab job what the homelab Sortie gave its machines; p1's
# own worker dials Pomerium too; a worker host's Sortie and p1's Huly tunnels
# are retired; the retired homelab profile moves only when a person says so;
# and a second run changes nothing.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export ORTUS_NOVA_HOME=$tmp/home
export ORTUS_NOVA_ENV=test-a
# shellcheck source=names.sh
. "$KIT/names.sh"
env_names test-a
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
say() { echo "say: $*" >> "$tmp/said"; }

mkdir -p "$tmp/bin" "$ORTUS_NOVA_HOME" "$tmp/units"
printf '#!/bin/sh\necho "systemctl $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/systemctl"
# smolvm too old: a deploy.sh run here stops before it builds or installs anything.
printf '#!/bin/sh\nexit 1\n' > "$tmp/bin/smolvm"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH
daemon=${WORKER_DAEMON_BIN:-}
if [[ -z $daemon ]]; then
  daemon=$tmp/worker-daemon
  (cd "$KIT/../worker-daemon" && go build -o "$daemon" ./cmd/worker-daemon)
fi
calls() { [[ -f $tmp/calls ]] && tr '\n' ';' < "$tmp/calls"; true; }

# shellcheck source=worker-config.sh
. "$KIT/worker-config.sh"
# shellcheck source=homelab-inputs.sh
. "$KIT/homelab-inputs.sh"
# shellcheck source=live-proof-pack.sh
. "$KIT/live-proof-pack.sh"
ORTUS_NOVA_PACK=$ORTUS_NOVA_HOME/agent.smolmachine
echo cert > "$tmp/ca.crt"
conf=$ORTUS_NOVA_HOME/worker.json

# A worker without homelab's inputs: the stage-0 pack, with the egress CA,
# and no nested.
url=https://p1.example.org:8443
tls=("$tmp/cp-worker.crt" "$tmp/cp-worker.key" "$tmp/cp-ca.crt")
render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}"
"$daemon" check -config "$conf" > "$tmp/out" 2>&1 && ok "the daemon accepts the configuration" || fail "check: $(cat "$tmp/out")"
grep -q '"agent.smolmachine": "'"$ORTUS_NOVA_PACK"'"' "$conf" && ok "the agent pack is agent.smolmachine, as the workflow names it" || fail "packs: $(cat "$conf")"
grep -q '"nested": false' "$conf" && ok "a worker without homelab's inputs does not declare nested" || fail "nested: $(cat "$conf")"
grep -q '"ORTUS_EGRESS_CA": "'"$tmp/ca.crt"'"' "$conf" && grep -q 'update-ca-certificates' "$conf" \
  && ok "every machine trusts the egress CA's interception" || fail "CA: $(cat "$conf")"
grep -q '"IS_SANDBOX": "1"' "$conf" && ok "the agent runs with IS_SANDBOX, which the remote kind does not pass" || fail "IS_SANDBOX: $(cat "$conf")"
python3 - "$conf" "$KIT/WORKFLOW.md" <<'PY' && ok "the workflow's agent command reads its token usage with agent-usage" || fail "usage_commands: $(cat "$conf")"
import json, os, re, sys
usage = json.load(open(sys.argv[1]))["usage_commands"]
command = re.search(r"^\s*command:\s*(\S+)", open(sys.argv[2]).read(), re.M).group(1)
# The daemon looks the run's command up by its base name.
sys.exit(0 if usage == {os.path.basename(command): ["agent-usage"]} else 1)
PY
# shellcheck disable=SC2016 # the script's own text
[[ -x $KIT/pack/agent-usage ]] && grep -q '"$dir/agent-usage"' "$KIT/pack/install-harness.sh" \
  && ok "the pack installs agent-usage" || fail "the pack does not install agent-usage"
grep -q '"url": "https://p1.example.org:8443"' "$conf" \
  && grep -q '"tls": {"cert_file": "'"$tmp/cp-worker.crt"'", "key_file": "'"$tmp/cp-worker.key"'", "ca_file": "'"$tmp/cp-ca.crt"'"}' "$conf" \
  && ok "the worker dials Pomerium with its client certificate" || fail "url: $(cat "$conf")"
for host in p1 taichi; do
  if grep -v '"worker_id"' "$conf" | grep -q "\"$host\""; then fail "the configuration names the host $host beyond worker_id"; fi
done

# The run slots follow the host unless ORTUS_NOVA_WORKER_SLOTS says otherwise:
# a fixed 2 left taichi, 32 CPUs and 91 GiB, with 18 CPUs and 42 GiB free
# while a job waited on its slots (2026-11-03).
if grep -q '"run_slots"' "$conf"; then fail "run_slots is written without ORTUS_NOVA_WORKER_SLOTS: $(cat "$conf")"; else ok "without ORTUS_NOVA_WORKER_SLOTS the daemon sizes the slots from the host"; fi
ORTUS_NOVA_WORKER_SLOTS=4 render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}"
grep -q '"run_slots": 4,' "$conf" && "$daemon" check -config "$conf" > "$tmp/out" 2>&1 \
  && ok "ORTUS_NOVA_WORKER_SLOTS sets run_slots" || fail "override: $(cat "$conf" "$tmp/out")"
for bad in 0 -1 two; do
  if ORTUS_NOVA_WORKER_SLOTS=$bad render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}" 2> /dev/null; then
    fail "ORTUS_NOVA_WORKER_SLOTS=$bad was taken"
  else ok "ORTUS_NOVA_WORKER_SLOTS=$bad is refused"; fi
done
ORTUS_NOVA_WORKER_SLOTS='' render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}"
grep -q '"run_slots"' "$conf" && fail "an empty ORTUS_NOVA_WORKER_SLOTS wrote run_slots" || ok "an empty ORTUS_NOVA_WORKER_SLOTS follows the host"

# The retired ORTUS_NOVA_MAX_AGENTS: the default the kit wrote goes, with its
# comment; a person's own value becomes ORTUS_NOVA_WORKER_SLOTS.
env_case() { # NAME COMMENT VALUE: migrate a host.env holding COMMENT and ORTUS_NOVA_MAX_AGENTS=VALUE
  printf 'ORTUS_NOVA_PROFILE=worker\n%s\nORTUS_NOVA_MAX_AGENTS=%s\n# The next one.\nORTUS_NOVA_HOMELAB_SRC=/h\n' "$2" "$3" > "$tmp/$1.env"
  (ORTUS_NOVA_MAX_AGENTS=$3; migrate_max_agents "$tmp/$1.env"; echo "${ORTUS_NOVA_WORKER_SLOTS-unset} ${ORTUS_NOVA_MAX_AGENTS-unset}") > "$tmp/$1.out"
}
env_case p1 '# Agents at once; each VM takes 4 vCPU and 8 GiB (WORKFLOW.md).' 3
env_case taichi '# Agents at once; each homelab machine takes 8 vCPU and 20 GiB (design D11).' 2
env_case newer "# p1's own worker's jobs at once; each VM takes 4 vCPU and 8 GiB (WORKFLOW.md)." 3
for c in p1 taichi newer; do
  [[ $(cat "$tmp/$c.env") == $'ORTUS_NOVA_PROFILE=worker\n# The next one.\nORTUS_NOVA_HOMELAB_SRC=/h' && $(cat "$tmp/$c.out") == "unset unset" ]] \
    && ok "$c's old default ORTUS_NOVA_MAX_AGENTS goes, with its comment" || fail "$c: $(cat "$tmp/$c.env" "$tmp/$c.out")"
done
env_case own '# Mine.' 6
[[ $(cat "$tmp/own.env") == $'ORTUS_NOVA_PROFILE=worker\n# Mine.\nORTUS_NOVA_WORKER_SLOTS=6\n# The next one.\nORTUS_NOVA_HOMELAB_SRC=/h' && $(cat "$tmp/own.out") == "6 unset" ]] \
  && ok "a person's ORTUS_NOVA_MAX_AGENTS becomes ORTUS_NOVA_WORKER_SLOTS" || fail "own: $(cat "$tmp/own.env" "$tmp/own.out")"
sum=$(sha256sum < "$tmp/own.env"); migrate_max_agents "$tmp/own.env"
[[ $(sha256sum < "$tmp/own.env") == "$sum" ]] && ok "migrating again changes nothing" || fail "a second migration changed $(cat "$tmp/own.env")"
render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}"

# Only a host that offers nested virtualization declares nested: /dev/kvm and
# the KVM module's nested parameter on.
host=$tmp/host
mkdir -p "$host/dev" "$host/sys/module/kvm_intel/parameters" "$host/sys/module/kvm_amd/parameters"
ORTUS_NOVA_HOST_ROOT=$host host_nested && fail "a host without /dev/kvm offers nested" || ok "a host without /dev/kvm offers no nested"
touch "$host/dev/kvm"
echo N > "$host/sys/module/kvm_intel/parameters/nested"
ORTUS_NOVA_HOST_ROOT=$host host_nested && fail "kvm_intel nested=N offers nested" || ok "a host whose KVM module has nested off offers no nested"
echo Y > "$host/sys/module/kvm_intel/parameters/nested"
ORTUS_NOVA_HOST_ROOT=$host host_nested && ok "kvm_intel nested=Y offers nested" || fail "kvm_intel nested=Y"
rm "$host/sys/module/kvm_intel/parameters/nested"; echo 1 > "$host/sys/module/kvm_amd/parameters/nested"
ORTUS_NOVA_HOST_ROOT=$host host_nested && ok "kvm_amd nested=1 offers nested" || fail "kvm_amd nested=1"

# p1's worker never declares nested, so the matcher never gives it a job of
# the nested live-proof profile (WORKFLOW.md): deploy.sh renders it so.
ORTUS_NOVA_EGRESS_CA=$tmp/ca.crt
ORTUS_NOVA_WORKER_NESTED=false render_worker_config "$conf" p1 "$url" "$tmp/ca.crt" "${tls[@]}"
grep -q '"nested": false' "$conf" && ! grep -q 'live-proof.smolmachine' "$conf" \
  && ok "p1's worker declares no nested and has no live-proof pack" || fail "p1: $(cat "$conf")"
grep -q '^ORTUS_NOVA_WORKER_NESTED=false$' <(sed -n '/^build_pack$/,/^render_worker_config/p' "$KIT/deploy.sh") \
  && ! sed -n '/^build_pack$/,/^render_worker_config/p' "$KIT/deploy.sh" | grep -q 'host_nested\|live_proof_pack\|homelab_inputs' \
  && ok "deploy.sh's control profile (p1) sets no nested, whatever the host" || fail "deploy.sh's p1 part"

# taichi, which offers nested: the live-proof pack as the workflow names it.
ORTUS_NOVA_LIVE_PROOF_PACK=$ORTUS_NOVA_HOME/packs/live-proof-4567.smolmachine
ORTUS_NOVA_LIVE_PROOF_IMAGE_JSON=$(live_proof_image_json)
ORTUS_NOVA_WORKER_NESTED=true
render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}"
"$daemon" check -config "$conf" > "$tmp/out" 2>&1 && ok "the daemon accepts the live-proof image" || fail "check live-proof: $(cat "$tmp/out")"
grep -q '"live-proof.smolmachine": "'"$ORTUS_NOVA_LIVE_PROOF_PACK"'"' "$conf" && grep -q '"nested": true' "$conf" \
  && ok "a worker that offers nested has the live-proof pack and declares nested" || fail "live-proof pack: $(cat "$conf")"
grep -q 'image: live-proof.smolmachine' "$KIT/WORKFLOW.md" && ok "the workflow's nested profile names that image" || fail "WORKFLOW.md names no live-proof.smolmachine"
[[ $(grep -c '"IS_SANDBOX": "1"' "$conf") == 2 && $(grep -c 'update-ca-certificates' "$conf") == 2 ]] \
  && ok "the live-proof image trusts the egress CA and has IS_SANDBOX" || fail "live-proof image: $(cat "$conf")"
if (unset ORTUS_NOVA_LIVE_PROOF_PACK; render_worker_config "$conf.x" taichi "$url" "$tmp/ca.crt" "${tls[@]}") 2> /dev/null; then
  fail "a worker declared nested without the live-proof pack"
else ok "a worker does not declare nested without the live-proof pack"; fi

# A worker with homelab's inputs: the homelab pack and what a homelab job needs.
mounts="{\"host\": \"$tmp/sources\", \"guest\": \"/home/builder/.cache/homelab-sources\", \"read_only\": true}"
mounts+=", $(homelab_state_mounts "$tmp/recorder" "$tmp/open-issues")"
ORTUS_NOVA_HOMELAB_PACK=$ORTUS_NOVA_HOME/packs/homelab-0123.smolmachine
ORTUS_NOVA_HOMELAB_IMAGE_JSON=$(homelab_image_json /opt/krunfw "$mounts")
render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}"
"$daemon" check -config "$conf" > "$tmp/out" 2>&1 && ok "the daemon accepts the homelab image" || fail "check homelab: $(cat "$tmp/out")"
grep -q '"homelab.smolmachine": "'"$ORTUS_NOVA_HOMELAB_PACK"'"' "$conf" && grep -q '"nested": true' "$conf" \
  && ok "a worker with homelab's inputs that offers nested has the homelab pack and declares nested" || fail "homelab pack: $(cat "$conf")"
[[ $(grep -c '"IS_SANDBOX": "1"' "$conf") == 3 ]] && ok "the homelab image has IS_SANDBOX too" || fail "homelab IS_SANDBOX: $(cat "$conf")"
grep -q '"user": "builder"' "$conf" && grep -q '"lib_dir": "/opt/krunfw"' "$conf" && grep -q '"per_job": true' "$conf" \
  && grep -q '"HOMELAB_VM_AGE_KEY": "'"$ORTUS_NOVA_HOME"'/inputs/vm-age/keys.txt"' "$conf" \
  && ok "the homelab job gets builder, the guest kernel, the shared folders and the vm key" || fail "homelab image: $(cat "$conf")"
python3 - "$conf" "$tmp/open-issues" <<'PY' && ok "the homelab job reads the host's open-issue evidence, read-only, where homelab looks for it" || fail "open-issues mount: $(cat "$conf")"
import json, sys
conf, host = sys.argv[1], sys.argv[2]
mounts = json.load(open(conf))["smolvm"]["images"]["homelab.smolmachine"]["mounts"]
want = {"host": host, "guest": "/home/builder/.local/state/homelab/open-issues", "read_only": True}
sys.exit(0 if want in mounts else 1)
PY
for step in 'sops/age/keys.txt' 'guest-start.sh' 'scripts/vmhost/prepare.sh' '/home/builder/.local/state /home/builder/.local/state/homelab'; do
  grep -q "$step" "$conf" && ok "its setup does what the hooks did: $step" || fail "no $step in the setup"
done
sum=$(sha256sum < "$conf")
render_worker_config "$conf" taichi "$url" "$tmp/ca.crt" "${tls[@]}"
[[ $(sha256sum < "$conf") == "$sum" ]] && ok "rendering again changes nothing" || fail "a second render differs"
unset ORTUS_NOVA_HOMELAB_PACK ORTUS_NOVA_HOMELAB_IMAGE_JSON ORTUS_NOVA_LIVE_PROOF_PACK ORTUS_NOVA_LIVE_PROOF_IMAGE_JSON ORTUS_NOVA_WORKER_NESTED

# Sortie's agent cap is a ceiling above the workers' run slots, not p1's own
# slots: the matcher holds each worker to its slots (a fixed 3 for p1 and
# taichi together left taichi idle while 10 stories waited, 2026-11-03).
cap=$(sortie_agent_cap)
(( cap >= 5 + 7 )) && ok "the default cap ($cap) is above p1's 5 slots and taichi's 7" || fail "default cap: $cap"
[[ $(ORTUS_NOVA_SORTIE_MAX_AGENTS='' sortie_agent_cap) == "$cap" ]] && ok "an empty ORTUS_NOVA_SORTIE_MAX_AGENTS is the default" || fail "empty override"
[[ $(ORTUS_NOVA_SORTIE_MAX_AGENTS=4 sortie_agent_cap) == 4 ]] && ok "ORTUS_NOVA_SORTIE_MAX_AGENTS overrides the cap" || fail "override: $(ORTUS_NOVA_SORTIE_MAX_AGENTS=4 sortie_agent_cap)"
for bad in 0 -1 three; do
  if ORTUS_NOVA_SORTIE_MAX_AGENTS=$bad sortie_agent_cap > /dev/null 2>&1; then fail "cap $bad was taken"; else ok "cap $bad is refused"; fi
done
# shellcheck disable=SC2016 # the placeholders, literally
grep -q 'max_concurrent_agents: ${ORTUS_NOVA_SORTIE_MAX_AGENTS}' "$KIT/WORKFLOW.md" && ! grep -q '${ORTUS_NOVA_WORKER_SLOTS}' "$KIT/WORKFLOW.md" \
  && ok "the workflow's cap is sortie_agent_cap's, not a worker's slots" || fail "WORKFLOW.md's max_concurrent_agents"

# A worker host's Sortie is stopped and its unit removed, once.
echo unit > "$tmp/units/ortus-nova-test-a-sortie.service"
echo env > "$ORTUS_NOVA_HOME/profile.env"
rm -f "$tmp/calls"
retire_sortie "$tmp/units"
[[ ! -e $tmp/units/ortus-nova-test-a-sortie.service && ! -e $ORTUS_NOVA_HOME/profile.env ]] && ok "the worker host's Sortie unit is gone" || fail "the Sortie unit is still there"
[[ $(calls) == "systemctl --user disable -q --now ortus-nova-test-a-sortie.service;systemctl --user daemon-reload;" ]] \
  && ok "and its Sortie is stopped" || fail "calls: $(calls)"
rm -f "$tmp/calls"; retire_sortie "$tmp/units"
[[ -z $(calls) ]] && ok "retiring it again changes nothing" || fail "calls again: $(calls)"

# p1 keeps no Huly tunnel: no worker host polls Huly.
mkdir -p "$tmp/units/default.target.wants"
echo unit > "$tmp/units/ortus-nova-test-a-huly-tunnel@.service"
ln -s ../ortus-nova-test-a-huly-tunnel@.service "$tmp/units/default.target.wants/ortus-nova-test-a-huly-tunnel@taichi.service"
mkdir -p "$ORTUS_NOVA_HOME/tunnels" "$ORTUS_NOVA_HOME/sorties"
echo SORTIE_LOCAL_PORT=7681 > "$ORTUS_NOVA_HOME/tunnels/taichi.env"
echo SORTIE_URL=http://127.0.0.1:7681 > "$ORTUS_NOVA_HOME/sorties/taichi.env"
echo token > "$ORTUS_NOVA_HOME/sorties/taichi.token"
rm -f "$tmp/calls"; retire_huly_tunnels "$tmp/units"
[[ ! -e $tmp/units/ortus-nova-test-a-huly-tunnel@.service && ! -L $tmp/units/default.target.wants/ortus-nova-test-a-huly-tunnel@taichi.service ]] \
  && grep -q 'disable -q --now ortus-nova-test-a-huly-tunnel@taichi.service' "$tmp/calls" \
  && ok "p1's Huly tunnel to taichi is closed and its unit gone" || fail "huly tunnel: $(calls)"
[[ ! -e $ORTUS_NOVA_HOME/tunnels/taichi.env && ! -e $ORTUS_NOVA_HOME/sorties/taichi.env && ! -e $ORTUS_NOVA_HOME/sorties/taichi.token ]] \
  && ok "and the workbench no longer reads taichi's Sortie" || fail "taichi's Sortie files are still there"
rm -f "$tmp/calls"; retire_huly_tunnels "$tmp/units"
[[ -z $(calls) ]] && ok "closing them again changes nothing" || fail "calls again: $(calls)"

# The homelab profile is retired: on a host under its stage-0 names, deploy.sh
# moves it to the worker profile when the environment file says the host
# is a worker, and refuses it, changing nothing, when the file says control.
# shellcheck source=testenv.sh
. "$KIT/testenv.sh"
testenv_init "$tmp/env"
testenv_file test-a 1 other=control+worker testhost=worker stage0_names:\ true
testenv_file test-b 2 testhost=control+worker
legacy=$tmp/fakehome/.local/share/ortus-nova/stage0
mkdir -p "$legacy"
printf 'STAGE0_PROFILE=homelab\nSTAGE0_KIT_ROOT=%s\n' "$KIT/.." > "$legacy/stage0.env"
rc=0
env -u ORTUS_NOVA_HOME HOME="$tmp/fakehome" ORTUS_NOVA_UNITS="$tmp/units" ORTUS_NOVA_LOCKED=1 ORTUS_NOVA_KIT_FROM_CHECKOUT=1 "$KIT/deploy.sh" test-b > "$tmp/out" 2>&1 || rc=$?
grep -qx 'STAGE0_PROFILE=homelab' "$legacy/stage0.env" && [[ -s $tmp/fakehome/.local/share/ortus-nova/test-b/host.env ]] \
  && ! grep -q 'stage-0 names' "$tmp/out" \
  && ok "an environment without stage0_names installs under its own names and leaves a stage-0 install alone" || fail "test-b: exit $rc, $(cat "$tmp/out")"
sed -i 's/testhost=worker/x/; s/  testhost: {runs: \[worker\]}/  testhost: {runs: [control, worker]}/; s/  other: {runs: \[control, worker\]}/  other: {runs: [worker]}/' "$tmp/env/environments/test-a.yml"
rc=0
env -u ORTUS_NOVA_HOME HOME="$tmp/fakehome" ORTUS_NOVA_UNITS="$tmp/units" ORTUS_NOVA_LOCKED=1 ORTUS_NOVA_KIT_FROM_CHECKOUT=1 "$KIT/deploy.sh" test-a > "$tmp/out" 2>&1 || rc=$?
(( rc != 0 )) && grep -q 'homelab profile is retired' "$tmp/out" && grep -qx 'STAGE0_PROFILE=homelab' "$legacy/stage0.env" \
  && ok "deploy.sh refuses the homelab profile on a host the environment file says runs control, changing nothing" || fail "homelab profile: exit $rc, $(cat "$tmp/out")"
testenv_file test-a 1 other=control+worker testhost=worker stage0_names:\ true
rc=0
env -u ORTUS_NOVA_HOME HOME="$tmp/fakehome" ORTUS_NOVA_UNITS="$tmp/units" ORTUS_NOVA_LOCKED=1 ORTUS_NOVA_KIT_FROM_CHECKOUT=1 "$KIT/deploy.sh" test-a > "$tmp/out" 2>&1 || rc=$?
grep -qx 'STAGE0_PROFILE=worker' "$legacy/stage0.env" && grep -q 'still has its stage-0 names' "$tmp/out" \
  && ok "a worker in the environment file moves the host to the worker profile, under its stage-0 names" \
  || fail "the move: exit $rc, $(cat "$tmp/out"), $(cat "$legacy/stage0.env")"
exit $failed
