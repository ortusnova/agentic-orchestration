#!/usr/bin/env bash
# Checks refresh-token.sh with systemctl, curl and anthropic-token.py
# stubbed: when it restarts both nonos onto a fresher Claude token, and when
# it waits; and, when api.anthropic.com refuses the token a nono holds, when
# it restarts them onto another token, when it records a credentials fault
# in Sortie once, and when it does nothing.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
mkdir -p "$tmp/bin" "$tmp/kit" "$STAGE0_HOME"
cp "$KIT/refresh-token.sh" "$tmp/kit/"
printf 'STAGE0_SORTIE_PORT=1\nSTAGE0_NONO_PORT=7690\nSTAGE0_NONO_LIVE_PROOF_PORT=7691\n' > "$STAGE0_HOME/stage0.env"
echo pass > "$STAGE0_HOME/nono-pass"
echo control > "$STAGE0_HOME/sortie-token"
export CALLS=$tmp/calls
cat > "$tmp/bin/systemctl" << 'EOF'
#!/bin/sh
if [ "$1 $2" = "--user is-active" ]; then
  [ "$4" = ortus-stage0-nono-live-proof.service ] && exit "${LIVE_DOWN:-0}"
  exit 0
fi
echo "systemctl $*" >> "$CALLS"
EOF
# curl: Sortie's state and routes, and api.anthropic.com through the nono
# on a port (KIT_NONO, LIVE_NONO) or straight (TOKEN_PROBE): an HTTP status,
# or 000 for no answer.
cat > "$tmp/bin/curl" << 'EOF'
#!/usr/bin/env bash
echo "$*" >> "$CALLS.argv"
out=/dev/stdout url= port= data= stdin=
while (( $# )); do
  case $1 in
    -o) out=$2; shift ;;
    --proxy) port=${2##*:}; shift ;;
    --data) data=$2; shift ;;
    -K|-H) [[ $2 == - || $2 == @- ]] && stdin=$(cat); shift ;;
    http*) url=$1 ;;
  esac
  shift
done
case $url in
  */api/v1/state) echo "{\"running\": [$BUSY], \"retrying\": [$RETRYING], \"parked\": []}" ;;
  */api/v1/supervisor/questions) echo "ask $data" >> "$CALLS"; echo '{"action": "ask_person", "platform_fault": {"id": "pf-9"}}' ;;
  */action-done) echo "done ${url#*/platform-faults/} $data" >> "$CALLS"; echo '{"resolved": true}' > "$out" ;;
  https://api.anthropic.com/*)
    case $port in
      7690) code=$KIT_NONO ;;
      7691) code=$LIVE_NONO ;;
      *) code=$TOKEN_PROBE; [[ $stdin == *"Bearer $TOKEN"* ]] || code=401 ;;
    esac
    case $code in
      000) printf 000; exit 7 ;;
      200) echo '{"data": []}' > "$out" ;;
      403) echo '{"type": "error", "error": {"type": "permission_error", "message": "not allowed"}}' > "$out" ;;
      *) echo '{"type": "error", "error": {"type": "authentication_error", "message": "OAuth token has been revoked."}}' > "$out" ;;
    esac
    printf '%s' "$code" ;;
esac
EOF
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\nif [ "$1" = --expiry ]; then echo "$FRESH"; exit 0; fi\n[ -n "$TOKEN" ] || exit 1\necho "$TOKEN"\n' > "$tmp/kit/anthropic-token.py"
chmod +x "$tmp/bin/"* "$tmp/kit/"*
export PATH=$tmp/bin:$PATH
failed=0
now=$(( $(date +%s) * 1000 ))
min=$(( 60 * 1000 ))
restart='systemctl --user restart ortus-stage0-nono.service ortus-stage0-nono-live-proof.service'

# run [VAR=VALUE...]: one pass of refresh-token.sh, with nono's token
# expiring at $expiry; by default both nonos hold an accepted token, a
# fresher one exists, and no agent runs.
expiry=$(( now + 300 * min ))
run() {
  rm -f "$CALLS"
  echo "$expiry" > "$STAGE0_HOME/nono-token-expiry"
  env FRESH=$(( now + 480 * min )) BUSY= RETRYING= KIT_NONO=200 LIVE_NONO=200 \
    TOKEN=fresh-token TOKEN_PROBE=200 LIVE_DOWN=0 "$@" "$tmp/kit/refresh-token.sh" 2> "$tmp/log" \
    || { echo "FAIL: refresh-token.sh $* exited $?"; cat "$tmp/log"; failed=1; }
}
calls() { cat "$CALLS" 2> /dev/null || true; }
expect() { # expect NAME CONDITION...
  local name=$1; shift
  if "$@"; then echo "ok: $name"; else echo "FAIL: $name"; calls; cat "$tmp/log"; failed=1; fi
}
restarted() { calls | grep -qx "$restart"; }
# shellcheck disable=SC2317,SC2329 # called through expect
quiet() { [[ -z $(calls) ]]; }
# shellcheck disable=SC2317,SC2329
asked() { [[ $(calls | grep -c '^ask ') == "$1" ]]; }

