# chora-delivery

Delivery service for Chora: courses, offerings, classes, enrolment,
attendance, assessments and submissions, exams, course content, media signing,
and the learner-progress projections.

The service is standalone and cloud-neutral: it uses PostgreSQL, NATS JetStream,
and S3-compatible object storage, all configured through environment variables.
No cloud account or managed service (managed SQL, message broker, secret
manager, object store, or CI/CD) is required.

## Layout

- `cmd/server` — HTTP + gRPC service entrypoint
- `cmd/backfill-course-directory` — one-shot directory backfill utility
- `cmd/smoke-classroom` — classroom smoke tool
- `internal/domain` — pure domain logic
- `internal/adapter` — HTTP, gRPC, Postgres, event-bus, and object-store adapters
- `migrations` — forward-only SQL schema migrations

## Requirements

- Go 1.26+
- Docker with Docker Compose (for the local Postgres/NATS/MinIO stack)

## Configuration

```sh
cp .env.example .env
```

The checked-in `.env.example` carries the complete local defaults; `.env` is
ignored by Git.

Important variables:

| Variable | Purpose | Local default |
| --- | --- | --- |
| `PORT` | HTTP port | `8080` |
| `CHORA_GRPC_PORT` | gRPC port (`GRPC_PORT` accepted as an alias) | `9090` |
| `CHORA_DB_DSN` | PostgreSQL connection string (app_rw role) | Compose PostgreSQL |
| `CHORA_OUTBOX_DSN` | Durable outbox database | Same PostgreSQL instance |
| `NATS_URL` | NATS JetStream event bus | `nats://nats:4222` |
| `CHORA_SOURCE_PROJECT` | Event envelope source project | `chora-local` |
| `S3_ENDPOINT` / `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` | S3-compatible object store | MinIO |
| `COURSE_MEDIA_BUCKET` | Bucket for course-media uploads; empty disables the signer | `media` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC trace endpoint | `http://otel-collector:4317` |

## Run locally

```sh
go build ./cmd/server
CHORA_DB_DSN='postgres://chora_delivery_app_rw:chora@localhost:5432/chora_delivery?sslmode=disable' \
NATS_URL='nats://127.0.0.1:4222' \
PORT=8080 CHORA_GRPC_PORT=9090 \
./server
```

`/healthz` returns 200 once the process is listening; `/readyz` returns 200 only
after the Postgres pool pings and the outbox table is reachable.

## Database

PostgreSQL is the durable backing store, also used for the transactional outbox
and subscriber idempotency. Schema changes live in `migrations/` and are applied
by the platform migration runner (mounting this repository's `migrations/`).
Every forward migration is a `*.sql` file that is not a `*.down.sql`.

## Event bus

Local messaging uses NATS JetStream via
`github.com/apollo-chora/chora-common/eventbus`. The event taxonomy
(`chora.{domain}.{aggregate}.{event_type}.v{N}`) is unchanged from the previous
broker. Consumers are durable, created on demand, and dead-letter to
`_dlq.<subject>`. When `NATS_URL` is unset the service falls back to an
in-memory bus (not durable).

The `/api/internal/pubsub/*` HTTP receivers are retained as a local event-push
transport (`internal/adapter/eventpush`) so the gateway can forward deliveries
without any cloud-specific authentication.

## Testing

```sh
go test ./...
```

Repository tests that need a real database are build-tagged `integration` and
run only when `CHORA_TEST_DSN` is set:

```sh
CHORA_TEST_DSN='postgres://chora_delivery_app_rw:chora@localhost:5432/chora_delivery?sslmode=disable' \
  go test -tags=integration ./internal/adapter/repo/pg/...
```
