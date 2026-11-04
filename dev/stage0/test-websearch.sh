#!/usr/bin/env bash
# Checks that opencode in a Sortie machine can search the web without a
# prompt: both opencode configs select Firecrawl's keyless provider and keep
# webfetch denied, and the workflow declares Firecrawl's MCP host among the
# harness's hosts (environment.remote.egress), which every job's machine
# reaches directly: nono, which serves only the credential hosts, does not
# allow it. Without a provider, opencode asks the user to choose
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

if grep -qE '^        - \{host: mcp\.firecrawl\.dev, reason: "[^"]+"\}$' "$KIT/WORKFLOW.md"; then
  echo "ok: WORKFLOW.md declares $host among the harness's hosts (environment.remote.egress)"
else
  echo "FAIL: WORKFLOW.md does not declare $host among the harness's hosts (environment.remote.egress)"; failed=1
fi
if jq -e --arg h "$host" '.network.allow_domain | index($h)' "$ROOT/control-plane/nono/egress-profile.json" > /dev/null; then
  echo "FAIL: control-plane/nono/egress-profile.json allows $host, which is no credential host"; failed=1
else
  echo "ok: control-plane/nono/egress-profile.json leaves $host to the worker"
fi

exit $failed
