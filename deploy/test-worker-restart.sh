#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks that deploy.sh restarts the worker daemon only when the binary it
# builds changes (worker-build.sh): a change under worker-daemon/ that the
# binary does not hold, as a test or a document, leaves the daemon and the
# jobs it carries running; a change to its code restarts it. systemctl is
# stubbed; the daemon is built for real, from a copy of worker-daemon/.
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

mkdir -p "$tmp/bin" "$ORTUS_NOVA_HOME/bin" "$tmp/root"
cat > "$tmp/bin/systemctl" <<EOF
#!/bin/sh
case "\$2" in
  is-active) exit 0 ;;
  restart) echo "restart \$3" >> "$tmp/calls" ;;
esac
EOF
chmod +x "$tmp/bin/systemctl"
export PATH=$tmp/bin:$PATH
cp -r "$KIT/../worker-daemon" "$tmp/root/worker-daemon"
# Built in two places, as the live kit and a checkout are: the binary must
# not hold the path it was built from.
cp -r "$tmp/root" "$tmp/other"
ROOT=$tmp/root
UNITS=$tmp/units
mkdir -p "$UNITS"
echo unit > "$UNITS/ortus-nova-test-a-worker.service"
echo '{}' > "$ORTUS_NOVA_HOME/worker.json"
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
# shellcheck source=worker-build.sh
. "$KIT/worker-build.sh"
calls() { [[ -f $tmp/calls ]] && tr '\n' ';' < "$tmp/calls"; true; }
deploy() { rm -f "$tmp/calls"; build_worker_daemon && restart_worker 2> /dev/null; }

deploy
[[ $(calls) == "restart ortus-nova-test-a-worker.service;" ]] && ok "a running daemon with no record of what it runs restarts once" || fail "first: $(calls)"
deploy; [[ -z $(calls) ]] && ok "the same kit leaves it running" || fail "same: $(calls)"

cat > "$ROOT/worker-daemon/internal/daemon/zz_extra_test.go" <<'EOF'
package daemon_test

import "testing"

func TestNothing(t *testing.T) {}
EOF
echo "more words" >> "$ROOT/worker-daemon/README.md"
deploy; [[ -z $(calls) ]] && ok "a test-only or document change leaves it running" || fail "test-only: $(calls)"

ROOT=$tmp/other deploy
[[ -z $(calls) ]] && ok "the same source built elsewhere leaves it running" || fail "elsewhere: $(calls)"

sed -i 's/^var version = "dev"$/var version = "dev-changed"/' "$ROOT/worker-daemon/cmd/worker-daemon/main.go"
grep -q dev-changed "$ROOT/worker-daemon/cmd/worker-daemon/main.go" || fail "the code change was not made"
deploy
[[ $(calls) == "restart ortus-nova-test-a-worker.service;" ]] && ok "a code change restarts it" || fail "code: $(calls)"
deploy; [[ -z $(calls) ]] && ok "and it then runs what is built" || fail "after code: $(calls)"

echo '{"a":1}' > "$ORTUS_NOVA_HOME/worker.json"
deploy
[[ $(calls) == "restart ortus-nova-test-a-worker.service;" ]] && ok "a configuration change restarts it" || fail "config: $(calls)"
echo unit2 > "$UNITS/ortus-nova-test-a-worker.service"
deploy
[[ $(calls) == "restart ortus-nova-test-a-worker.service;" ]] && ok "a unit change restarts it" || fail "unit: $(calls)"

# An earlier kit recorded the daemon's source: a daemon running that very
# source is adopted with its jobs, and one started from other source is
# restarted.
rec=$ORTUS_NOVA_HOME/started/ortus-nova-test-a-worker.service.sum
inputs_sum "$ROOT/worker-daemon" "$ORTUS_NOVA_HOME/worker.json" "$UNITS/ortus-nova-test-a-worker.service" > "$rec"
deploy; [[ -z $(calls) ]] && ok "a daemon an earlier kit started from this source keeps running" || fail "migrate: $(calls)"
deploy; [[ -z $(calls) ]] && ok "and its record then names the binary" || fail "migrated: $(calls)"
echo "an older source" > "$rec"
deploy
[[ $(calls) == "restart ortus-nova-test-a-worker.service;" ]] && ok "one started from other source restarts" || fail "old source: $(calls)"

# The worker daemon's restart keeps its jobs (worker-daemon's operator
# guide): systemd must leave the machines in its control group running.
grep -qx 'KillMode=process' "$KIT/units/ortus-nova-worker.service" \
  && ok "stopping the worker unit signals the daemon only, not its jobs' machines" \
  || fail "the worker unit kills its jobs' machines when it stops"

# deploy.sh builds the daemon and decides its restart only through
# worker-build.sh.
if grep -nE 'go build .*worker-daemon' "$KIT/deploy.sh"; then
  fail "deploy.sh builds the worker daemon outside build_worker_daemon"
else
  ok "deploy.sh builds the worker daemon only with build_worker_daemon"
fi
exit $failed
