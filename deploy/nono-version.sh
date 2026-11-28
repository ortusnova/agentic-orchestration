# shellcheck shell=bash
# The nono release the stage-0 kit runs on p1, for the egress flow behind
# Pomerium (README.md), pinned by the SHA-256 of each release archive:
# the release's SHA256SUMS.txt, also in nono's flake.nix after each release.
# To move to a new release, change NONO_VERSION and both sums together;
# `make -C deploy check-latest` says when a newer one is out.
# Sourced by deploy.sh.
NONO_VERSION=0.82.0
NONO_RELEASES=${NONO_RELEASES:-https://github.com/nolabs-ai/nono/releases/download}

# nono_sha256 ARCH: the pinned sum of the archive for ARCH (uname -m).
nono_sha256() {
  case $1 in
    x86_64) echo 81d7a5616622dcf68ca0ed2b7e6b3af08c1216fb0c42dc6d7ecfbd2bbf2d3f50 ;;
    aarch64) echo de16b663f1941b4ff8500f75f34d10a497ef1d1934281573888fb9e808dab53e ;;
    *) return 1 ;;
  esac
}

# nono_install DIR: install the pinned nono as DIR/nono, unless it is
# there already. An archive whose sum is not the pinned one is refused.
nono_install() {
  local dir=$1 arch sum archive tmp
  arch=$(uname -m)
  sum=$(nono_sha256 "$arch") || { echo "nono_install: no pinned nono for $arch" >&2; return 1; }
  if [[ -x $dir/nono && $(cat "$dir/nono.pin" 2>/dev/null) == "$NONO_VERSION $sum" ]]; then
    return 0
  fi
  archive=nono-v$NONO_VERSION-$arch-unknown-linux-gnu.tar.gz
  tmp=$(mktemp -d)
  if ! curl -fsSL -o "$tmp/$archive" "$NONO_RELEASES/v$NONO_VERSION/$archive"; then
    rm -rf "$tmp"
    echo "nono_install: could not download $archive" >&2
    return 1
  fi
  if ! (cd "$tmp" && echo "$sum  $archive" | sha256sum -c --status -); then
    rm -rf "$tmp"
    echo "nono_install: $archive does not match its pinned sum $sum" >&2
    return 1
  fi
  if ! tar -xzf "$tmp/$archive" -C "$tmp" nono; then
    rm -rf "$tmp"
    echo "nono_install: $archive holds no nono" >&2
    return 1
  fi
  install -m 0755 "$tmp/nono" "$dir/nono"
  echo "$NONO_VERSION $sum" > "$dir/nono.pin"
  rm -rf "$tmp"
}

# nono_latest: the tag of nono's latest stable release (network).
nono_latest() {
  curl -fsSL https://api.github.com/repos/nolabs-ai/nono/releases/latest \
    | sed -n 's/^ *"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p'
}
