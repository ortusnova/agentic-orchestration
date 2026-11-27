#!/usr/bin/env bash
# shellcheck disable=SC2031 # the subshells that write the deploy state only read main's variables
# One host's step of a push (spec store deploy-environments, design D3).
# deploy.sh <env> on the control host runs it there, and over
# `ssh <destination> bash -s` on each other host, from the kit of the target
# commit; it needs nothing of the host's own kit.
#
#   bootstrap.sh ENV COMMIT ORIGIN STAGE0_NAMES [--migrate-stage0] [ARG...]
#
# STAGE0_NAMES is the environment file's stage0_names (true or false), which
# the control host's loader read.
#
# It fetches exactly COMMIT from ORIGIN into the host's live kit, a detached
# checkout in the environment's state directory (made at the first deploy;
# a host still under its stage-0 names keeps its stage-0 one when the
# environment holds the stage-0 installs, D6), checks
# that the kit holds COMMIT, and runs its `deploy/deploy.sh ENV --this-host
# --commit COMMIT ARG...`. A failed deploy goes back to the commit the kit
# ran and runs that commit's deploy again, so what the failed run changed is
# the earlier commit's, and records the failure in the host's deploy state.
# It exits non-zero when the deploy at COMMIT failed.
#
# With --migrate-stage0, a host still under its stage-0 names is migrated
# instead (design D7): the target commit's deploy/migrate-stage0.sh, from a
# copy outside the home it moves, checks, moves the host to ENV's names,
# installs COMMIT there and rolls the host back by itself on a failure; the
# kit is not moved before. A host already under ENV's names is deployed as
# without the flag. Only standard tools: bash, git, sed, tar.
#
# With --no-start (passed on to deploy.sh and migrate-stage0.sh), nothing
# may start: a failed deploy's kit still goes back, and the earlier
# commit's deploy runs again only with --no-start, when that kit has it.
set -euo pipefail
# Over ssh the script arrives on standard input: the whole of main is read
# before it runs, and nothing it starts reads the rest.
main() {
  [[ $# -ge 4 ]] || { echo "usage: bootstrap.sh ENV COMMIT ORIGIN STAGE0_NAMES [ARG...]" >&2; exit 2; }
  env=$1 commit=$2 origin=$3 stage0_names=$4
  shift 4
  say() { echo "bootstrap: $*" >&2; }
  [[ $env =~ ^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ ]] || { say "'$env' is not an environment name"; exit 2; }
  [[ $commit =~ ^[0-9a-f]{40}$ ]] || { say "'$commit' is not a full commit id"; exit 2; }
  migrate='' no_start='' args=()
  for a; do
    if [[ $a == --migrate-stage0 ]]; then migrate=1; else args+=("$a"); fi
    [[ $a == --no-start ]] && no_start=1
  done

  # The state directory the host's names give: the environment's, unless
  # the host is still under its stage-0 names. It runs before the host has
  # the kit, so it cannot ask names.sh; env_names gives the same
  # (test-push.sh).
  root=$HOME/.local/share/ortus-nova
  home=$root/$env
  [[ $stage0_names == true && ! -e $root/$env/host.env && -e $root/stage0/stage0.env ]] && home=$root/stage0
  kit=$home/kit
  mkdir -p "$home"
  if [[ ! -e $kit/.git ]]; then
    say "making the live kit $kit from $origin"
    git clone -q --no-checkout "$origin" "$kit"
  fi
  old=$(git -C "$kit" rev-parse -q --verify HEAD 2> /dev/null || true)
  git -C "$kit" fetch -q "$origin" "$commit" || { say "could not fetch $commit from $origin"; exit 1; }
  if [[ -n $migrate && $home == "$root/stage0" ]]; then
    say "$(hostname -s): migrating from its stage-0 names to $env's, at ${commit:0:12}"
    m=$(mktemp -d)
    git -C "$kit" archive "$commit" deploy | tar -x -C "$m"
    rc=0
    bash "$m/deploy/migrate-stage0.sh" "$env" --commit "$commit" "${args[@]}" 2> >(tee "$m/migrate.err" >&2) || rc=$?
    wait
    if (( rc == 0 )); then
      rm -rf "$m"
      exit 0
    fi
    error="the migration to $env's names failed at ${commit:0:12}: $(sed 's/\x1b\[[0-9;]*m//g' "$m/migrate.err" | grep -v '^[[:space:]]*$' | tail -n 1 || true)"
    # Where the host is now: back in its stage-0 home, unless the rollback
    # failed too.
    [[ -e $home ]] || home=$root/$env
    (
      # shellcheck source=names.sh
      . "$m/deploy/names.sh"
      export ORTUS_NOVA_HOME=$home
      env_names "$env"
      # shellcheck source=deploy-state.sh
      . "$m/deploy/deploy-state.sh"
      deploy_state_write failed "$(git -C "$home/kit" rev-parse -q --verify HEAD 2> /dev/null || true)" "$commit" "$error"
    )
    rm -rf "$m"
    say "$error"
    exit 1
  fi
  git -C "$kit" checkout -q --force --detach "$commit"
  [[ $(git -C "$kit" rev-parse HEAD) == "$commit" ]] || { say "the live kit does not hold $commit"; exit 1; }
  say "$(hostname -s): deploying $env at ${commit:0:12} (was ${old:0:12})"

  log=$home/deploy.err
  rc=0
  "$kit/deploy/deploy.sh" "$env" --this-host --commit "$commit" "${args[@]}" 2> >(tee "$log" >&2) || rc=$?
  wait
  (( rc == 0 )) && exit 0

  why=$(sed 's/\x1b\[[0-9;]*m//g' "$log" 2> /dev/null | grep -v '^[[:space:]]*$' | tail -n 1 || true)
  error="deploy.sh failed at ${commit:0:12}: $why"
  # The deploy state, with the target commit's functions, before the kit
  # goes back.
  states=$(mktemp -d)
  cp "$kit/deploy/names.sh" "$kit/deploy/deploy-state.sh" "$states/"
  if [[ -n $old && $old != "$commit" ]]; then
    say "back on ${old:0:12}, and its deploy again, to undo what ${commit:0:12} left"
    git -C "$kit" checkout -q --force --detach "$old"
    back=0
    if [[ -n $no_start ]] && ! grep -q -- '--no-start' "$kit/deploy/deploy.sh" 2> /dev/null; then
      error+="; ${old:0:12}'s deploy not run again: its kit has no --no-start, and nothing may start"
    elif [[ -n $no_start ]]; then
      "$kit/deploy/deploy.sh" "$env" --this-host --commit "$old" --no-start > /dev/null 2> "$log.back" || back=$?
    elif grep -q -- '--this-host' "$kit/deploy/deploy.sh" 2> /dev/null; then
      "$kit/deploy/deploy.sh" "$env" --this-host --commit "$old" > /dev/null 2> "$log.back" || back=$?
    elif [[ -x $kit/deploy/deploy.sh ]]; then
      ORTUS_NOVA_KIT_FROM_CHECKOUT=1 "$kit/deploy/deploy.sh" "$env" > /dev/null 2> "$log.back" || back=$?
    else
      STAGE0_HOME=$home "$kit/dev/stage0/stage0.sh" > /dev/null 2> "$log.back" || back=$?
    fi
    (( back == 0 )) || error+="; its deploy on ${old:0:12} failed too: $(sed 's/\x1b\[[0-9;]*m//g' "$log.back" | grep -v '^[[:space:]]*$' | tail -n 1)"
  fi
  (
    # shellcheck source=names.sh
    . "$states/names.sh"
    export ORTUS_NOVA_HOME=$home
    env_names "$env"
    # shellcheck source=deploy-state.sh
    . "$states/deploy-state.sh"
    deploy_state_write failed "$(git -C "$kit" rev-parse HEAD)" "$commit" "$error"
  )
  rm -rf "$states"
  say "$error"
  exit 1
}
main "$@" < /dev/null
