#!/bin/sh
# Installs the stage-0 agent harness in a pack build, for the user the
# pack's agent runs as: Claude Code behind ACP, the agent-acp launcher, and
# both harnesses' policy files. Shared by the stage-0 pack (agent.Dockerfile,
# as root) and the homelab pack (homelab-pack/Dockerfile, as brian), so the
# harness and its pin live in one place.
#
#   install-harness.sh USER HOME
#
# The token is a credential binding's placeholder (CLAUDE_CODE_OAUTH_TOKEN),
# never in the image. Policy only: the VM is the sandbox, so neither harness
# asks before it acts.
set -eu
user=$1 home=$2
dir=$(dirname "$0")
npm install -g --no-fund --no-audit @agentclientprotocol/claude-agent-acp@0.99.1
command -v claude-agent-acp
install -m 0755 "$dir/agent-acp" /usr/local/bin/agent-acp
install -d -o "$user" -g "$user" "$home/.claude" "$home/.config" "$home/.config/opencode"
install -m 0644 -o "$user" -g "$user" "$dir/claude-settings.json" "$home/.claude/settings.json"
install -m 0644 -o "$user" -g "$user" "$dir/opencode.json" "$home/.config/opencode/opencode.json"
agent-acp --which
