#!/usr/bin/env bash
# shellcheck disable=SC2015 # each check is "test && echo ok || fail", and echo always succeeds
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
for tool in uv npm; do printf '#!/bin/sh\necho "%s $*" >> "%s/calls"\n' "$tool" "$tmp" > "$tmp/bin/$tool"; done
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\necho "npx $*" >> "%s/calls"\n[ -z "$FAIL_BUILD" ]\n' "$tmp" > "$tmp/bin/npx"
# systemctl: every call but is-active is logged; a restart of the unit
# FAIL_RESTART names fails.
cat > "$tmp/bin/systemctl" <<EOF
#!/bin/sh
[ "\$2" = is-active ] && exit 0
echo "systemctl \$*" >> "$tmp/calls"
# Sortie's restart: the stubbed Sortie started at SORTIE_RESTART_AT.
[ "\$2 \$3" != "restart ortus-stage0-sortie.service" ] || [ -z "\$SORTIE_RESTART_AT" ] || echo "\$SORTIE_RESTART_AT" > "\$SORTIE_STUB/started_at"
[ "\$2" != restart ] || [ -z "\$FAIL_RESTART" ] || [ "\$3" != "\$FAIL_RESTART" ]
EOF
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
# running. The state names when Sortie started (started_at, which a
# restart sets to SORTIE_RESTART_AT) and, with a jobs file of
# "IDENTIFIER STARTED_AT" lines, each running job and when it started;
# it has polled since it started unless unpolled; with down, Sortie does
# not answer.
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
    [[ ! -e $s/down ]] || exit 7
    dispatch
    paused=false; [[ -e $s/paused ]] && paused=true
    running=$(seq -s, 1 "$(n running)")
    if [[ -e $s/jobs ]]; then
      running=$(awk '{ printf "%s{\"issue_identifier\": \"%s\", \"started_at\": \"%s\"}", (NR > 1 ? ", " : ""), $1, $2 }' "$s/jobs")
    fi
    started=$(cat "$s/started_at" 2> /dev/null || echo 2026-11-05T09:00:00Z) polled=null
    [[ -e $s/unpolled ]] || polled="\"$started\""
    printf '{"started_at": "%s", "running": [%s], "dispatch_paused": %s, "health": {"poll": {"checked_at": %s}}}\n' \
      "$started" "$running" "$paused" "$polled" ;;
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
    "$tmp/failed-once" "$STAGE0_HOME/started" "$STAGE0_HOME/deploy-state.json" "$STAGE0_HOME/sortie.commit" \
    "$STAGE0_HOME/nono" "$STAGE0_HOME/sortie"
  sortie 0
  g init -q --bare "$tmp/remote"
  g clone -q "$tmp/remote" "$tmp/up" 2> /dev/null
  mkdir -p "$tmp/up/dev/stage0" "$tmp/up/workbench/frontend/node_modules" "$tmp/up/docs" "$tmp/up/worker-daemon" "$tmp/up/control-plane"
  # Like the real one, it restarts Sortie on p1, and --update-sortie moves
  # the Sortie checkout to plan-bench.
  # A gate.conf in the kit is an egress gate's configuration: stage0.sh
  # restarts the gate when it changed (unit-restart.sh), failing when the
  # restart does, unless IGNORE_RESTART.
  cat > "$tmp/up/dev/stage0/stage0.sh" <<EOF
