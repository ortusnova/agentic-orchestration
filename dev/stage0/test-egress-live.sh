#!/usr/bin/env bash
# On p1: the nono scenarios of the egress flow against the real thing
# (control-plane/internal/egress/live_test.go): the pinned nono with the
# control plane's egress profile and the credentials on p1, through the
# egress gate, with a fake Sortie for introspection. Calls api.anthropic.com
# once, with the Claude subscription, and Firecrawl's keyless search
# (mcp.firecrawl.dev) once, for opencode's websearch. Uses its own ports and
# CA, so the running units are untouched.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
# shellcheck source=nono-version.sh
. "$KIT/nono-version.sh"
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
if [[ -x $STAGE0_HOME/bin/nono && $(cat "$STAGE0_HOME/bin/nono.pin" 2>/dev/null) == "$NONO_VERSION $(nono_sha256 "$(uname -m)")" ]]; then
  cp "$STAGE0_HOME/bin/nono" "$bin/nono"
else
  nono_install "$bin"
fi
# shellcheck source=egress-credentials.sh
. "$KIT/egress-credentials.sh"
egress_credentials
cd "$KIT/../../control-plane"
CP_EGRESS_LIVE_NONO=$bin/nono go test -count=1 -run '^TestLiveNono$' -v ./internal/egress/
