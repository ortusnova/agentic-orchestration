# shellcheck shell=bash
# Sourced by deploy.sh, and by test-local-name.sh.
#
# The workbench's local name. homelab's workstation-setup.sh gives TLS and a
# proxy to each local service declared in its local-names directory, one
# `<name> <port>` per line (homelab's homelab/state_paths.py, `local-names`).
# The kit declares its workbench there; the next run of
# `sudo ./workstation-setup.sh` in homelab names it.

# The directory, under HOME (deploy.sh, test-local-name.sh).
# shellcheck disable=SC2034
ORTUS_NOVA_LOCAL_NAMES_REL=.config/homelab/local-names.d

# write_local_name DIR NAME PORT: DIR/$ORTUS_NOVA_LOCAL_NAME_FILE (names.sh)
# holds `NAME PORT`; with NAME empty, the environment declares no local
# name, and the file is removed. It prints whether the file changed, so a
# change can be followed by a run of workstation-setup.sh. It writes no
# other file in DIR.
write_local_name() {
  local dir=$1 name=$2 port=$3 file want
  file=$dir/$ORTUS_NOVA_LOCAL_NAME_FILE
  if [[ -z $name ]]; then
    [[ -e $file ]] || { echo unchanged; return 0; }
    rm -f "$file"
    echo changed
    return 0
  fi
  [[ $name =~ ^([a-z0-9-]+\.)+homelab\.dev$ ]] || { echo "local name $name is not a lower-case name under homelab.dev" >&2; return 1; }
  if ! [[ $port =~ ^[0-9]{1,5}$ ]] || ((port < 1 || port > 65535)); then
    echo "local name port $port is not 1-65535" >&2
    return 1
  fi
  want="# The workbench of environment $ORTUS_NOVA_ENV (agentic-orchestration deploy/local-name.sh).
$name $port"
  if [[ -f $file && $(<"$file") == "$want" ]]; then
    echo unchanged
    return 0
  fi
  mkdir -p "$dir"
  printf '%s\n' "$want" > "$file.tmp" && mv "$file.tmp" "$file"
  echo changed
}
