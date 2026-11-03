#!/usr/bin/env bash
# Checks the pack's install-opencode-plugins.sh against a fake opencode and
# runuser: every plugin in opencode.json is installed in the pack build, into
# the agent user's cache, so opencode never installs one at a session's start.
# opencode gives plugins 5 s to activate, then answers 503
# (ServiceUnavailableError, "Location plugins did not finish activating"),
# and installing this one from npm takes longer: agent-acp's credential swap
# and ACP session/new failed on every run in a fresh machine (ONS-103).
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT
failed=0

mkdir -p "$fake/bin" "$fake/home"
cat > "$fake/bin/runuser" <<'EOF'
#!/bin/sh
[ "$1" = -u ] || exit 64
echo "$2" >> "$FAKE_DIR/runuser"
shift 2
[ "$1" = -- ] && shift
exec "$@"
EOF
cat > "$fake/bin/opencode" <<'EOF'
#!/bin/sh
echo "$* HOME=$HOME XDG_DATA_HOME=${XDG_DATA_HOME:-} XDG_STATE_HOME=${XDG_STATE_HOME:-}" >> "$FAKE_DIR/opencode"
if [ "$1 $2" = "plugin add" ]; then
  mkdir -p "$HOME/.cache/opencode/npm/$3/.staging-1"
  [ -n "${FAKE_UNFINISHED:-}" ] || mkdir -p "$HOME/.cache/opencode/npm/$3/1793711047743"
  mkdir -p "$XDG_DATA_HOME/opencode" && : > "$XDG_DATA_HOME/opencode/opencode.db"
fi
EOF
chmod +x "$fake/bin/runuser" "$fake/bin/opencode"
cat > "$fake/opencode.json" <<'EOF'
{
  "model": "anthropic/claude-opus-5-5",
  "plugins": [
    "@ex-machina/opencode-anthropic-auth@2.0.0-next.6",
    "opencode-other@1.2.3"
  ]
}
EOF

check() { # check DESCRIPTION COMMAND...
  if "${@:2}"; then echo "ok: $1"; else echo "FAIL: $1"; failed=1; fi
}

check "it installs every plugin" \
  env FAKE_DIR="$fake" PATH="$fake/bin:$PATH" sh "$KIT/pack/install-opencode-plugins.sh" agent "$fake/home" "$fake/opencode.json"
check "the first plugin is added" grep -q "^plugin add @ex-machina/opencode-anthropic-auth@2.0.0-next.6 HOME=$fake/home " "$fake/opencode"
check "the second plugin is added" grep -q "^plugin add opencode-other@1.2.3 HOME=$fake/home " "$fake/opencode"
check "opencode runs as the agent user" sh -c "[ \"\$(sort -u '$fake/runuser')\" = agent ]"
check "opencode's data and state stay out of the image" \
  sh -c "! grep -qE 'XDG_(DATA|STATE)_HOME=( |\$|$fake/home)' '$fake/opencode' && test ! -e '$fake/home/.local'"

rm -rf "${fake:?}/home" "$fake/opencode" "$fake/runuser"
mkdir -p "$fake/home"
check "an install opencode did not finish fails the build" \
  sh -c "! FAKE_UNFINISHED=1 FAKE_DIR='$fake' PATH='$fake/bin':\$PATH sh '$KIT/pack/install-opencode-plugins.sh' agent '$fake/home' '$fake/opencode.json' 2>/dev/null"
exit $failed
