# shellcheck shell=bash
# The worker daemon's configuration on a stage-0 host (worker-daemon's
# docs/operator-guide.md), rendered from stage0.env. Sourced by stage0.sh,
# after stage0.env. Every worker, p1's and each worker host's, gets the
# same images, so the matcher may give a job to any of them (spec store
# agent-platform/one-sortie): the stage-0 pack as stage0.smolmachine, and,
# where homelab's inputs are on the host, the homelab pack as
# homelab.smolmachine with them.
STAGE0_WORKER_STATE=$STAGE0_HOME/worker
STAGE0_WORKER_RESERVE_MEM_GIB=${STAGE0_WORKER_RESERVE_MEM_GIB:-8}
STAGE0_WORKER_RESERVE_CPU=${STAGE0_WORKER_RESERVE_CPU:-2}
STAGE0_WORKER_FLOOR_DISK_GIB=${STAGE0_WORKER_FLOOR_DISK_GIB:-50}
# The image names WORKFLOW.md gives each repository.
STAGE0_AGENT_IMAGE=stage0.smolmachine
STAGE0_HOMELAB_IMAGE=homelab.smolmachine
# Where a machine's setup puts the egress CA's certificate, so it trusts
# nono's interception of the credential hosts.
STAGE0_GUEST_CA=/usr/local/share/ca-certificates/ortus-egress.crt

# json_str S: S as a JSON string.
json_str() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\t'/\\t}
  printf '"%s"' "$s"
}

# sortie_agent_cap's default: above every worker's run slots together
# (p1 3, taichi 2 in 2026-11). Raise it when the workers' slots pass it.
STAGE0_SORTIE_AGENT_CEILING=16

# sortie_agent_cap: Sortie's agent.max_concurrent_agents on p1. Under the
# remote kind the matcher places a job only on a worker with a free run slot
# and the CPU, memory and disk the job requires (sortie Matcher.Placeable),
# so every worker's own limit already holds, and a worker can join or leave
# without a Sortie restart. The Sortie-wide cap is then only a ceiling above
# the workers' slots together; a lower one leaves free workers idle while
# stories wait. STAGE0_SORTIE_MAX_AGENTS overrides it. STAGE0_MAX_AGENTS is
# p1's own worker's run slots, not this cap.
sortie_agent_cap() {
  local cap=${STAGE0_SORTIE_MAX_AGENTS:-$STAGE0_SORTIE_AGENT_CEILING}
  [[ $cap =~ ^[1-9][0-9]*$ ]] || { echo "STAGE0_SORTIE_MAX_AGENTS is $cap; want a positive whole number" >&2; return 1; }
  echo "$cap"
}

# The setup every image runs first: the egress CA's certificate, from
# ORTUS_EGRESS_CA (env_files), into the system's trust store.
guest_ca_setup() {
  cat <<EOF
set -eu
s=; [ "\$(id -u)" = 0 ] || s=sudo
printf '%s\\n' "\$ORTUS_EGRESS_CA" | \$s tee $STAGE0_GUEST_CA > /dev/null
\$s update-ca-certificates > /dev/null
EOF
}

# render_worker_config FILE NAME URL CA
#   NAME  the worker's name; the edge names it by its port anyway
#   URL   the control plane as this host reaches it: its edge port on p1,
#         or the tunnel's end on a worker host
#   CA    the egress CA's certificate on this host
# Reads STAGE0_PACK and STAGE0_MAX_AGENTS, and, when homelab_inputs ran,
# STAGE0_HOMELAB_PACK and STAGE0_HOMELAB_IMAGE_JSON. Without them the worker
# does not declare nested: it has no image that needs it.
render_worker_config() {
  local out=$1 name=$2 url=$3 ca=$4 packs images nested=false
  packs="$(json_str "$STAGE0_AGENT_IMAGE"): $(json_str "$STAGE0_PACK")"
  images="$(json_str "$STAGE0_AGENT_IMAGE"): {
        \"env\": {\"NODE_EXTRA_CA_CERTS\": $(json_str "$STAGE0_GUEST_CA"), \"IS_SANDBOX\": \"1\"},
        \"env_files\": {\"ORTUS_EGRESS_CA\": $(json_str "$ca")},
        \"setup\": [$(json_str "$(guest_ca_setup)")]
      }"
  if [[ -n ${STAGE0_HOMELAB_PACK:-} ]]; then
    packs+=", $(json_str "$STAGE0_HOMELAB_IMAGE"): $(json_str "$STAGE0_HOMELAB_PACK")"
    images+=",
      $(json_str "$STAGE0_HOMELAB_IMAGE"): $STAGE0_HOMELAB_IMAGE_JSON"
    nested=true
  fi
  mkdir -p "$STAGE0_WORKER_STATE"
  (umask 077; cat > "$out.tmp" <<EOF
{
  "worker_id": $(json_str "$name"),
  "control_plane": {"url": $(json_str "$url")},
  "run_slots": ${STAGE0_MAX_AGENTS:-1},
  "reserve": {"mem_gib": $STAGE0_WORKER_RESERVE_MEM_GIB, "cpu": $STAGE0_WORKER_RESERVE_CPU},
  "floor": {"disk_gib": $STAGE0_WORKER_FLOOR_DISK_GIB},
  "smallest_job": {"cpu": 4, "mem_gib": 8, "disk_gib": 20},
  "state_dir": $(json_str "$STAGE0_WORKER_STATE"),
  "smolvm": {
    "packs": {$packs},
    "nested": $nested,
    "images": {
      $images
    }
  }
}
EOF
  )
  mv "$out.tmp" "$out"
}

