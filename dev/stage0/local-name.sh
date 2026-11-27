# shellcheck shell=bash
# Sourced by stage0.sh, and by test-local-name.sh.
#
# The workbench's local name. homelab's workstation-setup.sh gives TLS and a
# proxy to each local service declared in its local-names directory, one
# `<name> <port>` per line (homelab's homelab/state_paths.py, `local-names`).
# The kit declares its workbench there; the next run of
# `sudo ./workstation-setup.sh` in homelab names it.

# The directory, under HOME (stage0.sh, test-local-name.sh).
# shellcheck disable=SC2034
STAGE0_LOCAL_NAMES_REL=.config/homelab/local-names.d

# write_local_name DIR NAME PORT: DIR/ortus-stage0-workbench holds `NAME PORT`.
# It prints whether the file changed, so a changed port can be followed by
# a run of workstation-setup.sh.
write_local_name() {
  local dir=$1 name=$2 port=$3 file want
  [[ $name =~ ^([a-z0-9-]+\.)+homelab\.dev$ ]] || { echo "local name $name is not a lower-case name under homelab.dev" >&2; return 1; }
  if ! [[ $port =~ ^[0-9]{1,5}$ ]] || ((port < 1 || port > 65535)); then
    echo "local name port $port is not 1-65535" >&2
    return 1
  fi
  file=$dir/ortus-stage0-workbench
  want="# The stage-0 kit's workbench (agentic-orchestration dev/stage0/local-name.sh).
$name $port"
  if [[ -f $file && $(<"$file") == "$want" ]]; then
    echo unchanged
    return 0
  fi
  mkdir -p "$dir"
  printf '%s\n' "$want" > "$file.tmp" && mv "$file.tmp" "$file"
  echo changed
}
