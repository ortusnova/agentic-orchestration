#!/usr/bin/env bash
# Checks that both stage-0 workflows, rendered as stage0.sh renders them, have
# Sortie read the Claude token at each launch from the kit's own
# anthropic-token.py, and that only the homelab profile limits it to the
# setup token.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$KIT/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0

render() { # render WORKFLOW OUT: every variable the template names gets a value
  local vars
  # shellcheck disable=SC2016 # literal ${VAR} patterns, not expansions
  vars=$(grep -o '\${[A-Z0-9_]*}' "$1" | sort -u | tr -d '${}')
  # shellcheck disable=SC2046 # one assignment per variable
  env $(for v in $vars; do printf '%s=x ' "$v"; done) STAGE0_KIT="$KIT" \
    python3 "$ROOT/dev/forge/render.py" "$1" "$2"
}

check() { # check NAME WORKFLOW WANT_ARGS
  render "$KIT/$2" "$tmp/$2"
  local line command
  line=$(grep -E '^\s*- \{name: anthropic,' "$tmp/$2") || { echo "FAIL: $1: no anthropic credential"; failed=1; return; }
  command=$(sed -E 's/.*command: "([^"]*)".*/\1/' <<< "$line")
  if [[ $command == "$KIT/anthropic-token.py$3" && -x ${command%% *} ]]; then
    echo "ok: $1"
  else
    echo "FAIL: $1: command is '$command'"; failed=1
  fi
}

check "the control workflow reads the freshest login at each launch" WORKFLOW.md ""
check "the homelab workflow reads only the setup token at each launch" WORKFLOW-homelab.md " --setup-token-only"

exit $failed
