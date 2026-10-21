# chora-delivery

> The delivery side of learning: courses, offerings, enrolments, bookings, rosters,
> scheduling, assessments, exams, certifications, course content, and live classroom
> features.

Go, HTTP/JSON plus gRPC. PostgreSQL for durable state, NATS JetStream for events, Redis
for realtime fan-out, and S3-compatible storage for course media.

## What it is

This is the largest surface in the platform by feature count. It owns:

- **Courses and catalogues** — courses, offerings, search, course content, and the
  course-directory backfill tool.
- **Enrolment** — enrolments, bookings, rosters, waitlists, learner module progress.
- **Scheduling** — scheduled classes, rooms, campus operations, double-booking checks.
- **Assessment** — assessments, submissions, test sets, and the grading handoff.
- **Exams** — exam candidates, sittings, invigilators, incidents, and result webhooks.
- **Live classroom** — live quizzes, classroom sessions, and polls with WebSocket
  fan-out at `/api/v1/live-polls/{id}/ws`.
- **Certifications and credentials** — certifications, SkillsFuture claims,
  work-based learning and project groups.

It serves HTTP on `PORT` (default `8080`) and gRPC on `CHORA_GRPC_PORT` (default
`9090`, with `GRPC_PORT` accepted as an alias). In the stack both are `expose`d and
neither is published to the host.

### Two features are off in the stack, by configuration

Both are visible in the live logs and neither is a fault:

- **Course-media upload is disabled.** `compose.yml` sets no `COURSE_MEDIA_BUCKET` and
  no `S3_*` values for this service, so at boot it logs
  `CourseMediaSigner NOT wired — COURSE_MEDIA_BUCKET empty (POST .../content/upload-url will 503)`
  and the upload-URL route answers `503`. Signed-URL *resolution* still works; it is
  *upload* that is closed.
- **Realtime fan-out is pod-local.** With `CHORA_REDIS_ADDR` unset it logs
  `classroom realtime POD-LOCAL in-memory (single-pod only)`. That is correct for a
  single-container stack; it would break classroom fan-out across replicas.

## Prerequisites

| Route | What you need |
| --- | --- |
| In the stack | A working `chora-stack` deployment. PostgreSQL, NATS and the creation/identity/payments peers come from it. |
| Standalone (core) | Go **1.26.1 or newer**. Leaving `CHORA_DB_DSN` and `NATS_URL` unset gives in-memory repository and event-bus fallbacks. |
| Standalone (durable) | Plus PostgreSQL with this repository's schema, and NATS JetStream. |
| Course media | An S3-compatible bucket **and** `COURSE_MEDIA_BUCKET` set; without it upload-url `503`s. |
| Multi-pod realtime | Redis, via `CHORA_REDIS_ADDR`. Optional — without it realtime is single-pod. |

## Credentials

This service holds **no model-provider credentials** — it delegates grading and
generation to its peers. What it needs:

| Value | Where it comes from | Variable |
| --- | --- | --- |
| PostgreSQL password | the stack's `POSTGRES_PASSWORD`, carried into the DSN as `chora_delivery_app_rw` | `CHORA_DB_DSN`, `CHORA_OUTBOX_DSN` |
| NATS endpoint | the stack's NATS service; no credential locally | `NATS_URL` |
| Course-media credentials | **not configured in the stack**; you supply an endpoint, key pair and bucket when you want media upload | `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `COURSE_MEDIA_BUCKET` |
| Redis | **not configured in the stack**; only needed for multi-pod realtime | `CHORA_REDIS_ADDR` |
| Backfill tool DSN | same database, read-only in practice | `CHORA_DELIVERY_DSN` |

In the stack, course media and Redis are deliberately absent — see
[Two features are off in the stack](#two-features-are-off-in-the-stack-by-configuration).
If you want media upload, the stack's Garage instance is already there **and the bucket
already exists**: `garage-init` provisions a `media` bucket, which is the value
`.env.example` defaults `COURSE_MEDIA_BUCKET` to. To enable upload in the stack, add
four values to this service's environment:

```yaml
COURSE_MEDIA_BUCKET: media
S3_ENDPOINT: http://garage:3900
S3_ACCESS_KEY_ID: ${CHORA_S3_ACCESS_KEY_ID:-chora-dev}
S3_SECRET_ACCESS_KEY: ${CHORA_S3_SECRET_KEY:-chora-local-dev-1}
```

Nothing needs creating — the bucket is there and those credentials already write to it
from the other services. A different bucket name works only if you create that bucket
first.

Where the stack's shared credentials come from, and how to obtain the provider keys
the *other* services need, is documented once in
[`chora-stack/README.md`](../chora-stack/README.md#credentials).

## Run it in the stack

`compose.yml` does four things:

1. Builds the image from the sibling checkout at `../chora-delivery`.
2. Runs `delivery-migrate` first — a `postgres:18.6-alpine` one-shot applying this
   repository's migrations to `chora_delivery`.
3. Starts the service once `postgres` and `nats` are healthy, `nats-init` has
   completed, and the migrate one-shot completed successfully.
4. Publishes no host port; it `expose`s `8080` and `9090`.

```sh
cd chora-stack
docker compose up -d delivery
docker compose ps delivery
```

What Compose sets:

| Variable | Stack value |
| --- | --- |
| `CHORA_DB_DSN`, `CHORA_OUTBOX_DSN`, `CHORA_DELIVERY_DSN` | all three point at `chora_delivery` as `chora_delivery_app_rw` |
| `NATS_URL` | `nats://nats:4222` |
| `SVC_CREATION_GRPC_URL` | `creation:9090` |
| `CHORA_PAYMENTS_GRPC_ADDR` | `payments:9090` |
| `SVC_IDENTITY_HTTP_URL` | `http://identity:8080` |
| `CHORA_SOURCE_PROJECT` | `chora-local` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `otel-collector:4317` |