check() { # check NAME RUNNING_EXPIRY FRESH_EXPIRY BUSY WANT_RESTART
  expiry=$2
  run FRESH="$3" BUSY="$4"
  expiry=$(( now + 300 * min ))
  local got=no
  restarted && got=yes
  if [[ $got == "$5" ]]; then echo "ok: $1"; else echo "FAIL: $1: restart $got, want $5"; failed=1; fi
}

check "a token far from expiry is kept" $(( now + 300 * min )) $(( now + 480 * min )) "" no
check "a token near expiry is replaced when idle" $(( now + 30 * min )) $(( now + 480 * min )) "" yes
check "a token near expiry waits while an agent runs" $(( now + 30 * min )) $(( now + 480 * min )) 1 no
check "an expired token is replaced although agents run" $(( now - 60 * min )) $(( now + 480 * min )) 1,2 yes
check "no fresher token: nothing to do" $(( now - 60 * min )) $(( now - 120 * min )) 1 no

run
expect "an accepted token far from expiry: nothing to do" quiet

waiting='{"issue_identifier": "ONS-7"}'
run KIT_NONO=401 BUSY=1,2 RETRYING="$waiting"
expect "a revoked token with a fresh one: both nonos restart at once, although agents run" restarted
expect "a revoked token with a fresh one: no fault" asked 0
expect "the probe's refusal is in the journal" grep -q 'refuses the Claude token the kit.s nono holds: .*401 .*revoked' "$tmp/log"

run LIVE_NONO=401 BUSY=1
expect "the live-proof nono's revoked token: both nonos restart" restarted

run LIVE_NONO=401 LIVE_DOWN=3
expect "a stopped live-proof nono is not probed" quiet

run KIT_NONO=401 TOKEN= BUSY=1 RETRYING="$waiting"
expect "a revoked token with none: no restart" eval '! restarted'
expect "a revoked token with none: a credentials fault for a person" asked 1
expect "the fault holds the waiting stories and says what to do" \
  grep -q '"stories": \["ONS-7"\].*"checked": "the kit.s nono: 401 .*anthropic-token.py: no live token.*"cause": "Credentials: ' "$CALLS"
expect "the fault's action is to log in again on the host" grep -q '"action": "Log in to Claude again on ' "$CALLS"
run KIT_NONO=401 TOKEN= BUSY=1 RETRYING="$waiting"
expect "the fault is recorded once" asked 0
run KIT_NONO=401 BUSY=1 RETRYING="$waiting"
expect "once the operator logs in again, both nonos restart onto the new token" restarted
expect "and the fault's action is marked done" grep -qx 'done pf-9/action-done {"by": "ortus-stage0-token"}' "$CALLS"
expect "and forgotten" test ! -e "$STAGE0_HOME/nono-token-fault"

run KIT_NONO=401 TOKEN_PROBE=401 RETRYING="$waiting"
expect "a revoked token whose replacement is refused too: no restart" eval '! restarted'
expect "a revoked token whose replacement is refused too: a fault" asked 1
run
expect "a nono with an accepted token again clears the fault" grep -q '^done pf-9' "$CALLS"

run KIT_NONO=401 TOKEN=
expect "no waiting story to hold: no fault recorded yet" asked 0
run KIT_NONO=401 TOKEN= RETRYING="$waiting"
expect "the next pass records it once a story waits" asked 1
rm -f "$STAGE0_HOME/nono-token-fault"

run KIT_NONO=000 LIVE_NONO=000 TOKEN= RETRYING="$waiting"
expect "a probe that cannot reach the API: no restart, no fault" quiet
run KIT_NONO=401 TOKEN_PROBE=000 RETRYING="$waiting"
expect "a fresh token that cannot be probed: no restart, no fault" quiet
run KIT_NONO=403 TOKEN= RETRYING="$waiting"
expect "a refusal that says neither revoked nor invalid: no restart, no fault" quiet
run KIT_NONO=503 TOKEN= RETRYING="$waiting"
expect "an API error: no restart, no fault" quiet

timer=$KIT/units/ortus-stage0-token.timer
expect "the timer probes every 10 minutes, with no systemd slack" \
  eval "grep -qx 'OnUnitActiveSec=10min' '$timer' && grep -qx 'AccuracySec=1s' '$timer'"

if grep -q 'fresh-token' "$CALLS.argv"; then echo "FAIL: a Claude token is on curl's command line"; failed=1; fi

exit $failed
