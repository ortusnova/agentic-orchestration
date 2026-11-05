#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks the stage-0 units' log files (logs.sh, logs-follow.sh,
# logs-rotate.sh) with systemctl and journalctl stubbed: the install is
# idempotent; the follower writes each stage-0 unit's lines to its file, with
# the unit, and resumes from its cursor with no line lost or repeated; the
# rotation compresses by day and by size, keeps 30 days, and has the
# follower reopen its files.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
mkdir -p "$tmp/bin" "$STAGE0_HOME"

# --- The install -----------------------------------------------------------
# is-enabled and is-active answer from $tmp/enabled and $tmp/active; each
# call that changes something is recorded in $tmp/calls.
cat > "$tmp/bin/systemctl" <<EOF
#!/bin/sh
case "\$2" in
  is-enabled) grep -qx "\$4" "$tmp/enabled" 2> /dev/null ;;
  is-active) grep -qx "\$4" "$tmp/active" 2> /dev/null ;;
  enable) echo "enable \$4" >> "$tmp/calls"; echo "\$4" >> "$tmp/enabled" ;;
  start|restart) echo "\$2 \$3" >> "$tmp/calls"; echo "\$3" >> "$tmp/active" ;;
  *) echo "\$*" >> "$tmp/calls" ;;
esac
EOF
chmod +x "$tmp/bin/systemctl"
PATH=$tmp/bin:$PATH
# shellcheck source=unit-restart.sh
. "$KIT/unit-restart.sh"
# shellcheck source=logs.sh
. "$KIT/logs.sh"
units=$tmp/units
# A copy of the kit's files the install reads, so the test can change them.
real_kit=$KIT KIT=$tmp/kit
mkdir -p "$KIT/units"
cp "$real_kit/logs-follow.sh" "$KIT/"
for u in "${LOGS_UNITS[@]}"; do cp "$real_kit/units/$u" "$KIT/units/"; done
calls() { [[ -f $tmp/calls ]] && tr '\n' ';' < "$tmp/calls"; true; }

logs_install "$units" 2> /dev/null
[[ $(calls) == "--user daemon-reload;enable ortus-stage0-logs.service;enable ortus-stage0-logs-rotate.timer;start ortus-stage0-logs-rotate.timer;restart ortus-stage0-logs.service;" ]] \
  && ok "the first install renders, enables and starts the units" || fail "first install: $(calls)"
grep -qx "ExecStart=$KIT/logs-follow.sh" "$units/ortus-stage0-logs.service" \
  && grep -qx "ExecStart=$KIT/logs-rotate.sh" "$units/ortus-stage0-logs-rotate.service" \
  && grep -qx 'Restart=always' "$units/ortus-stage0-logs.service" \
  && ok "the units run the kit's scripts, and the follower always restarts" || fail "rendered units"