#!/usr/bin/env bash
printf stage0.sh >> "$tmp/calls"; for a; do printf " %s" "\$a" >> "$tmp/calls"; done; echo >> "$tmp/calls"
echo "\$STAGE0_PROFILE" > "$tmp/profile"
. "\$STAGE0_HOME/stage0.env"
[ "\$1" != --update-sortie ] || git -C "\$STAGE0_SORTIE_SRC" checkout -q --detach origin/plan-bench
[ -z "\$FAIL_STAGE0" ] || { echo "stage0.sh: the pack did not build" >&2; exit 1; }
if [ -n "\$FAIL_STAGE0_ONCE" ] && [ ! -e "$tmp/failed-once" ]; then : > "$tmp/failed-once"; exit 1; fi
[ "\$STAGE0_PROFILE" = worker ] || echo restart >> "\$SORTIE_STUB/restarts"
. "$KIT/deploy-state.sh"
if [ "\$STAGE0_PROFILE" = worker ]; then deploy_sortie_retired; else deploy_sortie_ran "\$(git -C "\$STAGE0_SORTIE_SRC" rev-parse HEAD)"; fi
. "$KIT/unit-restart.sh"
d=\$(cd "\$(dirname "\$0")" && pwd)
if [ -e "\$d/gate.conf" ]; then restart_changed ortus-stage0-egress-gate.service "\$d/gate.conf" || [ -n "\$IGNORE_RESTART" ] || exit 1; fi
# A kit with nono's profile rolls it out as the real stage0.sh does
# (nono-rollout.sh): the profile staged, Sortie restarted on the rendered
# workflow (rendered at WORKFLOW_AT), the restart recorded, nono narrowed
# if it may be, and both nonos restarted when what they run changed.
if [ "\$STAGE0_PROFILE" != worker ] && [ -e "\$d/../../control-plane/nono/egress-profile.json" ]; then
  . "$KIT/nono-rollout.sh"
  root=\$(cd "\$d/../.." && pwd) wf=\$STAGE0_HOME/sortie/WORKFLOW.md
  mkdir -p "\$STAGE0_HOME/sortie"; cp "\$d/WORKFLOW.md" "\$wf"; touch -d "\${WORKFLOW_AT:-2026-11-05T09:59:58Z}" "\$wf"
  nono_profile_stage "\$root/control-plane/nono/egress-profile.json" "\$root" "\${STAGE0_KIT_FROM:-}" || exit 1
  systemctl --user restart ortus-stage0-sortie.service
  nono_rollout_mark "\$wf"
  nono_narrow "\$wf"
  nono_restart ortus-stage0-nono.service "\$(nono_profile)" || exit 1
  nono_restart ortus-stage0-nono-live-proof.service "\$(nono_profile)" || exit 1
fi
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
merge() { echo "$RANDOM" >> "$tmp/up/$1"; g -C "$tmp/up" add "$1"; g -C "$tmp/up" commit -qm "$1"; g -C "$tmp/up" push -q origin main; }
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

# Each host's deploy state (deploy-state.sh, design D7), which Sortie's
# health check and merge-and-prove read: what each deploy writes.
# shellcheck source=deploy-state.sh
. "$KIT/deploy-state.sh"
sha() { g -C "$tmp/remote" rev-parse "$1"; }
short() { sha "$1" | cut -c1-12; }
field() { deploy_state_field "$STAGE0_HOME/deploy-state.json" "$1"; }
# deployed NAME STATUS COMMIT MAIN BEHIND [ERROR]: the deploy state names
# STATUS, the refs COMMIT and MAIN, behind_since set (behind) or null
# (level), and an error that holds ERROR (none without it).
deployed() {
  local behind=level error
  [[ -n $(field behind_since) ]] && behind=behind
  error=$(field error)
  if [[ $(field status) == "$2" && $(field commit) == "$(sha "$3")" && $(field main) == "$(sha "$4")" && $behind == "$5" \
    && -n $(field updated_at) ]] && { [[ -z ${6:-} && -z $error ]] || [[ -n ${6:-} && $error == *"$6"* ]]; }; then
    echo "ok: $1"
  else echo "FAIL: $1: $(tr '\n' ' ' < "$STAGE0_HOME/deploy-state.json" 2> /dev/null), want $2 at $3, main $4, $5, error '${6:-}'"; failed=1; fi
}
setup; run; deployed "nothing merged: applied at main" succeeded main main level
setup; merge dev/stage0/units; run; deployed "applied: a kit change that ran stage0.sh is applied at the new commit" succeeded main main level
setup; merge docs/x; run; deployed "applied: a docs change too" succeeded main main level
setup; merge workbench/app; run; deployed "applied: a workbench change once the workbench restarted" succeeded main main level
setup; merge workbench/app; FAIL_RESTART=ortus-stage0-workbench.service run
check "a workbench that does not restart puts the previous commit back" main~1 "uv sync -q;npx vite build --logLevel warn;systemctl --user restart ortus-stage0-workbench.service;"
deployed "failed: and records it" failed main~1 main behind "the workbench's restart at $(short main)"
setup; merge dev/stage0/units; FAIL_STAGE0=1 STAGE0_NOW=2026-11-05T10:00:00Z run
deployed "failed: a failed stage0.sh is failed on the previous commit, with its error" failed main~1 main behind \
  "stage0.sh at $(short main): stage0.sh: the pack did not build; stage0.sh on $(short main~1) failed too"
