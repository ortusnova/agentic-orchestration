#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016,SC2030,SC2031 # ok and fail always succeed; the stubs' text; subshells on purpose
# Checks the push (spec store deploy-environments, design D3, D6): deploy.sh
# <env> on the control host, against a throwaway origin whose kit's
# deploy.sh is a stub, with a throwaway HOME per host and ssh stubbed to run
# each command in the named host's HOME, recording each call: the order of a
# push, its stop at the first failure with each host's outcome and the
# failed host back on its commit, the refusals, --check, and a host under
# its stage-0 names.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null
unset ORTUS_NOVA_HOME ORTUS_NOVA_ENV ORTUS_NOVA_LOCKED
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
g() { git -c user.name=t -c user.email=t@t -c init.defaultBranch=main "$@"; }

# shellcheck source=testenv.sh
. "$KIT/testenv.sh"
testenv_init "$tmp/env"
hosts=$tmp/hosts
mkdir -p "$hosts/control" "$hosts/alpha" "$hosts/beta"
# ssh DEST CMD...: run CMD in DEST's HOME, unless DEST is in $UNREACHABLE.
cat > "$tmp/env/bin/ssh" <<EOF
#!/usr/bin/env bash
while [[ \$1 == -o ]]; do shift 2; done
echo "ssh \$*" >> "$tmp/ssh.calls"
# A real ssh carries none of this shell's environment.
unset ORTUS_NOVA_HOME ORTUS_NOVA_ENV ORTUS_NOVA_LOCKED
for u in \$UNREACHABLE; do [[ \$u == "\$1" ]] && exit 255; done
export HOME=$hosts/\${1#*@}
shift
cd "\$HOME" && exec "\$@"
EOF
chmod +x "$tmp/env/bin/ssh"

# The origin: a kit whose deploy.sh records each host's deploy and writes
# its deploy state, failing on a host whose HOME holds fail-at with its
# commit; names.sh and deploy-state.sh are the kit's own.
origin=$tmp/origin.git up=$tmp/up
g init -q --bare "$origin"; g clone -q "$origin" "$up" 2> /dev/null
mkdir -p "$up/deploy"
cp "$KIT/names.sh" "$KIT/deploy-state.sh" "$up/deploy/"
cat > "$up/deploy/deploy.sh" <<'EOF'
#!/usr/bin/env bash
# A stub of a kit that takes --this-host.
set -euo pipefail
d=$(cd "$(dirname "$0")" && pwd)
head=$(git -C "$d" rev-parse HEAD)
echo "${HOME##*/} deploy.sh $* at $head" >> "$HOME/calls"
# As the real one reads stage0_names from the environment file.
. "$d/names.sh"; ORTUS_NOVA_ENV_STAGE0_NAMES=true; env_names "$1"; . "$d/deploy-state.sh"
if [[ -e $HOME/fail-at && $(cat "$HOME/fail-at") == "$head" ]]; then echo "deploy.sh: the pack did not build" >&2; exit 1; fi
deploy_state_write succeeded "$head" "$head"
EOF
chmod +x "$up/deploy/deploy.sh"
commit() { echo "$RANDOM" >> "$up/x"; g -C "$up" add -A; g -C "$up" commit -qm "$1"; g -C "$up" push -q origin HEAD:main 2> /dev/null; g -C "$up" rev-parse HEAD; }
c1=$(commit one)
c2=$(commit two)
export ORTUS_NOVA_ORIGIN=$origin
testenv_file test-a 1 control=control+worker alpha=worker beta=worker

push() { # push HOST ARG...: deploy.sh test-a ARG... run on HOST
  local h=$1
  shift
  rm -f "$tmp/ssh.calls"
  rc=0
  (export HOME=$hosts/$h TESTENV_HOST=$h; "$KIT/deploy.sh" test-a "$@") > "$tmp/out" 2>&1 || rc=$?
}
calls() { cat "$hosts"/*/calls 2> /dev/null | sed "s/ at [0-9a-f]*$//" | tr '\n' ';'; }
at() { git -C "$hosts/$1/.local/share/ortus-nova/test-a/kit" rev-parse HEAD 2> /dev/null; }

# --- A push --------------------------------------------------------------
push control --commit "$c1"
(( rc == 0 )) && ok "a push of test-a succeeds" || fail "push: exit $rc, $(cat "$tmp/out")"
[[ $(cat "$hosts/control/calls") == "control deploy.sh test-a --this-host --commit $c1 at $c1" ]] \
  && [[ $(cat "$hosts/alpha/calls") == "alpha deploy.sh test-a --this-host --commit $c1 at $c1" ]] \
  && [[ $(cat "$hosts/beta/calls") == "beta deploy.sh test-a --this-host --commit $c1 at $c1" ]] \
  && ok "each host runs its own --this-host step at the commit" || fail "calls: $(calls)"
[[ $(grep -c . "$tmp/ssh.calls") -ge 2 && $(grep -m1 'bash -s' "$tmp/ssh.calls") == "ssh alpha bash -s -- test-a $c1 $origin false" ]] \
  && [[ $(grep 'bash -s' "$tmp/ssh.calls" | sed -n 2p) == "ssh beta bash -s -- test-a $c1 $origin false" ]] \
  && ! grep -q 'ssh control' "$tmp/ssh.calls" \
  && ok "the control host deploys itself here, then alpha, then beta over ssh, in machine-id order" || fail "ssh: $(cat "$tmp/ssh.calls")"
[[ $(at control) == "$c1" && $(at alpha) == "$c1" && $(at beta) == "$c1" ]] && ok "every live kit holds exactly the commit" || fail "kits: $(at control) $(at alpha) $(at beta)"
grep -q "alpha: succeeded at ${c1:0:12}" "$tmp/out" && grep -q "beta: succeeded at ${c1:0:12}" "$tmp/out" \
  && ok "the push prints every host's outcome" || fail "outcomes: $(cat "$tmp/out")"
grep -q "\"commit\": \"$c1\"" "$hosts/control/.local/share/ortus-nova/test-a/hosts/beta/deploy-state.json" \
  && ok "and keeps each other host's deploy state on the control host" || fail "no copy of beta's state"

# --- The first failure stops the push -----------------------------------
rm -f "$hosts"/*/calls
echo "$c2" > "$hosts/alpha/fail-at"
push control --commit "$c2"
(( rc != 0 )) && ok "a push with a failed host fails" || fail "exit $rc"
grep -q "control: succeeded at ${c2:0:12}" "$tmp/out" && grep -q "alpha: failed: deploy.sh failed at ${c2:0:12}: deploy.sh: the pack did not build" "$tmp/out" \
  && grep -q "beta: not deployed" "$tmp/out" && ok "it stops at alpha, naming each host's outcome" || fail "outcomes: $(cat "$tmp/out")"
[[ $(at alpha) == "$c1" && $(at beta) == "$c1" ]] && ok "alpha is back on the commit it ran, and beta untouched" || fail "kits: alpha $(at alpha), beta $(at beta)"
[[ $(sed -n 2p "$hosts/alpha/calls") == "alpha deploy.sh test-a --this-host --commit $c1 at $c1" ]] \
  && ok "alpha ran its earlier commit's deploy again" || fail "alpha: $(cat "$hosts/alpha/calls")"
[[ ! -e $hosts/beta/calls ]] && ok "beta was not deployed" || fail "beta: $(cat "$hosts/beta/calls")"
st=$hosts/alpha/.local/share/ortus-nova/test-a/deploy-state.json
grep -q '"status": "failed"' "$st" && grep -q "\"commit\": \"$c1\"" "$st" && grep -q "\"main\": \"$c2\"" "$st" \
  && ok "alpha's deploy state says failed, at the commit it runs, behind the target" || fail "state: $(cat "$st")"

# --- --check -------------------------------------------------------------
rm -f "$hosts/alpha/fail-at"
push control --commit "$c2" --check
(( rc != 0 )) && grep -q "control: at ${c2:0:12}, succeeded" "$tmp/out" && grep -q "alpha: at ${c1:0:12}, failed (target ${c2:0:12})" "$tmp/out" \
  && grep -q "beta: at ${c1:0:12}, succeeded (target ${c2:0:12})" "$tmp/out" && ok "--check names each host behind the target" || fail "check: exit $rc, $(cat "$tmp/out")"
UNREACHABLE=beta push control --commit "$c2" --check
(( rc != 0 )) && grep -q "beta: unreachable" "$tmp/out" && ok "and a host that cannot be reached" || fail "unreachable: $(cat "$tmp/out")"
rm -f "$hosts"/*/calls
UNREACHABLE=beta push control --commit "$c2"
(( rc != 0 )) && grep -q "beta: failed" "$tmp/out" && ok "a push to an unreachable host fails, naming it" || fail "unreachable push: $(cat "$tmp/out")"
push control --commit "$c2"
push control --commit "$c2" --check
(( rc == 0 )) && [[ $(at beta) == "$c2" ]] && ok "the next push catches it up, and --check passes" || fail "catch up: exit $rc, $(cat "$tmp/out")"
before=$(calls)
push control --commit "$c2" --check
[[ $(calls) == "$before" ]] && ok "--check runs no host's deploy" || fail "check deployed: $(calls)"

# --- Refusals ------------------------------------------------------------
rm -f "$hosts"/*/calls
push alpha --commit "$c2"
(( rc != 0 )) && grep -q 'alpha does not run control' "$tmp/out" && grep -q 'on control' "$tmp/out" && [[ -z $(calls) ]] \
  && ok "deploy.sh on a host that does not run control refuses, naming the control host" || fail "alpha: exit $rc, $(cat "$tmp/out")"
loose=$(g -C "$up" commit-tree "$(git -C "$up" rev-parse 'HEAD^{tree}')" -p HEAD -m loose)
git -C "$KIT/.." fetch -q "$up" "$loose" 2> /dev/null || true
push control --commit "$loose"
(( rc != 0 )) && grep -q "no branch of" "$tmp/out" && grep -q "$loose" "$tmp/out" && [[ -z $(calls) ]] \
  && ok "an unpushed commit is refused, naming it, and no host is deployed" || fail "unpushed: exit $rc, $(cat "$tmp/out")"
TESTENV_UNRESOLVED=beta push control --commit "$c2"
(( rc != 0 )) && grep -q 'beta' "$tmp/out" && grep -q 'does not resolve' "$tmp/out" && [[ -z $(calls) ]] \
  && ok "a host whose destination does not resolve is refused before any host is deployed" || fail "unresolved: exit $rc, $(cat "$tmp/out")"
testenv_file test-a 1 control=control+worker alpha=worker beta=worker
sed -i 's/  beta: {runs: \[worker\]}/  beta: {runs: [worker], ssh: brian@beta}/' "$tmp/env/environments/test-a.yml"
push control --commit "$c2"
(( rc == 0 )) && grep -q 'ssh brian@beta bash -s' "$tmp/ssh.calls" && ok "a host is reached by the ssh destination its entry declares" || fail "ssh dest: $(cat "$tmp/ssh.calls" "$tmp/out")"

# --- A host under its stage-0 names --------------------------------------
# beta still has a stage-0 install: its step deploys it there, and names
# nothing of test-a's.
testenv_file test-a 1 control=control+worker alpha=worker beta=worker stage0_names:\ true
rm -rf "$hosts/beta"; mkdir -p "$hosts/beta/.local/share/ortus-nova/stage0"
printf 'STAGE0_PROFILE=worker\n' > "$hosts/beta/.local/share/ortus-nova/stage0/stage0.env"
c3=$(commit three)
push control --commit "$c3"
(( rc == 0 )) && [[ $(git -C "$hosts/beta/.local/share/ortus-nova/stage0/kit" rev-parse HEAD) == "$c3" ]] \
  && [[ ! -e $hosts/beta/.local/share/ortus-nova/test-a ]] \
  && grep -q "\"commit\": \"$c3\"" "$hosts/beta/.local/share/ortus-nova/stage0/deploy-state.json" \
  && ok "a host under its stage-0 names is deployed there, its kit and state in its stage-0 home" || fail "stage-0 beta: exit $rc, $(cat "$tmp/out")"
push control --commit "$c3" --check
grep -q "beta: at ${c3:0:12}, succeeded, under its stage-0 names" "$tmp/out" && ok "and --check says so" || fail "check: $(cat "$tmp/out")"

# --- No host follows main by itself --------------------------------------
[[ ! -e $KIT/follow.sh && -z $(ls "$KIT"/units/*deploy* 2> /dev/null) && ! -e $KIT/../dev/stage0 ]] \
  && ! grep -q --exclude='test-*' 'deploy_state_pull\|deploy-hosts/' "$KIT"/*.sh \
  && ok "the follower, its timer and service, the minute copy and the stage-0 shims are gone" || fail "a follower is left"
exit $failed
