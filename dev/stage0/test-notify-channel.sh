#!/usr/bin/env bash
# Checks where Sortie's notifications go (notify-channel.sh, spec store
# agent-platform/run-supervision): the workbench, by default, with this
# host's token; then the operator's channel: the entry each kind renders into
# the workflow, the credential file only the operator writes, what
# run-sortie.sh and run-workbench.sh hand on, and that with no channel of the
# operator's, the default, both start. The workbench and Sortie are stubs; test-review-gate.sh loads the same entries with Sortie's
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
[[ $(grep -c 'kind:' <<< "$section") == 3 ]] && ok "$wf's operator channel is one more entry" || fail "$wf with slack: $section"
render "$KIT/$wf" "$tmp/$wf" ""
section=$(sed -n '/^notifications:/,/^$/p' "$tmp/$wf")
[[ $(grep -c 'kind:' <<< "$section") == 2 ]] && ok "$wf with no channel keeps the comments and the workbench" \
  || fail "$wf with no channel: $section"
# The workbench is the default destination, by Sortie's webhook backend, for
# every event that needs a person.
workbench=$(grep -A2 -- '- kind: webhook' <<< "$section")
[[ $workbench == *'url: $SORTIE_WORKBENCH_NOTIFY_URL'* && $workbench == *"events: $WORKBENCH_EVENTS"* ]] \
  && ok "$wf sends the workbench its notifications" || fail "$wf's workbench entry: $workbench"
for e in session.stopped session.failed session.escalated budget.held escalation.ci_failure escalation.review_comments \
  escalation.bot_review escalation.merge_conflicts escalation.auto_merge escalation.merge_completion; do
  [[ $WORKBENCH_EVENTS == *"$e"[],]* ]] || fail "the workbench does not get $e: $WORKBENCH_EVENTS"
done
ok "the workbench gets session.stopped, session.failed, session.escalated, every escalation and budget.held"

