# shellcheck shell=bash
# The credentials nono holds for the egress flow (egress-profile.json), read
# on p1 at start and kept in the environment of the process that runs nono;
# never written to disk, and never sent to a worker host. Sourced by
# run-nono.sh and test-egress-live.sh, with KIT and STAGE0_HOME set.
#   STAGE0_EGRESS_ANTHROPIC     the Claude subscription token Sortie uses too
#                               (anthropic-token.py: claude-oauth-token, else
#                               the freshest live login)
#   STAGE0_EGRESS_GITHUB        $STAGE0_HOME/github-token, else `gh auth token`
#   STAGE0_EGRESS_GITHUB_BASIC  x-access-token:<that token>, for git over HTTPS
egress_credentials() {
  STAGE0_EGRESS_ANTHROPIC=$(STAGE0_HOME=$STAGE0_HOME "$KIT/anthropic-token.py") || {
    echo "no live Claude subscription token: run claude setup-token into $STAGE0_HOME/claude-oauth-token" >&2
    return 1
  }
  if [[ -s $STAGE0_HOME/github-token ]]; then
    STAGE0_EGRESS_GITHUB=$(cat "$STAGE0_HOME/github-token")
  else
    STAGE0_EGRESS_GITHUB=$(gh auth token) || { echo "gh has no token: run gh auth login" >&2; return 1; }
  fi
  STAGE0_EGRESS_GITHUB_BASIC=x-access-token:$STAGE0_EGRESS_GITHUB
  export STAGE0_EGRESS_ANTHROPIC STAGE0_EGRESS_GITHUB STAGE0_EGRESS_GITHUB_BASIC
}
