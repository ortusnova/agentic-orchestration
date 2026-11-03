#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks that stage0.sh restarts the worker daemon only when the binary it
# builds changes (worker-build.sh): a change under worker-daemon/ that the
# binary does not hold, as a test or a document, leaves the daemon and the
# jobs it carries running; a change to its code restarts it. systemctl is
# stubbed; the daemon is built for real, from a copy of worker-daemon/.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

mkdir -p "$tmp/bin" "$STAGE0_HOME/bin" "$tmp/root"
cat > "$tmp/bin/systemctl" <<EOF
#!/bin/sh
case "\$2" in
  is-active) exit 0 ;;
  restart) echo "restart \$3" >> "$tmp/calls" ;;
esac
EOF
chmod +x "$tmp/bin/systemctl"
export PATH=$tmp/bin:$PATH
cp -r "$KIT/../../worker-daemon" "$tmp/root/worker-daemon"
# Built in two places, as the live kit and a checkout are: the binary must
# not hold the path it was built from.
cp -r "$tmp/root" "$tmp/other"
ROOT=$tmp/root
UNITS=$tmp/units
mkdir -p "$UNITS"
echo unit > "$UNITS/ortus-stage0-worker.service"
echo '{}' > "$STAGE0_HOME/worker.json"
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
# shellcheck source=worker-build.sh
. "$KIT/worker-build.sh"
calls() { [[ -f $tmp/calls ]] && tr '\n' ';' < "$tmp/calls"; true; }
deploy() { rm -f "$tmp/calls"; build_worker_daemon && restart_worker 2> /dev/null; }

deploy
[[ -z $(calls) ]] && ok "a running daemon an earlier kit started is adopted" || fail "first: $(calls)"
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
[[ $(calls) == "restart ortus-stage0-worker.service;" ]] && ok "a code change restarts it" || fail "code: $(calls)"
deploy; [[ -z $(calls) ]] && ok "and it then runs what is built" || fail "after code: $(calls)"

echo '{"a":1}' > "$STAGE0_HOME/worker.json"
deploy
[[ $(calls) == "restart ortus-stage0-worker.service;" ]] && ok "a configuration change restarts it" || fail "config: $(calls)"
echo unit2 > "$UNITS/ortus-stage0-worker.service"
deploy
[[ $(calls) == "restart ortus-stage0-worker.service;" ]] && ok "a unit change restarts it" || fail "unit: $(calls)"

# An earlier kit recorded the daemon's source: a daemon running that very
# source is adopted with its jobs, and one started from other source is
# restarted.
rec=$STAGE0_HOME/started/ortus-stage0-worker.service.sum
inputs_sum "$ROOT/worker-daemon" "$STAGE0_HOME/worker.json" "$UNITS/ortus-stage0-worker.service" > "$rec"
deploy; [[ -z $(calls) ]] && ok "a daemon an earlier kit started from this source keeps running" || fail "migrate: $(calls)"
deploy; [[ -z $(calls) ]] && ok "and its record then names the binary" || fail "migrated: $(calls)"
echo "an older source" > "$rec"
deploy
[[ $(calls) == "restart ortus-stage0-worker.service;" ]] && ok "one started from other source restarts" || fail "old source: $(calls)"

# stage0.sh builds the daemon and decides its restart only through
# worker-build.sh.
if grep -nE 'go build .*worker-daemon' "$KIT/stage0.sh"; then
  fail "stage0.sh builds the worker daemon outside build_worker_daemon"
else
  ok "stage0.sh builds the worker daemon only with build_worker_daemon"
fi
exit $failed
