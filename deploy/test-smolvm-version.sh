#!/usr/bin/env bash
# Checks deploy.sh's smolvm version check against fake smolvm binaries.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=smolvm-version.sh
. "$KIT/smolvm-version.sh"

fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT
failed=0

check() { # check VERSION_OUTPUT WANT(accept|refuse)
  printf '#!/bin/sh\necho "%s"\n' "$1" > "$fake/smolvm"
  chmod +x "$fake/smolvm"
  local got=refuse
  PATH=$fake:$PATH smolvm_version_ok > /dev/null && got=accept
  if [[ $got == "$2" ]]; then
    echo "ok: '$1' -> $got"
  else
    echo "FAIL: '$1' -> $got, want $2"
    failed=1
  fi
}

check "smolvm 1.32.4" accept
check "smolvm 1.32.10" accept
check "smolvm 1.33.0" accept
check "smolvm 2.0.0" accept
check "smolvm 1.32.3" refuse
check "smolvm 1.26.1" refuse
check "smolvm 1.4.99" refuse
check "smolvm 1.33.0-rc1" refuse
check "not smolvm" refuse
exit $failed
