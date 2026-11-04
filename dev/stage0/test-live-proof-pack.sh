#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016 # ok and fail always succeed; the pins and fakes are literal
# Checks the live-proof pack (live-proof-pack/, live-proof-pack.sh), which the
# job profile nested runs in (WORKFLOW.md): every tool is pinned, Go at the
# go layer's pin and smolvm at or above the kit's minimum; check-tools.sh
# fails a build whose tool is not at its pin; and the pack is staged and built
# as the homelab pack is (staged-pack.sh), again only when its inputs change,
# with smol-builder and smolvm faked.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$KIT/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
say() { echo "say: $*" >> "$tmp/said"; }
die() { echo "die: $*" >&2; exit 1; }

dockerfile=$KIT/live-proof-pack/Dockerfile
pin() { sed -n "s/^ARG $1=//p" "$dockerfile"; }

# The pins.
smolvm_pin=$(pin SMOLVM_VERSION) smolvm_sha=$(pin SMOLVM_SHA256) go_pin=$(pin GO_VERSION)
passt_pin=$(pin PASST_VERSION) dnsmasq_pin=$(pin DNSMASQ_VERSION)
[[ $smolvm_sha =~ ^[0-9a-f]{64}$ ]] && grep -q 'sha256sum -c' "$dockerfile" \
  && ok "smolvm $smolvm_pin is pinned by its SHA-256, checked before use" || fail "smolvm's checksum: $smolvm_sha"
mkdir -p "$tmp/fake"
printf '#!/bin/sh\necho "smolvm %s"\n' "$smolvm_pin" > "$tmp/fake/smolvm"
chmod +x "$tmp/fake/smolvm"
# shellcheck source=smolvm-version.sh
. "$KIT/smolvm-version.sh"
PATH=$tmp/fake:$PATH smolvm_version_ok > /dev/null && ok "the pack's smolvm $smolvm_pin is at or above the kit's minimum $SMOLVM_MIN" \
  || fail "the pack's smolvm $smolvm_pin is below $SMOLVM_MIN"
layer_go=$(sed -n 's/^ARG GO_VERSION=//p' "$ROOT/smol-builder/layers/go/Dockerfile")
[[ -n $go_pin && $go_pin == "$layer_go" ]] && grep -q '^layers = \["go", "node"\]' "$KIT/live-proof-pack/smol-builder.toml" \
  && ok "Go $go_pin is the go layer's pin, checked in the build" || fail "Go: the pack checks $go_pin, the go layer pins $layer_go"
grep -q '"passt=${PASST_VERSION}" "dnsmasq-base=${DNSMASQ_VERSION}"' "$dockerfile" && [[ -n $passt_pin && -n $dnsmasq_pin ]] \
  && ok "pasta (passt $passt_pin) and dnsmasq ($dnsmasq_pin) are installed at their pinned versions" || fail "the Debian pins"
grep -q 'check-tools.sh "${GO_VERSION}" "${SMOLVM_VERSION}" "${PASST_VERSION}" "${DNSMASQ_VERSION}"' "$dockerfile" \
  && ok "the build runs check-tools.sh with every pin" || fail "the build does not check its tools"

