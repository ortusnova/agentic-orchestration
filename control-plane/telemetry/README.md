# The telemetry store

Langfuse, self-hosted, with an OpenTelemetry Collector as its only ingest
address: one compose project, `ortus-telemetry`, under rootless podman, on
p1 (spec store `agent-platform/session-telemetry`). The operator alone reads
it, over an SSH tunnel. Every service listens on p1's loopback or on the
project's internal network only, sign-up is off, and there is one user.

| Service | Image (pinned by digest) | What it does |
|---|---|---|
| `collector` | `otel/opentelemetry-collector-contrib` | The only ingest address: OTLP over gRPC and HTTP, behind the ingest token. Sets the Langfuse session and issue, masks token shapes, sends traces to Langfuse, writes log records and metrics to the file store. `127.0.0.1:4317` and `127.0.0.1:4318`; `collector:4317` and `collector:4318` on the internal network. |
| `langfuse-web` | `langfuse/langfuse` | Langfuse's UI and public API, `127.0.0.1:3000`. Its headless initialisation creates the organisation, the project, its key pair and the operator's user. |
| `langfuse-worker` | `langfuse/langfuse-worker` | Langfuse's ingestion and deletion queues. |
| `postgres` | `postgres` | Users, organisation, project, API keys. |
| `clickhouse` | `clickhouse/clickhouse-server` | The traces, observations and scores. |
| `redis` | `valkey/valkey` | Langfuse's queues and cache. |
| `minio` | `chainguard/minio` | The bucket of raw ingested events and media. |

No service but the collector and Langfuse's web publishes a port, and those
two publish on `127.0.0.1` only. Two networks: `ortus-telemetry-internal`
has no route out and every service is on it; `ortus-telemetry-outside`
carries the two published ports. A container that joins
`ortus-telemetry-internal` reaches the collector by its name.

Langfuse takes only traces over OTLP, so the collector writes log records
and metrics to a file store of its own: `$TELEMETRY_HOME/file-store/logs/`
and `metrics/`, one JSON-lines file per UTC day (`2026-11-05.jsonl`),
rotated at `COLLECTOR_CONFIG_EXPORTERS_FILE_ROTATION_MAX_MEGABYTES`.

## Before you start

On p1, as the operator's user (never root): podman 4.7 or later, rootless,
podman-compose 1.5 or later (`pipx install podman-compose`), curl, openssl
and python3; `loginctl enable-linger $USER` and `systemctl --user enable
podman-restart.service`, so the containers outlive the session and come back
after a reboot.

The stage-0 kit does all of this on p1 by itself (`dev/stage0/stage0.sh`,
`dev/stage0/README.md`, "Telemetry"): it runs the store as the unit
`ortus-stage0-telemetry`, with `TELEMETRY_HOME=$STAGE0_HOME/telemetry` and
the backups in `$STAGE0_HOME/telemetry-backups`.

## Start, stop, check

```sh
control-plane/telemetry/telemetry.sh init      # $TELEMETRY_HOME/telemetry.env and every secret file, once
control-plane/telemetry/telemetry.sh up        # podman secrets from the files, start, wait for health
control-plane/telemetry/telemetry.sh health    # each service, up or down
control-plane/telemetry/telemetry.sh down      # stop and remove the containers; volumes and secrets stay
```

`TELEMETRY_HOME` defaults to `$STAGE0_HOME/telemetry`
(`~/.local/share/ortus-nova/stage0/telemetry`). `up` runs `init` first, so a
first `up` is enough; edit `LANGFUSE_INIT_USER_EMAIL` in `telemetry.env`
before it. On p1 the kit's units run the same script through
`dev/stage0/run-telemetry.sh`: `systemctl --user restart
ortus-stage0-telemetry` stops and starts the store, `~/.local/state/ortus-nova/logs/ortus-stage0-telemetry.log`
shows it, and `dev/stage0/run-telemetry.sh health`
checks it.

`up` starts the stores first, waits until each answers its health check,
then starts Langfuse and the collector. Langfuse migrates PostgreSQL and
ClickHouse itself on start.

## Access by SSH tunnel

Nothing listens beyond p1's loopback. From the operator's machine:

