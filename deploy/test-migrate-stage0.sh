#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016,SC2030,SC2031 # ok and fail always succeed; the stubs' text; subshells on purpose
# Checks the migration from the stage-0 names (spec store
# deploy-environments, design D7): one deploy.sh test-a --migrate-stage0
# on a stubbed control host migrates a throwaway copy of a control host's
# stage-0 home, then a worker host's, over an ssh stub that runs each
# command in the named host's HOME. systemctl, podman and curl are stubs
# that keep their state in the host's HOME. Sortie's database is made by
# Sortie's own migrations (the sortie/ submodule, or SORTIE_SRC), with rows
# that hold paths under the old home. Without either the test skips; with
# SORTIE_SRC set, or ORTUS_NOVA_TEST_NO_SKIP=1 as in CI, a missing checkout
# fails.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
src=${SORTIE_SRC:-$KIT/../sortie}
if [[ ! -f $src/internal/persistence/migrate.go ]]; then
  [[ -z ${SORTIE_SRC:-} ]] || { echo "test-migrate-stage0: no Sortie checkout at $SORTIE_SRC" >&2; exit 1; }
  [[ ${ORTUS_NOVA_TEST_NO_SKIP:-} != 1 ]] || { echo "test-migrate-stage0: no Sortie checkout at $src, and ORTUS_NOVA_TEST_NO_SKIP=1 forbids a skip: run git submodule update --init" >&2; exit 1; }
  echo "test-migrate-stage0: skipped, no Sortie checkout: run git submodule update --init, or set SORTIE_SRC"
  exit 0
fi
src=$(cd "$src" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null
unset ORTUS_NOVA_HOME ORTUS_NOVA_ENV ORTUS_NOVA_LOCKED ORTUS_NOVA_STAGE0
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
g() { git -c user.name=t -c user.email=t@t -c init.defaultBranch=main "$@"; }

# Sortie's database, by Sortie's migrations.
printf '{"Replace":{"%s":"%s"}}\n' "$src/internal/persistence/kit_database_test.go" "$KIT/sortie-db/kit_database_test.go" > "$tmp/overlay.json"
(cd "$src" && ORTUS_NOVA_SORTIE_DB=$tmp/sortie.db GOWORK=off go test -count=1 -overlay "$tmp/overlay.json" -run '^TestKitMakeDatabase$' ./internal/persistence > "$tmp/makedb.out" 2>&1) \
  || { cat "$tmp/makedb.out"; echo "FAIL: Sortie's migrations did not make a database"; exit 1; }

# shellcheck source=testenv.sh
. "$KIT/testenv.sh"
testenv_init "$tmp/env"
bin=$tmp/env/bin
hosts=$tmp/hosts
export ORTUS_NOVA_CHECKIN_WAIT=2 MIGRATE_STAGE0_WAIT=2

# ssh DEST CMD...: CMD in DEST's HOME, as DEST.
cat > "$bin/ssh" <<EOF
#!/usr/bin/env bash
while [[ \$1 == -o ]]; do shift 2; done
echo "ssh \$*" >> "$tmp/ssh.calls"
unset ORTUS_NOVA_HOME ORTUS_NOVA_ENV ORTUS_NOVA_LOCKED ORTUS_NOVA_STAGE0
export HOME=$hosts/\${1#*@} TESTENV_HOST=\${1#*@}
shift
cd "\$HOME" && exec "\$@"
EOF
# systemctl --user: unit state in \$HOME/.systemd/{enabled,active}; a unit
# starts only when its file (or its template's) is in the unit directory.
# With \$HOME/write-on-reload, a daemon-reload adds a row to the database of
# the new home: something that wrote during the move.
cat > "$bin/systemctl" <<'EOF'
#!/usr/bin/env bash
s=$HOME/.systemd u=${ORTUS_NOVA_UNITS:-$HOME/.config/systemd/user}
mkdir -p "$s/enabled" "$s/active"
echo "systemctl $*" >> "$HOME/systemctl.calls"
args=() now='' state=''
for a; do
  case $a in --user | -q | --quiet | --plain | --no-legend | --all) ;; --now) now=1 ;; --state=*) state=${a#*=} ;; *) args+=("$a") ;; esac
done
cmd=${args[0]}
units=("${args[@]:1}")
has_file() { local t=$1; [[ $t == *@*.* ]] && t=${t%%@*}@.${t##*.}; [[ -e $u/$t ]]; }
case $cmd in
  daemon-reload)
    if [[ -e $HOME/write-on-reload ]]; then
      for db in "$HOME"/.local/share/ortus-nova/test-a/sortie/.sortie.db; do
        [[ -e $db ]] && python3 -c 'import sqlite3, sys; c = sqlite3.connect(sys.argv[1]); c.execute("INSERT INTO supervisor_actions (action, route, result, status, acted_at) VALUES (?, ?, ?, ?, ?)", ("late", "r", "ok", 200, "t")); c.commit()' "$db"
      done
    fi ;;
  enable) for x in "${units[@]}"; do touch "$s/enabled/$x"; if [[ -n $now ]]; then touch "$s/active/$x"; fi; done ;;
  disable) for x in "${units[@]}"; do rm -f "$s/enabled/$x"; [[ -n $now ]] && rm -f "$s/active/$x"; done; true ;;
  start | restart) for x in "${units[@]}"; do has_file "$x" || { echo "Unit $x not found." >&2; exit 5; }; touch "$s/active/$x"; done ;;
  stop) for x in "${units[@]}"; do for f in "$s/active/"$x; do rm -f "$f"; done; done; true ;;
  is-active) for x in "${units[@]}"; do [[ -e $s/active/$x ]] || exit 3; done ;;
  is-enabled) for x in "${units[@]}"; do [[ -e $s/enabled/$x ]] || exit 1; done ;;
  list-units) for x in "${units[@]:-*}"; do for f in "$s/active/"$x; do [[ -e $f ]] && echo "${f##*/} loaded active running x"; done; done; true ;;
  *) echo "systemctl stub: $cmd" >&2; exit 1 ;;
