#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# The telemetry store end to end, under rootless podman (README.md, "The
# test"): it starts the project in a throwaway home, sends spans with
# sortie.* attributes and token-shaped strings through the collector, reads
# them back from Langfuse's public API with the session id set and the
# tokens masked, signs in as the operator, checks that sign-up is off and
# that only the collector and Langfuse publish a port, on 127.0.0.1;
# writes log records and metrics to the file store; runs the retention;
# backs up, removes the volumes, restores, and reads the span, the user and
# the keys again. CI runs it (make -C control-plane/telemetry live-test).
#
#   control-plane/telemetry/test-live.sh                  start, test, stop and remove everything
#   TELEMETRY_LIVE_KEEP=1 control-plane/telemetry/test-live.sh   leave the project running
#
# Needs podman, podman-compose 1.5 or later, curl, openssl and python3, and
# no ortus-telemetry project on this host: it refuses to touch one.
set -euo pipefail

TM=$(cd "$(dirname "$0")" && pwd)
die() { printf 'test-live: %s\n' "$*" >&2; exit 1; }
say() { printf 'test-live: %s\n' "$*" >&2; }
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

for c in podman podman-compose curl openssl python3; do command -v "$c" > /dev/null || die "$c is not installed"; done
volumes() { podman volume ls --format '{{.Name}}' | grep '^ortus-telemetry_' || true; }
[[ -z $(volumes) && -z $(podman ps -a --format '{{.Names}}' | grep '^ortus-telemetry-' || true) ]] \
  || die "an ortus-telemetry project exists on this host ($(volumes | tr '\n' ' ')): run this on another host"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/telemetry-live.XXXXXX")
export STAGE0_HOME=$tmp/stage0 TELEMETRY_UP_TIMEOUT=${TELEMETRY_UP_TIMEOUT:-900}
H=$STAGE0_HOME/telemetry
T=$TM/telemetry.sh
WEB_PORT=${TELEMETRY_LIVE_WEB_PORT:-3000}
HTTP_PORT=${TELEMETRY_LIVE_OTLP_HTTP_PORT:-4318}
GRPC_PORT=${TELEMETRY_LIVE_OTLP_PORT:-4317}

dump() {
  local c
  for c in $(podman ps -a --format '{{.Names}}' | grep '^ortus-telemetry-' || true); do
    echo "===== podman logs $c" >&2
    podman logs --tail 100 "$c" >&2 2>&1 || true
  done
}
# shellcheck disable=SC2317,SC2329 # the EXIT trap runs it
stop() {
  if [[ ${TELEMETRY_LIVE_KEEP:-} == 1 ]]; then
    say "the project keeps running: STAGE0_HOME=$STAGE0_HOME"
    return
  fi
  "$T" down > /dev/null 2>&1 || true
  local v s
  for v in $(volumes); do podman volume rm -f "$v" > /dev/null 2>&1 || true; done
  for s in $(podman secret ls --format '{{.Name}}' | grep '^ortus-telemetry-' || true); do podman secret rm "$s" > /dev/null 2>&1 || true; done
  podman network rm ortus-telemetry-internal ortus-telemetry-outside > /dev/null 2>&1 || true
  podman unshare rm -rf "$tmp" 2> /dev/null || rm -rf "$tmp"
}
trap stop EXIT

"$T" init > /dev/null 2>&1
sed -i -e "s/^LANGFUSE_WEB_SERVICE_PORT=.*/LANGFUSE_WEB_SERVICE_PORT=$WEB_PORT/" \
  -e "s|^LANGFUSE_NEXTAUTH_URL=.*|LANGFUSE_NEXTAUTH_URL=http://localhost:$WEB_PORT|" \
  -e "s/^COLLECTOR_PORTS_OTLP_HTTP_HOST_PORT=.*/COLLECTOR_PORTS_OTLP_HTTP_HOST_PORT=$HTTP_PORT/" \
  -e "s/^COLLECTOR_PORTS_OTLP_HOST_PORT=.*/COLLECTOR_PORTS_OTLP_HOST_PORT=$GRPC_PORT/" "$H/telemetry.env"