```sh
ssh -N -L 3000:127.0.0.1:3000 p1
```

then open `http://localhost:3000` (`LANGFUSE_NEXTAUTH_URL`) and sign in as
`LANGFUSE_INIT_USER_EMAIL` with the password in
`$TELEMETRY_HOME/secrets/langfuse-init-user-password`. Use the same local
port as `LANGFUSE_WEB_SERVICE_PORT`, or change `LANGFUSE_NEXTAUTH_URL` to
the URL you open. The public API takes the project's key pair: the public key
is `LANGFUSE_INIT_PROJECT_PUBLIC_KEY`, the secret key is in
`secrets/langfuse-init-project-secret-key`.

To send from another host, tunnel the collector's port the same way
(`-L 4318:127.0.0.1:4318`).

## Sending telemetry

Send OTLP to the collector, never to Langfuse: `http://127.0.0.1:4318` (HTTP)
or `127.0.0.1:4317` (gRPC) on p1, `http://collector:4318` from a container on
`ortus-telemetry-internal`, with the header `Authorization: Bearer <ingest
token>` (`$TELEMETRY_HOME/secrets/collector-ingest-token`). Without it, the
collector answers 401 (gRPC: `UNAUTHENTICATED`). For acp-broker, point
`-otlp-endpoint` at the collector and give it the header in its
`-otlp-headers-file` (a line `Authorization: Bearer <token>`, mode 0600).

On every span that carries them the collector sets:

- `langfuse.session.id` to `<sortie.dispatch.id>/<sortie.attempt>`: one
  Langfuse session per attempt of a Sortie dispatch;
- `langfuse.trace.metadata.issue` to `sortie.issue.identifier`.

It sends traces to Langfuse's `/api/public/otel` with the project's key pair
(Basic auth) and `x-langfuse-ingestion-version: 4`. Langfuse v4 needs that
header to write a span straight to its v4 data model, where it shows at
once; without it a span may take minutes. Change
`COLLECTOR_CONFIG_EXPORTERS_OTLP_HTTP_LANGFUSE_HEADERS_X_LANGFUSE_INGESTION_VERSION`
when a Langfuse upgrade names another.

### Masking

Content capture is on: inputs and outputs reach Langfuse. Each hop that
holds a credential masks it before it sends a span on: acp-broker masks the
session's own tokens (`acp-broker/telemetry-masking`), and the collector
replaces every well-known token shape with `[redacted]` in span names,
status messages, attributes and events, in log bodies and attributes, and in
resource and metric data point attributes (`otelcol.yaml`):

