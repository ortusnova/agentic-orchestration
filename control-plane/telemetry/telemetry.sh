#!/usr/bin/env bash
# The telemetry store's compose project, ortus-telemetry, under rootless
# podman (README.md): Langfuse's web and worker, PostgreSQL, ClickHouse,
# Valkey, MinIO and the OpenTelemetry Collector, its only ingest address.
# Run it as the unprivileged user that owns the project; it refuses to run
# as root.
#
#   telemetry.sh init          write $TELEMETRY_HOME/telemetry.env and every missing secret file
#   telemetry.sh up            init, create the podman secrets from the files, start the project, wait for health
#   telemetry.sh down          stop and remove the containers (volumes and secrets stay)
#   telemetry.sh health        each service, up or down, by name; fails when one is down
#   telemetry.sh backup        a pg_dump, a ClickHouse BACKUP and a copy of the MinIO bucket
#                              into $TELEMETRY_BACKUP_DIR/<UTC time>/, keeping TELEMETRY_BACKUP_KEEP
#   telemetry.sh restore [DIR] replace the store's volumes with the backup DIR (default: the newest)
#   telemetry.sh retention     delete the traces older than TELEMETRY_RETENTION_DAYS, through
#                              Langfuse's public API, and prune the file store to the same age
#   telemetry.sh compose ARGS  run podman-compose on the project
#   telemetry.sh check-tools   fail, saying why, unless podman, podman-compose 1.5 or later, curl and python3 are here
#
# Settings, from the environment:
#   STAGE0_HOME           the stage-0 kit's directory (default ~/.local/share/ortus-nova/stage0)
#   TELEMETRY_HOME        the settings, secrets and file store (default $STAGE0_HOME/telemetry)
#   TELEMETRY_BACKUP_DIR  the backups (default $STAGE0_HOME/telemetry-backups)
#   TELEMETRY_UP_TIMEOUT  seconds up and restore wait for health (default 600)
# Everything else is in $TELEMETRY_HOME/telemetry.env (telemetry.env.example).
set -euo pipefail

TM=$(cd "$(dirname "$0")" && pwd)
die() { printf 'telemetry.sh: %s\n' "$*" >&2; exit 1; }
say() { printf 'telemetry.sh: %s\n' "$*" >&2; }

[[ $(id -u) != 0 ]] || die "run the telemetry store as an unprivileged user, not root"
# The settings, secrets and backups are this user's alone.
umask 077

export STAGE0_HOME=${STAGE0_HOME:-$HOME/.local/share/ortus-nova/stage0}
export TELEMETRY_HOME=${TELEMETRY_HOME:-$STAGE0_HOME/telemetry}
export TELEMETRY_BACKUP_DIR=${TELEMETRY_BACKUP_DIR:-$STAGE0_HOME/telemetry-backups}
export TELEMETRY_FILE_STORE=$TELEMETRY_HOME/file-store
ENV_FILE=$TELEMETRY_HOME/telemetry.env
SECRETS=$TELEMETRY_HOME/secrets
PROJECT=ortus-telemetry

# The secret files, each the podman secret ortus-telemetry-<file>, and how
# init makes one that is missing. Each is named after the chart value that
# holds it (README.md, "Secrets").
SECRET_FILES=(langfuse-salt langfuse-encryption-key langfuse-nextauth-secret langfuse-init-project-secret-key
  langfuse-init-user-password postgresql-auth-password clickhouse-auth-password redis-auth-password
  s3-auth-root-password collector-ingest-token)
new_secret() {
  case $1 in
    langfuse-encryption-key) openssl rand -hex 32 ;;
    langfuse-init-project-secret-key) printf 'sk-lf-%s\n' "$(uuid)" ;;
    *) openssl rand -hex 24 ;;
  esac
}
uuid() {
  if [[ -r /proc/sys/kernel/random/uuid ]]; then cat /proc/sys/kernel/random/uuid; else python3 -c 'import uuid; print(uuid.uuid4())'; fi
}

# The services the health check reports, in the order they start: the
# stores first, then Langfuse, then the collector.
STORES=(postgres clickhouse redis minio)
SERVICES=("${STORES[@]}" langfuse-worker langfuse-web collector)
VOLUMES=(postgres-data clickhouse-data redis-data minio-data)

