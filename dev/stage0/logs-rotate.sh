#!/bin/sh
# ortus-stage0-logs-rotate.service, every 10 minutes and just after midnight
# UTC (its timer): rotate the stage-0 units' log files that logs-follow.sh
# writes (README.md "Logs"). Plain sh, coreutils and gzip.
#
# Each <unit>.log whose first line is from an earlier UTC day, or that has
# passed the size cap, becomes <unit>.log.<UTC date>.gz, the date its first
# line is from; a second archive of the same day is <unit>.log.<date>.1.gz,
# and so on. An archive more than STAGE0_LOGS_KEEP_DAYS days older than
# today is deleted.
#
# The follower keeps its files open: each one to rotate is renamed, the
# follower restarted (its saved cursor loses no line and repeats none,
# logs-follow.sh), so it opens a new file, and only then is the renamed
# file compressed. A file a run renamed and did not compress is compressed
# by the next run.
#
#   STAGE0_LOGS_DIR        the files (default ~/.local/state/ortus-nova/logs)
#   STAGE0_LOGS_KEEP_DAYS  days of archives kept (default 30)
#   STAGE0_LOGS_MAX_BYTES  the size cap (default 200 MiB)
#   STAGE0_LOGS_TODAY      today's UTC date, YYYY-MM-DD (default: now; for a test)
#   STAGE0_LOGS_REOPEN     the command that makes the follower reopen its files
#                          (default: restart ortus-stage0-logs.service if it
#                          runs; empty: none, for a copy no follower writes)
set -eu

LOGS=${STAGE0_LOGS_DIR:-$HOME/.local/state/ortus-nova/logs}
KEEP_DAYS=${STAGE0_LOGS_KEEP_DAYS:-30}
MAX_BYTES=${STAGE0_LOGS_MAX_BYTES:-209715200}
TODAY=${STAGE0_LOGS_TODAY:-$(date -u +%F)}
REOPEN=${STAGE0_LOGS_REOPEN-systemctl --user try-restart ortus-stage0-logs.service}
say() { echo "logs-rotate: $*" >&2; }
isdate() { case $1 in [0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]) return 0 ;; esac; return 1; }
num() { echo "$1" | tr -d -; }

isdate "$TODAY" || { say "STAGE0_LOGS_TODAY is $TODAY; want YYYY-MM-DD"; exit 2; }
[ -d "$LOGS" ] || exit 0
cd "$LOGS"
mkdir .rotate.lock 2> /dev/null || { say "another rotation runs ($LOGS/.rotate.lock)"; exit 0; }
trap 'rmdir "$LOGS/.rotate.lock"' EXIT

today=$(num "$TODAY")
moved=0
for f in *.log; do
  [ -f "$f" ] && [ -s "$f" ] || continue
  day=$(head -c 10 "$f")
  isdate "$day" || day=$TODAY
  size=$(wc -c < "$f")
  if [ "$(num "$day")" -lt "$today" ] || [ "$size" -gt "$MAX_BYTES" ]; then
    name=$f.$day n=0
    while [ -e "$name" ] || [ -e "$name.gz" ]; do n=$((n + 1)); name=$f.$day.$n; done
    mv "$f" "$name"
    say "rotated $f ($size bytes) to $name.gz"
    moved=$((moved + 1))
  fi
done
reopened=1
if [ "$moved" -gt 0 ] && [ -n "$REOPEN" ]; then
  # shellcheck disable=SC2086 # a command and its words
  $REOPEN || { reopened=''; say "the follower did not reopen its files ($REOPEN): they are compressed by the next run"; }
fi

cutoff=$(num "$(date -u -d "$TODAY -$KEEP_DAYS days" +%F)")
for a in *.log.*; do
  [ -f "$a" ] || continue
  rest=${a#*.log.}
  day=${rest%%.*}
  isdate "$day" || continue
  case $a in
    *.gz) ;;
    *) [ -n "$reopened" ] || continue
      gzip "$a"; a=$a.gz ;;
  esac
  if [ "$(num "$day")" -lt "$cutoff" ]; then
    rm -f "$a"
    say "deleted $a, older than $KEEP_DAYS days"
  fi
done
