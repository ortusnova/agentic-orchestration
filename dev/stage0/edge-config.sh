# shellcheck shell=bash
# The stage-1 edge's configuration (edge/edge.go), rendered from stage0.env
# and the worker hosts in $STAGE0_HOME/workers (worker-host.sh). Sourced by
# stage0.sh and worker-host.sh, after stage0.env.
STAGE0_WORKERS=$STAGE0_HOME/workers
# The port each tunnel ends on, on its worker host's loopback; the same on
# every host, so one workflow names the flows for all of them.
STAGE0_EDGE_WORKER_PORT=${STAGE0_EDGE_WORKER_PORT:-7700}
# The first edge port on p1; each worker host gets the next free one.
STAGE0_EDGE_FIRST_PORT=${STAGE0_EDGE_FIRST_PORT:-7701}
STAGE0_NONO_PORT=${STAGE0_NONO_PORT:-7690}
# The service of each path flow; Sortie until a flow gets a service of its own.
STAGE0_EDGE_STDIO_URL=${STAGE0_EDGE_STDIO_URL:-http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/flows/stdio}
STAGE0_EDGE_MCP_URL=${STAGE0_EDGE_MCP_URL:-http://127.0.0.1:$STAGE0_SORTIE_PORT/mcp}
STAGE0_EDGE_BUNDLES_URL=${STAGE0_EDGE_BUNDLES_URL:-http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/flows/bundles}
STAGE0_EDGE_USAGE_URL=${STAGE0_EDGE_USAGE_URL:-http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/flows/usage}

# A worker's name is the identity Sortie knows it by: the file name of its
# settings in $STAGE0_WORKERS, which hold WORKER_HOST and EDGE_PORT.
EDGE_WORKER_NAME='^[A-Za-z0-9][A-Za-z0-9._-]*$'

# worker_names: each worker host's name, one per line, sorted.
worker_names() {
  local f
  for f in "$STAGE0_WORKERS"/*.env; do
    [[ -e $f ]] || continue
    f=${f##*/}
    echo "${f%.env}"
  done | sort
}

# worker_setting NAME KEY: one setting of a worker host.
worker_setting() {
  sed -n "s/^$2=//p" "$STAGE0_WORKERS/$1.env" | tail -n 1
}

# render_edge_config FILE: write the edge's configuration to FILE.
render_edge_config() {
  local out=$1 name sep='' workers=''
  for name in $(worker_names); do
    [[ $name =~ $EDGE_WORKER_NAME ]] || { echo "render_edge_config: bad worker name $name" >&2; return 1; }
    workers+="$sep
    {\"name\": \"$name\", \"port\": $(worker_setting "$name" EDGE_PORT)}"
    sep=,
  done
  (umask 077; cat > "$out.tmp" <<EOF
{
  "sortie": "http://127.0.0.1:$STAGE0_SORTIE_PORT",
  "control_token_file": "$STAGE0_HOME/sortie-token",
  "identity_header": "X-Sortie-Worker-Identity",
  "workers": [$workers
  ],
  "flows": {
    "stdio": "$STAGE0_EDGE_STDIO_URL",
    "mcp": "$STAGE0_EDGE_MCP_URL",
    "bundles": "$STAGE0_EDGE_BUNDLES_URL",
    "usage": "$STAGE0_EDGE_USAGE_URL"
  },
  "egress": "127.0.0.1:$STAGE0_NONO_PORT",
  "egress_pass_file": "$STAGE0_HOME/nono-pass"
}
EOF
  )
  mv "$out.tmp" "$out"
}
