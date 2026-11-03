#!/usr/bin/env bash
# Checks deploy.sh and dev-host-apply.sh against a throwaway kit repository
# and Sortie checkout, with uv, npm, npx and systemctl stubbed and Sortie
# stubbed behind curl: what each kind of merge does to the live kit, that a
# change waiting for no running agent drains Sortie (pauses its dispatch,
# applies once the runs going finish, and resumes), and that both run under
# stage0.sh's lock.
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
# queued and max, counts; paused while dispatch is paused). Like Sortie, it
# dispatches queued stories into free slots whenever it is asked anything,
# unless paused. no-pause makes it an older Sortie, without the pause;
# finish-on-poll ends one run at each state request. The pause and resume
# need the control token, and are logged to log.
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
n() { cat "$s/$1" 2> /dev/null || echo 0; }
dispatch() {
  local r q m; r=$(n running) q=$(n queued) m=$(n max)
  while [[ ! -e $s/paused ]] && (( r < m && q > 0 )); do r=$((r + 1)) q=$((q - 1)); done
  echo "$r" > "$s/running"; echo "$q" > "$s/queued"
}
case "$method $path" in
  "GET /api/v1/state")
    if [[ -e $s/finish-on-poll ]] && (( $(n running) > 0 )); then echo $(( $(n running) - 1 )) > "$s/running"; fi
    dispatch
    paused=false; [[ -e $s/paused ]] && paused=true
    printf '{"running": [%s], "dispatch_paused": %s}\n' "$(seq -s, 1 "$(n running)")" "$paused" ;;
  "POST /api/v1/dispatch/pause"|"POST /api/v1/dispatch/resume")
    [[ -e $s/no-pause ]] && exit 22
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

