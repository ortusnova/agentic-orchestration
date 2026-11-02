#!/usr/bin/env bash
# Check the kit's review gate (agent-platform/run-supervision) against Sortie
# itself: compile sortie-test/stage0_review_test.go into Sortie's
# orchestrator package with `go test -overlay`, so it loads WORKFLOW.md
# with Sortie's own loader and dispatch validation, and
# runs Sortie's exit, auto-merge and merge-completion code against its fake
# tracker and forge. Sortie is the sortie/ submodule, unless SORTIE_SRC names
# another checkout; without either the test skips, and with SORTIE_SRC set a
# missing checkout fails.
set -euo pipefail

KIT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
src=${SORTIE_SRC:-$KIT/../../sortie}
if [[ ! -f $src/internal/orchestrator/plan_control.go ]]; then
  if [[ -n ${SORTIE_SRC:-} ]]; then
    echo "test-review-gate: no Sortie checkout at $SORTIE_SRC" >&2
    exit 1
  fi
  echo "test-review-gate: skipped, no Sortie checkout: run git submodule update --init, or set SORTIE_SRC"
  exit 0
fi
src=$(cd "$src" && pwd)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '{"Replace":{"%s":"%s"}}\n' \
  "$src/internal/orchestrator/stage0_review_test.go" "$KIT/sortie-test/stage0_review_test.go" > "$tmp/overlay.json"

cd "$src"
STAGE0_KIT=$KIT GOWORK=off go test -count=1 -overlay "$tmp/overlay.json" -run '^TestStage0_' ./internal/orchestrator "$@"
