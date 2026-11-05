#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016 # ok and fail always succeed; the patterns are literal
# The telemetry store's configuration, without podman (make test): every
# image pinned by digest, every secret a podman secret, only the collector
# and Langfuse's web publishing a port and only on 127.0.0.1, every key of
# the environment file mapped to its chart value in README.md, the
# collector's token, session, mask and export settings, the mask's patterns
# against token shapes and ordinary text, telemetry.sh init, and the
# retention against a fake Langfuse. test-live.sh runs the project itself.
set -euo pipefail

TM=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
C=$TM/compose.yaml
O=$TM/otelcol.yaml
E=$TM/telemetry.env.example

COMPOSE_FILE=$C "$TM/../pin-images.sh" --check > "$tmp/pins" && ok "every image is pinned by digest" || fail "$(grep UNPINNED "$tmp/pins")"

# The services that publish a port, and the ports, from compose.yaml.
python3 - "$C" > "$tmp/ports" << 'EOF'
import re, sys
service = None
in_ports = False
for line in open(sys.argv[1]):
    m = re.match(r"^  ([a-z][a-z0-9-]*):\s*$", line)
    if m:
        service, in_ports = m.group(1), False
        continue
    if re.match(r"^    ports:\s*$", line):
        in_ports = True
        continue
    if in_ports:
        m = re.match(r'^      - "?([^"]+)"?\s*$', line)
        if m:
            print(service, m.group(1))
            continue
        in_ports = False
EOF
[[ $(awk '{print $1}' "$tmp/ports" | sort -u | tr '\n' ' ') == "collector langfuse-web " ]] \
  && ok "only the collector and Langfuse's web publish a port" || fail "ports: $(cat "$tmp/ports")"
[[ $(wc -l < "$tmp/ports") == 3 ]] && ! awk '{print $2}' "$tmp/ports" | grep -v '^127\.0\.0\.1:' \
  && ok "each on 127.0.0.1" || fail "ports: $(cat "$tmp/ports")"

# Every secret is external, a podman secret from a file telemetry.sh knows.
files=$(sed -n '/^SECRET_FILES=(/,/)/p' "$TM/telemetry.sh" | tr -d '()' | sed 's/SECRET_FILES=//' | tr ' ' '\n' | grep . | sort)
declared=$(sed -n 's/^  ortus-telemetry-\([a-z0-9-]*\): {external: true}$/\1/p' "$C" | sort)
[[ -n $files && $files == "$declared" ]] && ok "each secret is a podman secret made from a file of \$TELEMETRY_HOME/secrets" \
  || fail "secrets: telemetry.sh has $(echo "$files" | tr '\n' ' '), compose.yaml $(echo "$declared" | tr '\n' ' ')"
used=$(grep -o 'ortus-telemetry-[a-z0-9-]*' "$C" | grep -v -e '-internal$' -e '-outside$' -e "^ortus-telemetry-$" | sort -u | sed 's/^ortus-telemetry-//')
missing=$(comm -23 <(echo "$used" | grep -v -E '^(postgres|clickhouse|redis|minio|langfuse-worker|langfuse-web|collector)$') <(echo "$declared"))
[[ -z $missing ]] && ok "every secret a service uses is declared" || fail "undeclared: $missing"
if grep -nE '(PASSWORD|SECRET|_KEY|TOKEN|AUTH)[A-Z_]*: *[^$ {]' "$C" | grep -v -E 'LANGFUSE_INIT_PROJECT_PUBLIC_KEY|_ACCESS_KEY_ID|REDIS_AUTH_USERNAME|_FILE: /run/secrets/'; then
  fail "compose.yaml holds a credential as a value"
else
  ok "compose.yaml holds no credential"
fi

# The one environment file: each key in README.md's map, each variable of
# compose.yaml a key of it (TELEMETRY_FILE_STORE is telemetry.sh's).
keys=$(sed -n 's/^\([A-Z][A-Z0-9_]*\)=.*/\1/p' "$E")
unmapped=''
for k in $keys; do grep -q "^| \`$k\` |" "$TM/README.md" || unmapped+=" $k"; done
[[ -z $unmapped ]] && ok "README.md maps each key of telemetry.env to its chart value" || fail "not in README.md:$unmapped"
unknown=''
for v in $(grep -o '\${[A-Z][A-Z0-9_]*}' "$C" | tr -d '${}' | sort -u); do
  [[ $v == TELEMETRY_FILE_STORE ]] || grep -qx "$v" <<< "$keys" || unknown+=" $v"
done
[[ -z $unknown ]] && ok "every setting compose.yaml reads is a key of telemetry.env" || fail "not in telemetry.env.example:$unknown"
grep -q '^TELEMETRY_RETENTION_DAYS=90$' "$E" && ok "the retention is 90 days by default" || fail "TELEMETRY_RETENTION_DAYS"
grep -q '^LANGFUSE_FEATURES_SIGN_UP_DISABLED=true$' "$E" && grep -q 'AUTH_DISABLE_SIGNUP: ${LANGFUSE_FEATURES_SIGN_UP_DISABLED}' "$C" \
  && ok "sign-up is off" || fail "sign-up"