since=$(field behind_since)
[[ $since == 2026-11-05T10:00:00Z ]] && echo "ok: behind: since the failed deploy" || { echo "FAIL: behind_since '$since'"; failed=1; }
STAGE0_NOW=2026-11-05T10:45:00Z run
deployed "behind: the failed commit is not tried again, and stays failed and behind" failed main~1 main behind "the pack did not build"
[[ $(field behind_since) == "$since" && $(field updated_at) == 2026-11-05T10:45:00Z ]] && echo "ok: behind: since it first fell behind, written again each tick" \
  || { echo "FAIL: behind after a tick: $(tr '\n' ' ' < "$STAGE0_HOME/deploy-state.json")"; failed=1; }
merge dev/stage0/units; run; deployed "applied: the next merge that applies is applied and level again" succeeded main main level
setup; smerge; FAIL_STAGE0=1 run
deployed "failed: a failed Sortie update is failed too" failed main main level "(Sortie $(g -C "$tmp/sremote" rev-parse plan-bench | cut -c1-12))"
setup; merge dev/stage0/units; exec 8> "$STAGE0_HOME/stage0.lock"; flock 8; run; exec 8>&-
[[ ! -e $STAGE0_HOME/deploy-state.json ]] && echo "ok: a deploy that waits for the lock writes nothing" || { echo "FAIL: written under a held lock"; failed=1; }
# A deploy that stopped while deploying, as when the host went down.
deploy_state_write deploying "$(sha main~1)" "$(sha main)"; sha main > "$STAGE0_HOME/deploy.failed"; run
deployed "failed: a deploy that stopped while deploying is failed at the next tick" failed main~1 main behind "stopped before it finished"

# Each host's deploy state lists every repository the host deploys (design
# D13), keyed by owner/repo: the kit at the main commit it runs, and on a
# host that builds and runs a Sortie (p1), Sortie at the plan-bench commit
# of the binary it runs.
ssha() { g -C "$tmp/sremote" rev-parse "$1"; }
repos() { jq -c '.repositories' "${1:-$STAGE0_HOME/deploy-state.json}" 2> /dev/null; }
kit_repo() { printf '"ortusnova/agentic-orchestration":{"branch":"main","commit":"%s"}' "$(sha "$1")"; }
sortie_repo() { printf '"ortusnova/sortie":{"branch":"plan-bench","commit":"%s"}' "$1"; }
lists() { # lists NAME WANT [FILE]: the file's repositories are {WANT}
  if [[ $(repos "${3:-}") == "{$2}" ]]; then echo "ok: $1"
  else echo "FAIL: $1: repositories $(repos "${3:-}"), want {$2}"; failed=1; fi
}
setup; merge dev/stage0/units; smerge; run
deployed "repositories: p1's deploy keeps status, commit and main" succeeded main main level
lists "repositories: p1 lists the kit at main and Sortie at the plan-bench it built" "$(kit_repo main),$(sortie_repo "$(ssha plan-bench)")"
smerge; FAIL_STAGE0=1 run
deployed "repositories: a failed Sortie build is failed" failed main main level "(Sortie $(ssha plan-bench | cut -c1-12))"
lists "repositories: and lists Sortie at the commit still running" "$(kit_repo main),$(sortie_repo "$(ssha plan-bench~1)")"
rm -f "$tmp/calls"; run
lists "repositories: and still at the next tick" "$(kit_repo main),$(sortie_repo "$(ssha plan-bench~1)")"
built=$(ssha plan-bench~1)
echo 2 > "$tmp/sortie/go"; g -C "$tmp/sortie" commit -qam try; run
lists "repositories: a Sortie checkout moved since the build lists the Sortie that runs" "$(kit_repo main),$(sortie_repo "$built")"
setup; echo 2 > "$tmp/sortie/go"; g -C "$tmp/sortie" commit -qam try; smerge; run
lists "repositories: before stage0.sh records one, the Sortie checkout's commit, not origin's" \
  "$(kit_repo main),$(sortie_repo "$(g -C "$tmp/sortie" rev-parse HEAD)")"