# shellcheck source=/dev/null
. "$H/telemetry.env"
PK=$LANGFUSE_INIT_PROJECT_PUBLIC_KEY
SK=$(< "$H/secrets/langfuse-init-project-secret-key")
TOKEN=$(< "$H/secrets/collector-ingest-token")
LF=http://127.0.0.1:$WEB_PORT
OTLP=http://127.0.0.1:$HTTP_PORT

say "starting the project"
if ! "$T" up; then
  dump
  die "the project did not come up"
fi

# Only the collector and Langfuse's web publish a port, each on 127.0.0.1.
published=''
for c in $(podman ps --filter name='^ortus-telemetry-' --format '{{.Names}}'); do
  while read -r port _ addr; do [[ -n $port ]] && published+="$c $port $addr"$'\n'; done < <(podman port "$c")
done
[[ $(awk 'NF {print $1}' <<< "$published" | sort -u | tr '\n' ' ') == "ortus-telemetry-collector ortus-telemetry-langfuse-web " ]] \
  && ok "only the collector and Langfuse's web publish a port" || fail "published: $published"
[[ $(grep -c . <<< "$published") == 3 && -z $(awk 'NF && $3 !~ /^127\.0\.0\.1:/' <<< "$published") ]] \
  && ok "each published port is on 127.0.0.1: $(awk 'NF {printf "%s ", $3}' <<< "$published")" || fail "published: $published"
COMPOSE_FILE=$TM/compose.yaml "$TM/../pin-images.sh" --check > /dev/null && ok "every image is pinned by digest" || fail "an image is not pinned"

# The ingest token.
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
[[ $(code -X POST -H 'Content-Type: application/json' -d '{}' "$OTLP/v1/traces") == 401 ]] \
  && [[ $(code -X POST -H 'Authorization: Bearer wrong' -H 'Content-Type: application/json' -d '{}' "$OTLP/v1/traces") == 401 ]] \
  && ok "the collector refuses OTLP/HTTP without its ingest token" || fail "the collector took a request without its token"
# OTLP/gRPC: an empty export request, its status in the grpc-status trailer.
grpc_status() {
  printf '\0\0\0\0\0' | curl -s -o /dev/null -D - --http2-prior-knowledge -H 'Content-Type: application/grpc' -H 'TE: trailers' "$@" \
    --data-binary @- "http://127.0.0.1:$GRPC_PORT/opentelemetry.proto.collector.trace.v1.TraceService/Export" \
    | tr -d '\r' | sed -n 's/^grpc-status: *//p' | head -n 1
}
[[ $(grpc_status) == 16 && $(grpc_status -H "Authorization: Bearer $TOKEN") == 0 ]] \
  && ok "the collector takes OTLP/gRPC only with its ingest token" || fail "gRPC: without $(grpc_status), with $(grpc_status -H "Authorization: Bearer $TOKEN")"