for k in LANGFUSE_INIT_ORG_ID LANGFUSE_INIT_PROJECT_ID LANGFUSE_INIT_PROJECT_PUBLIC_KEY LANGFUSE_INIT_USER_EMAIL; do
  grep -q "$k: \${$k}" "$C" || fail "Langfuse's web has no $k"
done
grep -q 'target: LANGFUSE_INIT_PROJECT_SECRET_KEY' "$C" && grep -q 'target: LANGFUSE_INIT_USER_PASSWORD' "$C" \
  && ok "the headless initialisation has its organisation, project, key pair and user" || fail "the headless initialisation"

# The collector.
[[ $(grep -c 'authenticator: bearertokenauth/ingest' "$O") == 2 ]] && grep -q 'filename: /run/secrets/ortus-telemetry-collector-ingest-token' "$O" \
  && ok "OTLP over gRPC and HTTP takes only the ingest token" || fail "the receivers' auth"
grep -q 'endpoint: http://langfuse-web:3000/api/public/otel$' "$O" && grep -q 'x-langfuse-ingestion-version: "${env:LANGFUSE_INGESTION_VERSION}"' "$O" \
  && grep -q '^COLLECTOR_CONFIG_EXPORTERS_OTLP_HTTP_LANGFUSE_HEADERS_X_LANGFUSE_INGESTION_VERSION=4$' "$E" \
  && ok "traces go to Langfuse's /api/public/otel with x-langfuse-ingestion-version 4, which Langfuse v4 needs" || fail "the Langfuse exporter"
grep -q 'set(span.attributes\["langfuse.session.id"\], Concat(\[span.attributes\["sortie.dispatch.id"\], span.attributes\["sortie.attempt"\]\], "/"))' "$O" \
  && grep -q 'set(span.attributes\["langfuse.trace.metadata.issue"\], span.attributes\["sortie.issue.identifier"\])' "$O" \
  && ok "the session id is <sortie.dispatch.id>/<sortie.attempt> and the issue is trace metadata" || fail "the session statements"
grep -A3 '^    traces:' "$O" | grep -q 'processors: \[memory_limiter, transform/session, transform/mask-traces, batch\]' \
  && grep -A3 '^    logs:' "$O" | grep -q 'exporters: \[file/logs\]' && grep -A3 '^    metrics:' "$O" | grep -q 'exporters: \[file/metrics\]' \
  && ok "traces are masked on their way to Langfuse; log records and metrics go to the file store" || fail "the pipelines"

# The mask: the same three patterns in every statement, and what they do.
python3 - "$O" > "$tmp/patterns" << 'EOF'
import re, sys
text = open(sys.argv[1]).read()
for m in re.finditer(r'replace_(?:all_)?patterns?\((?:[^,]+), (?:"value", )?"((?:[^"\\]|\\.)*)", "((?:[^"\\]|\\.)*)"\)', text):
    print(m.group(1) + "\t" + m.group(2))
EOF
[[ $(sort -u "$tmp/patterns" | wc -l) == 3 && $(wc -l < "$tmp/patterns") -ge 30 ]] \
  && ok "every mask statement uses one of the same three patterns ($(wc -l < "$tmp/patterns") statements)" || fail "patterns: $(sort -u "$tmp/patterns")"
python3 - "$tmp/patterns" << 'EOF' && ok "the mask redacts each well-known token shape and leaves ordinary text" || fail "the mask"
import re, secrets, base64, sys

rules = []
for line in open(sys.argv[1]):
    pat, rep = line.rstrip("\n").split("\t")
    # From the OTTL string and the collector's $$ escape to RE2 and Python's re.
    pat = pat.replace("\\\\", "\\").replace("$$", "$")
    rep = rep.replace("$${", "\\g<").replace("}", ">").replace("$$1", "\\1")
    rules.append((re.compile(pat), rep))
rules = list(dict.fromkeys(rules))

def mask(s):
    for rx, rep in rules:
        s = rx.sub(rep, s)
    return s

job = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode().rstrip("=")
tokens = [
    "ghp_" + "a1B2" * 9, "github_pat_" + "x" * 30, "ghs_" + "Z9" * 18,
    "sk-ant-api03-" + "q" * 40, "sk-proj-" + "w" * 30, "sk-lf-1b2c3d4e-0000-1111-2222-333344445555",
    "glpat-" + "e" * 20, "xoxb-1234567890-abcdef", "AKIAABCDEFGHIJKLMNOP", "AIza" + "c" * 35,
    "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
    "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\n-----END OPENSSH PRIVATE KEY-----",
    job,
]
bad = []
for t in tokens:
    out = mask(f"output: {t} done")
    if t in out or out != "output: [redacted] done":
        bad.append(f"{t!r} -> {out!r}")
for header, want in [("Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345", "Authorization: Bearer [redacted]"),
                     ("authorization: basic dXNlcjpwYXNzd29yZHBhc3N3b3Jk", "authorization: basic [redacted]")]:
    if mask(header) != want:
        bad.append(f"{header!r} -> {mask(header)!r}")
