#!/usr/bin/env bash
# Checks the operator's channel (notify-channel.sh, spec store
# agent-platform/run-supervision): the entry each kind renders into the
# workflow, the credential file only the operator writes, what
# run-sortie.sh and run-workbench.sh hand on, and that with no channel, the
# default, both start. The workbench and Sortie are stubs; test-review-gate.sh loads the same entries with Sortie's
# own loader and delivers a question through them.
# shellcheck disable=SC2015,SC2016 # each check is "test && ok || fail"; the stubs' $ expand when they run
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$KIT/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
# shellcheck source=notify-channel.sh
. "$KIT/notify-channel.sh"

# The entry of each kind.
want_events="events: $NOTIFY_EVENTS"
for kind in slack discord; do
  entry=$(notify_entry "$kind")
  if [[ $entry == *"kind: $kind"* && $entry == *'webhook_url: $SORTIE_NOTIFY_SECRET'* && $entry == *"$want_events"* ]]; then
    ok "$kind posts to the webhook in the credential"
  else fail "$kind entry: $entry"; fi
done
entry=$(notify_entry webhook)
[[ $entry == *'url: $SORTIE_NOTIFY_SECRET'* && $entry == *"$want_events"* ]] && ok "webhook posts to the URL in the credential" || fail "webhook entry: $entry"
entry=$(notify_entry telegram -1001234567890)
[[ $entry == *'bot_token: $SORTIE_NOTIFY_SECRET'* && $entry == *'chat_id: "-1001234567890"'* ]] && ok "telegram names the bot token and the chat" || fail "telegram entry: $entry"
[[ $NOTIFY_EVENTS == *session.stopped* && $NOTIFY_EVENTS == *session.escalated* ]] && ok "a park and an escalation reach the channel" || fail "events: $NOTIFY_EVENTS"
[[ -z $(notify_entry '') ]] && ok "no kind, no channel" || fail "an entry with no kind"
notify_entry telegram 2>/dev/null && fail "telegram without a chat was accepted" || ok "telegram needs a chat"
notify_entry 'telegram' 'x; rm' 2>/dev/null && fail "a chat that is not an id was accepted" || ok "a chat is an id or @channel"
notify_entry email 2>/dev/null && fail "kind email was accepted" || ok "only Sortie's kinds"

# The credential: present, the operator's, mode 0600.
cred=$tmp/notify-credential
notify_credential "$cred" 2>/dev/null && fail "a missing credential was accepted" || ok "a missing credential is refused"
printf 'https://hooks.slack.test/x\n' > "$cred"
chmod 644 "$cred"
notify_credential "$cred" 2>/dev/null && fail "a 0644 credential was accepted" || ok "a credential others can read is refused"
chmod 620 "$cred"
notify_credential "$cred" 2>/dev/null && fail "a group-writable credential was accepted" || ok "a credential others can write is refused"
chmod 600 "$cred"
notify_credential "$cred" && ok "a 0600 credential of the operator's is taken" || fail "a 0600 credential was refused"

# The workflow, rendered as stage0.sh renders it, carries the entry.
render() { # render WORKFLOW OUT ENTRY
  local vars
  # shellcheck disable=SC2016 # literal ${VAR} patterns, not expansions
  vars=$(grep -o '\${[A-Z0-9_]*}' "$1" | sort -u | tr -d '${}' | grep -vx STAGE0_NOTIFY_ENTRY)
  # shellcheck disable=SC2046 # one assignment per variable
  env $(for v in $vars; do printf '%s=x ' "$v"; done) STAGE0_KIT="$KIT" STAGE0_NOTIFY_ENTRY="$3" \
    python3 "$ROOT/dev/forge/render.py" "$1" "$2"
}
wf=WORKFLOW.md
render "$KIT/$wf" "$tmp/$wf" "$(notify_entry slack)"
section=$(sed -n '/^notifications:/,/^$/p' "$tmp/$wf")
if [[ $section == *"kind: tracker_comment"* && $section == *"$(notify_entry slack)"* ]]; then
  ok "$wf sends each question to the channel"
else fail "$wf notifications: $section"; fi
render "$KIT/$wf" "$tmp/$wf" ""
[[ $(sed -n '/^notifications:/,/^$/p' "$tmp/$wf" | grep -c 'kind:') == 1 ]] && ok "$wf with no channel keeps the comments only" \
  || fail "$wf with no channel: $(sed -n '/^notifications:/,/^$/p' "$tmp/$wf")"

