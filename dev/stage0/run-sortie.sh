#!/usr/bin/env bash
# Start the stage-0 Sortie (the ortus-stage0-sortie user unit runs this);
# `run-sortie.sh validate` checks the rendered workflow with the same environment.
#
# Reads every secret at start and keeps it in this process's environment
# only; none is written to disk by this script:
#   STAGE0_HULY_TOKEN        a Huly login token (dev/huly_project.py)
#   STAGE0_GITHUB_TOKEN      $STAGE0_HOME/github-token when present (a token
#                            scoped to the repositories Sortie pushes to), else
#                            the operator's gh token (`gh auth token`)
#   CLAUDE_CODE_OAUTH_TOKEN  the operator's Claude subscription, for both
#                            harnesses: the freshest live token that
#                            anthropic-token.py finds (a `claude setup-token`
#                            token, else opencode's or Claude Code's OAuth
#                            login). Its expiry goes to $STAGE0_HOME/token-expiry,
#                            which ortus-stage0-token.timer watches.
# No other model provider is used: nothing here reads an OpenRouter or API key.
#   STAGE0_SORTIE_TOKEN      $STAGE0_HOME/sortie-token
# Git reaches GitHub over HTTPS with that same token, through a credential
# helper in a kit-owned global config (GIT_CONFIG_GLOBAL): no SSH agent and
# no change to the operator's git settings.
set -euo pipefail

STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
die() { printf 'run-sortie.sh: %s\n' "$*" >&2; exit 1; }

export PATH=$STAGE0_HOME/bin:$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin
export STAGE0_HULY_TOKEN
STAGE0_HULY_TOKEN=$(python3 "$STAGE0_KIT_ROOT/dev/huly_project.py" "$STAGE0_HULY_URL" "$STAGE0_HULY_WORKSPACE" \
  "$STAGE0_HULY_EMAIL" "$STAGE0_HULY_PASSWORD" "$STAGE0_HULY_PROJECT") || die "could not log in to Huly"
export STAGE0_GITHUB_TOKEN
if [[ -s $STAGE0_HOME/github-token ]]; then
  STAGE0_GITHUB_TOKEN=$(cat "$STAGE0_HOME/github-token")
else
  STAGE0_GITHUB_TOKEN=$(gh auth token) || die "gh has no token: run gh auth login"
fi
export STAGE0_SORTIE_TOKEN
STAGE0_SORTIE_TOKEN=$(cat "$STAGE0_HOME/sortie-token")

export CLAUDE_CODE_OAUTH_TOKEN
CLAUDE_CODE_OAUTH_TOKEN=$("$(dirname "$0")/anthropic-token.py") \
  || die "no live Claude subscription token: run claude setup-token into $STAGE0_HOME/claude-oauth-token"
"$(dirname "$0")/anthropic-token.py" --expiry > "$STAGE0_HOME/token-expiry"
export IS_SANDBOX=1
unset ANTHROPIC_API_KEY OPENROUTER_API_KEY

# Sortie's mirror git drops GIT_CONFIG_* but reads the global config file,
# so the helper lives in a config file of this kit's own (GIT_CONFIG_GLOBAL),
# which includes the operator's. It holds no secret: the helper reads the
# token from this process's environment.
cat > "$STAGE0_HOME/gitconfig" <<'EOF_GIT'
[include]
	path = ~/.gitconfig
[credential "https://github.com"]
	helper =
	helper = "!f() { test \"$1\" = get && printf 'username=x-access-token\\npassword=%s\\n' \"$STAGE0_GITHUB_TOKEN\"; }; f"
EOF_GIT
export GIT_CONFIG_GLOBAL=$STAGE0_HOME/gitconfig

cd "$STAGE0_HOME/sortie"
if [[ ${1:-} == validate ]]; then exec sortie validate WORKFLOW.md; fi
exec sortie WORKFLOW.md
