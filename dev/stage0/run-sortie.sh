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
#                            login). Sortie reads it again at each launch
#                            (the workflow's command source); this start
#                            value only serves the agent kinds' own checks.
# No other model provider is used: nothing here reads an OpenRouter or API key.
#   STAGE0_SORTIE_TOKEN      $STAGE0_HOME/sortie-token
#   STAGE0_SUPERVISOR_TOKEN  $STAGE0_HOME/supervisor-token (supervisor.sh), for
#                            server.supervisor_token; Sortie hands it to the
#                            supervisor session only
#   SORTIE_NOTIFY_SECRET     $STAGE0_HOME/notify-credential, mode 0600, when
#                            stage0.env names the operator's channel
# Git reaches GitHub over HTTPS with that same token, through a credential
# helper in a kit-owned global config (GIT_CONFIG_GLOBAL): no SSH agent and
# no change to the operator's git settings.
set -euo pipefail

STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=/dev/null
. "$STAGE0_HOME/stage0.env"
die() { printf 'run-sortie.sh: %s\n' "$*" >&2; exit 1; }

export PATH=$STAGE0_HOME/bin:$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin
# The supervisor session runs on this host with the host's opencode and gh,
# found where the last stage0.sh found them (it records its PATH), after the
# kit's own tools. Its opencode reads the kit's configuration on top of the
# operator's: the stories' model and the Anthropic sign-in plugin
# (supervisor-opencode.json).
if [[ -s $STAGE0_HOME/path ]]; then PATH=$PATH:$(cat "$STAGE0_HOME/path"); fi
export OPENCODE_CONFIG=$STAGE0_KIT_ROOT/dev/stage0/supervisor-opencode.json
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
export STAGE0_SUPERVISOR_TOKEN
STAGE0_SUPERVISOR_TOKEN=$(cat "$STAGE0_HOME/supervisor-token")
# The operator's channel (notify-channel.sh): its webhook URL or bot token,
# from the file only the operator writes, for the workflow's notifications.
if [[ -n ${STAGE0_NOTIFY_KIND:-} ]]; then
  # shellcheck source=notify-channel.sh
  . "$(dirname "$0")/notify-channel.sh"
  notify_credential "$STAGE0_HOME/notify-credential" || die "the $STAGE0_NOTIFY_KIND channel has no credential"
  export SORTIE_NOTIFY_SECRET
  SORTIE_NOTIFY_SECRET=$(cat "$STAGE0_HOME/notify-credential")
fi

export CLAUDE_CODE_OAUTH_TOKEN
CLAUDE_CODE_OAUTH_TOKEN=$("$(dirname "$0")/anthropic-token.py") \
  || die "no live Claude subscription token: run claude setup-token into $STAGE0_HOME/claude-oauth-token"
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
