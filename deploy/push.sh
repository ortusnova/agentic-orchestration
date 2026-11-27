# shellcheck shell=bash
# The control host deploys the environment (spec store deploy-environments,
# design D3). Sourced by deploy.sh on the control host, after the loader's
# values, names.sh and deploy-state.sh, with ROOT, KIT, ENV_NAME and
# THIS_HOST set.
#
#   push_deploy COMMIT ARG...  deploy COMMIT to this host, then to each other
#                              host of the environment by its machine id,
#                              one at a time; stop at the first that fails,
#                              print every host's outcome, and fail unless
#                              every host succeeded
#   push_check COMMIT          print each host's running commit and status
#                              beside COMMIT; fail when any host differs
#   push_commit [COMMIT]       the commit to deploy: COMMIT, else this
#                              checkout's; refuses one no branch of origin holds
#
# With --migrate-stage0, each host still under its stage-0 names is migrated
# in its step (bootstrap.sh, migrate-stage0.sh); once its step succeeded,
# its worker must check in at this host's Sortie, or the host is rolled
# back (migrate-stage0.sh --rollback) and the push stops (design D7).
#
# Each host's step is bootstrap.sh, run here for this host and over
# `ssh -o BatchMode=yes DEST bash -s` for another, DEST its destination in
# the environment file. The push holds the environment's push lock here,
# so one push runs at a time; each step takes its host's own lock.

# push_origin: where every host fetches the commit, origin of this
# checkout (ORTUS_NOVA_ORIGIN, for the tests, another repository).
push_origin() { echo "${ORTUS_NOVA_ORIGIN:-$(git -C "$ROOT" remote get-url origin)}"; }

push_commit() {
  local commit=${1:-} origin tip tips
  origin=$(push_origin)
  # The tips of origin's branches, and their history, into this checkout's
  # objects only: no ref of the checkout moves.
  tips=$(git -C "$ROOT" ls-remote -q --heads "$origin" | cut -f1 | sort -u) || { echo "could not list the branches of $origin" >&2; return 1; }
  # shellcheck disable=SC2086 # one argument per tip
  [[ -z $tips ]] || git -C "$ROOT" fetch -q "$origin" $tips 2> /dev/null || { echo "could not fetch $origin" >&2; return 1; }
  [[ -n $commit ]] || commit=$(git -C "$ROOT" rev-parse HEAD)
  commit=$(git -C "$ROOT" rev-parse -q --verify "$commit^{commit}" 2> /dev/null) || { echo "no commit $1 in $ROOT or $origin" >&2; return 1; }
  while read -r tip _; do
    if git -C "$ROOT" merge-base --is-ancestor "$commit" "$tip" 2> /dev/null; then
      echo "$commit"
      return 0
    fi
  done <<< "$tips"
  echo "no branch of $origin holds $commit: push it first, every host fetches it from there" >&2
  return 1
}

# push_dest HOST: HOST's SSH destination (the environment file's ssh, else
# HOST).
push_dest() {
  local pair
  for pair in ${ORTUS_NOVA_ENV_HOST_SSH:-}; do
    [[ ${pair%%=*} == "$1" ]] && { echo "${pair#*=}"; return 0; }
  done
  echo "$1"
}

# push_hosts: the control host first, then every other host by machine id.
push_hosts() {
  local h
  echo "$THIS_HOST"
  for h in $ORTUS_NOVA_ENV_HOSTS; do [[ $h == "$THIS_HOST" ]] || echo "$h"; done
}

