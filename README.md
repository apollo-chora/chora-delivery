# chora-delivery

## About

chora-delivery is the Go service that owns Chora's delivery-side learning data and workflows, including courses, offerings, enrolments, bookings, rosters, scheduling, assessments, exams, certifications, course content, and live classroom features. It exposes HTTP/JSON and gRPC APIs, with PostgreSQL for durable state, NATS JetStream for events, Redis for realtime fan-out, and S3-compatible object storage for course media. The repository is standalone and resolves its shared Chora dependencies through Go modules.

## Quick start

Prerequisites:

- Go 1.26.1 or newer
- A running PostgreSQL instance when using durable repositories
- NATS JetStream for durable event delivery
- An S3-compatible object store for course-media signing
- Redis is optional; the service has an in-process fallback for realtime fan-out

For local development, start the service without external dependencies for the repository-backed components by leaving `CHORA_DB_DSN` and `NATS_URL` unset. The service then uses its in-memory repository and event-bus fallbacks.

```sh
git clone https://github.com/apollo-chora/chora-delivery.git
cd chora-delivery

cp .env.example .env

go build ./cmd/server
./server
```

The checked-in `.env.example` contains local defaults for PostgreSQL, NATS, MinIO/S3, tracing, downstream services, and Redis. To run against a PostgreSQL database and NATS, set at least:

```sh
CHORA_DB_DSN='postgres://chora:chora@127.0.0.1:5432/chora_delivery?sslmode=disable' \
NATS_URL='nats://127.0.0.1:4222' \
PORT=8080 CHORA_GRPC_PORT=9090 \
./server
```

The Docker image builds from the repository root:

```sh
docker build -t chora-delivery .
```

## Usage

The main process listens on HTTP `8080` by default and gRPC `9090` by default. Override them with `PORT` and `CHORA_GRPC_PORT`; `GRPC_PORT` is also accepted as a gRPC-port alias.

Health endpoints:

- `GET /healthz` reports that the process is listening.
- `GET /readyz` reports readiness after the configured PostgreSQL pool and outbox are reachable.

The HTTP API includes tenant-scoped delivery operations under `/api/v1` and `/v1`. The service also retains legacy `/api/courses`, `/api/bookings`, `/api/certifications`, `/courses`, `/enrollments`, and `/me/enrollments` routes where the corresponding handlers are wired.

Major HTTP surfaces include:

- Courses and enrolments: `/v1/courses`, `/v1/me/enrolments`, `/api/v1/instructors/{instructor_gcid}/courses`
- Offerings and search: `/api/v1/offerings`, `/api/v1/search/offerings`
- Scheduling, rooms, campus operations, rosters, bookings, and certifications
- Assessments and submissions: `/api/v1/assessments`, `/api/v1/me/assessments`
- Exams, candidates, sittings, invigilators, incidents, and exam-result webhooks
- Live quizzes and classroom sessions: `/api/v1/live-quizzes`, `/api/v1/classroom-sessions`
- Live polls: `/api/v1/live-polls`, including WebSocket fan-out at `/api/v1/live-polls/{id}/ws`
- Course media signed URLs: `/v1/me/courses/{course_id}/content/media-urls`
- Work-based learning, project groups, SkillsFuture claims, credentials, applications, test sets, and learner module progress

The HTTP event-push receivers under `/api/internal/pubsub/*` are retained as a local compatibility transport. The canonical event bus is NATS JetStream when `NATS_URL` is set; when it is unset, the service uses an in-memory bus that is not durable across restarts. JetStream consumers use at-least-once delivery with durable consumers, five delivery attempts, and `_dlq.<subject>` dead-letter subjects.

Key environment variables:

