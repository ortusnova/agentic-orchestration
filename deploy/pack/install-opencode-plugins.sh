#!/bin/sh
# Installs the plugins of an opencode.json into USER's opencode cache, in a
# pack build, as install-harness.sh's last step.
#
#   install-opencode-plugins.sh USER HOME CONFIG
#
# opencode installs a config plugin from npm the first time it loads it, and
# gives plugins 5 s to activate before a request fails with 503
# (ServiceUnavailableError, "Location plugins did not finish activating").
# This plugin's install takes longer, so in a fresh machine agent-acp's
# credential swap and ACP session/new fail until one opencode process lives
# long enough to finish it. Installed here, it activates at once. opencode's
# data and state (its database and logs) go to a scratch directory, so the
# image carries only the cache.
set -eu
user=$1 home=$2 config=$3
plugins=$(node -e 'for (const p of JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")).plugins || []) console.log(p)' "$config")
[ -n "$plugins" ] || exit 0
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
chown "$user" "$scratch" 2>/dev/null || true
for plugin in $plugins; do
  (cd "$scratch" && runuser -u "$user" -- env HOME="$home" \
    XDG_DATA_HOME="$scratch/data" XDG_STATE_HOME="$scratch/state" \
    opencode plugin add "$plugin" </dev/null)
  # A finished install is a directory beside opencode's .staging-* ones.
  finished=
  for dir in "$home/.cache/opencode/npm/$plugin"/*; do
    [ -d "$dir" ] && finished=1
  done
  if [ -z "$finished" ]; then
    echo "install-opencode-plugins: opencode did not finish installing $plugin into $home/.cache/opencode/npm" >&2
    exit 1
  fi
done