settings() {
  [[ -s $ENV_FILE ]] || die "$ENV_FILE is missing: run telemetry.sh init"
  set -a
  # shellcheck source=/dev/null
  . "$ENV_FILE"
  set +a
  local d
  for d in TELEMETRY_RETENTION_DAYS TELEMETRY_BACKUP_KEEP; do
    [[ ${!d:-} =~ ^[1-9][0-9]*$ ]] || die "$d in $ENV_FILE is '${!d:-}'; want a whole number of days or backups"
  done
}

init() {
  mkdir -p "$SECRETS" "$TELEMETRY_FILE_STORE"
  chmod 700 "$TELEMETRY_HOME" "$SECRETS"
  if [[ ! -s $ENV_FILE ]]; then
    (umask 077 && sed "s/^LANGFUSE_INIT_PROJECT_PUBLIC_KEY=$/LANGFUSE_INIT_PROJECT_PUBLIC_KEY=pk-lf-$(uuid)/" \
      "$TM/telemetry.env.example" > "$ENV_FILE")
    say "wrote $ENV_FILE: set LANGFUSE_INIT_USER_EMAIL to the operator's address"
  fi
  # A setting telemetry.env.example gained after this file was written gets its default.
  local key line
  while IFS= read -r line; do
    [[ $line =~ ^([A-Z0-9_]+)= ]] || continue
    key=${BASH_REMATCH[1]}
    grep -q "^$key=" "$ENV_FILE" || { printf '%s\n' "$line" >> "$ENV_FILE"; say "added $key to $ENV_FILE"; }
  done < "$TM/telemetry.env.example"
  grep -q '^LANGFUSE_INIT_PROJECT_PUBLIC_KEY=pk-lf-' "$ENV_FILE" \
    || die "LANGFUSE_INIT_PROJECT_PUBLIC_KEY in $ENV_FILE is not a Langfuse public key (pk-lf-...)"
  local f
  for f in "${SECRET_FILES[@]}"; do
    [[ -s $SECRETS/$f ]] && continue
    (umask 077 && new_secret "$f" > "$SECRETS/$f")
    say "made the secret $SECRETS/$f"
  done
}

# podman-compose before 1.5 runs every health check through /bin/sh.
compose_version() { podman-compose --version 2> /dev/null | sed -n 's/^podman-compose version \([0-9.]*\).*/\1/p'; }
compose_version_ok() {
  local v major minor
  v=$(compose_version)
  IFS=. read -r major minor _ <<< "$v"
  [[ -n $major ]] && ((major > 1 || (major == 1 && ${minor:-0} >= 5)))
}

check_tools() {
  local c
  for c in podman curl python3 openssl; do command -v "$c" > /dev/null || die "$c is not installed"; done
  command -v podman-compose > /dev/null || die "podman-compose is not installed (pipx install podman-compose)"
  compose_version_ok || die "podman-compose $(compose_version) is too old: the health checks need 1.5 or later (pipx install podman-compose)"
}

compose() {
  check_tools
  podman-compose -p "$PROJECT" --env-file "$ENV_FILE" -f "$TM/compose.yaml" "$@"
}

# secrets: one podman secret per file, replaced at each start. A file's
# trailing newlines are not part of its secret.
secrets() {
  local f
  for f in "${SECRET_FILES[@]}"; do
    [[ -s $SECRETS/$f ]] || die "$SECRETS/$f is missing: run telemetry.sh init"
    printf '%s' "$(< "$SECRETS/$f")" | podman secret create --replace "$PROJECT-$f" - > /dev/null
  done
}

