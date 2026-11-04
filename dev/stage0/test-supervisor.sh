#!/usr/bin/env bash
# Checks the kit's part of the supervisor (supervisor.sh): stage0.sh mints
# the supervisor token and installs the prompt beside Sortie's workflow,
# run-sortie.sh hands the token and the supervisor's opencode settings to
# Sortie, and the operator reads the supervisor's actions and pauses and
# resumes it, a pause that outlives a restart of Sortie. Sortie is stubbed
# behind curl. Sortie's own side, with the kit's workflow and prompt, is
# sortie-test/stage0_supervisor_test.go (test-review-gate.sh).
# shellcheck disable=SC2015,SC2016 # each check is "test && ok || fail"; the patterns' $ are literal
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
command -v python3 > /dev/null || { echo "FAIL: python3 is not on PATH"; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

# shellcheck source=supervisor.sh
. "$KIT/supervisor.sh"

# The install: a token of its own, minted once, and the kit's prompt.
home=$tmp/home
mkdir -p "$home"
echo control > "$home/sortie-token"
supervisor_install "$home" "$KIT" || fail "supervisor_install failed"
token=$(cat "$home/supervisor-token")
[[ $token =~ ^[0-9a-f]{48}$ && $(stat -c %a "$home/supervisor-token") == 600 ]] \
  && ok "the supervisor token is minted, mode 0600" || fail "supervisor-token: '$token', mode $(stat -c %a "$home/supervisor-token")"
cmp -s "$KIT/supervisor.md" "$home/sortie/supervisor.md" && ok "the prompt is installed beside Sortie's workflow" \
  || fail "$home/sortie/supervisor.md is not the kit's prompt"
echo "an older prompt" > "$home/sortie/supervisor.md"
supervisor_install "$home" "$KIT" || fail "a second supervisor_install failed"
[[ $(cat "$home/supervisor-token") == "$token" ]] && ok "a second install keeps the token" || fail "a second install minted another token"
cmp -s "$KIT/supervisor.md" "$home/sortie/supervisor.md" && ok "a second install replaces the prompt with the kit's" \
  || fail "a second install left the older prompt"
[[ ! -e $home/sortie/supervisor.md.tmp ]] && ok "the install leaves no partial prompt" || fail "supervisor.md.tmp left behind"
cp "$home/sortie-token" "$home/supervisor-token"
if supervisor_install "$home" "$KIT" 2> "$tmp/err"; then fail "the install took the control token as the supervisor's"
else grep -q "is the control token" "$tmp/err" && ok "the install refuses the control token as the supervisor's" || fail "install: $(cat "$tmp/err")"; fi
mkdir -p "$tmp/nokit"
if supervisor_install "$home" "$tmp/nokit" 2> /dev/null; then fail "the install went on with no prompt"; else ok "the install fails with no prompt"; fi
echo "$token" > "$home/supervisor-token"

# stage0.sh installs it before Sortie validates the workflow; the unit
# pauses each restarted Sortie again.
install_at=$(grep -n 'supervisor_install "$STAGE0_HOME" "$KIT"' "$KIT/stage0.sh" | cut -d: -f1)
validate_at=$(grep -n 'run-sortie.sh" validate' "$KIT/stage0.sh" | cut -d: -f1)
[[ -n $install_at && -n $validate_at ]] && (( install_at < validate_at )) && ok "stage0.sh installs the supervisor before Sortie validates the workflow" \
  || fail "stage0.sh: install at '$install_at', validate at '$validate_at'"
grep -qx 'ExecStartPost=-@KIT@/supervisor.sh repause' "$KIT/units/ortus-stage0-sortie.service" \
  && ok "the Sortie unit pauses a restarted Sortie again" || fail "ortus-stage0-sortie.service has no repause"

# run-sortie.sh hands Sortie the token, the opencode settings and the PATH
# stage0.sh recorded; a stub Sortie prints them.
mkdir -p "$home/bin" "$tmp/kit/dev/stage0" "$tmp/tools"
cp "$KIT/run-sortie.sh" "$KIT/notify-channel.sh" "$tmp/kit/dev/stage0/"
# The host's token for the workbench's notifications, as stage0.sh mints it.
mkdir -p "$home/notify-tokens"
(umask 077; echo p1-notify-token > "$home/notify-tokens/p1.token")
printf '#!/bin/sh\necho token\n' > "$tmp/kit/dev/huly_project.py"
printf '#!/bin/sh\necho claude\n' > "$tmp/kit/dev/stage0/anthropic-token.py"
printf '#!/bin/sh\nenv | grep -E "^(STAGE0_SUPERVISOR_TOKEN|OPENCODE_CONFIG|PATH)=" | sort\n' > "$home/bin/sortie"
chmod +x "$tmp/kit/dev/huly_project.py" "$tmp/kit/dev/stage0/anthropic-token.py" "$home/bin/sortie"
sed -i 's|python3 "$STAGE0_KIT_ROOT/dev/huly_project.py"|"$STAGE0_KIT_ROOT/dev/huly_project.py"|' "$tmp/kit/dev/stage0/run-sortie.sh"
echo github > "$home/github-token"
echo "$tmp/tools:/usr/bin" > "$home/path"
cat > "$home/stage0.env" <<EOF
STAGE0_KIT_ROOT=$tmp/kit
STAGE0_HULY_URL=x STAGE0_HULY_WORKSPACE=x STAGE0_HULY_EMAIL=x STAGE0_HULY_PASSWORD=x STAGE0_HULY_PROJECT=ONS
STAGE0_SORTIE_PORT=7680
STAGE0_SORTIE_NAME=p1
EOF
out=$(STAGE0_HOME=$home HOME=$tmp "$tmp/kit/dev/stage0/run-sortie.sh" 2>&1 || true)
grep -qx "STAGE0_SUPERVISOR_TOKEN=$token" <<< "$out" && ok "Sortie gets the supervisor token from its file" || fail "run-sortie.sh: $out"
grep -qx "OPENCODE_CONFIG=$tmp/kit/dev/stage0/supervisor-opencode.json" <<< "$out" && ok "the supervisor's opencode reads the kit's settings" \
  || fail "run-sortie.sh OPENCODE_CONFIG: $out"
grep -q "^PATH=$home/bin:.*:$tmp/tools:/usr/bin$" <<< "$out" && ok "Sortie finds the host's opencode and gh on the recorded PATH, after the kit's tools" \
  || fail "run-sortie.sh PATH: $out"
python3 -c 'import json, sys; c = json.load(open(sys.argv[1])); assert "@ex-machina/opencode-anthropic-auth@2.0.0-next.6" in c["plugins"]
assert c["model"] == json.load(open(sys.argv[2]))["model"]' "$KIT/supervisor-opencode.json" "$KIT/pack/opencode.json" \
  && ok "the supervisor's opencode signs in with the kit's Anthropic plugin and runs the stories' model" || fail "supervisor-opencode.json"

# Pause, resume, a pause that outlives a restart, and the status, against a
# Sortie stubbed as curl: it logs each request (method, path, the token it
# carried, the body) and serves state.json and readyz while $tmp/s/up exists.
mkdir -p "$tmp/s"
cat > "$tmp/bin-curl" <<'EOF'
#!/usr/bin/env bash
s=$SORTIE_STUB method=GET url='' auth='' data='' out=''
while (( $# )); do
  case $1 in
    -X) method=$2; shift ;;
    -H) if [[ $2 == @- ]]; then auth=$(cat); fi; shift ;;
    --data) data=$2; shift ;;
    -o) out=$2; shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
