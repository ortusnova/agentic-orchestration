# shellcheck shell=bash
# Sourced by stage0.sh on a worker host with homelab's dev host (`make
# dev-host` in homelab; spec store run-homelab-through-sortie, design D7,
# D12). homelab_inputs prepares what a homelab job needs on this host, from
# homelab's own layout, as the co-located homelab Sortie gave its machines,
# and exports, for render_worker_config (worker-config.sh):
#   STAGE0_HOMELAB_PACK        the homelab pack, built when its inputs change
#   STAGE0_HOMELAB_IMAGE_JSON  the daemon's smolvm.images entry for it: the
#                              user builder, homelab's guest kernel, the shared
#                              folders, the vm key, and the setup the
#                              workflow's hooks ran
# Needs stage0.sh's say, die, STAGE0_* settings, REBUILD_PACK and
# worker-config.sh.

homelab_inputs() {
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
  local vm_key
  vm_key=$(sed -n 's/^SANDBOX_VM_KEY=//p' "$settings")
  [[ -r $vm_key ]] || die "the vm age key $vm_key is missing (homelab docs/operator-runbook.md, \"Dev host: place the vm age key\")"

  # The guest kernel make dev-host builds; without it machines get no swap.
  local krunfw
  krunfw=$("$hl/scripts/smolvm/libkrunfw/build.sh" --print-dir) || die "homelab's guest kernel build failed to name its directory"
  [[ -f $krunfw/build.json ]] || die "homelab's guest kernel $krunfw is not built: run \`make dev-host\` in homelab"

  # The vm age key, a test fixture (design D3), copied here alone: the daemon
  # hands it to the machines as HOMELAB_VM_AGE_KEY, so no other identity is
  # ever exposed.
  install -d -m 0700 "$STAGE0_HOME/inputs" "$STAGE0_HOME/inputs/vm-age"
  cmp -s "$vm_key" "$STAGE0_HOME/inputs/vm-age/keys.txt" || install -m 0600 "$vm_key" "$STAGE0_HOME/inputs/vm-age/keys.txt"

  # The shared folders, as scripts/smolvm/sandbox passes them (design D7);
  # the guest user's home is /home/builder. The uv and Go caches come from
  # homelab.cache, as in the sandbox, so host and guest paths cannot drift.
  local sources=$HOME/.cache/homelab-sources vmhost=$HOME/.cache/homelab-vmhost
  local recorders=$HOME/.local/state/homelab/sortie-recorder
  local open_issues=$HOME/.local/state/homelab/open-issues
  mkdir -p "$sources" "$vmhost" "$recorders" "$open_issues"
  local mounts ns hosts guests i
  mounts="{\"host\": $(json_str "$sources"), \"guest\": \"/home/builder/.cache/homelab-sources\", \"read_only\": true}"
  mounts+=", {\"host\": $(json_str "$vmhost"), \"guest\": \"/home/builder/.cache/homelab-vmhost\"}"
  # One mount per cache namespace (Go's mod and build share a parent), and
  # the vm key as an environment value, not a mount. The daemon adds one
  # folder, the job's share, beside its two sockets. On taichi (smolvm
  # 1.32.4, nested, with a storage disk and two sockets) a machine started
  # with 8 shared folders on 2026-11-04; the co-located kind once failed
  # with 7 ("no more IRQs are available"), so the kit keeps to 7 in all.
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
    mounts+=", {\"host\": $(json_str "${pair%%$'\n'*}"), \"guest\": $(json_str "${pair#*$'\n'}")}"
    count=$((count + 1))
  done
  mounts+=", $(homelab_state_mounts "$recorders" "$open_issues")"
  count=$((count + 2))
  (( count <= 6 )) || die "a homelab machine would get $count shared folders; the kit gives it at most 6 besides the daemon's own"

  homelab_pack "$hl"
  export STAGE0_HOMELAB_IMAGE_JSON
  STAGE0_HOMELAB_IMAGE_JSON=$(homelab_image_json "$krunfw" "$mounts")
}

# homelab_state_mounts RECORDERS OPEN_ISSUES: homelab's state folders, as
# items of a JSON list. The flight recorder's folder is one per job, written
# by the job, and outlives the machine; a job's own open-issue captures go
# there (HOMELAB_STATE_HOME=/var/log/homelab-recorder, homelab's AGENTS.md).
# The host's open-issue evidence (homelab.open_issues capture, under
# ~/.local/state/homelab/open-issues) is read-only, where homelab's own
# paths name it, so a job reads the evidence a row in docs/open-issues.md
# points to and cannot change it.
homelab_state_mounts() {
  printf '%s' "{\"host\": $(json_str "$1"), \"guest\": \"/var/log/homelab-recorder\", \"per_job\": true}, {\"host\": $(json_str "$2"), \"guest\": \"/home/builder/.local/state/homelab/open-issues\", \"read_only\": true}"
}

# homelab_image_json KRUNFW MOUNTS: the daemon's smolvm.images entry for the
# homelab pack, with homelab's guest kernel and the shared folders (a JSON
# list's items). Reads STAGE0_EGRESS_CA.
homelab_image_json() {
  local krunfw=$1 mounts=$2
  printf '%s\n' "{
        \"user\": \"builder\",
        \"lib_dir\": $(json_str "$krunfw"),
        \"mounts\": [$mounts],
        \"env\": {\"NODE_EXTRA_CA_CERTS\": $(json_str "$STAGE0_GUEST_CA"), \"IS_SANDBOX\": \"1\"},
        \"env_files\": {\"HOMELAB_VM_AGE_KEY\": $(json_str "$STAGE0_HOME/inputs/vm-age/keys.txt"), \"ORTUS_EGRESS_CA\": $(json_str "$STAGE0_EGRESS_CA")},
        \"setup\": [$(json_str "$(guest_ca_setup)"), $(json_str "$(homelab_setup)")]
      }"
}

