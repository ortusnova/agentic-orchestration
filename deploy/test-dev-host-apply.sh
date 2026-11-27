#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016 # ok and fail always succeed; the stubs' text
# Checks dev-host-apply.sh with deploy.sh and systemctl stubbed: an apply
# after a host change (a smolvm bump) runs at once, whatever runs, then this
# host's deploy (deploy.sh ENV --this-host); a failed apply or deploy fails
# it with its error in the deploy state; it waits for the host's lock; and
# it drains nothing.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null ORTUS_NOVA_HOME=$tmp/home ORTUS_NOVA_ENV=test-a ORTUS_NOVA_LIVE_KIT=$tmp/live
unset ORTUS_NOVA_LOCKED
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
g() { git -c user.name=t -c user.email=t@t -c init.defaultBranch=main "$@"; }

mkdir -p "$ORTUS_NOVA_HOME" "$tmp/bin" "$tmp/dkit"
g init -q "$tmp/live"; echo 1 > "$tmp/live/x"; g -C "$tmp/live" add x; g -C "$tmp/live" commit -qm one
main=$(g -C "$tmp/live" rev-parse HEAD)
cp "$KIT/dev-host-apply.sh" "$KIT/deploy-state.sh" "$KIT/unit-restart.sh" "$KIT/names.sh" "$tmp/dkit/"
printf '#!/bin/sh\necho "deploy.sh $*" >> "%s/calls"\n[ -z "$FAIL_DEPLOY" ]\n' "$tmp" > "$tmp/dkit/deploy.sh"
printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/systemctl"
# Any request to Sortie fails the test: an apply asks it nothing.
printf '#!/bin/sh\necho "curl $*" >> "%s/asked"\nexit 22\n' "$tmp" > "$tmp/bin/curl"
chmod +x "$tmp/dkit/deploy.sh" "$tmp/bin/"*
export PATH=$tmp/bin:$PATH FAIL_DEPLOY=''
printf 'ORTUS_NOVA_PROFILE=control\n' > "$ORTUS_NOVA_HOME/host.env"

apply() { rm -f "$tmp/calls"; rc=0; "$tmp/dkit/dev-host-apply.sh" test-a "$@" > /dev/null 2> "$tmp/err" || rc=$?; }
calls() { if [[ -f $tmp/calls ]]; then tr '\n' ';' < "$tmp/calls"; fi; }
applied() { # applied NAME WANT_RC WANT_CALLS
  [[ $rc == "$2" && $(calls) == "$3" ]] && ok "$1" || fail "$1: exit $rc, calls '$(calls)', want $2 and '$3': $(cat "$tmp/err")"
}
field() { sed -n "s/^  \"$1\": \"\\(.*\\)\",\\{0,1\\}\$/\\1/p" "$ORTUS_NOVA_HOME/deploy-state.json"; }
state() { # state NAME STATUS [ERROR]
  [[ $(field status) == "$2" && $(field commit) == "$main" && $(field error) == *"${3:-}"* ]] && ok "$1" \
    || fail "$1: $(cat "$ORTUS_NOVA_HOME/deploy-state.json")"
}

apply sh -c 'echo apply >> "$0/calls"' "$tmp"
applied "an apply runs at once, then this host's deploy" 0 "apply;deploy.sh test-a --this-host;"
state "and its deploy state says succeeded" succeeded
apply false
applied "a failed apply stops before the deploy" 1 ""
state "and is failed, with its error" failed "the dev-host apply failed: false failed"
FAIL_DEPLOY=1 apply true
applied "a failed deploy after the apply fails it" 1 "deploy.sh test-a --this-host;"
state "and is failed" failed "deploy.sh failed after the apply"
mkdir -p "$ORTUS_NOVA_HOME/started"; echo restarting > "$ORTUS_NOVA_HOME/started/ortus-nova-test-a-egress-gate.service.sum"
apply true
applied "an apply whose unit did not restart fails" 1 "deploy.sh test-a --this-host;"
state "naming the unit" failed "ortus-nova-test-a-egress-gate.service"
rm -rf "$ORTUS_NOVA_HOME/started"
apply
applied "a control host has no default apply" 1 ""
# A worker host: the apply is make dev-host in its homelab checkout.
mkdir -p "$tmp/homelab"; printf 'dev-host:\n\techo make dev-host >> %s/calls\n' "$tmp" > "$tmp/homelab/Makefile"
printf 'ORTUS_NOVA_PROFILE=worker\nORTUS_NOVA_HOMELAB_SRC=%s\n' "$tmp/homelab" > "$ORTUS_NOVA_HOME/host.env"
apply
applied "a worker host's apply is make dev-host in its homelab checkout" 0 "make dev-host;deploy.sh test-a --this-host;"
exec 8> "$ORTUS_NOVA_HOME/deploy.lock"; flock 8
rc=0; timeout 3 "$tmp/dkit/dev-host-apply.sh" test-a true 2> "$tmp/err" || rc=$?
(( rc == 124 )) && grep -q 'waiting for a deploy.sh or a deploy' "$tmp/err" && ok "the apply waits for the host's lock" || fail "lock: exit $rc, $(cat "$tmp/err")"
exec 8>&-
[[ ! -e $tmp/asked ]] && ok "it asks Sortie nothing" || fail "asked: $(cat "$tmp/asked")"
if grep -rqs 'drain.sh\|dispatch_pause\|dispatch/pause' "$KIT/deploy.sh" "$KIT/push.sh" "$KIT/bootstrap.sh" "$KIT/dev-host-apply.sh"; then
  fail "the kit still drains"
else ok "neither a push nor the dev-host apply drains"; fi
exit $failed
