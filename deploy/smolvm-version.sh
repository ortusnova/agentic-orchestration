# shellcheck shell=bash
# Sourced by deploy.sh, and by test-smolvm-version.sh.

# The oldest smolvm release Sortie accepts. There is no upper bound.
SMOLVM_MIN=1.32.4

# smolvm_version_ok: the smolvm on PATH is SMOLVM_MIN or later. It prints
# the version it found, or nothing when smolvm prints none it can read.
smolvm_version_ok() {
  local out have want i
  out=$(smolvm --version 2>/dev/null) || return 1
  [[ $out =~ ^smolvm\ ([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || return 1
  echo "${out#smolvm }"
  have=("${BASH_REMATCH[@]:1}")
  IFS=. read -ra want <<< "$SMOLVM_MIN"
  for i in 0 1 2; do
    ((10#${have[i]} > 10#${want[i]})) && return 0
    ((10#${have[i]} < 10#${want[i]})) && return 1
  done
  return 0
}
