# shellcheck shell=bash
# Sourced by stage0.sh for the homelab profile (spec store
# run-homelab-through-sortie, design D7, D12). homelab_profile prepares what
# WORKFLOW-homelab.md names, from homelab's own layout on this dev host, and
# exports the values stage0.sh renders into it:
#   STAGE0_PACK                     the homelab pack, built when its inputs change
#   STAGE0_HOMELAB_MOUNTS           the machines' host mounts, as a YAML flow list
#   STAGE0_HOMELAB_REGISTRY_CACHE   the dev host's registry caches, for prepare.sh
# and writes $STAGE0_HOME/profile.env for run-sortie.sh:
#   STAGE0_SMOLVM_LIB_DIR           homelab's guest kernel (swap, pressure stall information)
# Needs stage0.sh's say, die, STAGE0_* settings and REBUILD_PACK.

homelab_profile() {
  local src=${STAGE0_HOMELAB_SRC:-$HOME/dev/homelab} hl=$STAGE0_HOME/homelab
  local settings=$HOME/.config/homelab/sandbox.env

  # The kit's own detached worktree of homelab's origin/master: the pack and
  # the cache layout come from it, never from the operator's working tree.
  [[ -d $src/.git ]] || die "$src is not a homelab checkout (STAGE0_HOMELAB_SRC)"
  git -C "$src" fetch -q origin "$STAGE0_HOMELAB_REPO_BASE"
  if [[ ! -e $hl ]]; then
    say "Adding the kit's homelab worktree $hl"
    git -C "$src" worktree add -q --detach "$hl" "origin/$STAGE0_HOMELAB_REPO_BASE"
  else
    git -C "$hl" checkout -q --detach "origin/$STAGE0_HOMELAB_REPO_BASE"
  fi
  say "homelab at $(git -C "$hl" rev-parse --short HEAD)"

  [[ -r $settings ]] || die "$settings is missing: run \`make dev-host\` in homelab"
  local vm_key registry_cache
  vm_key=$(sed -n 's/^SANDBOX_VM_KEY=//p' "$settings")
  registry_cache=$(sed -n 's/^SANDBOX_REGISTRY_CACHE=//p' "$settings")
  [[ -r $vm_key ]] || die "the vm age key $vm_key is missing (homelab docs/operator-runbook.md, \"Dev host: place the vm age key\")"
  export STAGE0_HOMELAB_REGISTRY_CACHE=$registry_cache

  # The guest kernel make dev-host builds; without it machines get no swap.
  local krunfw
  krunfw=$("$hl/scripts/smolvm/libkrunfw/build.sh" --print-dir) || die "homelab's guest kernel build failed to name its directory"
  [[ -f $krunfw/build.json ]] || die "homelab's guest kernel $krunfw is not built: run \`make dev-host\` in homelab"

  # The vm age key, a test fixture (design D3), copied here alone: run-sortie.sh
  # hands it to the machines as HOMELAB_VM_AGE_KEY, so no other identity is
  # ever exposed.
  install -d -m 0700 "$STAGE0_HOME/inputs" "$STAGE0_HOME/inputs/vm-age"
  cmp -s "$vm_key" "$STAGE0_HOME/inputs/vm-age/keys.txt" || install -m 0600 "$vm_key" "$STAGE0_HOME/inputs/vm-age/keys.txt"

  # The mounts, as scripts/smolvm/sandbox passes them (design D7); the guest
  # user's home is /home/builder. Each path is quoted: {workspace} is not plain
  # YAML in a flow mapping. The uv and Go caches come from homelab.cache,
  # as in the sandbox, so host and guest paths cannot drift.
  local sources=$HOME/.cache/homelab-sources vmhost=$HOME/.cache/homelab-vmhost
  local recorders=$HOME/.local/state/homelab/sortie-recorder
  mkdir -p "$sources" "$vmhost" "$recorders"
  local mounts ns hosts guests i
  mounts="{host: \"$sources\", guest: \"/home/builder/.cache/homelab-sources\", read_only: true}"
  mounts+=", {host: \"$vmhost\", guest: \"/home/builder/.cache/homelab-vmhost\"}"
  # One mount per cache namespace (Go's mod and build share a parent), and
  # the vm key as an environment value (run-sortie.sh), not a mount: a
  # machine on taichi starts with at most 6 shared folders beside Sortie's
  # tool socket (smolvm 1.32.4, nested, virtio-net; one more fails with "no
  # more IRQs are available"), and Sortie adds one for its control directory.
  local count=2
  for ns in uv go; do
    mapfile -t hosts < <(cd "$hl" && python3 -m homelab.cache path --all "$ns")
    mapfile -t guests < <(cd "$hl" && env -u HOMELAB_CACHE_HOME HOME=/home/builder python3 -m homelab.cache path --all "$ns")
    (( ${#hosts[@]} == ${#guests[@]} && ${#hosts[@]} > 0 )) || die "homelab.cache gave no $ns paths"
    for i in "${!hosts[@]}"; do mkdir -p "${hosts[i]}"; done
    local pair
    pair=$(python3 - "${#hosts[@]}" "${hosts[@]}" "${guests[@]}" <<'PY'
import os, sys
n = int(sys.argv[1]); hosts, guests = sys.argv[2:2 + n], sys.argv[2 + n:]
h, g = (os.path.commonpath(hosts), os.path.commonpath(guests)) if n > 1 else (hosts[0], guests[0])
if [os.path.relpath(x, h) for x in hosts] != [os.path.relpath(x, g) for x in guests]:
    sys.exit(f"the host and guest {n} paths are laid out differently under {h} and {g}")
print(h); print(g)
PY
    ) || die "homelab.cache's $ns paths cannot share one mount"
    mounts+=", {host: \"${pair%%$'\n'*}\", guest: \"${pair#*$'\n'}\"}"
    count=$((count + 1))
  done
  mounts+=", {host: \"$recorders/{workspace}\", guest: \"/var/log/homelab-recorder\"}"
  count=$((count + 1))
  (( count <= 5 )) || die "the homelab workflow would mount $count folders; a machine starts with at most 5 besides Sortie's own"
  export STAGE0_HOMELAB_MOUNTS="[$mounts]"

  (umask 077; printf 'STAGE0_SMOLVM_LIB_DIR=%s\n' "$krunfw" > "$STAGE0_HOME/profile.env")
  homelab_pack "$hl"
}

# homelab_pack HL: stage homelab's image scripts and the kit's harness into one
# build context (design D12) and build the pack, named by a hash of that
# context and of the toolchain, so a change to either builds a new one. Packs
# no Sortie machine was created from are deleted.
homelab_pack() {
  local hl=$1 stage=$STAGE0_HOME/pack-src packs=$STAGE0_HOME/packs hash pack old in_use sum
  rm -rf "$stage"
  mkdir -p "$stage/homelab" "$stage/harness" "$packs"
  cp "$KIT/homelab-pack/smol-builder.toml" "$KIT/homelab-pack/Dockerfile" "$stage/"
  cp "$hl/scripts/vmhost/install-tools.sh" "$hl/scripts/vmhost/fetch.sh" \
     "$hl/scripts/smolvm/image/guest-setup.sh" "$hl/scripts/smolvm/image/guest-start.sh" \
     "$hl/scripts/smolvm/image/recorder.py" "$stage/homelab/"
  # homelab's guest-setup.sh gives its guest user these ids: smolvm's shared
  # folders let a guest write only as their host owner, this user.
  printf '%s:%s\n' "$(id -u)" "$(id -g)" > "$stage/homelab/guest-uid"
  cp "$KIT/pack/install-harness.sh" "$KIT/pack/agent-acp" "$KIT/pack/claude-settings.json" \
     "$KIT/pack/opencode.json" "$stage/harness/"
  hash=$({
    (cd "$stage" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
    sha256sum < "$(command -v smol-builder)"
    smolvm --version
  } | sha256sum | cut -c1-16)
  pack=$packs/homelab-$hash.smolmachine
  if [[ -n $REBUILD_PACK || ! -s $pack ]]; then
    say "Building the homelab pack $hash (report $STAGE0_HOME/pack-report.json)"
    smol-builder build --repo "$stage" --output "$pack.tmp" > "$STAGE0_HOME/pack-report.json" \
      || { rm -f "$pack.tmp"; die "the homelab pack did not build: $STAGE0_HOME/pack-report.json"; }
    mv "$pack.tmp" "$pack"
  fi
  rm -rf "$stage"
  in_use=$(smolvm machine ls --json | python3 -c 'import json, sys
print("\n".join(m.get("labels", {}).get("sortie.pack", "") for m in json.load(sys.stdin)))')
  for old in "$packs"/homelab-*.smolmachine "$packs"/*.tmp; do
    [[ -e $old && $old != "$pack" ]] || continue
    sum=$(sha256sum < "$old" | cut -d' ' -f1)
    if grep -qx "$sum" <<< "$in_use"; then continue; fi
    rm -f "$old" && say "Deleted the unused pack $(basename "$old")"
  done
  export STAGE0_PACK=$pack
}
