# shellcheck shell=bash
# Sourced by stage0.sh, and by test-huly-restart-policy.sh.

# huly_restart_policies DIR: give every container of the Huly compose project
# in DIR the restart policy unless-stopped, so podman-restart.service starts
# it after a reboot. huly-selfhost's compose.yml leaves redpanda, Huly's
# message queue, without one; after a stop it stayed down while the rest came
# back, and every Huly login failed. podman update changes the policy in
# place, without recreating the container. Prints each container it changed.
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