# worker_jobs: how many jobs the worker daemon runs now (one directory each).
worker_jobs() {
  local n=0 d
  for d in "$STAGE0_WORKER_STATE"/jobs/*/; do
    [[ -d $d ]] && n=$((n + 1))
  done
  echo "$n"
}

# register_local_worker NAME: p1's own worker in $STAGE0_WORKERS (edge-config.sh).
# Its edge port is the one every worker host's tunnel ends on, so on p1 too
# the flows are at 127.0.0.1:$STAGE0_EDGE_WORKER_PORT, as the workflow names
# them, and it needs no tunnel.
register_local_worker() {
  local f=$STAGE0_WORKERS/$1.env want
  want=$(printf 'LOCAL=1\nEDGE_PORT=%s' "$STAGE0_EDGE_WORKER_PORT")
  mkdir -p "$STAGE0_WORKERS"
  [[ -e $f && $(cat "$f") == "$want" ]] && return 0
  (umask 077; printf '%s\n' "$want" > "$f")
}

# retire_sortie UNITS: a worker host runs no Sortie (spec store
# agent-platform/one-sortie). Stop the one an earlier profile left here and
# remove its unit; its workspaces, its database and recorder folders stay.
# It stops it only once p1's Sortie holds every merge record it holds
# (worker-host.sh carry, on p1, which leaves merges-carried.json here), so
# no story waits on p1 for a blocker that merged here; until then it fails
# and the Sortie runs on. Does nothing once it is gone.
retire_sortie() {
  local units=$1 unit=ortus-stage0-sortie.service db=$STAGE0_HOME/sortie/.sortie.db
  local carried=$STAGE0_HOME/merges-carried.json left
  [[ -e $units/$unit ]] || return 0
  if [[ -e $db ]]; then
    left=$(python3 "$KIT/merges.py" missing "$db" "$carried") || {
      say "Could not read this host's Sortie's merge records ($db): it keeps running"
      return 1
    }
    if [[ -n $left ]]; then
      say "p1's Sortie lacks $(wc -l <<< "$left") merge record(s) of this host's Sortie, so it keeps running: on p1, run dev/stage0/worker-host.sh carry $(hostname -s), then this again"
      return 1
    fi
  fi
  say "Retiring this host's Sortie: p1's Sortie dispatches every repository now, and holds its merge records"
  systemctl --user disable -q --now "$unit" 2>/dev/null || true
  rm -f "$units/$unit" "$STAGE0_HOME/profile.env"
  systemctl --user daemon-reload
  # A merge it recorded after the check and before it stopped.
  if [[ -e $db ]] && left=$(python3 "$KIT/merges.py" missing "$db" "$carried") && [[ -n $left ]]; then
    say "This host's Sortie recorded $(wc -l <<< "$left") merge(s) as it stopped: on p1, run dev/stage0/worker-host.sh carry $(hostname -s) again"
  fi
}

# retire_huly_tunnels UNITS: no worker host polls Huly, so p1 keeps no
# tunnel that gives one Huly, nor the forwards in it to a worker host's
# Sortie and its brokers: the workbench reads p1's Sortie, the only one, so
# the files the tunnels left for the workbench go too. Does nothing once
# they are gone.
retire_huly_tunnels() {
  local units=$1 template=ortus-stage0-huly-tunnel@.service link
  rm -f "$STAGE0_HOME"/tunnels/*.env "$STAGE0_HOME"/sorties/*.env "$STAGE0_HOME"/sorties/*.token
  [[ -e $units/$template ]] || return 0
  for link in "$units"/default.target.wants/ortus-stage0-huly-tunnel@*.service; do
    [[ -e $link || -L $link ]] || continue
    say "Closing ${link##*/}: no worker host polls Huly now"
    systemctl --user disable -q --now "${link##*/}" 2>/dev/null || true
    rm -f "$link"
  done
  rm -f "$units/$template"
  systemctl --user daemon-reload
}