setup; merge dev/stage0/units; FAIL_STAGE0=1 run
lists "repositories: a failed kit deploy lists the kit commit that runs" "$(kit_repo main~1),$(sortie_repo "$(ssha plan-bench)")"
# A worker host (taichi) builds and runs no Sortie: it lists the kit only,
# whatever Sortie checkout its stage0.env still names and whatever record
# its retired Sortie left.
cp "$STAGE0_HOME/stage0.env" "$tmp/control.env"
printf 'STAGE0_PROFILE=worker\nSTAGE0_SORTIE_SRC=%s\n' "$tmp/sortie" > "$STAGE0_HOME/stage0.env"
setup; ssha plan-bench > "$STAGE0_HOME/sortie.commit"; run
lists "repositories: a worker host lists the kit only" "$(kit_repo main)"
merge dev/stage0/units; smerge; run
lists "repositories: a worker host's deploy lists the kit only" "$(kit_repo main)"
[[ ! -e $STAGE0_HOME/sortie.commit ]] && echo "ok: repositories: and its stage0.sh drops the retired Sortie's record" \
  || { echo "FAIL: a worker host kept $STAGE0_HOME/sortie.commit"; failed=1; }
cp "$tmp/control.env" "$STAGE0_HOME/stage0.env"

# A unit whose configuration changed (ONS-123: a deploy restarted nono,
# Pomerium and the worker but not the egress gate): applied only once it
# restarted.
gate="systemctl --user restart ortus-stage0-egress-gate.service;"
setup; merge dev/stage0/gate.conf; run
check "a running egress gate with no record of what it runs restarts" main "stage0.sh;$gate"
deployed "applied: once it restarted" succeeded main main level
rm -f "$tmp/calls"; merge dev/stage0/units; run
check "a kit change that leaves the gate's configuration as it is leaves it running" main "stage0.sh;"
rm -f "$tmp/calls"; merge dev/stage0/gate.conf; run
check "a change to the gate's configuration restarts it" main "stage0.sh;$gate"
deployed "applied: a unit whose configuration changed, once it restarted" succeeded main main level
rm -f "$tmp/calls"; merge dev/stage0/gate.conf; FAIL_RESTART=ortus-stage0-egress-gate.service run
check "a gate that does not restart puts the previous commit back, whose stage0.sh restarts it again" main~1 "stage0.sh;${gate}stage0.sh;$gate"
deployed "failed: and records which unit" failed main~1 main behind "ortus-stage0-egress-gate.service did not restart"
setup; merge dev/stage0/gate.conf; run; rm -f "$tmp/calls"; merge dev/stage0/gate.conf
FAIL_RESTART=ortus-stage0-egress-gate.service IGNORE_RESTART=1 run
deployed "failed: a stage0.sh that passes while the gate's restart did not is not applied" failed main~1 main behind \
  "stage0.sh's restart of ortus-stage0-egress-gate.service at $(short main)"
merge dev/stage0/gate.conf; run
deployed "applied: once the next merge restarts it" succeeded main main level

