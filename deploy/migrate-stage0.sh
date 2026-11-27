#!/usr/bin/env bash
# The migration of one host from its stage-0 names to its environment's
# (spec store deploy-environments, design D6, D7). The push runs it in each
# stage-0 host's step (deploy/deploy.sh ENV --migrate-stage0 on the control
# host; bootstrap.sh runs it from the target commit's kit); the operator
# runs --check, --rollback and --finish on a host directly.
#
#   migrate-stage0.sh ENV --commit SHA [ARG...]  migrate this host, then install
#                                                SHA under ENV's names (ARG to deploy.sh)
#   ... --no-start                               install, but start, enable and restart
#                                                nothing: every unit of ENV stays disabled
#                                                and stopped (deploy.sh ENV --start, later)
#   migrate-stage0.sh ENV --check                the checks of step 1 only; changes nothing
#   migrate-stage0.sh ENV --rollback             back to the stage-0 names, from the record
#   migrate-stage0.sh ENV --finish               delete the record, the database backups
#                                                and the old podman secrets; no rollback after
#
# In order (D7): (1) checks that change nothing: the host is declared with
# the runs its profile had, ENV's state, logs, worker host files, local
# name and units do not exist, each setting now in the environment file has
# its value (on a control host, which uses them), and the worker daemon
# records no job; (2) stops and disables every stage-0 unit, the deploy
# timer with them, under the stage-0 lock; (3) moves, with mv: the state
# directory, the logs, the worker host's files, the local-names file and
# the pack; the stage-0 unit files into the record; the settings file
# rewritten as ENV's; every path under the old home rewritten in each
# SQLite database (one transaction, after a backup beside it), in each git
# link (then git worktree repair) and in every other text file; a binary
# file that names the old home, a build output the install makes again, is
# moved aside into the record; podman secrets created under ENV's names;
# (4) the record, migration.json, with the counts of every table before;
# (5) the counts again, which must be equal, and a byte search of the new
# home, each database's dump included; (6) the install, deploy.sh ENV
# --this-host, from the moved live kit at SHA, then the units, Sortie's
# /readyz, the workbench and the append-only counts. Any failure in 3 to 6
# rolls the host back at once, and exits non-zero.
#
# With --no-start the stage-0 units are stopped as in (2), and nothing is
# started, enabled or restarted after: the install (deploy.sh --this-host
# --no-start) writes the units and files only, and (6) checks files only:
# ENV's unit files are there, none enabled or running, no stage-0 unit
# runs, and every count in Sortie's database, opened read-only, is as
# before. Sortie's /readyz, the workbench and the worker's check-in are
# deploy.sh ENV --start's checks. A rollback of such a migration starts and
# enables nothing either: it names the stage-0 units that ran before.
set -euo pipefail

KIT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'migrate-stage0.sh: %s\n' "$*" >&2; exit 1; }
usage() { echo "usage: $0 ENV [--commit SHA] [--check | --rollback | --finish] [--no-start] [--rebuild-pack] [--update-sortie]" >&2; exit 2; }
[[ $# -gt 0 && $1 != -* ]] || usage
ENV_NAME=$1
shift
MODE=migrate COMMIT='' NO_START='' PASS=()
while (( $# )); do
  case $1 in
    --check) MODE=check ;;
    --rollback) MODE=rollback ;;
    --finish) MODE=finish ;;
    --commit) [[ $# -gt 1 ]] || usage; COMMIT=$2; shift ;;
    --rebuild-pack | --update-sortie) PASS+=("$1") ;;
    --no-start) NO_START=1 PASS+=("$1") ;;
    *) usage ;;
  esac
  shift
done
# shellcheck source=names.sh
. "$KIT/names.sh"
# shellcheck source=local-name.sh
. "$KIT/local-name.sh"
migration_names "$ENV_NAME"
FROM=$MIGRATE_FROM_HOME TO=$MIGRATE_TO_HOME

