#!/usr/bin/env bash
# Checks store-sync.sh against throwaway repositories: it fast-forwards a clean
# store, and leaves one alone when moving it could lose work.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home GIT_CONFIG_GLOBAL=/dev/null
mkdir -p "$STAGE0_HOME"
g() { git -c user.name=t -c user.email=t@t -c init.defaultBranch=main "$@"; }
failed=0

setup() { # a remote, a store cloned from it, and a new commit on the remote
  rm -rf "$tmp/remote" "$tmp/up" "$tmp/store" "$STAGE0_HOME/store-sync.state"
  g init -q --bare "$tmp/remote"
  g clone -q "$tmp/remote" "$tmp/up" 2> /dev/null
  echo one > "$tmp/up/plan"; g -C "$tmp/up" add plan; g -C "$tmp/up" commit -qm one; g -C "$tmp/up" push -q origin main
  g clone -q "$tmp/remote" "$tmp/store"
  echo two > "$tmp/up/plan"; g -C "$tmp/up" commit -qam two; g -C "$tmp/up" push -q origin main
}

check() { # check NAME WANT(moved|held) [REASON]
  local out got=held
  out=$("$KIT/store-sync.sh" "$tmp/store" 2>&1) || { echo "FAIL: $1: store-sync.sh exited non-zero: $out"; failed=1; return; }
  [[ $(cat "$tmp/store/plan") == two ]] && got=moved
  if [[ $got == "$2" && ( -z ${3:-} || $out == *"$3"* ) ]]; then
    echo "ok: $1 -> $got"
  else
    echo "FAIL: $1 -> $got ($out), want $2 ${3:-}"
    failed=1
  fi
}

setup; check "a clean store" moved
setup; echo edit > "$tmp/store/plan"; check "an edited tracked file" held "uncommitted changes"
setup; echo note > "$tmp/store/notes"; g -C "$tmp/store" add notes; g -C "$tmp/store" commit -qm local; check "a local commit" held "commits that origin/main does not"
setup; g -C "$tmp/store" checkout -q --detach; check "a detached HEAD" held "HEAD is detached"
setup; g -C "$tmp/store" checkout -q -b work; check "a branch without upstream" held "no upstream"
setup; echo stray > "$tmp/store/untracked"; check "an untracked file" moved
setup; "$KIT/store-sync.sh" "$tmp/store" > /dev/null 2>&1; check "already current" moved

# The reason is said once per state, not every minute.
setup; echo edit > "$tmp/store/plan"
"$KIT/store-sync.sh" "$tmp/store" 2> /dev/null
again=$("$KIT/store-sync.sh" "$tmp/store" 2>&1)
if [[ -z $again ]]; then echo "ok: a held store is reported once"; else echo "FAIL: reported again: $again"; failed=1; fi

exit $failed
