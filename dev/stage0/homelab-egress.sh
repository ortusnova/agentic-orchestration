# shellcheck shell=bash
# The egress of homelab jobs (spec store homelab-on-sortie, "The homelab
# workflow allows only what homelab declares"; agent-platform-workers D18).
# Sourced by stage0.sh on p1, after homelab_worktree (homelab-inputs.sh).
# The kit keeps no list of homelab's hosts: homelab declares them in
# scripts/smolvm/egress-hosts.yaml, and the homelab nono
# (ortus-stage0-nono-homelab, run-nono.sh homelab) allows exactly those, at
# the commit of the kit's homelab worktree, plus the hosts of the
# credentials nono holds (egress-profile.json). The edge sends it every
# CONNECT of a homelab job (edge-config.sh).
# Needs stage0.sh's say and die, KIT, STAGE0_HOME and stage0-edge on PATH.

# homelab_egress_profile OUT: render the homelab nono's profile into OUT
# from the kit's homelab worktree, or fail, leaving OUT as it was, when
# egress-hosts.yaml is missing or malformed.
homelab_egress_profile() {
  local out=$1 hosts=$STAGE0_HOME/homelab/scripts/smolvm/egress-hosts.yaml err
  [[ -f $hosts ]] || die "$hosts is missing: homelab declares the hosts its jobs may reach there, and the kit keeps no list of its own"
  err=$(stage0-edge homelab-profile -base "$KIT/egress-profile.json" -hosts "$hosts" -out "$out" 2>&1) \
    || die "homelab's egress-hosts.yaml is malformed, so homelab jobs get no egress profile: $err"
  say "homelab jobs' egress: $out, from homelab's egress-hosts.yaml at $(git -C "$STAGE0_HOME/homelab" rev-parse --short HEAD)"
}
