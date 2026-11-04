#!/usr/bin/env bash
# Checks deploy.sh and dev-host-apply.sh against a throwaway kit repository
# and Sortie checkout, with uv, npm, npx and systemctl stubbed and Sortie
# stubbed behind curl: what each kind of merge does to the live kit, that a
# deploy never drains (spec store agent-platform/run-supervision: it
# restarts Sortie at once, pauses no dispatch and waits for no run), and
# that both run under stage0.sh's lock.
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
# A stubbed Sortie, as curl: its state is files in $SORTIE_STUB (running,
# queued and max, counts; paused while dispatch is paused, as an earlier kit
# could leave it). Like Sortie, it dispatches queued stories into free slots
# whenever it is asked anything, unless paused. Every request is logged to
# requests, and a pause or resume, which needs the control token, to log.
# lost, a count, is the runs whose worker stopped while Sortie restarted:
# once their leases expire (the next request), Sortie puts them back in the
# queue as retries, logged to retries, and dispatches them like any story.
# stage0.sh's stub logs each restart of Sortie to restarts; as Sortie
# re-adopts the runs its workers hold, a restart leaves the running runs
# running.
cat > "$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
s=$SORTIE_STUB method=GET url='' auth=''
while (( $# )); do
  case $1 in
    -X) method=$2; shift ;;
    -H) if [[ $2 == @- ]]; then auth=$(cat); else auth=$2; fi; shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
path=/${url#http://*/}
echo "$method $path" >> "$s/requests"
n() { cat "$s/$1" 2> /dev/null || echo 0; }
dispatch() {
  local r q m; r=$(n running) q=$(n queued) m=$(n max)
  while [[ ! -e $s/paused ]] && (( r < m && q > 0 )); do r=$((r + 1)) q=$((q - 1)); done
  echo "$r" > "$s/running"; echo "$q" > "$s/queued"
}
case "$method $path" in
  "GET /api/v1/state")
    if [[ -e $s/lost ]]; then
      echo $(( $(n running) - $(n lost) )) > "$s/running"; echo $(( $(n queued) + $(n lost) )) > "$s/queued"
      echo "$(n lost)" >> "$s/retries"; rm -f "$s/lost"
    fi
    dispatch
    paused=false; [[ -e $s/paused ]] && paused=true
    printf '{"running": [%s], "dispatch_paused": %s}\n' "$(seq -s, 1 "$(n running)")" "$paused" ;;
  "POST /api/v1/dispatch/pause"|"POST /api/v1/dispatch/resume")
    [[ $auth == "Authorization: Bearer control" ]] || exit 22
    echo "${path##*/}" >> "$s/log"
    if [[ $path == */pause ]]; then touch "$s/paused"; else rm -f "$s/paused"; dispatch; fi
    echo '{}' ;;
  *) exit 22 ;;