# The host's token: minted by stage0.sh, mode 0600, in the URL Sortie posts to.
STAGE0_SORTIE_NAME=p1
mkdir -p "$tmp/tokens-home/notify-tokens"
workbench_notify_url "$tmp/tokens-home" 8793 2>/dev/null && fail "a missing token was accepted" || ok "Sortie refuses a missing token"
printf 'host-token-0123\n' > "$tmp/tokens-home/notify-tokens/p1.token"
chmod 644 "$tmp/tokens-home/notify-tokens/p1.token"
workbench_notify_url "$tmp/tokens-home" 8793 2>/dev/null && fail "a token others can read was accepted" || ok "Sortie refuses a token others can read"
chmod 600 "$tmp/tokens-home/notify-tokens/p1.token"
url=$(workbench_notify_url "$tmp/tokens-home" 8793)
[[ $url == http://127.0.0.1:8793/api/notifications/p1/host-token-0123 ]] && ok "Sortie posts to the workbench with the host's token" || fail "workbench url: $url"
STAGE0_SORTIE_NAME='P1;x' notify_host 2>/dev/null && fail "a host name that is not a name was accepted" || ok "a host name is lower-case letters, digits and dashes"
unset STAGE0_SORTIE_NAME
grep -q 'notify_token "$STAGE0_HOME" "$notify_host"' "$KIT/stage0.sh" && ok "stage0.sh mints the host's token" || fail "stage0.sh mints no token"
notify_token "$tmp/mint" p1 && t1=$(cat "$tmp/mint/notify-tokens/p1.token") && notify_token "$tmp/mint" p1
[[ ${#t1} == 48 && $(cat "$tmp/mint/notify-tokens/p1.token") == "$t1" && $(stat -c %a "$tmp/mint/notify-tokens/p1.token") == 600 ]] \
  && ok "a host's token is minted once, mode 0600" || fail "minted: $(ls -l "$tmp/mint/notify-tokens")"
notify_token "$tmp/mint" 'Bad;name' 2>/dev/null && fail "a token for a host that is not a name was minted" || ok "a token is minted for a host name only"
# A Sortie on a worker host reaches p1's workbench through the tunnel.
url=$(STAGE0_SORTIE_NAME=p1 workbench_notify_url "$tmp/tokens-home" "$STAGE0_WORKBENCH_TUNNEL_PORT")
[[ $url == http://127.0.0.1:7790/api/notifications/p1/host-token-0123 ]] && ok "a worker host posts through the tunnel's port" || fail "tunnel url: $url"

# run-sortie.sh and run-workbench.sh: a stub kit, Sortie and workbench that
# print what they were handed. Both scripts put $STAGE0_HOME/bin first on PATH.
home=$tmp/home
mkdir -p "$home/sortie" "$home/sorties" "$home/bin" "$tmp/bin" "$tmp/kit/dev/stage0" "$tmp/kit/workbench/.venv/bin"
cp "$KIT"/{run-sortie.sh,run-workbench.sh,notify-channel.sh} "$tmp/kit/dev/stage0/"
printf '#!/bin/sh\necho token\n' > "$tmp/kit/dev/huly_project.py"
printf '#!/bin/sh\necho claude\n' > "$tmp/kit/dev/stage0/anthropic-token.py"
printf '#!/bin/sh\nenv | grep -E "^(SORTIE_NOTIFY_SECRET|WORKBENCH_)" | sort\n' > "$tmp/kit/workbench/.venv/bin/workbench"
printf '#!/bin/sh\nenv | grep -E "^SORTIE_(NOTIFY_SECRET|WORKBENCH_NOTIFY_URL)=" | sort\n' > "$home/bin/sortie"
printf '#!/bin/sh\necho p1\n' > "$home/bin/hostname"
chmod +x "$tmp/kit/dev/huly_project.py" "$tmp/kit/dev/stage0/anthropic-token.py" "$tmp/kit/workbench/.venv/bin/workbench" "$home/bin/"*
for f in github-token sortie-token supervisor-token workbench-password-hash workbench-secret; do echo "$f-value" > "$home/$f"; done
cat > "$home/stage0.env" <<EOF
STAGE0_KIT_ROOT=$tmp/kit
STAGE0_HULY_URL=x STAGE0_HULY_WORKSPACE=x STAGE0_HULY_EMAIL=x STAGE0_HULY_PASSWORD=x STAGE0_HULY_PROJECT=ONS
STAGE0_SORTIE_PORT=7680 STAGE0_WORKBENCH_PORT=8793 STAGE0_STORE=$tmp/store
STAGE0_NOTIFY_KIND=slack
STAGE0_WORKBENCH_PUBLIC_URL=https://workbench.homelab.dev
EOF
cp "$cred" "$home/notify-credential"
mkdir -p "$home/notify-tokens"
(umask 077; echo "p1-notify-token" > "$home/notify-tokens/p1.token")
wb_url=SORTIE_WORKBENCH_NOTIFY_URL=http://127.0.0.1:8793/api/notifications/p1/p1-notify-token
echo "taichi-token" > "$home/sorties/taichi.token"
echo "SORTIE_URL=http://127.0.0.1:7681" > "$home/sorties/taichi.env"
run() { STAGE0_HOME=$home PATH=$tmp/bin:$PATH HOME=$tmp "$@" 2>&1 || true; }
# huly_project.py runs under python3; the stub kit's runs as a script.
sed -i 's|python3 "$STAGE0_KIT_ROOT/dev/huly_project.py"|"$STAGE0_KIT_ROOT/dev/huly_project.py"|' "$tmp/kit/dev/stage0/run-sortie.sh"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ $out == "SORTIE_NOTIFY_SECRET=https://hooks.slack.test/x"$'\n'"$wb_url" ]] && ok "Sortie gets the credential from the file, and the workbench's URL" || fail "run-sortie.sh: $out"
chmod 644 "$home/notify-credential"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ $out == *"must be mode 0600"* && $out != *SORTIE_NOTIFY_SECRET=* ]] && ok "Sortie refuses a credential others can read" || fail "run-sortie.sh with 0644: $out"
chmod 600 "$home/notify-credential"
out=$(run "$tmp/kit/dev/stage0/run-workbench.sh")
for want in WORKBENCH_NOTIFY_KIND=slack "WORKBENCH_NOTIFY_CREDENTIAL_FILE=$home/notify-credential" \
  WORKBENCH_PUBLIC_URL=https://workbench.homelab.dev WORKBENCH_SORTIE_NAME=p1 "WORKBENCH_NOTIFY_TOKENS=$home/notify-tokens" \
  WORKBENCH_SORTIES=taichi=http://127.0.0.1:7681 "WORKBENCH_SORTIE_TOKEN_TAICHI_FILE=$home/sorties/taichi.token"; do
  grep -qxF "$want" <<< "$out" && ok "the workbench gets $want" || fail "run-workbench.sh lacks $want: $out"
done
grep -q 'hooks.slack.test' <<< "$out" && fail "the credential is in the workbench's environment" || ok "the credential stays in its file"
# No channel, the default: both start, with no credential file and nothing about a channel.
sed -i '/^STAGE0_NOTIFY_KIND=/d' "$home/stage0.env"
mv "$home/notify-credential" "$home/notify-credential.off"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ $out == "$wb_url" ]] && ok "with no channel Sortie starts, with no credential, and posts to the workbench" || fail "run-sortie.sh with no channel: $out"
mv "$home/notify-tokens/p1.token" "$home/p1.token.off"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ $out == *"Sortie has no token for the workbench's notifications"* && $out != *SORTIE_WORKBENCH_NOTIFY_URL=* ]] \
  && ok "Sortie does not start without the host's token" || fail "run-sortie.sh with no token: $out"
mv "$home/p1.token.off" "$home/notify-tokens/p1.token"
# A Sortie on a worker host posts to p1's workbench through the tunnel.
echo STAGE0_PROFILE=worker >> "$home/stage0.env"
out=$(run "$tmp/kit/dev/stage0/run-sortie.sh")
[[ $out == "SORTIE_WORKBENCH_NOTIFY_URL=http://127.0.0.1:7790/api/notifications/p1/p1-notify-token" ]] \
  && ok "a worker host's Sortie posts through the tunnel" || fail "run-sortie.sh on a worker host: $out"
sed -i '/^STAGE0_PROFILE=/d' "$home/stage0.env"
out=$(run "$tmp/kit/dev/stage0/run-workbench.sh")
[[ $out == *WORKBENCH_SORTIES=taichi=* && $out != *WORKBENCH_NOTIFY_KIND* && $out != *WORKBENCH_NOTIFY_CREDENTIAL* ]] && ok "with no channel the workbench starts, with no channel" \
  || fail "run-workbench.sh with no channel: $out"
echo STAGE0_NOTIFY_KIND=slack >> "$home/stage0.env"
mv "$home/notify-credential.off" "$home/notify-credential"

exit $failed