# A worker host writes its own; p1 copies it over SSH, from a stubbed
# worker host: ssh runs the command in taichi's home.
taichi=$tmp/taichi/.local/share/ortus-nova/stage0
mkdir -p "$taichi"
cat > "$tmp/bin/ssh" <<STUB
#!/bin/sh
echo "\$*" >> "$tmp/ssh"
while [ "\$1" = -o ]; do shift 2; done
cd "$tmp/\$1" 2> /dev/null || exit 255
shift
exec "\$@"
STUB
chmod +x "$tmp/bin/ssh"
cp "$STAGE0_HOME/stage0.env" "$tmp/control.env"
printf 'STAGE0_PROFILE=worker\n' > "$STAGE0_HOME/stage0.env"
setup; merge dev/stage0/units; run
deployed "a worker host writes its deploy state too" succeeded main main level
cp "$STAGE0_HOME/deploy-state.json" "$taichi/deploy-state.json"
taichi_repos="$(kit_repo main)"
lists "and lists the kit only" "$taichi_repos" "$taichi/deploy-state.json"
[[ ! -e $tmp/ssh ]] && echo "ok: and reads no other host's" || { echo "FAIL: a worker host ran ssh $(cat "$tmp/ssh")"; failed=1; }
cp "$tmp/control.env" "$STAGE0_HOME/stage0.env"
setup; deploy_host_add taichi taichi; run
copy=$STAGE0_HOME/hosts/taichi/deploy-state.json
if cmp -s "$copy" "$taichi/deploy-state.json" && grep -q 'BatchMode=yes' "$tmp/ssh"; then
  echo "ok: p1 copies the worker host's deploy state over ssh at each tick"
else echo "FAIL: p1's copy of taichi's: $(cat "$copy" 2> /dev/null), ssh: $(cat "$tmp/ssh" 2> /dev/null)"; failed=1; fi
lists "and p1's copy keeps taichi's repositories" "$taichi_repos" "$copy"
lists "beside p1's own, which lists both" "$(kit_repo main),$(sortie_repo "$(ssha plan-bench)")"
[[ $(deploy_hosts_yaml) == "    p1: $STAGE0_HOME/deploy-state.json"$'\n'"    taichi: $copy" ]] \
  && echo "ok: and Sortie's workflow names both hosts' files (merge_and_prove.hosts)" || { echo "FAIL: hosts: $(deploy_hosts_yaml)"; failed=1; }
# shellcheck disable=SC2016 # literal placeholders
grep -qx '  deploy_state: ${STAGE0_HOME}/deploy-state.json' "$KIT/WORKFLOW.md" && grep -qx '${STAGE0_DEPLOY_HOSTS}' "$KIT/WORKFLOW.md" \
  && echo "ok: the workflow reads p1's as health.deploy_state and every host's for merge-and-prove" || { echo "FAIL: WORKFLOW.md's deploy state"; failed=1; }
mv "$tmp/taichi" "$tmp/taichi.away"; merge docs/x; run
if cmp -s "$copy" "$tmp/taichi.away/.local/share/ortus-nova/stage0/deploy-state.json" && grep -q "could not read taichi's deploy state" "$tmp/err"; then
  echo "ok: a worker host p1 cannot reach keeps its last copy, and deploy says so"
else echo "FAIL: unreachable taichi: $(cat "$tmp/err")"; failed=1; fi
deployed "and p1's own deploy is applied anyway" succeeded main main level
mv "$tmp/taichi.away" "$tmp/taichi"
deploy_host_remove taichi; rm -f "$tmp/ssh"

