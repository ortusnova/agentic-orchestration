# shellcheck shell=bash
# Sourced by homelab-inputs.sh and live-proof-pack.sh. Needs stage0.sh's say
# and die, ROOT, STAGE0_HOME, REBUILD_PACK and worker-config.sh.

# build_staged_pack NAME STAGE: build the pack of the build context STAGE,
# named by a hash of that context and of the toolchain, so a change to either
# builds a new one: $STAGE0_HOME/packs/NAME-<hash>.smolmachine, with
# smol-builder's report in $STAGE0_HOME/NAME-pack-report.json. While no job
# runs, the NAME packs it replaced are deleted. Sets STAGED_PACK to the pack.
build_staged_pack() {
  local name=$1 stage=$2 packs=$STAGE0_HOME/packs hash pack old report
  report=$STAGE0_HOME/$name-pack-report.json
  mkdir -p "$packs"
  hash=$({
    (cd "$stage" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
    # smol-builder's source, which embeds its layers; not its binary, which
    # Go stamps with the kit's commit, so every kit change would build anew.
    (cd "$ROOT" && find smol-builder -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum)
    smolvm --version
  } | sha256sum | cut -c1-16)
  pack=$packs/$name-$hash.smolmachine
  if [[ -n ${REBUILD_PACK:-} || ! -s $pack ]]; then
    say "Building the $name pack $hash (report $report)"
    smol-builder build --repo "$stage" --output "$pack.tmp" > "$report" \
      || { rm -f "$pack.tmp"; die "the $name pack did not build: $report"; }
    mv "$pack.tmp" "$pack"
  fi
  if (( $(worker_jobs) == 0 )); then
    for old in "$packs"/"$name"-*.smolmachine "$packs"/*.tmp; do
      [[ -e $old && $old != "$pack" ]] || continue
      rm -f "$old" && say "Deleted the unused pack $(basename "$old")"
    done
  fi
  # shellcheck disable=SC2034 # the caller reads it
  STAGED_PACK=$pack
}

# stage_harness DIR: the stage-0 agent harness (pack/), as a pack built on a
# build context of its own installs it with install-harness.sh.
stage_harness() {
  mkdir -p "$1"
  cp "$KIT/pack/install-harness.sh" "$KIT/pack/install-opencode-plugins.sh" \
     "$KIT/pack/agent-acp" "$KIT/pack/agent-usage" "$KIT/pack/sortie-usage.js" \
     "$KIT/pack/claude-settings.json" \
     "$KIT/pack/opencode.json" "$1/"
}
