#!/usr/bin/env bash
# Checks refresh-token.sh with systemctl, curl and anthropic-token.py stubbed:
# when it restarts the three nonos onto a fresher Claude token, and when it waits.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
mkdir -p "$tmp/bin" "$tmp/kit" "$STAGE0_HOME"
cp "$KIT/refresh-token.sh" "$tmp/kit/"
printf 'STAGE0_SORTIE_PORT=1\n' > "$STAGE0_HOME/stage0.env"
# shellcheck disable=SC2016 # expanded by the stubs, not here
printf '#!/bin/sh\n[ "$1 $2" = "--user is-active" ] && exit 0\necho "systemctl $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/systemctl"
# shellcheck disable=SC2016
printf '#!/bin/sh\necho "{\\"running\\": [$BUSY]}"\n' > "$tmp/bin/curl"
# shellcheck disable=SC2016
printf '#!/bin/sh\necho "$FRESH"\n' > "$tmp/kit/anthropic-token.py"
chmod +x "$tmp/bin/"* "$tmp/kit/"*
export PATH=$tmp/bin:$PATH
failed=0
now=$(( $(date +%s) * 1000 ))
min=$(( 60 * 1000 ))

check() { # check NAME RUNNING_EXPIRY FRESH_EXPIRY BUSY WANT_RESTART
  rm -f "$tmp/calls"
  echo "$2" > "$STAGE0_HOME/nono-token-expiry"
  FRESH=$3 BUSY=$4 "$tmp/kit/refresh-token.sh" 2> /dev/null
  local got=no
  grep -q 'restart ortus-stage0-nono.service ortus-stage0-nono-live-proof.service ortus-stage0-nono-homelab.service' "$tmp/calls" 2> /dev/null && got=yes
  if [[ $got == "$5" ]]; then echo "ok: $1"; else echo "FAIL: $1: restart $got, want $5"; failed=1; fi
}

check "a token far from expiry is kept" $(( now + 300 * min )) $(( now + 480 * min )) "" no
check "a token near expiry is replaced when idle" $(( now + 30 * min )) $(( now + 480 * min )) "" yes
check "a token near expiry waits while an agent runs" $(( now + 30 * min )) $(( now + 480 * min )) 1 no
check "an expired token is replaced although agents run" $(( now - 60 * min )) $(( now + 480 * min )) 1,2 yes
check "no fresher token: nothing to do" $(( now - 60 * min )) $(( now - 120 * min )) 1 no

exit $failed
