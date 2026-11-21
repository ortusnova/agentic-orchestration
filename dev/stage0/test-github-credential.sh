#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2329 # ok and fail always succeed; cleanup runs on EXIT
# Checks the job machine's git credential for GitHub (pack/gitconfig-github,
# pack/git-credential-sortie-github): git, through an HTTPS proxy as a
# machine's egress is, sends for https://github.com/<owner>/... the
# placeholder Sortie bound for that owner, SORTIE_GITHUB_TOKEN_<OWNER>, and
# nothing for an owner the job is granted nothing on. A fake proxy takes
# the CONNECT, plays github.com with a test certificate, and records what
# git sent. No network.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
pid=
cleanup() { [[ -n $pid ]] && kill "$pid" 2>/dev/null; rm -rf "$tmp"; }
trap cleanup EXIT
failed=0
ok() { echo "ok: $*"; }
fail() { echo "FAIL: $*"; failed=1; }

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=github.com \
  -addext subjectAltName=DNS:github.com -keyout "$tmp/key.pem" -out "$tmp/cert.pem" 2>/dev/null

cat > "$tmp/proxy.py" <<'EOF'
import json, socket, ssl, sys, threading, base64
d = sys.argv[1]
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(d + "/cert.pem", d + "/key.pem")
ls = socket.socket()
ls.bind(("127.0.0.1", 0))
ls.listen()
open(d + "/port", "w").write(str(ls.getsockname()[1]))
lock = threading.Lock()
def serve(c):
    f = c.makefile("rb")
    if not f.readline().startswith(b"CONNECT github.com:443 "):
        c.close(); return
    while f.readline() not in (b"\r\n", b""):
        pass
    c.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
    t = ctx.wrap_socket(c, server_side=True)
    r = t.makefile("rb")
    path = r.readline().split()[1].decode()
    auth = ""
    while (line := r.readline()) not in (b"\r\n", b""):
        k, _, v = line.decode().partition(":")
        if k.lower() == "authorization":
            auth = v.strip()
    user = base64.b64decode(auth[6:]).decode() if auth.startswith("Basic ") else ""
    with lock, open(d + "/seen", "a") as out:
        out.write(json.dumps({"path": path, "basic": user}) + "\n")
    if user:
        t.sendall(b"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
    else:
        t.sendall(b'HTTP/1.1 401 Unauthorized\r\nWWW-Authenticate: Basic realm="GitHub"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
    t.close()
while True:
    c, _ = ls.accept()
    threading.Thread(target=serve, args=(c,), daemon=True).start()
EOF
python3 "$tmp/proxy.py" "$tmp" & pid=$!
for _ in $(seq 100); do [[ -s $tmp/port ]] && break; sleep 0.05; done
[[ -s $tmp/port ]] || { echo "FAIL: the fake proxy did not start"; exit 1; }

# The machine's git: the kit's gitconfig, the helper on PATH, nothing of
# this host's git settings.
printf '[include]\n\tpath = %s\n' "$KIT/pack/gitconfig-github" > "$tmp/gitconfig"
gitm() {
  env -i PATH="$KIT/pack:/usr/local/bin:/usr/bin:/bin" HOME="$tmp" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$tmp/gitconfig" \
    GIT_TERMINAL_PROMPT=0 SORTIE_GITHUB_TOKEN_ORTUSNOVA=sortie-credential-github-ortusnova \
    SORTIE_GITHUB_TOKEN_BRIAN_CAPPELLO=sortie-credential-github-brian-cappello \
    https_proxy="http://127.0.0.1:$(cat "$tmp/port")" GIT_SSL_CAINFO="$tmp/cert.pem" git "$@"
}
for repo in ortusnova/fleet-vm brian-cappello/personal-site someoneelse/thing; do
  gitm ls-remote "https://github.com/$repo.git" > /dev/null 2>&1 || true
done

sent() { jq -r --arg p "/$1.git/info/refs" 'select(.path | startswith($p)) | .basic' "$tmp/seen" | sort -u | paste -sd' '; }
[[ $(sent ortusnova/fleet-vm) == " x-access-token:sortie-credential-github-ortusnova" ]] \
  && ok "git sends ortusnova's placeholder for ortusnova/fleet-vm, after GitHub's 401" || fail "ortusnova/fleet-vm: $(sent ortusnova/fleet-vm)"
[[ $(sent brian-cappello/personal-site) == " x-access-token:sortie-credential-github-brian-cappello" ]] \
  && ok "git sends brian-cappello's placeholder for brian-cappello/personal-site" || fail "brian-cappello/personal-site: $(sent brian-cappello/personal-site)"
[[ $(sent someoneelse/thing) == "" ]] \
  && ok "git sends no credential for an owner the job is granted nothing on" || fail "someoneelse/thing: $(sent someoneelse/thing)"
grep -q '"/ortusnova/fleet-vm.git/info/refs' "$tmp/seen" && ok "the requests went through the proxy as github.com" || fail "no request: $(cat "$tmp/seen")"

# The packs install the helper and its gitconfig system-wide.
grep -q 'git-credential-sortie-github' "$KIT/pack/install-harness.sh" && grep -q '/etc/sortie-github.gitconfig' "$KIT/pack/install-harness.sh" \
  && ok "install-harness.sh installs the helper and includes gitconfig-github from /etc/gitconfig" || fail "install-harness.sh"
grep -q 'git-credential-sortie-github' "$KIT/pack/agent.Dockerfile" && grep -q 'git-credential-sortie-github' "$KIT/staged-pack.sh" \
  && ok "the stage-0 pack and the staged harness carry the helper" || fail "the packs lack the helper"

exit $failed
