#!/usr/bin/env bash
# Stage 0 of agent-platform-workers (spec store, design D13) on one host: a
# persistent Sortie under plan control with co-located smolvm, the plan
# workbench, acp-broker, and the local Huly, as systemd user units. GitHub is
# the forge. Rootless throughout: no sudo, no system unit.
#
#   dev/stage0/stage0.sh                  set up or update, then (re)start the units
#   dev/stage0/stage0.sh --rebuild-pack   also rebuild the smolvm pack
#   dev/stage0/stage0.sh --update-sortie  also move the Sortie checkout to origin/plan-bench
#
# Every step checks first, so a second run changes nothing but a restart.
# Settings live in $STAGE0_HOME/stage0.env, written on the first run (edit it,
# then run this again). State, tokens and workspaces live in $STAGE0_HOME.
#
# Needs: smolvm 1.32.4 or later, podman, Go, uv, npm, the openspec CLI, gh signed in,
# a live Claude subscription login (claude setup-token, opencode or Claude Code), Huly running
# (HULY_COMPOSE_DIR) and the host's opencode signed in to Anthropic for
# planning sessions.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
KIT=$ROOT/dev/stage0
export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
REBUILD_PACK='' UPDATE_SORTIE=''
for arg; do
  case $arg in
    --rebuild-pack) REBUILD_PACK=1 ;;
    --update-sortie) UPDATE_SORTIE=1 ;;
    *) echo "usage: $0 [--rebuild-pack] [--update-sortie]" >&2; exit 2 ;;
  esac
done
say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'stage0.sh: %s\n' "$*" >&2; exit 1; }
secret() { [[ -s $1 ]] || { (umask 077; openssl rand -hex 24 > "$1"); }; }

mkdir -p "$STAGE0_HOME"/{bin,sortie,workspaces,home,sessions}
chmod 700 "$STAGE0_HOME"
ENV_FILE=$STAGE0_HOME/stage0.env
if [[ ! -s $ENV_FILE ]]; then
  say "Writing the default settings to $ENV_FILE"
  (umask 077; cat > "$ENV_FILE" <<EOF
# Stage-0 settings (dev/stage0/stage0.sh). Run stage0.sh again after an edit.
STAGE0_KIT_ROOT=$ROOT
STAGE0_SORTIE_SRC=$HOME/dev/sortie-worktrees/stage0
STAGE0_STORE=$HOME/dev/ortus-nova-specs
STAGE0_SORTIE_PORT=7680
STAGE0_WORKBENCH_PORT=8793
STAGE0_HULY_URL=http://localhost:8087
STAGE0_HULY_WORKSPACE=sortie
STAGE0_HULY_EMAIL=sortie@example.com
STAGE0_HULY_PASSWORD=sortie-dev-pass
STAGE0_HULY_PROJECT=ONS
STAGE0_REPO_URL=https://github.com/ortusnova/sortie.git
STAGE0_REPO_BASE=plan-bench
STAGE0_REPO_PROJECT=ortusnova/sortie
STAGE0_PACK=$STAGE0_HOME/stage0.smolmachine
# Agents at once; each VM takes 4 vCPU and 8 GiB (WORKFLOW.md).
STAGE0_MAX_AGENTS=3
EOF
  )
fi
# shellcheck source=/dev/null
. "$ENV_FILE"
STAGE0_MAX_AGENTS=${STAGE0_MAX_AGENTS:-3}
[[ $STAGE0_KIT_ROOT == "$ROOT" ]] || say "Note: $ENV_FILE runs the kit from $STAGE0_KIT_ROOT, not $ROOT"
export STAGE0_HOME STAGE0_HULY_URL STAGE0_HULY_WORKSPACE STAGE0_HULY_PROJECT STAGE0_SORTIE_PORT STAGE0_MAX_AGENTS \
  STAGE0_PACK STAGE0_REPO_URL STAGE0_REPO_BASE STAGE0_REPO_PROJECT

for cmd in smolvm podman go uv npm openspec gh git openssl python3; do
  command -v "$cmd" >/dev/null || die "$cmd is not on PATH"
done
# shellcheck source=smolvm-version.sh
. "$KIT/smolvm-version.sh"
# shellcheck source=huly-restart-policy.sh
. "$KIT/huly-restart-policy.sh"
smolvm_version_ok > /dev/null || die "Sortie needs smolvm $SMOLVM_MIN or later; found: $(smolvm --version 2>&1 | head -1)"
gh auth token >/dev/null || die "gh has no token: run gh auth login"
STAGE0_HOME=$STAGE0_HOME "$KIT/anthropic-token.py" >/dev/null \
  || die "no live Claude subscription token: run claude setup-token into $STAGE0_HOME/claude-oauth-token"

if [[ ! -d $STAGE0_SORTIE_SRC ]]; then
  say "Creating the Sortie checkout $STAGE0_SORTIE_SRC (ortusnova/sortie plan-bench)"
  git clone -q --branch plan-bench https://github.com/ortusnova/sortie.git "$STAGE0_SORTIE_SRC"