Three DSN variables all carry the same value here. `CHORA_DB_DSN` and
`CHORA_OUTBOX_DSN` are what the server reads; `CHORA_DELIVERY_DSN` is what the
backfill tool reads. Keeping them identical keeps the tool pointed at the same
database as the service.

Probe it from inside the network:

```sh
docker compose exec -T nats wget -qO- http://delivery:8080/readyz
```

## Run it standalone

```sh
git clone https://github.com/apollo-chora/chora-delivery.git
cd chora-delivery
cp .env.example .env
go build ./cmd/server
./server
```

Note that this builds to `./server` and runs it — the service does **not** read `.env`
itself. Export the file first if you want those values in the environment:

```sh
set -a; . ./.env; set +a
```

With `CHORA_DB_DSN` and `NATS_URL` unset, the repository-backed components fall back
to in-memory stores and an in-memory bus, which is the intended dependency-free mode —
everything is lost on restart and no event leaves the process.

Durable, against your own database:

```sh
CHORA_DB_DSN='postgres://chora:chora@127.0.0.1:5432/chora_delivery?sslmode=disable' \
NATS_URL='nats://127.0.0.1:4222' \
PORT=8080 CHORA_GRPC_PORT=9090 \
./server
```

Apply migrations first — the service does not migrate itself. Use the stack's
`scripts/migrate.sh` or any runner honouring the same conventions.

Tests and containers:

```sh
go test ./...

CHORA_TEST_DSN='postgres://chora:chora@127.0.0.1:5432/chora_delivery?sslmode=disable' \
  go test -tags=integration ./internal/adapter/repo/pg/...

docker build -t chora-delivery .
```

The production image uses a Go 1.26.6 Alpine builder and an Alpine 3.23 runtime — note
that `go.mod` declares 1.26.1 while the builder pins 1.26.6.

### Operator tools

Two one-shot binaries live in this repository and are not part of the stack:

```sh
# course-directory backfill — dry run by default
export CHORA_DELIVERY_DSN='postgres://...'
go run ./cmd/backfill-course-directory --tenants=<tenant-id>
go run ./cmd/backfill-course-directory --canary --tenants=<tenant-id>
go run ./cmd/backfill-course-directory --emit --canary-verified=<course-id> --tenants=<tenant-id>

# live-classroom smoke against a running service
go run ./cmd/smoke-classroom -a http://127.0.0.1:18081 \
  -tenant <uuid> -instructor <uuid> -learner <uuid>
```

The backfill defaults to a dry run and requires an explicit canary before it will emit;
it reads only the delivery database and tees
`chora.delivery.course.released.v1` events into the delivery outbox.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `CHORA_GRPC_PORT` | `9090` | gRPC listen port (`GRPC_PORT` also accepted) |
| `CHORA_DB_DSN` | unset | PostgreSQL DSN; unset selects in-memory repositories |
| `CHORA_DB_DSN_SECRET_ID` | unset | secret-resolved DSN |
| `CHORA_OUTBOX_DSN` | unset | PostgreSQL DSN for the outbox |
| `CHORA_OUTBOX_DSN_SECRET_ID` | unset | secret-resolved outbox DSN |
| `CHORA_OUTBOX_WORKER_ID` | `HOSTNAME`, else `chora-delivery-local` | outbox worker identity |
| `CHORA_BOOTSTRAP_TIMEOUT_SECONDS` | `30` | dependency bootstrap timeout |
| `NATS_URL` | unset | NATS JetStream URL; unset means the in-memory bus |
| `CHORA_SOURCE_PROJECT` | `chora-local` | event envelope source project |
| `CHORA_REDIS_ADDR` | unset | Redis for realtime; unset means pod-local in-memory |
| `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | unset | course-media object store |
| `COURSE_MEDIA_BUCKET` | unset in the stack (`.env.example` uses `media`) | course-media bucket; **empty disables upload-url** |
| `CHORA_DELIVERY_DSN` | unset | DSN read by `cmd/backfill-course-directory` |
| `SVC_CREATION_GRPC_URL` | unset (`.env.example`: `creation:9090`) | question snapshotter and media resolver |
| `SVC_IDENTITY_HTTP_URL` | unset (`.env.example`: `http://identity:8080`) | identity lookups |
| `CHORA_PAYMENTS_GRPC_ADDR` | unset (`.env.example`: `payments:9090`) | payments |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP/gRPC tracing endpoint |

