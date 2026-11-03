#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks homelab-egress.sh against a homelab origin in a temporary git
# repository, with systemctl stubbed (spec store homelab-on-sortie, "The
# homelab workflow allows only what homelab declares"): stage0.sh renders
# the homelab nono's profile from scripts/smolvm/egress-hosts.yaml in the
# kit's homelab worktree, with only those hosts and the credentials'; a
# merged homelab change that adds a host reaches the profile, and restarts
# the homelab nono, the next time the kit runs; and a missing or malformed
# file stops the kit, which leaves the profile and the nono as they were.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export STAGE0_HOME=$tmp/home
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }

mkdir -p "$tmp/bin" "$STAGE0_HOME"
if [[ -n ${STAGE0_EDGE_BIN:-} ]]; then
  ln -s "$STAGE0_EDGE_BIN" "$tmp/bin/stage0-edge"
else
  (cd "$KIT/edge" && go build -o "$tmp/bin/stage0-edge" .)
fi
# The homelab nono runs once started.
# shellcheck disable=SC2016 # expanded by the stub, not here
printf '#!/bin/sh\ncase "$2" in\n  is-active) [ -e "%s/active" ] ;;\n  restart) echo "restart $3" >> "%s/calls"; touch "%s/active" ;;\nesac\n' \
  "$tmp" "$tmp" "$tmp" > "$tmp/bin/systemctl"
chmod +x "$tmp/bin/systemctl"
export PATH=$tmp/bin:$PATH

# homelab's origin, on master, with its egress-hosts.yaml.
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
git init -q -b master "$tmp/origin"
mkdir -p "$tmp/origin/scripts/smolvm"
cp "$KIT/edge/testdata/egress-hosts.yaml" "$tmp/origin/scripts/smolvm/egress-hosts.yaml"
merge() { git -C "$tmp/origin" add -A && git -C "$tmp/origin" commit -q -m "$1"; }
merge "the hosts the vm chain needs"
export STAGE0_HOMELAB_REPO_URL=$tmp/origin STAGE0_HOMELAB_REPO_BASE=master

profile=$STAGE0_HOME/egress-profile-homelab.json
# kit_run: what stage0.sh does on p1 for the homelab nono, in a run of its own.
kit_run() {
  (
    say() { :; }
    die() { echo "$*" >&2; exit 1; }
    # shellcheck source=unit-restart.sh
    . "$KIT/unit-restart.sh"
    # shellcheck source=homelab-inputs.sh
    . "$KIT/homelab-inputs.sh"
    # shellcheck source=homelab-egress.sh
    . "$KIT/homelab-egress.sh"
    homelab_worktree "${STAGE0_HOMELAB_SRC:-}"
    homelab_egress_profile "$profile"
    restart_changed ortus-stage0-nono-homelab.service "$profile" 2> /dev/null
  ) > "$tmp/out" 2>&1
}
allowed() { sed -n '/"allow_domain"/,/]/p' "$profile" | grep -o '"[^"]*"' | tr -d '"' | grep -vx allow_domain | tr '\n' ' '; }
allows() { grep -q "\"$1\"" "$profile"; }
restarted() { grep -qx 'restart ortus-stage0-nono-homelab.service' "$tmp/calls" 2> /dev/null; }

rm -f "$tmp/calls"
kit_run && ok "the kit renders the homelab profile from its own clone of homelab" || fail "first run: $(cat "$tmp/out")"
[[ -d $STAGE0_HOME/homelab/.git ]] && ok "the kit's homelab worktree is in \$STAGE0_HOME/homelab" || fail "no worktree"
want="api.anthropic.com api.github.com auth.docker.io deb.debian.org ghcr.io github.com us-central1-docker.pkg.dev "
[[ $(allowed) == "$want" ]] && ok "the profile allows exactly homelab's hosts and the credentials' hosts" \
  || fail "the profile allows $(allowed), want exactly $want"
restarted && ok "the homelab nono starts on it" || fail "no restart: $(cat "$tmp/calls" 2> /dev/null)"

rm -f "$tmp/calls"
kit_run || fail "second run: $(cat "$tmp/out")"
restarted && fail "an unchanged list restarted the homelab nono" || ok "an unchanged list leaves the homelab nono running"

# Scenario: A new host. A merged homelab change adds it; the next kit run
# allows it, and the homelab nono restarts onto it.
sed -i 's/^  - host: ghcr.io$/  - host: get.helm.sh\n    ports: [443]\n    steps: [seed]\n    reason: The helm release the seed installs.\n  - host: ghcr.io/' \
  "$tmp/origin/scripts/smolvm/egress-hosts.yaml"
merge "the seed reaches get.helm.sh"
allows get.helm.sh && fail "the profile allows the new host before the kit runs"
rm -f "$tmp/calls"
kit_run || fail "run after the merge: $(cat "$tmp/out")"
allows get.helm.sh && ok "a host homelab adds is allowed once the kit runs again" || fail "get.helm.sh is not allowed: $(allowed | tr '\n' ' ')"
restarted && ok "and the homelab nono restarts onto it" || fail "no restart after the new host"
cp "$profile" "$tmp/good.json"

refused() { # refused NAME: the kit run fails, and leaves the profile and the nono as they were
  rm -f "$tmp/calls"
  if kit_run; then
    fail "$1: the kit ran"
  elif ! cmp -s "$profile" "$tmp/good.json"; then
    fail "$1: the profile changed"
  elif restarted; then
    fail "$1: the homelab nono restarted"
  else
    ok "$1 stops the kit; the profile and the homelab nono stay as they were ($(head -c 100 "$tmp/out"))"
  fi
}

git -C "$tmp/origin" rm -q scripts/smolvm/egress-hosts.yaml
merge "no list"
refused "a missing egress-hosts.yaml"
mkdir -p "$tmp/origin/scripts/smolvm"

printf 'hosts:\n  - host: "*.docker.io"\n' > "$tmp/origin/scripts/smolvm/egress-hosts.yaml"
merge "a wildcard"
refused "an egress-hosts.yaml with a wildcard"

printf 'hosts: [\n  - host: ghcr.io\n' > "$tmp/origin/scripts/smolvm/egress-hosts.yaml"
merge "not YAML"
refused "an egress-hosts.yaml that is not YAML"

printf 'refused: []\n' > "$tmp/origin/scripts/smolvm/egress-hosts.yaml"
merge "no hosts"
refused "an egress-hosts.yaml with no hosts list"

# With the operator's homelab checkout (STAGE0_HOMELAB_SRC), the worktree is
# added from it, as on a worker host with homelab's dev host.
cp "$KIT/edge/testdata/egress-hosts.yaml" "$tmp/origin/scripts/smolvm/egress-hosts.yaml"
merge "the list again"
git clone -q "$tmp/origin" "$tmp/src"
rm -rf "$STAGE0_HOME/homelab"
STAGE0_HOMELAB_SRC=$tmp/src kit_run && [[ -f $STAGE0_HOME/homelab/.git ]] \
  && ok "with STAGE0_HOMELAB_SRC, the worktree is added from the operator's checkout" \
  || fail "from STAGE0_HOMELAB_SRC: $(cat "$tmp/out")"

exit $failed