before=$(cd "$units" && ls -l --time-style=+%s.%N && sha256sum ./*)
rm -f "$tmp/calls"
logs_install "$units" 2> /dev/null
[[ -z $(calls) ]] && ok "a second install changes nothing" || fail "second install: $(calls)"
[[ $(cd "$units" && ls -l --time-style=+%s.%N && sha256sum ./*) == "$before" ]] \
  && ok "and rewrites no unit" || fail "a unit was rewritten"
echo '# edited' >> "$units/ortus-stage0-logs-rotate.service"
rm -f "$tmp/calls"
logs_install "$units" 2> /dev/null
[[ $(calls) == "--user daemon-reload;" ]] && ok "a unit edited by hand is rendered again" || fail "edited: $(calls)"
for f in units/ortus-stage0-logs.service logs-follow.sh; do
  echo '# changed' >> "$KIT/$f"
  rm -f "$tmp/calls"
  logs_install "$units" 2> /dev/null
  [[ $(calls) == *"restart ortus-stage0-logs.service;" ]] && ok "a change to the kit's $f restarts the follower" || fail "$f: $(calls)"
done
KIT=$real_kit
# shellcheck disable=SC2016 # the text of stage0.sh
[[ $(grep -cF 'logs_install "$UNITS" ||' "$KIT/stage0.sh") == 2 ]] \
  && ok "stage0.sh installs the log files' units on p1 and on a worker host" || fail "stage0.sh does not call logs_install in both profiles"

# --- The follower ----------------------------------------------------------
# journalctl prints the entries of $tmp/journal after the cursor in its
# --cursor-file (all of them with --no-tail and no cursor), then follows
# until SIGTERM, when it writes the cursor of the last entry it printed.
cat > "$tmp/bin/journalctl" <<'EOF'
#!/usr/bin/env bash
cf='' all='' last=''
trap '[[ -z $last ]] || echo "$last" > "$cf"; exit 0' TERM
for a; do
  case $a in --cursor-file=*) cf=${a#*=} ;; --no-tail) all=1 ;; esac
done
last=$(cat "$cf" 2> /dev/null || true)
echo "$*" >> "$STUB_JOURNALCTL_ARGS"
skip=1
[[ -z $last && -n $all ]] && skip=''
[[ -z $last && -z $all ]] && skip=never
while IFS= read -r line; do
  cur=$(sed 's/.*"__CURSOR":"\([^"]*\)".*/\1/' <<< "$line")
  if [[ -n $skip ]]; then [[ $cur == "$last" ]] && skip=''; continue; fi
  printf '%s\n' "$line"
  last=$cur
done < "$STUB_JOURNAL"
while :; do sleep 0.05 & wait $!; done
EOF
chmod +x "$tmp/bin/journalctl"
export STUB_JOURNAL=$tmp/journal STUB_JOURNALCTL_ARGS=$tmp/journalctl-args STAGE0_LOGS_DIR=$tmp/logs
logs=$tmp/logs
entry() { # cursor usec unit message [field...]
  local c=$1 t=$2 u=$3 m=$4 more=''
  shift 4
  (( $# == 0 )) || more=$(printf ',%s' "$@")
  printf '{"__CURSOR":"%s","__REALTIME_TIMESTAMP":"%s","_HOSTNAME":"p1","_SYSTEMD_USER_UNIT":"%s","SYSLOG_IDENTIFIER":"run-sortie.sh","_PID":"42","MESSAGE":%s%s}\n' \
    "$c" "$t" "$u" "$m" "$more"
}
{
  entry c1 1793917807003294 ortus-stage0-sortie.service '"level=INFO msg=\"started\" path=C:\\x"'
  entry c2 1793917808000001 podman-restart.service '"not ours"'
  entry c3 1793917809000002 init.scope '"Started ortus-stage0-worker.service."' '"USER_UNIT":"ortus-stage0-worker.service"'
  entry c4 1793917810123456 ortus-stage0-preview@ons-9.service '[27,91,51,49,109,114,101,100,27,91,48,109]'
} > "$tmp/journal"
# follow N: run the follower until its files hold N lines, then stop it.
follow() {
  local want=$1 pid have=0 starts started
  starts=$({ cat "$tmp/journalctl-args" 2> /dev/null || true; } | wc -l)
  "$KIT/logs-follow.sh" & pid=$!
  for _ in $(seq 200); do
    have=$({ cat "$logs"/*.log 2> /dev/null || true; } | wc -l)
    started=$({ cat "$tmp/journalctl-args" 2> /dev/null || true; } | wc -l)
    (( have >= want && started > starts )) && break
    sleep 0.05
  done
  kill -TERM "$pid"; wait "$pid"
}
follow 3
[[ $(cat "$logs/ortus-stage0-sortie.log") == '2026-11-05T22:30:07.003294+00:00 p1 ortus-stage0-sortie.service run-sortie.sh[42]: level=INFO msg="started" path=C:\x' ]] \
  && ok "a unit's line is short-iso-precise in UTC, with the unit, its message decoded" || fail "sortie: $(cat "$logs/ortus-stage0-sortie.log")"
grep -q ' ortus-stage0-worker.service run-sortie.sh\[42\]: Started ortus-stage0-worker.service\.$' "$logs/ortus-stage0-worker.log" \
  && ok "systemd's lines about a unit go to the unit's file" || fail "worker: $(cat "$logs/ortus-stage0-worker.log" 2>&1)"
[[ $(cat "$logs/ortus-stage0-preview@ons-9.log") == *"ortus-stage0-preview@ons-9.service run-sortie.sh[42]: "$'\e'"[31mred"$'\e'"[0m" ]] \
  && ok "a unit that first logs after the follower started is kept, and a binary message too" || fail "preview: $(cat -v "$logs/ortus-stage0-preview@ons-9.log")"
[[ $(ls "$logs") == $'ortus-stage0-preview@ons-9.log\nortus-stage0-sortie.log\nortus-stage0-worker.log' ]] \
  && ok "another unit's lines are dropped" || fail "files: $(ls "$logs")"
[[ $(cat "$logs/.cursor") == c4 ]] && ok "a stop saves the cursor of the last line" || fail "cursor: $(cat "$logs/.cursor")"
grep -q -- '--no-tail' "$tmp/journalctl-args" && ok "the first start reads the whole retained journal" || fail "first start: $(cat "$tmp/journalctl-args")"
entry c5 1793917811000000 ortus-stage0-sortie.service '"second start"' >> "$tmp/journal"
entry c6 1793917812000000 ortus-stage0-sortie.service '"and more"' >> "$tmp/journal"
follow 5
[[ $(wc -l < "$logs/ortus-stage0-sortie.log") == 3 && $(sort "$logs"/*.log | uniq -d) == '' ]] \
  && ok "a restart resumes from the cursor: no line lost, none repeated" || fail "resume: $(cat "$logs/ortus-stage0-sortie.log")"
[[ $(tail -1 "$tmp/journalctl-args") != *--no-tail* ]] && ok "and does not read the journal again" || fail "resume args: $(tail -1 "$tmp/journalctl-args")"
follow 5
[[ $(cat "$logs"/*.log | wc -l) == 5 ]] && ok "a restart with nothing new writes nothing" || fail "idle restart: $(cat "$logs"/*.log | wc -l) lines"

# --- The rotation ----------------------------------------------------------
rot=$tmp/rot
mkdir -p "$rot"
export STAGE0_LOGS_DIR=$rot STAGE0_LOGS_TODAY=2026-11-05 STAGE0_LOGS_MAX_BYTES=100 STAGE0_LOGS_REOPEN="$tmp/bin/reopen"
printf '#!/bin/sh\necho reopen >> %s/reopens\n' "$tmp" > "$tmp/bin/reopen"; chmod +x "$tmp/bin/reopen"
reopens() { cat "$tmp/reopens" 2> /dev/null | wc -l; }
printf '2026-11-04T23:59:59.000000+00:00 p1 a.service x[1]: yesterday\n' > "$rot/a.log"
printf '2026-11-05T00:00:01.000000+00:00 p1 b.service x[1]: today\n' > "$rot/b.log"
head -c 150 /dev/zero | tr '\0' x | sed 's/^/2026-11-05T01:00:00.000000+00:00 /' > "$rot/c.log"
: > "$rot/e.log"
for d in 2026-10-05 2026-10-06; do echo old | gzip > "$rot/x.log.$d.gz"; done
echo left > "$rot/y.log.2026-11-03"
"$KIT/logs-rotate.sh" 2> /dev/null
[[ $(zcat "$rot/a.log.2026-11-04.gz") == *yesterday && ! -e $rot/a.log ]] \
  && ok "a file from an earlier day is compressed under that day" || fail "a: $(ls "$rot")"
[[ -s $rot/b.log && ! -e $rot/b.log.2026-11-05.gz ]] && ok "today's file under the cap stays" || fail "b: $(ls "$rot")"
[[ -e $rot/c.log.2026-11-05.gz && ! -e $rot/c.log ]] && ok "a file over the size cap rotates early" || fail "c: $(ls "$rot")"
[[ -e $rot/e.log && ! -e $rot/e.log.2026-11-05.gz ]] && ok "an empty file stays" || fail "e: $(ls "$rot")"
[[ ! -e $rot/x.log.2026-10-05.gz && -e $rot/x.log.2026-10-06.gz ]] \
  && ok "an archive more than 30 days old goes, one 30 days old stays" || fail "x: $(ls "$rot")"
[[ $(zcat "$rot/y.log.2026-11-03.gz") == left ]] && ok "a file an earlier run renamed is compressed" || fail "y: $(ls "$rot")"
[[ $(reopens) == 1 ]] && ok "the follower reopens its files once" || fail "reopens: $(reopens)"
head -c 150 /dev/zero | tr '\0' x | sed 's/^/2026-11-05T02:00:00.000000+00:00 /' > "$rot/c.log"
"$KIT/logs-rotate.sh" 2> /dev/null
[[ -e $rot/c.log.2026-11-05.1.gz && -e $rot/c.log.2026-11-05.gz ]] \
  && ok "a second rotation of the same day keeps the first" || fail "c again: $(ls "$rot")"
before=$(ls -l --time-style=+%s.%N "$rot")
"$KIT/logs-rotate.sh" 2> /dev/null
[[ $(ls -l --time-style=+%s.%N "$rot") == "$before" && $(reopens) == 2 ]] \
  && ok "a run with nothing to rotate changes nothing and restarts nothing" || fail "idle: $(ls "$rot"), reopens $(reopens)"
STAGE0_LOGS_TODAY=2026-12-05 "$KIT/logs-rotate.sh" 2> /dev/null
[[ -e $rot/b.log.2026-11-05.gz && ! -e $rot/a.log.2026-11-04.gz && ! -e $rot/x.log.2026-10-06.gz ]] \
  && ok "a month later, only the last 30 days remain" || fail "month: $(ls "$rot")"
exit $failed