| Variable | Purpose | Default |
| --- | --- | --- |
| `PORT` | HTTP listen port | `8080` |
| `CHORA_GRPC_PORT` | gRPC listen port | `9090` |
| `CHORA_DB_DSN` | PostgreSQL DSN | unset |
| `CHORA_OUTBOX_DSN` | PostgreSQL DSN for the transactional outbox | unset |
| `CHORA_OUTBOX_WORKER_ID` | Outbox worker identity | `HOSTNAME` or `chora-delivery-local` |
| `NATS_URL` | NATS JetStream URL | unset |
| `CHORA_SOURCE_PROJECT` | Event envelope source project | `chora-local` |
| `S3_ENDPOINT` | S3-compatible endpoint | unset |
| `S3_ACCESS_KEY_ID` | S3 access key | unset |
| `S3_SECRET_ACCESS_KEY` | S3 secret key | unset |
| `COURSE_MEDIA_BUCKET` | Course-media bucket | `media` in `.env.example` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC tracing endpoint | unset |
| `SVC_CREATION_GRPC_URL` | chora-creation gRPC address | `creation:9090` in `.env.example` |
| `SVC_IDENTITY_HTTP_URL` | chora-identity HTTP URL | `http://identity:8080` in `.env.example` |
| `CHORA_PAYMENTS_GRPC_ADDR` | chora-payments gRPC address | `payments:9090` in `.env.example` |
| `CHORA_REDIS_ADDR` | Redis realtime address | unset |

For PostgreSQL-backed startup, `CHORA_DB_DSN` may be supplied directly or resolved with `CHORA_DB_DSN_SECRET_ID` through the environment-backed Chora secrets client. The outbox follows the same pattern with `CHORA_OUTBOX_DSN` or `CHORA_OUTBOX_DSN_SECRET_ID`. `CHORA_BOOTSTRAP_TIMEOUT_SECONDS` controls dependency bootstrap timeout and defaults to 30 seconds.

The one-shot course-directory backfill tool reads only the delivery database and tees `chora.delivery.course.released.v1` events into the delivery outbox. It defaults to a dry run and requires an explicit canary before a sweep:

```sh
export CHORA_DELIVERY_DSN='postgres://...'

go run ./cmd/backfill-course-directory --tenants=<tenant-id>
go run ./cmd/backfill-course-directory --canary --tenants=<tenant-id>
go run ./cmd/backfill-course-directory --emit --canary-verified=<course-id> --tenants=<tenant-id>
```

The classroom smoke tool targets a running service and exercises the live-quiz, WebSocket, and Redis realtime path:

```sh
go run ./cmd/smoke-classroom \
  -a http://127.0.0.1:18081 \
  -tenant <uuid> \
  -instructor <uuid> \
  -learner <uuid>
```

Pass `-b http://127.0.0.1:18082` to add the cross-pod probes after port-forwarding two running pods.

## Development

The repository is a Go module named `github.com/apollo-chora/chora-delivery` and uses Go 1.26.1. Build the HTTP/gRPC server with:

```sh
go build ./cmd/server
```

Run the full test suite with:

```sh
go test ./...
```

Integration tests that exercise a real PostgreSQL database use the `integration` build tag and require `CHORA_TEST_DSN`:

```sh
CHORA_TEST_DSN='postgres://chora:chora@127.0.0.1:5432/chora_delivery?sslmode=disable' \
  go test -tags=integration ./internal/adapter/repo/pg/...
```

The main source tree is organized by responsibility:

- `cmd/server`: HTTP and gRPC service bootstrap and dependency wiring
- `cmd/backfill-course-directory`: manual course-directory event backfill
- `cmd/smoke-classroom`: live classroom acceptance smoke
- `internal/domain`: domain models, ports, and business rules
- `internal/adapter/http`: HTTP handlers and routing
- `internal/adapter/grpc`: generated-contract gRPC service adapter
- `internal/adapter/repo`: PostgreSQL and in-memory repositories
- `internal/adapter/events`: event publishers and subscribers
- `internal/adapter/eventpush`: HTTP event-push compatibility transport
- `internal/adapter/objectmedia`: course-media object storage integration
- `internal/adapter/outbox`: transactional outbox implementation
- `internal/adapter/realtime`: Redis-backed realtime fan-out
- `config`: runtime configuration files such as `PII_Closure_Map.yaml`
- `migrations`: forward-only PostgreSQL schema migrations

The production container is built by the root `Dockerfile` with a Go 1.26.6 Alpine builder and Alpine 3.23 runtime. GitHub Actions publishes multi-architecture Docker images for `linux/amd64` and `linux/arm64` on pushes to `main` and version tags.