- GitHub tokens (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`);
- `sk-` keys: Anthropic, OpenAI, Langfuse;
- GitLab (`glpat-`), Slack (`xoxb-` and the like), AWS access key IDs
  (`AKIA`, `ASIA`), Google API keys (`AIza`), JWTs, PEM private keys;
- `Bearer` and `Basic` credentials (the scheme stays);
- a standalone run of exactly 43 base64url characters: the shape of a Sortie
  job token, which is a job's egress token. Ordinary text of exactly that
  shape is masked too.

## Settings

Every setting is in one environment file, `$TELEMETRY_HOME/telemetry.env`,
which `init` copies from `telemetry.env.example` and to which it adds a key
the example gained later. Each key is named after the Helm chart value that
holds the same setting: the value's path in capitals, with each dot, dash
and change of case an underscore. The Langfuse chart
([langfuse-k8s](https://github.com/langfuse/langfuse-k8s), `charts/langfuse`)
is at the top; the collector chart
([opentelemetry-helm-charts](https://github.com/open-telemetry/opentelemetry-helm-charts),
`charts/opentelemetry-collector`) sits under the alias `collector`, as an
umbrella chart would hold it. A setting the Langfuse chart passes only as
`langfuse.additionalEnv` keeps its variable's name. `TELEMETRY_*` are the
kit's own. After an edit, run `telemetry.sh up` again (on p1, `stage0.sh`
does, as the file is one of the store's inputs).

| Key | Chart value | What it sets | Default |
|---|---|---|---|
| `LANGFUSE_NEXTAUTH_URL` | `langfuse.nextauth.url` | `NEXTAUTH_URL`: the URL people open | `http://localhost:3000` |
| `LANGFUSE_WEB_SERVICE_PORT` | `langfuse.web.service.port` | Langfuse's port on `127.0.0.1` | `3000` |
| `LANGFUSE_FEATURES_SIGN_UP_DISABLED` | `langfuse.features.signUpDisabled` | `AUTH_DISABLE_SIGNUP` | `true` |
| `LANGFUSE_FEATURES_TELEMETRY_ENABLED` | `langfuse.features.telemetryEnabled` | `TELEMETRY_ENABLED`: usage reports to Langfuse | `false` |
| `LANGFUSE_FEATURES_EXPERIMENTAL_FEATURES_ENABLED` | `langfuse.features.experimentalFeaturesEnabled` | `LANGFUSE_ENABLE_EXPERIMENTAL_FEATURES` | `false` |
| `LANGFUSE_LOGGING_LEVEL` | `langfuse.logging.level` | `LANGFUSE_LOG_LEVEL` | `info` |
| `LANGFUSE_LOGGING_FORMAT` | `langfuse.logging.format` | `LANGFUSE_LOG_FORMAT` | `text` |
| `LANGFUSE_INIT_ORG_ID` | `langfuse.additionalEnv` `LANGFUSE_INIT_ORG_ID` | the one organisation's id | `ortus-nova` |
| `LANGFUSE_INIT_ORG_NAME` | `langfuse.additionalEnv` `LANGFUSE_INIT_ORG_NAME` | its name | `Ortus-Nova` |
| `LANGFUSE_INIT_PROJECT_ID` | `langfuse.additionalEnv` `LANGFUSE_INIT_PROJECT_ID` | the one project's id | `agent-platform` |
| `LANGFUSE_INIT_PROJECT_NAME` | `langfuse.additionalEnv` `LANGFUSE_INIT_PROJECT_NAME` | its name | `agent-platform` |
| `LANGFUSE_INIT_PROJECT_PUBLIC_KEY` | `langfuse.additionalEnv` `LANGFUSE_INIT_PROJECT_PUBLIC_KEY` | the project's public key; also the collector's Basic-auth user | `pk-lf-<uuid>`, made by `init` |
| `LANGFUSE_INIT_USER_EMAIL` | `langfuse.additionalEnv` `LANGFUSE_INIT_USER_EMAIL` | the operator's user, owner of both | `operator@ortusnova.example` |
| `LANGFUSE_INIT_USER_NAME` | `langfuse.additionalEnv` `LANGFUSE_INIT_USER_NAME` | its name | `operator` |
| `POSTGRESQL_AUTH_USERNAME` | `postgresql.auth.username` | `POSTGRES_USER`, Langfuse's `DATABASE_USERNAME` | `langfuse` |
| `POSTGRESQL_AUTH_DATABASE` | `postgresql.auth.database` | `POSTGRES_DB`, Langfuse's `DATABASE_NAME` | `langfuse` |
| `CLICKHOUSE_AUTH_USERNAME` | `clickhouse.auth.username` | `CLICKHOUSE_USER` | `default` |
| `CLICKHOUSE_DATABASE` | `clickhouse.database` | `CLICKHOUSE_DB`; the database the backup holds | `default` |
| `REDIS_AUTH_USERNAME` | `redis.auth.username` | Langfuse's `REDIS_USERNAME` | `default` |
| `S3_BUCKET` | `s3.bucket` | the MinIO bucket, Langfuse's event and media bucket | `langfuse` |
| `S3_REGION` | `s3.region` | `LANGFUSE_S3_*_REGION` | `auto` |
| `S3_FORCE_PATH_STYLE` | `s3.forcePathStyle` | `LANGFUSE_S3_*_FORCE_PATH_STYLE` | `true` |
| `S3_AUTH_ROOT_USER` | `s3.auth.rootUser` | `MINIO_ROOT_USER`, and Langfuse's access key ID (`s3.accessKeyId`) | `langfuse` |
| `S3_EVENT_UPLOAD_PREFIX` | `s3.eventUpload.prefix` | `LANGFUSE_S3_EVENT_UPLOAD_PREFIX` | `events/` |
| `S3_MEDIA_UPLOAD_PREFIX` | `s3.mediaUpload.prefix` | `LANGFUSE_S3_MEDIA_UPLOAD_PREFIX` | `media/` |
| `S3_BATCH_EXPORT_ENABLED` | `s3.batchExport.enabled` | `LANGFUSE_S3_BATCH_EXPORT_ENABLED` | `false` |
| `COLLECTOR_PORTS_OTLP_HOST_PORT` | `collector.ports.otlp.hostPort` | the collector's OTLP/gRPC port on `127.0.0.1` | `4317` |
| `COLLECTOR_PORTS_OTLP_HTTP_HOST_PORT` | `collector.ports.otlp-http.hostPort` | its OTLP/HTTP port on `127.0.0.1` | `4318` |
| `COLLECTOR_CONFIG_EXPORTERS_OTLP_HTTP_LANGFUSE_HEADERS_X_LANGFUSE_INGESTION_VERSION` | `collector.config.exporters.otlp_http/langfuse.headers.x-langfuse-ingestion-version` | the header Langfuse's version needs | `4` |
| `COLLECTOR_CONFIG_EXPORTERS_FILE_ROTATION_MAX_MEGABYTES` | `collector.config.exporters.file/logs.rotation.max_megabytes` (and `file/metrics`) | the file store's rotation size | `100` |
| `TELEMETRY_RETENTION_DAYS` | none (the kit's own; Langfuse's data retention is Enterprise Edition); also `collector.config.exporters.file/*.rotation.max_days` | the age past which telemetry is deleted | `90` |
| `TELEMETRY_BACKUP_KEEP` | none (the kit's own) | the backups kept | `7` |

The collector chart's `config` value would hold `otelcol.yaml` as it is.

## Secrets

Every secret is a file in `$TELEMETRY_HOME/secrets` (mode 0600, the
directory 0700), made by `init` when it is missing and never replaced. `up`
turns each file into the podman secret `ortus-telemetry-<file>` (its
trailing newline dropped), which reaches the containers as a file in
`/run/secrets` or as one environment variable; no secret is a value in
`compose.yaml` or `telemetry.env`. Each file is named after the chart value
that holds it.

| File | Chart value | Reaches |
|---|---|---|
| `langfuse-salt` | `langfuse.salt.value` | `SALT` (web, worker): the API keys' hash |
| `langfuse-encryption-key` | `langfuse.encryptionKey.value` | `ENCRYPTION_KEY` (web, worker), 256 bits in hex |
| `langfuse-nextauth-secret` | `langfuse.nextauth.secret.value` | `NEXTAUTH_SECRET` (web) |
| `langfuse-init-project-secret-key` | `langfuse.additionalEnv` `LANGFUSE_INIT_PROJECT_SECRET_KEY` | the project's secret key (web); the collector's Basic-auth password |
| `langfuse-init-user-password` | `langfuse.additionalEnv` `LANGFUSE_INIT_USER_PASSWORD` | the operator's password (web) |
| `postgresql-auth-password` | `postgresql.auth.password` | `POSTGRES_PASSWORD_FILE`, Langfuse's `DATABASE_PASSWORD` |
| `clickhouse-auth-password` | `clickhouse.auth.password` | `CLICKHOUSE_PASSWORD` (ClickHouse, web, worker) |
| `redis-auth-password` | `redis.auth.password` | Valkey's `requirepass`, Langfuse's `REDIS_AUTH` |
| `s3-auth-root-password` | `s3.auth.rootPassword` | `MINIO_ROOT_PASSWORD`, and Langfuse's secret access key (`s3.secretAccessKey`) |
| `collector-ingest-token` | `collector.config.extensions.bearertokenauth/ingest.filename` | the collector's ingest token, read from `/run/secrets/ortus-telemetry-collector-ingest-token` |

Do not replace a file under a running store: PostgreSQL takes its password
only when its volume is new, and the
salt and the encryption key must stay what the data was written with. A
backup holds the files its data needs (below).

## Backups

```sh
control-plane/telemetry/telemetry.sh backup
```

writes `$TELEMETRY_BACKUP_DIR/<UTC time>/` (default
`$STAGE0_HOME/telemetry-backups`, mode 0700) with:

- `postgres.dump`: a `pg_dump -Fc` of Langfuse's database (users,
  organisation, project, keys);
- `clickhouse/`: a ClickHouse `BACKUP DATABASE` of `CLICKHOUSE_DATABASE`,
  which holds the trace tables (traces, observations, scores and Langfuse
  v4's events) and the record of Langfuse's migrations, written to the disk
  `backups` (`clickhouse-backups.xml`) and copied out;
- `minio/`: a copy of the bucket (`mc mirror`);
- `settings/`: `telemetry.env` and the secret files the data needs (the
  salt of the key hashes, the encryption key, the key pair).

A backup is written as `<time>.partial` and renamed when complete; the
newest `TELEMETRY_BACKUP_KEEP` (7) are kept and older ones removed. On p1,
`ortus-stage0-telemetry-backup.timer` runs it nightly at 02:30 (and at the
next start after a night p1 was off), and `stage0.sh` runs it
once when no backup exists. `~/.local/state/ortus-nova/logs/ortus-stage0-telemetry-backup.log`
shows each.

## Restore

```sh
control-plane/telemetry/telemetry.sh restore              # the newest backup
control-plane/telemetry/telemetry.sh restore DIR          # that one
```

It stops the project, removes its volumes, starts the stores alone,
`pg_restore`s PostgreSQL, `RESTORE`s the ClickHouse database, mirrors the
bucket back, then starts Langfuse and the collector and waits for health.
A secret file or `telemetry.env` that this host lacks comes from the
backup's `settings/`; the files the host has stay. Langfuse then serves the
traces, users and keys the backup held. Traces sent after the backup are
lost.

## Retention

Langfuse's data retention is an Enterprise Edition feature when self-hosted,
so the kit runs its own:

```sh
control-plane/telemetry/telemetry.sh retention
```

deletes the traces older than `TELEMETRY_RETENTION_DAYS` (90) through
Langfuse's public API (`retention.py`), and deletes each file of the file
store last written more than that many days ago. On p1,
`ortus-stage0-telemetry-retention.timer` runs it daily at 04:00.

**Why the public API, not a ClickHouse TTL.** The API selects by age:
`GET /api/public/v2/observations?toStartTime=<cutoff>` lists every
observation that started before the cutoff, with its trace. The retention
reads every page, then deletes those traces with `DELETE
/api/public/traces`, 1,000 at a time. Langfuse's worker then deletes each
trace's observations, scores and raw events from ClickHouse and the bucket,
within minutes. A TTL would delete rows in ClickHouse alone, behind
Langfuse's back, and leave the bucket's events; it would also be a change
to Langfuse's own schema, which its migrations own.

## The test

```sh
make -C control-plane/telemetry test       # the configuration, without podman (make test runs it)
make -C control-plane/telemetry live-test  # the project itself, under rootless podman (CI runs it)
```

`test-config.sh` checks the pins, the published ports, the secrets, that
this README maps every key, the collector's settings, the mask against each
token shape and ordinary text, `init`, and the retention against a fake
Langfuse. `test-live.sh` starts the project in a throwaway home, sends a
span with `sortie.*` attributes and token-shaped strings through the
collector, reads it back from Langfuse's API within a minute with the
session id set and every token `[redacted]`, signs in as the operator,
checks that sign-up is refused, that only the collector and Langfuse
publish a port and only on `127.0.0.1`, and that OTLP over HTTP and gRPC
needs the ingest token; writes log records and metrics to the file store;
runs the retention on a hundred-day-old trace; backs up, removes the
volumes, restores, and reads the span, the user and the keys again. It
refuses to run on a host with an `ortus-telemetry` project.

## Pin the images

Every image is pinned by digest. After changing a tag:

```sh
COMPOSE_FILE=control-plane/telemetry/compose.yaml control-plane/pin-images.sh
make -C control-plane pins       # lists the pins of both compose projects
```

A Langfuse upgrade moves `langfuse-web` and `langfuse-worker` together;
check its release notes for the ingestion version and for the ClickHouse
and PostgreSQL versions it supports.