# check-tools.sh against fake tools.
tools=$tmp/tools
mkdir -p "$tools"
fake_tools() { # GO SMOLVM PASST DNSMASQ
  printf '#!/bin/sh\necho "go version go%s linux/amd64"\n' "$1" > "$tools/go"
  printf '#!/bin/sh\necho "smolvm %s"\n' "$2" > "$tools/smolvm"
  printf '#!/bin/sh\ncase "$4" in passt) printf %%s "%s" ;; dnsmasq-base) printf %%s "%s" ;; *) exit 1 ;; esac\n' "$3" "$4" > "$tools/dpkg-query"
  printf '#!/bin/sh\n' > "$tools/pasta"
  printf '#!/bin/sh\n' > "$tools/dnsmasq"
  chmod +x "$tools"/*
}
check_tools() { PATH=$tools:/usr/bin:/bin "$KIT/live-proof-pack/check-tools.sh" "$go_pin" "$smolvm_pin" "$passt_pin" "$dnsmasq_pin"; }
fake_tools "$go_pin" "$smolvm_pin" "$passt_pin" "$dnsmasq_pin"
check_tools > "$tmp/out" 2>&1 && grep -q "^smolvm smolvm $smolvm_pin$" "$tmp/out" \
  && ok "check-tools.sh passes the pinned tools and prints their versions" || fail "check-tools: $(cat "$tmp/out")"
fake_tools "$go_pin" 1.32.4 "$passt_pin" "$dnsmasq_pin"
check_tools > "$tmp/out" 2>&1 && fail "check-tools passed smolvm 1.32.4" || ok "check-tools.sh fails another smolvm"
fake_tools 1.26.0 "$smolvm_pin" "$passt_pin" 2.90-1
check_tools > "$tmp/out" 2>&1 && fail "check-tools passed another Go and dnsmasq" \
  || { grep -q '^go: want' "$tmp/out" && grep -q '^dnsmasq-base: want' "$tmp/out" && ok "check-tools.sh names each tool off its pin"; } || fail "check-tools: $(cat "$tmp/out")"
fake_tools "$go_pin" "$smolvm_pin" "$passt_pin" "$dnsmasq_pin"
rm "$tools/pasta"
check_tools > "$tmp/out" 2>&1 && fail "check-tools passed without pasta" || ok "check-tools.sh fails without pasta"

# nested-probe.sh, the measurement, against a fake smolvm whose machine boots
# and whose policy lets only the declared host through.
cat > "$tools/smolvm" <<'SH'
#!/bin/sh
[ "$1" = --version ] && { echo "smolvm 1.33.0"; exit 0; }
[ "$1 $2" = "machine run" ] || exit 2
allowed=; prev=; for a; do case $prev in --allow-host-pattern) allowed=$a ;; esac; prev=$a; done
echo "nested-vm booted"
echo "nested-vm api.github.com: $( [ "$allowed" = api.github.com ] && echo reached || echo refused)"
echo "nested-vm example.com: $( [ "$allowed" = example.com ] && echo reached || echo refused)"
SH
chmod +x "$tools/smolvm"
PATH=$tools:/usr/bin:/bin "$KIT/live-proof-pack/nested-probe.sh" api.github.com example.com > "$tmp/out" 2>&1
grep -qx 'nested boot: booted' "$tmp/out" && grep -qx 'nested allowed: reached' "$tmp/out" && grep -qx 'nested denied: refused' "$tmp/out" \
  && grep -q '^\$ smolvm machine run .*--allow-host-pattern api.github.com' "$tmp/out" && grep -qx 'smolvm 1.33.0' "$tmp/out" \
  && ok "nested-probe.sh records the version, the commands, their output and the verdicts" || fail "nested-probe: $(cat "$tmp/out")"
printf '#!/bin/sh\n[ "$1" = --version ] && echo "smolvm 1.33.0"; echo "no kvm" >&2; exit 1\n' > "$tools/smolvm"
PATH=$tools:/usr/bin:/bin "$KIT/live-proof-pack/nested-probe.sh" api.github.com example.com > "$tmp/out" 2>&1
grep -qx 'nested boot: failed' "$tmp/out" && grep -q 'no kvm' "$tmp/out" \
  && ok "nested-probe.sh records a machine that does not boot" || fail "nested-probe, no boot: $(cat "$tmp/out")"

# The build, as the homelab pack's: staged, named by its inputs' hash, again
# only when they change, and the replaced pack deleted while no job runs.
export STAGE0_HOME=$tmp/home
mkdir -p "$STAGE0_HOME" "$tmp/bin"
cat > "$tmp/bin/smol-builder" <<SH
#!/bin/sh
# smol-builder build --repo DIR --output FILE
echo "\$@" >> "$tmp/builds"
ls -R "\$3" > "$tmp/staged"
echo pack > "\$5"
echo '{"ok": true}'
SH
printf '#!/bin/sh\necho "smolvm %s"\n' "$smolvm_pin" > "$tmp/bin/smolvm"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH
# shellcheck source=worker-config.sh
. "$KIT/worker-config.sh"
# shellcheck source=staged-pack.sh
. "$KIT/staged-pack.sh"
# shellcheck source=live-proof-pack.sh
. "$KIT/live-proof-pack.sh"
STAGE0_EGRESS_CA=$tmp/ca.crt
REBUILD_PACK=
live_proof_pack
pack=$STAGE0_LIVE_PROOF_PACK
[[ $pack == "$STAGE0_HOME"/packs/live-proof-*.smolmachine && -s $pack && $(wc -l < "$tmp/builds") == 1 ]] \
  && ok "the pack is built as live-proof-<hash>.smolmachine" || fail "build: $pack, $(cat "$tmp/builds")"
for f in Dockerfile smol-builder.toml check-tools.sh nested-probe.sh install-harness.sh agent-acp opencode.json; do
  grep -qx "$f" "$tmp/staged" && ok "its build context holds $f" || fail "the staged context lacks $f: $(cat "$tmp/staged")"
done
[[ -s $STAGE0_HOME/live-proof-pack-report.json && ! -e $STAGE0_HOME/live-proof-pack-src ]] \
  && ok "smol-builder's report is kept and the staged context removed" || fail "report or stage"
grep -q '"ORTUS_EGRESS_CA": "'"$tmp/ca.crt"'"' <<< "$STAGE0_LIVE_PROOF_IMAGE_JSON" \
  && ok "its image entry trusts the egress CA" || fail "image: $STAGE0_LIVE_PROOF_IMAGE_JSON"
live_proof_pack
[[ $STAGE0_LIVE_PROOF_PACK == "$pack" && $(wc -l < "$tmp/builds") == 1 ]] && ok "unchanged inputs build nothing" || fail "rebuilt: $(cat "$tmp/builds")"
printf '#!/bin/sh\necho "smolvm 9.9.9"\n' > "$tmp/bin/smolvm"
mkdir -p "$STAGE0_HOME/worker/jobs/ONS-1"
live_proof_pack
[[ $STAGE0_LIVE_PROOF_PACK != "$pack" && $(wc -l < "$tmp/builds") == 2 && -e $pack ]] \
  && ok "another smolvm builds a new pack, and a running job keeps the old one" || fail "smolvm change: $(ls "$STAGE0_HOME/packs")"
rmdir "$STAGE0_HOME/worker/jobs/ONS-1"
live_proof_pack
[[ ! -e $pack && -s $STAGE0_LIVE_PROOF_PACK ]] && ok "with no job running, the replaced pack is deleted" || fail "old pack: $(ls "$STAGE0_HOME/packs")"
exit $failed
