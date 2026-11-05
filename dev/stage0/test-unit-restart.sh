#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks unit-restart.sh with systemctl stubbed: stage0.sh restarts nono, the
# egress gate, Pomerium and the worker daemon, which carry the runs, only when what they run
# changed or they are not running, so a deploy that leaves them as they are
# interrupts no run (spec store agent-platform/run-supervision).
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

mkdir -p "$tmp/bin" "$STAGE0_HOME" "$tmp/src/sub"
# is-active fails while $tmp/down names the unit, and a restart brings it up
# unless $tmp/dies names it; restart fails while $tmp/broken does.
cat > "$tmp/bin/systemctl" <<EOF
#!/bin/sh
case "\$2" in
  is-active) ! grep -qx "\$4" "$tmp/down" 2> /dev/null ;;
  restart) echo "restart \$3" >> "$tmp/calls"; ! grep -qx "\$3" "$tmp/broken" 2> /dev/null || exit 1
    grep -qx "\$3" "$tmp/dies" 2> /dev/null || sed -i "/^\$3\$/d" "$tmp/down" 2> /dev/null; true ;;
esac
EOF
chmod +x "$tmp/bin/systemctl"
export PATH=$tmp/bin:$PATH
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
calls() { [[ -f $tmp/calls ]] && tr '\n' ';' < "$tmp/calls"; true; }
start() { rm -f "$tmp/calls"; rc=0; restart_changed worker.service "$tmp/src" "$tmp/conf" 2> /dev/null || rc=$?; }
echo a > "$tmp/src/main.go"; echo b > "$tmp/src/sub/x.go"; echo c > "$tmp/conf"

echo worker.service > "$tmp/down"; start
[[ $(calls) == "restart worker.service;" ]] && ok "a unit not running and never started by the kit starts" || fail "first: $(calls)"
rm "$tmp/down"
start; [[ -z $(calls) ]] && ok "unchanged and running, it is left running, with its runs" || fail "unchanged: $(calls)"
rm -rf "$STAGE0_HOME/started"; start
[[ $(calls) == "restart worker.service;" ]] && ok "a running unit with no record of what it runs restarts (ONS-123)" || fail "no record: $(calls)"
start; [[ -z $(calls) ]] && ok "and is then left running" || fail "after no record: $(calls)"
echo a2 > "$tmp/src/sub/x.go"; start
[[ $(calls) == "restart worker.service;" ]] && ok "a changed source file restarts it" || fail "source: $(calls)"
echo c2 > "$tmp/conf"; start
[[ $(calls) == "restart worker.service;" ]] && ok "a changed configuration restarts it" || fail "conf: $(calls)"
echo new > "$tmp/src/new.go"; start
[[ $(calls) == "restart worker.service;" ]] && ok "a new source file restarts it" || fail "new file: $(calls)"
rm "$tmp/conf"; start
[[ $(calls) == "restart worker.service;" ]] && ok "a missing input restarts it" || fail "missing: $(calls)"
start; [[ -z $(calls) ]] && ok "and is then the same again" || fail "missing again: $(calls)"
echo worker.service > "$tmp/down"; start
[[ $(calls) == "restart worker.service;" ]] && ok "a unit that is not running starts" || fail "down: $(calls)"
rm "$tmp/down"
echo c3 > "$tmp/conf"; echo worker.service > "$tmp/broken"; start
(( rc != 0 )) && [[ $(calls) == "restart worker.service;" ]] && ok "a failed restart fails" || fail "broken: exit $rc, $(calls)"
[[ $(units_unsettled) == worker.service ]] && ok "and its restart is unsettled" || fail "unsettled after broken: '$(units_unsettled)'"
rm "$tmp/broken"; start
[[ $(calls) == "restart worker.service;" ]] && ok "and is tried again on the next run, though it runs" || fail "after broken: $(calls)"
[[ -z $(units_unsettled) ]] && ok "and is settled once it restarted" || fail "unsettled after restart: '$(units_unsettled)'"
echo c4 > "$tmp/conf"; echo worker.service > "$tmp/down"; echo worker.service > "$tmp/dies"; start
(( rc != 0 )) && [[ $(units_unsettled) == worker.service ]] && ok "a unit that is not running after its restart fails, unsettled" \
  || fail "dies: exit $rc, unsettled '$(units_unsettled)'"
rm "$tmp/dies"; start
(( rc == 0 )) && [[ -z $(units_unsettled) ]] && ok "and settles once it runs after a restart" || fail "after dies: exit $rc, '$(units_unsettled)'"
echo edge.service > "$tmp/down"; rm -f "$tmp/calls"; restart_changed edge.service "$tmp/conf" 2> /dev/null
[[ $(calls) == "restart edge.service;" ]] && ok "each unit keeps its own record" || fail "other unit: $(calls)"
rm "$tmp/down"

# stage0.sh restarts Sortie at once and the units that carry runs only
# through restart_changed.
grep -qx 'systemctl --user restart ortus-stage0-sortie.service' "$KIT/stage0.sh" \
  && ok "stage0.sh restarts Sortie at once" || fail "stage0.sh does not restart Sortie"
if grep -E 'systemctl --user restart .*ortus-stage0-(nono|nono-live-proof|egress-gate|pomerium|worker)\.service' "$KIT/stage0.sh"; then
  fail "stage0.sh restarts a unit that carries runs unconditionally"
else
  ok "stage0.sh restarts both nonos, the egress gate, Pomerium and the worker daemon only when changed"
fi
exit $failed
