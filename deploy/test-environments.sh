#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016,SC2012,SC2030,SC2031 # ok and fail always succeed; the stubs' text; listings of plain names; subshells on purpose
# Checks deploy.sh <env> against throwaway environment files (spec store
# deploy-environments, design D1-D4, D6, D8), each run with a throwaway
# HOME and systemctl, smolvm, curl, go, hostname and getent stubbed: what a
# worker host's step installs and under which names, that two environments
# on one host share nothing, the refusals, that no script branches on an
# environment's name, and that a host under its stage-0 names keeps them,
# as dev.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$KIT/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
unset ORTUS_NOVA_HOME ORTUS_NOVA_ENV ORTUS_NOVA_LOCKED STAGE0_HOME STAGE0_LOCKED

# The worker daemon this kit builds, to check each worker.json it renders.
daemon=$tmp/worker-daemon
(cd "$ROOT/worker-daemon" && go build -o "$daemon" ./cmd/worker-daemon)

# shellcheck source=testenv.sh
. "$KIT/testenv.sh"
testenv_init "$tmp/env"
# The stubs: every systemctl call is logged per HOME and every unit is
# active; smolvm is new enough; curl answers Pomerium 200; go builds a stub
# for each binary (the worker daemon's check accepts any configuration;
# smol-builder writes its output) and logs what it built.
cat > "$tmp/env/bin/systemctl" <<'EOF'
#!/bin/sh
[ "$2" = is-active ] || [ "$2" = is-enabled ] && exit 0
echo "systemctl $*" >> "$HOME/systemctl.calls"
EOF
printf '#!/bin/sh\n[ "$1" = --version ] && echo "smolvm 9.9.9"\nexit 0\n' > "$tmp/env/bin/smolvm"
printf '#!/bin/sh\nprintf 200\n' > "$tmp/env/bin/curl"
cat > "$tmp/env/bin/go" <<'EOF'
#!/bin/sh
[ "$1" = build ] || exit 0
out='' pkg=''
while [ $# -gt 0 ]; do
  case $1 in -o) out=$2; shift ;; ./*) pkg=$1 ;; esac
  shift
done
echo "go build $pkg" >> "$HOME/go.calls"
case $pkg in
  ./cmd/smol-builder) printf '#!/bin/sh\nwhile [ $# -gt 0 ]; do [ "$1" = --output ] && echo pack > "$2"; shift; done\necho {}\n' > "$out" ;;
  *) printf '#!/bin/sh\nexit 0\n' > "$out" ;;
esac
chmod +x "$out"
EOF
chmod +x "$tmp/env/bin/"*

# worker_host HOME ENV: what worker-host.sh add would have copied there.
worker_host() {
  local d=$1/.config/ortus-nova${3:+/}${3:-}
  mkdir -p "$d"
  for f in egress-ca.crt cp-worker.crt cp-worker.key cp-ca.crt; do echo "$f" > "$d/$f"; done
  echo "https://control:$2" > "$d/control-plane-url"
}
# deploy HOME ENV [ARG...]: this host's step of ENV, from this checkout.
deploy() {
  local home=$1
  shift
  rc=0
  local env=$1
  shift
  HOME=$home ORTUS_NOVA_KIT_FROM_CHECKOUT=1 ORTUS_NOVA_HOST_ROOT=$tmp/nohost "$KIT/deploy.sh" "$env" --this-host "$@" > "$home/out" 2>&1 || rc=$?
}
units() { (cd "$1/.config/systemd/user" 2> /dev/null && ls) | tr '\n' ' '; }

testenv_file test-a 1 control=control+worker testhost=worker
testenv_file test-b 2 control=control+worker testhost=worker

# --- A worker host of test-a --------------------------------------------
h=$tmp/a
worker_host "$h" 10143 test-a
deploy "$h" test-a
(( rc == 0 )) && ok "deploy.sh test-a installs a worker host" || fail "test-a: exit $rc, $(cat "$h/out")"
[[ $(units "$h") == "ortus-nova-test-a-logs-rotate.service ortus-nova-test-a-logs-rotate.timer ortus-nova-test-a-logs.service ortus-nova-test-a-worker.service " ]] \
  && ok "a worker host gets the worker daemon and its logs, named by the environment, and nothing of the control plane" \
  || fail "units: $(units "$h")"
conf=$h/.local/share/ortus-nova/test-a/worker.json
python3 - "$conf" <<'PY' && ok "worker.json declares test-a, and names its machines wd-test-a- with the label ortus-nova.worker=test-a/testhost" || fail "worker.json: $(cat "$conf")"
import json, sys
c = json.load(open(sys.argv[1]))
sys.exit(0 if (c["deploy_env"], c["machine_prefix"], c["machine_label"], c["worker_id"]) == ("test-a", "wd-test-a-", "ortus-nova.worker=test-a/testhost", "testhost") else 1)
PY
"$daemon" check -config "$conf" > "$tmp/check" 2>&1 && ok "the worker daemon accepts it" || fail "check: $(cat "$tmp/check")"
installed=$( (cd "$h" && find .local .config -type f -o -type d) | sort)
if grep -i stage0 <<< "$installed" || grep -rqi stage0 "$h/.config/systemd/user" "$h/.local/share/ortus-nova/test-a/host.env"; then
  fail "a name, path or setting holds stage0"
else ok "no installed name, path, unit or setting holds stage0"; fi
grep -q "^ORTUS_NOVA_ENV=test-a$" "$h/.local/share/ortus-nova/test-a/environment.env" \
  && grep -qx 'Environment=ORTUS_NOVA_ENV=test-a' "$h/.config/systemd/user/ortus-nova-test-a-logs.service" \
  && grep -qx "Environment=ORTUS_NOVA_LOGS_DIR=$h/.local/state/ortus-nova/test-a/logs" "$h/.config/systemd/user/ortus-nova-test-a-logs.service" \
  && ok "the units carry the environment and its log directory" || fail "unit environment: $(cat "$h/.config/systemd/user/ortus-nova-test-a-logs.service")"

# --- test-b beside it, on the same host ---------------------------------
worker_host "$h" 10243 test-b
cp -r "$h/.config/systemd/user" "$tmp/a-units-before"
a_before=$(cd "$h/.local/share/ortus-nova/test-a" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
: > "$h/systemctl.calls"
deploy "$h" test-b
(( rc == 0 )) && ok "deploy.sh test-b installs beside test-a" || fail "test-b: exit $rc, $(cat "$h/out")"
b_units=$(cd "$h/.config/systemd/user" && ls ortus-nova-test-b-* | tr '\n' ' ')
[[ $b_units == "ortus-nova-test-b-logs-rotate.service ortus-nova-test-b-logs-rotate.timer ortus-nova-test-b-logs.service ortus-nova-test-b-worker.service " ]] \
  && ok "test-b has its own units" || fail "test-b units: $b_units"
diff -q <(cd "$tmp/a-units-before" && cat ./*) <(cd "$h/.config/systemd/user" && cat ortus-nova-test-a-*) > /dev/null \
  && ok "test-b's install changes no unit of test-a" || fail "test-a's units changed"
[[ $(cd "$h/.local/share/ortus-nova/test-a" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) == "$a_before" ]] \
  && ok "nor any file of test-a's" || fail "test-a's files changed"
if grep -q 'test-a' "$h/systemctl.calls"; then fail "test-b's install touched a test-a unit: $(grep test-a "$h/systemctl.calls")"
else ok "and starts, stops or restarts no test-a unit"; fi
if grep -rq 'test-a' "$h/.config/systemd/user/"ortus-nova-test-b-* "$h/.local/share/ortus-nova/test-b/worker.json" "$h/.local/share/ortus-nova/test-b/host.env"; then
  fail "a test-b unit or setting names test-a"
else ok "no test-b unit, setting or machine label names test-a"; fi
grep -q '"machine_label": "ortus-nova.worker=test-b/testhost"' "$h/.local/share/ortus-nova/test-b/worker.json" \
  && ok "test-b's machines carry test-b's label" || fail "test-b label"
# shellcheck source=names.sh
. "$KIT/names.sh"
a=$(HOME=$h; env_names test-a; echo "$ORTUS_NOVA_UNIT_PREFIX $ORTUS_NOVA_PODMAN_PREFIX $ORTUS_NOVA_LOCAL_NAME_FILE $ORTUS_NOVA_HOME $ORTUS_NOVA_LOG_DIR $ORTUS_NOVA_LOCK_FILE")
b=$(HOME=$h; env_names test-b; echo "$ORTUS_NOVA_UNIT_PREFIX $ORTUS_NOVA_PODMAN_PREFIX $ORTUS_NOVA_LOCAL_NAME_FILE $ORTUS_NOVA_HOME $ORTUS_NOVA_LOG_DIR $ORTUS_NOVA_LOCK_FILE")
[[ $a != *test-b* && $b != *test-a* && $(tr ' ' '\n' <<< "$a" | grep -vx deploy.lock | grep -cxFf - <(tr ' ' '\n' <<< "$b")) == 0 ]] \
  && ok "the two share no unit prefix, podman name, local-names file, state or log directory" || fail "names: $a / $b"
: > "$h/systemctl.calls"
deploy "$h" test-b
if grep -q 'test-a' "$h/systemctl.calls"; then fail "test-b again touched test-a"; else ok "deploying test-b again restarts no test-a unit"; fi

# --- A worker dials only its environment's control plane ----------------
h=$tmp/crossed
worker_host "$h" 10243 test-a
deploy "$h" test-a
(( rc != 0 )) && grep -q 'names https://control:10243' "$h/out" && grep -q "control plane is https://control:10143" "$h/out" \
  && [[ ! -e $h/.config/systemd/user/ortus-nova-test-a-worker.service ]] \
  && ok "a worker whose control-plane-url names another environment's front is refused, naming both, and its daemon is not started" \
  || fail "crossed: exit $rc, $(cat "$h/out")"
h=$tmp/bare
mkdir -p "$h"
deploy "$h" test-a
(( rc != 0 )) && grep -q 'run deploy/worker-host.sh test-a add testhost' "$h/out" && grep -q 'on control' "$h/out" \
  && [[ ! -e $h/.config/systemd/user/ortus-nova-test-a-worker.service ]] \
  && ok "a worker with no files of its environment is refused, naming the worker-host.sh command to run on the control host" \
  || fail "bare: exit $rc, $(cat "$h/out")"
# The control host warns of each declared worker with no certificate yet.
missing=$(ORTUS_NOVA_HOME=$tmp/c/home; . "$KIT/front-config.sh"
  # shellcheck disable=SC2329 # front_workers calls it
  cpctl() { printf 'NAME EXPIRES ROUTES FINGERPRINT\ncontrol x y z\nalpha x y z\n'; }
  front_missing_workers control control alpha beta)
[[ $missing == beta ]] && grep -q 'front_missing_workers' "$KIT/deploy.sh" && grep -q 'say "Warning: worker host $name' "$KIT/deploy.sh" \
  && ok "the control host's deploy warns, without failing, of each declared worker with no certificate" || fail "missing: '$missing'"

# --- The control host's units -------------------------------------------
# Every unit a control host installs, rendered for test-a, is named and
# configured by test-a, and no template names another environment.
bad=''
for t in "$KIT"/units/ortus-nova-*; do
  part=${t##*/units/ortus-nova-}
  out=$(HOME=$tmp/c; env_names test-a; unit "$part"; render_unit "$KIT" "$part")
  [[ $out == ortus-nova-test-a-* ]] && ! grep -qi 'stage0' <<< "$out" && ! grep -q '@[A-Z_]*@' <<< "$out" || bad+=" $part"