# push_state HOST: HOST's deploy state, read where its step wrote it: its
# state directory under the environment's names, else its stage-0 one
# (design D6), which push_stage0 HOST then reports.
PUSH_STAGE0=()
push_state() {
  local out home
  if [[ $1 == "$THIS_HOST" ]]; then
    [[ -n $ORTUS_NOVA_STAGE0 ]] && PUSH_STAGE0+=("$1")
    cat "$ORTUS_NOVA_HOME/deploy-state.json" 2> /dev/null
    return
  fi
  for home in "$ORTUS_NOVA_WORKER_HOST_HOME" "$ORTUS_NOVA_STAGE0_WORKER_HOST_HOME"; do
    if out=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "$(push_dest "$1")" cat "$home/deploy-state.json" < /dev/null 2> /dev/null); then
      [[ $home == "$ORTUS_NOVA_STAGE0_WORKER_HOST_HOME" ]] && PUSH_STAGE0+=("$1")
      printf '%s\n' "${out:0:65536}"
      return 0
    fi
  done
  return 1
}
push_stage0() { [[ " ${PUSH_STAGE0[*]} " == *" $1 "* ]]; }

# push_keep_state HOST: keep HOST's deploy state here, where Sortie's
# health check reads it; a host that cannot be read keeps its last copy.
# Prints the state.
push_keep_state() {
  local dir=$ORTUS_NOVA_HOME/hosts/$1
  [[ $1 == "$THIS_HOST" ]] && { push_state "$1"; return; }
  mkdir -p "$dir"
  if push_state "$1" > "$dir/deploy-state.json.tmp" && [[ -n $(deploy_state_field "$dir/deploy-state.json.tmp" status) ]]; then
    mv "$dir/deploy-state.json.tmp" "$dir/deploy-state.json"
    cat "$dir/deploy-state.json"
    return 0
  fi
  rm -f "$dir/deploy-state.json.tmp"
  return 1
}

# push_checked_in HOST SINCE: HOST's worker checked in at this host's
# Sortie at SINCE (epoch seconds) or later, within ORTUS_NOVA_CHECKIN_WAIT
# seconds (120).
push_checked_in() {
  local i
  for (( i = 0; i < ${ORTUS_NOVA_CHECKIN_WAIT:-120}; i += 2 )); do
    curl -sf "http://127.0.0.1:$ORTUS_NOVA_ENV_SORTIE_PORT/api/v1/state" 2> /dev/null | python3 -c '
import datetime, json, re, sys
host, since = sys.argv[1], int(sys.argv[2])
workers = ((json.load(sys.stdin).get("workers") or {}).get("workers")) or []
def at(t):
    t = re.sub(r"(\.\d{6})\d*", r"\1", t).replace("Z", "+00:00")
    return datetime.datetime.fromisoformat(t).timestamp()
sys.exit(0 if any(w.get("id") == host and at(w.get("last_check_in", "")) >= since for w in workers) else 1)
' "$1" "$2" && return 0
    sleep 2
  done
  return 1
}

# push_rollback HOST: HOST back on its stage-0 names, by the
# migrate-stage0.sh of its live kit under the environment's names.
push_rollback() {
  local home
  home=$(migration_names "$ENV_NAME" && echo "$MIGRATE_TO_HOME")
  if [[ $1 == "$THIS_HOST" ]]; then
    bash "$home/kit/deploy/migrate-stage0.sh" "$ENV_NAME" --rollback 8>&-
  else
    ssh -o BatchMode=yes -o ConnectTimeout=10 "$(push_dest "$1")" bash "${home#"$HOME"/}/kit/deploy/migrate-stage0.sh" "$ENV_NAME" --rollback < /dev/null 8>&-
  fi
}

# push_unmigrated HOST: HOST still has its stage-0 install and none of the
# environment's.
push_unmigrated() {
  local from to
  from=$(migration_names "$ENV_NAME" && echo "${MIGRATE_FROM_HOME#"$HOME"/}/$MIGRATE_FROM_SETTINGS_FILE")
  to=$(migration_names "$ENV_NAME" && echo "${MIGRATE_TO_HOME#"$HOME"/}/$MIGRATE_TO_SETTINGS_FILE")
  if [[ $1 == "$THIS_HOST" ]]; then
    [[ -e $HOME/$from && ! -e $HOME/$to ]]
  else
    ssh -o BatchMode=yes -o ConnectTimeout=10 "$(push_dest "$1")" test -e "$from" -a ! -e "$to" < /dev/null
  fi
}