esac
EOF
# podman secret: one file per secret in $HOME/.podman/secrets.
cat > "$bin/podman" <<'EOF'
#!/usr/bin/env bash
d=$HOME/.podman/secrets
mkdir -p "$d"
[[ $1 == secret ]] || { echo "podman stub: $*" >&2; exit 1; }
shift
cmd=$1
shift
args=()
for a; do case $a in --replace | --showsecret) ;; --format=*) ;; *) args+=("$a") ;; esac; done
[[ $cmd == ls || $cmd == inspect ]] && [[ ${args[0]:-} == --format ]] && args=("${args[@]:2}")
case $cmd in
  ls) ls -1 "$d" ;;
  exists) [[ -e $d/${args[0]} ]] ;;
  inspect) cat "$d/${args[0]}" ;;
  create) if [[ ${args[1]} == - ]]; then cat > "$d/${args[0]}"; else cp "${args[1]}" "$d/${args[0]}"; fi ;;
  rm) [[ -e $d/${args[0]} ]] && rm "$d/${args[0]}" ;;
esac
EOF
# curl: Sortie answers while its unit runs, unless $HOME/no-readyz; the
# workbench while its unit runs; Sortie's state lists the workers that
# $tmp/checkins names, checked in now.
cat > "$bin/curl" <<EOF
#!/usr/bin/env bash
url=\${*: -1}
a=\$HOME/.systemd/active
case \$url in
  */readyz) [[ -e \$a/ortus-nova-test-a-sortie.service && ! -e \$HOME/no-readyz ]] ;;
  */api/auth/mode) [[ -e \$a/ortus-nova-test-a-workbench.service ]] ;;
  */api/v1/state)
    now=\$(date -u +%Y-%m-%dT%H:%M:%S.123456789Z)
    printf '{"workers": {"workers": ['
    sep=''
    for w in \$(cat "$tmp/checkins" 2> /dev/null); do printf '%s{"id": "%s", "last_check_in": "%s"}' "\$sep" "\$w" "\$now"; sep=,; done
    printf ']}}\n' ;;
  *) exit 7 ;;
esac
EOF
chmod +x "$bin/ssh" "$bin/systemctl" "$bin/podman" "$bin/curl"

# The origin: the kit's own names, deploy state and migration, and a
# deploy.sh that installs the host under the names env_names gives: its
# units started, a worker.json and a binary naming its home. It fails with
# $HOME/fail-install. A tracked file names the control host's old home: the
# commit's source, which the migration leaves alone.
origin=$tmp/origin.git up=$tmp/up
g init -q --bare "$origin"
g clone -q "$origin" "$up" 2> /dev/null
mkdir -p "$up/deploy"
cp "$KIT"/{names.sh,local-name.sh,deploy-state.sh,migrate-stage0.sh,migrate-stage0.py} "$up/deploy/"
echo "An example path: $hosts/control/.local/share/ortus-nova/stage0/sessions" > "$up/deploy/example.md"
cat > "$up/deploy/deploy.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
trap 'echo "deploy.sh stub: line $LINENO: $BASH_COMMAND" >&2' ERR
d=$(cd "$(dirname "$0")" && pwd)
head=$(git -C "$d" rev-parse HEAD)
. "$d/names.sh"
ORTUS_NOVA_ENV_STAGE0_NAMES=true
env_names "$1"
. "$d/deploy-state.sh"
echo "${HOME##*/} deploy.sh $* at $head in $ORTUS_NOVA_HOME" >> "$HOME/calls"
[[ ! -e $HOME/fail-install ]] || { echo "deploy.sh: the pack did not build" >&2; exit 1; }
u=${ORTUS_NOVA_UNITS:-$HOME/.config/systemd/user}
mkdir -p "$u" "$ORTUS_NOVA_HOME/bin"
parts=(worker.service logs.service)
[[ $(cat "$HOME/runs") == control ]] && parts+=(sortie.service workbench.service)
for p in "${parts[@]}"; do
  printf '[Service]\nEnvironment=ORTUS_NOVA_HOME=%s\nExecStart=%s/deploy/run\n' "$ORTUS_NOVA_HOME" "$d" > "$u/$(unit "$p")"
  systemctl --user enable -q "$(unit "$p")"
  systemctl --user restart "$(unit "$p")"
