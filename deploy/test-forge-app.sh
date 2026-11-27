#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016 # ok and fail always succeed; the grep pattern is literal
# Checks forge-app.sh: deploy.sh starts Sortie only with the GitHub App's
# key at mode 0600 or narrower, and says what to do otherwise.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $*"; }
fail() { echo "FAIL: $*"; failed=1; }
ORTUS_NOVA_HOME=$tmp
# shellcheck source=forge-app.sh
. "$KIT/forge-app.sh"

[[ $ORTUS_NOVA_GITHUB_APP_KEY == "$tmp/github-app.pem" ]] && ok "the key is the stage-0 home's github-app.pem" || fail "key path $ORTUS_NOVA_GITHUB_APP_KEY"
if err=$(forge_app_key_ok "$ORTUS_NOVA_GITHUB_APP_KEY" 2>&1); then fail "a missing key passes"
else [[ $err == *"is not at $tmp/github-app.pem"* ]] && ok "a missing key is refused, naming where it goes" || fail "missing: $err"; fi
printf 'not a real key\n' > "$ORTUS_NOVA_GITHUB_APP_KEY"
for mode in 640 604 644 660 700 610; do
  chmod "$mode" "$ORTUS_NOVA_GITHUB_APP_KEY"
  if err=$(forge_app_key_ok "$ORTUS_NOVA_GITHUB_APP_KEY" 2>&1); then fail "mode $mode passes"
  else [[ $err == *"has mode $mode"*"chmod 600"* ]] && ok "mode $mode is refused with the fix" || fail "mode $mode: $err"; fi
done
for mode in 600 400; do
  chmod "$mode" "$ORTUS_NOVA_GITHUB_APP_KEY"
  forge_app_key_ok "$ORTUS_NOVA_GITHUB_APP_KEY" && ok "mode $mode passes" || fail "mode $mode is refused"
done
grep -q 'forge_app_key_ok "$ORTUS_NOVA_GITHUB_APP_KEY" || die' "$KIT/deploy.sh" \
  && ok "deploy.sh checks the key before it renders Sortie's workflow" || fail "deploy.sh does not check the key"
exit $failed
