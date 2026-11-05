# shellcheck shell=bash
# Sourced by stage0.sh on a worker that offers nested (worker-config.sh,
# host_nested). live_proof_pack builds the pack the live-proof job profile
# nested runs in (WORKFLOW.md environment.remote.profiles; README.md "Live
# proofs"), gives its jobs the stage-0 pack as the base pack the proofs
# start machines from (live_proof_base), and exports, for
# render_worker_config:
#   STAGE0_LIVE_PROOF_PACK        the live-proof pack, built when its inputs change
#   STAGE0_LIVE_PROOF_IMAGE_JSON  the daemon's smolvm.images entry for it
# Needs stage0.sh's say, die, STAGE0_* settings, STAGE0_PACK, REBUILD_PACK,
# worker-config.sh and staged-pack.sh.

# The host folder that holds the base pack alone, shared read-only with a
# live-proof job at LIVE_PROOF_BASE_DIR; its pack is LIVE_PROOF_BASE_PACK
# there.
STAGE0_LIVE_PROOF_BASE=$STAGE0_HOME/live-proof-base
LIVE_PROOF_BASE_DIR=/opt/live-proof/base
LIVE_PROOF_BASE_PACK=$LIVE_PROOF_BASE_DIR/base.smolmachine

# live_proof_pack: stage live-proof-pack/ and the kit's harness into one build
# context and build the pack (staged-pack.sh), as homelab_pack does.
live_proof_pack() {
  local stage=$STAGE0_HOME/live-proof-pack-src
  rm -rf "$stage"
  mkdir -p "$stage"
  cp "$KIT/live-proof-pack/smol-builder.toml" "$KIT/live-proof-pack/Dockerfile" \
     "$KIT/live-proof-pack/check-tools.sh" "$KIT/live-proof-pack/nested-probe.sh" "$stage/"
  stage_harness "$stage/harness"
  build_staged_pack live-proof "$stage"
  rm -rf "$stage"
  live_proof_base
  export STAGE0_LIVE_PROOF_PACK=$STAGED_PACK STAGE0_LIVE_PROOF_IMAGE_JSON
  STAGE0_LIVE_PROOF_IMAGE_JSON=$(live_proof_image_json)
}

# live_proof_base: the stage-0 pack, whose base layer has sh, git and socat,
# as base.smolmachine in STAGE0_LIVE_PROOF_BASE, the one folder a live-proof
# job sees of this host: the pack a proof of the worker daemon's smolvm tests
# starts its machines from (worker-daemon/live-proofs.json). A hard link
# where the file system allows one, a copy otherwise, each replaced in one
# rename, so a running job keeps the pack it opened.
live_proof_base() {
  local base=$STAGE0_LIVE_PROOF_BASE/base.smolmachine
  [[ -s $STAGE0_PACK ]] || die "the stage-0 pack $STAGE0_PACK is missing: the live-proof base pack is made from it"
  mkdir -p "$STAGE0_LIVE_PROOF_BASE"
  [[ $base -ef $STAGE0_PACK ]] && return 0
  ln -f "$STAGE0_PACK" "$base.tmp" 2> /dev/null || cp "$STAGE0_PACK" "$base.tmp"
  mv -f "$base.tmp" "$base"
  say "The live-proof base pack is the stage-0 pack ($base)"
}

# live_proof_image_json: the daemon's smolvm.images entry for the live-proof
# pack: the stage-0 image's, root with the egress CA, and the base pack's
# folder read-only, named by LIVE_PROOF_BASE_PACK. Reads STAGE0_EGRESS_CA.
# Its job runs as root, which opens /dev/kvm for the machines the proof
# starts.
live_proof_image_json() {
  printf '%s\n' "{
        \"mounts\": [{\"host\": $(json_str "$STAGE0_LIVE_PROOF_BASE"), \"guest\": $(json_str "$LIVE_PROOF_BASE_DIR"), \"read_only\": true}],
        \"env\": {\"NODE_EXTRA_CA_CERTS\": $(json_str "$STAGE0_GUEST_CA"), \"IS_SANDBOX\": \"1\", \"LIVE_PROOF_BASE_PACK\": $(json_str "$LIVE_PROOF_BASE_PACK")},
        \"env_files\": {\"ORTUS_EGRESS_CA\": $(json_str "$STAGE0_EGRESS_CA")},
        \"setup\": [$(json_str "$(guest_ca_setup)")]
      }"
}
