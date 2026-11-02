#!/usr/bin/env bash
# Checks that stage0.sh retires the stage-1 edge's units, against a fake
# systemctl.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=retire-edge.sh
. "$KIT/retire-edge.sh"

fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT
failed=0
expect() { # expect DESCRIPTION GOT WANT
  if [[ $2 == "$3" ]]; then echo "ok: $1"; else echo "FAIL: $1: got '$2', want '$3'"; failed=1; fi
}

cat > "$fake/systemctl" <<FAKE
#!/usr/bin/env bash
echo "systemctl \$*" >> "$fake/log"
case "\$*" in
  *list-units*) cat "$fake/tunnels" ;;
esac
FAKE
chmod +x "$fake/systemctl"
unitdir=$fake/units
mkdir -p "$unitdir"

# A host that ran the edge: its units, and the tunnels of two worker hosts.
for u in ortus-stage0-edge.service ortus-stage0-nono.service ortus-stage0-token.timer ortus-stage0-token.service \
  ortus-stage0-worker-tunnel@.service ortus-stage0-sortie.service; do : > "$unitdir/$u"; done
printf 'ortus-stage0-worker-tunnel@taichi.service loaded active running tunnel\nortus-stage0-worker-tunnel@mac-mini.service loaded active running tunnel\n' > "$fake/tunnels"
: > "$fake/log"
retired=$(PATH=$fake:$PATH retire_edge "$unitdir" | sort | tr '\n' ' ')
expect "every unit of the edge and each tunnel is retired" "$retired" \
  "ortus-stage0-edge.service ortus-stage0-nono.service ortus-stage0-token.service ortus-stage0-token.timer ortus-stage0-worker-tunnel@mac-mini.service ortus-stage0-worker-tunnel@taichi.service "
expect "each is stopped and disabled" "$(grep -c 'disable -q --now' "$fake/log")" 6
expect "only Sortie's unit file is left" "$(ls "$unitdir")" ortus-stage0-sortie.service

# A host that never ran it, or already retired it.
: > "$fake/tunnels"
: > "$fake/log"
retired=$(PATH=$fake:$PATH retire_edge "$unitdir")
expect "a second run retires nothing" "$retired" ""
expect "and disables nothing" "$(grep -c 'disable' "$fake/log" || true)" 0
exit $failed
