#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2329 # ok and fail always succeed; cleanup runs on EXIT
# Checks check-forge-rules.py against a fake GitHub API: a bounded default
# branch passes; a missing rule, the App as a bypass actor, a ruleset on
# another branch, and GitHub Free's refusal each fail, naming the
# repository; it reads only the repositories a ceiling grants write on,
# and sends only GET requests. No network.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
pid=
cleanup() { [[ -n $pid ]] && kill "$pid" 2>/dev/null; rm -rf "$tmp"; }
trap cleanup EXIT
failed=0
ok() { echo "ok: $*"; }
fail() { echo "FAIL: $*"; failed=1; }

cat > "$tmp/WORKFLOW.md" <<'EOF'
---
environment:
  remote:
    forge_ceiling:
      a:
        - repository: o/good
          permissions: {contents: write}
        - repository: o/readonly
          permissions: {contents: read}
      b:
        - repository: o/norules
          permissions: {contents: write}
        - repository: o/bypass
          permissions: {pull_requests: write}
        - repository: o/otherbranch
          permissions: {contents: write}
        - repository: o/free
          permissions: {contents: write}
---
EOF
cat > "$tmp/api.py" <<'EOF'
import http.server, json, sys
d = sys.argv[1]
full = [{"type": "pull_request"}, {"type": "non_fast_forward"}, {"type": "deletion"}]
on = {"ref_name": {"include": ["~DEFAULT_BRANCH"], "exclude": []}}
sets = {
    "o/good": {"rules": full, "conditions": on, "bypass_actors": []},
    "o/norules": {"rules": [{"type": "deletion"}], "conditions": on, "bypass_actors": []},
    "o/bypass": {"rules": full, "conditions": on, "bypass_actors": [{"actor_type": "Integration", "actor_id": 5857256}]},
    "o/otherbranch": {"rules": full, "conditions": {"ref_name": {"include": ["refs/heads/dev"], "exclude": []}}, "bypass_actors": []},
}
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def answer(self, code, body):
        b = json.dumps(body).encode()
        self.send_response(code); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        with open(d + "/requests", "a") as f:
            f.write("GET " + self.path + " " + self.headers.get("Authorization", "") + "\n")
        parts = self.path.split("?")[0].strip("/").split("/")
        repo = parts[1] + "/" + parts[2]
        if len(parts) == 3:
            return self.answer(200, {"default_branch": "main", "private": repo == "o/free"})
        if repo == "o/free":
            return self.answer(403, {"message": "Upgrade to GitHub Pro or make this repository public to enable this feature."})
        if len(parts) == 4:
            return self.answer(200, [{"id": 1}] if repo in sets else [])
        return self.answer(200, {"id": 1, "target": "branch", "enforcement": "active", **sets[repo]})
    def do_POST(self):
        with open(d + "/requests", "a") as f: f.write("POST " + self.path + "\n")
        self.answer(405, {})
    do_PUT = do_PATCH = do_DELETE = do_POST
s = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
open(d + "/port", "w").write(str(s.server_address[1]))
s.serve_forever()
EOF
python3 "$tmp/api.py" "$tmp" & pid=$!
for _ in $(seq 100); do [[ -s $tmp/port ]] && break; sleep 0.05; done

rc=0
out=$(FORGE_RULES_API="http://127.0.0.1:$(cat "$tmp/port")" FORGE_RULES_TOKEN=read-only-token \
  "$KIT/check-forge-rules.py" "$tmp/WORKFLOW.md") || rc=$?
[[ $rc == 1 ]] && ok "it fails when a repository is not bounded" || fail "exit $rc"
grep -qx 'ok: o/good: .*' <<< "$out" && ok "a bounded default branch passes" || fail "o/good: $out"
grep -q '^FAIL: o/norules: main: no active ruleset that require a pull request$' <<< "$out" \
  && grep -q '^FAIL: o/norules: main: no active ruleset that block force pushes$' <<< "$out" \
  && ok "each missing rule is named" || fail "o/norules: $out"
grep -q '^FAIL: o/bypass: main: a ruleset lists the App (ID 5857256) as a bypass actor$' <<< "$out" && ok "the App as a bypass actor fails" || fail "o/bypass: $out"
grep -q '^FAIL: o/otherbranch: main: no active ruleset that require a pull request$' <<< "$out" && ok "a ruleset on another branch does not count" || fail "o/otherbranch: $out"
grep -q '^FAIL: o/free: GitHub refuses rulesets on this private repository on its plan (HTTP 403: Upgrade to GitHub Pro' <<< "$out" \
  && ok "GitHub Free's refusal fails, naming the repository" || fail "o/free: $out"
! grep -q 'o/readonly' "$tmp/requests" && ok "a repository granted only read is not read" || fail "it read o/readonly"
! grep -q '^POST' "$tmp/requests" && ! grep -qv 'Bearer read-only-token$' "$tmp/requests" \
  && ok "it sends only GET requests, with the read-only token" || fail "requests: $(cat "$tmp/requests")"
grep -q '^check-forge-rules: ## ' "$KIT/Makefile" && ok "make check-forge-rules runs it" || fail "no make target"
exit $failed
