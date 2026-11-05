#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks the token usage of a run, which the worker daemon reads after it
# with the pack's agent-usage (usage_commands, worker-config.sh): the
# opencode plugin pack/sortie-usage.js against a fake plugin context, then
# agent-usage, and, where opencode is installed, a real agent-acp run of
# opencode against a fake model provider, with a shell command and a
# subagent, whose totals must be the provider's. Without the usage file
# agent-usage fails, so the daemon reports the run unmeasured, never a zero.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
pids=()
# shellcheck disable=SC2329 # the EXIT trap runs it
cleanup() {
  local p
  for p in "${pids[@]}"; do kill "$p" 2> /dev/null || true; done
  rm -rf "$tmp"
}
trap cleanup EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

# The plugin, as an ES module wherever node looks for one.
cp "$KIT/pack/sortie-usage.js" "$tmp/sortie-usage.mjs"
node "$KIT/usage-test/plugin-unit.mjs" "$tmp/sortie-usage.mjs" "$tmp/unit" || failed=1

# agent-usage prints what the daemon reads, and fails without a file.
mkdir -p "$tmp/bin" "$tmp/home/.local/state/sortie"
printf '#!/bin/sh\necho opencode\n' > "$tmp/bin/agent-acp"
chmod +x "$tmp/bin/agent-acp"
usage=$tmp/home/.local/state/sortie/usage.json
au() { HOME=$tmp/home PATH=$tmp/bin:$PATH sh "$KIT/pack/agent-usage"; }
echo '{"input_tokens":12000,"output_tokens":3000,"cache_read_tokens":400,"cache_write_tokens":50,"model":"claude-opus-5-5","sessions":2,"updated_at":"x"}' > "$usage"
[[ $(au) == '{"input_tokens":12000,"output_tokens":3000,"cache_read_tokens":400,"cache_write_tokens":50,"model":"claude-opus-5-5"}' ]] \
  && ok "agent-usage prints the run's totals as the daemon reads them" || fail "agent-usage: $(au 2>&1)"
echo '{"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"model":""}' > "$usage"
[[ $(au) == '{"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0}' ]] \
  && ok "a run without model requests prints zero and no model" || fail "zero: $(au 2>&1)"
rm "$usage"
if au > "$tmp/out" 2> "$tmp/err"; then fail "agent-usage printed $(cat "$tmp/out") without a usage file"
else grep -q "no usage file" "$tmp/err" && [[ ! -s $tmp/out ]] && ok "without a usage file agent-usage fails, so the run is unmeasured" || fail "no file: $(cat "$tmp/err")"; fi
for bad in 'not json' '{"input_tokens":1}' '{"input_tokens":-1,"output_tokens":1,"cache_read_tokens":0,"cache_write_tokens":0}' '{"input_tokens":"1","output_tokens":1,"cache_read_tokens":0,"cache_write_tokens":0}'; do
  echo "$bad" > "$usage"
  if au > /dev/null 2>&1; then fail "agent-usage took $bad"; else ok "agent-usage refuses $bad"; fi
done
rm -f "$usage"

# A real run: agent-acp starts opencode acp against the fake provider.
if ! command -v opencode > /dev/null; then
  echo "skip: no opencode here, so no real run (p1 and the workers have it)"
  exit $failed
fi
fake=$tmp/fake
mkdir -p "$fake" "$tmp/home/.config/opencode/plugins" "$tmp/ws"
export FAKE_PORT=$fake/port FAKE_LOG=$fake/served
node "$KIT/usage-test/fake-provider.mjs" &
pids+=($!)
for _ in $(seq 50); do [[ -s $FAKE_PORT ]] && break; sleep 0.1; done
[[ -s $FAKE_PORT ]] || { fail "the fake provider did not start"; exit 1; }
cat > "$tmp/home/.config/opencode/opencode.json" << EOF
{
  "model": "fake/fake-model",
  "update": "disable",
  "providers": {
    "fake": {
      "package": "@opencode/ai/providers/openai-compatible",
      "settings": {"baseURL": "http://127.0.0.1:$(cat "$FAKE_PORT")/v1", "apiKey": "fake"},
      "models": {"fake-model": {"name": "Fake", "capabilities": {"tools": true}}}
    }
  },
  "permissions": [{"action": "*", "resource": "*", "effect": "allow"}]
}
EOF
cp "$KIT/pack/sortie-usage.js" "$tmp/home/.config/opencode/plugins/"
cp "$KIT/pack/agent-acp" "$KIT/pack/agent-usage" "$tmp/bin/"
chmod +x "$tmp/bin/agent-acp" "$tmp/bin/agent-usage"
run_env=(env -i "PATH=$tmp/bin:$PATH" "HOME=$tmp/home" "XDG_CONFIG_HOME=$tmp/home/.config"
  "XDG_DATA_HOME=$tmp/data" "XDG_STATE_HOME=$tmp/state" "XDG_CACHE_HOME=$tmp/cache" SORTIE_RULE=build)
# An earlier run's file, which agent-acp must drop.
echo '{"input_tokens":5,"output_tokens":5,"cache_read_tokens":0,"cache_write_tokens":0}' > "$usage"
(cd "$tmp/ws" && "${run_env[@]}" CLAUDE_CODE_OAUTH_TOKEN=placeholder-value \
  node "$KIT/usage-test/acp-client.mjs" agent-acp > "$tmp/acp.out" 2> "$tmp/acp.err") \
  && grep -q '^stop: end_turn$' "$tmp/acp.out" \
  && ok "agent-acp's opencode runs the prompt over ACP, its stdout only ACP" || fail "acp: $(cat "$tmp/acp.out" "$tmp/acp.err" | tail -20)"
grep -q '^main shell$' "$FAKE_LOG" && grep -q '^main subagent$' "$FAKE_LOG" && grep -q '^child text$' "$FAKE_LOG" \
  && ok "the run ran a shell command and a subagent" || fail "served: $(cat "$FAKE_LOG")"
[[ -f $tmp/ws/env.txt ]] && ! grep -q '^SORTIE_USAGE_FILE=' "$tmp/ws/env.txt" \
  && ok "the agent's shell commands do not get SORTIE_USAGE_FILE" || fail "shell env: $(grep SORTIE "$tmp/ws/env.txt" 2>&1)"
out=$(cd "$tmp/ws" && "${run_env[@]}" agent-usage 2> "$tmp/err") || fail "agent-usage after the run: $(cat "$tmp/err")"
served=$(wc -l < "$FAKE_LOG")
# Each served request: 1000 prompt tokens, 200 of them cached, 70 completion.
want="{\"input_tokens\":$((served * 800)),\"output_tokens\":$((served * 70)),\"cache_read_tokens\":$((served * 200)),\"cache_write_tokens\":0,\"model\":\"fake-model\"}"
[[ $out == "$want" ]] && ok "agent-usage prints what the provider served: $served requests, the subagent's and the titles' too" \
  || fail "usage $out, want $want ($(tr '\n' ',' < "$FAKE_LOG")); log: $(cat "$usage.log" 2>/dev/null)"
exit $failed
