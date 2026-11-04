#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks the control plane's front on p1 (front-config.sh), which replaced
# the stage-1 edge (design D18), with podman and cp-egress stubbed: Pomerium
# runs the image the compose project pins, on the host's network, with its
# certificate and key as podman secrets; the egress gate listens on
# loopback, introspects with Sortie's control token, and sends a running
# live proof's egress to the live-proof nono; every flow of the workflow is
# under Pomerium's public URL; and the edge is gone from the kit. The route
# a review job takes to a ui story's preview is checked in the configuration
# cpctl renders (only a listed worker's certificate) and through the real
# egress gate, with stand-ins for Sortie and the preview (only a live job
# token, every refusal in the gate's journal).
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

mkdir -p "$tmp/bin" "$STAGE0_HOME/bin" "$STAGE0_HOME/control-plane/secrets"
# shellcheck disable=SC2016 # expanded by the stubs, not here
printf '#!/bin/sh\necho "podman $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/podman"
# shellcheck disable=SC2016
printf '#!/bin/sh\necho "cp-egress $*" >> "%s/calls"\n' "$tmp" > "$STAGE0_HOME/bin/cp-egress"
chmod +x "$tmp/bin/podman" "$STAGE0_HOME/bin/cp-egress"
export PATH=$tmp/bin:$PATH
printf 'STAGE0_SORTIE_PORT=7680\nSTAGE0_CP_PUBLIC_URL=https://p1.example.org:8443\n' > "$STAGE0_HOME/stage0.env"