done
printf '{"state_dir": "%s/worker"}\n' "$ORTUS_NOVA_HOME" > "$ORTUS_NOVA_HOME/worker.json"
printf 'ELF\0%s\0' "$ORTUS_NOVA_HOME" > "$ORTUS_NOVA_HOME/bin/worker-daemon"
deploy_state_write succeeded "$head" "$head"
EOF
chmod +x "$up/deploy/deploy.sh"
commit() { echo "$1" >> "$up/x"; g -C "$up" add -A; g -C "$up" commit -qm "$1"; g -C "$up" push -q origin HEAD:main 2> /dev/null; g -C "$up" rev-parse HEAD; }
c0=$(commit stage0)
c1=$(commit one)
export ORTUS_NOVA_ORIGIN=$origin
testenv_file test-a 1 control=control+worker alpha=worker stage0_names:\ true
# The operator's checkout of the kit and the homelab checkout, whose
# worktrees the stage-0 homes hold.
operator=$tmp/operator homelab=$tmp/homelab
g clone -q "$origin" "$operator" 2> /dev/null
g init -q "$homelab"
g -C "$homelab" commit -q --allow-empty -m homelab
mkdir -p "$hosts"

s0() { echo "$hosts/$1/.local/share/ortus-nova/stage0"; }
new() { echo "$hosts/$1/.local/share/ortus-nova/test-a"; }
# stage0_host HOST RUNS: a fresh stage-0 install of HOST, which runs RUNS
# (control or worker), its kit at c0, its units running.
stage0_host() {
  local h=$hosts/$1 runs=$2 s
  s=$(s0 "$1")
  if [[ -d $h ]]; then
    git -C "$operator" worktree remove --force "$s/kit" 2> /dev/null || true
    git -C "$homelab" worktree remove --force "$(new "$1")/homelab" "$s/homelab" 2> /dev/null || true
    rm -rf "$h"
    git -C "$operator" worktree prune
    git -C "$homelab" worktree prune
  fi
  mkdir -p "$s"/{sortie,worker/jobs,bin,inputs,workspaces/ONS-7/.sortie-host,sessions} "$h/.local/state/ortus-nova/logs" "$h/.config/systemd/user"
  echo "$runs" > "$h/runs"
  g -C "$operator" worktree add -q --detach "$s/kit" "$c0" 2> /dev/null
  [[ $runs == worker ]] && g -C "$homelab" worktree add -q --detach "$s/homelab" 2> /dev/null
  {
    echo "# Stage-0 settings (dev/stage0/stage0.sh)."
    echo "STAGE0_PROFILE=$runs"
    echo "STAGE0_KIT_ROOT=$s/kit"
    echo "STAGE0_SORTIE_SRC=$h/dev/sortie"
    echo "STAGE0_SORTIE_PORT=10180"
    echo "STAGE0_WORKBENCH_PORT=10193"
    echo "STAGE0_HULY_URL=http://localhost:10187"
    echo "STAGE0_HULY_PROJECT=TST"
    echo "STAGE0_PACK=$s/stage0.smolmachine"
    echo "STAGE0_HOMELAB_SRC=$homelab"
  } > "$s/stage0.env"
  printf 'ORTUS_NOVA_ENV=test-a\nORTUS_NOVA_ENV_FILE=%s/kit/deploy/environments/test-a.yml\n' "$s" > "$s/environment.env"
  printf '{"state_dir": "%s/worker", "pack": "%s/stage0.smolmachine"}\n' "$s" "$s" > "$s/worker.json"
  printf 'workspace: {root: %s/workspaces}\n' "$s" > "$s/sortie/WORKFLOW.md"
  printf '{"command": "%s/bin/acp-broker"}\n' "$s" > "$s/workspaces/ONS-7/.sortie-host/mcp.json"
  printf 'ELF\0%s/kit/worker-daemon\0' "$s" > "$s/bin/worker-daemon"
  printf 'PACK\0\1\2' > "$s/stage0.smolmachine"
  echo 1234 > "$s/stage0.smolmachine.inputs"
  echo key > "$s/inputs/key"
  ln -s "$s/inputs/key" "$s/inputs/current"
  : > "$s/stage0.lock"
  printf '{"status": "succeeded", "commit": "%s"}\n' "$c0" > "$s/deploy-state.json"
  echo "log line naming $s" > "$h/.local/state/ortus-nova/logs/ortus-stage0-worker.log"
  local units=(ortus-stage0-worker.service ortus-stage0-logs.service ortus-stage0-deploy.service ortus-stage0-deploy.timer)
  [[ $runs == control ]] && units+=(ortus-stage0-sortie.service ortus-stage0-workbench.service ortus-stage0-preview@.service)
  for f in "${units[@]}"; do printf '[Service]\nEnvironment=STAGE0_HOME=%s\n' "$s" > "$h/.config/systemd/user/$f"; done
  mkdir -p "$h/.systemd/enabled" "$h/.systemd/active"
  for f in ortus-stage0-worker.service ortus-stage0-logs.service ortus-stage0-deploy.timer; do touch "$h/.systemd/enabled/$f" "$h/.systemd/active/$f"; done
  if [[ $runs == control ]]; then
    for f in ortus-stage0-sortie.service ortus-stage0-workbench.service; do touch "$h/.systemd/enabled/$f" "$h/.systemd/active/$f"; done
    touch "$h/.systemd/active/ortus-stage0-preview@ONS-7.service"
    mkdir -p "$h/.podman/secrets" "$h/.config/homelab/local-names.d" "$s/control-plane"
    echo cert > "$h/.podman/secrets/ortus-stage0-pomerium-cert"
    echo key > "$h/.podman/secrets/ortus-stage0-pomerium-key"
    printf '# The workbench of environment test-a.\ntest-a.homelab.dev 10193\n' > "$h/.config/homelab/local-names.d/ortus-stage0-workbench"
    cp "$tmp/sortie.db" "$s/sortie/.sortie.db"
    python3 - "$s/sortie/.sortie.db" "$s" <<'PY'
import sqlite3, sys
db, home = sqlite3.connect(sys.argv[1]), sys.argv[2]
for i in range(1, 4):
    db.execute("INSERT INTO issue_outcomes (issue_id, kind, observed_at) VALUES (?, 'merged', 't')", (f"i{i}",))
    db.execute("INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace, started_at, completed_at, status) "
               "VALUES (?, ?, 1, 'acp', ?, 't', 't', 'succeeded')", (f"i{i}", f"ONS-{i}", f"{home}/workspaces/ONS-{i}"))
    db.execute("INSERT INTO remote_runs (issue_id, job_id, attempt, worker_id, record, updated_at) VALUES (?, ?, 1, 'alpha', ?, 't')",
               (f"i{i}", f"ONS-{i}-1", f'{{"workspace": "{home}/workspaces/ONS-{i}"}}'))
    db.execute("INSERT INTO plan_satisfaction (issue_id, kind, ref, recorded_at) VALUES (?, 'merged', 'abc', 't')", (f"i{i}",))
    db.execute("INSERT INTO supervisor_actions (action, route, evidence, result, status, acted_at) VALUES ('fix', 'r', ?, 'ok', 200, 't')",
               (f"{home}/sessions/s{i}.log",))
    db.execute("INSERT INTO platform_faults (id, signature, class, action_where, recorded_at) VALUES (?, 'sig', 'host', ?, 't')",
               (f"f{i}", f"{home}/kit/deploy",))
db.commit()
PY
  else
    mkdir -p "$h/.config/ortus-nova"
    for f in cp-worker.crt cp-worker.key cp-ca.crt egress-ca.crt; do echo "$f" > "$h/.config/ortus-nova/$f"; done
    echo https://control:10143 > "$h/.config/ortus-nova/control-plane-url"
  fi
}
fresh() {
  stage0_host control control
  stage0_host alpha worker
  echo "control alpha" > "$tmp/checkins"
}
# snapshot HOST: every file of HOST's HOME with its content, and its units'
# and secrets' state; not the stubs' call logs or the push's report (its
# deploy state, kept copies and lock), which the push writes on purpose.
snapshot() {
  (cd "$hosts/$1" && find . \( -name calls -o -name systemctl.calls -o -name deploy-state.json -o -name .push-state -o -name push.lock -o -path ./.local/share/ortus-nova/stage0/hosts \) -prune \
    -o \( -type f -o -type l \) -print | LC_ALL=C sort | while read -r f; do
    if [[ -L $f ]]; then echo "$f -> $(readlink "$f")"; else echo "$f $(sha256sum < "$f" | cut -c1-16)"; fi
  done)
  git -C "$(s0 "$1")/kit" rev-parse HEAD 2> /dev/null || true
}
push() { # push ARG...: deploy.sh test-a ARG... on the control host
  rm -f "$tmp/ssh.calls" "$hosts"/*/calls
  rc=0
  (export HOME=$hosts/control TESTENV_HOST=control; "$KIT/deploy.sh" test-a "$@") > "$tmp/out" 2>&1 || rc=$?
}
on() { # on HOST CMD...: CMD on HOST, as the operator there
  local h=$1
  shift
  rc=0
  (export HOME=$hosts/$h TESTENV_HOST=$h; cd "$HOME"; "$@") > "$tmp/out" 2>&1 || rc=$?
}
counts() { python3 "$KIT/migrate-stage0.py" counts "$1"; }
record() { python3 "$KIT/migrate-stage0.py" record "$(new control)/migration.json" get "$1"; }
active() { [[ -e $hosts/$1/.systemd/active/$2 ]]; }
enabled() { [[ -e $hosts/$1/.systemd/enabled/$2 ]]; }
# names_old HOST: the files under HOST's new home that still name its old
# home, a database's dump included, but the record, the backup and the
# kit's tracked files (the commit's source).
names_old() {
  local n old
  n=$(new "$1") old=$(s0 "$1")
  (cd "$n" && grep -rlF "$old" --exclude-dir=migration --exclude='migration.json' --exclude='*.pre-migration' --exclude='*.sortie.db*' . 2> /dev/null \
    | sed 's|^\./||' | grep -vxFf <(git -C "$n/kit" ls-files | sed 's|^|kit/|') || true)
  if [[ -e $n/sortie/.sortie.db ]] && python3 -c 'import sqlite3, sys; sys.exit(0 if any(sys.argv[2] in l for l in sqlite3.connect(sys.argv[1]).iterdump()) else 1)' "$n/sortie/.sortie.db" "$old"; then
    echo "sortie/.sortie.db (dump)"
  fi
}
worktrees_ok() { # worktrees_ok HOME: both checkouts name their worktrees under HOME, and none is prunable
  local l
  l=$(git -C "$operator" worktree list --porcelain; git -C "$homelab" worktree list --porcelain)
  ! grep -q prunable <<< "$l" && grep -qx "worktree $1/control/.local/share/ortus-nova/$2/kit" <<< "$l" \
    && grep -qx "worktree $1/alpha/.local/share/ortus-nova/$3/kit" <<< "$l" && grep -qx "worktree $1/alpha/.local/share/ortus-nova/$3/homelab" <<< "$l"
}

# --- The migration of both hosts ----------------------------------------
fresh
c=$(s0 control) a=$(s0 alpha)
inode=$(stat -c %i "$c/sortie/.sortie.db")
before=$(counts "$c/sortie/.sortie.db")
push --commit "$c1" --migrate-stage0
(( rc == 0 )) && grep -q "control: succeeded at ${c1:0:12}" "$tmp/out" && grep -q "alpha: succeeded at ${c1:0:12}" "$tmp/out" \
  && ok "one push with --migrate-stage0 migrates the control host, then the worker host" || fail "push: exit $rc, $(cat "$tmp/out")"
nc=$(new control) na=$(new alpha)
[[ ! -e $c && ! -e $a && -d $nc && -d $na ]] && ok "each stage-0 home is gone, and the environment's home is there" || fail "homes: $(ls "$hosts"/*/.local/share/ortus-nova)"
[[ $(stat -c %i "$nc/sortie/.sortie.db" 2> /dev/null) == "$inode" ]] && ok "the database moved: the same inode" || fail "the database was copied"
after=$(counts "$nc/sortie/.sortie.db")
python3 -c 'import json, sys; r = json.loads(sys.argv[1]); sys.exit(r["sortie/.sortie.db"] != json.loads(sys.argv[2]) or r["sortie/.sortie.db"]["run_history"] != 3)' "$(record counts_before)" "$after" \
  && [[ $before == "$after" ]] && ok "the counts recorded before equal Sortie's after" || fail "counts: before $before, recorded $(record counts_before), after $after"
