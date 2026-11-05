# shellcheck shell=bash
# Sourced by stage0.sh on a worker that offers nested (worker-config.sh,
# host_nested). live_proof_pack builds the pack the live-proof job profile
# nested runs in (WORKFLOW.md environment.remote.profiles; README.md "Live
# proofs") and exports, for render_worker_config:
#   STAGE0_LIVE_PROOF_PACK        the live-proof pack, built when its inputs change
#   STAGE0_LIVE_PROOF_IMAGE_JSON  the daemon's smolvm.images entry for it
# Needs stage0.sh's say, die, STAGE0_* settings, REBUILD_PACK,
# worker-config.sh and staged-pack.sh.

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
  export STAGE0_LIVE_PROOF_PACK=$STAGED_PACK STAGE0_LIVE_PROOF_IMAGE_JSON
  STAGE0_LIVE_PROOF_IMAGE_JSON=$(live_proof_image_json)
}

# live_proof_image_json: the daemon's smolvm.images entry for the live-proof
# pack: the stage-0 image's, root with the egress CA. Reads STAGE0_EGRESS_CA.
# Its job runs as root, which opens /dev/kvm for the machines the proof
# starts.
live_proof_image_json() {
  printf '%s\n' "{
        \"env\": {\"NODE_EXTRA_CA_CERTS\": $(json_str "$STAGE0_GUEST_CA"), \"IS_SANDBOX\": \"1\"},
        \"env_files\": {\"ORTUS_EGRESS_CA\": $(json_str "$STAGE0_EGRESS_CA")},
        \"setup\": [$(json_str "$(guest_ca_setup)")]
      }"
}
