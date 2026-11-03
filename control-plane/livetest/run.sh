#!/usr/bin/env bash
# Start the whole compose project under rootless podman with test
# certificates and fake credentials, run every scenario of
# agent-platform/control-plane against it (livetest/*_test.go), and take it
# down. CI runs this; so can an operator's VM with podman and podman-compose.
# The images must be built first: make -C control-plane images.
#
#   control-plane/livetest/run.sh            start, test, stop
#   CP_LIVE_KEEP=1 control-plane/livetest/run.sh   leave the project running
set -euo pipefail

LIVE=$(cd "$(dirname "$0")" && pwd)
CP=$(dirname "$LIVE")
die() { printf 'livetest: %s\n' "$*" >&2; exit 1; }
say() { printf 'livetest: %s\n' "$*" >&2; }

export CP_TAG=${CP_TAG:-$(git -C "$CP" rev-parse HEAD)}
export CP_LIVE_DIR=${CP_LIVE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/cp-live.XXXXXX")}
export CP_HOME=$CP_LIVE_DIR/home
export CP_SORTIE_CONFIG_DIR=$CP_LIVE_DIR/sortie
export CP_NONO_DIR=$CP_LIVE_DIR/nono
export CP_COMPOSE_FILES=$LIVE/compose.test.yaml
export CP_PUBLIC_BIND=127.0.0.1 CP_PUBLIC_PORT=${CP_PUBLIC_PORT:-8443}
export CPCTL=$CP_LIVE_DIR/bin/cpctl

mkdir -p "$CP_LIVE_DIR/bin" "$CP_LIVE_DIR/model" "$CP_SORTIE_CONFIG_DIR" "$CP_NONO_DIR"
(cd "$CP" && go build -o "$CP_LIVE_DIR/bin/" ./cmd/cpctl ./livetest/fakemodel)
podman build -q -f "$LIVE/fakemodel.Dockerfile" -t "localhost/ortus-nova/cp-fakemodel:$CP_TAG" "$CP/.." > /dev/null

# The control plane's CA, Pomerium's certificate for cp.test, the generated
# secrets, and fake values for what an operator provides.
"$CPCTL" -home "$CP_HOME" init -public-url "https://cp.test:$CP_PUBLIC_PORT" -name localhost > /dev/null
fake() { printf '%s-%s\n' "$1" "$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')" > "$CP_HOME/secrets/$2"; chmod 600 "$CP_HOME/secrets/$2"; }
fake ghp_livetest cp-github-token
fake sk-ant-livetest cp-anthropic-token
fake huly-livetest cp-huly-token
printf 'live-test-password\n' | podman run --rm -i "localhost/ortus-nova/workbench:$CP_TAG" workbench hash-password \
  | tail -n 1 > "$CP_HOME/secrets/cp-workbench-password-hash"
chmod 600 "$CP_HOME/secrets/cp-workbench-password-hash"
for w in p1 taichi; do "$CPCTL" -home "$CP_HOME" worker issue "$w" > /dev/null; done

# Sortie's workflow and tracker, written by Sortie's user in its container.
cp "$LIVE/sortie/WORKFLOW.md" "$LIVE/sortie/issues.json" "$CP_SORTIE_CONFIG_DIR/"
sed -i "s/cp\.test:8443/cp.test:$CP_PUBLIC_PORT/" "$CP_SORTIE_CONFIG_DIR/WORKFLOW.md"
chmod 0777 "$CP_SORTIE_CONFIG_DIR" && chmod 0666 "$CP_SORTIE_CONFIG_DIR"/*

# nono's test profile, and the fake model API it binds the credential to.
"$CP_LIVE_DIR/bin/fakemodel" -issue "$CP_LIVE_DIR/model" -name model.cp.test
cp "$LIVE/nono-profile.json" "$CP_NONO_DIR/egress-profile.json"
cp "$CP_LIVE_DIR/model/ca.crt" "$CP_NONO_DIR/model-ca.crt"
chmod -R a+rX "$CP_LIVE_DIR/model" "$CP_NONO_DIR"

dump() {
  local c
  for c in $(podman ps -a --format '{{.Names}}' | grep '^cp-' || true); do
    echo "===== podman logs $c" >&2
    podman logs --tail 200 "$c" >&2 2>&1 || true
  done
}
stop() {
  if [[ ${CP_LIVE_KEEP:-} == 1 ]]; then
    say "the project keeps running: CP_HOME=$CP_HOME CP_TAG=$CP_TAG CP_LIVE_DIR=$CP_LIVE_DIR"
    return
  fi
  "$CP/cp.sh" down > /dev/null 2>&1 || true
}
trap stop EXIT

if ! "$CP/cp.sh" up; then
  dump
  die "the project did not come up"
fi

say "running the scenarios"
if ! (cd "$CP" && CP_LIVE=1 go test -count=1 -timeout 30m -v ./livetest/); then
  dump
  die "a scenario failed"
fi