# shellcheck source=front-config.sh
(. "$STAGE0_HOME/stage0.env"; . "$KIT/front-config.sh"; pomerium_image) > "$tmp/image"
pinned=$(grep -o 'docker.io/pomerium/pomerium:[^ ]*' "$KIT/../../control-plane/compose.yaml")
[[ $(cat "$tmp/image") == "$pinned" && $pinned == *@sha256:* ]] && ok "p1 runs the Pomerium image the compose project pins by digest" || fail "image: $(cat "$tmp/image") vs $pinned"
# shellcheck source=front-config.sh
[[ $(. "$KIT/front-config.sh"; STAGE0_CP_PUBLIC_URL=https://cp.example.org; cp_public_port) == 443 ]] \
  && [[ $(. "$KIT/front-config.sh"; STAGE0_CP_PUBLIC_URL=https://cp.example.org:9443; cp_public_port) == 9443 ]] \
  && ok "Pomerium listens on the public URL's port" || fail "cp_public_port"

"$KIT/run-pomerium.sh" > "$tmp/out" 2>&1 && fail "Pomerium started without its certificate" || ok "Pomerium needs its certificate and key"
for s in cp-pomerium-cert cp-pomerium-key; do echo "$s" > "$STAGE0_HOME/control-plane/secrets/$s"; done
rm -f "$tmp/calls"
"$KIT/run-pomerium.sh" > "$tmp/out" 2>&1 || fail "run-pomerium.sh: $(cat "$tmp/out")"
grep -q "^podman secret create --replace ortus-stage0-pomerium-key $STAGE0_HOME/control-plane/secrets/cp-pomerium-key$" "$tmp/calls" \
  && grep -q -- "--secret ortus-stage0-pomerium-key,target=cp-pomerium-key" "$tmp/calls" \
  && ok "Pomerium's key enters its container as a podman secret, at the path its configuration names" || fail "secrets: $(cat "$tmp/calls")"
grep -q -- "--network host .* $pinned --config /pomerium/config.yaml$" "$tmp/calls" \
  && grep -q -- "-v $STAGE0_HOME/control-plane/pomerium:/pomerium:ro,z" "$tmp/calls" \
  && ok "Pomerium runs on the host's network with cpctl's configuration" || fail "podman run: $(cat "$tmp/calls")"

rm -f "$tmp/calls"
"$KIT/run-egress-gate.sh" > "$tmp/out" 2>&1 || fail "run-egress-gate.sh: $(cat "$tmp/out")"
want="cp-egress -listen 127.0.0.1:7692 -sortie http://127.0.0.1:7680 -control-token-file $STAGE0_HOME/sortie-token -nono 127.0.0.1:7690 -nono-pass-file $STAGE0_HOME/nono-pass -live-proof-nono 127.0.0.1:7691 -live-proof-jobs-file $STAGE0_HOME/live-proofs/active"
want+=" -preview-domain preview.p1.example.org -previews-file $STAGE0_HOME/previews/front-routes -preview-review-key-file $STAGE0_HOME/preview-review-key"
[[ $(cat "$tmp/calls") == "$want" ]] \
  && ok "the egress gate listens on loopback, tunnels through nono, sends a running live proof's egress to the live-proof nono, and serves the previews under Pomerium's host from preview.sh's routes" \
  || fail "cp-egress: $(cat "$tmp/calls")"
(cd "$KIT/../../control-plane" && go run ./cmd/cp-egress -h > "$tmp/flags" 2>&1 || true)
for f in listen sortie control-token-file nono nono-pass-file live-proof-nono live-proof-jobs-file preview-domain previews-file preview-review-key-file; do
  grep -q -- "^  -$f " "$tmp/flags" || fail "cp-egress has no -$f flag"
done
ok "cp-egress takes every flag run-egress-gate.sh gives it"

# The route a review job's machine takes to a ui story's preview, in the
# configuration p1 renders (front_init, with control-plane/'s cpctl): every
# CONNECT, whatever host it names, goes on Pomerium's egress route to the
# egress gate on p1's loopback, and only a listed worker's certificate gets
# through; a revoked worker's is refused there, before the gate.
(cd "$KIT/../../control-plane" && go build -o "$STAGE0_HOME/bin/cpctl" ./cmd/cpctl && go build -o "$tmp/cp-egress" ./cmd/cp-egress) \
  || fail "cpctl and cp-egress do not build"
egress_route() { tail -n +2 "$STAGE0_HOME/control-plane/pomerium/config.yaml" | jq -c '.routes[] | select(.name == "egress")'; }
fingerprint() { cpctl worker list | awk -v n="$1" '$1 == n { print $4 }'; }
(
  # shellcheck source=/dev/null
  . "$STAGE0_HOME/stage0.env"
  # shellcheck source=front-config.sh
  . "$KIT/front-config.sh"
  say() { :; }
  front_init && issue_local_worker p1 && cpctl worker issue taichi > /dev/null || exit 1
  route=$(egress_route) p1=$(fingerprint p1) taichi=$(fingerprint taichi)
  jq -e --arg gate "http://127.0.0.1:$STAGE0_EGRESS_GATE_PORT" '.from == "tcp+https://*" and .to == $gate and .preserve_host_header' <<< "$route" > /dev/null \
    && jq -e --arg p1 "$p1" --arg t "$taichi" '.policy[0].allow.or[0].client_certificate.fingerprint | sort == ([$p1, $t] | sort)' <<< "$route" > /dev/null \
    && ok "Pomerium's egress route sends every CONNECT, a preview's name too, to the egress gate on loopback, for p1's and taichi's certificates" \
    || fail "egress route: $route"
  [[ $(cpctl worker list | tail -n +2 | grep -c .) == 2 ]] && cpctl worker revoke taichi > /dev/null
  route=$(egress_route)
  jq -e --arg p1 "$p1" '.policy[0].allow.or[0].client_certificate.fingerprint == [$p1]' <<< "$route" > /dev/null \
    && [[ $(tail -n +2 "$STAGE0_HOME/control-plane/pomerium/config.yaml" | jq -r '.downstream_mtls.enforcement') == reject_connection ]] \
    && ok "a revoked worker's certificate reaches no route, the previews' neither" || fail "after revoke: $route"
  # Pomerium refuses it there, so Pomerium's journal records the refusal:
  # its authorize check names the host and the job, and its http-request
  # line, with the same request-id, the worker's certificate.
  tail -n +2 "$STAGE0_HOME/control-plane/pomerium/config.yaml" | jq -e '
    (["request-id", "host", "headers.X-Sortie-Job"] - .authorize_log_fields == [])
    and (["request-id", "authority", "response-code", "client-certificate", "headers.X-Sortie-Job"] - .access_log_fields == [])' > /dev/null \
    && ok "Pomerium's journal records a refusal with the worker's certificate, the job and the host" \
    || fail "Pomerium's log fields: $(tail -n +2 "$STAGE0_HOME/control-plane/pomerium/config.yaml" | jq -c '{authorize_log_fields, access_log_fields}')"
) || fail "front_init: could not render Pomerium's configuration"

# The egress gate itself (cp-egress as run-egress-gate.sh runs it), with a
# stand-in for Sortie's token introspection and for a preview on loopback:
# a CONNECT for the preview's name with a live job token opens its home
# and its API under that name; one with no token, or an ended one, is
# refused, and the gate's journal names the worker, the job and the host.
mkdir -p "$tmp/fake" "$STAGE0_HOME/previews"
cat > "$tmp/fake/main.go" <<'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
)

func listen() net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	return l
}

func main() {
	sortie, preview, gate := listen(), listen(), listen()
	_ = gate.Close()
	go func() {
		_ = http.Serve(sortie, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var b struct{ Token string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.Token != "live-job-token" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"job":"ONS-7","attempt":1,"flow":"egress"}`))
		}))
	}()
	f, _ := os.Create(os.Args[1])
	fmt.Fprintf(f, "%d %d %d\n", sortie.Addr().(*net.TCPAddr).Port, preview.Addr().(*net.TCPAddr).Port, gate.Addr().(*net.TCPAddr).Port)
	_ = f.Close()
	_ = http.Serve(preview, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s review-job=%s", r.Host, r.URL.Path, r.Header.Get("X-Workbench-Review-Job"))
	}))
}
EOF
(cd "$tmp/fake" && GOFLAGS='' GOWORK=off go build -o "$tmp/fake/fake" main.go) || fail "the stand-ins do not build"
"$tmp/fake/fake" "$tmp/ports" &
fake=$!
for _ in $(seq 50); do [[ -s $tmp/ports ]] && break; sleep 0.1; done
read -r sortie_port preview_port gate_port < "$tmp/ports"
cp "$tmp/cp-egress" "$STAGE0_HOME/bin/cp-egress"
for s in sortie-token nono-pass preview-review-key; do echo "$s-value" > "$STAGE0_HOME/$s"; done
echo "ons-7.preview.p1.example.org $preview_port" > "$STAGE0_HOME/previews/front-routes"
printf 'STAGE0_SORTIE_PORT=%s\nSTAGE0_EGRESS_GATE_PORT=%s\nSTAGE0_CP_PUBLIC_URL=https://p1.example.org:8443\n' "$sortie_port" "$gate_port" > "$STAGE0_HOME/stage0.env"
"$KIT/run-egress-gate.sh" 2> "$tmp/journal" &
gate=$!
trap 'kill "$fake" "$gate" 2> /dev/null; rm -rf "$tmp"' EXIT
for _ in $(seq 50); do curl -s -o /dev/null "http://127.0.0.1:$gate_port/" && break; sleep 0.1; done
# through WORKER TOKEN PATH: curl through the gate as Pomerium hands it a
# worker's CONNECT for the preview's name; prints the CONNECT's status and
# the preview's answer.
through() {
  local auth=()
  [[ -z $2 ]] || auth=(--proxy-header "Proxy-Authorization: Bearer $2")
  curl -s -p -x "http://127.0.0.1:$gate_port" --proxy-header "X-Sortie-Worker-Identity: $1" --proxy-header "X-Sortie-Job: ONS-7" \
    "${auth[@]}" -w ' %{http_connect}' "http://ons-7.preview.p1.example.org$3" || true
}
[[ $(through taichi live-job-token /) == "ons-7.preview.p1.example.org / review-job=ONS-7 200" ]] \
  && [[ $(through taichi live-job-token /api/auth/mode) == "ons-7.preview.p1.example.org /api/auth/mode review-job=ONS-7 200" ]] \
  && ok "a job with a live token opens the preview's home and its API under its name, signed in as its review" \
  || fail "through the gate: $(through taichi live-job-token /)"
[[ $(through taichi "" /) == *" 407" && $(through p1 ended-job-token /) == *" 407" ]] \
  && ok "a CONNECT with no token, or an ended one, is refused" || fail "refusals: $(through taichi "" /)"
journal=$(cat "$tmp/journal")
for line in 'msg="preview refused" worker=taichi job=ONS-7 host=ons-7.preview.p1.example.org:80 status=407 reason="no live egress token"' \
  'msg="preview refused" worker=p1 job=ONS-7 host=ons-7.preview.p1.example.org:80 status=407 reason="no live egress token"' \
  'msg="preview request" worker=taichi job=ONS-7 attempt=1 host=ons-7.preview.p1.example.org:80'; do
  [[ $journal == *"$line"* ]] && ok "the gate's journal: ${line%% host=*}" || fail "the gate's journal has no $line:"$'\n'"$journal"
done

# nono on p1 is the compose project's: the same release, and the same
# profile, whose credentials egress-credentials.sh gives it by name.
# shellcheck source=nono-version.sh
(. "$KIT/nono-version.sh"; grep -q "image: ghcr.io/nolabs-ai/nono:$NONO_VERSION@sha256:" "$KIT/../../control-plane/compose.yaml") \
  && ok "p1 pins the nono release the compose project runs" || fail "nono-version.sh and control-plane/compose.yaml pin different nono releases"
want=$(grep -o 'env://CP_EGRESS_[A-Z_]*' "$KIT/../../control-plane/nono/egress-profile.json" | sed 's|env://||' | sort -u | tr '\n' ' ')
got=$(sed -n 's/^  export \(.*\)$/\1/p' "$KIT/egress-credentials.sh" | tr ' ' '\n' | sort -u | tr '\n' ' ')
[[ -n $want && $got == "$want" ]] && ok "run-nono.sh gives nono every credential the profile reads ($want)" || fail "profile reads '$want', egress-credentials.sh exports '$got'"
grep -q 'PROFILE=.*control-plane/nono' "$KIT/run-nono.sh" && ok "run-nono.sh runs the control plane's egress profile" || fail "run-nono.sh's profile"


# The units: the edge and its tunnels are gone, and the worker daemon
# starts after Pomerium, its door.
[[ ! -e $KIT/units/ortus-stage0-edge.service && ! -e $KIT/units/ortus-stage0-worker-tunnel@.service && ! -e $KIT/edge && ! -e $KIT/edge-config.sh ]] \
  && ok "the edge, its configuration and its tunnel unit are gone" || fail "the edge is still in the kit"
grep -q '^After=.*ortus-stage0-pomerium.service' "$KIT/units/ortus-stage0-worker.service" && ok "p1's worker starts after Pomerium" || fail "worker unit"
grep -q '^ExecStart=@KIT@/run-pomerium.sh$' "$KIT/units/ortus-stage0-pomerium.service" \
  && grep -q '^ExecStart=@KIT@/run-egress-gate.sh$' "$KIT/units/ortus-stage0-egress-gate.service" \
  && ok "Pomerium and the egress gate run as user units" || fail "front units"
exit $failed