setup() {
  rm -rf "$tmp/remote" "$tmp/up" "$STAGE0_LIVE_KIT" "$tmp/calls" "$STAGE0_HOME/deploy.failed" \
    "$tmp/sremote" "$tmp/sup" "$tmp/sortie" "$STAGE0_HOME/deploy-sortie.failed" "$STAGE0_HOME/dispatch.paused"
  sortie 0
  g init -q --bare "$tmp/remote"
  g clone -q "$tmp/remote" "$tmp/up" 2> /dev/null
  mkdir -p "$tmp/up/dev/stage0" "$tmp/up/workbench/frontend/node_modules" "$tmp/up/docs" "$tmp/up/worker-daemon"
  # Like the real one, --update-sortie moves the Sortie checkout to plan-bench.
  # shellcheck disable=SC2016 # expanded by the stub, not here
  printf '#!/bin/sh\nprintf stage0.sh >> "%s/calls"; for a; do printf " %%s" "$a" >> "%s/calls"; done; echo >> "%s/calls"\n[ "$1" != --update-sortie ] || git -C "$STAGE0_SORTIE_SRC" checkout -q --detach origin/plan-bench\n[ -z "$FAIL_STAGE0" ]\n' \
    "$tmp" "$tmp" "$tmp" > "$tmp/up/dev/stage0/stage0.sh"
  chmod +x "$tmp/up/dev/stage0/stage0.sh"
  echo 1 > "$tmp/up/workbench/app"; echo 1 > "$tmp/up/docs/x"; echo 1 > "$tmp/up/dev/stage0/units"; echo 1 > "$tmp/up/worker-daemon/x"
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
setup; merge dev/stage0/units; sortie 1; run; check "a kit change waits while an agent runs" main~1 ""
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
setup; smerge; sortie 1; run; check "a plan-bench merge waits while an agent runs" main ""
if sortie_at plan-bench~1; then echo "ok: and Sortie stays where it is"; else echo "FAIL: Sortie moved while busy"; failed=1; fi
finish 1; run; check "a plan-bench merge runs stage0.sh --update-sortie when idle" main "stage0.sh --update-sortie;"
setup; merge dev/stage0/units; smerge; run; check "a kit and a plan-bench merge run stage0.sh once" main "stage0.sh --update-sortie;"
setup; smerge; FAIL_STAGE0=1 run; check "a failed update runs once" main "stage0.sh --update-sortie;"
if sortie_at plan-bench~1; then echo "ok: and puts Sortie back"; else echo "FAIL: Sortie not put back"; failed=1; fi
rm -f "$tmp/calls"; run; check "and does not try that plan-bench commit again" main ""
smerge; run; check "until another one merges" main "stage0.sh --update-sortie;"
setup; echo 2 > "$tmp/sortie/go"; g -C "$tmp/sortie" commit -qam try; smerge; run
check "a Sortie checkout not behind plan-bench stays where it is" main ""

# A pending deploy drains its host (agent-platform/run-supervision): a busy
# p1, 2 stories running and 9 waiting, 2 at a time.
setup; sortie 2 9 2; merge dev/stage0/units; run
check "busy p1: the kit change waits for the 2 runs" main~1 ""
want "busy p1: Sortie dispatches no new run" "running 2, queued 9, paused"
finish 1; want "busy p1: a run ends, and none takes its slot" "running 1, queued 9, paused"
run; check "busy p1: the change still waits for the last run" main~1 ""
finish 1; run; check "busy p1: the change applies once the 2 finish" main "stage0.sh;"
want "busy p1: then Sortie dispatches the 9" "running 2, queued 7, dispatching"
if [[ ! -e $STAGE0_HOME/dispatch.paused && $(tr '\n' ' ' < "$SORTIE_STUB/log") == "pause pause pause resume " ]]; then echo "ok: busy p1: each wait pauses, the apply resumes"
else echo "FAIL: busy p1: Sortie calls '$(tr '\n' ' ' < "$SORTIE_STUB/log")'"; failed=1; fi
setup; sortie 1 4 1; smerge; run; finish 1; want "a plan-bench merge pauses dispatch too" "running 0, queued 4, paused"
run; check "and applies when the run finishes" main "stage0.sh --update-sortie;"
want "and dispatches again" "running 1, queued 3, dispatching"
setup; sortie 1 4 1; smerge; run; finish 1; FAIL_STAGE0=1 run
check "a failed apply after a drain" main "stage0.sh --update-sortie;"
want "dispatches again on the previous commit" "running 1, queued 3, dispatching"
setup; sortie 1 4 1; touch "$SORTIE_STUB/no-pause"; merge dev/stage0/units; run; finish 1
want "an older Sortie without the pause keeps dispatching" "running 1, queued 3, dispatching"
run; check "and the change waits until it happens to run nothing" main~1 ""
if grep -q 'Sortie did not pause dispatch' "$tmp/err"; then echo "ok: and deploy says so"; else echo "FAIL: no word of the missing pause: $(cat "$tmp/err")"; failed=1; fi
setup; sortie 1 4 1; echo wrong > "$STAGE0_HOME/sortie-token"; merge dev/stage0/units; run
want "a pause without the control token is refused" "running 1, queued 4, dispatching"
echo control > "$STAGE0_HOME/sortie-token"
setup; sortie 0 4 1; echo "deploy of kit 0" > "$STAGE0_HOME/dispatch.paused"; touch "$SORTIE_STUB/paused"; run
want "a pause the kit left with nothing pending is resumed" "running 1, queued 3, dispatching"
# A worker host: it waits while its daemon runs a job, and builds no Sortie.
cp "$STAGE0_HOME/stage0.env" "$tmp/control.env"
printf 'STAGE0_PROFILE=worker\nSTAGE0_SORTIE_SRC=%s\n' "$tmp/sortie" > "$STAGE0_HOME/stage0.env"
setup; merge dev/stage0/units; mkdir -p "$STAGE0_HOME/worker/jobs/ONS-9-1"; run
check "on a worker host a kit change waits while a job runs" main~1 ""
rmdir "$STAGE0_HOME/worker/jobs/ONS-9-1"; run; check "and runs stage0.sh once none does" main "stage0.sh;"
setup; merge worker-daemon/x; run; check "a worker-daemon change runs stage0.sh" main "stage0.sh;"
setup; smerge; run; check "a worker host follows no plan-bench merge" main ""
cp "$tmp/control.env" "$STAGE0_HOME/stage0.env"

# One run at a time: deploy.sh waits for a stage0.sh, and stage0.sh for it.
setup; merge dev/stage0/units; exec 8> "$STAGE0_HOME/stage0.lock"; flock 8
run; check "a kit change waits while a stage0.sh holds the lock" main~1 ""
rc=0; timeout 3 env -u STAGE0_LOCKED "$KIT/stage0.sh" > "$tmp/out" 2>&1 || rc=$?
if (( rc == 124 )) && grep -q 'Waiting for another stage0.sh' "$tmp/out"; then echo "ok: stage0.sh waits for the lock"
else echo "FAIL: stage0.sh with the lock held: exit $rc, $(cat "$tmp/out")"; failed=1; fi
exec 8>&-
run; check "and runs once the lock is free" main "stage0.sh;"

# The dev-host apply after a smolvm bump drains the host the same way, then
# runs stage0.sh so the pack and Sortie follow the new smolvm.
mkdir -p "$tmp/dkit"
cp "$KIT/dev-host-apply.sh" "$KIT/drain.sh" "$tmp/dkit/"
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\necho stage0.sh >> "%s/calls"\n[ -z "$FAIL_STAGE0" ]\n' "$tmp" > "$tmp/dkit/stage0.sh"
chmod +x "$tmp/dkit/stage0.sh"
# shellcheck disable=SC2016 # expanded by the apply, not here
apply=(sh -c 'echo "apply: running $(cat "$SORTIE_STUB/running"), $([ -e "$SORTIE_STUB/paused" ] && echo paused)" >> "$0/calls"' "$tmp")
apply() { rm -f "$tmp/calls"; rc=0; STAGE0_DRAIN_POLL=0 "$tmp/dkit/dev-host-apply.sh" "$@" > /dev/null 2> "$tmp/err" || rc=$?; }
calls() { if [[ -f $tmp/calls ]]; then tr '\n' ';' < "$tmp/calls"; fi; }
applied() { # applied NAME WANT_RC WANT_CALLS
  if [[ $rc == "$2" && $(calls) == "$3" ]]; then echo "ok: $1"; else echo "FAIL: $1: exit $rc, calls '$(calls)', want $2 and '$3': $(cat "$tmp/err")"; failed=1; fi
}
sortie 2 9 2; touch "$SORTIE_STUB/finish-on-poll"; apply "${apply[@]}"
applied "a smolvm bump on busy p1 applies once its 2 runs finish, dispatch paused" 0 "apply: running 0, paused;stage0.sh;"
rm "$SORTIE_STUB/finish-on-poll"; finish 0
want "and then Sortie dispatches the 9" "running 2, queued 7, dispatching"
sortie 1 4 1; touch "$SORTIE_STUB/finish-on-poll"; apply false
applied "a failed apply stops before stage0.sh" 1 ""
rm "$SORTIE_STUB/finish-on-poll"; finish 0
want "and dispatches again" "running 1, queued 3, dispatching"
sortie 0; FAIL_STAGE0=1 apply true; applied "a failed stage0.sh after the apply fails it" 1 "stage0.sh;"
want "and dispatches again" "running 0, queued 0, dispatching"
sortie 0; apply; applied "the control profile has no default apply" 1 ""
mkdir -p "$tmp/homelab"; printf 'dev-host:\n\techo make dev-host >> %s/calls\n' "$tmp" > "$tmp/homelab/Makefile"
cp "$STAGE0_HOME/stage0.env" "$tmp/env"
printf 'STAGE0_PROFILE=homelab\nSTAGE0_SORTIE_PORT=1\nSTAGE0_HOMELAB_SRC=%s\n' "$tmp/homelab" > "$STAGE0_HOME/stage0.env"
sortie 0; apply; applied "the homelab profile's apply is make dev-host in its homelab checkout" 0 "make dev-host;stage0.sh;"
cp "$tmp/env" "$STAGE0_HOME/stage0.env"
exec 8> "$STAGE0_HOME/stage0.lock"; flock 8
rc=0; timeout 3 "$tmp/dkit/dev-host-apply.sh" true 2> "$tmp/err" || rc=$?
if (( rc == 124 )) && grep -q 'waiting for a stage0.sh or a deploy' "$tmp/err"; then echo "ok: the dev-host apply waits for stage0.sh's lock"
else echo "FAIL: the dev-host apply with the lock held: exit $rc, $(cat "$tmp/err")"; failed=1; fi
exec 8>&-

exit $failed
