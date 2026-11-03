#!/usr/bin/env bash
# Checks the previews of review: ui stories' pull requests on the kit's side
# (preview.sh, run-workbench.sh IDENTIFIER, run-previews.sh) against a
# throwaway repository with the story's branch, with uv, npm, npx, systemctl
# and curl stubbed: a deploy checks out the branch's head, builds it, starts
# its read-only workbench (WORKBENCH_SYNC=0) on its port with a copy of the
# live registry, and routes its name; a new head moves it; a closed pull
# request removes it. Which pull request gets a preview, and when, is
# workbench preview-sync's (workbench/tests/workbench/test_preview.py).
# check NAME CONDITION evaluates CONDITION here, so its expansions are quoted on purpose.
# shellcheck disable=SC2016,SC2034
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null STAGE0_HOME=$tmp/home STAGE0_PREVIEW_WAIT=1
g() { git -c user.name=t -c user.email=t@t -c init.defaultBranch=main "$@"; }
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
check() { if eval "$2"; then ok "$1"; else fail "$1"; fi; }

mkdir -p "$tmp/bin" "$STAGE0_HOME/home"
for tool in uv npx systemctl; do printf '#!/bin/sh\necho "%s $*" >> "%s/calls"\n' "$tool" "$tmp" > "$tmp/bin/$tool"; done
# curl: the preview answers unless $tmp/no-start exists.
printf '#!/bin/sh\n[ ! -e "%s/no-start" ]\n' "$tmp" > "$tmp/bin/curl"
# npm ci installs node_modules where it runs.
printf '#!/bin/sh\necho "npm $*" >> "%s/calls"\n[ "$1" != ci ] || mkdir -p node_modules\n' "$tmp" > "$tmp/bin/npm"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH

# The kit's repository on GitHub, with the ui story's branch.
g init -q --bare "$tmp/remote"
g clone -q "$tmp/remote" "$tmp/up" 2> /dev/null
mkdir -p "$tmp/up/workbench/frontend"
echo '{}' > "$tmp/up/workbench/frontend/package-lock.json"
g -C "$tmp/up" add -A && g -C "$tmp/up" commit -qm base && g -C "$tmp/up" push -q origin HEAD:main
g -C "$tmp/up" checkout -qb sortie/ONS-97
echo one > "$tmp/up/workbench/view.txt"
g -C "$tmp/up" add -A && g -C "$tmp/up" commit -qm one && g -C "$tmp/up" push -q origin sortie/ONS-97
sha1=$(g -C "$tmp/up" rev-parse HEAD)
g clone -q "$tmp/remote" "$tmp/kit" 2> /dev/null

cat > "$STAGE0_HOME/stage0.env" <<EOF
STAGE0_KIT_ROOT=$tmp/kit
STAGE0_STORE=$tmp/store
STAGE0_HULY_PROJECT=ONS
STAGE0_SORTIE_PORT=7680
STAGE0_WORKBENCH_PORT=8793
STAGE0_AO_REPO_PROJECT=ortusnova/agentic-orchestration
STAGE0_WORKBENCH_URL=https://workbench.example.net
STAGE0_PREVIEW_ROUTES_HOOK='echo hook >> $tmp/calls'
EOF
for f in sortie-token workbench-password-hash workbench-secret github-token; do echo "$f-value" > "$STAGE0_HOME/$f"; done
if command -v python3 > /dev/null; then
  python3 -c 'import sqlite3, sys; c = sqlite3.connect(sys.argv[1]); c.execute("create table approvals (k)"); c.execute("insert into approvals values (1)"); c.commit()' "$STAGE0_HOME/home/workbench.db"
else
  echo live > "$STAGE0_HOME/home/workbench.db"
fi

P=$STAGE0_HOME/previews/ONS-97
ADDRESS=https://workbench-ons-97.example.net/

# Scenario: A UI story from spec to merge: its reviewed, green head is deployed.
: > "$tmp/calls"
if "$KIT/preview.sh" up ONS-97 sortie/ONS-97 "$sha1" 8800 "$ADDRESS" 2> "$tmp/err"; then ok "up deploys"; else fail "up: $(cat "$tmp/err")"; fi
check "the checkout is the branch's head" '[[ $(git -C "$P/src" rev-parse HEAD) == "$sha1" && -e $P/src/workbench/view.txt ]]'
check "it builds the branch's workbench" 'grep -qx "uv sync -q" "$tmp/calls" && grep -qx "npm ci --silent" "$tmp/calls" && grep -qx "npx vite build --logLevel warn" "$tmp/calls"'
check "it enables and restarts its unit" 'grep -qx "systemctl --user enable -q ortus-stage0-preview@ONS-97.service" "$tmp/calls" && grep -qx "systemctl --user restart ortus-stage0-preview@ONS-97.service" "$tmp/calls"'
check "it has a copy of the live registry" '[[ -s $P/home/workbench.db ]] && ! [[ $P/home/workbench.db -ef $STAGE0_HOME/home/workbench.db ]]'
check "its name is routed to its port" '[[ $(cat "$STAGE0_HOME/previews/routes") == "workbench-ons-97.example.net 8800" ]] && grep -qx hook "$tmp/calls"'

