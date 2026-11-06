#!/usr/bin/env bash
# Run a story's declared post-deploy check on this host and post its result
# to Sortie (spec store agent-platform/supervisor-autonomy, design D15,
# point 2). deploy.sh runs it at the end of every tick, after a deploy and
# on its timer alike, under stage0.sh's lock; it never fails the deploy.
#
#   1. Read Sortie's state (GET /api/v1/state) for a story in merge-and-prove
#      in phase proving whose post_deploy_check names this host in hosts.
#   2. Run the check once per merge commit and host: the executable
#      checks/<check> (or checks/<check>.<ext>) of the kit this host runs,
#      with its args, under a timeout (STAGE0_CHECK_TIMEOUT seconds, 900 by
#      default). Only once this host runs a commit that holds the merge.
#   3. Post the result to POST /api/v1/<identifier>/post-deploy-check with
#      Sortie's control token: proven (exit 0) or failed, the merge commit,
#      the host, the exit code and the last 200 lines of its output. Sortie
#      writes it on the pull request as the story's live-proof record.
#
# A host that posted for a merge commit does not run its check again. A
# result Sortie did not take (unreachable, or any answer but 2xx or 409)
# is kept and posted again at the next tick, without a new run; a 409 is
# Sortie refusing it (another commit, story or host), and is not posted
# again. When Sortie's state cannot be read nothing runs: no result is
# ever made up.
#
#   $STAGE0_HOME/post-deploy-checks/<merge>.out      the check's whole output
#   $STAGE0_HOME/post-deploy-checks/<merge>.result   its result, not yet taken by Sortie
#   $STAGE0_HOME/post-deploy-checks/<merge>.posted   its result, taken (or .refused, refused)
#
# This host is p1 on the control profile, and STAGE0_WORKER_NAME (else
# hostname -s) on a worker host. p1 reaches its own Sortie on
# STAGE0_SORTIE_PORT; a worker host only on STAGE0_SORTIE_URL in its
# stage0.env, with the control token in $STAGE0_HOME/sortie-token, and runs
# no check without them. Needs curl and python3 (stage0.sh checks both).
set -uo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
LIVE=${STAGE0_LIVE_KIT:-$STAGE0_HOME/kit}
if [[ -r $STAGE0_HOME/stage0.env ]]; then
  # shellcheck source=/dev/null
  . "$STAGE0_HOME/stage0.env"
fi
STAGE0_PROFILE=${STAGE0_PROFILE:-control}
CHECKS=$LIVE/dev/stage0/checks
TIMEOUT=${STAGE0_CHECK_TIMEOUT:-900}
TAIL_LINES=200
AGENT=ortus-stage0-post-deploy-checks
dir=$STAGE0_HOME/post-deploy-checks
say() { echo "post-deploy-checks: $*" >&2; }

