#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks worker-host.sh with ssh and systemctl stubbed, against control-plane/'s
# cpctl: each worker host gets a client certificate from the control plane's
# CA, copied to it with the egress CA's certificate and Pomerium's URL;
# Pomerium's configuration admits exactly the listed workers and routes to
# p1's Sortie and egress gate on loopback; a removed worker is refused on
# every route (spec store agent-platform/control-plane, "Revoked worker").
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

mkdir -p "$tmp/bin" "$tmp/remote" "$ORTUS_NOVA_HOME/bin" "$ORTUS_NOVA_HOME/egress-ca"
# ssh stands in for the worker host: each command runs in $tmp/remote, its home.
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\necho "ssh $*" >> "%s/calls"\nwhile [ "$1" = -o ]; do shift 2; done\nshift\ncd "%s/remote" && HOME="%s/remote" sh -c "$*"\n' \
  "$tmp" "$tmp" "$tmp" > "$tmp/bin/ssh"
# Pomerium is not enabled yet.
# shellcheck disable=SC2016
printf '#!/bin/sh\necho "systemctl $*" >> "%s/calls"\ncase "$*" in *is-enabled*) exit 1 ;; esac\n' "$tmp" > "$tmp/bin/systemctl"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH
cpctl_bin=${CPCTL_BIN:-}
if [[ -z $cpctl_bin ]]; then
  cpctl_bin=$ORTUS_NOVA_HOME/bin/cpctl
  (cd "$KIT/../control-plane" && go build -o "$cpctl_bin" ./cmd/cpctl)
else
  cp "$cpctl_bin" "$ORTUS_NOVA_HOME/bin/cpctl"
fi
printf 'ORTUS_NOVA_SORTIE_PORT=7680\nORTUS_NOVA_CP_PUBLIC_URL=https://p1.example.org:8443\n' > "$ORTUS_NOVA_HOME/host.env"
echo egress-ca-cert > "$ORTUS_NOVA_HOME/egress-ca/ca.crt"
# shellcheck source=testenv.sh
. "$KIT/testenv.sh"
testenv_init "$tmp/env"
testenv_file test-a 1 p1=control+worker taichi=worker mac-mini=worker
sed -i 's/  taichi: {runs: \[worker\]}/  taichi: {runs: [worker], ssh: brian@taichi}/; s/  mac-mini: {runs: \[worker\]}/  mac-mini: {runs: [worker], ssh: mac.lan}/' "$tmp/env/environments/test-a.yml"
export TESTENV_HOST=p1
wh() { "$KIT/worker-host.sh" test-a "$@" > "$tmp/out" 2>&1; }
config() { cat "$ORTUS_NOVA_HOME/control-plane/pomerium/config.yaml"; }
fingerprint() { "$ORTUS_NOVA_HOME/bin/cpctl" -home "$ORTUS_NOVA_HOME/control-plane" worker list | awk -v n="$1" '$1 == n {print $4}'; }

wh add taichi && fail "a worker was added before deploy.sh set up the front" || ok "add needs the front deploy.sh sets up"

# What deploy.sh does on p1 (front-config.sh).
(
  # shellcheck source=/dev/null
  . "$ORTUS_NOVA_HOME/host.env"
  say() { :; }
  # shellcheck source=front-config.sh
  . "$KIT/front-config.sh"
  front_init
  issue_local_worker p1
)
config | grep -q '"to": "http://127.0.0.1:7680"' && config | grep -q '"to": "http://127.0.0.1:7692"' && config | grep -q '"address": ":8443"' \
  && ok "Pomerium routes to p1's Sortie and egress gate on loopback, on the public URL's port" || fail "config: $(config)"
config | grep -q '"enforcement": "reject_connection"' && ok "Pomerium rejects a connection without a certificate from the CA" || fail "no reject_connection"
p1fp=$(fingerprint p1)
[[ -n $p1fp ]] && config | grep -q "$p1fp" && [[ -s $ORTUS_NOVA_HOME/control-plane/workers/p1/worker.key ]] \
  && ok "p1's own worker is listed, and keeps its key on p1" || fail "p1: $p1fp"