esac
EOF
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH FAIL_BUILD='' FAIL_STAGE0='' SORTIE_STUB=$tmp/s
unset STAGE0_LOCKED
printf 'STAGE0_PROFILE=control\nSTAGE0_SORTIE_PORT=1\nSTAGE0_SORTIE_SRC=%s\n' "$tmp/sortie" > "$STAGE0_HOME/stage0.env"
echo control > "$STAGE0_HOME/sortie-token"
# sortie RUNNING [QUEUED [MAX]]: a fresh stubbed Sortie, dispatching.
sortie() { rm -rf "$SORTIE_STUB"; mkdir -p "$SORTIE_STUB"; echo "$1" > "$SORTIE_STUB/running"; echo "${2:-0}" > "$SORTIE_STUB/queued"; echo "${3:-3}" > "$SORTIE_STUB/max"; }
finish() { echo $(( $(cat "$SORTIE_STUB/running") - $1 )) > "$SORTIE_STUB/running"; curl -sf http://127.0.0.1:1/api/v1/state > /dev/null; }
stub() { echo "running $(cat "$SORTIE_STUB/running"), queued $(cat "$SORTIE_STUB/queued"), $([[ -e $SORTIE_STUB/paused ]] && echo paused || echo dispatching)"; }
want() { # want NAME STUB_STATE
  if [[ $(stub) == "$2" ]]; then echo "ok: $1"; else echo "FAIL: $1: Sortie $(stub), want $2"; failed=1; fi
}
# never_drained NAME: deploy asked Sortie for no pause and no state, and Sortie never paused.
never_drained() {
  if [[ ! -s $SORTIE_STUB/log && ! -e $SORTIE_STUB/paused ]] && ! grep -q 'state\|pause' "$SORTIE_STUB/requests" 2> /dev/null; then echo "ok: $1"
  else echo "FAIL: $1: Sortie asked $(tr '\n' ';' < "$SORTIE_STUB/requests" 2> /dev/null)"; failed=1; fi
}
restarts() { # restarts NAME N: stage0.sh restarted Sortie N times
  local got; got=$(wc -l < "$SORTIE_STUB/restarts" 2> /dev/null || echo 0)
  if (( got == $2 )); then echo "ok: $1"; else echo "FAIL: $1: Sortie restarted $got time(s), want $2"; failed=1; fi
}

setup() {
  rm -rf "$tmp/remote" "$tmp/up" "$STAGE0_LIVE_KIT" "$tmp/calls" "$STAGE0_HOME/deploy.failed" "$STAGE0_HOME/deploy-workbench.at" \
    "$tmp/sremote" "$tmp/sup" "$tmp/sortie" "$STAGE0_HOME/deploy-sortie.failed" "$STAGE0_HOME/dispatch.paused" "$tmp/profile" \
    "$tmp/failed-once"
  sortie 0
  g init -q --bare "$tmp/remote"
  g clone -q "$tmp/remote" "$tmp/up" 2> /dev/null
  mkdir -p "$tmp/up/dev/stage0" "$tmp/up/workbench/frontend/node_modules" "$tmp/up/docs" "$tmp/up/worker-daemon" "$tmp/up/control-plane"
  # Like the real one, it restarts Sortie on p1, and --update-sortie moves
  # the Sortie checkout to plan-bench.
  cat > "$tmp/up/dev/stage0/stage0.sh" <<EOF
#!/bin/sh
printf stage0.sh >> "$tmp/calls"; for a; do printf " %s" "\$a" >> "$tmp/calls"; done; echo >> "$tmp/calls"
echo "\$STAGE0_PROFILE" > "$tmp/profile"
. "\$STAGE0_HOME/stage0.env"
[ "\$1" != --update-sortie ] || git -C "\$STAGE0_SORTIE_SRC" checkout -q --detach origin/plan-bench
[ -z "\$FAIL_STAGE0" ] || exit 1
if [ -n "\$FAIL_STAGE0_ONCE" ] && [ ! -e "$tmp/failed-once" ]; then : > "$tmp/failed-once"; exit 1; fi
[ "\$STAGE0_PROFILE" = worker ] || echo restart >> "\$SORTIE_STUB/restarts"
EOF
  chmod +x "$tmp/up/dev/stage0/stage0.sh"
  echo 1 > "$tmp/up/workbench/app"; echo 1 > "$tmp/up/docs/x"; echo 1 > "$tmp/up/dev/stage0/units"; echo 1 > "$tmp/up/worker-daemon/x"
  echo 1 > "$tmp/up/dev/stage0/supervisor.md"; echo 1 > "$tmp/up/control-plane/x"
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
# The supervisor's prompt is the kit's: stage0.sh installs it on the host.
setup; merge dev/stage0/supervisor.md; run; check "a change to the supervisor's prompt runs stage0.sh, which installs it" main "stage0.sh;"
setup; merge dev/stage0/units; run; check "a kit change runs stage0.sh" main "stage0.sh;"
# p1 runs the control plane's egress gate and Pomerium image and nono profile.
setup; merge control-plane/x; run; check "a control-plane change runs stage0.sh" main "stage0.sh;"
setup; echo "$RANDOM" >> "$tmp/up/workbench/app"; echo "$RANDOM" >> "$tmp/up/dev/stage0/units"
g -C "$tmp/up" commit -qam both; g -C "$tmp/up" push -q origin main; sortie 1; run
check "a kit and workbench merge runs stage0.sh, which builds the workbench too" main "stage0.sh;"
if [[ -z $(g -C "$STAGE0_LIVE_KIT" status --porcelain) ]]; then echo "ok: and leaves the live kit clean"; else echo "FAIL: live kit not clean: $(g -C "$STAGE0_LIVE_KIT" status --porcelain)"; failed=1; fi
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

# Sortie follows plan-bench.
setup; smerge; run; check "a plan-bench merge runs stage0.sh --update-sortie" main "stage0.sh --update-sortie;"
if sortie_at plan-bench; then echo "ok: and moves Sortie to plan-bench"; else echo "FAIL: Sortie not on plan-bench"; failed=1; fi
setup; merge dev/stage0/units; smerge; run; check "a kit and a plan-bench merge run stage0.sh once" main "stage0.sh --update-sortie;"
setup; merge dev/stage0/units; FAIL_STAGE0_ONCE=1 run
check "a kit change whose stage0.sh fails runs the previous commit's stage0.sh" main~1 "stage0.sh;stage0.sh;"
if grep -q 'running stage0.sh on .* again' "$tmp/err" && ! grep -q 'failed too' "$tmp/err"; then echo "ok: and says so"; else echo "FAIL: re-apply: $(cat "$tmp/err")"; failed=1; fi
rm -f "$tmp/calls"; run; check "and does not try the failed commit again" main~1 ""
setup; smerge; FAIL_STAGE0=1 run; check "a failed update runs once" main "stage0.sh --update-sortie;"
if sortie_at plan-bench~1; then echo "ok: and puts Sortie back"; else echo "FAIL: Sortie not put back"; failed=1; fi
rm -f "$tmp/calls"; run; check "and does not try that plan-bench commit again" main ""
smerge; run; check "until another one merges" main "stage0.sh --update-sortie;"
setup; echo 2 > "$tmp/sortie/go"; g -C "$tmp/sortie" commit -qam try; smerge; run
check "a Sortie checkout not behind plan-bench stays where it is" main ""

# A deploy during a long run (agent-platform/run-supervision): ONS-104's
# agent is in the middle of a turn, 2 stories wait, 3 slots. The kit merge
# restarts Sortie at once; the run goes on, and no story waits for the deploy.
setup; sortie 1 2 3; merge dev/stage0/units; run
check "a long run: a kit merge runs stage0.sh at once" main "stage0.sh;"
restarts "a long run: which restarts Sortie" 1
never_drained "a long run: deploy pauses no dispatch and waits for no run"
finish 0; want "a long run: the run goes on and the 2 waiting are dispatched" "running 3, queued 0, dispatching"
setup; sortie 1 2 3; smerge; run
check "a long run: a plan-bench merge applies at once too" main "stage0.sh --update-sortie;"
restarts "a long run: and restarts Sortie" 1
never_drained "a long run: with no drain"
finish 0; want "a long run: and the run goes on" "running 3, queued 0, dispatching"
# A busy p1, 2 stories running and 9 waiting, 2 at a time: a kit merge
# neither waits for the 2 nor holds back the 9.
setup; sortie 2 9 2; merge dev/stage0/units; run
check "busy p1: a kit merge applies at once" main "stage0.sh;"
never_drained "busy p1: with no drain"
want "busy p1: both runs go on" "running 2, queued 9, dispatching"
finish 1; want "busy p1: a run ends and the next is dispatched" "running 2, queued 8, dispatching"
# A worker gone during the restart (agent-platform/run-supervision): of 2
# runs, one's worker stops while the deploy restarts Sortie. Its lease
# expires and it is retried at once, as for any lost lease: no pause holds
# the retry back; the other run goes on.
setup; sortie 2 0 2; merge dev/stage0/units; run; echo 1 > "$SORTIE_STUB/lost"
check "a worker gone: the kit merge applies at once" main "stage0.sh;"
never_drained "a worker gone: with no drain"
finish 0
if [[ $(cat "$SORTIE_STUB/retries" 2> /dev/null) == 1 ]]; then echo "ok: a worker gone: its run is retried"; else echo "FAIL: a worker gone: retries '$(cat "$SORTIE_STUB/retries" 2> /dev/null)'"; failed=1; fi
want "a worker gone: and dispatched again beside the run that went on" "running 2, queued 0, dispatching"
setup; sortie 1 4 1; merge dev/stage0/units; FAIL_STAGE0=1 run
check "a failed apply during a run goes back, and runs the previous stage0.sh again" main~1 "stage0.sh;stage0.sh;"
never_drained "and drains nothing either"
# An earlier kit paused dispatch while a change waited for a drain: the
# first deploy of this kit dispatches again.
setup; sortie 0 4 1; echo "deploy of kit 0" > "$STAGE0_HOME/dispatch.paused"; touch "$SORTIE_STUB/paused"; run
want "a pause an earlier kit left is resumed" "running 1, queued 3, dispatching"
if [[ ! -e $STAGE0_HOME/dispatch.paused && $(cat "$SORTIE_STUB/log") == resume ]]; then echo "ok: and forgotten"; else echo "FAIL: left pause: $(cat "$SORTIE_STUB/log" 2> /dev/null)"; failed=1; fi
rm -f "$SORTIE_STUB/log"; run; never_drained "and asked of Sortie only once"
setup; sortie 0 4 1; echo "deploy of kit 0" > "$STAGE0_HOME/dispatch.paused"; touch "$SORTIE_STUB/paused"; merge dev/stage0/units; run
want "a pause an earlier kit left is resumed after the apply too" "running 1, queued 3, dispatching"
setup; sortie 0 4 1; echo "deploy of kit 0" > "$STAGE0_HOME/dispatch.paused"; touch "$SORTIE_STUB/paused"; echo wrong > "$STAGE0_HOME/sortie-token"; run
if [[ -e $STAGE0_HOME/dispatch.paused ]] && grep -q 'did not resume' "$tmp/err"; then echo "ok: a refused resume is kept for the next minute"; else echo "FAIL: refused resume: $(cat "$tmp/err")"; failed=1; fi
echo control > "$STAGE0_HOME/sortie-token"

# A worker host: a kit change applies at once while its daemon runs a job,
# and it builds no Sortie. Its stage0.env is taichi's after the move from
# the retired homelab profile: it still names a Sortie checkout and port and
# p1's Huly through a tunnel, and a pause that Sortie left is still
# recorded. No Sortie runs there, so deploy asks none anything, and follows
# no plan-bench merge.
cp "$STAGE0_HOME/stage0.env" "$tmp/control.env"
printf 'STAGE0_PROFILE=worker\nSTAGE0_SORTIE_SRC=%s\nSTAGE0_SORTIE_PORT=1\nSTAGE0_HULY_URL=http://127.0.0.1:8087\n' "$tmp/sortie" > "$STAGE0_HOME/stage0.env"
no_sortie_asked() { # no_sortie_asked NAME
  if [[ ! -s $SORTIE_STUB/requests ]]; then echo "ok: $1"; else echo "FAIL: $1: asked Sortie $(tr '\n' ';' < "$SORTIE_STUB/requests")"; failed=1; fi
}
profile_was() { # profile_was NAME PROFILE: the profile the last stage0.sh ran with
  if [[ $(cat "$tmp/profile" 2> /dev/null) == "$2" ]]; then echo "ok: $1"; else echo "FAIL: $1: stage0.sh ran with STAGE0_PROFILE='$(cat "$tmp/profile" 2> /dev/null)', want $2"; failed=1; fi
}
setup; merge dev/stage0/units; mkdir -p "$STAGE0_HOME/worker/jobs/ONS-9-1"; run
check "on a worker host a kit change runs stage0.sh at once while a job runs" main "stage0.sh;"
if [[ -d $STAGE0_HOME/worker/jobs/ONS-9-1 ]]; then echo "ok: and leaves the job alone"; else echo "FAIL: the job is gone"; failed=1; fi
rmdir "$STAGE0_HOME/worker/jobs/ONS-9-1"
profile_was "and stage0.sh runs the worker profile" worker
no_sortie_asked "and deploy asks no Sortie"
setup; merge worker-daemon/x; run; check "a worker-daemon change runs stage0.sh" main "stage0.sh;"
profile_was "with the worker profile" worker
setup; smerge; echo "deploy of Sortie 0" > "$STAGE0_HOME/dispatch.paused"; run
check "a worker host follows no plan-bench merge" main ""
if sortie_at plan-bench~1; then echo "ok: and leaves the Sortie checkout where it is"; else echo "FAIL: a worker host moved the Sortie checkout"; failed=1; fi
no_sortie_asked "and asks no Sortie, not even to resume a pause it left"
setup; smerge; merge dev/stage0/units; run
check "a kit and a plan-bench merge on a worker host run stage0.sh without --update-sortie" main "stage0.sh;"
profile_was "with the worker profile" worker
no_sortie_asked "and ask no Sortie"
if [[ ! -e $STAGE0_HOME/deploy-sortie.failed ]] && grep -q 'running ' "$tmp/err" && ! grep -qi 'sortie' "$tmp/err"; then echo "ok: and deploy names no Sortie"
else echo "FAIL: a worker host's deploy: $(cat "$tmp/err")"; failed=1; fi
cp "$tmp/control.env" "$STAGE0_HOME/stage0.env"

# One run at a time: deploy.sh waits for a stage0.sh, and stage0.sh for it.
setup; merge dev/stage0/units; exec 8> "$STAGE0_HOME/stage0.lock"; flock 8
run; check "a kit change waits while a stage0.sh holds the lock" main~1 ""
rc=0; timeout 3 env -u STAGE0_LOCKED "$KIT/stage0.sh" > "$tmp/out" 2>&1 || rc=$?
if (( rc == 124 )) && grep -q 'Waiting for another stage0.sh' "$tmp/out"; then echo "ok: stage0.sh waits for the lock"
else echo "FAIL: stage0.sh with the lock held: exit $rc, $(cat "$tmp/out")"; failed=1; fi
exec 8>&-
run; check "and runs once the lock is free" main "stage0.sh;"

# The dev-host apply after a smolvm bump applies at once too, whatever runs,
# then runs stage0.sh so the pack and Sortie follow the new smolvm.
mkdir -p "$tmp/dkit"
cp "$KIT/dev-host-apply.sh" "$tmp/dkit/"
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\necho stage0.sh >> "%s/calls"\n[ -z "$FAIL_STAGE0" ]\n' "$tmp" > "$tmp/dkit/stage0.sh"
chmod +x "$tmp/dkit/stage0.sh"
# shellcheck disable=SC2016 # expanded by the apply, not here
apply=(sh -c 'echo "apply: running $(cat "$SORTIE_STUB/running"), $([ -e "$SORTIE_STUB/paused" ] && echo paused || echo dispatching)" >> "$0/calls"' "$tmp")
apply() { rm -f "$tmp/calls"; rc=0; "$tmp/dkit/dev-host-apply.sh" "$@" > /dev/null 2> "$tmp/err" || rc=$?; }
calls() { if [[ -f $tmp/calls ]]; then tr '\n' ';' < "$tmp/calls"; fi; }
applied() { # applied NAME WANT_RC WANT_CALLS
  if [[ $rc == "$2" && $(calls) == "$3" ]]; then echo "ok: $1"; else echo "FAIL: $1: exit $rc, calls '$(calls)', want $2 and '$3': $(cat "$tmp/err")"; failed=1; fi
}
sortie 2 9 2; apply "${apply[@]}"
applied "a smolvm bump on busy p1 applies at once, with its 2 runs going" 0 "apply: running 2, dispatching;stage0.sh;"
never_drained "and pauses no dispatch nor waits for a run"
sortie 1 4 1; apply false
applied "a failed apply stops before stage0.sh" 1 ""
never_drained "and drains nothing"
sortie 0; FAIL_STAGE0=1 apply true; applied "a failed stage0.sh after the apply fails it" 1 "stage0.sh;"
sortie 0; apply; applied "the control profile has no default apply" 1 ""
# A worker host (taichi): the apply is make dev-host by default, and runs at
# once while the worker daemon runs a job, the daemon left up.
mkdir -p "$tmp/homelab"; printf 'dev-host:\n\techo make dev-host >> %s/calls\n' "$tmp" > "$tmp/homelab/Makefile"
cp "$STAGE0_HOME/stage0.env" "$tmp/env"
printf 'STAGE0_PROFILE=worker\nSTAGE0_HOMELAB_SRC=%s\n' "$tmp/homelab" > "$STAGE0_HOME/stage0.env"
sortie 0; apply; applied "a worker host's apply is make dev-host in its homelab checkout" 0 "make dev-host;stage0.sh;"
no_sortie_asked "and asks no Sortie"
mkdir -p "$STAGE0_HOME/worker/jobs/ONS-9-1"
apply; applied "on a worker host the apply runs at once while a job runs, the daemon left up" 0 "make dev-host;stage0.sh;"
rm -rf "$STAGE0_HOME/worker/jobs/ONS-9-1"
apply false; applied "a failed apply on a worker host stops before stage0.sh" 1 ""
printf 'STAGE0_PROFILE=worker\n' > "$STAGE0_HOME/stage0.env"
apply; applied "a worker host without homelab has no default apply" 1 ""
cp "$tmp/env" "$STAGE0_HOME/stage0.env"
exec 8> "$STAGE0_HOME/stage0.lock"; flock 8
rc=0; timeout 3 "$tmp/dkit/dev-host-apply.sh" true 2> "$tmp/err" || rc=$?
if (( rc == 124 )) && grep -q 'waiting for a stage0.sh or a deploy' "$tmp/err"; then echo "ok: the dev-host apply waits for stage0.sh's lock"
else echo "FAIL: the dev-host apply with the lock held: exit $rc, $(cat "$tmp/err")"; failed=1; fi
exec 8>&-

if grep -rqs 'drain.sh\|dispatch_pause\|dispatch/pause' "$KIT/deploy.sh" "$KIT/dev-host-apply.sh"; then echo "FAIL: the kit still drains"; failed=1
else echo "ok: neither deploy.sh nor the dev-host apply drains"; fi

exit $failed
