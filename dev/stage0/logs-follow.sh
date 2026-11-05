#!/bin/sh
# ortus-stage0-logs.service: copy every stage-0 unit's journal lines into a
# durable file per unit, $STAGE0_LOGS_DIR/<unit>.log (README.md "Logs").
# The user journal keeps about 40 MB on a host, which a few hours of the
# host's other units fill, so it is no record; these files are, rotated by
# logs-rotate.sh.
#
# One journalctl follows the whole user journal, from the cursor it saved
# when it last stopped ($STAGE0_LOGS_DIR/.cursor; the whole retained journal
# on the first start), so a restart neither loses nor repeats a line. A
# glob of units (-u 'ortus-stage0-*') would not do: journalctl expands it
# once, against the units already in the journal, and would miss a unit
# that first logs later, such as a new preview. awk keeps each line of a
# unit named ortus-stage0-* (its own lines, and systemd's about it) as
#
#   2026-11-05T22:28:39.162795+00:00 p1 ortus-stage0-sortie.service run-sortie.sh[1023457]: message
#
# (short-iso-precise, in UTC, with the unit), in <unit>.log, the .service
# suffix dropped from the file's name.
#
# A stop (SIGTERM) stops journalctl, which writes its cursor, and lets awk
# write what it already read: the unit's KillMode=mixed signals this script
# only. Plain sh, journalctl and awk.
set -eu

# A stop before journalctl starts stops it as soon as it starts.
jpid='' stop=''
trap 'stop=1; [ -z "$jpid" ] || kill -TERM "$jpid" 2> /dev/null || true' TERM INT HUP
LOGS=${STAGE0_LOGS_DIR:-$HOME/.local/state/ortus-nova/logs}
JOURNALCTL=${STAGE0_JOURNALCTL:-journalctl}
mkdir -p "$LOGS"
fifo=$LOGS/.follow.fifo
rm -f "$fifo"
mkfifo "$fifo"

LC_ALL=C awk -v logs="$LOGS" '
# The JSON string at s[i] (just past its opening quote), decoded.
function jstring(s, i,    out, rest, c, n) {
  out = ""
  rest = substr(s, i)
  while (match(rest, /[\\"]/)) {
    out = out substr(rest, 1, RSTART - 1)
    c = substr(rest, RSTART, 1)
    if (c == "\"") return out
    c = substr(rest, RSTART + 1, 1)
    if (c == "n") out = out "\n"
    else if (c == "t") out = out "\t"
    else if (c == "r" || c == "b" || c == "f") out = out ""
    else if (c == "u") {
      n = hex(substr(rest, RSTART + 2, 4))
      out = out utf8(n)
      rest = substr(rest, RSTART + 6)
      continue
    }
    else out = out c
    rest = substr(rest, RSTART + 2)
  }
  return out rest
}
function hex(h,    i, n) {
  n = 0
  for (i = 1; i <= length(h); i++) n = n * 16 + index("0123456789abcdef", tolower(substr(h, i, 1))) - 1
  return n
}
function utf8(n) {
  if (n < 128) return sprintf("%c", n)
  if (n < 2048) return sprintf("%c%c", 192 + int(n / 64), 128 + n % 64)
  return sprintf("%c%c%c", 224 + int(n / 4096), 128 + int(n / 64) % 64, 128 + n % 64)
}
# Field k of the entry: a string, the first of several, or the bytes of a
# binary value (an array of numbers); "" when the entry lacks it.
function field(k,    p, rest, out, nb, b, j) {
  # A key follows "{" or ","; inside a string value a quote is escaped.
  p = index($0, "{\"" k "\":")
  if (!p) p = index($0, ",\"" k "\":")
  if (!p) return ""
  p += length(k) + 4
  if (substr($0, p, 1) == "\"") return jstring($0, p + 1)
  if (substr($0, p, 2) == "[\"") return jstring($0, p + 2)
  if (substr($0, p, 1) == "[") {
    rest = substr($0, p + 1)
    rest = substr(rest, 1, index(rest, "]") - 1)
    out = ""
    nb = split(rest, b, ",")
    for (j = 1; j <= nb; j++) out = out sprintf("%c", b[j] + 0)
    return out
  }
  return ""
}
# Microseconds since the epoch, as short-iso-precise in UTC.
function iso(us,    s, frac, days, secs, z, era, doe, yoe, doy, mp, d, m, y) {
  if (length(us) < 7) us = sprintf("%07d", us)
  frac = substr(us, length(us) - 5)
  s = substr(us, 1, length(us) - 6) + 0
  days = int(s / 86400); secs = s - days * 86400
  z = days + 719468
  era = int(z / 146097)
  doe = z - era * 146097
  yoe = int((doe - int(doe / 1460) + int(doe / 36524) - int(doe / 146096)) / 365)
  y = yoe + era * 400
  doy = doe - (365 * yoe + int(yoe / 4) - int(yoe / 100))
  mp = int((5 * doy + 2) / 153)
  d = doy - int((153 * mp + 2) / 5) + 1
  m = mp < 10 ? mp + 3 : mp - 9
  if (m <= 2) y++
  return sprintf("%04d-%02d-%02dT%02d:%02d:%02d.%s+00:00", y, m, d, int(secs / 3600), int(secs % 3600 / 60), secs % 60, frac)
}
{
  unit = field("_SYSTEMD_USER_UNIT")
  if (unit !~ /^ortus-stage0-/) unit = field("USER_UNIT")
  if (unit !~ /^ortus-stage0-[A-Za-z0-9@._:\\-]+$/) next
  ident = field("SYSLOG_IDENTIFIER"); pid = field("_PID")
  if (ident == "") ident = unit
  if (pid != "") ident = ident "[" pid "]"
  msg = field("MESSAGE")
  gsub(/\n/, "\n    ", msg)
  name = unit
  sub(/\.service$/, "", name)
  file = logs "/" name ".log"
  printf "%s %s %s %s: %s\n", iso(field("__REALTIME_TIMESTAMP")), field("_HOSTNAME"), unit, ident, msg >> file
  fflush(file)
}' < "$fifo" &
apid=$!

if [ -s "$LOGS/.cursor" ]; then start=''; else start=--no-tail; fi
# shellcheck disable=SC2086 # $start is one flag or none
"$JOURNALCTL" --user --follow $start --cursor-file="$LOGS/.cursor" --output=json \
  --output-fields=MESSAGE,_SYSTEMD_USER_UNIT,USER_UNIT,SYSLOG_IDENTIFIER,_PID,_HOSTNAME > "$fifo" &
jpid=$!
[ -z "$stop" ] || kill -TERM "$jpid"

# A trapped signal ends a wait early: wait until journalctl has written its
# cursor and exited, then until awk has written every line it read.
rc=0
wait "$jpid" || rc=$?
while kill -0 "$jpid" 2> /dev/null; do rc=0; wait "$jpid" || rc=$?; done
wait "$apid" || rc=$?
rm -f "$fifo"
exit "$rc"
