# shellcheck shell=bash
# Sourced by stage0.sh and test-unit-restart.sh. A deploy never waits for
# the runs going (spec store agent-platform/run-supervision): Sortie and the
# workbench restart at once, as Sortie re-adopts the runs its workers hold.
# nono, the egress gate, Pomerium and the worker daemon carry those runs (their egress, their
# stream, their machines), so stage0.sh restarts each only when what it runs
# changed, or when it is not running. Needs STAGE0_HOME.
#
#   restart_changed UNIT PATH...  restart UNIT unless it runs and every PATH
#                                 (a file, or a directory: all its files) is
#                                 as when it last started
#   units_unsettled               each unit whose restart began and did not
#                                 end with the unit running
#
# $STAGE0_HOME/started/UNIT.sum records one digest of the PATHs each start,
# once the unit runs after it. A running unit with no record is restarted:
# what it runs is not known, as for an egress gate an earlier kit left
# running on its own configuration (ONS-123). A deploy is applied only once
# units_unsettled names none (deploy.sh).

# inputs_sum PATH...: one digest of the PATHs' names and contents; a
# missing PATH counts as missing.
inputs_sum() {
  local p
  for p; do
    if [[ -d $p ]]; then
      echo "$p/"
      (cd "$p" && find . -type f -not -path '*/.git/*' -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum)
    elif [[ -e $p ]]; then
      echo "$p $(sha256sum < "$p")"
    else
      echo "$p missing"
    fi
  done | sha256sum | cut -d' ' -f1
}

restart_changed() {
  local unit=$1 rec sum
  shift
  rec=$STAGE0_HOME/started/$unit.sum
  sum=$(inputs_sum "$@")
  mkdir -p "${rec%/*}"
  if systemctl --user is-active -q "$unit"; then
    if [[ -s $rec && $(cat "$rec") == "$sum" ]]; then
      echo "$unit runs what it would run now: left running, with its runs" >&2
      return 0
    fi
    [[ -e $rec ]] || echo "$unit runs with no record of what it runs: restarting it" >&2
  fi
  echo restarting > "$rec"
  systemctl --user restart "$unit" || { echo "$unit did not restart" >&2; return 1; }
  systemctl --user is-active -q "$unit" || { echo "$unit is not running after its restart" >&2; return 1; }
  echo "$sum" > "$rec"
  echo "$unit restarted: what it runs changed" >&2
}

units_unsettled() {
  local rec
  for rec in "$STAGE0_HOME"/started/*.sum; do
    [[ -e $rec && $(cat "$rec") == restarting ]] && basename "$rec" .sum
  done
  return 0
}
