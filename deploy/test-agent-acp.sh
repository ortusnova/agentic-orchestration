#!/usr/bin/env bash
# Checks the pack's agent-acp launcher against a fake opencode: the
# credential placeholder goes into opencode's store, and the opencode that
# runs the agent, whose shell commands inherit its environment, never holds
# it. A placeholder in a command's output (an env dump in a test traceback)
# reaches the model, and smolvm refuses every later request of the session.
# It also tells opencode where to record the run's token usage, and drops
# the file an earlier run left, for either harness.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT
failed=0

cat > "$fake/opencode" <<'EOF'
#!/bin/sh
case "$1" in
  auth)
    if [ "$2" = import ]; then cp "$4" "$FAKE_DIR/imported"; fi ;;
  acp)
    env > "$FAKE_DIR/acp-env" ;;
esac
EOF
# shellcheck disable=SC2016 # expanded by the fake
printf '#!/bin/sh\nenv > "$FAKE_DIR/claude-env"\n' > "$fake/claude-agent-acp"
chmod +x "$fake/opencode" "$fake/claude-agent-acp"

usage=$fake/home/.local/state/sortie/usage.json
mkdir -p "$(dirname "$usage")"
echo '{"input_tokens": 1}' > "$usage"
token=placeholder-$$-value
HOME=$fake/home FAKE_DIR=$fake PATH=$fake:$PATH SORTIE_RULE=build CLAUDE_CODE_OAUTH_TOKEN=$token \
  sh "$KIT/pack/agent-acp" 2> /dev/null

check() { # check DESCRIPTION COMMAND...
  if "${@:2}"; then echo "ok: $1"; else echo "FAIL: $1"; failed=1; fi
}

check "the store import carries the token" grep -q "\"access\":\"$token\"" "$fake/imported"
check "opencode acp ran" test -f "$fake/acp-env"
check "opencode acp's environment lacks CLAUDE_CODE_OAUTH_TOKEN" \
  sh -c "! grep -q '^CLAUDE_CODE_OAUTH_TOKEN=' '$fake/acp-env'"
check "opencode acp's environment lacks the token's value" \
  sh -c "! grep -q '$token' '$fake/acp-env'"
check "opencode acp is told where to record the run's token usage" \
  grep -qx "SORTIE_USAGE_FILE=$usage" "$fake/acp-env"
check "an earlier run's usage file is gone before opencode starts" test ! -e "$usage"

echo '{"input_tokens": 1}' > "$usage"
HOME=$fake/home FAKE_DIR=$fake PATH=$fake:$PATH SORTIE_RULE=claude-build CLAUDE_CODE_OAUTH_TOKEN=$token \
  sh "$KIT/pack/agent-acp" 2> /dev/null
check "a claude rule runs claude-agent-acp" test -f "$fake/claude-env"
check "a Claude Code run leaves no earlier run's usage file" test ! -e "$usage"

echo '{"input_tokens": 1}' > "$usage"
HOME=$fake/home PATH=$fake:$PATH SORTIE_RULE=build sh "$KIT/pack/agent-acp" --which > /dev/null
check "agent-acp --which, which agent-usage runs, leaves the usage file" test -e "$usage"
exit $failed
