#!/usr/bin/env bash
# Pin every upstream image of compose.yaml by digest: each `image:` that is
# not built here (localhost/ortus-nova/...) becomes NAME:TAG@sha256:DIGEST,
# the digest of the manifest (or manifest list) the registry serves for
# NAME:TAG now. Run it after changing a tag, on a host that reaches the
# registries, and commit the result; `make -C control-plane test` fails
# while an upstream image has no digest.
#
#   control-plane/pin-images.sh            pin from each tag
#   control-plane/pin-images.sh --check    only list the images and their pins
#
# Needs skopeo (or set SKOPEO to a compatible command).
set -euo pipefail

CP=$(cd "$(dirname "$0")" && pwd)
FILE=${COMPOSE_FILE:-$CP/compose.yaml}
SKOPEO=${SKOPEO:-skopeo}
die() { printf 'pin-images.sh: %s\n' "$*" >&2; exit 1; }

check=0
[[ ${1:-} == --check ]] && check=1

refs=$(sed -n 's/^[[:space:]]*image:[[:space:]]*\([^[:space:]]*\).*/\1/p' "$FILE" | grep -v '^localhost/ortus-nova/' | sort -u)
[[ -n $refs ]] || die "no upstream image in $FILE"

if ((check)); then
  bad=0
  for ref in $refs; do
    if [[ $ref =~ @sha256:[0-9a-f]{64}$ ]]; then echo "pinned   $ref"; else echo "UNPINNED $ref"; bad=1; fi
  done
  exit $bad
fi

command -v "$SKOPEO" > /dev/null || die "$SKOPEO is not installed"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
cp "$FILE" "$tmp"
for ref in $refs; do
  name=${ref%@*}
  digest=sha256:$("$SKOPEO" inspect --raw "docker://$name" | sha256sum | cut -d' ' -f1)
  [[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || die "no digest for $name"
  echo "$name@$digest"
  # Replace this image's line, whatever digest it had.
  awk -v old="$ref" -v new="$name@$digest" '
    { line = $0; sub(/^[[:space:]]*image:[[:space:]]*/, "", line); sub(/[[:space:]].*$/, "", line) }
    $0 ~ /^[[:space:]]*image:/ && line == old { i = index($0, old); print substr($0, 1, i - 1) new substr($0, i + length(old)); next }
    { print }' "$tmp" > "$tmp.new"
  mv "$tmp.new" "$tmp"
done
cp "$tmp" "$FILE"