# homelab_setup: what the homelab workflow's hooks did around a machine
# (design D7), as builder, homelab's guest user, with the host user's uid:
# the vm key where sops looks, then guest-start.sh (the flight recorder and
# swap, through builder's passwordless sudo; its swap file goes on the
# storage disk at /storage, not /workspace, the story's checkout), then the
# vm-host preparation. No registry cache: the dev host's caches listen on its
# loopback, which a machine does not reach.
homelab_setup() {
  cat <<'EOF'
set -eu
# The read-only open-issues mount leaves its parents to root: give them back.
sudo chown "$(id -u):$(id -g)" /home/builder/.local/state /home/builder/.local/state/homelab
mkdir -p /home/builder/.config/sops/age
(umask 077; printf '%s\n' "$HOMELAB_VM_AGE_KEY" > /home/builder/.config/sops/age/keys.txt)
sudo env HOMELAB_SWAP_FILE=/storage/swapfile /opt/homelab-sandbox/guest-start.sh "$(id -u)" --swap-gib 8
HOMELAB_CACHE_HOME=/home/builder/.cache VMHOST_CACHE=/home/builder/.cache/homelab-vmhost \
  VMHOST_REGISTRY_CACHE= scripts/vmhost/prepare.sh
EOF
}

# homelab_pack HL: stage homelab's image scripts and the kit's harness into one
# build context (design D12) and build the pack, named by a hash of that
# context and of the toolchain, so a change to either builds a new one. While
# no job runs, the packs it replaced are deleted.
homelab_pack() {
  local hl=$1 stage=$STAGE0_HOME/pack-src packs=$STAGE0_HOME/packs hash pack old
  rm -rf "$stage"
  mkdir -p "$stage/homelab" "$stage/harness" "$packs"
  cp "$KIT/homelab-pack/smol-builder.toml" "$KIT/homelab-pack/Dockerfile" "$stage/"
  cp "$hl/scripts/vmhost/install-tools.sh" "$hl/scripts/vmhost/fetch.sh" \
     "$hl/scripts/smolvm/image/guest-setup.sh" "$hl/scripts/smolvm/image/guest-start.sh" \
     "$hl/scripts/smolvm/image/recorder.py" "$stage/homelab/"
  # homelab's guest-setup.sh gives its guest user these ids: smolvm's shared
  # folders let a guest write only as their host owner, this user.
  printf '%s:%s\n' "$(id -u)" "$(id -g)" > "$stage/homelab/guest-uid"
  cp "$KIT/pack/install-harness.sh" "$KIT/pack/install-opencode-plugins.sh" \
     "$KIT/pack/agent-acp" "$KIT/pack/claude-settings.json" \
     "$KIT/pack/opencode.json" "$stage/harness/"
  hash=$({
    (cd "$stage" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
    # smol-builder's source, which embeds its layers; not its binary, which
    # Go stamps with the kit's commit, so every kit change would build anew.
    (cd "$ROOT" && find smol-builder -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
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
  if (( $(worker_jobs) == 0 )); then
    for old in "$packs"/homelab-*.smolmachine "$packs"/*.tmp; do
      [[ -e $old && $old != "$pack" ]] || continue
      rm -f "$old" && say "Deleted the unused pack $(basename "$old")"
    done
  fi
  export STAGE0_HOMELAB_PACK=$pack
}
