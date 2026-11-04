#!/usr/bin/env bash
# Sortie reads the Claude token at each launch (the workflow's command
# source), so it never needs a restart for it. nono holds the same token for
# the egress flow and reads it only at start. So every 10 minutes
# (ortus-stage0-token.timer):
#
# Revocation (design D10). It probes the token each nono holds (the kit's
# and the live-proof one) with one GET /v1/models through that nono, which
# puts its token in. When api.anthropic.com refuses it as revoked or
# invalid, it probes the token anthropic-token.py yields, straight; when
# that one is accepted (so it is a different token), it restarts both nonos
# onto it at once, whatever runs. When none is accepted, it records a
# credentials platform fault through Sortie's person-action path
# (POST /api/v1/supervisor/questions, with the control token), once: the
# fault's id stays in $STAGE0_HOME/nono-token-fault until a nono runs with
# an accepted token again, and then it marks the fault's action done. A
# probe that cannot reach the API, or that gets any other answer, changes
# nothing.
#
# Expiry. An OAuth login's token lasts hours: when a fresher token exists
# and nono's expires within 90 minutes, restart both nonos alone, but only
# while no agent runs, so no session's egress is cut off. Once nono's token
# has expired, no egress can work with it, so it restarts at once. A
# `claude setup-token` token never needs this.
set -euo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
systemctl --user is-active -q ortus-stage0-nono.service || exit 0