done
[[ -z $bad ]] && ok "every unit template renders under the environment's names, with no stage0 and nothing left unrendered" || fail "units:$bad"

# --- The local name -----------------------------------------------------
# shellcheck source=local-name.sh
. "$KIT/local-name.sh"
d=$tmp/ln
mkdir -p "$d"; echo "other 1" > "$d/homelab-thing"
(HOME=$tmp/c; env_names test-a; write_local_name "$d" test-a.homelab.dev 10193 > /dev/null; env_names test-b; write_local_name "$d" test-b.homelab.dev 10293 > /dev/null)
[[ $(ls "$d" | tr '\n' ' ') == "homelab-thing ortus-nova-test-a-workbench ortus-nova-test-b-workbench " ]] \
  && grep -qx 'test-a.homelab.dev 10193' "$d/ortus-nova-test-a-workbench" && ok "each environment's workbench has its own local-names file" || fail "local names: $(ls "$d")"
changed=$(HOME=$tmp/c; env_names test-a; write_local_name "$d" '' 10193)
[[ $changed == changed && ! -e $d/ortus-nova-test-a-workbench && -e $d/ortus-nova-test-b-workbench && $(cat "$d/homelab-thing") == "other 1" ]] \
  && ok "a local name removed from the file removes only its own file" || fail "removed: $changed, $(ls "$d")"

