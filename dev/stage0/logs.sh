# shellcheck shell=bash
# Sourced by stage0.sh and test-logs.sh: the stage-0 units' durable log
# files, on every host the kit sets up (README.md "Logs"). Needs KIT,
# STAGE0_HOME and unit-restart.sh.
#
#   logs_install UNITS  render the follower's and the rotation's units into
#                       UNITS, enable them, start the rotation's timer, and
#                       (re)start the follower only when what it runs changed
#                       or it is not running: a second run changes nothing
#
# ortus-stage0-logs.service (logs-follow.sh) copies each ortus-stage0-* unit's
# journal lines into ~/.local/state/ortus-nova/logs/<unit>.log;
# ortus-stage0-logs-rotate.timer (logs-rotate.sh) rotates them.

LOGS_UNITS=(ortus-stage0-logs.service ortus-stage0-logs-rotate.service ortus-stage0-logs-rotate.timer)

logs_install() {
  local units=$1 unit new changed=''
  mkdir -p "$units"
  for unit in "${LOGS_UNITS[@]}"; do
    new=$(sed -e "s|@KIT@|$KIT|g" -e "s|@STAGE0_HOME@|$STAGE0_HOME|g" "$KIT/units/$unit")
    if [[ ! -f $units/$unit || $(cat "$units/$unit") != "$new" ]]; then
      printf '%s\n' "$new" > "$units/$unit"
      changed=1
    fi
  done
  [[ -z $changed ]] || systemctl --user daemon-reload
  systemctl --user is-enabled -q ortus-stage0-logs.service || systemctl --user enable -q ortus-stage0-logs.service
  systemctl --user is-enabled -q ortus-stage0-logs-rotate.timer || systemctl --user enable -q ortus-stage0-logs-rotate.timer
  systemctl --user is-active -q ortus-stage0-logs-rotate.timer || systemctl --user start ortus-stage0-logs-rotate.timer
  restart_changed ortus-stage0-logs.service "$KIT/logs-follow.sh" "$units/ortus-stage0-logs.service"
}
