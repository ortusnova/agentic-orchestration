#!/usr/bin/env bash
# Checks the workbench's local-name declaration (local-name.sh), and, when a
# homelab checkout is at hand, that homelab's workstation-setup.sh reads the
# directory homelab declares for it.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=names.sh
. "$KIT/names.sh"
env_names test-a
# shellcheck source=local-name.sh
. "$KIT/local-name.sh"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
fail() { echo "FAIL: $*"; failed=1; }

dir=$tmp/local-names.d
[[ $(write_local_name "$dir" workbench.homelab.dev 8793) == changed ]] || fail "a first write is a change"
grep -qx 'workbench.homelab.dev 8793' "$dir/ortus-nova-test-a-workbench" || fail "the file holds '<name> <port>'"
[[ $(write_local_name "$dir" workbench.homelab.dev 8793) == unchanged ]] || fail "the same declaration is no change"
[[ $(write_local_name "$dir" workbench.homelab.dev 9100) == changed ]] || fail "a new port is a change"
grep -qx 'workbench.homelab.dev 9100' "$dir/ortus-nova-test-a-workbench" || fail "the new port is written"
[[ $(grep -cv '^#' "$dir/ortus-nova-test-a-workbench") == 1 ]] || fail "one declaration line"
for bad in "Workbench.homelab.dev 8793" "workbench.example.com 8793" "workbench.homelab.dev 0" "workbench.homelab.dev 65536" "workbench.homelab.dev x"; do
  # shellcheck disable=SC2086
  if write_local_name "$dir" $bad 2>/dev/null; then fail "'$bad' is refused"; fi
done
grep -qx 'workbench.homelab.dev 9100' "$dir/ortus-nova-test-a-workbench" || fail "a refused declaration leaves the file as it was"

# homelab declares the directory; the kit's default must be it.
hl=${ORTUS_NOVA_HOMELAB_SRC:-$HOME/dev/homelab}
if [[ -f $hl/homelab/state_paths.py ]]; then
  if declared=$(cd "$hl" && HOME=/home/u python3 -S -m homelab.state_paths path local-names 2>/dev/null); then
    [[ $declared == "/home/u/$ORTUS_NOVA_LOCAL_NAMES_REL" ]] || fail "homelab declares $declared, the kit writes ~/$ORTUS_NOVA_LOCAL_NAMES_REL"
  else
    echo "skip: $hl declares no local-names directory yet"
  fi
else
  echo "skip: no homelab checkout at $hl"
fi

((failed == 0)) && echo "ok: local-name.sh"
exit $failed