path=/${url#http://*/}
[[ -e $s/up ]] || exit 7
echo "$method $path $auth $data" >> "$s/requests"
case "$method $path" in
  "GET /readyz") ;;
  "GET /api/v1/state") cat "$s/state.json" ;;
  "POST /api/v1/supervisor/pause"|"POST /api/v1/supervisor/resume")
    [[ $auth == "Authorization: Bearer control" ]] || exit 22; echo '{}' ;;
  *) exit 22 ;;
esac
EOF
mkdir -p "$tmp/curlbin"; mv "$tmp/bin-curl" "$tmp/curlbin/curl"; chmod +x "$tmp/curlbin/curl"
export SORTIE_STUB=$tmp/s STAGE0_HOME=$home STAGE0_SORTIE_PORT=7680
PATH=$tmp/curlbin:$PATH
requests() { cat "$tmp/s/requests" 2> /dev/null; rm -f "$tmp/s/requests"; }
touch "$tmp/s/up"

supervisor_pause 'rewriting "the" prompt' || fail "supervisor_pause failed"
got=$(requests)
[[ $got == 'POST /api/v1/supervisor/pause Authorization: Bearer control {"by": "operator", "reason": "rewriting \"the\" prompt"}' ]] \
  && ok "pause asks Sortie, with the control token, the operator and the reason" || fail "pause sent: $got"
[[ $(cat "$home/supervisor.paused") == 'rewriting "the" prompt' ]] && ok "pause records itself" || fail "supervisor.paused: $(cat "$home/supervisor.paused" 2>&1)"