if [[ $STAGE0_PROFILE == control ]]; then
  host=p1
  url=${STAGE0_SORTIE_URL:-${STAGE0_SORTIE_PORT:+http://127.0.0.1:$STAGE0_SORTIE_PORT}}
else
  host=${STAGE0_WORKER_NAME:-$(hostname -s)}
  url=${STAGE0_SORTIE_URL:-}
fi
url=${url%/}
[[ -n $url ]] || exit 0
token=$(cat "$STAGE0_HOME/sortie-token" 2> /dev/null || true)
[[ -n $token ]] || { say "no Sortie token in $STAGE0_HOME/sortie-token: no post-deploy check runs on $host"; exit 0; }
mkdir -p "$dir"

# reach STATE MESSAGE: say MESSAGE only when Sortie's state was read
# otherwise at the last tick (STATE is unreachable, unreadable or read).
reach() {
  local was
  was=$(cat "$dir/sortie.state" 2> /dev/null || echo read)
  [[ $was == "$1" ]] || say "$2"
  printf '%s\n' "$1" > "$dir/sortie.state"
}

# sortie_curl ARGS...: curl Sortie with the control token, bounded.
sortie_curl() {
  printf 'Authorization: Bearer %s\n' "$token" | curl -s -A "$AGENT" -m 30 --connect-timeout 10 -H @- "$@"
}

if ! state=$(sortie_curl -f "$url/api/v1/state"); then
  reach unreachable "could not read Sortie's state at $url; trying again at the next tick"
  exit 0
fi

# The stories in proving whose check names this host, each as
# identifier, merge commit, check, the count of its args, then its args,
# every field ended by a NUL.
# shellcheck disable=SC2016 # Python, not the shell
if ! proving=$(printf '%s' "$state" | python3 -c '
import json, sys
host = sys.argv[1]
state = json.load(sys.stdin)
mp = state.get("merge_and_prove") or {}
for e in [mp.get("in_flight")] + list(mp.get("queue") or []):
    if not isinstance(e, dict) or e.get("phase") != "proving":
        continue
    c = e.get("post_deploy_check")
    if not isinstance(c, dict) or not isinstance(c.get("check"), str):
        continue
    args = c.get("args") or []
    hosts = c.get("hosts") or ["p1"]
    if host not in hosts or not all(isinstance(a, str) for a in args):
        continue
    fields = [e.get("issue_identifier") or "", e.get("merge_commit") or "", c["check"], str(len(args))] + args
    sys.stdout.write("".join(f.replace("\0", " ") + "\0" for f in fields))
' "$host" | base64 -w0); then
  reach unreadable "Sortie's state at $url is not what this kit reads; trying again at the next tick"
  exit 0
fi
reach read "read Sortie's state at $url again"

# deployed MERGE: this host runs a commit that holds MERGE, in each
# repository it deploys that knows MERGE (the kit; Sortie on p1). A merge
# no repository here knows is of one this host does not deploy, which
# Sortie counts as deployed.
deployed() {
  local repo at sortie
  sortie=$(head -n 1 "$STAGE0_HOME/sortie.commit" 2> /dev/null || true)
  for repo in "$LIVE" "${STAGE0_SORTIE_SRC:-}"; do
    [[ -n $repo && -d $repo ]] || continue
    git -C "$repo" cat-file -e "$1^{commit}" 2> /dev/null || continue
    at=HEAD
    [[ $repo != "$LIVE" ]] && { [[ -n $sortie ]] || continue; at=$sortie; }
    git -C "$repo" merge-base --is-ancestor "$1" "$at" 2> /dev/null || return 1
  done
  return 0
}

# check_file NAME: the kit's executable for check NAME, with or without
# its extension; nothing when there is none, or more than one.
check_file() {
  local f found=()
  for f in "$CHECKS/$1" "$CHECKS/$1".*; do
    [[ -f $f && -x $f ]] && found+=("$f")
  done
  (( ${#found[@]} == 1 )) && printf '%s\n' "${found[0]}"
}

# run_check ID MERGE CHECK ARGS...: run the check and write its result.
run_check() {
  local id=$1 merge=$2 check=$3 file rc=0 result=failed out=$dir/$2.out started finished
  shift 3
  started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  file=$(check_file "$check")
  if [[ -z $file ]]; then
    rc=127
    echo "post-deploy-checks: the kit at $(git -C "$LIVE" rev-parse --short=12 HEAD 2> /dev/null) has no single executable check $check under dev/stage0/checks/" > "$out"
  else
    say "$id: running $check on $host for ${merge:0:12}"
    (cd "$LIVE" && STAGE0_HOME=$STAGE0_HOME STAGE0_PROFILE=$STAGE0_PROFILE STAGE0_KIT_ROOT=$LIVE STAGE0_SORTIE_URL=$url \
      STAGE0_CHECK_HOST=$host STAGE0_CHECK_STORY=$id STAGE0_CHECK_MERGE_COMMIT=$merge \
      timeout -k 30 "$TIMEOUT" "$file" "$@" < /dev/null > "$out" 2>&1) || rc=$?
    (( rc == 124 || rc == 137 )) && echo "post-deploy-checks: $check did not finish within ${TIMEOUT}s; stopped (exit $rc)" >> "$out"
  fi
  (( rc == 0 )) && result=proven
  finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  tail -n "$TAIL_LINES" "$out" | python3 -c '
import json, sys
result, rc, host, merge, check, started, finished, *args = sys.argv[1:]
json.dump({"result": result, "merge_commit": merge, "host": host, "exit_code": int(rc),
           "output": sys.stdin.buffer.read().decode("utf-8", "replace"),
           "check": check, "args": args, "started_at": started, "finished_at": finished}, sys.stdout)
' "$result" "$rc" "$host" "$merge" "$check" "$started" "$finished" "$@" > "$dir/$merge.result.tmp" \
    && mv "$dir/$merge.result.tmp" "$dir/$merge.result"
  say "$id: $check $result on $host for ${merge:0:12} (exit $rc)"
}

# post ID MERGE: post the kept result.
post() {
  local id=$1 merge=$2 code
  code=$(sortie_curl -o "$dir/$merge.answer" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    --data-binary @"$dir/$merge.result" "$url/api/v1/$id/post-deploy-check") || code=000
  case $code in
    2??) mv "$dir/$merge.result" "$dir/$merge.posted"; say "$id: posted the result for ${merge:0:12} from $host" ;;
    409) mv "$dir/$merge.result" "$dir/$merge.refused"
      say "$id: Sortie refused the result for ${merge:0:12} from $host: $(head -c 500 "$dir/$merge.answer" 2> /dev/null)" ;;
    *) say "$id: Sortie did not take the result for ${merge:0:12} (HTTP $code); posting it again at the next tick" ;;
  esac
}

mapfile -d '' fields < <(printf '%s' "$proving" | base64 -d)
i=0
while (( i + 4 <= ${#fields[@]} )); do
  id=${fields[i]} merge=${fields[i + 1]} check=${fields[i + 2]} n=${fields[i + 3]}
  args=("${fields[@]:i + 4:n}")
  i=$((i + 4 + n))
  if [[ ! $id =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ || ! $merge =~ ^[0-9a-f]{7,64}$ || ! $check =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]]; then
    say "skipping a story in proving whose identifier, merge commit or check is not one: '$id' '$merge' '$check'"
    continue
  fi
  [[ -e $dir/$merge.posted || -e $dir/$merge.refused ]] && continue
  if [[ ! -e $dir/$merge.result ]]; then
    if ! deployed "$merge"; then
      [[ -e $dir/$merge.waiting ]] || say "$id: $host does not run ${merge:0:12} yet; its check waits"
      : > "$dir/$merge.waiting"
      continue
    fi
    rm -f "$dir/$merge.waiting"
    run_check "$id" "$merge" "$check" "${args[@]}"
  fi
  [[ -e $dir/$merge.result ]] && post "$id" "$merge"
done
exit 0
