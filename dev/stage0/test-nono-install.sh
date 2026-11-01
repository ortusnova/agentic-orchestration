#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks that the kit installs nono only from an archive with the pinned sum,
# against throwaway archives served from a file:// URL.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export NONO_RELEASES=file://$tmp/releases
# shellcheck source=nono-version.sh
. "$KIT/nono-version.sh"
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

[[ $NONO_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && ok "the pin is a stable version ($NONO_VERSION)" || fail "NONO_VERSION $NONO_VERSION is not a stable version"
for arch in x86_64 aarch64; do
  [[ $(nono_sha256 $arch) =~ ^[0-9a-f]{64}$ ]] && ok "$arch has a pinned sum" || fail "$arch has no pinned sum"
done
nono_sha256 riscv64 > /dev/null && fail "an unpinned architecture has a sum" || ok "an unpinned architecture has no sum"

arch=$(uname -m)
archive=nono-v$NONO_VERSION-$arch-unknown-linux-gnu.tar.gz
release() { # release BODY: publish an archive holding a nono that prints BODY
  mkdir -p "$tmp/build" "$tmp/releases/v$NONO_VERSION"
  printf '#!/bin/sh\necho %s\n' "$1" > "$tmp/build/nono"
  chmod +x "$tmp/build/nono"
  tar -czf "$tmp/releases/v$NONO_VERSION/$archive" -C "$tmp/build" nono
}
release genuine
pinned=$(sha256sum "$tmp/releases/v$NONO_VERSION/$archive" | cut -d' ' -f1)
# The test's archive stands in for the release: pin its sum for this architecture.
nono_sha256() { [[ $1 == "$arch" ]] && echo "$pinned"; }

bin=$tmp/bin
mkdir -p "$bin"
nono_install "$bin" 2> "$tmp/err" && [[ $("$bin/nono") == genuine ]] \
  && ok "the archive with the pinned sum is installed" || fail "the archive with the pinned sum: $(cat "$tmp/err")"

rm -rf "$tmp/releases"
nono_install "$bin" 2> "$tmp/err" && ok "an installed pinned nono is not downloaded again" || fail "the installed nono was downloaded again: $(cat "$tmp/err")"

release tampered
rm -f "$bin/nono.pin"
if nono_install "$bin" 2> "$tmp/err"; then
  fail "an archive with another sum was installed"
else
  grep -q "does not match its pinned sum" "$tmp/err" && [[ $("$bin/nono") == genuine ]] \
    && ok "an archive with another sum is refused, and the installed nono stays" || fail "the refusal: $(cat "$tmp/err")"
fi
exit $failed
