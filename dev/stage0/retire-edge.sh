# shellcheck shell=bash
# Sourced by stage0.sh, and by test-retire-edge.sh.

# The units of the stage-1 edge, which the control plane's Pomerium replaced
# (control-plane/, design D18): the edge, and one SSH tunnel per worker
# host. nono and its token timer stay: nono is the egress behind Pomerium's
# egress route, as it was behind the edge's.
RETIRED_UNITS=(ortus-stage0-edge.service)

# retire_edge DIR: stop and disable each retired unit a host still has, and
# every worker tunnel, and delete their unit files from DIR. Prints each
# unit it retired. A host without them is left alone.
retire_edge() {
  local dir=$1 unit units=()
  for unit in "${RETIRED_UNITS[@]}"; do
    [[ -e $dir/$unit ]] && units+=("$unit")
  done
  while read -r unit _; do
    [[ $unit == ortus-stage0-worker-tunnel@*.service ]] && units+=("$unit")
  done < <(systemctl --user list-units --all --plain --no-legend 'ortus-stage0-worker-tunnel@*' 2>/dev/null || true)
  [[ -e $dir/ortus-stage0-worker-tunnel@.service ]] || [[ ${#units[@]} -gt 0 ]] || return 0
  for unit in "${units[@]}"; do
    systemctl --user disable -q --now "$unit" 2>/dev/null || true
    echo "$unit"
  done
  for unit in "${RETIRED_UNITS[@]}" ortus-stage0-worker-tunnel@.service; do
    rm -f "${dir:?}/$unit"
  done
}
