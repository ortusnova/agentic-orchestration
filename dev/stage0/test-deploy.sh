#!/usr/bin/env bash
# Checks deploy.sh against a throwaway kit repository and Sortie checkout,
# with uv, npm, npx, systemctl and curl stubbed: what each kind of merge does
# to the live kit, and that it runs under stage0.sh's lock.
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
export PATH=$tmp/bin:$PATH BUSY='' FAIL_BUILD='' FAIL_STAGE0=''
unset STAGE0_LOCKED
printf 'STAGE0_PROFILE=control\nSTAGE0_SORTIE_PORT=1\nSTAGE0_SORTIE_SRC=%s\n' "$tmp/sortie" > "$STAGE0_HOME/stage0.env"

setup() {
  rm -rf "$tmp/remote" "$tmp/up" "$STAGE0_LIVE_KIT" "$tmp/calls" "$STAGE0_HOME/deploy.failed" \
    "$tmp/sremote" "$tmp/sup" "$tmp/sortie" "$STAGE0_HOME/deploy-sortie.failed"
  g init -q --bare "$tmp/remote"
  g clone -q "$tmp/remote" "$tmp/up" 2> /dev/null
  mkdir -p "$tmp/up/dev/stage0" "$tmp/up/workbench/frontend/node_modules" "$tmp/up/docs"
  # Like the real one, --update-sortie moves the Sortie checkout to plan-bench.
  # shellcheck disable=SC2016 # expanded by the stub, not here
  printf '#!/bin/sh\nprintf stage0.sh >> "%s/calls"; for a; do printf " %%s" "$a" >> "%s/calls"; done; echo >> "%s/calls"\n[ "$1" != --update-sortie ] || git -C "$STAGE0_SORTIE_SRC" checkout -q --detach origin/plan-bench\n[ -z "$FAIL_STAGE0" ]\n' \
    "$tmp" "$tmp" "$tmp" > "$tmp/up/dev/stage0/stage0.sh"
  chmod +x "$tmp/up/dev/stage0/stage0.sh"
  echo 1 > "$tmp/up/workbench/app"; echo 1 > "$tmp/up/docs/x"; echo 1 > "$tmp/up/dev/stage0/units"
  g -C "$tmp/up" add -A; g -C "$tmp/up" commit -qm one; g -C "$tmp/up" push -q origin main
  g clone -q "$tmp/remote" "$STAGE0_LIVE_KIT"
  mkdir -p "$STAGE0_LIVE_KIT/workbench/frontend/node_modules"
  g init -q --bare "$tmp/sremote"
  g clone -q "$tmp/sremote" "$tmp/sup" 2> /dev/null
  echo 1 > "$tmp/sup/go"; g -C "$tmp/sup" add -A; g -C "$tmp/sup" commit -qm one; g -C "$tmp/sup" push -q origin HEAD:plan-bench
  g clone -q --branch plan-bench "$tmp/sremote" "$tmp/sortie"
  g -C "$tmp/sortie" checkout -q --detach
}
merge() { echo "$RANDOM" >> "$tmp/up/$1"; g -C "$tmp/up" commit -qam "$1"; g -C "$tmp/up" push -q origin main; }
smerge() { echo "$RANDOM" >> "$tmp/sup/go"; g -C "$tmp/sup" commit -qam sortie; g -C "$tmp/sup" push -q origin HEAD:plan-bench; }
sortie_at() { [[ $(g -C "$tmp/sortie" rev-parse HEAD) == $(g -C "$tmp/sremote" rev-parse "$1") ]]; }
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
# stage0.sh runs with the PATH the last stage0.sh recorded, not the unit's.
setup; mkdir -p "$tmp/tools"; echo "$tmp/tools:$PATH" > "$STAGE0_HOME/path"
# shellcheck disable=SC2016 # the stub expands $PATH, not this script
printf '#!/bin/sh\necho "stage0.sh $PATH" >> "%s/calls"\n' "$tmp" > "$tmp/up/dev/stage0/stage0.sh"
g -C "$tmp/up" commit -qam path; g -C "$tmp/up" push -q origin main
g -C "$STAGE0_LIVE_KIT" pull -q
merge dev/stage0/units; run
if grep -q "^stage0.sh $tmp/tools:" "$tmp/calls"; then echo "ok: stage0.sh gets the recorded PATH"; else echo "FAIL: stage0.sh PATH: $(cat "$tmp/calls")"; failed=1; fi
rm -f "$STAGE0_HOME/path"
setup; merge workbench/app; FAIL_BUILD=1 run; check "a failed build puts the previous commit back" main~1 "uv sync -q;npx vite build --logLevel warn;"
rm -f "$tmp/calls"; FAIL_BUILD=1 run; check "and does not try that commit again" main~1 ""
merge workbench/app; run; check "until another one merges" main "uv sync -q;npx vite build --logLevel warn;systemctl --user restart ortus-stage0-workbench.service;"

# Sortie follows plan-bench, when no agent runs.
setup; smerge; BUSY=1 run; check "a plan-bench merge waits while an agent runs" main ""
if sortie_at plan-bench~1; then echo "ok: and Sortie stays where it is"; else echo "FAIL: Sortie moved while busy"; failed=1; fi
run; check "a plan-bench merge runs stage0.sh --update-sortie when idle" main "stage0.sh --update-sortie;"
setup; merge dev/stage0/units; smerge; run; check "a kit and a plan-bench merge run stage0.sh once" main "stage0.sh --update-sortie;"
setup; smerge; FAIL_STAGE0=1 run; check "a failed update runs once" main "stage0.sh --update-sortie;"
if sortie_at plan-bench~1; then echo "ok: and puts Sortie back"; else echo "FAIL: Sortie not put back"; failed=1; fi
rm -f "$tmp/calls"; run; check "and does not try that plan-bench commit again" main ""
smerge; run; check "until another one merges" main "stage0.sh --update-sortie;"
setup; echo 2 > "$tmp/sortie/go"; g -C "$tmp/sortie" commit -qam try; smerge; run
check "a Sortie checkout not behind plan-bench stays where it is" main ""

# One run at a time: deploy.sh waits for a stage0.sh, and stage0.sh for it.
setup; merge dev/stage0/units; exec 8> "$STAGE0_HOME/stage0.lock"; flock 8
run; check "a kit change waits while a stage0.sh holds the lock" main~1 ""
rc=0; timeout 3 env -u STAGE0_LOCKED "$KIT/stage0.sh" > "$tmp/out" 2>&1 || rc=$?
if (( rc == 124 )) && grep -q 'Waiting for another stage0.sh' "$tmp/out"; then echo "ok: stage0.sh waits for the lock"
else echo "FAIL: stage0.sh with the lock held: exit $rc, $(cat "$tmp/out")"; failed=1; fi
exec 8>&-
run; check "and runs once the lock is free" main "stage0.sh;"

exit $failed