rm -f "$tmp/calls"
wh add mini && fail "an undeclared host was added" || { grep -q 'mini is not a worker host of environment test-a' "$tmp/out" && ! grep -q '^ssh' "$tmp/calls" 2> /dev/null \
  && ok "a host the environment file does not declare is refused, naming it, with no SSH connection" || fail "mini: $(cat "$tmp/out")"; }
[[ ! -e $ORTUS_NOVA_HOME/control-plane/workers/mini ]] && ok "and is issued no certificate" || fail "mini has a certificate"
wh add taichi || fail "add taichi: $(cat "$tmp/out")"
fp=$(fingerprint taichi)
[[ -n $fp ]] && config | grep -q "$fp" && ok "taichi is listed on Pomerium's routes" || fail "taichi: $fp $(cat "$tmp/out")"
grep -q '^ssh -o BatchMode=yes -o ConnectTimeout=10 brian@taichi true' "$tmp/calls" && ! grep -q '^ssh.* taichi ' "$tmp/calls" \
  && ok "taichi is reached by the destination the environment file declares" || fail "ssh: $(grep ^ssh "$tmp/calls")"
remote=$tmp/remote/.config/ortus-nova/test-a
[[ $(cat "$remote/egress-ca.crt") == egress-ca-cert && $(cat "$remote/control-plane-url") == https://p1:10143 ]] \
  && ok "taichi gets the egress CA's certificate and the environment's control URL" || fail "remote: $(ls -la "$remote") $(cat "$remote/control-plane-url")"
[[ $(stat -c %a "$remote/cp-worker.key") == 600 ]] && openssl x509 -in "$remote/cp-worker.crt" -noout -ext subjectAltName 2> /dev/null | grep -q 'DNS:taichi' \
  && ok "taichi gets its certificate, for its name, and its key, mode 0600" || fail "certificate: $(ls -la "$remote")"
openssl verify -CAfile "$remote/cp-ca.crt" "$remote/cp-worker.crt" > /dev/null 2>&1 && ok "and the CA that issued it" || fail "cp-ca.crt does not verify it"
[[ ! -e $ORTUS_NOVA_HOME/control-plane/workers/taichi/worker.key ]] && ok "taichi's key does not stay on p1" || fail "taichi's key is still on p1"
grep -q 'restart ortus-nova-test-a-pomerium.service' "$tmp/calls" 2> /dev/null && fail "Pomerium restarted while not enabled" || ok "a Pomerium not yet enabled is left alone"

# Pomerium is enabled from here on.
# shellcheck disable=SC2016
printf '#!/bin/sh\necho "systemctl $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/systemctl"
wh add mac-mini || fail "add mac-mini: $(cat "$tmp/out")"
grep -q 'restart ortus-nova-test-a-pomerium.service' "$tmp/calls" && ok "Pomerium restarts onto the new list" || fail "calls: $(cat "$tmp/calls")"
wh list && grep -q '^mac-mini ' "$tmp/out" && grep -q '^taichi ' "$tmp/out" && grep -q '^p1 ' "$tmp/out" \
  && ok "list shows every worker, p1's own too" || fail "list: $(cat "$tmp/out")"

wh add p1 && fail "p1 was added as a worker host" || ok "the control host is not added as a worker host"
wh add 'taichi;rm' && fail "a host with a shell character was added" || ok "a host with a shell character is refused"
wh add taichi mac-mini && fail "add took a second name" || ok "add takes one machine id"
(export TESTENV_HOST=taichi; wh add mac-mini) && fail "add ran on a host that does not run control" || ok "add runs on the control host only"

# Scenario: Revoked worker. taichi's certificate leaves every route.
wh remove taichi || fail "remove taichi: $(cat "$tmp/out")"
! config | grep -q "$fp" && config | grep -q "$p1fp" && ok "a removed worker's certificate is on no route, and p1's still is" || fail "after remove: $(config)"
wh remove p1 && fail "p1's own worker was removed" || ok "p1's own worker is not removed here"
wh remove taichi && fail "taichi was removed twice" || ok "an unlisted worker cannot be removed"
wh add taichi || fail "add taichi back: $(cat "$tmp/out")"
[[ $(fingerprint taichi) != "$fp" && -n $(fingerprint taichi) ]] && ok "a worker added back gets a new certificate" || fail "re-add: $(fingerprint taichi)"
exit $failed
