# shellcheck shell=bash
# Sourced by the tests: throwaway environment files (design D8), and the
# stubs deploy.sh needs to find its host. No test depends on dev.yml's hosts.
#
#   testenv_init DIR        DIR/bin first on PATH, with hostname printing
#                           $TESTENV_HOST (default testhost) and getent
#                           resolving every name but those in
#                           $TESTENV_UNRESOLVED; the loader built once into
#                           DIR/envfile; ORTUS_NOVA_ENVIRONMENTS and
#                           ORTUS_NOVA_ENVFILE exported for deploy.sh
#   testenv_file ENV N HOST=RUNS... [KEY: VALUE...]
#                           write DIR/environments/ENV.yml: the hosts (RUNS
#                           is control+worker or worker), ports from
#                           10000+N*100 so two files share none, a local
#                           name ENV.homelab.dev, and each extra top-level
#                           line given

testenv_init() {
  TESTENV_DIR=$1
  mkdir -p "$TESTENV_DIR/bin" "$TESTENV_DIR/environments"
  cp "$KIT/environments/environment.schema.json" "$TESTENV_DIR/environments/"
  # shellcheck disable=SC2016 # expanded by the stubs
  printf '#!/bin/sh\necho "${TESTENV_HOST:-testhost}"\n' > "$TESTENV_DIR/bin/hostname"
  # shellcheck disable=SC2016
  printf '#!/bin/sh\nfor n in $TESTENV_UNRESOLVED; do [ "$n" = "$2" ] && exit 2; done\necho "127.0.0.1 $2"\n' > "$TESTENV_DIR/bin/getent"
  chmod +x "$TESTENV_DIR/bin/hostname" "$TESTENV_DIR/bin/getent"
  [[ -x $TESTENV_DIR/envfile ]] || (cd "$KIT/envfile" && go build -o "$TESTENV_DIR/envfile" .)
  export PATH=$TESTENV_DIR/bin:$PATH ORTUS_NOVA_ENVIRONMENTS=$TESTENV_DIR/environments ORTUS_NOVA_ENVFILE=$TESTENV_DIR/envfile
}

testenv_file() {
  local env=$1 n=$2 base host runs extra=()
  shift 2
  base=$((10000 + n * 100))
  {
    echo "version: 1"
    for arg; do [[ $arg == *=* ]] || extra+=("$arg"); done
    for line in "${extra[@]}"; do echo "$line"; done
    echo "hosts:"
    for arg; do
      [[ $arg == *=* ]] || continue
      host=${arg%%=*} runs=${arg#*=}
      echo "  $host: {runs: [${runs//+/, }]}"
    done
    echo "control: {front_port: $((base + 43))}"
    echo "ports: {sortie: $((base + 80)), workbench: $((base + 93)), nono: $((base + 90)), nono_live_proof: $((base + 91)), egress_gate: $((base + 92)), previews_from: $((base + 1))}"
    echo "tracker: {url: 'http://localhost:$((base + 87))', workspace: $env, project: TST}"
    echo "local_names: {workbench: $env.homelab.dev}"
  } > "$TESTENV_DIR/environments/$env.yml"
}