# The collector's image has no shell for a health check: it is up when its
# OTLP/HTTP port refuses a request without the ingest token.
check_one() {
  case $1 in
    collector)
      [[ $(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' \
        "http://127.0.0.1:$COLLECTOR_PORTS_OTLP_HTTP_HOST_PORT/v1/traces" 2> /dev/null) == 401 ]] ;;
    *) podman healthcheck run "$PROJECT-$1" > /dev/null 2>&1 ;;
  esac
}

health() {
  local svc failed=0
  for svc in "${SERVICES[@]}"; do
    if check_one "$svc"; then printf '%-16s up\n' "$svc"; else printf '%-16s down\n' "$svc"; failed=1; fi
  done
  return $failed
}

# wait_healthy SERVICE...: until each is up, or TELEMETRY_UP_TIMEOUT.
wait_healthy() {
  local timeout=${TELEMETRY_UP_TIMEOUT:-600} svc pending
  local deadline=$((SECONDS + timeout))
  while :; do
    pending=()
    for svc; do check_one "$svc" || pending+=("$svc"); done
    [[ ${#pending[@]} -eq 0 ]] && return 0
    if ((SECONDS >= deadline)); then
      say "not up after $timeout s: ${pending[*]}"
      return 1
    fi
    sleep 3
  done
}

start() {
  secrets
  mkdir -p "$TELEMETRY_FILE_STORE"
  compose up -d "${STORES[@]}" > /dev/null
  wait_healthy "${STORES[@]}"
  compose up -d > /dev/null
  wait_healthy "${SERVICES[@]}"
}

up() {
  init
  settings
  say "starting $PROJECT"
  start
  health
}

# In a store's container, with the credentials its own environment holds.
ch() {
  # shellcheck disable=SC2016 # expanded in the container
  podman exec "$PROJECT-clickhouse" sh -c 'exec clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --query "$1"' sh "$1"
}
mcx() {
  # shellcheck disable=SC2016 # expanded in the container
  podman exec "$PROJECT-minio" sh -c 'export MC_HOST_local="http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@127.0.0.1:9000" MC_CONFIG_DIR=/tmp/.mc; exec mc --quiet --no-color "$@"' sh "$@"
}

backup() {
  settings
  local svc
  for svc in postgres clickhouse minio; do check_one "$svc" || die "$svc is down: start the project first (telemetry.sh up)"; done
  mkdir -p "$TELEMETRY_BACKUP_DIR"
  chmod 700 "$TELEMETRY_BACKUP_DIR"
  local stamp dir part
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  dir=$TELEMETRY_BACKUP_DIR/$stamp
  part=$dir.partial
  rm -rf "$part"
  mkdir -p "$part"
  say "backing up into $dir"
  # PostgreSQL: users, organisation, project and keys.
  # shellcheck disable=SC2016 # expanded in the container
  podman exec "$PROJECT-postgres" sh -c 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$part/postgres.dump" \
    || die "pg_dump failed"
  # ClickHouse: the Langfuse database, its trace tables (traces,
  # observations, scores, events...) with their schema-migration record.
  ch "BACKUP DATABASE $CLICKHOUSE_DATABASE TO Disk('backups', '$stamp')" > /dev/null || die "the ClickHouse BACKUP failed"
  podman cp "$PROJECT-clickhouse:/var/lib/clickhouse/backups/$stamp" "$part/clickhouse" \
    || die "could not copy the ClickHouse backup out"
  podman exec "$PROJECT-clickhouse" rm -rf "/var/lib/clickhouse/backups/$stamp"
  # MinIO: a copy of the bucket.
  podman exec "$PROJECT-minio" rm -rf /tmp/bucket
  mcx mirror "local/$S3_BUCKET" /tmp/bucket > /dev/null || die "could not copy the MinIO bucket $S3_BUCKET"
  podman exec "$PROJECT-minio" mkdir -p /tmp/bucket
  podman cp "$PROJECT-minio:/tmp/bucket" "$part/minio" || die "could not copy the MinIO bucket out"
  podman exec "$PROJECT-minio" rm -rf /tmp/bucket
  # The settings and secrets the data needs: the salt the key hashes use,
  # the encryption key, the key pair.
  mkdir -p "$part/settings"
  cp -p "$ENV_FILE" "$part/settings/telemetry.env"
  cp -rp "$SECRETS" "$part/settings/secrets"
  mv "$part" "$dir"
  # Keep the newest TELEMETRY_BACKUP_KEEP.
  local old
  while IFS= read -r old; do
    rm -rf "${TELEMETRY_BACKUP_DIR:?}/$old"
    say "removed the backup $old"
  done < <(backups | head -n "-$TELEMETRY_BACKUP_KEEP")
  echo "$dir"
}

# backups: the complete backups, oldest first.
backups() {
  [[ -d $TELEMETRY_BACKUP_DIR ]] || return 0
  find "$TELEMETRY_BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '[0-9]*T[0-9]*Z' -printf '%f\n' | LC_ALL=C sort
}

restore() {
  local dir=${1:-}
  if [[ -z $dir ]]; then
    dir=$(backups | tail -n 1)
    [[ -n $dir ]] || die "no backup in $TELEMETRY_BACKUP_DIR"
    dir=$TELEMETRY_BACKUP_DIR/$dir
  fi
  dir=$(cd "$dir" && pwd) || die "no backup at $dir"
  local f
  for f in postgres.dump clickhouse minio settings/telemetry.env; do
    [[ -e $dir/$f ]] || die "$dir is not a complete backup: $f is missing"
  done
  # Settings or secrets this host lost come from the backup; the rest stay.
  mkdir -p "$SECRETS"
  chmod 700 "$TELEMETRY_HOME" "$SECRETS"
  [[ -s $ENV_FILE ]] || cp -p "$dir/settings/telemetry.env" "$ENV_FILE"
  for f in "$dir"/settings/secrets/*; do
    [[ -s $SECRETS/${f##*/} ]] || { cp -p "$f" "$SECRETS/"; say "took ${f##*/} from the backup"; }
  done
  init
  settings
  say "restoring $dir: the project stops and its volumes are replaced"
  compose down > /dev/null 2>&1 || true
  local v
  for v in "${VOLUMES[@]}"; do podman volume rm -f "${PROJECT}_$v" > /dev/null 2>&1 || true; done
  secrets
  compose up -d "${STORES[@]}" > /dev/null
  wait_healthy "${STORES[@]}"

  # shellcheck disable=SC2016 # expanded in the container
  podman exec -i "$PROJECT-postgres" sh -c 'exec pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner --exit-on-error' \
    < "$dir/postgres.dump" || die "pg_restore failed"

  local name
  name=restore-$(date -u +%Y%m%dT%H%M%SZ)
  podman cp "$dir/clickhouse" "$PROJECT-clickhouse:/var/lib/clickhouse/backups/$name" || die "could not copy the ClickHouse backup in"
  podman exec -u 0 "$PROJECT-clickhouse" chown -R 101:101 "/var/lib/clickhouse/backups/$name"
  ch "RESTORE DATABASE $CLICKHOUSE_DATABASE FROM Disk('backups', '$name') SETTINGS allow_non_empty_tables = true" > /dev/null \
    || die "the ClickHouse RESTORE failed"
  podman exec "$PROJECT-clickhouse" rm -rf "/var/lib/clickhouse/backups/$name"

  podman exec -u 0 "$PROJECT-minio" rm -rf /tmp/bucket
  podman cp "$dir/minio" "$PROJECT-minio:/tmp/bucket" || die "could not copy the MinIO bucket in"
  podman exec -u 0 "$PROJECT-minio" chown -R 65532:65532 /tmp/bucket
  mcx mirror --overwrite /tmp/bucket "local/$S3_BUCKET" > /dev/null || die "could not restore the MinIO bucket $S3_BUCKET"
  podman exec -u 0 "$PROJECT-minio" rm -rf /tmp/bucket

  compose up -d > /dev/null
  wait_healthy "${SERVICES[@]}"
  say "restored $dir"
  health
}

retention() {
  settings
  check_one langfuse-web || die "Langfuse is down: start the project first (telemetry.sh up)"
  local days=$TELEMETRY_RETENTION_DAYS
  say "deleting the traces older than $days days"
  LANGFUSE_HOST=http://127.0.0.1:$LANGFUSE_WEB_SERVICE_PORT \
    LANGFUSE_PUBLIC_KEY=$LANGFUSE_INIT_PROJECT_PUBLIC_KEY \
    LANGFUSE_SECRET_KEY_FILE=$SECRETS/langfuse-init-project-secret-key \
    python3 "$TM/retention.py" "$days" || die "the deletion through Langfuse's public API failed"
  say "pruning the file store $TELEMETRY_FILE_STORE to $days days"
  [[ -d $TELEMETRY_FILE_STORE ]] || return 0
  find "$TELEMETRY_FILE_STORE" -type f -mmin "+$((days * 1440))" -print -delete
  find "$TELEMETRY_FILE_STORE" -mindepth 2 -type d -empty -delete
}

cmd=${1:-}
[[ $# -gt 0 ]] && shift
case $cmd in
  init) init ;;
  up) up ;;
  down) settings; compose down ;;
  health) settings; health ;;
  backup) backup ;;
  restore) restore "$@" ;;
  retention) retention ;;
  compose) settings; compose "$@" ;;
  check-tools) check_tools ;;
  *) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2; exit 2 ;;
esac