# The migration moves the home the live kit is in: run from a copy.
if [[ $KIT == "$FROM"/* || $KIT == "$TO"/* ]]; then
  copy=$(mktemp -d)
  for f in names.sh local-name.sh deploy-state.sh migrate-stage0.sh migrate-stage0.py envfile environments; do
    [[ ! -e $KIT/$f ]] || cp -r "$KIT/$f" "$copy/"
  done
  again=("$ENV_NAME")
  [[ -z $COMMIT ]] || again+=(--commit "$COMMIT")
  [[ $MODE == migrate ]] || again+=("--$MODE")
  MIGRATE_STAGE0_COPY=$copy exec bash "$copy/migrate-stage0.sh" "${again[@]}" "${PASS[@]}"
fi
[[ -z ${MIGRATE_STAGE0_COPY:-} ]] || trap 'rm -rf "$MIGRATE_STAGE0_COPY"' EXIT

THIS_HOST=$(hostname -s | tr '[:upper:]' '[:lower:]')
UNITS=${ORTUS_NOVA_UNITS:-$HOME/.config/systemd/user}
RECORD=$TO/migration.json
KEEP=$TO/migration
LOCAL_NAMES=$HOME/$ORTUS_NOVA_LOCAL_NAMES_REL
# A worker host's files from worker-host.sh add.
WORKER_HOST_FILES=(cp-worker.crt cp-worker.key cp-ca.crt egress-ca.crt control-plane-url)
# The settings that moved to the environment file (design D2), with the
# loader's value each must have: what env_values in names.sh maps.
MOVED_SETTINGS=(SORTIE_PORT=SORTIE_PORT WORKBENCH_PORT=WORKBENCH_PORT NONO_PORT=NONO_PORT
  NONO_LIVE_PROOF_PORT=NONO_LIVE_PROOF_PORT EGRESS_GATE_PORT=EGRESS_GATE_PORT PREVIEW_FIRST_PORT=PREVIEW_FIRST_PORT
  HULY_URL=TRACKER_URL HULY_WORKSPACE=TRACKER_WORKSPACE HULY_PROJECT=TRACKER_PROJECT WORKBENCH_LOCAL_NAME=WORKBENCH_LOCAL_NAME)
py() { python3 "$KIT/migrate-stage0.py" "$@"; }
rec() { py record "$RECORD" "$@"; }
jstr() { python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"; }
jlist() { python3 -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "$@"; }
# setting_of FILE NAME: NAME's last value in a settings file.
setting_of() { sed -n "s/^$MIGRATE_FROM_SETTINGS_PREFIX$2=//p" "$1" | tail -n 1; }
# stage0_units: the stage-0 unit files.
stage0_units() {
  local f
  for f in "$UNITS/$MIGRATE_FROM_UNIT_PREFIX"*; do [[ -e $f ]] && echo "${f##*/}"; done
  return 0
}
# databases HOME: every SQLite file under HOME, relative.
databases() { py databases "$1" migration; }
# all_counts HOME DB...: each database's table counts, one JSON object.
all_counts() {
  local home=$1 db out='{'
  shift
  for db; do out+="$(jstr "$db"): $(py counts "$home/$db"),"; done
  echo "${out%,}}"
}
# repair_worktrees HOME: each git work tree under HOME, linked again to its
# repository, wherever that is.
repair_worktrees() {
  local g
  while IFS= read -r g; do
    git -C "${g%/.git}" worktree repair > /dev/null 2>&1 || true
  done < <(find "$1" -name .git -type f 2> /dev/null)
}
deploy_unit() { [[ $1 == "${2}deploy."* ]]; }

