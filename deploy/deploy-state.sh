# shellcheck shell=bash
# Each host's deploy state (spec store agent-platform/supervisor-autonomy,
# design D7): the file Sortie's internal/deploystate reads, which its
# health check (health.deploy_state) reads on p1. Each host's step of a push
# (deploy.sh --this-host, bootstrap.sh) and dev-host-apply.sh write it on
# every host; the push keeps each worker host's beside p1's (push.sh).
# Sourced by deploy.sh, bootstrap.sh, dev-host-apply.sh, worker-host.sh and
# the tests. Needs
# ORTUS_NOVA_HOME.
#
#   $ORTUS_NOVA_HOME/deploy-state.json              this host's
#   $ORTUS_NOVA_HOME/hosts/NAME/deploy-state.json   p1's copy of worker host NAME's
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
#   repositories  each repository the host deploys, keyed by owner/repo,
#                 each {branch, commit} on a line of its own (design D13):
#                 ortusnova/agentic-orchestration at commit, on main, and,
#                 on a host that builds and runs a Sortie (the control
#                 profile, p1), ortusnova/sortie at the plan-bench commit
#                 of the Sortie it runs (deploy_state_sortie)
#   updated_at    when the kit last wrote it
#
# Sortie's deploystate knows the statuses succeeded, failed and deploying,
# and refuses any other: an applied deploy is succeeded.

deploy_state_file() { echo "$ORTUS_NOVA_HOME/deploy-state.json"; }

# The repositories the kit deploys, as the forge names them.
DEPLOY_STATE_KIT_REPO=ortusnova/agentic-orchestration
DEPLOY_STATE_SORTIE_REPO=ortusnova/sortie

# The commit of the Sortie this host runs: deploy.sh records the commit of
# the Sortie checkout it built once that Sortie restarted, and removes the
# record on a worker host, which runs none.
deploy_sortie_file() { echo "$ORTUS_NOVA_HOME/sortie.commit"; }
deploy_sortie_ran() { mkdir -p "$ORTUS_NOVA_HOME" && printf '%s\n' "$1" > "$(deploy_sortie_file)"; }
deploy_sortie_retired() { rm -f "$(deploy_sortie_file)"; }

# deploy_state_sortie: the commit of the Sortie this host runs, or nothing
# on a host that runs none (a worker host, whatever Sortie checkout its
# stage0.env still names). Before deploy.sh first records it, the commit
# its Sortie checkout (ORTUS_NOVA_SORTIE_SRC) holds, never origin's.
deploy_state_sortie() {
  local file
  [[ ${ORTUS_NOVA_PROFILE:-control} == control ]] || return 0
  file=$(deploy_sortie_file)
  if [[ -s $file ]]; then
    head -n 1 "$file"
  elif [[ -n ${ORTUS_NOVA_SORTIE_SRC:-} && -d $ORTUS_NOVA_SORTIE_SRC ]]; then
    git -C "$ORTUS_NOVA_SORTIE_SRC" rev-parse -q --verify HEAD 2> /dev/null || true
  fi
}

# deploy_state_now: the time to record, UTC (ORTUS_NOVA_NOW in the tests).
deploy_state_now() { echo "${ORTUS_NOVA_NOW:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"; }

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
  local status=$1 commit=$2 main=$3 error=${4:-} file now behind=null prev sortie repos=()
  file=$(deploy_state_file)
  [[ -n $commit ]] && repos+=("$(deploy_state_repo_line "$DEPLOY_STATE_KIT_REPO" main "$commit")")
  sortie=$(deploy_state_sortie)
  [[ -n $sortie ]] && repos+=("$(deploy_state_repo_line "$DEPLOY_STATE_SORTIE_REPO" plan-bench "$sortie")")
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
    if (( ${#repos[@]} )); then
      echo '  "repositories": {'
      printf '%s\n' "${repos[@]}" | sed '$!s/$/,/'
      echo '  },'
    else
      echo '  "repositories": {},'
    fi
    printf '  "updated_at": %s\n' "$(json_string "$now")"
    echo '}'
  } > "$file.tmp" && mv "$file.tmp" "$file"
}

# deploy_state_repo_line OWNER/REPO BRANCH COMMIT: its member of repositories.
deploy_state_repo_line() {
  printf '    %s: {"branch": %s, "commit": %s}' "$(json_string "$1")" "$(json_string "$2")" "$(json_string "$3")"
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
