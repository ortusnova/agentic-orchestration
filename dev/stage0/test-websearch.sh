#!/usr/bin/env bash
# Checks that opencode in a Sortie machine can search the web without a
# prompt: both opencode configs select Firecrawl's keyless provider and keep
# webfetch denied, and p1's egress profile, the one host list under the
# remote kind (nono enforces it for every worker), allows Firecrawl's MCP
# host. Without a provider, opencode asks the user to choose
# one, and Sortie refuses the prompt.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$KIT/../.." && pwd)
host=mcp.firecrawl.dev
failed=0

for cfg in "$KIT/pack/opencode.json" "$ROOT/smol-builder/layers/base/opencode.json"; do
  name=${cfg#"$ROOT"/}
  if [[ $(jq -r '.websearch.provider // empty' "$cfg") == firecrawl ]]; then
    echo "ok: $name selects Firecrawl for websearch"
  else
    echo "FAIL: $name sets no Firecrawl websearch provider"; failed=1
  fi
  if jq -e '.permissions[-1] == {action: "webfetch", resource: "*", effect: "deny"}' "$cfg" > /dev/null; then
    echo "ok: $name still denies webfetch"
  else
    echo "FAIL: $name no longer ends with the webfetch deny"; failed=1
  fi
done

if jq -e --arg h "$host" '.network.allow_domain | index($h)' "$KIT/egress-profile.json" > /dev/null; then
  echo "ok: egress-profile.json allows $host"
else
  echo "FAIL: egress-profile.json does not allow $host"; failed=1
fi

exit $failed