# push_names: this host's names again, after its step moved its home.
push_names() {
  unset ORTUS_NOVA_HOME
  env_names "$ENV_NAME"
}

push_deploy() {
  local commit=$1 origin host rc outcome state stopped='' out=() failed=0 migrating='' was_stage0 since why
  shift
  [[ " $* " == *" --migrate-stage0 "* ]] && migrating=1
  origin=$(push_origin)
  mkdir -p "$ORTUS_NOVA_HOME"
  exec 8> "$ORTUS_NOVA_HOME/push.lock"
  flock -n 8 || { say "Waiting for another push of $ENV_NAME to finish"; flock 8; }
  for host in $(push_hosts); do
    if [[ -n $stopped ]]; then
      out+=("$host: not deployed")
      continue
    fi
    say "Deploying $host at ${commit:0:12}"
    was_stage0=''
    [[ -n $migrating ]] && push_unmigrated "$host" 2> /dev/null && was_stage0=1
    since=$(date +%s) why=''
    rc=0
    if [[ $host == "$THIS_HOST" ]]; then
      bash "$KIT/bootstrap.sh" "$ENV_NAME" "$commit" "$origin" "$ORTUS_NOVA_ENV_STAGE0_NAMES" "$@" 8>&- || rc=$?
    else
      ssh -o BatchMode=yes -o ConnectTimeout=10 "$(push_dest "$host")" bash -s -- "$ENV_NAME" "$commit" "$origin" "$ORTUS_NOVA_ENV_STAGE0_NAMES" "$@" \
        < "$KIT/bootstrap.sh" 8>&- || rc=$?
    fi
    [[ -z $migrating || $host != "$THIS_HOST" ]] || push_names
    if (( rc == 0 )) && [[ -n $was_stage0 ]] && ! push_checked_in "$host" "$since"; then
      why="its worker did not check in at $THIS_HOST's Sortie after its migration"
      rc=1
      if push_rollback "$host"; then why+="; rolled back to its stage-0 names"; else why+="; and its rollback failed: run deploy/migrate-stage0.sh $ENV_NAME --rollback on $host"; fi
      [[ $host != "$THIS_HOST" ]] || push_names
    fi
    state=''
    push_keep_state "$host" > "$ORTUS_NOVA_HOME/.push-state" 2> /dev/null && state=$(cat "$ORTUS_NOVA_HOME/.push-state")
    if (( rc == 0 )); then
      outcome="$host: succeeded at ${commit:0:12}"
    else
      outcome="$host: failed: $(deploy_state_field <(printf '%s\n' "$state") error)"
      [[ -n $state ]] || outcome="$host: failed: unreachable or no deploy state (exit $rc)"
      [[ -z $why ]] || outcome="$host: failed: $why"
      stopped=1 failed=1
    fi
    out+=("$outcome")
  done
  exec 8>&-
  say "Push of $ENV_NAME at ${commit:0:12}:"
  printf '  %s\n' "${out[@]}"
  return "$failed"
}

push_check() {
  local commit=$1 host state running status names differs=0
  for host in $(push_hosts); do
    state=''
    push_state "$host" > "$ORTUS_NOVA_HOME/.check-state" 2> /dev/null && state=$(cat "$ORTUS_NOVA_HOME/.check-state")
    if [[ -z $(deploy_state_field <(printf '%s\n' "$state") status) ]]; then
      echo "$host: unreachable or no deploy state (target ${commit:0:12})"
      differs=1
      continue
    fi
    running=$(deploy_state_field <(printf '%s\n' "$state") commit)
    status=$(deploy_state_field <(printf '%s\n' "$state") status)
    names=''
    push_stage0 "$host" && names=', under its stage-0 names'
    if [[ $running == "$commit" && $status == succeeded ]]; then
      echo "$host: at ${commit:0:12}, $status$names"
    else
      echo "$host: at ${running:0:12}, $status$names (target ${commit:0:12})"
      differs=1
    fi
  done
  return "$differs"
}
