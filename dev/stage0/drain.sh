# shellcheck shell=bash
# Sourced by deploy.sh and dev-host-apply.sh, and by test-deploy.sh: drain
# this host's Sortie before a change that must not land under a running
# agent (a kit or Sortie deploy, a smolvm bump), so that the change waits
# only for the runs already going, never behind the queue of stories:
#
#   POST /api/v1/dispatch/pause   Sortie starts no new run; running ones go on
#   POST /api/v1/dispatch/resume  Sortie dispatches again
#   GET  /api/v1/state            "running" lists the runs still going
#
# Both POSTs take Sortie's control token. $STAGE0_HOME/dispatch.paused
# records that the kit paused dispatch, and why, until it resumes it.
# Needs STAGE0_HOME and STAGE0_SORTIE_PORT.

# sortie_api METHOD PATH: print Sortie's answer; fail on no answer or an
# error status. The token goes to curl on its standard input, not its
# command line.
sortie_api() {
  local url="http://127.0.0.1:$STAGE0_SORTIE_PORT$2"
  if [[ $1 == GET ]]; then
    curl -sf "$url"
  else
    printf 'Authorization: Bearer %s\n' "$(cat "$STAGE0_HOME/sortie-token" 2> /dev/null)" \
      | curl -sf -X "$1" -H @- "$url"
  fi
}

# sortie_running: print how many runs Sortie has going; fail when it does
# not answer.
sortie_running() {
  sortie_api GET /api/v1/state \
    | python3 -c 'import json, sys; print(len(json.load(sys.stdin).get("running", [])))'
}

# dispatch_pause REASON: ask Sortie to start no new run, and record REASON.
# Fails when Sortie does not answer or has no pause (an older Sortie).
dispatch_pause() {
  sortie_api POST /api/v1/dispatch/pause > /dev/null || return 1
  printf '%s\n' "$1" > "$STAGE0_HOME/dispatch.paused"
}

# dispatch_resume: ask Sortie to dispatch again, and forget the pause once
# it has. A pause the kit did not record is left alone.
dispatch_resume() {
  [[ -e $STAGE0_HOME/dispatch.paused ]] || return 0
  sortie_api POST /api/v1/dispatch/resume > /dev/null || return 1
  rm -f "$STAGE0_HOME/dispatch.paused"
}
