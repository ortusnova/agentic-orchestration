# shellcheck shell=bash
# The credentials nono holds for the egress flow, read on p1 at start and
# kept in the environment of the process that runs nono; never written to
# disk, and never sent to a worker host. The names are those
# control-plane/nono/egress-profile.json reads, as the compose project's
# podman secrets give them. Sourced by run-nono.sh and test-egress-live.sh,
# with KIT and STAGE0_HOME set.
#   CP_EGRESS_ANTHROPIC     the Claude subscription token Sortie uses too
#                           (anthropic-token.py: claude-oauth-token, else
#                           the freshest live login)
#   CP_EGRESS_GITHUB        $STAGE0_HOME/github-token, else `gh auth token`,
#                           for GitHub's API (the github_api credential)
egress_credentials() {
  CP_EGRESS_ANTHROPIC=$(STAGE0_HOME=$STAGE0_HOME "$KIT/anthropic-token.py") || {
    echo "no live Claude subscription token: run claude setup-token into $STAGE0_HOME/claude-oauth-token" >&2
    return 1
  }
  if [[ -s $STAGE0_HOME/github-token ]]; then
    CP_EGRESS_GITHUB=$(cat "$STAGE0_HOME/github-token")
  else
    CP_EGRESS_GITHUB=$(gh auth token) || { echo "gh has no token: run gh auth login" >&2; return 1; }
  fi
  export CP_EGRESS_ANTHROPIC CP_EGRESS_GITHUB
}