# --- Step 1: the checks --------------------------------------------------
checks() {
  local reasons=() profile want pair setting key value want_value jobs f err
  [[ -s $FROM/$MIGRATE_FROM_SETTINGS_FILE ]] || die "$THIS_HOST has no stage-0 install ($FROM/$MIGRATE_FROM_SETTINGS_FILE): nothing to migrate"
  err=$(mktemp)
  if env_file_load "$ENV_NAME" "$THIS_HOST" 2> "$err"; then
    profile=$(setting_of "$FROM/$MIGRATE_FROM_SETTINGS_FILE" PROFILE)
    profile=${profile:-control}
    [[ $profile == homelab ]] && profile=worker
    want=worker
    [[ " $ORTUS_NOVA_ENV_HOST_RUNS " == *" control "* ]] && want=control
    [[ $profile == "$want" ]] \
      || reasons+=("$FROM/$MIGRATE_FROM_SETTINGS_FILE says ${MIGRATE_FROM_SETTINGS_PREFIX}PROFILE=$profile, and $ORTUS_NOVA_ENV_FILE says $THIS_HOST runs $ORTUS_NOVA_ENV_HOST_RUNS ($want)")
    if [[ $want == control ]]; then
      for pair in "${MOVED_SETTINGS[@]}"; do
        setting=${pair%%=*} key=ORTUS_NOVA_ENV_${pair#*=}
        value=$(setting_of "$FROM/$MIGRATE_FROM_SETTINGS_FILE" "$setting")
        want_value=${!key:-}
        [[ -z $value || $value == "$want_value" ]] \
          || reasons+=("${MIGRATE_FROM_SETTINGS_PREFIX}$setting is $value in $FROM/$MIGRATE_FROM_SETTINGS_FILE, and $ORTUS_NOVA_ENV_FILE says $want_value: make them agree first")
      done
    fi
  else
    reasons+=("$(tr '\n' ' ' < "$err")")
  fi
  rm -f "$err"
  for f in "$TO" "$MIGRATE_TO_LOG_DIR" "$HOME/$MIGRATE_TO_WORKER_HOST_CONFIG" "$LOCAL_NAMES/$MIGRATE_TO_LOCAL_NAME_FILE" "$UNITS/$MIGRATE_TO_UNIT_PREFIX"*; do
    [[ -e $f ]] && reasons+=("$f exists: environment $ENV_NAME is already installed here, at least in part")
  done
  jobs=$(find "$FROM/worker/jobs" -mindepth 1 -maxdepth 1 -printf '%f ' 2> /dev/null || true)
  [[ -z $jobs ]] || reasons+=("the worker daemon records jobs: ${jobs% } ($FROM/worker/jobs): pause dispatch and let them end first")
  if [[ -e $FROM/$MIGRATE_FROM_LOCK_FILE ]]; then
    exec 7>> "$FROM/$MIGRATE_FROM_LOCK_FILE"
    flock -n 7 || reasons+=("a deploy of this host runs ($FROM/$MIGRATE_FROM_LOCK_FILE)")
  fi
  command -v python3 > /dev/null || reasons+=("python3 is not on PATH")
  if (( ${#reasons[@]} )); then
    printf 'migrate-stage0.sh: %s cannot be migrated to %s, nothing changed:\n' "$THIS_HOST" "$ENV_NAME" >&2
    printf '  %s\n' "${reasons[@]}" >&2
    exit 1
  fi
}

# --- The rollback --------------------------------------------------------
# rollback: back to the stage-0 names from the record, whatever part of
# the migration it lists; every step is tried, and it fails naming those
# that did not work.
rollback() {
  set +e
  trap - ERR
  local bad=() f db from to rel commit back enabled active no_start
  [[ -s $RECORD ]] || { echo "migrate-stage0.sh: no migration record at $RECORD" >&2; return 1; }
  say "Rolling $THIS_HOST back to its stage-0 names ($FROM)"
  commit=$(rec get stage0_commit)
  mapfile -t enabled < <(rec lines units_enabled)
  mapfile -t active < <(rec lines units_active)
  no_start=$(rec get no_start)
  # ENV's units, stopped and removed.
  systemctl --user stop "$MIGRATE_TO_UNIT_PREFIX*" 2> /dev/null
  for f in "$UNITS/$MIGRATE_TO_UNIT_PREFIX"*; do
    [[ -e $f ]] || continue
    systemctl --user disable -q "${f##*/}" 2> /dev/null
    rm -f "$f"
  done
  systemctl --user daemon-reload
  while read -r f; do [[ -n $f ]] && { podman secret rm "$f" > /dev/null 2>&1 || bad+=("podman secret $f"); }; done < <(rec lines secrets_created)
  # The databases, the git links and the text files back to the old home.
  while read -r db; do
    [[ -n $db ]] || continue
    py rewrite-db "$TO/$db" "$TO" "$FROM" > /dev/null || bad+=("the paths in $db")
  done < <(rec lines databases)
  py rewrite-links "$TO" "$TO" "$FROM" > /dev/null || bad+=("the git links")
  mkdir -p "$KEEP/rolled-back"
  py rewrite-files "$TO" "$TO" "$FROM" "$KEEP/rolled-back" migration migration.json > /dev/null || bad+=("the files naming $TO")
  if [[ -d $KEEP/derived ]]; then
    while IFS= read -r -d '' rel; do
      rel=${rel#./}
      mkdir -p "$(dirname "$TO/$rel")"
      mv -f "$KEEP/derived/$rel" "$TO/$rel" || bad+=("$rel")
    done < <(cd "$KEEP/derived" && find . \( -type f -o -type l \) -print0)
  fi
  if [[ -e $KEEP/$MIGRATE_FROM_SETTINGS_FILE ]]; then
    [[ -e $TO/$MIGRATE_TO_SETTINGS_FILE ]] && mv -f "$TO/$MIGRATE_TO_SETTINGS_FILE" "$KEEP/rolled-back/"
    mv -f "$KEEP/$MIGRATE_FROM_SETTINGS_FILE" "$TO/$MIGRATE_FROM_SETTINGS_FILE" || bad+=("$MIGRATE_FROM_SETTINGS_FILE")
  fi
  # What moved, back, the last first; ENV's local-names file, which the
  # install may have written, with it.
  while IFS=$'\t' read -r from to; do
    [[ -n $from && $from != "$FROM" ]] || continue
    mkdir -p "$(dirname "$from")"
    mv -f "$to" "$from" || bad+=("$to -> $from")
  done < <(rec lines moves | tac)
  rm -f "$LOCAL_NAMES/$MIGRATE_TO_LOCAL_NAME_FILE"
  rmdir "$HOME/$MIGRATE_TO_WORKER_HOST_CONFIG" "$(dirname "$MIGRATE_TO_LOG_DIR")" 2> /dev/null
  mkdir -p "$UNITS"
  while read -r f; do [[ -n $f ]] && { mv -f "$KEEP/units/$f" "$UNITS/$f" || bad+=("unit $f"); }; done < <(rec lines units_files)
  while read -r f; do [[ -n $f ]] && mv -f "$TO/$f" "$KEEP/rolled-back/"; done < <(rec lines backups)
  mv -f "$RECORD" "$KEEP/rolled-back/migration.json"
  back=migration-rolled-back
  [[ -e $TO/$back ]] && back+=-$(date -u +%Y%m%dT%H%M%SZ)
  mv "$KEEP/rolled-back" "$TO/$back" && rm -rf "$KEEP"
  mv "$TO" "$FROM" || { bad+=("$TO -> $FROM"); printf 'migrate-stage0.sh: the rollback did not finish: %s\n' "${bad[*]}" >&2; return 1; }
  repair_worktrees "$FROM"
  [[ -z $commit || ! -e $FROM/kit ]] || git -C "$FROM/kit" checkout -q --force --detach "$commit" || bad+=("the live kit at $commit")
  systemctl --user daemon-reload
  if [[ $no_start == true ]]; then
    if (( ${#bad[@]} )); then
      printf 'migrate-stage0.sh: the rollback did not restore: %s\n' "${bad[*]}" >&2
      return 1
    fi
    say "$THIS_HOST is back on its stage-0 names at ${commit:0:12}, every unit stopped and disabled (the migration ran with --no-start); the record is in $FROM/$back"
    [[ -z ${enabled[*]} ]] || say "Enabled before the migration: ${enabled[*]}"
    [[ -z ${active[*]} ]] || say "Running before the migration: ${active[*]}"
    return 0
  fi
  for f in "${enabled[@]}"; do
    [[ -n $f ]] && ! deploy_unit "$f" "$MIGRATE_FROM_UNIT_PREFIX" && systemctl --user enable -q "$f"
  done
  for f in "${active[@]}"; do
    [[ -n $f ]] && ! deploy_unit "$f" "$MIGRATE_FROM_UNIT_PREFIX" && { systemctl --user start "$f" || bad+=("start $f"); }
  done
  if (( ${#bad[@]} )); then
    printf 'migrate-stage0.sh: the rollback did not restore: %s\n' "${bad[*]}" >&2
    return 1
  fi
  say "$THIS_HOST is back on its stage-0 names at ${commit:0:12}, its deploy timer disabled; the record is in $FROM/$back"
}

# fail_back WHY: roll back, and fail saying WHY.
fail_back() {
  local why=$1
  printf 'migrate-stage0.sh: %s\n' "$why" >&2
  if rollback; then
    die "$THIS_HOST is back on its stage-0 names: $why"
  fi
  die "$why; and the rollback failed too (see above)"
}

case $MODE in
  check)
    checks
    say "$THIS_HOST can be migrated to $ENV_NAME ($FROM -> $TO)"
    exit 0
    ;;
  rollback)
    [[ -s $RECORD ]] || die "no migration record at $RECORD: the record is gone (--finish ran, or $THIS_HOST was never migrated), so there is nothing to roll back"
    [[ ! -e $FROM ]] || die "$FROM exists: a rollback would overwrite it"
    exec 7>> "$TO/$MIGRATE_TO_LOCK_FILE"
    flock -n 7 || die "a deploy of this host runs ($TO/$MIGRATE_TO_LOCK_FILE)"
    exec 7>&-
    rollback || exit 1
    exit 0
    ;;
  finish)
    [[ -s $RECORD ]] || die "no migration record at $RECORD: --finish ran already, or $THIS_HOST was never migrated"
    [[ ! -e $FROM ]] || die "$FROM exists: the migration did not move it"
    while read -r f; do [[ -n $f ]] && { podman secret rm "$f" > /dev/null 2>&1 || true; }; done < <(rec lines secrets_old)
    while read -r f; do [[ -n $f ]] && rm -f "$TO/$f"; done < <(rec lines backups)
    rm -rf "$KEEP"
    rm -f "$RECORD"
    say "Finished $THIS_HOST's migration to $ENV_NAME: no rollback from here"
    exit 0
    ;;
esac

[[ -n $COMMIT ]] || usage
checks
say "Migrating $THIS_HOST from its stage-0 names ($FROM) to $ENV_NAME's ($TO)"

# --- Step 2: stop the stage-0 units --------------------------------------
mapfile -t unit_files < <(stage0_units)
mapfile -t units_active < <(systemctl --user list-units --plain --no-legend --state=active "$MIGRATE_FROM_UNIT_PREFIX*" 2> /dev/null | awk '{print $1}')
units_enabled=()
for f in "${unit_files[@]}"; do
  systemctl --user is-enabled -q "$f" 2> /dev/null && units_enabled+=("$f")
done
stage0_commit=$(git -C "$FROM/kit" rev-parse -q --verify HEAD 2> /dev/null || true)
say "Stopping the stage-0 units (${#units_active[@]} running)"
systemctl --user stop "$MIGRATE_FROM_UNIT_PREFIX*" 2> /dev/null || true
for f in "${unit_files[@]}"; do systemctl --user disable -q "$f" 2> /dev/null || true; done
mapfile -t dbs < <(databases "$FROM")
counts_before=$(all_counts "$FROM" "${dbs[@]}")

# --- Step 3: move --------------------------------------------------------
mkdir -p "$(dirname "$TO")"
mv "$FROM" "$TO"
mkdir -p "$KEEP/units"
rec set version 1
rec set environment "$(jstr "$ENV_NAME")"
rec set host "$(jstr "$THIS_HOST")"
rec set stage0_commit "$(jstr "$stage0_commit")"
rec set commit "$(jstr "$COMMIT")"
rec set started_at "$(jstr "$(date -u +%Y-%m-%dT%H:%M:%SZ)")"
rec set status '"moving"'
[[ -z $NO_START ]] || rec set no_start true
rec set units_files "$(jlist "${unit_files[@]}")"
rec set units_enabled "$(jlist "${units_enabled[@]}")"
rec set units_active "$(jlist "${units_active[@]}")"
rec set databases "$(jlist "${dbs[@]}")"
rec set counts_before "$counts_before"
rec append moves "$(jlist "$FROM" "$TO")"
# From here on, any failure rolls the host back.
set -E
trap 'fail_back "line $LINENO of migrate-stage0.sh failed"' ERR

for f in "${unit_files[@]}"; do mv "$UNITS/$f" "$KEEP/units/$f"; done
systemctl --user daemon-reload
# move FROM TO: one mv, recorded.
move() {
  [[ -e $1 ]] || return 0
  mkdir -p "$(dirname "$2")"
  mv "$1" "$2"
  rec append moves "$(jlist "$1" "$2")"
}
move "$MIGRATE_FROM_LOG_DIR" "$MIGRATE_TO_LOG_DIR"
for f in "${WORKER_HOST_FILES[@]}"; do move "$HOME/$MIGRATE_FROM_WORKER_HOST_CONFIG/$f" "$HOME/$MIGRATE_TO_WORKER_HOST_CONFIG/$f"; done
move "$LOCAL_NAMES/$MIGRATE_FROM_LOCAL_NAME_FILE" "$LOCAL_NAMES/$MIGRATE_TO_LOCAL_NAME_FILE"
move "$TO/$MIGRATE_FROM_PACK_FILE" "$TO/$MIGRATE_TO_PACK_FILE"
move "$TO/$MIGRATE_FROM_PACK_FILE.inputs" "$TO/$MIGRATE_TO_PACK_FILE.inputs"

# The settings, as ENV's: the stage-0 file kept in the record, and host.env
# without what the environment file or the environment's names give now.
mv "$TO/$MIGRATE_FROM_SETTINGS_FILE" "$KEEP/$MIGRATE_FROM_SETTINGS_FILE"
drop="PROFILE|WORKER_NAME"
for pair in "${MOVED_SETTINGS[@]}"; do drop+="|${pair%%=*}"; done
[[ $(setting_of "$KEEP/$MIGRATE_FROM_SETTINGS_FILE" PACK) == "$FROM/$MIGRATE_FROM_PACK_FILE" ]] && drop+="|PACK"
(umask 077; grep -vE "^$MIGRATE_FROM_SETTINGS_PREFIX($drop)=" "$KEEP/$MIGRATE_FROM_SETTINGS_FILE" \
  | sed "s/\\b$MIGRATE_FROM_SETTINGS_PREFIX/$MIGRATE_TO_SETTINGS_PREFIX/g" > "$TO/$MIGRATE_TO_SETTINGS_FILE")
IFS='|' read -ra dropped <<< "$drop"
rec set settings_dropped "$(jlist "${dropped[@]}")"

backups=()
for db in "${dbs[@]}"; do
  rec append backups "$(jstr "$db.pre-migration")"
  backups+=("$db.pre-migration")
  say "Rewriting the paths in $db (backup $db.pre-migration)"
  py rewrite-db "$TO/$db" "$FROM" "$TO" "$TO/$db.pre-migration" > /dev/null
done
links=$(py rewrite-links "$TO" "$FROM" "$TO")
rec set links "$links"
repair_worktrees "$TO"
files=$(py rewrite-files "$TO" "$FROM" "$TO" "$KEEP/derived" migration migration.json "${backups[@]}")
rec set files "$files"

if command -v podman > /dev/null; then
  while read -r s; do
    [[ $s == "$MIGRATE_FROM_PODMAN_PREFIX"* ]] || continue
    new=$MIGRATE_TO_PODMAN_PREFIX${s#"$MIGRATE_FROM_PODMAN_PREFIX"}
    rec append secrets_old "$(jstr "$s")"
    podman secret exists "$new" && continue
    podman secret inspect --showsecret --format '{{.SecretData}}' "$s" | podman secret create "$new" - > /dev/null
    rec append secrets_created "$(jstr "$new")"
  done < <(podman secret ls --format '{{.Name}}')
fi
git -C "$TO/kit" checkout -q --force --detach "$COMMIT"

# --- Step 5: check the move ----------------------------------------------
counts_after=$(all_counts "$TO" "${dbs[@]}")
rec set counts_after "$counts_after"
if ! differ=$(py diff-counts "$counts_before" "$counts_after"); then
  fail_back "the counts in Sortie's database differ after the move ($differ): something wrote to it"
fi
if ! hits=$(py scan "$TO" "$FROM" migration migration.json "${backups[@]}"); then
  fail_back "these still name $FROM: $(tr '\n' ' ' <<< "$hits")"
fi
rec set status '"installing"'

# --- Step 6: install under ENV's names, and check it ---------------------
exec 7>&-
say "Installing $THIS_HOST under $ENV_NAME's names at ${COMMIT:0:12}"
log=$(mktemp)
if ! env -u ORTUS_NOVA_HOME -u ORTUS_NOVA_STAGE0 -u ORTUS_NOVA_LOCKED -u ORTUS_NOVA_ENV \
  "$TO/kit/deploy/deploy.sh" "$ENV_NAME" --this-host --commit "$COMMIT" "${PASS[@]}" 2> >(tee "$log" >&2); then
  wait
  why=$(sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -v '^[[:space:]]*$' | tail -n 1 || true)
  rm -f "$log"
  fail_back "the install under $ENV_NAME's names failed: $why"
fi
wait
rm -f "$log"
# shellcheck disable=SC2153 # set by env_file_load
want_runs=$ORTUS_NOVA_ENV_HOST_RUNS
if [[ -n $NO_START ]]; then
  # Files only: nothing runs to answer, and nothing may start.
  want_parts=(worker.service)
  [[ " $want_runs " == *" control "* ]] && want_parts+=(sortie.service workbench.service)
  for part in "${want_parts[@]}"; do
    [[ -e $UNITS/$MIGRATE_TO_UNIT_PREFIX$part ]] || fail_back "the install wrote no $MIGRATE_TO_UNIT_PREFIX$part in $UNITS"
  done
  for f in "$UNITS/$MIGRATE_TO_UNIT_PREFIX"*; do
    [[ -e $f ]] || continue
    state=$(systemctl --user is-enabled "${f##*/}" 2> /dev/null || true)
    [[ $state != enabled* ]] || fail_back "${f##*/} is $state after an install with --no-start"
  done
  running=$(systemctl --user list-units --plain --no-legend --state=active "$MIGRATE_TO_UNIT_PREFIX*" "$MIGRATE_FROM_UNIT_PREFIX*" 2> /dev/null | awk '{print $1}' | paste -sd' ' -)
  [[ -z $running ]] || fail_back "these run after an install with --no-start: $running"
  counts_installed=$(all_counts "$TO" "${dbs[@]}")
  rec set counts_installed "$counts_installed"
  if ! differ=$(py diff-counts "$counts_before" "$counts_installed"); then
    fail_back "the counts in Sortie's database differ after the install with --no-start ($differ): something wrote to it"
  fi
  trap - ERR
  rec set status '"installed"'
  rec set installed_at "$(jstr "$(date -u +%Y-%m-%dT%H:%M:%SZ)")"
  say "Migrated $THIS_HOST to $ENV_NAME's names at ${COMMIT:0:12}, nothing started: every $MIGRATE_TO_UNIT_PREFIX* unit is disabled and stopped; the record is $RECORD. deploy/deploy.sh $ENV_NAME --start on $ORTUS_NOVA_ENV_CONTROL_HOST starts the environment; $TO/kit/deploy/migrate-stage0.sh $ENV_NAME --rollback returns this host"
  exit 0
fi
systemctl --user is-active -q "${MIGRATE_TO_UNIT_PREFIX}worker.service" || fail_back "${MIGRATE_TO_UNIT_PREFIX}worker.service does not run after the install"
if [[ " $want_runs " == *" control "* ]]; then
  for part in sortie.service workbench.service; do
    systemctl --user is-active -q "$MIGRATE_TO_UNIT_PREFIX$part" || fail_back "$MIGRATE_TO_UNIT_PREFIX$part does not run after the install"
  done
  wait_for() {
    local i
    for (( i = 0; i < ${MIGRATE_STAGE0_WAIT:-60}; i++ )); do
      curl -sf -o /dev/null "$1" && return 0
      sleep 1
    done
    return 1
  }
  wait_for "http://127.0.0.1:$ORTUS_NOVA_ENV_SORTIE_PORT/readyz" || fail_back "Sortie does not answer /readyz on 127.0.0.1:$ORTUS_NOVA_ENV_SORTIE_PORT after the install"
  wait_for "http://127.0.0.1:$ORTUS_NOVA_ENV_WORKBENCH_PORT/api/auth/mode" || fail_back "the workbench does not answer on 127.0.0.1:$ORTUS_NOVA_ENV_WORKBENCH_PORT after the install"
fi
counts_installed=$(all_counts "$TO" "${dbs[@]}")
rec set counts_installed "$counts_installed"
if ! lost=$(py lost "$counts_before" "$counts_installed"); then
  fail_back "rows are missing after the install: $lost"
fi
trap - ERR
rec set status '"migrated"'
rec set migrated_at "$(jstr "$(date -u +%Y-%m-%dT%H:%M:%SZ)")"
say "Migrated $THIS_HOST to $ENV_NAME's names at ${COMMIT:0:12}: the record is $RECORD; $TO/kit/deploy/migrate-stage0.sh $ENV_NAME --rollback returns it, --finish after a day ends it"
