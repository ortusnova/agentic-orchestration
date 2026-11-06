#!/usr/bin/env bash
# Run the telemetry store's script (control-plane/telemetry/telemetry.sh) for
# the stage-0 kit on p1: its settings, secrets and file store in
# $STAGE0_HOME/telemetry, its backups in $STAGE0_HOME/telemetry-backups.
# The units ortus-stage0-telemetry (up, down), -telemetry-backup (nightly)
# and -telemetry-retention (daily) run it with the PATH stage0.sh last ran
# with, which has podman-compose.
#
#   run-telemetry.sh up|down|health|backup|restore [DIR]|retention
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
export TELEMETRY_HOME=$STAGE0_HOME/telemetry
export TELEMETRY_BACKUP_DIR=$STAGE0_HOME/telemetry-backups
if [[ -s $STAGE0_HOME/path ]]; then
  PATH=$(cat "$STAGE0_HOME/path")
  export PATH
fi
exec "$KIT/../../control-plane/telemetry/telemetry.sh" "$@"