# The unit's workbench: the branch's, read-only, on its own port and home, against the live store and Sortie.
mkdir -p "$P/src/workbench/.venv/bin"
printf '#!/bin/sh\necho "sync=$WORKBENCH_SYNC home=$WORKBENCH_HOME store=$WORKBENCH_STORE sortie=$WORKBENCH_SORTIE_URL previews=${WORKBENCH_PREVIEWS:-none} $*"\n' > "$P/src/workbench/.venv/bin/workbench"
chmod +x "$P/src/workbench/.venv/bin/workbench"
check "the preview unit runs run-workbench.sh for its identifier" 'grep -qx "ExecStart=@KIT@/run-workbench.sh %i" "$KIT/units/ortus-stage0-preview@.service"'
out=$("$KIT/run-workbench.sh" ONS-97)
check "the preview is read-only, on its port" '[[ $out == "sync=0 home=$P/home store=$tmp/store sortie=http://127.0.0.1:7680 previews=none serve --host 127.0.0.1 --port 8800" ]]'
mkdir -p "$tmp/kit/workbench/.venv/bin"
cp "$P/src/workbench/.venv/bin/workbench" "$tmp/kit/workbench/.venv/bin/workbench"
out=$("$KIT/run-workbench.sh")
check "the live workbench shows the previews and syncs" '[[ $out == "sync= home=$STAGE0_HOME/home store=$tmp/store sortie=http://127.0.0.1:7680 previews=$STAGE0_HOME/previews.json serve --host 127.0.0.1 --port 8793" ]]'

# Scenario: The operator asks for a change: the fix's head moves the preview.
echo two > "$tmp/up/workbench/view.txt"
g -C "$tmp/up" commit -qam two && g -C "$tmp/up" push -q origin sortie/ONS-97
sha2=$(g -C "$tmp/up" rev-parse HEAD)
: > "$tmp/calls"
if "$KIT/preview.sh" up ONS-97 sortie/ONS-97 "$sha2" 8800 "$ADDRESS" 2> "$tmp/err"; then ok "up follows the branch"; else fail "up again: $(cat "$tmp/err")"; fi
check "the checkout moved to the new head" '[[ $(git -C "$P/src" rev-parse HEAD) == "$sha2" && $(cat "$P/src/workbench/view.txt") == two ]]'
check "an unchanged lock file needs no npm ci" '! grep -q "npm ci" "$tmp/calls" && grep -qx "systemctl --user restart ortus-stage0-preview@ONS-97.service" "$tmp/calls"'

# Refusals: nothing that is not an identifier, a commit, or a free port.
for args in "up ../x sortie/ONS-97 $sha2 8801 $ADDRESS" "up ONS-98 sortie/ONS-97 HEAD 8801 $ADDRESS" \
  "up ONS-98 sortie/ONS-97 $sha2 8793 $ADDRESS" "up ONS-98 sortie/ONS-97 $sha2 8801" "down"; do
  # shellcheck disable=SC2086 # split on purpose
  if "$KIT/preview.sh" $args 2> /dev/null; then fail "preview.sh $args was accepted"; else ok "refused: preview.sh ${args%% *} …"; fi
done
check "a refusal deploys nothing" '[[ ! -e $STAGE0_HOME/previews/ONS-98 ]]'
touch "$tmp/no-start"
if "$KIT/preview.sh" up ONS-99 sortie/ONS-97 "$sha2" 8801 https://workbench-ons-99.example.net/ 2> /dev/null; then
  fail "a preview that does not start counts as deployed"
else
  ok "a preview that does not start fails the deploy"
fi
rm "$tmp/no-start"
"$KIT/preview.sh" down ONS-99 2> /dev/null

# Scenario: The operator is satisfied: the closed pull request takes its preview down.
: > "$tmp/calls"
if "$KIT/preview.sh" down ONS-97 2> "$tmp/err"; then ok "down"; else fail "down: $(cat "$tmp/err")"; fi
check "its unit is stopped and disabled" 'grep -qx "systemctl --user disable -q --now ortus-stage0-preview@ONS-97.service" "$tmp/calls"'
check "its checkout and home are gone" '[[ ! -e $P ]] && ! git -C "$tmp/kit" worktree list | grep -q ONS-97'
check "its ref is gone" '! git -C "$tmp/kit" rev-parse -q --verify refs/previews/ONS-97 > /dev/null'
check "its route is gone" '[[ ! -s $STAGE0_HOME/previews/routes ]] && grep -qx hook "$tmp/calls"'
if "$KIT/preview.sh" down ONS-97 2> /dev/null; then ok "down twice is fine"; else fail "down twice"; fi

# run-previews.sh: one pass of workbench preview-sync, with the kit's settings.
printf '#!/bin/sh\necho "sortie=$WORKBENCH_SORTIE_URL project=$WORKBENCH_HULY_PROJECT token=$WORKBENCH_SORTIE_TOKEN github=$WORKBENCH_GITHUB_TOKEN $*"\n' > "$tmp/kit/workbench/.venv/bin/workbench"
out=$("$KIT/run-previews.sh")
want="sortie=http://127.0.0.1:7680 project=ONS token=sortie-token-value github=github-token-value preview-sync --repo ortusnova/agentic-orchestration --record $STAGE0_HOME/previews.json --deploy $KIT/preview.sh --live-url https://workbench.example.net --first-port 8800"
check "run-previews.sh runs preview-sync with the kit's settings" '[[ $out == "$want" ]]'
check "the previews' timer runs run-previews.sh" 'grep -qx "ExecStart=@KIT@/run-previews.sh" "$KIT/units/ortus-stage0-previews.service" && grep -q ortus-stage0-previews.timer "$KIT/stage0.sh"'

exit "$failed"
