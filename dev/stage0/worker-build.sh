# shellcheck shell=bash
# Sourced by stage0.sh and test-worker-restart.sh, after unit-restart.sh.
# Builds the worker daemon and restarts it only when what it runs changed:
# its binary, its configuration or its unit. The worker daemon's machines
# carry the runs (spec store agent-platform/run-supervision), so a kit
# change the binary does not hold, as a test or a document under
# worker-daemon/, must not restart it. Needs ROOT, STAGE0_HOME and UNITS.
#
#   build_worker_daemon  build $STAGE0_HOME/bin/worker-daemon from
#                        $ROOT/worker-daemon
#   restart_worker       restart ortus-stage0-worker.service when its
#                        binary, configuration or unit changed

# The build is reproducible: -trimpath leaves out the checkout's path and
# -buildvcs=false the kit's commit, so the same code builds the same bytes
# wherever and whenever it is built.
build_worker_daemon() {
  (cd "$ROOT/worker-daemon" && CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -o "$STAGE0_HOME/bin/worker-daemon" ./cmd/worker-daemon)
}

restart_worker() {
  local rec=$STAGE0_HOME/started/ortus-stage0-worker.service.sum
  # An earlier kit recorded the daemon's source in place of its binary. A
  # daemon that runs from the very source this kit builds runs what is
  # built now: its record moves to the binary, and its jobs keep running.
  if [[ -s $rec ]] && systemctl --user is-active -q ortus-stage0-worker.service \
    && [[ $(cat "$rec") == "$(inputs_sum "$ROOT/worker-daemon" "$STAGE0_HOME/worker.json" "$UNITS/ortus-stage0-worker.service")" ]]; then
    inputs_sum "$STAGE0_HOME/bin/worker-daemon" "$STAGE0_HOME/worker.json" "$UNITS/ortus-stage0-worker.service" > "$rec"
    echo "ortus-stage0-worker.service runs the source this kit built: its record now names the binary" >&2
  fi
  restart_changed ortus-stage0-worker.service "$STAGE0_HOME/bin/worker-daemon" \
    "$STAGE0_HOME/worker.json" "$UNITS/ortus-stage0-worker.service"
}
