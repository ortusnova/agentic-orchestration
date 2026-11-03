#!/usr/bin/env bash
# Checks the pack's agent-acp launcher against a fake opencode: the
# credential placeholder goes into opencode's store, and the opencode that
# runs the agent, whose shell commands inherit its environment, never holds
# it. A placeholder in a command's output (an env dump in a test traceback)
# reaches the model, and smolvm refuses every later request of the session.
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
chmod +x "$fake/opencode"

token=placeholder-$$-value
FAKE_DIR=$fake PATH=$fake:$PATH SORTIE_RULE=build CLAUDE_CODE_OAUTH_TOKEN=$token \
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
exit $failed
