# shellcheck shell=bash
# Sourced by deploy.sh and test-logs.sh: the kit's units' durable log
# files, on every host the kit sets up (README.md "Logs"). Needs KIT,
# ORTUS_NOVA_HOME, names.sh's names and unit-restart.sh.
#
#   logs_install UNITS  render the follower's and the rotation's units into
#                       UNITS, enable them, start the rotation's timer, and
#                       (re)start the follower only when what it runs changed
#                       or it is not running: a second run changes nothing
#   logs_render UNITS   render those units into UNITS only: nothing is
#                       enabled or started (deploy.sh --no-start)
#
# ortus-nova-<env>-logs.service (logs-follow.sh) copies each ortus-nova-<env>-* unit's
# journal lines into $ORTUS_NOVA_LOG_DIR/<unit>.log (names.sh);
# ortus-nova-<env>-logs-rotate.timer (logs-rotate.sh) rotates them.

LOGS_UNITS=(logs.service logs-rotate.service logs-rotate.timer)

logs_render() {
  local units=$1 part new changed=''
  mkdir -p "$units"
  for part in "${LOGS_UNITS[@]}"; do
    new=$(render_unit "$KIT" "$part")
    if [[ ! -f $units/$(unit "$part") || $(cat "$units/$(unit "$part")") != "$new" ]]; then
      printf '%s\n' "$new" > "$units/$(unit "$part")"
      changed=1
    fi
  done
  [[ -z $changed ]] || systemctl --user daemon-reload
}

logs_install() {
  local units=$1
  logs_render "$units"
  systemctl --user is-enabled -q "$(unit logs.service)" || systemctl --user enable -q "$(unit logs.service)"
  systemctl --user is-enabled -q "$(unit logs-rotate.timer)" || systemctl --user enable -q "$(unit logs-rotate.timer)"
  systemctl --user is-active -q "$(unit logs-rotate.timer)" || systemctl --user start "$(unit logs-rotate.timer)"
  restart_changed "$(unit logs.service)" "$KIT/logs-follow.sh" "$units/$(unit logs.service)"
}
