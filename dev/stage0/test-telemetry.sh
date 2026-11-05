#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016 # ok and fail always succeed; the greps are literal
# Checks the telemetry store's place in the kit on p1, with stubs: the units
# that start and stop it (ortus-stage0-telemetry), back it up nightly into
# $STAGE0_HOME/telemetry-backups and prune it daily, each through
# run-telemetry.sh with the PATH stage0.sh recorded; stage0.sh installs
# them, restarts the store only when what it runs changed, takes the first
# backup at once, and leaves the store out, saying why, without
# podman-compose 1.5. control-plane/telemetry's own tests check the store.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
U=$KIT/units

grep -qx 'ExecStart=@KIT@/run-telemetry.sh up' "$U/ortus-stage0-telemetry.service" \
  && grep -qx 'ExecStop=@KIT@/run-telemetry.sh down' "$U/ortus-stage0-telemetry.service" \
  && grep -qx 'RemainAfterExit=yes' "$U/ortus-stage0-telemetry.service" \
  && ok "ortus-stage0-telemetry starts and stops the compose project" || fail "ortus-stage0-telemetry.service"
grep -qx 'ExecStart=@KIT@/run-telemetry.sh backup' "$U/ortus-stage0-telemetry-backup.service" \
  && grep -qx 'OnCalendar=\*-\*-\* 02:30:00' "$U/ortus-stage0-telemetry-backup.timer" && grep -qx 'Persistent=true' "$U/ortus-stage0-telemetry-backup.timer" \
  && ok "a nightly timer backs it up, and catches up on a night p1 was off" || fail "the backup timer"
grep -qx 'ExecStart=@KIT@/run-telemetry.sh retention' "$U/ortus-stage0-telemetry-retention.service" \
  && grep -qx 'OnCalendar=\*-\*-\* 04:00:00' "$U/ortus-stage0-telemetry-retention.timer" && grep -qx 'Persistent=true' "$U/ortus-stage0-telemetry-retention.timer" \
  && ok "a daily timer runs the retention" || fail "the retention timer"

for u in ortus-stage0-telemetry.service ortus-stage0-telemetry-backup.service ortus-stage0-telemetry-backup.timer \
  ortus-stage0-telemetry-retention.service ortus-stage0-telemetry-retention.timer; do
  grep -q "$u" "$KIT/stage0.sh" || fail "stage0.sh does not install $u"
done
grep -q 'restart_changed ortus-stage0-telemetry.service "$TELEMETRY/compose.yaml" "$TELEMETRY/otelcol.yaml"' "$KIT/stage0.sh" \
  && ! grep -qE 'systemctl --user (re)?start [^|]*ortus-stage0-telemetry\.service' "$KIT/stage0.sh" \
  && ok "stage0.sh restarts the store only when what it runs changed" || fail "stage0.sh restarts the store unconditionally"
grep -q 'systemctl --user enable -q --now ortus-stage0-telemetry-backup.timer ortus-stage0-telemetry-retention.timer' "$KIT/stage0.sh" \
  && grep -q 'systemctl --user start --no-block ortus-stage0-telemetry-backup.service' "$KIT/stage0.sh" \
  && ok "stage0.sh starts both timers, and the first backup at once" || fail "stage0.sh: the timers"

if [[ $(id -u) == 0 ]]; then
  echo "skip: telemetry.sh refuses root"
  exit $failed
fi
# run-telemetry.sh: the kit's directories, and the PATH stage0.sh recorded.
export STAGE0_HOME=$tmp/home
mkdir -p "$STAGE0_HOME" "$tmp/bin"
for c in podman curl python3 openssl; do ln -s "$(command -v "$c" || command -v true)" "$tmp/bin/$c"; done
ln -s "$(command -v sed)" "$tmp/bin/sed"
printf '#!/bin/sh\necho "podman-compose version %s"\n' 1.4.1 > "$tmp/bin/podman-compose"
chmod +x "$tmp/bin/podman-compose"
echo "$tmp/bin:/usr/bin:/bin" > "$STAGE0_HOME/path"
out=$(PATH=/usr/bin:/bin "$KIT/run-telemetry.sh" check-tools 2>&1) && fail "check-tools took podman-compose 1.4" \
  || { [[ $out == *"podman-compose 1.4.1 is too old"* ]] && ok "without podman-compose 1.5 it says why" || fail "check-tools: $out"; }
printf '#!/bin/sh\necho "podman-compose version 1.6.0"\n' > "$tmp/bin/podman-compose"
PATH=/usr/bin:/bin "$KIT/run-telemetry.sh" check-tools > "$tmp/out" 2>&1 && ok "with podman-compose 1.6 from the recorded PATH it runs" || fail "check-tools: $(cat "$tmp/out")"
PATH=/usr/bin:/bin "$KIT/run-telemetry.sh" init > /dev/null 2>&1
[[ -s $STAGE0_HOME/telemetry/telemetry.env && -s $STAGE0_HOME/telemetry/secrets/collector-ingest-token ]] \
  && ok "its settings and secrets are in \$STAGE0_HOME/telemetry" || fail "init: $(find "$STAGE0_HOME" | head)"
[[ $(grep -c 'TELEMETRY_BACKUP_DIR=$STAGE0_HOME/telemetry-backups' "$KIT/run-telemetry.sh") == 1 ]] \
  && ok "its backups go to \$STAGE0_HOME/telemetry-backups" || fail "the backup directory"
exit $failed