# --- Refusals -----------------------------------------------------------
h=$tmp/mini
mkdir -p "$h"
TESTENV_HOST=mini deploy "$h" test-a
(( rc != 0 )) && grep -q 'mini' "$h/out" && grep -q 'control testhost' "$h/out" && [[ ! -e $h/.local/share/ortus-nova && ! -e $h/.config/systemd ]] \
  && ok "an undeclared host is refused, naming it and the declared hosts, and nothing is written" || fail "mini: exit $rc, $(cat "$h/out")"
h=$tmp/typo
mkdir -p "$h"
TESTENV_UNRESOLVED=control deploy "$h" test-a
(( rc != 0 )) && grep -q 'control' "$h/out" && grep -q 'does not resolve' "$h/out" && [[ ! -e $h/.local/share/ortus-nova ]] \
  && ok "a host key nothing resolves is refused, naming it" || fail "unresolved: exit $rc, $(cat "$h/out")"
deploy "$h" test-c
(( rc != 0 )) && grep -q 'test-c.yml' "$h/out" && ok "an environment with no file is refused" || fail "test-c: exit $rc"

# --- No script branches on an environment's name ------------------------
names_compared() { # names_compared DIR: each comparison of an environment name to a literal
  grep -rnE '(==|!=|=~)[[:space:]]*"?(dev|prod)"?([[:space:]]|\]|$)|^[[:space:]]*"?(dev|prod)"?\)' "$1" \
    --include='*.sh' --include='*.py' --include='*.go' --exclude='test-*' --exclude='*_test.go' --exclude-dir=environments || true
}
found=$(names_compared "$KIT")
[[ -z $found ]] && ok "no script of the kit compares an environment's name to a literal" || fail "branches on a name: $found"
mkdir -p "$tmp/branch"; printf 'if [[ $env == prod ]]; then :; fi\n' > "$tmp/branch/x.sh"
[[ $(names_compared "$tmp/branch") == *"x.sh:1:"* ]] && ok "and the check finds one, naming the file and line" || fail "the check missed a branch"

