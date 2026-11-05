# shellcheck shell=bash
# shellcheck disable=SC2034 # the scripts that source it use these
# The control plane's front on p1 (README.md "Worker hosts"), which
# replaced the stage-1 edge and its SSH tunnels (spec store
# agent-platform/control-plane, design D18): Pomerium, the egress gate
# behind its egress route, and nono. Every worker, p1's own too, reaches
# Sortie only through Pomerium, with a client certificate from the control
# plane's CA; Pomerium names the worker to Sortie from it. The CA, the
# worker list and Pomerium's configuration are control-plane/'s (cpctl), in
# $STAGE0_CP_HOME. Sourced by stage0.sh and worker-host.sh, after
# stage0.env, with KIT and STAGE0_HOME set.
STAGE0_CP_HOME=$STAGE0_HOME/control-plane
# Pomerium's public URL: every worker dials it, and the workflow's flows are
# under it. Its host must resolve to p1 on every worker host.
STAGE0_CP_PUBLIC_URL=${STAGE0_CP_PUBLIC_URL:-https://$(hostname -f):8443}
# nono, the live-proof nono and the egress gate, on p1's loopback.
STAGE0_NONO_PORT=${STAGE0_NONO_PORT:-7690}
STAGE0_NONO_LIVE_PROOF_PORT=${STAGE0_NONO_LIVE_PROOF_PORT:-7691}
STAGE0_EGRESS_GATE_PORT=${STAGE0_EGRESS_GATE_PORT:-7692}
# The file of the live-proof jobs that run, which stage0-live-proofs writes
# and the egress gate reads.
STAGE0_LIVE_PROOFS=$STAGE0_HOME/live-proofs
# Where a worker host keeps what worker-host.sh add copies to it, as its
# own shell sees it (relative to its home).
STAGE0_WORKER_HOST_CONFIG=.config/ortus-nova
# The files of a worker's certificate there, beside the egress CA's
# certificate (egress-ca.crt) and Pomerium's URL (control-plane-url).
STAGE0_WORKER_TLS_FILES=(cp-worker.crt cp-worker.key cp-ca.crt)

# The previews of review: ui stories' pull requests (README.md "Previews of
# ui stories"): a job's machine on any worker opens one at
# http://<identifier>.$(preview_domain)/ through its HTTP proxy, which
# reaches the egress gate through Pomerium's egress route; the gate serves
# it from the preview's port on p1's loopback, by the "NAME PORT" lines of
# STAGE0_PREVIEW_FRONT_ROUTES (preview.sh), and gives a review job's
# requests the review key, which signs the review in on the preview.
STAGE0_PREVIEW_FRONT_ROUTES=$STAGE0_HOME/previews/front-routes
STAGE0_PREVIEW_REVIEW_KEY=$STAGE0_HOME/preview-review-key
# p1's own worker's certificate, which preview-sync checks a preview
# through Pomerium with (stage0.sh names p1's worker p1).
STAGE0_PREVIEW_CHECK_TLS=$STAGE0_CP_HOME/workers/p1

# A worker's name is its identity: a DNS label (control-plane/internal/workers).
FRONT_WORKER_NAME='^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'

# cp_public_port: the port of Pomerium's public URL, where it listens.
cp_public_port() {
  local hostport=${STAGE0_CP_PUBLIC_URL#https://}
  hostport=${hostport%%/*}
  if [[ $hostport == *:* ]]; then echo "${hostport##*:}"; else echo 443; fi
}

# preview_domain: the previews' domain, under the public URL's host
# (https://p1.example.org:8443 gives preview.p1.example.org). No name needs
# to resolve: a job's proxy sends it to Pomerium as a CONNECT.
preview_domain() {
  local host=${STAGE0_CP_PUBLIC_URL#https://}
  host=${host%%/*}
  host=${host%:*}
  echo "preview.${host,,}"
}

# pomerium_image: the Pomerium image the compose project pins by digest
# (control-plane/compose.yaml), so p1 runs the same one.
pomerium_image() {
  sed -n 's/^ *image: *\(docker\.io\/pomerium\/pomerium:[^ @]*@sha256:[0-9a-f]\{64\}\) *$/\1/p' "$KIT/../../control-plane/compose.yaml"
}

# cpctl ARGS...: control-plane/'s cpctl on p1's control-plane directory.
cpctl() {
  "$STAGE0_HOME/bin/cpctl" -home "$STAGE0_CP_HOME" "$@"
}

# front_init: the control plane's CA, Pomerium's certificate for the public
# URL's host, and its configuration, which routes every flow to Sortie and
# the egress gate on p1's loopback and listens on the public URL's port on
# every address. It replaces nothing that exists but Pomerium's certificate
# when the host changes.
front_init() {
  cpctl init -public-url "$STAGE0_CP_PUBLIC_URL" -address ":$(cp_public_port)" \
    -sortie "http://127.0.0.1:$STAGE0_SORTIE_PORT" -egress "http://127.0.0.1:$STAGE0_EGRESS_GATE_PORT" > /dev/null
}

# front_workers: each listed worker's name, one per line.
front_workers() {
  cpctl worker list | tail -n +2 | cut -d' ' -f1
}

# issue_local_worker NAME: p1's own worker's certificate, issued once and
# kept in $STAGE0_CP_HOME/workers/NAME, where its daemon reads it: p1 is
# its host, so its key stays there. Prints nothing; says when it issued.
issue_local_worker() {
  local dir=$STAGE0_CP_HOME/workers/$1
  if [[ -s $dir/worker.crt && -s $dir/worker.key ]] && front_workers | grep -qx "$1"; then
    return 0
  fi
  cpctl worker issue "$1" > /dev/null || return 1
  say "Issued p1's own worker $1 its client certificate ($dir)"
}
