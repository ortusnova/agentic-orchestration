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
#
# $STAGE0_HOME/started/UNIT.sum records one digest of the PATHs each start.
# A running unit with no record yet was started by an earlier kit, which
# restarted it at every stage0.sh: it is adopted as it runs.

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
    # An earlier kit restarted these units at every stage0.sh and kept no
    # record: what runs is what that stage0.sh built, so it is adopted as is.
    if [[ ! -e $rec ]]; then
      echo "$sum" > "$rec"
      echo "$unit runs, started by an earlier kit: left running, with its runs (restart it by hand to be sure)" >&2
      return 0
    fi
  fi
  echo restarting > "$rec"
  systemctl --user restart "$unit" || return 1
  echo "$sum" > "$rec"
}