# --- The control host's copy of a worker's deploy state (push.sh) ------
# From the worker's state directory under the environment's names, and
# from its stage-0 one while it has no other, so a half-migrated
# environment keeps its copies.
w=$tmp/worker-host
printf '#!/bin/sh\nwhile [ "$1" = -o ]; do shift 2; done\nshift\ncd "%s" && exec "$@"\n' "$w" > "$tmp/env/bin/ssh"
chmod +x "$tmp/env/bin/ssh"
mkdir -p "$w/.local/share/ortus-nova/stage0" "$w/.local/share/ortus-nova/test-a"
state() { printf '{\n  "status": "%s",\n  "updated_at": "2026-11-27T00:00:00Z"\n}\n' "$1"; }
state from-stage0 > "$w/.local/share/ortus-nova/stage0/deploy-state.json"
keep() { (HOME=$tmp/c ORTUS_NOVA_HOME=$tmp/c/home THIS_HOST=control; env_names test-a; . "$KIT/deploy-state.sh"; . "$KIT/push.sh"
  push_keep_state taichi > /dev/null; deploy_state_field "$ORTUS_NOVA_HOME/hosts/taichi/deploy-state.json" status); }
pulled=$(keep)
state from-test-a > "$w/.local/share/ortus-nova/test-a/deploy-state.json"
pulled+=" $(keep)"
[[ $pulled == "from-stage0 from-test-a" ]] && ok "the control host copies a worker's deploy state from its new home, else its stage-0 one" || fail "pulled: $pulled"
rm -f "$tmp/env/bin/ssh"