# A span from a session: sortie.* attributes, and in the tool's output a
# GitHub token, an Anthropic key and the job's egress token (the shape of a
# Sortie job token: 32 random bytes, base64url).
hex() { openssl rand -hex "$1"; }
ghp=ghp_$(hex 18)
ant=sk-ant-api03-$(hex 20)
egress=$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')
trace=$(hex 16)
now=$(date +%s%N)
span() { # TRACE START_NS NAME ATTRS
  printf '{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"telemetry-live"}}]},"scopeSpans":[{"spans":[{"traceId":"%s","spanId":"%s","name":"%s","kind":1,"startTimeUnixNano":"%s","endTimeUnixNano":"%s","attributes":[%s]}]}]}]}' \
    "$1" "$(hex 8)" "$3" "$2" "$(($2 + 1000000))" "$4"
}
attr() { printf '{"key":"%s","value":{"stringValue":"%s"}}' "$1" "$2"; }
attrs="$(attr sortie.dispatch.id d-live-1),$(attr sortie.attempt 2),$(attr sortie.issue.identifier ONS-155)"
attrs+=",$(attr langfuse.observation.output "the tool printed $ghp, $ant and $egress")"
sent=$SECONDS
[[ $(code -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$(span "$trace" "$now" "tool call" "$attrs")" "$OTLP/v1/traces") == 200 ]] \
  || fail "the collector refused the span"
# One more, a hundred days old, for the retention.
old=$(hex 16)
code -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(span "$old" "$(( $(date +%s) - 100 * 86400 ))000000000" "old turn" "$(attr sortie.issue.identifier ONS-1)")" "$OTLP/v1/traces" > /dev/null

observation() { # TRACE: its first observation as JSON, or nothing
  curl -s -u "$PK:$SK" "$LF/api/public/v2/observations?traceId=$1&fields=core,basic,io,metadata&limit=10" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin).get("data") or []; print(json.dumps(d[0]) if d else "")'
}
field() { # JSON PATH: the value at the dotted PATH, or nothing
  python3 -c '
import json, sys
v = json.loads(sys.argv[1])
for k in sys.argv[2].split("."):
    v = v.get(k) if isinstance(v, dict) else None
print("" if v is None else v)' "$1" "$2"
}
wait_observation() { # TRACE SECONDS
  local deadline=$((SECONDS + $2)) o
  while ((SECONDS < deadline)); do
    o=$(observation "$1")
    [[ -n $o ]] && { echo "$o"; return 0; }
    sleep 2
  done
  return 1
}

check_span() { # WHEN
  local o
  if ! o=$(wait_observation "$trace" 60); then fail "$1: the span is not in Langfuse within a minute"; dump; return; fi
  ok "$1: the span is in Langfuse ($((SECONDS - sent)) s after it was sent)"
  [[ $(field "$o" sessionId) == d-live-1/2 ]] && ok "$1: its session id is <sortie.dispatch.id>/<sortie.attempt>" \
    || fail "$1: session id $(field "$o" sessionId)"
  [[ $(field "$o" metadata.issue) == ONS-155 ]] && ok "$1: its trace metadata holds the issue" || fail "$1: metadata $(field "$o" metadata)"
  local out
  out=$(field "$o" output)
  [[ $out == "the tool printed [redacted], [redacted] and [redacted]" ]] \
    && ok "$1: each token in the tool's output shows [redacted], the egress token's included" || fail "$1: output $out"
  [[ $o != *"$ghp"* && $o != *"$ant"* && $o != *"$egress"* ]] && ok "$1: no token is stored" || fail "$1: a token is stored"
}
check_span "sent"

# The operator signs in; nobody signs up.
signin() {
  local jar csrf
  jar=$(mktemp)
  csrf=$(curl -s -c "$jar" -b "$jar" "$LF/api/auth/csrf" | python3 -c 'import json,sys; print(json.load(sys.stdin)["csrfToken"])')
  curl -s -o /dev/null -c "$jar" -b "$jar" --data-urlencode "email=$LANGFUSE_INIT_USER_EMAIL" \
    --data-urlencode "password=$(< "$H/secrets/langfuse-init-user-password")" --data-urlencode "csrfToken=$csrf" \
    --data json=true "$LF/api/auth/callback/credentials"
  curl -s -b "$jar" "$LF/api/auth/session"
  rm -f "$jar"
}
check_user() { # WHEN
  local s
  s=$(signin)
  [[ $(field "$s" user.email) == "$LANGFUSE_INIT_USER_EMAIL" && $s == *'"id":"'"$LANGFUSE_INIT_ORG_ID"'"'* && $s == *'"id":"'"$LANGFUSE_INIT_PROJECT_ID"'"'* ]] \
    && ok "$1: the operator signs in, owner of the one organisation and project" || fail "$1: session $s"
}
check_user "started"
[[ $(code -H 'Content-Type: application/json' -d '{"name":"x","email":"someone@example.org","password":"a-Long-passw0rd!"}' "$LF/api/auth/signup") == 422 ]] \
  && ok "sign-up is off" || fail "sign-up is not refused"

# Log records and metrics go to the file store, masked.
code -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"resourceLogs":[{"resource":{},"scopeLogs":[{"logRecords":[{"timeUnixNano":"'"$now"'","body":{"stringValue":"pushed with '"$ghp"'"}}]}]}]}' "$OTLP/v1/logs" > /dev/null
code -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"resourceMetrics":[{"resource":{},"scopeMetrics":[{"metrics":[{"name":"telemetry.live","gauge":{"dataPoints":[{"timeUnixNano":"'"$now"'","asInt":"1"}]}}]}]}]}' "$OTLP/v1/metrics" > /dev/null
day=$(date -u +%Y-%m-%d)
for _ in $(seq 20); do [[ -s $H/file-store/logs/$day.jsonl && -s $H/file-store/metrics/$day.jsonl ]] && break; sleep 1; done
grep -q 'pushed with \[redacted\]' "$H/file-store/logs/$day.jsonl" 2> /dev/null && ! grep -q "$ghp" "$H/file-store/logs/$day.jsonl" \
  && ok "log records go to the file store, masked" || fail "logs: $(cat "$H/file-store/logs/$day.jsonl" 2>&1)"
