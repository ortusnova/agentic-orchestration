#!/usr/bin/env bash
# Checks the kit's Huly stack fix (huly-restart-policy.sh) against a fake
# podman: the restart policy in the compose files, the policy of older
# containers, and a start whenever any container is down.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=huly-restart-policy.sh
. "$KIT/huly-restart-policy.sh"

fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT
: > "$fake/log"
# The fake's clock: a restart is the newest start, after every fixture's.
echo 10000 > "$fake/clock"
failed=0

# Containers as "id name policy compose-dir state service started", one
# per line, started in Unix seconds. podman update rewrites a line's
# policy; podman compose up -d starts every container of the directory it
# runs in; podman restart starts the named ones anew. Each logs the call.
cat > "$fake/containers" <<'EOF'
c1 huly_v7_redpanda_1 no /srv/huly running redpanda 100
c2 huly_v7_account_1 unless-stopped /srv/huly running account 200
c3 huly_v7_kvs_1 always /srv/huly running kvs 50
c4 other_db_1 no /srv/other exited db 10
EOF
cat > "$fake/podman" <<EOF
#!/usr/bin/env bash
db=$fake/containers
case "\$1 \$2" in
  "ps -aq") dir=\${4#label=com.docker.compose.project.working_dir=}; awk -v d="\$dir" '\$4 == d { print \$1 }' "\$db" ;;
  "ps -a") dir=\${4#label=com.docker.compose.project.working_dir=}; awk -v d="\$dir" '\$4 == d { print \$2, \$5 }' "\$db" ;;
  "compose up") echo "compose up \$3 in \$PWD" >> $fake/log; awk -v d="\$PWD" '\$4 == d && \$5 != "running" { \$5 = "running"; \$7 = 1000 } { print }' "\$db" > "\$db.new"; mv "\$db.new" "\$db" ;;
  *)
    case "\$1" in
      inspect)
        if [[ "\$*" == *compose.service* ]]; then awk -v i="\${@: -1}" '\$1 == i { print \$2, \$6, \$5, \$7 }' "\$db"
        else awk -v i="\${@: -1}" '\$1 == i { print \$2, \$3 }' "\$db"; fi ;;
      restart) shift; echo "restart \$*" >> $fake/log
        for n in "\$@"; do
          now=\$((\$(cat $fake/clock) + 1)); echo "\$now" > $fake/clock
          awk -v n="\$n" -v t="\$now" '\$2 == n { \$7 = t } { print }' "\$db" > "\$db.new"; mv "\$db.new" "\$db"
        done ;;
      update) echo "update \$*" >> $fake/log; awk -v i="\${@: -1}" -v p="\$3" '\$1 == i { \$3 = p } { print }' "\$db" > "\$db.new"; mv "\$db.new" "\$db" ;;
      *) exit 2 ;;
    esac ;;
esac
EOF
cat > "$fake/docker" <<EOF
#!/usr/bin/env bash
echo "docker \$1 \$2 \$3 in \$PWD" >> $fake/log
EOF
chmod +x "$fake/podman" "$fake/docker"

expect() { # expect DESCRIPTION GOT WANT
  if [[ $2 == "$3" ]]; then echo "ok: $1"; else echo "FAIL: $1: got '$2', want '$3'"; failed=1; fi
}
state() { awk -v i="$1" '$1 == i { print $5 }' "$fake/containers"; }
calls() { grep -c "$1" "$fake/log" || true; }

# The restart policy of containers created before the override.
changed=$(PATH=$fake:$PATH huly_restart_policies /srv/huly)
expect "only the container without a policy is changed" "$changed" huly_v7_redpanda_1
expect "it now restarts unless stopped" "$(awk '$1 == "c1" { print $3 }' "$fake/containers")" unless-stopped
expect "another project's container is left alone" "$(awk '$1 == "c4" { print $3 }' "$fake/containers")" no
expect "one update call" "$(calls update)" 1
changed=$(PATH=$fake:$PATH huly_restart_policies /srv/huly)
expect "a second run changes nothing" "$changed" ""
expect "still one update call" "$(calls update)" 1

# The override: installed beside compose.yml, kept out of git status,
# replaced only when it is the kit's, and it gives redpanda its policy.
huly=$fake/huly-selfhost
mkdir -p "$huly/.git/info"
echo "services: {}" > "$huly/compose.yml"
installed=$(huly_compose_override "$huly")
expect "the override is installed" "$installed" "$huly/compose.override.yml"
expect "it is the kit's file" "$(cmp -s "$huly/compose.override.yml" "$KIT/huly-compose.override.yml" && echo same)" same
expect "it is excluded from the checkout's git status" "$(grep -cxF /compose.override.yml "$huly/.git/info/exclude")" 1
installed=$(huly_compose_override "$huly")
expect "a second run changes nothing" "$installed" ""
expect "the exclude is written once" "$(grep -cxF /compose.override.yml "$huly/.git/info/exclude")" 1
echo "$HULY_OVERRIDE_MARK" > "$huly/compose.override.yml"
installed=$(huly_compose_override "$huly")
expect "a stale kit override is replaced" "$installed" "$huly/compose.override.yml"
echo "services: {front: {ports: ['8088:80']}}" > "$huly/compose.override.yml"
if huly_compose_override "$huly" 2> "$fake/err"; then
  expect "a person's own override is refused" ok refused
else
  expect "a person's own override is refused, naming it" "$(grep -c "$huly/compose.override.yml is not the kit's" "$fake/err")" 1
