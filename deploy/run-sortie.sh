#!/usr/bin/env bash
# Start the stage-0 Sortie (the ortus-nova-<env>-sortie user unit runs this);
# `run-sortie.sh validate` checks the rendered workflow with the same environment.
#
# Reads every secret at start and keeps it in this process's environment
# only; none is written to disk by this script:
#   ORTUS_NOVA_HULY_TOKEN        a Huly login token (dev/huly_project.py)
#   ORTUS_NOVA_GITHUB_TOKEN      $ORTUS_NOVA_HOME/github-token when present (a token
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
#   ORTUS_NOVA_SORTIE_TOKEN      $ORTUS_NOVA_HOME/sortie-token
#   SORTIE_WORKBENCH_NOTIFY_URL  the workbench's notifications endpoint, with
#                            this host's token from
#                            $ORTUS_NOVA_HOME/notify-tokens/<host>.token, mode 0600
#   ORTUS_NOVA_SUPERVISOR_TOKEN  $ORTUS_NOVA_HOME/supervisor-token (supervisor.sh), for
#                            server.supervisor_token; Sortie hands it to the
#                            supervisor session only
#   SORTIE_NOTIFY_SECRET     $ORTUS_NOVA_HOME/notify-credential, mode 0600, when
#                            stage0.env names the operator's channel
# Git reaches GitHub over HTTPS with that same token, through a credential
# helper in a kit-owned global config (GIT_CONFIG_GLOBAL): no SSH agent and
# no change to the operator's git settings.
set -euo pipefail

# shellcheck source=names.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/names.sh"
env_load "${ORTUS_NOVA_ENV:?the unit names the environment (ORTUS_NOVA_ENV)}"
die() { printf 'run-sortie.sh: %s\n' "$*" >&2; exit 1; }

export PATH=$ORTUS_NOVA_HOME/bin:$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin
# The supervisor session runs on this host with the host's opencode and gh,
# found where the last deploy.sh found them (it records its PATH), after the
# kit's own tools. Its opencode reads the kit's configuration on top of the
# operator's: the stories' model and the Anthropic sign-in plugin
# (supervisor-opencode.json).
if [[ -s $ORTUS_NOVA_HOME/path ]]; then PATH=$PATH:$(cat "$ORTUS_NOVA_HOME/path"); fi
export OPENCODE_CONFIG=$ORTUS_NOVA_KIT_ROOT/deploy/supervisor-opencode.json
export ORTUS_NOVA_HULY_TOKEN
ORTUS_NOVA_HULY_TOKEN=$(python3 "$ORTUS_NOVA_KIT_ROOT/dev/huly_project.py" "$ORTUS_NOVA_HULY_URL" "$ORTUS_NOVA_HULY_WORKSPACE" \
  "$ORTUS_NOVA_HULY_EMAIL" "$ORTUS_NOVA_HULY_PASSWORD" "$ORTUS_NOVA_HULY_PROJECT") || die "could not log in to Huly"
export ORTUS_NOVA_GITHUB_TOKEN
if [[ -s $ORTUS_NOVA_HOME/github-token ]]; then
  ORTUS_NOVA_GITHUB_TOKEN=$(cat "$ORTUS_NOVA_HOME/github-token")
else
  ORTUS_NOVA_GITHUB_TOKEN=$(gh auth token) || die "gh has no token: run gh auth login"
fi
export ORTUS_NOVA_SORTIE_TOKEN
ORTUS_NOVA_SORTIE_TOKEN=$(cat "$ORTUS_NOVA_HOME/sortie-token")
# shellcheck source=notify-channel.sh
. "$(dirname "$0")/notify-channel.sh"
# The workbench, the default destination of the workflow's notifications.
export SORTIE_WORKBENCH_NOTIFY_URL
# p1's workbench is on this host's port, or on a worker host the tunnel's.
workbench_port=${ORTUS_NOVA_WORKBENCH_PORT:-8793}
[[ ${ORTUS_NOVA_PROFILE:-control} == worker ]] && workbench_port=$ORTUS_NOVA_WORKBENCH_TUNNEL_PORT
SORTIE_WORKBENCH_NOTIFY_URL=$(workbench_notify_url "$ORTUS_NOVA_HOME" "${ORTUS_NOVA_WORKBENCH_NOTIFY_PORT:-$workbench_port}") \
  || die "Sortie has no token for the workbench's notifications"
export ORTUS_NOVA_SUPERVISOR_TOKEN
ORTUS_NOVA_SUPERVISOR_TOKEN=$(cat "$ORTUS_NOVA_HOME/supervisor-token")
# The operator's channel (notify-channel.sh): its webhook URL or bot token,
# from the file only the operator writes, for the workflow's notifications.
if [[ -n ${ORTUS_NOVA_NOTIFY_KIND:-} ]]; then
  notify_credential "$ORTUS_NOVA_HOME/notify-credential" || die "the $ORTUS_NOVA_NOTIFY_KIND channel has no credential"
  export SORTIE_NOTIFY_SECRET
  SORTIE_NOTIFY_SECRET=$(cat "$ORTUS_NOVA_HOME/notify-credential")
fi

export CLAUDE_CODE_OAUTH_TOKEN
CLAUDE_CODE_OAUTH_TOKEN=$("$(dirname "$0")/anthropic-token.py") \
  || die "no live Claude subscription token: run claude setup-token into $ORTUS_NOVA_HOME/claude-oauth-token"
export IS_SANDBOX=1
unset ANTHROPIC_API_KEY OPENROUTER_API_KEY

# Sortie's mirror git drops GIT_CONFIG_* but reads the global config file,
# so the helper lives in a config file of this kit's own (GIT_CONFIG_GLOBAL),
# which includes the operator's. It holds no secret: the helper reads the
# token from this process's environment.
cat > "$ORTUS_NOVA_HOME/gitconfig" <<'EOF_GIT'
[include]
	path = ~/.gitconfig
[credential "https://github.com"]
	helper =
	helper = "!f() { test \"$1\" = get && printf 'username=x-access-token\\npassword=%s\\n' \"$ORTUS_NOVA_GITHUB_TOKEN\"; }; f"
EOF_GIT
export GIT_CONFIG_GLOBAL=$ORTUS_NOVA_HOME/gitconfig

cd "$ORTUS_NOVA_HOME/sortie"
if [[ ${1:-} == validate ]]; then exec sortie validate WORKFLOW.md; fi
exec sortie WORKFLOW.md