grep -q telemetry.live "$H/file-store/metrics/$day.jsonl" 2> /dev/null && ok "metrics go to the file store" || fail "no metrics in the file store"

# The retention: the hundred-day-old trace goes, today's stays; the file
# store loses what is older than the retention.
wait_observation "$old" 60 > /dev/null || fail "the old span is not in Langfuse"
touch -d '100 days ago' "$H/file-store/logs/2000-01-01.jsonl"
"$T" retention > "$tmp/retention" 2>&1 || fail "retention: $(cat "$tmp/retention")"
gone=''
for _ in $(seq 60); do [[ -z $(observation "$old") ]] && gone=1 && break; sleep 3; done
[[ $gone ]] && ok "the retention deletes a trace older than TELEMETRY_RETENTION_DAYS through Langfuse's API" || fail "the old trace stays"
[[ -n $(observation "$trace") ]] && ok "and keeps today's" || fail "the retention deleted today's trace"
[[ ! -e $H/file-store/logs/2000-01-01.jsonl && -s $H/file-store/logs/$day.jsonl ]] \
  && ok "and prunes the file store to the same age" || fail "file store: $(ls "$H/file-store/logs")"

# The backup, keeping seven.
B=$STAGE0_HOME/telemetry-backups
mkdir -p "$B"
for i in 1 2 3 4 5 6 7; do mkdir -p "$B/2000010${i}T000000Z"; done
"$T" backup > "$tmp/backup" 2>&1 || { fail "backup: $(cat "$tmp/backup")"; dump; }
latest=$(tail -n 1 "$tmp/backup")
[[ -s $latest/postgres.dump && -d $latest/clickhouse/metadata && -d $latest/minio ]] \
  && ok "the backup holds a pg_dump, a ClickHouse BACKUP and a copy of the MinIO bucket" || fail "backup: $(find "$latest" -maxdepth 2 | head -20)"
[[ $(find "$B" -mindepth 1 -maxdepth 1 -type d | wc -l) == 7 && ! -e $B/20000101T000000Z ]] \
  && ok "seven backups are kept" || fail "backups: $(ls "$B")"

# The restore: the volumes go, the restore script runs on the last backup.
"$T" down > /dev/null 2>&1
for v in $(volumes); do podman volume rm -f "$v" > /dev/null; done
[[ -z $(volumes) ]] && ok "the volumes are removed" || fail "volumes left: $(volumes)"
if "$T" restore > "$tmp/restore" 2>&1; then
  ok "the restore script ran on the last backup"
else
  fail "restore: $(tail -n 20 "$tmp/restore")"
  dump
fi
sent=$SECONDS
check_span "restored"
check_user "restored"
[[ $(code -u "$PK:$SK" "$LF/api/public/projects") == 200 ]] && ok "restored: the project's keys work" || fail "restored: the keys do not"

exit $failed