## Verify it works

### It is up, and it says what it checked

```sh
docker compose exec -T nats wget -qO- http://delivery:8080/healthz
docker compose exec -T nats wget -qO- http://delivery:8080/readyz
```

```json
{"service":"chora-delivery","status":"ok","time":"..."}
{"outbox":"ok","pool":"ok","service":"chora-delivery","status":"ready"}
```

`readyz` is one of the few in the platform that reports something useful — `pool` and
`outbox` are checked, and either turning bad makes readiness fail. It still does not
prove the object store or the downstream peers, so keep reading.

### Its migrations landed

```sh
docker compose exec -T postgres psql -U chora_delivery_migrate -d chora_delivery \
  -c "select count(*) from schema_migrations;"
```

Expect a count at or just under the 60 forward files listed under
[Migrations](#migrations).

### The features that are configured off say so

```sh
docker compose logs delivery | grep -E 'CourseMediaSigner|CHORA_REDIS_ADDR' | tail -4
```

Two lines should appear, and both are expected in this stack:

```text
delivery: CourseMediaSigner NOT wired — COURSE_MEDIA_BUCKET empty (POST .../content/upload-url will 503)
delivery: WARNING CHORA_REDIS_ADDR unset — classroom realtime POD-LOCAL in-memory (single-pod only; ...)
```

If you intended to enable media upload and still see the first line, `COURSE_MEDIA_BUCKET`
did not reach the process.

### Its HTTP surface answers

```sh
docker compose exec -T nats wget -qO- -S \
  --header="X-Tenant-Id: 00000000-0000-7000-8000-000000000001" \
  http://delivery:8080/api/v1/offerings
```

A `2xx` with JSON is the pass. A `401`/`403` means the tenant and caller headers the
BFF normally stamps are missing; a connection error means the service is down.

## Troubleshooting

- **`POST .../content/upload-url` returns `503`** — `COURSE_MEDIA_BUCKET` is empty, so
  the signer was never wired. This is the stack's default. Set the bucket plus the
  three `S3_*` values to enable it.
- **Classroom realtime works on one pod and not across replicas** — `CHORA_REDIS_ADDR`
  is unset and fan-out is pod-local in-memory. Set it for multi-pod.
- **`readyz` reports `pool` or `outbox` as not ok** — the database or the outbox is
  unreachable. Since both point at the same DSN in the stack, suspect the database
  first, then the migrate one-shot.
- **Everything is lost on restart** — `CHORA_DB_DSN` and `NATS_URL` are both unset, so
  the service is on in-memory fallbacks.
- **Events are published but nothing consumes them** — `NATS_URL` is unset, so the bus
  is in-process. The `/api/internal/pubsub/*` HTTP receivers are a local compatibility
  transport, not the canonical path.
- **A consumer keeps redelivering** — JetStream consumers here use at-least-once with
  durable consumers, five delivery attempts, and `_dlq.<subject>` dead-letter
  subjects. A message that exhausts its attempts lands there rather than looping.
- **`go run ./cmd/backfill-course-directory` does nothing** — it defaults to a dry run.
  Use `--canary`, then `--emit --canary-verified=<course-id>`.
- **The service starts with no configuration** — `.env` was not exported. The binary
  does not read the file.
- **`GRPC_PORT` has no effect** — `CHORA_GRPC_PORT` is the primary name; `GRPC_PORT` is
  only an alias, so a conflicting pair leaves the outcome ambiguous. Set one.
- **Traces never reach Tempo** — every service in this stack logs
  `invalid target address http://otel-collector:4317 ... too many colons in address`,
  so spans are dropped. Observed in the running stack; the fix belongs to the shared
  tracing initialiser. Only tracing is affected.

## API surface

Two HTTP route families plus legacy routes, and a gRPC service.

| Group | Prefix | Examples |
| --- | --- | --- |
| Tenant-scoped delivery API | `/api/v1`, `/v1` | offerings, search, assessments, exams, live quizzes, classroom sessions, live polls |
| Legacy handlers | `/api/courses`, `/api/bookings`, `/api/certifications`, `/courses`, `/enrollments`, `/me/enrollments` | retained where wired |
| Internal event push | `/api/internal/pubsub/*` | local compatibility transport |
| Health | `/healthz`, `/readyz` | liveness and readiness |

Major surfaces:

| Area | Paths |
| --- | --- |
| Courses and enrolments | `/v1/courses`, `/v1/me/enrolments`, `/api/v1/instructors/{instructor_gcid}/courses` |
| Offerings and search | `/api/v1/offerings`, `/api/v1/search/offerings` |
| Assessments | `/api/v1/assessments`, `/api/v1/me/assessments` |
| Live classroom | `/api/v1/live-quizzes`, `/api/v1/classroom-sessions` |
| Live polls | `/api/v1/live-polls`, WebSocket at `/api/v1/live-polls/{id}/ws` |
| Course media | `/v1/me/courses/{course_id}/content/media-urls` |

Plus scheduling, rooms, campus operations, rosters, bookings, certifications, exams
(candidates, sittings, invigilators, incidents, result webhooks), work-based learning,
project groups, SkillsFuture claims, credentials, applications, test sets and learner
module progress. Requests carry tenant and caller identity from the request context;
the BFF gateway stamps it in the normal topology.

## Architecture

```text
cmd/server/                       HTTP and gRPC bootstrap and wiring
cmd/backfill-course-directory/    course-directory event backfill (dry-run by default)
cmd/smoke-classroom/              live-classroom acceptance smoke
internal/domain/                  domain models, ports, business rules
internal/adapter/http/            HTTP handlers and routing
internal/adapter/grpc/            generated-contract gRPC adapter
internal/adapter/repo/            PostgreSQL and in-memory repositories
internal/adapter/events/          event publishers and subscribers
internal/adapter/eventpush/       HTTP event-push compatibility transport
internal/adapter/objectmedia/     course-media object storage
internal/adapter/outbox/          transactional outbox
internal/adapter/realtime/        Redis-backed realtime fan-out
config/                           runtime config, e.g. PII_Closure_Map.yaml
migrations/                       forward-only PostgreSQL schema
```

The two adapters that matter for an operator are `objectmedia` and `realtime`, because
they are the two that degrade quietly rather than failing: an unset bucket closes
upload, and an unset Redis address narrows fan-out to one pod. Both announce themselves
at boot.

## Events

Events go through a PostgreSQL transactional outbox and are then published to NATS
JetStream.

| Concern | Setting |
| --- | --- |
| Outbox store | `CHORA_OUTBOX_DSN` (same database as the service in the stack) |
| Dispatcher identity | `CHORA_OUTBOX_WORKER_ID` |
| Broker | `NATS_URL`; unset falls back to the in-memory bus |
| Delivery guarantee | at-least-once, durable consumers |
| Retry ceiling | five delivery attempts |
| Dead letters | `_dlq.<subject>` |
| Source label | `CHORA_SOURCE_PROJECT` |

The `/api/internal/pubsub/*` HTTP receivers are a local compatibility transport, not
the canonical bus. The course-directory backfill tool writes into the same outbox, so
its events flow out through the same dispatcher.

## Migrations

**60 forward migration files**, applied by the stack's `scripts/migrate.sh` in the
`delivery-migrate` one-shot against `chora_delivery`. The runner is not part of this
repository.

The set runs from `0001_initial.sql`, `0002_applications.sql` and `0003_outbox.sql`
through `0060_uuidv7_pk_defaults.up.sql` and `9999_grant_app_roles.sql`, and covers
courses, offerings, enrolments, bookings, scheduling, assessments, exams,
certifications, live classroom, and the outbox. Migrations here are **forward-only** —
the convention is documented in the repository rather than relying on rollback files.

Two files carry requirements worth knowing:

- **`0060_uuidv7_pk_defaults`** switches primary-key defaults to PostgreSQL 18's
  native `uuidv7()`. The stack's `pgvector/pgvector:0.8.7-pg18` satisfies it; an older
  server fails the migrate one-shot.
- **`9999_grant_app_roles.sql`** carries the grants for the `_app_rw` role. Without it
  the service connects but every query fails on permissions.

Where `.down.sql` siblings exist they are destructive rollbacks and are never applied:
the runner takes every `*.sql` except `*.down.sql`, records a SHA-256 checksum per file
in `schema_migrations`, and refuses a file whose contents changed after application.