plain = [
    "the task-runner-for-the-release finished in sk-short",
    "commit 3f2a9c1d4b5e6f708192a3b4c5d6e7f8091a2b3c on main",
    "uuid 0b6a3d1e-6c1f-4f39-9e4b-2a4f5d9e8c7a and sha256:" + "ab" * 32,
    "ONS-155 sortie.dispatch.id=d-1/2 bearer of good news",
]
for p in plain:
    if mask(p) != p:
        bad.append(f"ordinary text changed: {p!r} -> {mask(p)!r}")
if bad:
    print("\n".join(bad), file=sys.stderr)
    sys.exit(1)
EOF

# telemetry.sh init: the environment file and every secret, once.
if [[ $(id -u) == 0 ]]; then
  echo "skip: telemetry.sh init refuses root"
else
  export STAGE0_HOME=$tmp/stage0
  "$TM/telemetry.sh" init 2> /dev/null
  H=$STAGE0_HOME/telemetry
  grep -q '^LANGFUSE_INIT_PROJECT_PUBLIC_KEY=pk-lf-[0-9a-f-]\{36\}$' "$H/telemetry.env" && grep -q '^sk-lf-[0-9a-f-]\{36\}$' "$H/secrets/langfuse-init-project-secret-key" \
    && grep -q '^[0-9a-f]\{64\}$' "$H/secrets/langfuse-encryption-key" && ok "init makes the project's key pair and the 256-bit encryption key" || fail "init: key pair"
  [[ $(find "$H/secrets" -type f | wc -l) == "$(echo "$files" | wc -l)" && -z $(find "$H/secrets" "$H/telemetry.env" -type f -perm /077) ]] \
    && [[ $(stat -c %a "$H/secrets") == 700 ]] && ok "init writes each secret file, for this user alone" || fail "init: $(ls -l "$H/secrets")"
  sum=$(cat "$H/telemetry.env" "$H"/secrets/* | sha256sum)
  sed -i '/^TELEMETRY_BACKUP_KEEP=/d' "$H/telemetry.env"
  "$TM/telemetry.sh" init 2> /dev/null
  [[ $(cat "$H/telemetry.env" "$H"/secrets/* | sha256sum) == "$sum" ]] \
    && ok "init again replaces nothing, and gives a missing setting its default" || fail "init again changed a setting or a secret"
  unset STAGE0_HOME
fi

# The retention, against a fake Langfuse: every page, the cutoff, 1,000
# trace ids per deletion.
python3 - "$TM/retention.py" << 'EOF' && ok "the retention reads every page older than the cutoff and deletes those traces, 1,000 at a time" || fail "the retention"
import base64, datetime, http.server, json, os, subprocess, sys, tempfile, threading, urllib.parse

pages = {None: ([f"t{i}" for i in range(900)], "c1"), "c1": ([f"t{i}" for i in range(850, 1500)], None)}
seen = {"get": [], "delete": [], "auth": set()}

class Fake(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass
    def reply(self, body):
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def do_GET(self):
        u = urllib.parse.urlparse(self.path)
        q = dict(urllib.parse.parse_qsl(u.query))
        seen["get"].append((u.path, q))
        seen["auth"].add(self.headers.get("Authorization"))
        ids, cursor = pages[q.get("cursor")]
        self.reply({"data": [{"traceId": t, "id": "o" + t} for t in ids], "meta": {"cursor": cursor} if cursor else {}})
    def do_DELETE(self):
        n = int(self.headers.get("Content-Length", 0))
        seen["delete"].append((self.path, json.loads(self.rfile.read(n))))
        self.reply({"message": "Traces deleted"})

srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fake)
threading.Thread(target=srv.serve_forever, daemon=True).start()
with tempfile.NamedTemporaryFile("w", delete=False) as f:
    f.write("sk-lf-secret\n")
env = dict(os.environ, LANGFUSE_HOST=f"http://127.0.0.1:{srv.server_port}", LANGFUSE_PUBLIC_KEY="pk-lf-public", LANGFUSE_SECRET_KEY_FILE=f.name)
subprocess.run([sys.executable, sys.argv[1], "90"], env=env, check=True, stdout=subprocess.DEVNULL)
os.unlink(f.name)
want_cut = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=90)
assert [g[0] for g in seen["get"]] == ["/api/public/v2/observations"] * 2, seen["get"]
cut = datetime.datetime.strptime(seen["get"][0][1]["toStartTime"], "%Y-%m-%dT%H:%M:%S.000Z").replace(tzinfo=datetime.timezone.utc)
assert abs((cut - want_cut).total_seconds()) < 60, cut
assert seen["get"][1][1]["cursor"] == "c1"
assert seen["auth"] == {"Basic " + base64.b64encode(b"pk-lf-public:sk-lf-secret").decode()}, seen["auth"]
deleted = [d[1]["traceIds"] for d in seen["delete"]]
assert all(d[0] == "/api/public/traces" for d in seen["delete"])
assert [len(d) for d in deleted] == [1000, 500], [len(d) for d in deleted]
assert sorted(sum(deleted, [])) == sorted(f"t{i}" for i in range(1500))
EOF

exit $failed