[[ -z $(names_old control) && -z $(names_old alpha) ]] && ok "no file under either new home but the record and the backup names the old home, the database's dump included" \
  || fail "still naming the old home: $(names_old control) $(names_old alpha)"
python3 -c 'import sqlite3, sys; [print(l) for l in sqlite3.connect(sys.argv[1]).iterdump()]' "$nc/sortie/.sortie.db" > "$tmp/dump"
grep -q "$nc/workspaces/ONS-1" "$tmp/dump" \
  && [[ -s $nc/sortie/.sortie.db.pre-migration ]] && ok "the database's paths name the new home, after a backup beside it" || fail "database paths"
! enabled control ortus-stage0-deploy.timer && ! active control ortus-stage0-deploy.timer && ! enabled alpha ortus-stage0-deploy.timer \
  && ! compgen -G "$hosts/*/.config/systemd/user/*stage0*" > /dev/null && [[ -e $nc/migration/units/ortus-stage0-deploy.timer ]] \
  && ok "the stage-0 deploy timer is disabled, and no stage-0 unit is left, its files in the record" || fail "stage-0 units: $(ls "$hosts"/*/.config/systemd/user/ "$hosts"/*/.systemd/enabled)"
active control ortus-nova-test-a-sortie.service && active control ortus-nova-test-a-worker.service && active alpha ortus-nova-test-a-worker.service \
  && ok "the environment's units run on both hosts" || fail "units: $(ls "$hosts"/*/.systemd/active)"