# A rollout that moves hosts from nono to the offer's policy (spec store
# agent-platform/supervisor-autonomy, "A rollout narrows egress only after
# the offers carry the new hosts"; nono-rollout.sh): the kit before allows
# the harness hosts in nono's profile, the merge takes them out and offers
# them in WORKFLOW.md. Sortie restarts on that workflow first; nono keeps
# the full list until the state API shows a Sortie started on it, polled
# since, and no running job started before it.
harness=(models.opencode.ai registry.npmjs.org codeload.github.com '*.githubusercontent.com')
nono_up="systemctl --user restart ortus-stage0-nono.service;systemctl --user restart ortus-stage0-nono-live-proof.service;"
sortie_up="systemctl --user restart ortus-stage0-sortie.service;"
force=$STAGE0_HOME/nono/egress-profile.json
# rollout_setup: the kit before, live and run once, so nono runs its profile.
rollout_setup() {
  setup
  mkdir -p "$tmp/up/control-plane/nono"
  python3 - "$KIT/../../control-plane/nono/egress-profile.json" "${harness[@]}" > "$tmp/up/control-plane/nono/egress-profile.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
p["network"]["allow_domain"] += sys.argv[2:]
print(json.dumps(p, indent=2))
PY
  echo "egress: none" > "$tmp/up/dev/stage0/WORKFLOW.md"
  g -C "$tmp/up" add -A; g -C "$tmp/up" commit -qm before; g -C "$tmp/up" push -q origin main
  g -C "$STAGE0_LIVE_KIT" pull -q
  STAGE0_KIT_FROM='' SORTIE_RESTART_AT=2026-11-05T09:00:00Z WORKFLOW_AT=2026-11-05T08:59:00Z "$tmp/live/dev/stage0/stage0.sh" 2> /dev/null
  rm -f "$tmp/calls" "$SORTIE_STUB/requests"
}
# rollout_merge: the merge that cuts nono to the credential hosts.
rollout_merge() {
  cp "$KIT/../../control-plane/nono/egress-profile.json" "$tmp/up/control-plane/nono/egress-profile.json"
  printf 'egress:\n%s\n' "$(printf '  - %s\n' "${harness[@]}")" > "$tmp/up/dev/stage0/WORKFLOW.md"
  g -C "$tmp/up" commit -qam narrow; g -C "$tmp/up" push -q origin main
}
# allows NAME HOSTS...: the profile in force allows exactly HOSTS.
allows() {
  local got want
  got=$(python3 -c 'import json,sys; print(" ".join(sorted(json.load(open(sys.argv[1]))["network"]["allow_domain"])))' "$force")
  want=$(printf '%s\n' "${@:2}" | LC_ALL=C sort | paste -sd' ')
  got=$(tr ' ' '\n' <<< "$got" | LC_ALL=C sort | paste -sd' ')
  if [[ $got == "$want" ]]; then echo "ok: $1"; else echo "FAIL: $1: nono allows '$got', want '$want'"; failed=1; fi
}
credential_hosts=(api.anthropic.com api.github.com)
full=("${credential_hosts[@]}" "${harness[@]}")
tick() { rm -f "$tmp/calls"; run; }
export SORTIE_RESTART_AT=2026-11-05T10:00:00Z

# The ONS-122 case: dispatched 4 s before Sortie restarted on the workflow
# that offers the harness hosts, so its offer has none of them.
rollout_setup; allows "rollout: before the merge nono runs the full list" "${full[@]}"
echo "ONS-122 2026-11-05T09:59:56Z" > "$SORTIE_STUB/jobs"
rollout_merge; run
check "rollout: the merge restarts Sortie on the new workflow, and leaves nono as it is" main "stage0.sh;$sortie_up"
allows "rollout: nono keeps the harness hosts while ONS-122, dispatched 4 s before the restart, runs" "${full[@]}"
grep -q 'ONS-122, started 2026-11-05T09:59:56Z holds an offer from before' "$tmp/err" \
  && echo "ok: rollout: and says which job it waits for" || { echo "FAIL: rollout: $(cat "$tmp/err")"; failed=1; }
deployed "rollout: the deploy is applied while nono waits" succeeded main main level
echo "ONS-122 2026-11-05T09:59:56Z" > "$SORTIE_STUB/jobs"; echo "ONS-140 2026-11-05T10:00:30Z" >> "$SORTIE_STUB/jobs"
tick; check "rollout: a tick while ONS-122 runs, beside a job dispatched after the restart, restarts nothing" main ""
allows "rollout: and nono keeps the full list" "${full[@]}"
echo "ONS-140 2026-11-05T10:00:30Z" > "$SORTIE_STUB/jobs"
tick; check "rollout: the first tick after ONS-122 ends restarts both nonos" main "$nono_up"
allows "rollout: onto the credential hosts alone" "${credential_hosts[@]}"
if cmp -s "$force" "$tmp/up/control-plane/nono/egress-profile.json" && [[ ! -e $STAGE0_HOME/nono/egress-profile.next.json ]]; then
  echo "ok: rollout: the kit's profile is in force and nothing waits"
