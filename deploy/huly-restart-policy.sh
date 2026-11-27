# shellcheck shell=bash
# Keeps the Huly compose stack whole: every container restarts unless
# stopped, and a stack with any container down is started again. Sourced by
# deploy.sh, dev/dev.sh, dev/live/live.sh and test-huly-restart-policy.sh.
#
# huly-selfhost's compose.yml gives redpanda, Huly's message queue, no
# restart policy. A restart policy set on a container is lost when compose
# recreates it, so the policy lives in the compose files themselves
# (huly_compose_override), and huly_restart_policies only fixes containers
# created before that. A down redpanda leaves nginx and the front end
# serving, so /config.json answers while every workspace call fails: Huly
# is up only when every container of its project runs (huly_down_containers).
# A service that reads the queue and started while redpanda was down fails
# to create its topics and keeps running broken, never retrying: the
# override starts it after redpanda, and huly_stale_consumers finds one that
# started before it. nginx, Huly's entry, resolves each service's address
# once, at its start: after compose recreates a service, nginx sends its
# requests to the old address until it restarts (huly_stale_nginx).

HULY_OVERRIDE_SRC=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/huly-compose.override.yml
HULY_OVERRIDE_MARK='# kit: huly-compose.override.yml'
# The compose services that read redpanda (QUEUE_CONFIG); the override
# starts each after it.
HULY_QUEUE_SERVICES="account fulltext transactor workspace"

# huly_compose_override DIR: install the kit's override as
# DIR/compose.override.yml, which compose merges on every `compose up`, and
# keep it out of the checkout's git status. A compose.override.yml the kit
# did not write is never replaced: it fails, naming the file. Prints the
# path when it changed the file.
huly_compose_override() {
  local dir=$1 dst=$1/compose.override.yml exclude
  [[ -f $dir/compose.yml ]] || { echo "huly_compose_override: no compose.yml in $dir" >&2; return 1; }
  if [[ -e $dst && $(head -n 1 "$dst") != "$HULY_OVERRIDE_MARK"* ]]; then
    echo "huly_compose_override: $dst is not the kit's; merge huly-compose.override.yml into it, or remove it" >&2
    return 1
  fi
  if ! cmp -s "$HULY_OVERRIDE_SRC" "$dst"; then
    cp "$HULY_OVERRIDE_SRC" "$dst.tmp" && mv "$dst.tmp" "$dst"
    echo "$dst"
  fi
  exclude=$dir/.git/info/exclude
  if [[ -d $dir/.git/info ]] && ! grep -qxF /compose.override.yml "$exclude" 2>/dev/null; then
    echo /compose.override.yml >> "$exclude"
  fi
}

# huly_restart_policies DIR: give every container of the Huly compose project
# in DIR the restart policy unless-stopped, so podman-restart.service starts
# it after a reboot. podman update changes the policy in place, without
# recreating the container. Prints each container it changed.
huly_restart_policies() {
  local dir=$1 id name policy
  while read -r id; do
    [[ -n $id ]] || continue
    read -r name policy < <(podman inspect --format '{{.Name}} {{.HostConfig.RestartPolicy.Name}}' "$id")
    [[ $policy == unless-stopped || $policy == always ]] && continue
    podman update --restart unless-stopped "$id" > /dev/null
    echo "$name"
  done < <(podman ps -aq --filter "label=com.docker.compose.project.working_dir=$dir")
}

# huly_down_containers DIR: print each container of the Huly compose project
# in DIR that is not running; print "none" when the project has no
# container at all.
huly_down_containers() {
  local dir=$1 name state any=
  while read -r name state; do
    [[ -n $name ]] || continue
    any=1
    [[ $state == running ]] || echo "$name"
  done < <(podman ps -a --filter "label=com.docker.compose.project.working_dir=$dir" --format '{{.Names}} {{.State}}')
  [[ -n $any ]] || echo none
}

# huly_stale_consumers DIR: print each running container of the Huly
# compose project in DIR, of a service of HULY_QUEUE_SERVICES, that last
# started before its redpanda did. Prints nothing while redpanda does not
# run: huly_down_containers names it then.
huly_stale_consumers() {
  local dir=$1 name service state started queue=
  while read -r name service state started; do
    [[ $service == redpanda && $state == running ]] && queue=$started
  done < <(huly_containers "$dir")
  [[ -n $queue ]] || return 0
  while read -r name service state started; do
    [[ $state == running && " $HULY_QUEUE_SERVICES " == *" $service "* ]] || continue
    ((started < queue)) && echo "$name"
  done < <(huly_containers "$dir")
}

# huly_stale_nginx DIR: print the nginx container of the Huly compose
# project in DIR when it runs and last started before another running
# container of the project did.
huly_stale_nginx() {
  local name service state started nginx='' at=0 latest=0
  while read -r name service state started; do
    [[ $state == running ]] || continue
    if [[ $service == nginx ]]; then nginx=$name at=$started
    elif ((started > latest)); then latest=$started; fi
  done < <(huly_containers "$1")
  [[ -n $nginx ]] && ((at < latest)) && echo "$nginx"
  return 0
}

# huly_containers DIR: each container of the compose project in DIR as
# "name service state started", started in Unix seconds.
huly_containers() {
  local id
  while read -r id; do
    [[ -n $id ]] || continue
    podman inspect --format '{{.Name}} {{index .Config.Labels "com.docker.compose.service"}} {{.State.Status}} {{.State.StartedAt.Unix}}' "$id"
  done < <(podman ps -aq --filter "label=com.docker.compose.project.working_dir=$1")
}

# huly_ensure DIR [COMPOSE...]: make the Huly stack in DIR whole. It
# installs the override, fixes the policy of older containers, runs
# COMPOSE up -d (default: podman compose) when any container is down or
# none exists, then restarts each queue service that started before
# redpanda, and last nginx when it started before any other service;
# compose starts what is down and recreates only what the compose files
# changed. Says what it did on stderr.
huly_ensure() {
  local dir=$1 down name stale
  shift
  [[ $# -gt 0 ]] || set -- podman compose
  huly_compose_override "$dir" > /dev/null || return 1
  for name in $(huly_restart_policies "$dir"); do echo "Huly container $name now restarts unless stopped" >&2; done
  down=$(huly_down_containers "$dir")
  if [[ -n $down ]]; then
    echo "Starting Huly in $dir (down: $(echo "$down" | tr '\n' ' '))" >&2
    # The containers outlive the caller; they must not hold its lock (fd 9).
    (cd "$dir" && "$@" up -d > /dev/null 9>&-) || return 1
  fi
  stale=$(huly_stale_consumers "$dir")
  if [[ -n $stale ]]; then
    # shellcheck disable=SC2086 # one name per word
    echo "Restarting Huly's queue services that started before redpanda: "$stale >&2
    # shellcheck disable=SC2086
    podman restart $stale > /dev/null 9>&- || return 1
  fi
  stale=$(huly_stale_nginx "$dir")
  [[ -z $stale ]] && return 0
  echo "Restarting $stale, which started before a service it serves" >&2
  podman restart "$stale" > /dev/null 9>&-
}
