#!/usr/bin/env bash
# Checks stage0.sh's Huly restart-policy fix against a fake podman.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=huly-restart-policy.sh
. "$KIT/huly-restart-policy.sh"

fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT
: > "$fake/log"
failed=0

# Containers as "id name policy compose-dir", one per line; podman update
# rewrites a line's policy and logs the call.
cat > "$fake/containers" <<'EOF'
c1 huly_v7_redpanda_1 no /srv/huly
c2 huly_v7_account_1 unless-stopped /srv/huly
c3 huly_v7_kvs_1 always /srv/huly
c4 other_db_1 no /srv/other
EOF
cat > "$fake/podman" <<EOF
#!/usr/bin/env bash
db=$fake/containers
case "\$1" in
  ps) dir=\${4#label=com.docker.compose.project.working_dir=}; awk -v d="\$dir" '\$4 == d { print \$1 }' "\$db" ;;
  inspect) awk -v i="\${@: -1}" '\$1 == i { print \$2, \$3 }' "\$db" ;;
  update) echo "update \$*" >> $fake/log; awk -v i="\${@: -1}" -v p="\$3" '\$1 == i { \$3 = p } { print }' "\$db" > "\$db.new"; mv "\$db.new" "\$db" ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$fake/podman"

expect() { # expect DESCRIPTION GOT WANT
  if [[ $2 == "$3" ]]; then echo "ok: $1"; else echo "FAIL: $1: got '$2', want '$3'"; failed=1; fi
}

changed=$(PATH=$fake:$PATH huly_restart_policies /srv/huly)
expect "only the container without a policy is changed" "$changed" huly_v7_redpanda_1
expect "it now restarts unless stopped" "$(awk '$1 == "c1" { print $3 }' "$fake/containers")" unless-stopped
expect "another project's container is left alone" "$(awk '$1 == "c4" { print $3 }' "$fake/containers")" no
expect "one update call" "$(wc -l < "$fake/log" | tr -d ' ')" 1

changed=$(PATH=$fake:$PATH huly_restart_policies /srv/huly)
expect "a second run changes nothing" "$changed" ""
expect "still one update call" "$(wc -l < "$fake/log" | tr -d ' ')" 1
exit $failed
