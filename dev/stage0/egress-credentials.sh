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
# No GitHub credential: each attempt's GitHub requests get that attempt's
# own tokens from the egress gate (spec store job-scoped-github-tokens), so
# neither github-token nor `gh auth token` reaches a job.
egress_credentials() {
  CP_EGRESS_ANTHROPIC=$(STAGE0_HOME=$STAGE0_HOME "$KIT/anthropic-token.py") || {
    echo "no live Claude subscription token: run claude setup-token into $STAGE0_HOME/claude-oauth-token" >&2
    return 1
  }
  export CP_EGRESS_ANTHROPIC
}
