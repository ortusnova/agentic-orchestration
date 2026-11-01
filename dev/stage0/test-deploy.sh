#!/usr/bin/env bash
# Checks deploy.sh against a throwaway kit repository, with uv, npm, npx,
# systemctl and curl stubbed: what each kind of merge does to the live kit.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null STAGE0_HOME=$tmp/home STAGE0_LIVE_KIT=$tmp/live
g() { git -c user.name=t -c user.email=t@t -c init.defaultBranch=main "$@"; }
failed=0

mkdir -p "$tmp/bin" "$STAGE0_HOME"
for tool in uv npm systemctl; do printf '#!/bin/sh\necho "%s $*" >> "%s/calls"\n' "$tool" "$tmp" > "$tmp/bin/$tool"; done
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\necho "npx $*" >> "%s/calls"\n[ -z "$FAIL_BUILD" ]\n' "$tmp" > "$tmp/bin/npx"
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\necho "{\\"running\\": [$BUSY]}"\n' > "$tmp/bin/curl"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH BUSY='' FAIL_BUILD=''
printf 'STAGE0_PROFILE=control\nSTAGE0_SORTIE_PORT=1\n' > "$STAGE0_HOME/stage0.env"

setup() {
  rm -rf "$tmp/remote" "$tmp/up" "$STAGE0_LIVE_KIT" "$tmp/calls" "$STAGE0_HOME/deploy.failed"
  g init -q --bare "$tmp/remote"
  g clone -q "$tmp/remote" "$tmp/up" 2> /dev/null
  mkdir -p "$tmp/up/dev/stage0" "$tmp/up/workbench/frontend/node_modules" "$tmp/up/docs"
  printf '#!/bin/sh\necho "stage0.sh" >> "%s/calls"\n' "$tmp" > "$tmp/up/dev/stage0/stage0.sh"
  chmod +x "$tmp/up/dev/stage0/stage0.sh"
  echo 1 > "$tmp/up/workbench/app"; echo 1 > "$tmp/up/docs/x"; echo 1 > "$tmp/up/dev/stage0/units"
  g -C "$tmp/up" add -A; g -C "$tmp/up" commit -qm one; g -C "$tmp/up" push -q origin main
  g clone -q "$tmp/remote" "$STAGE0_LIVE_KIT"
  mkdir -p "$STAGE0_LIVE_KIT/workbench/frontend/node_modules"
}
merge() { echo "$RANDOM" >> "$tmp/up/$1"; g -C "$tmp/up" commit -qam "$1"; g -C "$tmp/up" push -q origin main; }
at() { [[ $(g -C "$STAGE0_LIVE_KIT" rev-parse HEAD) == $(g -C "$tmp/remote" rev-parse "$1") ]]; }
check() { # check NAME WANT_REF WANT_CALLS
  local calls=''; [[ -f $tmp/calls ]] && calls=$(tr '\n' ';' < "$tmp/calls")
  if at "$2" && [[ $calls == "$3" ]]; then echo "ok: $1"; else echo "FAIL: $1: at $(g -C "$STAGE0_LIVE_KIT" rev-parse --short HEAD), calls '$calls', want $2 and '$3'"; failed=1; fi
}
run() { "$KIT/deploy.sh" 2> "$tmp/err" || true; }

setup; run; check "nothing merged" main ""
setup; merge docs/x; run; check "a docs change moves the kit, builds nothing" main ""
setup; merge workbench/app; run
check "a workbench change rebuilds and restarts the workbench" main "uv sync -q;npx vite build --logLevel warn;systemctl --user restart ortus-stage0-workbench.service;"
setup; merge dev/stage0/units; BUSY=1 run; check "a kit change waits while an agent runs" main~1 ""
setup; merge dev/stage0/units; run; check "a kit change runs stage0.sh when idle" main "stage0.sh;"
setup; merge workbench/app; FAIL_BUILD=1 run; check "a failed build puts the previous commit back" main~1 "uv sync -q;npx vite build --logLevel warn;"
rm -f "$tmp/calls"; FAIL_BUILD=1 run; check "and does not try that commit again" main~1 ""
merge workbench/app; run; check "until another one merges" main "uv sync -q;npx vite build --logLevel warn;systemctl --user restart ortus-stage0-workbench.service;"

exit $failed
