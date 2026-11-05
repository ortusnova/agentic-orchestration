# shellcheck shell=bash
# Each host's deploy state (spec store agent-platform/supervisor-autonomy,
# design D7): the file Sortie's internal/deploystate reads, which its
# health check (health.deploy_state) and merge-and-prove
# (merge_and_prove.hosts) read on p1. deploy.sh and dev-host-apply.sh write
# it on every host, after every deploy and at each tick of the deploy timer;
# on p1, deploy.sh also copies each worker host's beside it, over the SSH
# that worker-host.sh add already uses. Sourced by deploy.sh,
# dev-host-apply.sh, stage0.sh, worker-host.sh and the tests. Needs
# STAGE0_HOME.
#
#   $STAGE0_HOME/deploy-state.json              this host's
#   $STAGE0_HOME/deploy-hosts/NAME              worker host NAME's SSH destination (worker-host.sh add)
#   $STAGE0_HOME/hosts/NAME/deploy-state.json   p1's copy of worker host NAME's
#
# The file is one JSON object, replaced whole each time:
#
#   status        succeeded (the deploy applied: every unit whose
#                 configuration changed restarted), failed, or deploying
#                 while one runs
#   commit        the kit's commit of main that runs
#   main          the newest commit of main the kit saw
#   behind_since  when commit first fell behind main; null while it is main
#   error         why the last deploy failed; empty otherwise
#   updated_at    when the kit last wrote it
#
# Sortie's deploystate knows the statuses succeeded, failed and deploying,
# and refuses any other: an applied deploy is succeeded.

# A worker host's STAGE0_HOME, as its own shell sees it (the default there).
DEPLOY_STATE_WORKER_HOME=${STAGE0_WORKER_HOST_HOME:-.local/share/ortus-nova/stage0}

deploy_state_file() { echo "$STAGE0_HOME/deploy-state.json"; }

# deploy_state_now: the time to record, UTC (STAGE0_NOW in the tests).
deploy_state_now() { echo "${STAGE0_NOW:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"; }

# json_string S: S as a JSON string, on one line, control characters as spaces.
json_string() {
  local s
  s=$(printf '%s' "$1" | tr '\000-\037' ' ' | sed $'s/\x7f/ /g')
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  printf '"%s"' "$s"
}

# deploy_state_field FILE KEY: KEY's string value in a file this kit wrote
# (one member a line), or nothing for null or a missing file or member.
deploy_state_field() {
  [[ -r $1 ]] || return 0
  sed -n "s/^  \"$2\": \"\\(.*\\)\",\\{0,1\\}\$/\\1/p" "$1" | head -n 1 | sed 's/\\"/"/g; s/\\\\/\\/g'
}

# deploy_state_write STATUS COMMIT MAIN [ERROR]: replace this host's file.
# behind_since stays what it was while the same commit stays behind main.
deploy_state_write() {
  local status=$1 commit=$2 main=$3 error=${4:-} file now behind=null prev
  file=$(deploy_state_file)
  now=$(deploy_state_now)
  if [[ -n $main && $commit != "$main" ]]; then
    prev=$(deploy_state_field "$file" behind_since)
    if [[ -n $prev && $(deploy_state_field "$file" commit) == "$commit" ]]; then
      behind=$(json_string "$prev")
    else
      behind=$(json_string "$now")
    fi
  fi
  mkdir -p "${file%/*}"
  {
    echo '{'
    printf '  "status": %s,\n' "$(json_string "$status")"
    printf '  "commit": %s,\n' "$(json_string "$commit")"
    printf '  "main": %s,\n' "$(json_string "$main")"
    printf '  "behind_since": %s,\n' "$behind"
    printf '  "error": %s,\n' "$(json_string "$error")"
    printf '  "updated_at": %s\n' "$(json_string "$now")"
    echo '}'
  } > "$file.tmp" && mv "$file.tmp" "$file"
}

# deploy_state_refresh COMMIT MAIN: a tick with nothing to deploy. The last
# deploy's status and error stay, with the commit that runs and main as
# they are now; a deploy that stopped while deploying failed.
deploy_state_refresh() {
  local file status error
  file=$(deploy_state_file)
  status=$(deploy_state_field "$file" status)
  error=$(deploy_state_field "$file" error)
  case $status in
    succeeded | failed) ;;
    deploying) status=failed error="the deploy stopped before it finished${error:+: $error}" ;;
    *) status=succeeded error='' ;;
  esac
  deploy_state_write "$status" "$1" "$2" "$error"
}

# deploy_hosts_yaml: merge_and_prove.hosts of the workflow, each host's
# deploy state file on p1: p1's own, then a copy of each worker host's.
deploy_hosts_yaml() {
  local f
  printf '    p1: %s\n' "$(deploy_state_file)"
  for f in "$STAGE0_HOME"/deploy-hosts/*; do
    [[ -s $f ]] || continue
    printf '    %s: %s\n' "${f##*/}" "$STAGE0_HOME/hosts/${f##*/}/deploy-state.json"
  done
}

# deploy_host_add NAME DEST: p1 copies worker host NAME's deploy state
# over ssh DEST.
deploy_host_add() {
  mkdir -p "$STAGE0_HOME/deploy-hosts"
  printf '%s\n' "$2" > "$STAGE0_HOME/deploy-hosts/$1"
}

# deploy_host_remove NAME: p1 no longer copies NAME's deploy state.
deploy_host_remove() {
  rm -rf "${STAGE0_HOME:?}/deploy-hosts/$1" "${STAGE0_HOME:?}/hosts/$1"
}

# deploy_state_pull: on p1, copy each worker host's deploy state over SSH
# (ssh -o BatchMode=yes, as worker-host.sh does). A host that cannot be
# reached, or whose file is not a deploy state, keeps its last copy, which
# Sortie reports once it is an hour old. Never fails.
deploy_state_pull() {
  local f name dest dir
  for f in "$STAGE0_HOME"/deploy-hosts/*; do
    [[ -s $f ]] || continue
    name=${f##*/} dest=$(head -n 1 "$f") dir=$STAGE0_HOME/hosts/${f##*/}
    mkdir -p "$dir"
    if ! ssh -o BatchMode=yes -o ConnectTimeout=10 "$dest" cat "$DEPLOY_STATE_WORKER_HOME/deploy-state.json" \
      < /dev/null 2> /dev/null | head -c 65536 > "$dir/deploy-state.json.tmp"; then
      echo "deploy-state: could not read $name's deploy state over ssh $dest; its last copy stays" >&2
      rm -f "$dir/deploy-state.json.tmp"
      continue
    fi
    if [[ -z $(deploy_state_field "$dir/deploy-state.json.tmp" status) || -z $(deploy_state_field "$dir/deploy-state.json.tmp" updated_at) ]]; then
      echo "deploy-state: $name's deploy state over ssh $dest is not one; its last copy stays" >&2
      rm -f "$dir/deploy-state.json.tmp"
      continue
    fi
    mv "$dir/deploy-state.json.tmp" "$dir/deploy-state.json"
  done
  return 0
}