fi
expect "and left as it was" "$(cat "$huly/compose.override.yml")" "services: {front: {ports: ['8088:80']}}"
expect "the kit's override gives redpanda unless-stopped" \
  "$(awk '/^  redpanda:/ { r = 1; next } r && /restart:/ { print $2; exit }' "$KIT/huly-compose.override.yml")" unless-stopped

# 2026-11-18: redpanda down while nginx and the front end serve. The stack
# is started again, though /config.json would answer.
sed -i 's#^c1 \(.*\) running \(.*\)$#c1 \1 exited \2#' "$fake/containers"
mkdir -p "$fake/srv/.git/info"
cp "$huly/compose.yml" "$fake/srv/compose.yml"
sed -i "s#/srv/huly#$fake/srv#" "$fake/containers"
expect "redpanda is found down" "$(PATH=$fake:$PATH huly_down_containers "$fake/srv")" huly_v7_redpanda_1
PATH=$fake:$PATH huly_ensure "$fake/srv" 2> "$fake/err"
expect "the stack is started in its directory" "$(grep -c "compose up -d in $fake/srv" "$fake/log")" 1
expect "it says which container was down" "$(grep -c "down: huly_v7_redpanda_1" "$fake/err")" 1
expect "redpanda runs again" "$(state c1)" running
expect "and the account service that started before it is restarted" "$(grep "^restart" "$fake/log")" "restart huly_v7_account_1"
PATH=$fake:$PATH huly_ensure "$fake/srv" 2> "$fake/err"
expect "a whole stack is not started again" "$(calls "compose up")" 1
expect "the other project is never started" "$(state c4)" exited

# 2026-11-20: redpanda started again after the transactor and account, which
# kept running without their topics. Each queue service that started
# before redpanda is restarted, and only those.
cat >> "$fake/containers" <<EOF
c5 huly_v7_transactor_1 unless-stopped $fake/srv running transactor 500
c6 huly_v7_fulltext_1 unless-stopped $fake/srv running fulltext 3000
c7 huly_v7_front_1 unless-stopped $fake/srv running front 500
c8 huly_v7_nginx_1 unless-stopped $fake/srv running nginx 9000
EOF
sed -i 's#^c1 \(.*\) [0-9]*$#c1 \1 1500#; s#^c2 \(.*\) [0-9]*$#c2 \1 600#' "$fake/containers"
expect "the queue services that started before redpanda are found" \
  "$(PATH=$fake:$PATH huly_stale_consumers "$fake/srv" | sort | tr '\n' ' ')" "huly_v7_account_1 huly_v7_transactor_1 "
PATH=$fake:$PATH huly_ensure "$fake/srv" 2> "$fake/err"
expect "they are restarted, and no other queue service" "$(grep "^restart" "$fake/log" | tail -n 2 | head -n 1)" "restart huly_v7_account_1 huly_v7_transactor_1"
expect "then nginx, which now started before them" "$(grep "^restart" "$fake/log" | tail -n 1)" "restart huly_v7_nginx_1"
expect "it says why" "$(grep -c "started before redpanda" "$fake/err")" 1
expect "no compose up for a stack that runs" "$(calls "compose up")" 1
expect "a later check finds none" "$(PATH=$fake:$PATH huly_stale_consumers "$fake/srv")" ""
sed -i 's#^c1 \(.*\) running \(.*\)$#c1 \1 exited \2#' "$fake/containers"
expect "with redpanda down, none is stale: it is down instead" "$(PATH=$fake:$PATH huly_stale_consumers "$fake/srv")" ""
expect "the override starts exactly the queue services after redpanda" \
  "$(awk '/^  [a-z]+:$/ { svc = $1 } /depends_on: \[redpanda\]/ { sub(":", "", svc); printf "%s ", svc }' "$KIT/huly-compose.override.yml")" "$HULY_QUEUE_SERVICES "

# 2026-11-20: compose recreated account after nginx started; nginx kept
# sending logins to the old address. nginx is restarted after any service
# started later than it, and only then.
sed -i 's#^c1 \(.*\) exited \(.*\)$#c1 \1 running \2#; s#^c8 \(.*\) [0-9]*$#c8 \1 1800#' "$fake/containers"
: > "$fake/log"
expect "nginx that started before another service is found" "$(PATH=$fake:$PATH huly_stale_nginx "$fake/srv")" huly_v7_nginx_1
PATH=$fake:$PATH huly_ensure "$fake/srv" 2> "$fake/err"
expect "it is restarted, alone" "$(grep "^restart" "$fake/log")" "restart huly_v7_nginx_1"
expect "a later check finds it current" "$(PATH=$fake:$PATH huly_stale_nginx "$fake/srv")" ""
PATH=$fake:$PATH huly_ensure "$fake/srv" 2> "$fake/err"
expect "and a later run restarts nothing" "$(grep -c "^restart huly_v7_nginx_1" "$fake/log")" 1

# A directory whose project has no container yet is started.
mkdir -p "$fake/new"
echo "services: {}" > "$fake/new/compose.yml"
expect "a project with no container reads as none" "$(PATH=$fake:$PATH huly_down_containers "$fake/new")" none
PATH=$fake:$PATH huly_ensure "$fake/new" docker compose 2> /dev/null
expect "with the compose command it is given" "$(grep -c "docker compose up -d in $fake/new" "$fake/log")" 1
exit $failed
