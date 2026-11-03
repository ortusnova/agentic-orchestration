#!/usr/bin/env bash
# Deploy, move and remove the preview of a review: ui story's pull request
# (spec store agent-platform/run-supervision, "A UI story is built before it
# is approved, and its merge is the approval"). A preview is the branch's
# workbench, read-only toward the tracker (WORKBENCH_SYNC=0), against live
# data: the live spec store, Sortie's plan graph, and a copy of the live
# workbench's registry taken at each deploy. `workbench preview-sync`
# (run-previews.sh, every minute) decides when, and calls this:
#
#   preview.sh up IDENTIFIER BRANCH SHA PORT ADDRESS
#       check out SHA of BRANCH in $STAGE0_HOME/previews/IDENTIFIER/src, build
#       its workbench, and (re)start ortus-stage0-preview@IDENTIFIER on
#       127.0.0.1:PORT; ADDRESS is the name it is reached at
#   preview.sh down IDENTIFIER
#       stop it and remove its checkout, its home and its route
#
# Each change rewrites $STAGE0_HOME/previews/routes, one "HOST PORT" line per
# preview, for the proxy that serves the live workbench's TLS name, and then
# runs STAGE0_PREVIEW_ROUTES_HOOK (stage0.env) when it is set, for example
# to reload that proxy.
set -euo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
if [[ -s $STAGE0_HOME/path ]]; then
  PATH=$(cat "$STAGE0_HOME/path")
  export PATH
fi
say() { echo "preview: $*" >&2; }
die() { say "$*"; exit 1; }
# The repository the previews' checkouts come from: the live kit's.
REPO=${STAGE0_PREVIEW_REPO:-$STAGE0_KIT_ROOT}
PREVIEWS=$STAGE0_HOME/previews
WAIT=${STAGE0_PREVIEW_WAIT:-60}

cmd=${1:-} ident=${2:-}
[[ $cmd == up && $# -eq 6 || $cmd == down && $# -eq 2 ]] \
  || die "usage: $0 up IDENTIFIER BRANCH SHA PORT ADDRESS | down IDENTIFIER"
[[ $ident =~ ^[A-Za-z0-9]+-[0-9]+$ ]] || die "not a tracker identifier: $ident"
dir=$PREVIEWS/$ident
unit=ortus-stage0-preview@$ident.service

routes() {
  local out=$PREVIEWS/routes.tmp env
  mkdir -p "$PREVIEWS"
  : > "$out"
  for env in "$PREVIEWS"/*/preview.env; do
    [[ -e $env ]] || continue
    (
      # shellcheck source=/dev/null
      . "$env"
      printf '%s %s\n' "$PREVIEW_HOST" "$PREVIEW_PORT"
    ) >> "$out"
  done
  sort -o "$out" "$out"
  mv "$out" "$PREVIEWS/routes"
  if [[ -n ${STAGE0_PREVIEW_ROUTES_HOOK:-} ]]; then
    bash -c "$STAGE0_PREVIEW_ROUTES_HOOK" || say "STAGE0_PREVIEW_ROUTES_HOOK failed"
  fi
}

if [[ $cmd == down ]]; then
  systemctl --user disable -q --now "$unit" 2>/dev/null || true
  if [[ -e $dir/src ]]; then
    git -C "$REPO" worktree remove --force "$dir/src" 2>/dev/null || rm -rf "$dir/src"
    git -C "$REPO" worktree prune
  fi
  git -C "$REPO" update-ref -d "refs/previews/$ident" 2>/dev/null || true
  rm -rf "$dir"
  routes
  say "$ident removed"
  exit 0
fi

branch=$3 sha=$4 port=$5 address=$6
[[ $sha =~ ^[0-9a-f]{40}$ ]] || die "not a commit: $sha"
if ! [[ $port =~ ^[0-9]+$ ]] || (( port <= 1024 || port >= 65536 )); then die "not a port: $port"; fi
[[ $port != "${STAGE0_WORKBENCH_PORT:-}" && $port != "${STAGE0_SORTIE_PORT:-}" ]] || die "port $port is the live kit's"
git check-ref-format --branch "$branch" > /dev/null || die "not a branch: $branch"
host=${address#*://}
host=${host%%/*}
host=${host%%:*}
[[ $host =~ ^[A-Za-z0-9.-]+$ ]] || die "not an address: $address"

say "$ident: deploying $branch at ${sha:0:12} on 127.0.0.1:$port ($address)"
git -C "$REPO" fetch -q origin "+refs/heads/$branch:refs/previews/$ident"
git -C "$REPO" cat-file -e "$sha^{commit}" || die "$branch has no commit $sha"
mkdir -p "$dir"
if [[ -e $dir/src ]]; then
  git -C "$dir/src" checkout -q --detach "$sha"
else
  git -C "$REPO" worktree add -q --detach "$dir/src" "$sha"
fi

wb=$dir/src/workbench
(cd "$wb" && uv sync -q) || die "$ident: uv sync failed at ${sha:0:12}"
lock=$(sha256sum "$wb/frontend/package-lock.json" | cut -d' ' -f1)
(cd "$wb/frontend" \
  && { [[ -d node_modules && $(cat "$dir/frontend.lock" 2>/dev/null) == "$lock" ]] || npm ci --silent; } \
  && npx vite build --logLevel warn) || die "$ident: the frontend build failed at ${sha:0:12}"
echo "$lock" > "$dir/frontend.lock"

# Live data: the live workbench's approvals and records, as they are now.
mkdir -p "$dir/home" "$dir/sessions"
live_db=$STAGE0_HOME/home/workbench.db
if [[ -s $live_db ]]; then
  rm -f "$dir/home/workbench.db"
  if command -v python3 > /dev/null; then
    python3 - "$live_db" "$dir/home/workbench.db" <<'EOF'
import sqlite3, sys
src = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
dst = sqlite3.connect(sys.argv[2])
src.backup(dst)
dst.close()
src.close()
EOF
  else
    cp "$live_db" "$dir/home/workbench.db"
  fi
fi
(umask 077; cat > "$dir/preview.env" <<EOF
PREVIEW_PORT=$port
PREVIEW_HOST=$host
PREVIEW_ADDRESS=$address
PREVIEW_BRANCH=$branch
PREVIEW_SHA=$sha
EOF
)

systemctl --user enable -q "$unit"
systemctl --user restart "$unit"
for _ in $(seq "$WAIT"); do
  curl -sf -o /dev/null "http://127.0.0.1:$port/api/auth/mode" && break
  sleep 1
done
curl -sf -o /dev/null "http://127.0.0.1:$port/api/auth/mode" || die "$ident did not start: journalctl --user -u $unit"
routes
say "$ident: $address serves ${sha:0:12}"