NONOS=(ortus-stage0-nono.service ortus-stage0-nono-live-proof.service)
FAULT=$STAGE0_HOME/nono-token-fault
SORTIE=http://127.0.0.1:$STAGE0_SORTIE_PORT
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# probe NAME [PORT]: GET api.anthropic.com/v1/models with a Claude token,
# through the nono on PORT (which replaces the placeholder with the token
# it holds), else straight with the token in $token. Sets verdict to
# accepted, refused (401 or 403 saying revoked or invalid) or unknown, and
# said to the status and the start of the answer. The secrets go to curl on
# its standard input, never on its command line.
probe() {
  local name=$1 port=${2:-} code
  local args=(-s -m 30 -o "$tmp/$name" -w '%{http_code}' -K -
    -H 'anthropic-version: 2023-06-01' -H 'anthropic-beta: oauth-2025-04-20')
  : > "$tmp/$name"
  if [[ -n $port ]]; then
    code=$(printf 'proxy-user = "nono:%s"\nheader = "Authorization: Bearer sortie-credential-anthropic"\n' \
      "$(cat "$STAGE0_HOME/nono-pass" 2> /dev/null)" \
      | curl "${args[@]}" --proxy "http://127.0.0.1:$port" --cacert "$STAGE0_HOME/egress-ca/ca.crt" \
        https://api.anthropic.com/v1/models) || true
  else
    code=$(printf 'header = "Authorization: Bearer %s"\n' "$token" \
      | curl "${args[@]}" --noproxy '*' https://api.anthropic.com/v1/models) || true
  fi
  said="$code $(head -c 300 "$tmp/$name" | tr -s '\n' ' ')"
  if [[ $code == 200 ]]; then
    verdict=accepted
  elif [[ $code == 401 || $code == 403 ]] && grep -qiE 'revoked|invalid|authentication_error' "$tmp/$name"; then
    verdict=refused
  else
    verdict=unknown
  fi
}

# fault_done: mark the recorded credentials fault's action done, since a
# nono now runs with an accepted token, and forget it.
fault_done() {
  [[ -s $FAULT ]] || return 0
  local id
  id=$(cat "$FAULT")
  if printf 'Authorization: Bearer %s\n' "$(cat "$STAGE0_HOME/sortie-token" 2> /dev/null)" \
    | curl -sf -o /dev/null -X POST -H @- -H 'Content-Type: application/json' \
      --data '{"by": "ortus-stage0-token"}' "$SORTIE/api/v1/platform-faults/$id/action-done"; then
    echo "nono holds an accepted Claude token again; marked platform fault $id done" >&2
  fi
  rm -f "$FAULT"
}

# record_fault CHECKED: record the credentials fault for a person through
# Sortie's person-action path, holding the stories that wait to run again,
# unless it is recorded already.
record_fault() {
  if [[ -s $FAULT ]]; then
    echo "no valid Claude token on this host; platform fault $(cat "$FAULT") already asks a person" >&2
    return 0
  fi
  local host body id
  host=$(hostname -s 2> /dev/null || hostname)
  body=$(curl -sf "$SORTIE/api/v1/state" | python3 -c '
import json, sys
state = json.load(sys.stdin)
stories = []
for key in ("retrying", "parked"):
    for e in state.get(key) or []:
        if e.get("issue_identifier") and e["issue_identifier"] not in stories:
            stories.append(e["issue_identifier"])
if not stories:
    sys.exit(3)
host, home, checked = sys.argv[1:4]
print(json.dumps({
    "action": "Log in to Claude again on %s, so that nono gets a token api.anthropic.com accepts" % host,
    "where": "on %s: claude, then /login (or claude setup-token into %s/claude-oauth-token); ortus-stage0-token.timer then restarts nono onto it" % (host, home),
    "stories": stories,
    "checked": checked,
    "cause": "Credentials: api.anthropic.com refuses the Claude token nono holds, and this host has no other token it accepts.",
}))' "$host" "$STAGE0_HOME" "$1") || {
    echo "no valid Claude token on this host, and no waiting story to hold under a fault yet; retrying later" >&2
    return 0
  }
  id=$(printf 'Authorization: Bearer %s\n' "$(cat "$STAGE0_HOME/sortie-token" 2> /dev/null)" \
    | curl -sf -X POST -H @- -H 'Content-Type: application/json' --data "$body" "$SORTIE/api/v1/supervisor/questions" \
    | python3 -c 'import json, sys; print(json.load(sys.stdin)["platform_fault"]["id"])') || {
    echo "no valid Claude token on this host, and Sortie did not record the fault; retrying later" >&2
    return 0
  }
  printf '%s\n' "$id" > "$FAULT"
  echo "no valid Claude token on this host; recorded credentials platform fault $id for a person" >&2
}

refused=() checked=()
probe nono "${STAGE0_NONO_PORT:-7690}"
checked+=("the kit's nono: $said")
[[ $verdict == refused ]] && refused+=("the kit's nono")
nono_verdict=$verdict
if systemctl --user is-active -q ortus-stage0-nono-live-proof.service; then
  probe live-proof "${STAGE0_NONO_LIVE_PROOF_PORT:-7691}"
  checked+=("the live-proof nono: $said")
  [[ $verdict == refused ]] && refused+=("the live-proof nono")
fi
if (( ${#refused[@]} )); then
  echo "api.anthropic.com refuses the Claude token ${refused[*]} holds: ${checked[*]}" >&2
  if token=$("$KIT/anthropic-token.py"); then
    probe fresh
    checked+=("anthropic-token.py's token: $said")
  else
    verdict=none
    checked+=("anthropic-token.py: no live token")
  fi
  case $verdict in
    accepted)
      echo "restarting both nonos onto the Claude token anthropic-token.py yields, which api.anthropic.com accepts" >&2
      systemctl --user restart "${NONOS[@]}"
      fault_done
      ;;
    unknown) echo "could not probe anthropic-token.py's token ($said); retrying later" >&2 ;;
    *) record_fault "$(printf '%s; ' "${checked[@]}")" ;;
  esac
  exit 0
fi
[[ $nono_verdict == accepted ]] && fault_done

running=$(cat "$STAGE0_HOME/nono-token-expiry" 2>/dev/null || echo 1)
[[ $running == 0 ]] && exit 0
fresh=$("$KIT/anthropic-token.py" --expiry) || { echo "no live Claude token on this host" >&2; exit 0; }
now=$(( $(date +%s) * 1000 ))
(( fresh == 0 || fresh > running )) || exit 0
(( running - now < 90 * 60 * 1000 )) || exit 0
busy=$(curl -sf "$SORTIE/api/v1/state" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("running", [])))') || exit 0
if (( busy > 0 && running > now )); then
  echo "Claude token expires soon, but $busy agent(s) run; retrying later" >&2
  exit 0
fi
if (( running <= now )); then
  echo "nono's Claude token has expired; restarting nono onto a fresher one" >&2
else
  echo "restarting nono onto a fresher Claude token" >&2
fi
systemctl --user restart "${NONOS[@]}"