# A restarted Sortie: the unit's repause waits until it answers, then pauses it again.
rm "$tmp/s/up"
( sleep 2; touch "$tmp/s/up" ) &
supervisor_repause 10 || fail "repause failed"
wait
got=$(requests)
[[ $got == *"GET /readyz"*'POST /api/v1/supervisor/pause Authorization: Bearer control {"by": "operator", "reason": "rewriting \"the\" prompt"}'* ]] \
  && ok "a restarted Sortie is paused again, with the recorded reason" || fail "repause sent: $got"
rm "$tmp/s/up"
if supervisor_repause 2 2> /dev/null; then fail "repause went on with no Sortie"; else ok "repause fails when Sortie does not come up"; fi
touch "$tmp/s/up"

supervisor_resume || fail "supervisor_resume failed"
got=$(requests)
[[ $got == "POST /api/v1/supervisor/resume Authorization: Bearer control {}" && ! -e $home/supervisor.paused ]] \
  && ok "resume asks Sortie and forgets the pause" || fail "resume sent: $got; paused file: $(ls "$home")"
supervisor_repause 1 || fail "repause with no pause failed"
[[ -z $(requests) ]] && ok "with no pause recorded, a restart pauses nothing" || fail "repause with no pause asked Sortie"
echo wrong > "$home/sortie-token"
if supervisor_pause 2> /dev/null; then fail "pause went on without the control token"; else ok "pause fails when Sortie refuses the token"; fi
[[ ! -e $home/supervisor.paused ]] && ok "a refused pause records nothing" || fail "a refused pause was recorded"
echo control > "$home/sortie-token"

# The status: what the operator reads of the supervisor's actions.
cat > "$tmp/s/state.json" <<'EOF'
{"supervisor": {"status": "paused", "agent": "opencode",
  "paused": {"since": "2026-11-03T12:00:00Z", "by": "operator", "reason": "rewriting the prompt"},
  "budget": {"sessions_used": 3, "sessions_limit": 96, "tokens_used": 120000, "tokens_limit": 3000000},
  "last_session": {"started_at": "2026-11-03T11:30:00Z", "ended_at": "2026-11-03T11:34:00Z", "outcome": "completed", "tokens": 40000,
    "triggers": [{"kind": "sweep", "issue_identifier": null, "detail": "the periodic sweep", "at": "2026-11-03T11:30:00Z"}]}},
 "supervisor_actions": [
  {"id": 2, "action": "merge", "route": "POST /api/v1/ONS-7/merge", "issue_identifier": "ONS-7", "evidence": "gh pr view 12: review passed, CI success at abc123",
   "result": "done", "status": 200, "detail": null, "pull_request": "ortusnova/sortie#12", "merge_commit": "def456", "at": "2026-11-03T11:33:00Z"},
  {"id": 1, "action": "merge", "route": "POST /api/v1/ONS-8/merge", "issue_identifier": "ONS-8", "evidence": "gh pr view 13: CI success",
   "result": "refused", "status": 409, "detail": "ui_story", "pull_request": null, "merge_commit": null, "at": "2026-11-03T11:32:00Z"}],
 "platform_faults": [{"id": "pf-3", "class": "supervisor_question", "person_action": {"action": "Give the GitHub token the workflow scope", "where": "GitHub settings"},
   "stories": [{"issue_identifier": "ONS-49"}, {"issue_identifier": "ONS-50"}]}]}
EOF
out=$(supervisor_status)
for want in "supervisor: paused (paused since 2026-11-03T12:00:00Z by operator: rewriting the prompt)" \
  "budget today: 3 of 96 sessions, 120000 of 3000000 tokens" \
  "last session: completed at 2026-11-03T11:34:00Z, 40000 tokens" \
  "question pf-3: Give the GitHub token the workflow scope, at GitHub settings, holds ONS-49, ONS-50" \
  "actions (2, newest first):" \
  "  2026-11-03T11:33:00Z merge ONS-7: 200 done ortusnova/sortie#12 def456" \
  "    evidence: gh pr view 12: review passed, CI success at abc123" \
  "  2026-11-03T11:32:00Z merge ONS-8: 409 refused" "    detail: ui_story"; do
  grep -qF -- "$want" <<< "$out" && ok "status shows: ${want# }" || fail "status lacks '$want': $out"
done
echo '{"running": []}' > "$tmp/s/state.json"
[[ $(supervisor_status) == "supervisor: not configured" ]] && ok "status says when Sortie runs no supervisor" || fail "status with no supervisor: $(supervisor_status)"

exit $failed