# --- names.sh is the only place a name is spelled ----------------------
spelled=$(cd "$KIT" && grep -nE 'ortus-stage0-|ortus-nova-[a-z]|\.local/share/ortus-nova|\.local/state/ortus-nova|\.config/ortus-nova|stage0\.env|host\.env|stage0\.lock|deploy\.lock' ./*.sh ./*.py \
  | grep -v '^./names.sh:\|^./test-\|^./testenv.sh:\|^./bootstrap.sh:' | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' | grep -v 'ortus-nova-specs' || true)
[[ -z $spelled ]] && ok "no script but names.sh spells an installed name" || fail "spelled outside names.sh: $spelled"

# --- A host under its stage-0 names -------------------------------------
# taichi is still installed under its stage-0 names: its step of a push of
# dev, from dev.yml, deploys it under them.
h=$tmp/taichi
s0=$h/.local/share/ortus-nova/stage0
mkdir -p "$s0" "$h/.config/systemd/user"
printf 'STAGE0_PROFILE=worker\nSTAGE0_KIT_ROOT=%s\nSTAGE0_WORKER_NAME=taichi\n' "$ROOT" > "$s0/stage0.env"
worker_host "$h" 8443
echo https://p1:8443 > "$h/.config/ortus-nova/control-plane-url"
rc=0
(export HOME=$h ORTUS_NOVA_KIT_FROM_CHECKOUT=1 ORTUS_NOVA_HOST_ROOT=$tmp/nohost TESTENV_HOST=taichi
  unset ORTUS_NOVA_ENVIRONMENTS
  "$KIT/deploy.sh" dev --this-host) > "$h/out" 2>&1 || rc=$?
(( rc == 0 )) && ok "taichi's step deploys it as dev" || fail "taichi: exit $rc, $(cat "$h/out")"
[[ $(units "$h") == "ortus-stage0-logs-rotate.service ortus-stage0-logs-rotate.timer ortus-stage0-logs.service ortus-stage0-worker.service " ]] \
  && [[ ! -e $h/.local/share/ortus-nova/dev ]] \
  && ok "under its stage-0 units and state directory" || fail "taichi units: $(units "$h"), $(ls "$h/.local/share/ortus-nova")"
python3 - "$s0/worker.json" <<'PY' && ok "its worker declares dev and keeps today's machine prefix and label" || fail "taichi worker.json: $(cat "$s0/worker.json")"
import json, sys
c = json.load(open(sys.argv[1]))
sys.exit(0 if (c["deploy_env"], c["machine_prefix"], c["machine_label"]) == ("dev", "wd-", "worker-daemon.worker=taichi") else 1)
PY
"$daemon" check -config "$s0/worker.json" > "$tmp/check" 2>&1 && ok "which the worker daemon accepts" || fail "check: $(cat "$tmp/check")"
grep -q 'still has its stage-0 names' "$h/out" && grep -q -- '--migrate-stage0' "$h/out" && ok "and every run says the host still has its stage-0 names" || fail "no stage-0 note"
grep -qx 'Environment=ORTUS_NOVA_LOGS_DIR='"$h"'/.local/state/ortus-nova/logs' "$h/.config/systemd/user/ortus-stage0-logs.service" \
  && grep -qx "Environment=ORTUS_NOVA_HOME=$s0" "$h/.config/systemd/user/ortus-stage0-logs.service" \
  && ok "its logs stay where they were" || fail "logs unit"
grep -qx 'STAGE0_PROFILE=worker' "$s0/stage0.env" && ok "its settings file keeps its stage-0 keys" || fail "stage0.env: $(cat "$s0/stage0.env")"
# A profile the file contradicts: p1 says control, its stage0.env worker.
h=$tmp/p1
s0=$h/.local/share/ortus-nova/stage0
mkdir -p "$s0"
printf 'STAGE0_PROFILE=worker\nSTAGE0_KIT_ROOT=%s\n' "$ROOT" > "$s0/stage0.env"
rc=0
(export HOME=$h ORTUS_NOVA_KIT_FROM_CHECKOUT=1 TESTENV_HOST=p1
  unset ORTUS_NOVA_ENVIRONMENTS
  "$KIT/deploy.sh" dev --this-host) > "$tmp/p1.out" 2>&1 || rc=$?
(( rc != 0 )) && grep -q 'STAGE0_PROFILE=worker' "$tmp/p1.out" && grep -q 'control' "$tmp/p1.out" && [[ ! -e $h/.config/systemd ]] \
  && [[ $(ls -A "$s0") == stage0.env ]] && grep -qx 'STAGE0_PROFILE=worker' "$s0/stage0.env" \
  && ok "a stage-0 profile the environment file contradicts is refused, naming both, and no unit is written" || fail "p1: exit $rc, $(cat "$tmp/p1.out")"
exit $failed