else echo "FAIL: rollout: the profile in force is not the kit's"; failed=1; fi
rm -f "$SORTIE_STUB/requests"; tick; check "rollout: the next tick restarts nothing" main ""
[[ ! -s $SORTIE_STUB/requests ]] && echo "ok: rollout: and asks Sortie nothing" || { echo "FAIL: rollout: asked $(cat "$SORTIE_STUB/requests")"; failed=1; }
rm -f "$tmp/calls"; "$tmp/live/dev/stage0/stage0.sh" 2> /dev/null
check "rollout: a second stage0.sh changes nothing of nono" main "stage0.sh;$sortie_up"
allows "rollout: and nono still serves the credential hosts alone" "${credential_hosts[@]}"

# No job running at the restart: nono narrows in the same deploy, after
# Sortie's restart.
rollout_setup; rollout_merge; run
check "rollout: with no job running, Sortie restarts first, then both nonos" main "stage0.sh;$sortie_up$nono_up"
allows "rollout: onto the credential hosts" "${credential_hosts[@]}"

# Sortie that has not restarted on the workflow: one started before it was
# rendered, one that does not answer, one that has not polled since its
# start and so may not have re-adopted its runs yet.
rollout_setup; rollout_merge; SORTIE_RESTART_AT=2026-11-05T09:30:00Z run
allows "rollout: a Sortie started before the workflow was rendered keeps nono's full list" "${full[@]}"
tick; allows "rollout: at each tick" "${full[@]}"
rollout_setup; touch "$SORTIE_STUB/down"; rollout_merge; run
allows "rollout: a Sortie that does not answer keeps nono's full list" "${full[@]}"
rm "$SORTIE_STUB/down"; tick; check "rollout: and once it answers, with no job, nono narrows" main "$nono_up"
allows "rollout: to the credential hosts" "${credential_hosts[@]}"
rollout_setup; touch "$SORTIE_STUB/unpolled"; rollout_merge; run
allows "rollout: a Sortie that has not polled since its start keeps nono's full list" "${full[@]}"
grep -q 'has not polled since it started' "$tmp/err" && echo "ok: rollout: and says so" || { echo "FAIL: rollout unpolled: $(cat "$tmp/err")"; failed=1; }
rm "$SORTIE_STUB/unpolled"; tick; allows "rollout: until it has" "${credential_hosts[@]}"

# A change that only adds hosts to nono is in force at once.
rollout_setup; python3 - "$tmp/up/control-plane/nono/egress-profile.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1])); p["network"]["allow_domain"].append("example.org")
json.dump(p, open(sys.argv[1], "w"), indent=2)
PY
g -C "$tmp/up" commit -qam add; g -C "$tmp/up" push -q origin main
echo "ONS-122 2026-11-05T09:59:56Z" > "$SORTIE_STUB/jobs"; run
allows "rollout: a change that adds a host is in force at once, whatever runs" "${full[@]}" example.org
check "rollout: and restarts nono with it" main "stage0.sh;$sortie_up$nono_up"
rm -f "$SORTIE_STUB/jobs"
unset SORTIE_RESTART_AT

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
setup; run
mkdir -p "$tmp/dkit"
cp "$KIT/dev-host-apply.sh" "$KIT/deploy-state.sh" "$KIT/unit-restart.sh" "$tmp/dkit/"
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
deployed "the dev-host apply writes its deploy state: applied" succeeded main main level
lists "with the repositories p1 runs" "$(kit_repo main),$(sortie_repo "$(ssha plan-bench)")"
sortie 1 4 1; apply false
applied "a failed apply stops before stage0.sh" 1 ""
never_drained "and drains nothing"
deployed "a failed apply is failed, with its error" failed main main level "the dev-host apply failed: false failed"
sortie 0; FAIL_STAGE0=1 apply true; applied "a failed stage0.sh after the apply fails it" 1 "stage0.sh;"
deployed "and is failed" failed main main level "stage0.sh failed after the apply"
mkdir -p "$STAGE0_HOME/started"; echo restarting > "$STAGE0_HOME/started/ortus-stage0-egress-gate.service.sum"
sortie 0; apply true; applied "an apply whose unit did not restart fails" 1 "stage0.sh;"
deployed "and is failed, naming the unit" failed main main level "restart of ortus-stage0-egress-gate.service"
rm -rf "$STAGE0_HOME/started"
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