grep -qx "ORTUS_NOVA_KIT_ROOT=$nc/kit" "$nc/host.env" && ! grep -qE 'STAGE0_|PROFILE=|SORTIE_PORT=|HULY_URL=|_PACK=' "$nc/host.env" \
  && grep -qx "ORTUS_NOVA_HOMELAB_SRC=$homelab" "$nc/host.env" && [[ $(stat -c %a "$nc/host.env") == 600 ]] && [[ ! -e $nc/stage0.env ]] \
  && ok "stage0.env is host.env, ORTUS_NOVA_*, without the settings the environment file holds" || fail "host.env: $(cat "$nc/host.env")"
lnd=$hosts/control/.config/homelab/local-names.d
[[ -e $lnd/ortus-nova-test-a-workbench && ! -e $lnd/ortus-stage0-workbench ]] && ok "the local-names file moved to its new name" || fail "local names: $(ls "$lnd")"
[[ $(cat "$hosts/alpha/.config/ortus-nova/test-a/control-plane-url") == https://control:10143 && ! -e $hosts/alpha/.config/ortus-nova/control-plane-url ]] \
  && ok "the worker host's files moved to ~/.config/ortus-nova/test-a" || fail "worker host files: $(find "$hosts/alpha/.config/ortus-nova")"
[[ -e $hosts/control/.local/state/ortus-nova/test-a/logs/ortus-stage0-worker.log && ! -e $hosts/control/.local/state/ortus-nova/logs ]] \
  && ok "the logs moved" || fail "logs: $(find "$hosts/control/.local/state")"
[[ -e $nc/agent.smolmachine && -e $nc/agent.smolmachine.inputs && ! -e $nc/stage0.smolmachine ]] && ok "the pack moved to its new name" || fail "pack: $(ls "$nc")"
[[ $(readlink "$nc/inputs/current") == "$nc/inputs/key" ]] && grep -q "$nc/workspaces" "$nc/sortie/WORKFLOW.md" \
  && grep -qa "$c/kit" "$nc/migration/derived/bin/worker-daemon" && ! grep -qa "$c" "$nc/bin/worker-daemon" \
  && ok "text files and symlinks are rewritten, a binary naming the old home is kept aside in the record" || fail "rewrites"
[[ $(cat "$hosts/control/.podman/secrets/ortus-nova-test-a-pomerium-cert") == cert && -e $hosts/control/.podman/secrets/ortus-stage0-pomerium-cert ]] \
  && ok "the podman secrets have their new names, and the old ones stay" || fail "secrets: $(ls "$hosts/control/.podman/secrets")"
worktrees_ok "$hosts" test-a test-a && ok "git worktree list names the moved worktrees, none prunable" \
  || fail "worktrees: $(git -C "$operator" worktree list; git -C "$homelab" worktree list)"
grep -q "deploy.sh test-a --this-host --commit $c1 at $c1 in $nc" "$hosts/control/calls" && grep -q "in $na" "$hosts/alpha/calls" \
  && ok "each host is installed from its moved live kit at the commit" || fail "calls: $(cat "$hosts"/*/calls)"
[[ $(record status) == migrated && $(record stage0_commit) == "$c0" ]] && ok "the record says migrated, from the stage-0 commit" || fail "record: $(cat "$nc/migration.json")"
push --commit "$c1" --check
(( rc == 0 )) && ! grep -q 'stage-0' "$tmp/out" && ok "--check then passes, with no host under its stage-0 names" || fail "check: $(cat "$tmp/out")"
push --commit "$c1" --migrate-stage0
(( rc == 0 )) && grep -q "in $na" "$hosts/alpha/calls" && [[ -e $nc/migration.json ]] && ok "a second push with --migrate-stage0 deploys the migrated hosts as they are" || fail "again: $(cat "$tmp/out")"

# --- A run under the new names, then --rollback ---------------------------
python3 -c 'import sqlite3, sys; c = sqlite3.connect(sys.argv[1]); c.execute("INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace, started_at, completed_at, status) VALUES (?, ?, 1, ?, ?, ?, ?, ?)", ("i9", "ONS-9", "acp", sys.argv[2] + "/workspaces/ONS-9", "t", "t", "succeeded")); c.commit()' "$nc/sortie/.sortie.db" "$nc"
mkdir -p "$nc/workspaces/ONS-9/.sortie-host"
printf '{"command": "%s/bin/acp-broker"}\n' "$nc" > "$nc/workspaces/ONS-9/.sortie-host/mcp.json"
on control bash "$nc/kit/deploy/migrate-stage0.sh" test-a --rollback
(( rc == 0 )) && [[ -d $c && ! -e $nc ]] && ok "--rollback returns the control host to its stage-0 home" || fail "rollback: exit $rc, $(cat "$tmp/out")"
[[ $(stat -c %i "$c/sortie/.sortie.db" 2> /dev/null) == "$inode" ]] \
  && python3 -c 'import sqlite3, sys; sys.exit(sqlite3.connect(sys.argv[1]).execute("SELECT workspace FROM run_history WHERE identifier = ?", ("ONS-9",)).fetchone()[0] != sys.argv[2] + "/workspaces/ONS-9")' "$c/sortie/.sortie.db" "$c" \
  && grep -q "$c/bin" "$c/workspaces/ONS-9/.sortie-host/mcp.json" \
  && ok "the run written under the new names is in the stage-0 home's database, its paths the old home's" || fail "the run: $(python3 "$KIT/migrate-stage0.py" counts "$c/sortie/.sortie.db")"
active control ortus-stage0-sortie.service && active control ortus-stage0-worker.service && active control ortus-stage0-preview@ONS-7.service \
  && enabled control ortus-stage0-sortie.service && [[ -e $hosts/control/.config/systemd/user/ortus-stage0-deploy.timer ]] \
  && ! enabled control ortus-stage0-deploy.timer && ! active control ortus-stage0-deploy.timer \
  && ! compgen -G "$hosts/control/.config/systemd/user/*test-a*" > /dev/null && ! active control ortus-nova-test-a-sortie.service \
  && ok "the stage-0 units run again, the deploy timer stays disabled, the new units are gone" || fail "units: $(ls "$hosts/control/.systemd/active" "$hosts/control/.config/systemd/user")"
[[ $(git -C "$c/kit" rev-parse HEAD) == "$c0" ]] && grep -q "$c/kit" "$c/bin/worker-daemon" && grep -qx "STAGE0_PROFILE=control" "$c/stage0.env" \
  && [[ ! -e $c/host.env && -e $lnd/ortus-stage0-workbench && ! -e $lnd/ortus-nova-test-a-workbench && -e $hosts/control/.local/state/ortus-nova/logs ]] \
  && [[ ! -e $hosts/control/.podman/secrets/ortus-nova-test-a-pomerium-cert && -e $hosts/control/.podman/secrets/ortus-stage0-pomerium-cert ]] \
  && ok "the live kit is at the recorded stage-0 commit, with its settings, binaries, local name, logs and secrets" || fail "after rollback: $(git -C "$c/kit" rev-parse HEAD) $(ls "$c" "$lnd")"
l=$(git -C "$operator" worktree list --porcelain)
! grep -q prunable <<< "$l" && grep -qx "worktree $c/kit" <<< "$l" && ok "git worktree list names the worktree back in the stage-0 home, none prunable" || fail "worktrees: $l"

# --- --finish, then --rollback refuses ----------------------------------
fresh
push --commit "$c1" --migrate-stage0
on control bash "$nc/kit/deploy/migrate-stage0.sh" test-a --finish
(( rc == 0 )) && [[ ! -e $nc/migration.json && ! -e $nc/migration && ! -e $nc/sortie/.sortie.db.pre-migration && ! -e $c ]] \
  && [[ ! -e $hosts/control/.podman/secrets/ortus-stage0-pomerium-cert && -e $hosts/control/.podman/secrets/ortus-nova-test-a-pomerium-cert ]] \
  && ok "--finish deletes the record, the backup and the old podman secrets" || fail "finish: exit $rc, $(cat "$tmp/out"), $(ls "$nc" "$hosts/control/.podman/secrets")"
was=$(snapshot control)
on control bash "$nc/kit/deploy/migrate-stage0.sh" test-a --rollback
(( rc != 0 )) && grep -q 'the record is gone' "$tmp/out" && [[ $(snapshot control) == "$was" ]] \
  && ok "--rollback after --finish refuses, saying the record is gone, and changes nothing" || fail "rollback after finish: exit $rc, $(cat "$tmp/out")"

# --- A failed install rolls the host back --------------------------------
fresh
was_alpha=$(snapshot alpha)
inode=$(stat -c %i "$c/sortie/.sortie.db")
touch "$hosts/control/fail-install"
push --commit "$c1" --migrate-stage0
(( rc != 0 )) && grep -q "control: failed: the migration to test-a's names failed at ${c1:0:12}" "$tmp/out" && grep -q "alpha: not deployed" "$tmp/out" \
  && ok "a failed install fails the host's step and stops the push" || fail "failed install: exit $rc, $(cat "$tmp/out")"
[[ -d $c && ! -e $nc && $(stat -c %i "$c/sortie/.sortie.db") == "$inode" ]] && active control ortus-stage0-sortie.service && active control ortus-stage0-preview@ONS-7.service \
  && ! active control ortus-stage0-deploy.timer && [[ $(git -C "$c/kit" rev-parse HEAD) == "$c0" && $(counts "$c/sortie/.sortie.db") == "$before" ]] \
  && grep -q '"status": "failed"' "$c/deploy-state.json" \
  && ok "the host rolled itself back: its stage-0 units run again from its stage-0 home, the same database, its deploy state failed" || fail "after a failed install: $(ls "$hosts/control/.local/share/ortus-nova" "$hosts/control/.systemd/active")"
[[ ! -e $hosts/alpha/calls && $(snapshot alpha) == "$was_alpha" ]] && ok "and no later host is deployed or changed" || fail "alpha: $(cat "$hosts/alpha/calls" 2> /dev/null)"

# --- Counts that differ after the move roll the host back ---------------
fresh
touch "$hosts/control/write-on-reload"
push --commit "$c1" --migrate-stage0
(( rc != 0 )) && grep -q 'counts in Sortie' "$tmp/out" && grep -q "alpha: not deployed" "$tmp/out" && [[ -d $c && ! -e $nc ]] \
  && active control ortus-stage0-sortie.service && [[ ! -e $hosts/control/calls ]] \
  && ok "a count that differs after the move rolls the host back before any install, and stops the push" || fail "count mismatch: exit $rc, $(cat "$tmp/out")"

# --- A worker that does not check in -------------------------------------
fresh
echo control > "$tmp/checkins"
push --commit "$c1" --migrate-stage0
(( rc != 0 )) && grep -q "control: succeeded" "$tmp/out" && grep -q "alpha: failed: its worker did not check in" "$tmp/out" && grep -q 'rolled back' "$tmp/out" \
  && [[ -d $a && ! -e $na && -d $nc ]] && active alpha ortus-stage0-worker.service && [[ -e $hosts/alpha/.config/ortus-nova/control-plane-url ]] \
  && ok "a worker host whose worker does not check in is rolled back, the control host stays migrated" || fail "no check-in: exit $rc, $(cat "$tmp/out")"

# --- The checks of step 1: each refuses, and changes nothing -------------
# refused NAME REASON SETUP...: on fresh hosts, after SETUP, the push
# refuses with REASON at the control host (or deploy.sh before it, for a
# profile, D6) and leaves both hosts as they were, deploying neither.
refused() {
  local name=$1 reason=$2
  shift 2
  fresh
  "$@"
  local wc wa
  wc=$(snapshot control) wa=$(snapshot alpha)
  push --commit "$c1" --migrate-stage0
  (( rc != 0 )) && grep -q -- "$reason" "$tmp/out" \
    && [[ $(snapshot control) == "$wc" && $(snapshot alpha) == "$wa" && ! -e $hosts/control/calls && ! -e $hosts/alpha/calls ]] \
    && ok "$name: refused, naming why; the push stops and changes no file, unit or secret of either host" \
    || fail "$name: exit $rc, $(cat "$tmp/out"); $(diff <(echo "$wc") <(snapshot control) | head -5)"
}
refused "a profile the environment file contradicts" 'STAGE0_PROFILE=worker' sed -i 's/^STAGE0_PROFILE=control/STAGE0_PROFILE=worker/' "$(s0 control)/stage0.env"
refused "an environment's home that exists" "$(new control) exists" mkdir -p "$(new control)"
refused "a moved setting that differs from the environment file" 'STAGE0_SORTIE_PORT is 7680' sed -i 's/^STAGE0_SORTIE_PORT=.*/STAGE0_SORTIE_PORT=7680/' "$(s0 control)/stage0.env"
# shellcheck disable=SC2329 # refused runs it
hold_lock() { flock -o "$(s0 control)/stage0.lock" sleep 60 & echo $! > "$tmp/lock.pid"; sleep 0.5; }
refused "a deploy that runs" 'a deploy of this host runs' hold_lock
kill "$(cat "$tmp/lock.pid")" 2> /dev/null || true

# A job in progress on the worker host: the control host is migrated, the
# worker host refused, naming the job, and left as it was.
fresh
mkdir -p "$(s0 alpha)/worker/jobs/ONS-7-1"
wa=$(snapshot alpha)
push --commit "$c1" --migrate-stage0
(( rc != 0 )) && grep -q "control: succeeded" "$tmp/out" && grep -q "alpha: failed: .*ONS-7-1" "$tmp/out" && [[ $(snapshot alpha) == "$wa" ]] \
  && active alpha ortus-stage0-worker.service && ! active alpha ortus-nova-test-a-worker.service \
  && ok "a job in progress refuses the worker host's migration, naming it, with its units, files and machines unchanged" || fail "job: exit $rc, $(cat "$tmp/out")"

# A host the environment file does not declare.
fresh
on control env TESTENV_HOST=mini bash "$KIT/migrate-stage0.sh" test-a --check
(( rc != 0 )) && grep -q 'mini' "$tmp/out" && [[ -d $c ]] && ok "a host the environment file does not declare is refused, naming it" || fail "undeclared: exit $rc, $(cat "$tmp/out")"
on control bash "$KIT/migrate-stage0.sh" test-a --check
(( rc == 0 )) && ok "--check passes on a host that can be migrated, changing nothing" || fail "check: exit $rc, $(cat "$tmp/out")"
exit $failed
