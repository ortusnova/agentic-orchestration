#!/usr/bin/env bash
# The supervisor (spec store agent-platform/run-supervision, design D9): the
# one agent session Sortie runs for what its rules cannot settle, set by the
# workflow's supervisor section. On each Sortie host, stage0.sh installs:
#
#   $STAGE0_HOME/supervisor-token     the supervisor token, minted once, mode
#                                     0600: Sortie accepts it only on the
#                                     supervisor's own routes, each one
#                                     recorded with its evidence
#                                     (run-sortie.sh reads it into
#                                     server.supervisor_token)
#   $STAGE0_HOME/sortie/supervisor.md the kit's prompt (supervisor.md), beside
#                                     the rendered workflow; Sortie reads it at
#                                     each session's start
#
# A worker host runs no Sortie, so it gets neither. Sourced by stage0.sh;
# the operator runs it too:
#
#   dev/stage0/supervisor.sh status          what the supervisor does: its
#                                            status, budget, last session and
#                                            its recent actions with their
#                                            evidence and result
#   dev/stage0/supervisor.sh pause [REASON]  stop its running session and
#                                            start none; triggers wait
#   dev/stage0/supervisor.sh resume          start it again, at once, for
#                                            the triggers that waited
#
# Sortie keeps a pause in memory only, so a restart (each deploy) would
# clear it: $STAGE0_HOME/supervisor.paused records the operator's pause, and
# ortus-stage0-sortie.service pauses each restarted Sortie again
# (`supervisor.sh repause`) until the operator resumes it.

# supervisor_install HOME KIT: mint HOME/supervisor-token unless it holds
# one, and install KIT/supervisor.md as HOME/sortie/supervisor.md, replacing
# the one there in one step, so a session never reads half a prompt.
supervisor_install() {
  local home=$1 kit=$2
  [[ -s $kit/supervisor.md ]] || { echo "$kit/supervisor.md is missing" >&2; return 1; }
  if [[ ! -s $home/supervisor-token ]]; then
    (umask 077; openssl rand -hex 24 > "$home/supervisor-token") || return 1
  fi
  if [[ -s $home/sortie-token ]] && cmp -s "$home/supervisor-token" "$home/sortie-token"; then
    echo "$home/supervisor-token is the control token; delete it and run stage0.sh again" >&2
    return 1
  fi
  mkdir -p "$home/sortie"
  cp "$kit/supervisor.md" "$home/sortie/supervisor.md.tmp"
  mv -f "$home/sortie/supervisor.md.tmp" "$home/sortie/supervisor.md"
}

# supervisor_api PATH [BODY]: POST BODY to Sortie's PATH with the control
# token, which goes to curl on its standard input, not its command line.
# Needs STAGE0_HOME and STAGE0_SORTIE_PORT.
supervisor_api() {
  local body='{}'
  [[ -n ${2:-} ]] && body=$2
  printf 'Authorization: Bearer %s\n' "$(cat "$STAGE0_HOME/sortie-token" 2> /dev/null)" \
    | curl -sf -X POST -H @- -H 'Content-Type: application/json' --data "$body" \
      "http://127.0.0.1:$STAGE0_SORTIE_PORT$1"
}

# supervisor_pause [REASON]: pause the supervisor, and record it so that a
# restarted Sortie is paused again.
supervisor_pause() {
  local reason=${1:-paused by the operator} body
  body=$(python3 -c 'import json, sys; print(json.dumps({"by": "operator", "reason": sys.argv[1]}))' "$reason")
  supervisor_api /api/v1/supervisor/pause "$body" > /dev/null \
    || { echo "Sortie did not pause the supervisor (is it running, with a supervisor section?)" >&2; return 1; }
  printf '%s\n' "$reason" > "$STAGE0_HOME/supervisor.paused"
}

