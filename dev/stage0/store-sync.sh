#!/usr/bin/env bash
# Keep the spec store the workbench reads on what its upstream has merged,
# so no person ever pulls it: every minute (ortus-stage0-store.timer) fetch,
# then fast-forward. The workbench sees the new files by itself.
#
#   store-sync.sh [STORE]     default: $STAGE0_STORE from stage0.env
#
# The store is also a working checkout: the planning session edits it, and the
# workbench commits schema updates there. So it moves only when that loses
# nothing: on a branch with an upstream, with no change to a tracked file, and
# when the upstream contains HEAD. Otherwise it leaves the store as it is and
# says why, once per state (journalctl --user -u ortus-stage0-store).
set -euo pipefail
STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
store=${1:-}
if [[ -z $store ]]; then
  # shellcheck source=/dev/null
  . "$STAGE0_HOME/stage0.env"
  store=$STAGE0_STORE
fi
note=$STAGE0_HOME/store-sync.state

held() { # say why the store did not move, only when the reason changes
  [[ $(cat "$note" 2>/dev/null) == "$1" ]] || echo "store-sync: $store not updated: $1" >&2
  printf '%s\n' "$1" > "$note"
  exit 0
}

git -C "$store" rev-parse --git-dir > /dev/null 2>&1 || held "not a git checkout"
branch=$(git -C "$store" symbolic-ref -q --short HEAD) || held "HEAD is detached"
upstream=$(git -C "$store" rev-parse -q --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null) \
  || held "branch $branch has no upstream"
git -C "$store" fetch -q --prune "${upstream%%/*}" || held "fetching ${upstream%%/*} failed"
git -C "$store" diff --quiet HEAD -- || held "it has uncommitted changes to tracked files"
[[ $(git -C "$store" rev-parse HEAD) == "$(git -C "$store" rev-parse '@{upstream}')" ]] && { rm -f "$note"; exit 0; }
git -C "$store" merge-base --is-ancestor HEAD '@{upstream}' || held "$branch has commits that $upstream does not"
out=$(git -C "$store" merge -q --ff-only '@{upstream}' 2>&1) || held "the fast-forward failed: ${out//$'\n'/ }"
rm -f "$note"
echo "store-sync: $store is at $(git -C "$store" log -1 --format='%h %s')" >&2
