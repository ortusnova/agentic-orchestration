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
  restart_changed ortus-stage0-worker.service "$STAGE0_HOME/bin/worker-daemon" \
    "$STAGE0_HOME/worker.json" "$UNITS/ortus-stage0-worker.service"
}
