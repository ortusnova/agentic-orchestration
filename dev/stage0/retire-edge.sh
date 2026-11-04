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

# carry_edge_workers DIR ADD...: give each worker host of the stage-1 edge
# (DIR/NAME.env with WORKER_HOST, from the edge's worker-host.sh add) its
# certificate from the control plane's CA, with ADD HOST NAME, unless the
# worker list already names it. p1's own worker (LOCAL=1) has its own.
# Prints each worker it added. DIR stays, so a revert to the edge finds it.
carry_edge_workers() {
  local dir=$1 env name host listed
  shift
  [[ -d $dir ]] || return 0
  listed=$(front_workers) || return 1
  for env in "$dir"/*.env; do
    [[ -e $env ]] || continue
    name=$(basename "$env" .env)
    grep -qx 'LOCAL=1' "$env" && continue
    host=$(sed -n 's/^WORKER_HOST=//p' "$env" | tail -n 1)
    [[ -n $host ]] || continue
    grep -qx "$name" <<< "$listed" && continue
    "$@" "$host" "$name" > /dev/null || { echo "could not add worker $name ($host)" >&2; return 1; }
    echo "$name"
  done
}

# wait_for_worker_files DIR SECONDS: wait until DIR holds what p1's
# worker-host.sh add copies (the certificate, its key, the CA, the egress
# CA's certificate and, last, Pomerium's URL), checking every 10 s for at
# most SECONDS.
wait_for_worker_files() {
  local dir=$1 left=$2 f missing
  while :; do
    missing=''
    for f in cp-worker.crt cp-worker.key cp-ca.crt egress-ca.crt control-plane-url; do
      [[ -s $dir/$f ]] || { missing=$f; break; }
    done
    [[ -z $missing ]] && return 0
    ((left > 0)) || return 1
    [[ $left == "$2" ]] && echo "stage0: waiting up to $2 s for p1 to give this host its certificate ($dir/$missing)" >&2
    sleep "${STAGE0_WAIT_STEP:-10}"
    left=$((left - ${STAGE0_WAIT_STEP:-10}))
  done
}