# supervisor_resume: resume the supervisor and forget the recorded pause.
supervisor_resume() {
  supervisor_api /api/v1/supervisor/resume > /dev/null \
    || { echo "Sortie did not resume the supervisor" >&2; return 1; }
  rm -f "$STAGE0_HOME/supervisor.paused"
}

# supervisor_repause [TRIES]: when the operator paused the supervisor,
# wait for the restarted Sortie to answer (once a second, TRIES times,
# default 60) and pause it again; nothing otherwise.
supervisor_repause() {
  [[ -s $STAGE0_HOME/supervisor.paused ]] || return 0
  local tries=${1:-60}
  until curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_SORTIE_PORT/readyz"; do
    (( --tries > 0 )) || { echo "Sortie did not answer; the supervisor is not paused again" >&2; return 1; }
    sleep 1
  done
  supervisor_pause "$(cat "$STAGE0_HOME/supervisor.paused")"
}

# supervisor_status: print the supervisor's state from Sortie's state API:
# its status, its budget, its last session, any question it raised that is
# still open, and its recent actions, newest first, each with its result
# and evidence.
supervisor_status() {
  curl -sf "http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/state" | python3 -c '
import json, sys
state = json.load(sys.stdin)
sup = state.get("supervisor")
if not sup:
    print("supervisor: not configured")
    sys.exit(0)
line = "supervisor: " + sup.get("status", "?")
if sup.get("paused"):
    p = sup["paused"]
    line += " (paused since %s by %s: %s)" % (p.get("since"), p.get("by"), p.get("reason") or "no reason")
if sup.get("unavailable"):
    line += " (" + sup["unavailable"] + ")"
print(line)
b = sup.get("budget") or {}
print("budget today: %s of %s sessions, %s of %s tokens" % (
    b.get("sessions_used"), b.get("sessions_limit"), b.get("tokens_used"), b.get("tokens_limit")))
def trigger(t):
    return "%s%s: %s" % (t.get("kind"), " " + t["issue_identifier"] if t.get("issue_identifier") else "", t.get("detail"))
if sup.get("running"):
    print("running since %s, for %s" % (sup["running"].get("started_at"), "; ".join(trigger(t) for t in sup["running"].get("triggers") or [])))
last = sup.get("last_session")
if last:
    print("last session: %s at %s, %s tokens%s" % (last.get("outcome"), last.get("ended_at"), last.get("tokens"),
        ", " + last["error"] if last.get("error") else ""))
for f in state.get("platform_faults") or []:
    if f.get("class") == "supervisor_question":
        a = f.get("person_action") or {}
        held = ", ".join(s.get("issue_identifier", "?") for s in f.get("stories") or [])
        print("question %s: %s, at %s, holds %s (POST /api/v1/platform-faults/%s/action-done once done)" % (
            f.get("id"), a.get("action"), a.get("where"), held or "no story", f.get("id")))
actions = state.get("supervisor_actions") or []
print("actions (%d, newest first):" % len(actions))
for a in actions:
    print("  %s %s %s: %s %s%s%s" % (a.get("at"), a.get("action"), a.get("issue_identifier") or "-", a.get("status"), a.get("result"),
        " " + a["pull_request"] if a.get("pull_request") else "", " " + a["merge_commit"] if a.get("merge_commit") else ""))
    if a.get("detail"):
        print("    detail: " + a["detail"])
    print("    evidence: " + (a.get("evidence") or ""))
'
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  set -euo pipefail
  if [[ ${1:-} == install ]]; then
    shift; supervisor_install "$@"; exit
  fi
  STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
  # shellcheck source=/dev/null
  . "$STAGE0_HOME/stage0.env"
  case ${1:-} in
    status) supervisor_status ;;
    pause) shift; supervisor_pause "$*" ;;
    resume) supervisor_resume ;;
    repause) supervisor_repause 60 ;;
    *) echo "usage: $0 status | pause [REASON] | resume | repause | install STAGE0_HOME KIT" >&2; exit 2 ;;
  esac
fi