fi
if [[ -n $UPDATE_SORTIE ]]; then
  say "Moving $STAGE0_SORTIE_SRC to origin/plan-bench"
  git -C "$STAGE0_SORTIE_SRC" fetch -q origin plan-bench
  git -C "$STAGE0_SORTIE_SRC" checkout -q --detach origin/plan-bench
fi
say "Building sortie $(git -C "$STAGE0_SORTIE_SRC" rev-parse --short HEAD), acp-broker and smol-builder into $STAGE0_HOME/bin"
(cd "$STAGE0_SORTIE_SRC" && GOWORK=off go build -o "$STAGE0_HOME/bin/sortie" ./cmd/sortie)
(cd "$ROOT/acp-broker" && go build -o "$STAGE0_HOME/bin/acp-broker" ./cmd/acp-broker)
(cd "$ROOT/smol-builder" && go build -o "$STAGE0_HOME/bin/smol-builder" ./cmd/smol-builder)
export PATH=$STAGE0_HOME/bin:$PATH

say "Installing workbench dependencies and building the frontend"
(cd "$ROOT/workbench" && uv sync -q)
(cd "$ROOT/workbench/frontend" && { [[ -d node_modules ]] || npm install --silent; } && npx vite build --logLevel warn)

HULY_DIR=${HULY_COMPOSE_DIR:-$HOME/dev/huly-selfhost}
if ! curl -sf -o /dev/null "$STAGE0_HULY_URL/config.json"; then
  [[ -d $HULY_DIR ]] || die "Huly is not running and HULY_COMPOSE_DIR is not set"
  say "Starting Huly"
  (cd "$HULY_DIR" && podman compose up -d >/dev/null)
  for _ in $(seq 60); do curl -sf -o /dev/null "$STAGE0_HULY_URL/config.json" && break; sleep 2; done
fi
# Every Huly container must restart unless stopped, and podman honors that
# after a reboot only through this user unit. huly-selfhost leaves redpanda
# without a policy (huly-restart-policy.sh).
for name in $(huly_restart_policies "$HULY_DIR"); do say "Huly container $name now restarts unless stopped"; done
systemctl --user is-enabled -q podman-restart.service || { say "Enabling podman-restart.service"; systemctl --user enable -q podman-restart.service; }
say "Preparing Huly project $STAGE0_HULY_PROJECT"
python3 "$ROOT/dev/huly_project.py" "$STAGE0_HULY_URL" "$STAGE0_HULY_WORKSPACE" "$STAGE0_HULY_EMAIL" \
  "$STAGE0_HULY_PASSWORD" "$STAGE0_HULY_PROJECT" >/dev/null || die "could not prepare Huly project $STAGE0_HULY_PROJECT"

secret "$STAGE0_HOME/sortie-token"
secret "$STAGE0_HOME/viewer-token"
secret "$STAGE0_HOME/workbench-secret"
if [[ ! -s $STAGE0_HOME/workbench-password-hash ]]; then
  secret "$STAGE0_HOME/workbench-password"
  (cd "$ROOT/workbench" && umask 077 && uv run python -c 'import sys
from workbench.auth import hash_password
print(hash_password(open(sys.argv[1]).read().strip()))' "$STAGE0_HOME/workbench-password" > "$STAGE0_HOME/workbench-password-hash")
fi

if [[ -n $REBUILD_PACK || ! -s $STAGE0_PACK ]]; then
  say "Building the pack $STAGE0_PACK"
  smol-builder build --repo "$KIT/pack" --output "$STAGE0_PACK" > "$STAGE0_HOME/pack-report.json"
fi

say "Rendering Sortie's workflow"
python3 "$ROOT/dev/forge/render.py" "$KIT/WORKFLOW.md" "$STAGE0_HOME/sortie/WORKFLOW.md"
"$KIT/run-sortie.sh" validate >/dev/null || die "Sortie refused the workflow: $KIT/run-sortie.sh validate"

say "Installing the user units"
UNITS=$HOME/.config/systemd/user
mkdir -p "$UNITS"
for unit in ortus-stage0-sortie.service ortus-stage0-workbench.service ortus-stage0-token.service ortus-stage0-token.timer; do
  sed -e "s|@KIT@|$KIT|g" -e "s|@STAGE0_HOME@|$STAGE0_HOME|g" "$KIT/units/$unit" > "$UNITS/$unit"
done
systemctl --user daemon-reload
systemctl --user enable -q ortus-stage0-sortie.service ortus-stage0-workbench.service
systemctl --user enable -q --now ortus-stage0-token.timer
systemctl --user restart ortus-stage0-sortie.service
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_SORTIE_PORT/readyz" && break; sleep 1; done
systemctl --user restart ortus-stage0-workbench.service
for _ in $(seq 60); do curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_WORKBENCH_PORT/api/auth/mode" && break; sleep 1; done
curl -sf -o /dev/null "http://127.0.0.1:$STAGE0_WORKBENCH_PORT/api/auth/mode" || die "the workbench did not start: journalctl --user -u ortus-stage0-workbench"
say "Ready: workbench http://127.0.0.1:$STAGE0_WORKBENCH_PORT (password in $STAGE0_HOME/workbench-password), Sortie 127.0.0.1:$STAGE0_SORTIE_PORT, Huly $STAGE0_HULY_URL/workbench/$STAGE0_HULY_WORKSPACE/tracker"