# run-sortie.sh and run-workbench.sh: a stub kit, Sortie and workbench that
# print what they were handed. Both scripts put $STAGE0_HOME/bin first on PATH.
home=$tmp/home
mkdir -p "$home/sortie" "$home/sorties" "$home/bin" "$tmp/bin" "$tmp/kit/dev/stage0" "$tmp/kit/workbench/.venv/bin"
cp "$KIT"/{run-sortie.sh,run-workbench.sh,notify-channel.sh} "$tmp/kit/dev/stage0/"
printf '#!/bin/sh\necho token\n' > "$tmp/kit/dev/huly_project.py"
printf '#!/bin/sh\necho claude\n' > "$tmp/kit/dev/stage0/anthropic-token.py"
printf '#!/bin/sh\nenv | grep -E "^(SORTIE_NOTIFY_SECRET|WORKBENCH_)" | sort\n' > "$tmp/kit/workbench/.venv/bin/workbench"
printf '#!/bin/sh\nenv | grep -E "^SORTIE_NOTIFY_SECRET=" | sort\n' > "$home/bin/sortie"
printf '#!/bin/sh\necho p1\n' > "$home/bin/hostname"
chmod +x "$tmp/kit/dev/huly_project.py" "$tmp/kit/dev/stage0/anthropic-token.py" "$tmp/kit/workbench/.venv/bin/workbench" "$home/bin/"*
for f in github-token sortie-token workbench-password-hash workbench-secret; do echo "$f-value" > "$home/$f"; done
cat > "$home/stage0.env" <<EOF
STAGE0_KIT_ROOT=$tmp/kit
STAGE0_HULY_URL=x STAGE0_HULY_WORKSPACE=x STAGE0_HULY_EMAIL=x STAGE0_HULY_PASSWORD=x STAGE0_HULY_PROJECT=ONS
STAGE0_SORTIE_PORT=7680 STAGE0_WORKBENCH_PORT=8793 STAGE0_STORE=$tmp/store
STAGE0_NOTIFY_KIND=slack
STAGE0_WORKBENCH_PUBLIC_URL=https://workbench.homelab.dev
EOF
cp "$cred" "$home/notify-credential"
echo "taichi-token" > "$home/sorties/taichi.token"
echo "SORTIE_URL=http://127.0.0.1:7681" > "$home/sorties/taichi.env"
run() { STAGE0_HOME=$home PATH=$tmp/bin:$PATH HOME=$tmp "$@" 2>&1 || true; }
# huly_project.py runs under python3; the stub kit's runs as a script.
sed -i 's|python3 "$STAGE0_KIT_ROOT/dev/huly_project.py"|"$STAGE0_KIT_ROOT/dev/huly_project.py"|' "$tmp/kit/dev/stage0/run-sortie.sh"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ $out == "SORTIE_NOTIFY_SECRET=https://hooks.slack.test/x" ]] && ok "Sortie gets the credential from the file" || fail "run-sortie.sh: $out"
chmod 644 "$home/notify-credential"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ $out == *"must be mode 0600"* && $out != *SORTIE_NOTIFY_SECRET=* ]] && ok "Sortie refuses a credential others can read" || fail "run-sortie.sh with 0644: $out"
chmod 600 "$home/notify-credential"
out=$(run "$tmp/kit/dev/stage0/run-workbench.sh")
for want in WORKBENCH_NOTIFY_KIND=slack "WORKBENCH_NOTIFY_CREDENTIAL_FILE=$home/notify-credential" \
  WORKBENCH_PUBLIC_URL=https://workbench.homelab.dev WORKBENCH_SORTIE_NAME=p1 \
  WORKBENCH_SORTIES=taichi=http://127.0.0.1:7681 "WORKBENCH_SORTIE_TOKEN_TAICHI_FILE=$home/sorties/taichi.token"; do
  grep -qxF "$want" <<< "$out" && ok "the workbench gets $want" || fail "run-workbench.sh lacks $want: $out"
done
grep -q 'hooks.slack.test' <<< "$out" && fail "the credential is in the workbench's environment" || ok "the credential stays in its file"
# No channel, the default: both start, with no credential file and nothing about a channel.
sed -i '/^STAGE0_NOTIFY_KIND=/d' "$home/stage0.env"
mv "$home/notify-credential" "$home/notify-credential.off"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ -z $out ]] && ok "with no channel Sortie starts, with no credential" || fail "run-sortie.sh with no channel: $out"
out=$(run "$tmp/kit/dev/stage0/run-workbench.sh")
[[ $out == *WORKBENCH_SORTIES=taichi=* && $out != *WORKBENCH_NOTIFY_* ]] && ok "with no channel the workbench starts, with no channel" \
  || fail "run-workbench.sh with no channel: $out"
echo STAGE0_NOTIFY_KIND=slack >> "$home/stage0.env"
mv "$home/notify-credential.off" "$home/notify-credential"

exit $failed
