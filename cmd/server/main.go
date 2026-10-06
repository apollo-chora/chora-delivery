// Package main wires the chora-delivery Go service.
//
// This is the standalone, cloud-neutral build: PostgreSQL repositories, a NATS
// JetStream event bus (chora-common/eventbus), OTLP tracing via
// OTEL_EXPORTER_OTLP_ENDPOINT, and S3-compatible object storage
// (chora-common/objectstore). The former Google Cloud dependencies (Pub/Sub,
// Cloud Trace, Secret Manager, Cloud Storage + IAM SignBlob, Cloud
// Build/Deploy) have been removed.
//
// Hexagonal layout assembled here:
//
//	internal/domain/delivery   (pure domain — Course/Class/Booking/Roster/Cert)
//	internal/adapter/inmem     (in-memory repos)
//	internal/adapter/http      (HTTP/JSON adapter)
//	internal/observability     (log + traceparent shim)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	// pgx stdlib driver — registered for sql.Open("pgx", dsn) used by
	// the per-domain outbox PostgresStore in bootstrap.go.
	_ "github.com/jackc/pgx/v5/stdlib"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthgrpc "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/apollo-chora/chora-common/durabilityguard"
	"github.com/apollo-chora/chora-common/eventbus"
	choraserver "github.com/apollo-chora/chora-common/http"
	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/objectstore"
	cgcsecrets "github.com/apollo-chora/chora-common/secrets"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/delivery/v1"
	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/payments/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	deliveryclients "github.com/apollo-chora/chora-delivery/internal/adapter/clients"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	deliverygrpc "github.com/apollo-chora/chora-delivery/internal/adapter/grpc"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/invoice"
	coursemedia "github.com/apollo-chora/chora-delivery/internal/adapter/objectmedia"
	deliveryoutbox "github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
	paymentsAdapter "github.com/apollo-chora/chora-delivery/internal/adapter/payments"
	deliveryrealtime "github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	deliverypg "github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/adapter/singpass"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
	"github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
	"github.com/apollo-chora/chora-delivery/internal/domain/credential"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
	offeringattendance "github.com/apollo-chora/chora-delivery/internal/domain/offering_attendance"
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
	"github.com/apollo-chora/chora-delivery/internal/domain/project_group"
	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
	"github.com/apollo-chora/chora-delivery/internal/domain/skillsfutures"
	"github.com/apollo-chora/chora-delivery/internal/domain/survey"
	"github.com/apollo-chora/chora-delivery/internal/domain/wbl"
	"github.com/apollo-chora/chora-delivery/internal/observability"
)

// splitCSV trims + filters a comma-separated env-var into a slice.
func splitCSV(s string) []string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

const (
	serviceName    = "chora-delivery"
	serviceVersion = "0.1.0"

	// defaultSourceProject — fallback for CHORA_SOURCE_PROJECT; the local
	// stack stamps every event with this source project.
	defaultSourceProject = "chora-local"
)

// simple comment
func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Source project + service for event envelopes — sourced from env per
	// .claude/rules/ddd-enforcement.md "no inline config". CHORA_SOURCE_PROJECT
	// is canonical; SOURCE_PROJECT is accepted as a legacy alias.
	sourceProject := os.Getenv("CHORA_SOURCE_PROJECT")
	if sourceProject == "" {
		sourceProject = os.Getenv("SOURCE_PROJECT")
	}
	if sourceProject == "" {
		sourceProject = defaultSourceProject
	}

	// ----------------------------------------------------------------------
	// pgxpool + event-bus wiring (Wave-B service-wiring per
	// feedback_resilience_priority).
	//
	// ctx is cancelled on SIGINT/SIGTERM so the outbox dispatcher goroutine
	// (started below) exits cleanly.
	// ----------------------------------------------------------------------
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// OTLP wiring — emits real spans via the OTLP exporter configured from
	// OTEL_EXPORTER_OTLP_ENDPOINT (stdout fallback when unset).
	//
	// Per C(a).S1 path (b) — tracker #151 — OTLP init runs in its own
	// goroutine with its own (env-tunable, default 15s) deadline + fail-
	// soft semantics. Timeout / init-error degrade to a no-op shutdown,
	// so the rest of bootstrap (pgx pool, event bus) gets the FULL
	// CHORA_BOOTSTRAP_TIMEOUT_SECONDS budget.
	otlpHandle := observability.InitAsync(ctx)

	pool, poolShutdown := bootstrapDBPool(ctx)
	if poolShutdown != nil {
		defer poolShutdown()
	}
	jetBus, busShutdown := bootstrapBus(ctx)
	if busShutdown != nil {
		defer busShutdown()
	}
	// The outbox dispatcher publishes to the JetStream bus when wired;
	// otherwise an in-memory bus (in-process events; not durable).
	var outboxBus deliveryoutbox.Bus = eventbus.NewInMemoryBus()
	if jetBus != nil {
		outboxBus = jetBus
		log.Printf("delivery: NATS JetStream event bus wired (url=%s)", os.Getenv("NATS_URL"))
	} else {
		log.Printf("delivery: in-memory event bus wired (NATS_URL unset; NOT durable across restart)")
	}

	// bindEventbus registers a durable consumer for one subject on the
	// JetStream bus. No-op without a bus (dev/in-memory). Consumer names are
	// unique per subject so two legs of the same subscriber cannot collide on a
	// shared durable name.
	bindEventbus := func(name, subject string, h eventbus.Handler) {
		if jetBus == nil {
			return
		}
		go func() {
			if err := jetBus.Subscribe(ctx, consumerConfig(name, subject), h); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("delivery: eventbus subscriber %s (%s) exited: %v", name, subject, err)
			}
		}()
	}

	// ----------------------------------------------------------------------
	// Public course catalogue — DB-backed in production, in-memory in dev.
	//
	// Per the Phyllis Step 5 directive, the public course catalogue is NOT
	// a stub: it is backed by the real chora_delivery Postgres database
	// (the `courses` table + the `public_courses_catalog` cross-tenant view
	// from migrations/0006) and SURVIVES POD RESTARTS. The in-memory
	// domain.Catalogue is dev-only (no pgx pool wired).
	//
	// Seed data lives in chora-infra/seed/phyllis/04_delivery.sql (applied
	// by chora-infra/scripts/seed-phyllis-demo.sh) — NEVER hardcoded in this
	// binary. The previous inline seedPhyllisCatalogue() + the CSM-Prep /
	// PMP-Crash-Course inline blocks have been removed.
	var catalogue domain.CataloguePort
	if txr := newPgxTxRunner(pool); txr != nil {
		catalogue = deliverypg.NewCatalogueRepo(txr)
		log.Printf("delivery: catalogue wired to pg.CatalogueRepo (chora_delivery-backed, durable across restart)")
	} else {
		catalogue = domain.NewInMemCatalogue()
		log.Printf("delivery: catalogue wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// Enrollments — DB-backed in production (CJ#2 Stripe path requires
	// durable Enrolment rows so GET /me/enrolments returns the row after
	// pod restart + PaymentsSubscriber writes survive). Fix for the Bug-2
	// path surfaced during CJ#2 last-gap diagnosis 2026-05-26 — before
	// this branch the wire was unconditionally in-memory, and the dead
	// course_enrollments schema in migrations/0001_initial.sql never got
	// a row even on successful payment.
	//
	// In-memory fallback preserves dev / unit-test wiring when CHORA_DB_DSN
	// is unset (matches catalogue + testSets fallback pattern above).
	var enrollments domain.EnrollmentPort
	if txr := newPgxTxRunner(pool); txr != nil {
		enrollments = deliverypg.NewEnrollmentRepo(txr)
		log.Printf("delivery: enrollments wired to pg.EnrollmentRepo (chora_delivery.course_enrollments, durable across restart)")
	} else {
		enrollments = domain.NewInMemEnrollmentStore()
		log.Printf("delivery: enrollments wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// User directory (Q3 name projection) — pg.UserDirectoryRepo
	// (chora_delivery.user_directory, tenant-agnostic global GCID → display_name,
	// migration 0040) when a pool is available; in-mem fallback for dev / unit
	// tests. Fed by the chora.identity.user.profile_updated.v1 push subscriber
	// + stitched onto the course roster READ VIEW. No RLS (global key).
	var userDirectory directory.UserDirectoryPort
	if txr := newPgxTxRunner(pool); txr != nil {
		userDirectory = deliverypg.NewUserDirectoryRepo(txr)
		log.Printf("delivery: user_directory wired to pg.UserDirectoryRepo (chora_delivery.user_directory, durable across restart)")
	} else {
		userDirectory = directory.NewInMemUserDirectory()
		log.Printf("delivery: user_directory wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// Bookings — pg.BookingRepo (chora_delivery.bookings, RLS-isolated +
	// listable + durable across pod restart) when a pool is available; in-mem
	// fallback for dev / unit tests. HANDOFF_RPLUS §6 follow-up: before this,
	// bookings were ephemeral and had no GET-list (FE session-tracked them).
	var bookings domain.BookingPort
	if txr := newPgxTxRunner(pool); txr != nil {
		bookings = deliverypg.NewBookingRepo(txr)
		log.Printf("delivery: bookings wired to pg.BookingRepo (chora_delivery.bookings, durable across restart)")
	} else {
		bookings = inmem.NewBookingRepo()
		log.Printf("delivery: bookings wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// Exams — pg.ExamRepo (chora_delivery.exams, RLS-isolated + durable) when
	// a pool is available; in-mem fallback for dev / unit tests. R+ durability
	// sweep (CHO-1580): before this, exams were ephemeral (lost on pod restart).
	var exams exam.ExamStore
	if txr := newPgxTxRunner(pool); txr != nil {
		exams = deliverypg.NewExamRepo(txr)
		log.Printf("delivery: exams wired to pg.ExamRepo (chora_delivery.exams, durable across restart)")
	} else {
		exams = inmem.NewExamRepo()
		log.Printf("delivery: exams wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// W4 Brick-1 (ADR-190 D2) — Exam BC ExamForm + ExamResult (chora_delivery.
	// exam_forms / exam_results, migration 0046, RLS-isolated + durable).
	var examForms exam.ExamFormStore
	var examResults exam.ExamResultStore
	if txr := newPgxTxRunner(pool); txr != nil {
		examForms = deliverypg.NewExamFormRepo(txr)
		examResults = deliverypg.NewExamResultRepo(txr)
		log.Printf("delivery: exam forms/results wired to pg (exam_forms/exam_results, durable)")
	} else {
		examForms = inmem.NewExamFormRepo()
		examResults = inmem.NewExamResultRepo()
		log.Printf("delivery: exam forms/results wired to in-memory store (NOT durable)")
	}

	// W5 FRANCHISE bypass-free slice (ADR-192 D1 + ADR-193 D1, CHO-2230):
	// franchise_satellite mapping store (mig 0056) + inbound satellite exam
	// webhook dedup gate (mig 0057, RLS-disabled). Both migrations are
	// COMMITTED-NOT-APPLIED until the W5 deploy burst runs the tracked
	// migration runner; until then a pg-wired receiver fails LOUD (42P01) on
	// first use, which is the honest state.
	var franchiseSatellites franchise.Store
	var examWebhookEvents examwebhook.Repo
	if txr := newPgxTxRunner(pool); txr != nil {
		franchiseSatellites = deliverypg.NewFranchiseSatelliteRepo(txr)
		examWebhookEvents = deliverypg.NewExamWebhookEventRepo(txr)
		log.Printf("delivery: franchise satellites + exam webhook dedup wired to pg (franchise_satellite/exam_webhook_events, durable)")
	} else {
		franchiseSatellites = inmem.NewFranchiseSatelliteRepo()
		examWebhookEvents = inmem.NewExamWebhookEventRepo()
		log.Printf("delivery: franchise satellites + exam webhook dedup wired to in-memory store (CHORA_DB_DSN unset; NOT durable)")
	}

	// Inbound HMAC shared secret (ADR-193 D1). Env-sourced only, the value
	// itself lives in Secret Manager and reaches the pod as a k8s secret env
	// (no inline config). Empty means the receiver 503s fail-loud on every
	// delivery: no trust anchor, no processing (the payments precedent).
	examWebhookSecret := strings.TrimSpace(os.Getenv("CHORA_EXAM_WEBHOOK_SECRET"))
	if examWebhookSecret == "" {
		log.Printf("delivery: WARNING: CHORA_EXAM_WEBHOOK_SECRET not set: POST %s will 503", httpapi.ExamWebhookPath)
	} else {
		log.Printf("delivery: CHORA_EXAM_WEBHOOK_SECRET loaded (%d chars)", len(examWebhookSecret))
	}

	// W4 Brick-3 (ADR-190 D2) — Exam BC candidate admission (chora_delivery.
	// exam_candidates, migration 0047, RLS-isolated + durable). Claims is the
	// Identity verification-claim port; no service-to-service by-GCID endpoint
	// exists yet ⇒ Claims nil ⇒ verify/admit are DARK (501), allocate/list live.
	var candidates exam.CandidateStore
	if txr := newPgxTxRunner(pool); txr != nil {
		candidates = deliverypg.NewCandidateRepo(txr)
		log.Printf("delivery: exam candidates wired to pg.CandidateRepo (exam_candidates, mig 0047)")
	} else {
		candidates = inmem.NewCandidateRepo()
		log.Printf("delivery: exam candidates wired to in-memory store (NOT durable)")
	}
	// W4 follow-up A (ADR-190 D2) — real Identity verification-claim adapter.
	// Identity URL from env (SVC_IDENTITY_HTTP_URL); unset ⇒ Claims nil ⇒
	// verify/admit stay DARK (501), never a fake (no-stubs).
	var claims exam.VerificationClaimReader
	identityHTTPURL := strings.TrimSpace(os.Getenv("SVC_IDENTITY_HTTP_URL"))
	if identityHTTPURL == "" {
		log.Printf("delivery: SVC_IDENTITY_HTTP_URL unset — exam verify/admit remain DARK (501)")
	} else {
		claims = deliveryclients.NewVerificationClient(identityHTTPURL, nil)
		log.Printf("delivery: exam candidate verification-claim reader wired to %s (HTTP mesh)", identityHTTPURL)
	}
	examCandidateDeps := &httpapi.ExamCandidateDeps{Candidates: candidates, Claims: claims}

	// W4 follow-up B (ADR-190 D2 + ADR-191) — operational sitting aggregates.
	var examSittings exam.ExamSittingStore
	var examInvigilators exam.ExamInvigilatorStore
	var examIncidents exam.IncidentReportStore
	if txr := newPgxTxRunner(pool); txr != nil {
		examSittings = deliverypg.NewSittingRepo(txr)
		examInvigilators = deliverypg.NewInvigilatorRepo(txr)
		examIncidents = deliverypg.NewIncidentRepo(txr)
		log.Printf("delivery: exam sittings/invigilators/incidents wired to pg (mig 0048, durable)")
	} else {
		examSittings = inmem.NewSittingRepo()
		examInvigilators = inmem.NewInvigilatorRepo()
		examIncidents = inmem.NewIncidentRepo()
		log.Printf("delivery: exam sittings/invigilators/incidents wired to in-memory store (NOT durable)")
	}
	examSittingDeps := &httpapi.ExamSittingDeps{
		Sittings: examSittings, Invigilators: examInvigilators, Incidents: examIncidents,
	}

	// Offerings — pg.OfferingRepo (chora_delivery.offerings, RLS-isolated +
	// durable) when a pool is available; in-mem fallback for dev / unit tests.
	// R+ four-mode W1 (ADR-190, CHO-1849): the delivery_type-bearing instance.
	var offerings domain.OfferingPort
	if txr := newPgxTxRunner(pool); txr != nil {
		offerings = deliverypg.NewOfferingRepo(txr)
		log.Printf("delivery: offerings wired to pg.OfferingRepo (chora_delivery.offerings, durable across restart)")
	} else {
		offerings = inmem.NewOfferingRepo()
		log.Printf("delivery: offerings wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// R+ Phase-2 W7 (WS-A) — Module course-structure aggregate: groups a course's
	// flat course_content items into ordered modules (migration 0039). pg.ModuleRepo
	// (chora_delivery.course_modules(+_items), durable + RLS-isolated) when a pool
	// is available; in-mem fallback for dev / unit tests.
	var modules module.ModulePort
	if txr := newPgxTxRunner(pool); txr != nil {
		modules = deliverypg.NewModuleRepo(txr)
		log.Printf("delivery: modules wired to pg.ModuleRepo (chora_delivery.course_modules, durable across restart)")
	} else {
		modules = module.NewInMemModuleStore()
		log.Printf("delivery: modules wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// R+ durability sweep (CHO-1580) — Wbl / ProjectGroups / SkillsFutures, the
	// siblings of Exams. pg.<X>Repo (chora_delivery JSONB-snapshot tables, RLS-
	// isolated + durable) when a pool is available; in-mem fallback for dev.
	var wblStore wbl.WblStore
	if txr := newPgxTxRunner(pool); txr != nil {
		wblStore = deliverypg.NewWblRepo(txr)
		log.Printf("delivery: wbl-placements wired to pg.WblRepo (chora_delivery.wbl_placements, durable across restart)")
	} else {
		wblStore = inmem.NewWblRepo()
		log.Printf("delivery: wbl-placements wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	var projectGroups project_group.ProjectGroupStore
	if txr := newPgxTxRunner(pool); txr != nil {
		projectGroups = deliverypg.NewProjectGroupRepo(txr)
		log.Printf("delivery: project-groups wired to pg.ProjectGroupRepo (chora_delivery.project_groups, durable across restart)")
	} else {
		projectGroups = inmem.NewProjectGroupRepo()
		log.Printf("delivery: project-groups wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	var skillsFutures skillsfutures.SkillsFuturesStore
	if txr := newPgxTxRunner(pool); txr != nil {
		skillsFutures = deliverypg.NewSkillsFuturesRepo(txr)
		log.Printf("delivery: skillsfutures-claims wired to pg.SkillsFuturesRepo (chora_delivery.skillsfutures_claims, durable across restart)")
	} else {
		skillsFutures = inmem.NewSkillsFuturesRepo()
		log.Printf("delivery: skillsfutures-claims wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// Lane A (B-FE-X5) — test-set authoring surface (A+ X.2). Wire pg.TestSetRepo
	// when a pool is available; fall back to the in-memory store in dev.
	var testSets httpapi.TestSetPort
	if txr := newPgxTxRunner(pool); txr != nil {
		testSets = deliverypg.NewTestSetRepo(txr)
		log.Printf("delivery: test-sets wired to pg.TestSetRepo (chora_delivery-backed)")
	} else {
		testSets = httpapi.NewInMemTestSetStore()
		log.Printf("delivery: test-sets wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// ----------------------------------------------------------------------
	// D6.2 producer-side outbox wiring (M12.3 W1c + W1.5c bootstrap).
	//
	// Per `feedback_d6_resilience_first_class` + `agentic-resilience-d6`
	// skill Pillar 2. Inner publisher mints envelopes + payloads (existing
	// idempotency-key + IMDA tagging logic preserved); TransactionalOutbox-
	// Publisher tees each emitted event into outbox_events; Dispatcher
	// drains to the NATS JetStream event bus on a background goroutine.
	//
	// Fallback ladder (same as chora-creation W1.5a):
	//   1. pool nil                   → innerPub direct (dev)
	//   2. pool set + outboxDB nil    → outbox.InMemoryStore (dev w/ pool)
	//   3. pool set + outboxDB set    → outbox.PostgresStore (prod)
	// Dispatcher goroutine only starts when the event bus + outboxDB are both
	// wired. Final synchronous DrainOnce on shutdown flushes in-flight rows.
	// ----------------------------------------------------------------------
	innerPub := events.NewInMemoryPublisher(sourceProject, serviceName)
	var publisher events.Publisher = innerPub
	var outboxStore deliveryoutbox.Store
	var outboxDispatcher *deliveryoutbox.Dispatcher
	var dispatcherDone chan struct{}

	outboxDB, outboxDBShutdown := bootstrapOutboxDB(ctx)
	if outboxDBShutdown != nil {
		defer outboxDBShutdown()
	}

	if pool != nil {
		if outboxDB != nil {
			outboxStore = deliveryoutbox.NewPostgresStore(
				sqlDBAdapter{db: outboxDB},
				deliveryoutbox.PostgresStoreOptions{WorkerID: outboxWorkerID()},
			)
			log.Printf("delivery: outbox PostgresStore wired (worker_id=%s)", outboxWorkerID())
		} else {
			outboxStore = deliveryoutbox.NewInMemoryStore()
			log.Printf("delivery: outbox InMemoryStore wired (CHORA_OUTBOX_DSN unset; NOT durable across restart)")
		}
		publisher = deliveryoutbox.NewTransactionalPublisher(deliveryoutbox.PublisherConfig{
			Inner:         innerPub,
			Store:         outboxStore,
			SourceProject: sourceProject,
			SourceService: serviceName,
		})

		if jetBus != nil && outboxDB != nil {
			outboxDispatcher = deliveryoutbox.NewDispatcher(deliveryoutbox.DispatcherConfig{
				Store:    outboxStore,
				Bus:      outboxBus,
				WorkerID: outboxWorkerID(),
			})
			dispatcherDone = make(chan struct{})
			go func() {
				defer close(dispatcherDone)
				if err := outboxDispatcher.Run(ctx, 100); err != nil &&
					!errors.Is(err, context.Canceled) &&
					!errors.Is(err, context.DeadlineExceeded) {
					log.Printf("delivery: outbox dispatcher exited: %v", err)
				}
			}()
			log.Printf("delivery: outbox dispatcher goroutine started (batch=100)")
		} else if jetBus == nil {
			log.Printf("delivery: NATS_URL unset — outbox dispatcher NOT started; rows accumulate")
		}
	}

	// Federated closure-saga subscriber (CHO-1719 / Tier 3 D11): consumes
	// chora.delivery.pii.pseudonymise.requested.v1, applies the per-domain
	// PII_Closure_Map.yaml duty, and acks on
	// chora.delivery.account.pseudonymised.v1.
	//
	// Repo seam (CHO-2198, W0-F1 durability + W0-F5 error-honesty): pg on a
	// healthy pool (durable ack/dedup — migration 0049,
	// closure_pseudonymisation_state), in-memory ONLY when the pool is
	// absent, mirroring the chora-payments / chora-notifications else-branch
	// shape. The repo is gated on the event bus being wired AND pool health.
	// Real per-table pg tokenisation (actually redacting certifications /
	// bookings / ... columns) remains separate, deeper debt — this is
	// durability of the ack/dedup SIGNAL only, not the redaction itself. The
	// subscription is a durable NATS JetStream consumer; override the name via
	// env.
	// closureRepo is hoisted to function scope so the ADR-236 D5 durability
	// guard (below, after Deps) can classify it alongside the other repos. It
	// stays nil when the event bus is absent (closure subscriber unwired) — the
	// guard reports nil as UNKNOWN, never a violation.
	var closureRepo events.ClosureRepository
	if jetBus != nil {
		piiPath := os.Getenv("CHORA_PII_CLOSURE_MAP_PATH")
		if piiPath == "" {
			piiPath = "config/PII_Closure_Map.yaml"
		}
		closureAckPub := eventbus.NewClosureAckPublisher(jetBus, sourceProject, "chora-delivery")
		if txr := newPgxTxRunner(pool); txr != nil {
			closureRepo = deliverypg.NewClosureRepository(txr)
			log.Printf("delivery: pg ClosureRepository wired (table=closure_pseudonymisation_state)")
		} else {
			closureRepo = events.NewInMemoryClosureRepo()
			log.Printf("delivery: CHORA_DB_DSN unset — closure repo uses in-memory store (NOT durable across restart)")
		}
		if closureSub, err := events.BootstrapClosureSubscriber(piiPath, closureRepo, closureAckPub, nil); err != nil {
			log.Printf("delivery: closure subscriber DISABLED (PII map load: %v)", err)
		} else {
			closureSubName := os.Getenv("CHORA_CLOSURE_SUBSCRIPTION")
			if closureSubName == "" {
				closureSubName = "chora-delivery.closure-pseudonymise"
			}
			go func() {
				log.Printf("delivery: closure subscriber binding %s -> %s", closureSubName, events.TopicPseudonymiseRequested)
				if err := jetBus.Subscribe(ctx, consumerConfig(closureSubName, events.TopicPseudonymiseRequested), events.ClosurePullHandler(closureSub)); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("delivery: closure subscriber exited: %v", err)
				}
			}()
		}
	}

	// ----------------------------------------------------------------------
	// E2E-INFRA-COLD-START §B — canonical /readyz + /healthz handlers.
	//
	// Per docs/m13/E2E-INFRA-COLD-START-be-svc-ack-2026-05-16.md (Infra
	// option 1 accepted at user prompt 2026-05-16). The canonical handler
	// from libs/chora-go-common/http enforces strict ready vs live
	// semantic:
	//
	//   /healthz — 200 once HTTP server is listening (process alive).
	//   /readyz  — 200 ONLY after pgxpool.Ping succeeds AND outbox table
	//             is reachable. 503 otherwise. Gates EndpointSlice
	//             publication.
	//
	// Fixes INFRA-LEG3-D race where the HTTP server starts listening
	// before pool warm-up finishes; old /readyz returned 200 immediately,
	// EndpointSlice flipped pod Ready, first inbound request failed.
	// ----------------------------------------------------------------------
	var readyzOutbox choraserver.OutboxChecker
	if outboxDB != nil {
		readyzOutbox = choraserver.NewSQLOutboxChecker(outboxDB, "outbox_events")
	}
	readyzHandler := choraserver.ReadyzHandler(choraserver.ReadyzDeps{
		ServiceName: serviceName,
		Pool:        pool, // *pgxpool.Pool satisfies choraserver.Pinger
		Outbox:      readyzOutbox,
		// CheckTimeout intentionally left as default (2s) — tuned for
		// Cloud SQL Auth Proxy cold start headroom.
	})
	healthzHandler := choraserver.HealthzHandler(choraserver.HealthzDeps{
		ServiceName: serviceName,
	})
	log.Printf("delivery: /readyz wired to canonical chora-go-common handler (pool=%t outbox=%t)",
		pool != nil, readyzOutbox != nil)

	// NOTE: the catalogue is NO LONGER seeded inline — the public course
	// catalogue is DB-backed (chora_delivery), seeded durably via
	// chora-infra/seed/phyllis/04_delivery.sql.
	//
	// CHO-2293: Campus is no longer an in-memory Registry seeded with a demo
	// row. It is wired below (next to the other pool-gated stores) onto
	// chora_delivery.campuses, and seedDemoCampus is retired: a hardcoded fake
	// campus that resurrected on every boot is not seed data, it is a lie about
	// what the tenant owns.

	// S6.1 — Course Application stack wiring.
	// All adapter config sourced from env vars per .claude/skills/secrets-and-env.
	singpassRedirect := os.Getenv("SINGPASS_REDIRECT_URL")
	invoiceBucket := os.Getenv("INVOICE_BUCKET")
	if invoiceBucket == "" {
		invoiceBucket = "chora-invoices-dev"
	}

	// ----------------------------------------------------------------------
	// chora-payments PaymentService gRPC client — ADR-164 Stage C
	// (2026-05-24).
	//
	// Per `feedback_no_inline_config`: upstream URL sourced from
	// CHORA_PAYMENTS_GRPC_ADDR. Per `feedback_no_stubs_real_wiring`: we
	// FAIL LOUD when the env var is unset — chora-delivery cannot mint a
	// Stripe Checkout Session without the canonical chora-payments
	// upstream. Defaults to the in-mesh payments:9090 endpoint.
	//
	// mTLS sidecar (Cloud Service Mesh) terminates TLS so the dial uses
	// plain insecure credentials inside the mesh. The scoped
	// AuthorizationPolicy on chora-payments constrains which sources can
	// reach the PaymentService RPC.
	paymentsGRPCAddr := strings.TrimSpace(os.Getenv("CHORA_PAYMENTS_GRPC_ADDR"))
	if paymentsGRPCAddr == "" {
		paymentsGRPCAddr = "payments:9090"
	}
	paymentsConn, paymentsDialErr := grpc.NewClient(
		paymentsGRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if paymentsDialErr != nil {
		log.Fatalf("delivery: payments gRPC NewClient %s: %v",
			paymentsGRPCAddr, paymentsDialErr)
	}
	defer func() {
		if cerr := paymentsConn.Close(); cerr != nil {
			log.Printf("delivery: closing payments grpc conn: %v", cerr)
		}
	}()
	paymentsClient := paymentsAdapter.NewClient(paymentsv1.NewPaymentServiceClient(paymentsConn))
	log.Printf("delivery: payments gRPC client wired to %s", paymentsGRPCAddr)

	// Singpass client (stub until A-Singpass S6.3 lands the real NDI client).
	singClient := singpass.NewStubClient(singpass.Config{
		RedirectURL: singpassRedirect,
	})

	// Invoice issuer — in-memory storage for local; GCS adapter lands at S6.2.
	pdfGen := invoice.NewPDFGenerator()
	pdfStore := invoice.NewInMemoryStorage(invoiceBucket)
	issuer := invoice.NewIssuer(pdfGen, pdfStore, invoiceBucket)

	// ----------------------------------------------------------------------
	// Fix-E (ADR-155 Lane B completion-event subscriber) wiring.
	//
	// chora-delivery subscribes to chora.delivery.grading.oe_batch_completed.v1
	// (emitted by chora-ai-kernel-orchestrator after the oe_grader Vertex
	// engine returns). The push subscription delivers HTTP POST to
	// /api/internal/pubsub/grading-inbox; the handler decodes + dispatches
	// into the GradingInboxSubscriber which calls Submission.ApplyOEGradingBatch.
	//
	// SubmissionRepo: pg.SubmissionRepo when CHORA_DB_DSN set; in-mem fallback
	// for dev. Per feedback_no_stubs_real_wiring we do NOT short-circuit a
	// missing pool — local dev with the in-mem store is the documented
	// fallback. Pod-survival is via the pg path.
	// ----------------------------------------------------------------------
	var submissionRepo domain.SubmissionRepo
	if txr := newPgxTxRunner(pool); txr != nil {
		submissionRepo = deliverypg.NewSubmissionRepo(txr)
	} else {
		submissionRepo = domain.NewInMemSubmissionRepo()
		log.Printf("delivery: submission repo wired to in-memory store (CHORA_DB_DSN unset)")
	}
	// Local event-push verifier. The cloud-neutral build ships no bundled
	// token validator, so verification is a no-op unless an operator wires a
	// ValidateTokenFunc; the mesh/network layer is expected to restrict
	// /api/internal/* in production.
	pushVerifier := eventpush.NewVerifier(eventpush.VerifierConfig{
		Audience:           strings.TrimSpace(os.Getenv("CHORA_PUBSUB_PUSH_AUDIENCE")),
		TrustedServiceAccs: splitCSV(os.Getenv("CHORA_PUBSUB_PUSH_TRUSTED_SA")),
	})
	gradingSubscriber := subscribers.NewGradingInboxSubscriber(
		submissionRepo, publisher, nil, // inbox MemoryStore fallback; pg-backed inbox lands in Wave-E follow-up
	)
	gradingPushHandler := httpapi.NewGradingInboxPushHandler(httpapi.GradingInboxPushDeps{
		Subscriber: gradingSubscriber,
		Verifier:   pushVerifier,
	})
	log.Printf("delivery: pub/sub push wired: POST /api/internal/pubsub/grading-inbox (chora.delivery.grading.oe_batch_completed.v1)")
	bindEventbus("chora-delivery.grading-inbox.oe_batch_completed", subscribers.TopicGradingOEBatchCompleted, httpapi.GradingInboxEventbusHandler(gradingSubscriber))
	bindEventbus("chora-delivery.grading-inbox.submission_completed", subscribers.TopicGradingSubmissionCompleted, httpapi.GradingInboxEventbusHandler(gradingSubscriber))

	// ----------------------------------------------------------------------
	// ADR-164 Stage C — PaymentsSubscriber + push handler wiring.
	//
	// Per `feedback_agentic_pubsub_only` + Tier 2 D6 (inter-domain comms via
	// Pub/Sub). Wires the 4 canonical chora.payments.* subscriber handlers
	// + the HTTP push endpoint POST /api/internal/pubsub/payments-inbox.
	//
	// Idempotency store: Postgres-backed when CHORA_DB_DSN is set (uses the
	// outbox-side *sql.DB so the dedup table lives in chora_delivery DB);
	// MemoryStore fallback in dev. Per `feedback_no_stubs_real_wiring` the
	// in-mem path is dev-only.
	//
	// applicationsRepo is hoisted here so both the HTTP layer (Deps below)
	// + the subscriber share the same backing store. The HTTP path
	// transitions Submitted → ... → Accepted; the payment-captured subscriber
	// subsequently advances Accepted → Paid → Enrolled — under pg they share
	// durable chora_delivery.applications state (the subscriber re-Gets by id
	// + tenant), so a pod restart between accept-offer and payment-capture no
	// longer drops the application. R+ durability sweep Wave 2 (CHO-1580):
	// before this, applications were inmem-only (ephemeral) even though
	// pg.ApplicationRepo + migration 0002_applications.sql already existed.
	var applicationsRepo application.ApplicationPort
	if txr := newPgxTxRunner(pool); txr != nil {
		applicationsRepo = deliverypg.NewApplicationRepo(txr)
		log.Printf("delivery: applications wired to pg.ApplicationRepo (chora_delivery.applications + application_state_history, durable across restart)")
	} else {
		applicationsRepo = repoinmem.NewApplicationRepo()
		log.Printf("delivery: applications wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}
	enrollTracker := events.NewEnrollmentTracker(enrollments)
	var paymentsInbox idempotent.Store
	if outboxDB != nil {
		paymentsInbox = idempotent.NewPostgresStore(idempotentSQLDBAdapter{db: outboxDB})
		log.Printf("delivery: payments-inbox idempotency wired to pg.PostgresStore (chora_delivery.idempotency_keys)")
	} else {
		paymentsInbox = idempotent.NewMemoryStore()
		log.Printf("delivery: payments-inbox idempotency wired to in-memory store (CHORA_OUTBOX_DSN unset; NOT durable across restart)")
	}
	paymentsSubscriber := events.NewPaymentsSubscriber(events.PaymentsSubscriberDeps{
		Enrollments:  enrollTracker,
		Applications: applicationsRepo,
		Publisher:    publisher,
		Inbox:        paymentsInbox,
	})
	paymentsPushHandler := httpapi.NewPaymentsPushHandler(httpapi.PaymentsPushDeps{
		Subscriber: paymentsSubscriber,
		Verifier:   pushVerifier,
	})
	log.Printf("delivery: pub/sub push wired: POST /api/internal/pubsub/payments-inbox (4 chora.payments.* topics)")
	bindEventbus("chora-delivery.payments-inbox.course_purchase_payment_captured", events.TopicCoursePurchasePaymentCaptured, httpapi.PaymentsInboxEventbusHandler(paymentsSubscriber))
	bindEventbus("chora-delivery.payments-inbox.course_purchase_refunded", events.TopicCoursePurchaseRefunded, httpapi.PaymentsInboxEventbusHandler(paymentsSubscriber))
	bindEventbus("chora-delivery.payments-inbox.application_payment_payment_captured", events.TopicApplicationPaymentPaymentCaptured, httpapi.PaymentsInboxEventbusHandler(paymentsSubscriber))
	bindEventbus("chora-delivery.payments-inbox.application_payment_refunded", events.TopicApplicationPaymentRefunded, httpapi.PaymentsInboxEventbusHandler(paymentsSubscriber))

	// ----------------------------------------------------------------------
	// Q3 name projection — IdentityProfileSubscriber + push handler.
	//
	// Consumes chora.identity.user.profile_updated.v1 and upserts the
	// (gcid → display_name) row into chora_delivery.user_directory. Shares the
	// same idempotency store as the payments inbox (chora_delivery.idempotency_keys
	// — keys are topic-namespaced, so no collision). JSON on the wire: the
	// envelope rides in Pub/Sub attributes, the payload in the message data.
	identityProfileSubscriber := events.NewIdentityProfileSubscriber(events.IdentityProfileSubscriberDeps{
		Directory: userDirectory,
		Inbox:     paymentsInbox,
	})
	identityProfilePushHandler := httpapi.NewIdentityProfilePushHandler(httpapi.IdentityProfilePushDeps{
		Subscriber: identityProfileSubscriber,
		Verifier:   pushVerifier,
	})
	log.Printf("delivery: pub/sub push wired: POST /api/internal/pubsub/identity-profile-inbox (chora.identity.user.profile_updated.v1)")
	bindEventbus("chora-delivery.identity-profile-inbox", events.TopicIdentityUserProfileUpdated, httpapi.IdentityProfileInboxEventbusHandler(identityProfileSubscriber))

	// ----------------------------------------------------------------------
	// W7 StudentModuleProgress projection (CHO-2074) — per-learner, per-module
	// completion. Two push inboxes feed one projector:
	//   chora.consumption.atom_session.completed.v1 → atom-kind module items
	//   chora.delivery.submission.graded.v1         → assessment-kind module items
	// The projector resolves ENROLLED module targets (course_content_items ⋈
	// course_module_items ⋈ course_modules ⋈ course_enrollments, intra-DB) then
	// folds the completion into student_module_progress (0045). DB-gated: the
	// resolver is a SQL join, so there is no in-mem fallback — unwired without a
	// pool (an honest DB requirement, not a stubbed no-op). Shares the pg
	// idempotency store (paymentsInbox) with topic-namespaced keys.
	// ----------------------------------------------------------------------
	var moduleProgressAtomPushHandler, moduleProgressGradedPushHandler http.Handler
	var moduleProgressPort moduleprogress.ProgressPort
	if txr := newPgxTxRunner(pool); txr != nil {
		moduleProgressPort = deliverypg.NewProgressRepo(txr, deliverypg.ProgressRepoOptions{
			SourceProject: sourceProject,
			SourceService: serviceName,
		})
		moduleProgressProjector := moduleprogress.NewProjector(
			deliverypg.NewProgressResolver(txr), modules, moduleProgressPort,
		)
		moduleProgressSubscriber := subscribers.NewModuleProgressInboxSubscriber(moduleProgressProjector, paymentsInbox)
		moduleProgressAtomPushHandler = httpapi.NewModuleProgressAtomPushHandler(httpapi.ModuleProgressPushDeps{
			Subscriber: moduleProgressSubscriber,
			Verifier:   pushVerifier,
		})
		moduleProgressGradedPushHandler = httpapi.NewModuleProgressGradedPushHandler(httpapi.ModuleProgressPushDeps{
			Subscriber: moduleProgressSubscriber,
			Verifier:   pushVerifier,
		})
		log.Printf("delivery: pub/sub push wired: POST /api/internal/pubsub/module-progress-{atom,graded}-inbox (W7 StudentModuleProgress, CHO-2074)")
		bindEventbus("chora-delivery.module-progress-atom-inbox", subscribers.TopicAtomSessionCompleted, httpapi.ModuleProgressAtomInboxEventbusHandler(moduleProgressSubscriber))
		bindEventbus("chora-delivery.module-progress-graded-inbox", subscribers.TopicSubmissionGraded, httpapi.ModuleProgressGradedInboxEventbusHandler(moduleProgressSubscriber))
	} else {
		log.Printf("delivery: module-progress projection NOT wired (CHORA_DB_DSN unset; enrolment-gated resolver requires pg)")
	}

	// ----------------------------------------------------------------------
	// ASYNC-mode analytics: the CourseLearnerProgress projection (CHO-1827,
	// R+ Four-Mode DoD §10.3 step 3 "R+ Analytics reflects the enrolment/progress").
	//
	// chora-consumption owns learner progress and a cross-DB query is FORBIDDEN, so
	// its learning_path.{advanced,completed}.v1 are the bridge. Two single-topic
	// push endpoints feed one projection (course_learner_progress, migration 0054),
	// which the offering Analytics tab rolls up into avg_progress_pct +
	// completion_rate.
	//
	// DB-gated: the projection is an RLS-scoped SQL upsert under a row lock, so
	// there is no in-mem fallback. Staying unwired without a pool is an honest DB
	// requirement, not a stubbed no-op. Shares the pg idempotency store
	// (paymentsInbox) with topic-namespaced keys.
	// ----------------------------------------------------------------------
	var courseProgressAdvancedPushHandler, courseProgressCompletedPushHandler http.Handler
	var courseProgressPort courseprogress.ProgressPort
	if txr := newPgxTxRunner(pool); txr != nil {
		courseProgressPort = deliverypg.NewCourseProgressRepo(txr)
		courseProgressSubscriber := subscribers.NewCourseProgressSubscriber(courseProgressPort, paymentsInbox)
		courseProgressAdvancedPushHandler = httpapi.NewCourseProgressAdvancedPushHandler(httpapi.CourseProgressPushDeps{
			Subscriber: courseProgressSubscriber,
			Verifier:   pushVerifier,
		})
		courseProgressCompletedPushHandler = httpapi.NewCourseProgressCompletedPushHandler(httpapi.CourseProgressPushDeps{
			Subscriber: courseProgressSubscriber,
			Verifier:   pushVerifier,
		})
		log.Printf("delivery: pub/sub push wired: POST /api/internal/pubsub/course-progress-{advanced,completed}-inbox ← %v (ASYNC analytics, CHO-1827)",
			courseProgressSubscriber.SubscribedTopics())
		bindEventbus("chora-delivery.course-progress-advanced-inbox", subscribers.TopicLearningPathAdvanced, httpapi.CourseProgressAdvancedInboxEventbusHandler(courseProgressSubscriber))
		bindEventbus("chora-delivery.course-progress-completed-inbox", subscribers.TopicLearningPathCompleted, httpapi.CourseProgressCompletedInboxEventbusHandler(courseProgressSubscriber))
	} else {
		log.Printf("delivery: course-progress projection NOT wired (CHORA_DB_DSN unset; the RLS-scoped projection requires pg); the offering Analytics tab will 503 rather than imply nobody is learning")
	}

	// ----------------------------------------------------------------------
	// ----------------------------------------------------------------------
	// Lane B (ADR-155) — AssessmentDeps wiring (companion to Fix-F).
	//
	// Assessment + Submission repos: pg.{Assessment,Submission}Repo when
	// CHORA_DB_DSN set; in-mem fallback for dev. Submission repo reuses the
	// same instance Fix-E wired for the grading-inbox subscriber.
	//
	// Fix-F: TestSets carried through so the /submit handler can resolve
	// the parent test-set's payload_snapshot column for deterministic MCQ
	// grading (per `feedback_no_stubs_real_wiring`).
	// ----------------------------------------------------------------------
	var assessmentRepo domain.AssessmentRepo
	// cohortRoster resolves assessment cohort authz facts — offering enrolment
	// (course_enrollments ⋈ offerings) + class booking (bookings), all in
	// chora_delivery (CHO-2153 / ADR-234). Without it, an assessment whose
	// cohort mode needs roster facts is REFUSED, not admitted: the handlers
	// surface domain.ErrCohortRosterUnavailable as a 500 rather than falling
	// back to "everyone is eligible", which is the defect this replaces.
	var cohortRoster domain.CohortRoster
	if txr := newPgxTxRunner(pool); txr != nil {
		assessmentRepo = deliverypg.NewAssessmentRepo(txr)
		cohortRoster = deliverypg.NewCohortRosterRepo(txr)
		log.Printf("delivery: assessment repo wired to pg.AssessmentRepo")
		log.Printf("delivery: cohort roster wired to pg.CohortRosterRepo (offering enrolment + class bookings)")
	} else {
		inmemAssessments := domain.NewInMemAssessmentRepo()
		inmemRoster := domain.NewInMemCohortRoster()
		inmemAssessments.SetRosterLink(inmemRoster)
		assessmentRepo = inmemAssessments
		cohortRoster = inmemRoster
		log.Printf("delivery: assessment repo wired to in-memory store (CHORA_DB_DSN unset)")
	}
	// WS-6 (CHO-1958): give the grading-inbox subscriber the assessment reader
	// so OE/essay-graded submissions also snapshot assessment_title onto
	// submission.graded.v1 (the MCQ fast path already does via the loaded
	// Assessment). Attached post-construction — the subscriber + its push
	// handler are wired above, before assessmentRepo exists; WithAssessmentReader
	// mutates in place, and the server only starts serving after all wiring.
	gradingSubscriber.WithAssessmentReader(assessmentRepo)
	// CHO-2224 (§10.6 criterion 1): give the same subscriber the Offering port so
	// OE/essay-graded submissions also snapshot the parent Offering's
	// delivery_type onto submission.graded.v1 — chora-consumption's only
	// mode-bearing signal for the unified transcript. Same post-construction
	// attach as the reader above; `offerings` is wired far earlier.
	gradingSubscriber.WithOfferings(offerings)

	assessmentDeps := &httpapi.AssessmentDeps{
		Assessments:     assessmentRepo,
		Submissions:     submissionRepo,
		OutboxPublisher: publisher,
		TestSets:        testSets,
		Roster:          cohortRoster,
		// CHO-2224: the HTTP emit funnel (MCQ fast path + instructor approve /
		// approve-all) resolves delivery_type through this port.
		Offerings: offerings,
		// CHO-2343: the R+ grading-queue LEARNER column resolves learner GCID →
		// display name through the SAME chora_delivery.user_directory projection
		// CHO-2335 wires for WBL (intra-domain read-model, no cross-DB query).
		LearnerDirectory: userDirectory,
	}
	log.Printf("delivery: assessment routes wired (11 endpoints: instructor + learner)")

	// Fix-F (Lane A snapshot debt close) — QuestionSnapshotter wiring.
	//
	// At TestSet.PublishWithSnapshot() time chora-delivery makes a sync gRPC
	// call to chora-creation's Creation/SnapshotQuestionByID RPC to fetch
	// the canonical MCQ + OE payload, then stores it inline on
	// chora_delivery.test_set_questions.payload_snapshot (per migrations/
	// 0011_test_set_questions_snapshot.up.sql). The runtime grading path at
	// /submit reads the snapshot from chora_delivery only — never touches
	// chora_creation per ddd-enforcement #3.
	//
	// Per feedback_no_inline_config: the upstream URL is sourced from
	// SVC_CREATION_GRPC_URL (default chora-creation:8081 in the mesh).
	// ----------------------------------------------------------------------
	var questionSnapshotter domain.QuestionSnapshotter
	creationGRPCURL := strings.TrimSpace(os.Getenv("SVC_CREATION_GRPC_URL"))
	if creationGRPCURL == "" {
		// EXPLICITLY-UNSET fallback path: chora-creation hasn't implemented
		// the SnapshotQuestionByID gRPC server side yet (follow-up debt).
		// When the env is unset, leave QuestionSnapshotter nil — Publish
		// falls back to vanilla DRAFT→PUBLISHED without snapshot capture,
		// AND /submit grading fails loud per ErrMCQSnapshotMissing (per
		// `feedback_no_stubs_real_wiring`). Setting SVC_CREATION_GRPC_URL
		// to any non-empty value engages the gRPC path.
		log.Printf("delivery: SVC_CREATION_GRPC_URL unset — QuestionSnapshotter NIL; Publish will SKIP snapshots, /submit grading will fail loud per ErrMCQSnapshotMissing")
	} else {
		creationConn, dialErr := grpc.NewClient(
			creationGRPCURL,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if dialErr == nil {
			qc := deliveryclients.NewQuestionClient(
				creationv1.NewCreationClient(creationConn),
			)
			questionSnapshotter = qc
			// OT#4 — the same gRPC client resolves durable gs:// image refs into
			// fresh signed GET URLs at learner read-time (MintAtomMediaDownloadURL).
			assessmentDeps.MediaResolver = qc
			log.Printf("delivery: question snapshotter + media resolver wired to %s (gRPC)", creationGRPCURL)
			defer func() {
				if cerr := creationConn.Close(); cerr != nil {
					log.Printf("delivery: closing creation grpc conn: %v", cerr)
				}
			}()
		} else {
			log.Printf("delivery: SVC_CREATION_GRPC_URL=%s dial failed (%v); QuestionSnapshotter nil", creationGRPCURL, dialErr)
		}
	}

	// ----------------------------------------------------------------------
	// Lane 1c W4 (CHO-1703 / ADR-180 D10) — BatchTestSetSubscriber wiring.
	//
	// chora-delivery subscribes to chora.creation.question_batch.accepted.v1
	// (BINARY, emitted by chora-creation's outbox when an author accepts a
	// batch with the "Create test set" toggle ON). The push subscription
	// chora-delivery.creation-question_batch-accepted delivers HTTP POST to
	// /api/internal/pubsub/batch-testset-inbox; the handler decodes via the
	// protodecode registry + dispatches into BatchTestSetSubscriber, which
	// assembles ONE DRAFT test set per job (idempotent on
	// test_sets.source_job_id, migration 0028) and re-emits the existing
	// chora.delivery.test_set.created.v1 via the outbox publisher.
	//
	// Store: the SAME testSets port the HTTP layer uses (pg.TestSetRepo in
	// production — RLS via the aggregate's tenant; InMemTestSetStore in dev).
	// Snapshotter: the SAME questionSnapshotter Fix-F wired (nil ⇒ existence
	// validation skipped, dev-only). Inbox: Postgres-backed dedup when the
	// outbox DB is up (mirrors payments-inbox), MemoryStore in dev — the
	// 0028 UNIQUE partial index is the durable backstop either way.
	// ----------------------------------------------------------------------
	var batchTestSetInbox idempotent.Store
	if outboxDB != nil {
		batchTestSetInbox = idempotent.NewPostgresStore(idempotentSQLDBAdapter{db: outboxDB})
		log.Printf("delivery: batch-testset-inbox idempotency wired to pg.PostgresStore (chora_delivery.idempotency_keys)")
	} else {
		batchTestSetInbox = idempotent.NewMemoryStore()
		log.Printf("delivery: batch-testset-inbox idempotency wired to in-memory store (CHORA_OUTBOX_DSN unset; NOT durable across restart)")
	}
	batchTestSetSubscriber := subscribers.NewBatchTestSetSubscriber(
		testSets, questionSnapshotter, publisher, batchTestSetInbox,
	)
	batchTestSetPushHandler := httpapi.NewBatchTestSetInboxPushHandler(httpapi.BatchTestSetInboxPushDeps{
		Subscriber: batchTestSetSubscriber,
		Verifier:   pushVerifier,
	})
	log.Printf("delivery: pub/sub push wired: POST /api/internal/pubsub/batch-testset-inbox (chora.creation.question_batch.accepted.v1)")
	bindEventbus("chora-delivery.batch-testset-inbox", subscribers.TopicQuestionBatchAccepted, httpapi.BatchTestSetInboxEventbusHandler(batchTestSetSubscriber))

	// Repos hoisted out of the Deps literal so the gRPC adapter shares the
	// same in-memory state as the HTTP path (mirrors the chora-identity
	// ManaService pattern — see services/chora-identity/cmd/server/main.go
	// lines 660-690 for the canonical Wave-1 gRPC registration template).
	//
	// ADR-236 D1 — courseRepo was UNCONDITIONALLY in-memory here (no pool
	// gate, no boot log — the only wiring in this file with neither), even
	// though it serves the live POST/GET /api/courses HTTP routes (phyllis
	// composite-create) AND the gRPC CreateCourse RPC. Every course created
	// through those paths died on pod restart and, on multi-pod GKE, was
	// invisible to sibling pods. pg.CourseRepo (durable adapter for the same
	// `courses` table) already existed with the exact Save/Get/ListByTenant
	// shape but was dead code — only its CJ#2 sibling methods were wired
	// (courseCJ2Port below). Pool-gated like every other durable store in
	// this file.
	var courseRepo domain.CourseRepo
	if txr := newPgxTxRunner(pool); txr != nil {
		courseRepo = deliverypg.NewCourseRepo(txr)
		log.Printf("delivery: pg CourseRepo wired (chora_delivery.courses, durable across restart)")
	} else {
		courseRepo = inmem.NewCourseRepo()
		log.Printf("delivery: CourseRepo in-memory (CHORA_DB_DSN unset — NOT durable)")
	}
	// CHO-2157 — certificates must SURVIVE the pod. Until now the only store was
	// the in-memory CertificationRegistry, so every certificate the platform
	// issued died with the process (the certifications table, created in
	// migration 0001, was never read or written by anything). The learner's
	// transcript row survived via certification.issued.v1, so learners were shown
	// credentials that no longer existed and could not be verified.
	var certStore domain.CertificationStore = domain.NewCertificationRegistry()
	if txr := newPgxTxRunner(pool); txr != nil {
		certStore = deliverypg.NewCertificationRepo(txr)
		log.Printf("delivery: certifications wired to pg.CertificationRepo (durable; UNIQUE(course_id,gcid) enforces idempotency across restarts)")
	} else {
		log.Printf("delivery: certifications wired to the IN-MEMORY registry — NOT durable (CHORA_DB_DSN unset)")
	}
	certRegistry := certStore

	// E2E-BE-CJ2 — Customer Journey #2 (Course authoring + release).
	// Production wires pg.CourseRepoCJ2Port (DB-backed, survives pod
	// restart); local dev / CHORA_DB_DSN unset falls back to
	// domain.InMemCourseCJ2Store. Per feedback_no_stubs_real_wiring the
	// in-mem path is dev-only — production must land on the pg adapter.
	var courseCJ2Port domain.CourseCJ2Port
	if txr := newPgxTxRunner(pool); txr != nil {
		cj2Repo := deliverypg.NewCourseRepo(txr)
		// CHO-1795 — engage cert-definition columns (migration 0030) once
		// applied. Gated so the code ships ahead of the migration; default off
		// ⇒ the cert UPDATE/SELECT are skipped (cert stays in-memory on the
		// aggregate only). Flip COURSE_CERT_PG_ENABLED at deploy after 0030.
		if envBool("COURSE_CERT_PG_ENABLED") {
			cj2Repo.EnableCertColumns()
			log.Printf("delivery: CJ#2 cert-definition columns ENABLED (migration 0030)")
		}
		courseCJ2Port = &deliverypg.CourseRepoCJ2Port{Repo: cj2Repo}
		log.Printf("delivery: CJ#2 course port wired to pg.CourseRepoCJ2Port")
	} else {
		courseCJ2Port = domain.NewInMemCourseCJ2Store()
		log.Printf("delivery: CJ#2 course port wired to in-memory store (CHORA_DB_DSN unset)")
	}
	// ----------------------------------------------------------------------
	// CHO-2157 — the auto-issue engine. The CompletionPolicy editor had shipped
	// and persisted for weeks with NOTHING consuming it: a learner who passed at
	// 95% against an active {AwardsCertificate: true, PassingScorePct: 70} policy
	// got no certificate, and the certifications table was empty tenant-wide.
	// Every downstream link already worked (a manual issue mints a certificate
	// AND rolls it up to the transcript) — only the TRIGGER was missing.
	//
	// It rides chora.delivery.submission.released.v1, which chora-delivery
	// already publishes per-submission and which nothing consumed. RELEASE, not
	// approve: a certificate minted before results are released would leak the
	// outcome to the learner ahead of their instructor.
	// ----------------------------------------------------------------------
	// CHO-2222 — the DECLARED-component resolver (ADR-190 D1). It reads
	// submissions + exam_results, both in chora_delivery, so the resolution is
	// intra-domain (no cross-DB query). There is deliberately NO in-memory
	// fallback: component completion is a fact about durable rows, and a stub
	// that answered without them would be a gate reporting on nothing. Left nil
	// without a DB, which makes an offering that DECLARES components fail LOUD
	// in the engine rather than certify unchecked.
	//
	// Assigned only inside the branch: an unassigned interface is a true nil, so
	// the engine's nil check works (a typed-nil pointer would read as non-nil).
	var componentCompletion subscribers.ComponentCompletionReader
	if txr := newPgxTxRunner(pool); txr != nil {
		componentCompletion = deliverypg.NewComponentCompletionRepo(txr)
		log.Printf("delivery: declared-component resolver wired to pg.ComponentCompletionRepo (CHO-2222; submissions + exam_results, intra-domain)")
	} else {
		log.Printf("delivery: declared-component resolver NOT wired (CHORA_DB_DSN unset) — an offering that DECLARES components will fail LOUD rather than certify without checking them")
	}

	var completionReleasedPushHandler http.Handler
	if pushVerifier != nil {
		completionEngine := subscribers.NewCompletionSubscriber(
			submissionRepo,
			assessmentRepo,
			offerings,
			courseCertReader{courses: courseCJ2Port},
			moduleContentReader{modules: modules, progress: moduleProgressPort},
			componentCompletion,
			certStore,
			publisher,
			nil, // in-process dedupe of broker retries; the DURABLE guarantee is
			// certifications' UNIQUE (course_id, gcid), which holds across pods.
		)
		completionReleasedPushHandler = httpapi.NewCompletionReleasedPushHandler(httpapi.CompletionPushDeps{
			Subscriber: completionEngine,
			Verifier:   pushVerifier,
		})
		log.Printf("delivery: completion engine wired: POST /api/internal/pubsub/completion-released-inbox ← %s (CHO-2157 auto-cert)", subscribers.TopicSubmissionReleased)
		bindEventbus("chora-delivery.completion-released-inbox", subscribers.TopicSubmissionReleased, httpapi.CompletionReleasedInboxEventbusHandler(completionEngine))
	} else {
		log.Printf("delivery: completion engine NOT wired (no push verifier) — certificates will not auto-issue")
	}

	// ----------------------------------------------------------------------
	// EXAM-mode auto-cert engine (R+ Four-Mode DoD §10.4 keystone). The exam
	// outcome spine was wired only up to the PUBLISH: chora-delivery emits
	// chora.delivery.exam_result.released.v1 at ExamForm.Grade, but nothing
	// consumed it, so a PASSED proctored exam minted no credential and never
	// reached the transcript. This engine is that consumer. It anchors the
	// certificate on the EXAM's course (loaded locally, intra-domain, nothing
	// sensitive on the wire) and relies on the same certification.issued.v1 →
	// chora-consumption transcript projector for rollup. Same push-verifier gate
	// as the graduate-mode completion engine; the durable idempotency guarantee
	// is certifications' UNIQUE(course_id, gcid), which converges the two lanes on
	// one credential per (course, learner).
	// ----------------------------------------------------------------------
	var examResultReleasedPushHandler http.Handler
	if pushVerifier != nil {
		examResultEngine := subscribers.NewExamResultCertSubscriber(exams, certStore, publisher, nil)
		examResultReleasedPushHandler = httpapi.NewExamResultReleasedPushHandler(httpapi.ExamResultPushDeps{
			Subscriber: examResultEngine,
			Verifier:   pushVerifier,
		})
		log.Printf("delivery: exam-result cert engine wired: POST /api/internal/pubsub/exam-result-released-inbox ← %s (R+ Four-Mode DoD §10.4 exam auto-cert)", examResultEngine.SubscribedTopic())
		bindEventbus("chora-delivery.exam-result-released-inbox", events.TopicExamResultReleased, httpapi.ExamResultReleasedInboxEventbusHandler(examResultEngine))
	} else {
		log.Printf("delivery: exam-result cert engine NOT wired (no push verifier): exam passes will not auto-issue certificates")
	}

	// CJ#2 Checkout URLs (2026-05-24) — success/cancel templates
	// with `{COURSE_ID}` placeholder. CHORA_PUBLIC_BASE_URL is sourced
	// from Secret Manager / env (per secrets-and-env). Defaults to the
	// LIVE chora.site host so a missing env var in prod still works.
	publicBase := strings.TrimRight(os.Getenv("CHORA_PUBLIC_BASE_URL"), "/")
	if publicBase == "" {
		publicBase = "https://chora.site"
	}
	// L3 (CHO-1793) — course-content media signer. Mints presigned PUT URLs for
	// video/PDF/image uploads (the FE PUTs bytes direct to the S3-compatible
	// store) and resolves durable refs to presigned GET URLs on the instructor
	// read-path. Per feedback_no_stubs_real_wiring an empty COURSE_MEDIA_BUCKET
	// (or missing S3 endpoint/credentials) leaves the signer nil and
	// POST .../content/upload-url 503s. NO stub fallback.
	var courseMediaSigner *coursemedia.CourseMediaSigner
	courseMediaBucket := strings.TrimSpace(os.Getenv("COURSE_MEDIA_BUCKET"))
	if courseMediaBucket == "" {
		log.Printf("delivery: CourseMediaSigner NOT wired — COURSE_MEDIA_BUCKET empty (POST .../content/upload-url will 503)")
	} else {
		s3cfg := objectstore.ConfigFromEnv()
		s3cfg.Bucket = courseMediaBucket
		store, sErr := objectstore.New(s3cfg)
		if sErr != nil {
			log.Printf("delivery: CourseMediaSigner NOT wired — objectstore.New failed: %v (upload-url will 503)", sErr)
		} else {
			opts := []coursemedia.CourseMediaSignerOption{}
			if presign := coursemedia.PresignClientFromConfig(s3cfg); presign != nil {
				opts = append(opts, coursemedia.WithPresignClient(presign))
			} else {
				log.Printf("delivery: CourseMediaSigner upload path disabled — S3 endpoint/credentials incomplete (upload-url will 503)")
			}
			if envM := strings.TrimSpace(os.Getenv("COURSE_MEDIA_MAX_BYTES")); envM != "" {
				if n, perr := strconv.ParseInt(envM, 10, 64); perr == nil && n > 0 {
					opts = append(opts, coursemedia.WithMaxBytes(n))
				}
			}
			if envT := strings.TrimSpace(os.Getenv("COURSE_MEDIA_TTL")); envT != "" {
				if d, perr := time.ParseDuration(envT); perr == nil && d > 0 {
					opts = append(opts, coursemedia.WithTTL(d))
				}
			}
			if s, sErr := coursemedia.NewCourseMediaSignerWithOptions(store, courseMediaBucket, opts...); sErr != nil {
				log.Printf("delivery: CourseMediaSigner NOT wired — NewCourseMediaSignerWithOptions(%q) failed: %v (upload-url will 503)", courseMediaBucket, sErr)
			} else {
				courseMediaSigner = s
				log.Printf("delivery: CourseMediaSigner wired (bucket=%s, endpoint=%s, max=%d bytes)", courseMediaBucket, s3cfg.Endpoint, s.MaxBytes())
			}
		}
	}

	// CHO-1612 — heterogeneous course curriculum. Repository selected by
	// newCourseContentRepo (pg-backed + RLS when the pool is up — survives pod
	// restart; in-mem dev fallback) + the outbox-teeing publisher (emits
	// chora.delivery.course.content_composed.v1).
	courseContentSvc := course_content.NewService(
		newCourseContentRepo(pool),
		events.NewCourseContentPublisher(publisher),
	)
	// The signer doubles as the read-path MediaResolver (gs:// → signed GET).
	courseContentDeps := &httpapi.CourseContentDeps{Svc: courseContentSvc}
	if courseMediaSigner != nil {
		courseContentDeps.Signer = courseMediaSigner
		courseContentDeps.MediaResolver = courseMediaSigner
	}
	// ADR-226 — Course Prerequisite DAG. The edge store is pg-backed
	// (chora_delivery.course_prerequisites, migration 0041) when the pool is up,
	// else the in-mem dev store. The service composes it with the CJ#2 course
	// port for catalogue-existence checks + cycle-safe authoring.
	// COURSE_MAX_PREREQ_EDGES caps edges/course (0/unset ⇒ domain default 32).
	var prereqEdges domain.CoursePrerequisitePort
	if txr := newPgxTxRunner(pool); txr != nil {
		prereqEdges = deliverypg.NewCoursePrerequisiteRepo(txr)
		log.Printf("delivery: course-prerequisite port wired to pg.CoursePrerequisiteRepo")
	} else {
		prereqEdges = domain.NewInMemCoursePrerequisiteStore()
		log.Printf("delivery: course-prerequisite port wired to in-memory store (CHORA_DB_DSN unset)")
	}
	maxPrereqEdges := 0
	if v := strings.TrimSpace(os.Getenv("COURSE_MAX_PREREQ_EDGES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxPrereqEdges = n
		}
	}
	prereqSvc := domain.NewCoursePrerequisiteService(prereqEdges, courseCJ2Port, maxPrereqEdges)

	courseCJ2Deps := &httpapi.CourseCJ2Deps{
		Courses:         courseCJ2Port,
		OutboxPublisher: publisher,
		Payments:        paymentsClient,
		Content:         courseContentDeps,
		Prerequisites:   prereqSvc,
		CheckoutSuccessURLTemplate: publicBase +
			"/a/courses/{COURSE_ID}/enrolled?session_id={CHECKOUT_SESSION_ID}",
		CheckoutCancelURLTemplate: publicBase +
			"/a/courses/{COURSE_ID}?checkout_cancelled=1",
	}

	// CJ#2 Stripe webhook DELETED per ADR-164 Stage C (2026-05-24) — the
	// canonical webhook ingress moved to chora-payments. Originating-service
	// notification arrives via Pub/Sub events handled by
	// events/payments_subscriber.go (wired below).

	// R+ Wave-6 (2026-05-26) — classroom-realtime fan-out broker shared
	// across LiveQuiz session WS + LivePoll WS + REST mutation handlers
	// per ADR-166. POD-LOCAL (multi-pod fan-out via Pub/Sub redistribution
	// is M14/M15 hardening). Buffer size 0 → broker default (64).
	classroomBroker := wsadapter.NewBroker(0)
	defer classroomBroker.Close()

	// ADR-168 — classroom-realtime hot path. Redis backplane + atomic tally +
	// ZSET leaderboard when CHORA_REDIS_ADDR is set (multi-pod correct);
	// otherwise a POD-LOCAL in-memory fallback (single-pod only — logs loud).
	var (
		rtBackplane   deliveryrealtime.Backplane
		rtTally       deliveryrealtime.TallyStore
		rtLeaderboard deliveryrealtime.LeaderboardStore
	)
	if addr := strings.TrimSpace(os.Getenv("CHORA_REDIS_ADDR")); addr != "" {
		// AUTH + TLS config from Secret Manager (no inline config). Memorystore
		// SERVER_AUTHENTICATION (port 6378) requires both; a missing CA cert ⇒
		// plaintext dial (dev / DISABLED instances). Each value resolves from a
		// *_SECRET_ID env via the shared secrets helper (mirrors the DSN
		// bootstrap path), falling back to a literal env for local dev.
		redisProject := firstNonEmptyEnv("CHORA_REDIS_SECRET_PROJECT", "CHORA_DB_PROJECT")
		redisCA := resolveRedisSecret(ctx, redisProject, "CHORA_REDIS_CA_CERT_SECRET_ID", "CHORA_REDIS_CA_CERT")
		rc, err := deliveryrealtime.NewRedis(deliveryrealtime.RedisConfig{
			Addr:      addr,
			Password:  resolveRedisSecret(ctx, redisProject, "CHORA_REDIS_PASSWORD_SECRET_ID", "CHORA_REDIS_PASSWORD"),
			CACertPEM: redisCA,
		})
		if err != nil {
			log.Printf("delivery: WARNING CHORA_REDIS_ADDR=%s client build failed (%v) — classroom realtime POD-LOCAL in-memory (single-pod only)", addr, err)
			rtBackplane, rtTally, rtLeaderboard = deliveryrealtime.NewMemBackplane(), deliveryrealtime.NewMemTally(), deliveryrealtime.NewMemLeaderboard()
		} else if err := rc.Ping(ctx); err != nil {
			log.Printf("delivery: WARNING CHORA_REDIS_ADDR=%s ping failed (%v) — classroom realtime POD-LOCAL in-memory (single-pod only)", addr, err)
			_ = rc.Close()
			rtBackplane, rtTally, rtLeaderboard = deliveryrealtime.NewMemBackplane(), deliveryrealtime.NewMemTally(), deliveryrealtime.NewMemLeaderboard()
		} else {
			defer func() { _ = rc.Close() }()
			rtBackplane, rtTally, rtLeaderboard = rc, rc, rc
			tlsOn := strings.TrimSpace(redisCA) != ""
			log.Printf("delivery: classroom realtime wired to Redis %s (tls=%v, backplane + tally + ZSET leaderboard)", addr, tlsOn)
		}
	} else {
		log.Printf("delivery: WARNING CHORA_REDIS_ADDR unset — classroom realtime POD-LOCAL in-memory (single-pod only; set CHORA_REDIS_ADDR for multi-pod fan-out per ADR-168)")
		rtBackplane, rtTally, rtLeaderboard = deliveryrealtime.NewMemBackplane(), deliveryrealtime.NewMemTally(), deliveryrealtime.NewMemLeaderboard()
	}
	// Per-pod FanIn: re-emit cross-pod backplane messages into this pod's broker.
	// One FanIn per channel namespace — the quiz FanIn matches rt:quiz:* and the
	// poll FanIn matches rt:poll:* (a single subscriber per pod for each).
	deliverToBroker := func(idOrSession string, payload []byte) {
		var m wsadapter.Message
		if err := json.Unmarshal(payload, &m); err != nil {
			return
		}
		classroomBroker.Publish(idOrSession, m)
	}
	classroomFanIn := deliveryrealtime.NewFanIn(rtBackplane, "rt:quiz:*", deliverToBroker)
	go func() { _ = classroomFanIn.Run(ctx) }()
	pollFanIn := deliveryrealtime.NewFanIn(rtBackplane, "rt:poll:*", deliverToBroker)
	go func() { _ = pollFanIn.Run(ctx) }()

	// ADR-168 gap 1 — Postgres-back the session/quiz stores when a pool is
	// present so a LiveQuizSession resolves on ANY pod (a participant's WS no
	// longer 404s on a non-owning pod). In-memory fallback is per-pod (correct
	// only single-pod-per-session). Mirrors the catalogue pg-or-inmem pattern.
	var classroomSessions classroom.SessionStore = inmem.NewClassroomSessionRepo()
	var liveQuizzes classroom.QuizStore = inmem.NewLiveQuizRepo()
	// ADR-168 gap 1b — same treatment for LivePoll. The poll's UNEXPORTED
	// `voters` set (first-vote-wins) survives the JSONB snapshot via LivePoll's
	// lossless MarshalJSON, so an already-voted learner is still rejected after
	// cross-pod rehydration (the lossy-snapshot risk that deferred gap-1b).
	var livePolls classroom.PollStore = inmem.NewLivePollRepo()
	if txr := newPgxTxRunner(pool); txr != nil {
		classroomSessions = deliverypg.NewClassroomSessionRepo(txr)
		liveQuizzes = deliverypg.NewLiveQuizRepo(txr)
		livePolls = deliverypg.NewLivePollRepo(txr)
		log.Printf("delivery: classroom session/quiz/poll stores → Postgres (chora_delivery; cross-pod resolution)")
	} else {
		log.Printf("delivery: classroom session/quiz/poll stores → in-memory (single-pod; set CHORA_DB_DSN_SECRET_ID for multi-pod)")
	}

	// R+ durability sweep Wave 2 (CHO-1580) — Survey + SurveyResponse
	// aggregates. pg.SurveyRepo (chora_delivery.surveys + survey_responses
	// JSONB-snapshot tables, RLS-isolated + durable) when a pool is available;
	// in-mem fallback for dev. Before this, surveys + their responses were
	// ephemeral (lost on pod restart).
	var surveys survey.SurveyStore
	if txr := newPgxTxRunner(pool); txr != nil {
		surveys = deliverypg.NewSurveyRepo(txr)
		log.Printf("delivery: surveys wired to pg.SurveyRepo (chora_delivery.surveys + survey_responses, durable across restart)")
	} else {
		surveys = inmem.NewSurveyRepo()
		log.Printf("delivery: surveys wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// R+ durability sweep Wave 2 follow-up (CHO-1626) — ScheduledClass now has
	// a create/reschedule/cancel write-path, so pg-backing it is worthwhile.
	// pg.SchedulingRepo (chora_delivery.scheduled_classes JSONB-snapshot, RLS-
	// isolated + durable) when a pool is available; in-mem fallback for dev.
	var scheduledClasses scheduling.SchedulingStore
	if txr := newPgxTxRunner(pool); txr != nil {
		scheduledClasses = deliverypg.NewSchedulingRepo(txr)
		log.Printf("delivery: scheduled-classes wired to pg.SchedulingRepo (chora_delivery.scheduled_classes, durable across restart)")
	} else {
		scheduledClasses = repoinmem.NewSchedulingRepo()
		log.Printf("delivery: scheduled-classes wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// R+ Four-Mode Schedule & Rooms tab — OfferingSession aggregate
	// (chora_delivery.offering_sessions JSONB-snapshot, RLS-isolated + durable,
	// mig 0036). pg-backed when a pool is available; in-mem fallback for dev.
	var offeringSessions offeringsession.Store
	if txr := newPgxTxRunner(pool); txr != nil {
		offeringSessions = deliverypg.NewOfferingSessionRepo(txr)
		log.Printf("delivery: offering-sessions wired to pg.OfferingSessionRepo (chora_delivery.offering_sessions, durable across restart)")
	} else {
		offeringSessions = repoinmem.NewOfferingSessionRepo()
		log.Printf("delivery: offering-sessions wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// R+ Four-Mode Attendance tab — session-scoped attendance records
	// (chora_delivery.attendance_records, UNIQUE(tenant,session,gcid) idempotency
	// + RLS, mig 0037). pg-backed when a pool is available; in-mem for dev.
	var offeringAttendance offeringattendance.Store
	if txr := newPgxTxRunner(pool); txr != nil {
		offeringAttendance = deliverypg.NewOfferingAttendanceRepo(txr)
		log.Printf("delivery: offering-attendance wired to pg.OfferingAttendanceRepo (chora_delivery.attendance_records, durable across restart)")
	} else {
		offeringAttendance = repoinmem.NewOfferingAttendanceRepo()
		log.Printf("delivery: offering-attendance wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// ADR-216 WS-1 (CHO-1996) — operator Credential-with-competencies catalogue
	// (chora_delivery.credentials JSONB-snapshot, RLS-isolated + durable, mig
	// 0038). pg-backed when a pool is available; in-mem fallback for dev.
	var credentials credential.Store
	if txr := newPgxTxRunner(pool); txr != nil {
		credentials = deliverypg.NewCredentialRepo(txr)
		log.Printf("delivery: credentials wired to pg.CredentialRepo (chora_delivery.credentials, durable across restart)")
	} else {
		credentials = repoinmem.NewCredentialRepo()
		log.Printf("delivery: credentials wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// CHO-2191 SP1 — Room aggregate promoted to a durable, tenant-scoped store
	// (chora_delivery.rooms, mig 0051). The foundation for the ratified
	// room_id-keyed double-book/over-capacity gate (SP2). pg-backed when a pool
	// is available; in-mem fallback for dev.
	var roomStore campusops.RoomStore = repoinmem.NewRoomRepo()
	if txr := newPgxTxRunner(pool); txr != nil {
		roomStore = deliverypg.NewRoomRepo(txr)
		log.Printf("delivery: rooms wired to pg.RoomRepo (chora_delivery.rooms, durable across restart)")
	} else {
		log.Printf("delivery: rooms wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	// CHO-2293: Campus aggregate promoted to a durable, tenant-scoped store
	// (chora_delivery.campuses, mig 0058). It was the last in-memory binding in
	// this service serving production reads/writes, and it was absent from the
	// durability guard slice below, so the boot log reported in_memory=0 while
	// campuses died on every pod restart. pg-backed when a pool is available;
	// in-mem fallback for dev only.
	var campusStore campusops.CampusStore = repoinmem.NewCampusRepo()
	if txr := newPgxTxRunner(pool); txr != nil {
		campusStore = deliverypg.NewCampusRepo(txr)
		log.Printf("delivery: campuses wired to pg.CampusRepo (chora_delivery.campuses, durable across restart)")
	} else {
		log.Printf("delivery: campuses wired to in-memory store (CHORA_DB_DSN unset; NOT durable across restart)")
	}

	deps := httpapi.Deps{
		Courses:        courseRepo,
		Bookings:       bookings,
		Certifications: certRegistry,
		// CHO-1795 — issuance consumes the course's cert definition (pg CJ#2 port).
		CourseCertLookup: courseCJ2Port,
		Catalogue:        catalogue,
		Enrollments:      enrollments,
		Publisher:        publisher,
		// ATOMIC roster bulk-enrol (POST /api/v1/offerings/{id}/roster/bulk):
		// writes each new learner's enrollment.created.v1 outbox row on the SAME
		// tx as the enrolment INSERT (true transactional outbox). Wired with the
		// INNER publisher (mint-only) - the row is teed on the tx, not via the
		// wrapping TransactionalOutboxPublisher (which would double-write on a
		// separate connection). Used only when Enrollments is the pg repo; the
		// in-mem store falls back to per-learner after-commit publish.
		EnrollmentTxTee: deliveryoutbox.NewBulkEnrollTxTee(innerPub),
		// CHO-2293: Campus aggregate (chora_delivery.campuses, mig 0058).
		CampusOps: campusStore,
		// CHO-2191 SP1 — Room aggregate (chora_delivery.rooms, mig 0051).
		Rooms: roomStore,
		// R+ M6 — ScheduledClass week-view + Wave-2 create/reschedule/cancel
		// (CHO-1626). pg-backed when CHORA_DB_DSN set; in-mem otherwise.
		Scheduling: scheduledClasses,
		// R+ Four-Mode Schedule & Rooms — OfferingSession aggregate (mig 0036).
		OfferingSessions: offeringSessions,
		// R+ Four-Mode Attendance — session-scoped attendance records (mig 0037).
		OfferingAttendance: offeringAttendance,
		// ADR-216 WS-1 (CHO-1996) — operator Credential-with-competencies
		// catalogue (chora_delivery.credentials, mig 0038).
		Credentials: credentials,
		// R+ M7 (2026-05-26) — Exam aggregate (proctored sitting).
		Exams: exams,
		// W4 Brick-1 (ADR-190 D2) — Exam BC ExamForm + ExamResult stores.
		ExamForms:   examForms,
		ExamResults: examResults,
		// W5 FRANCHISE bypass-free slice (ADR-192 D1 + ADR-193 D1, CHO-2230).
		FranchiseSatellites: franchiseSatellites,
		ExamWebhookEvents:   examWebhookEvents,
		ExamWebhookSecret:   examWebhookSecret,
		// W4 Brick-3 (ADR-190 D2) — Exam BC candidate admission deps.
		ExamCandidateDeps: examCandidateDeps,
		// W4 follow-up B (ADR-190 D2 + ADR-191) — Exam BC operational sitting deps.
		ExamSittingDeps: examSittingDeps,
		// R+ four-mode W1 (ADR-190, CHO-1849) — Offering aggregate (delivery_type).
		Offerings: offerings,
		// R+ Phase-2 W7 (WS-A) — Module course-structure aggregate (migration 0039).
		Modules: modules,
		// R+ M13 (2026-05-26) WBL Placement aggregate. CHO-2335: wrapped in
		// the display-enrichment decorator so the /r/wbl list resolves
		// learner_name (user_directory projection) + course_title (courses
		// repo); an unresolved id degrades to the raw-id fallback. Same-DB
		// read-stitch (cross-DB is forbidden), mirroring the CourseRoster view.
		Wbl: inmem.NewEnrichedWblStore(wblStore, userDirectory, courseRepo),
		// R+ M15b (2026-05-26) — ProjectGroup aggregate (FORMING→GRADED).
		ProjectGroups: projectGroups,
		// R+ M15c (2026-05-26) — SkillsFutures Claims (SSG funding queue).
		SkillsFutures: skillsFutures,
		// R+ M4 (2026-05-26) — CourseRoster READ VIEW materialised on-demand
		// from the EnrollmentPort. Wired only when the runtime enrollments
		// adapter satisfies EnrollmentListByCoursePort (in-mem store does;
		// pg.EnrollmentRepo will once the ListByCourse query lands).
		Rosters: wireRosters(enrollments, userDirectory),
		// R+ Wave-6 (2026-05-26) — classroom-realtime + survey wiring.
		ClassroomSessions:        classroomSessions,
		LiveQuizzes:              liveQuizzes,
		Surveys:                  surveys,
		LivePolls:                livePolls,
		ClassroomRealtimeBroker:  classroomBroker,
		RealtimeBackplane:        rtBackplane,
		RealtimeTally:            rtTally,
		RealtimeLeaderboard:      rtLeaderboard,
		Applications:             applicationsRepo,
		CourseCJ2:                courseCJ2Deps,
		Payments:                 paymentsClient,
		Singpass:                 singClient,
		Invoice:                  issuer,
		TestSets:                 testSets,
		QuestionSnapshotter:      questionSnapshotter,
		AssessmentDeps:           assessmentDeps,
		GradingInboxPushHandler:  gradingPushHandler,
		PaymentsInboxPushHandler: paymentsPushHandler,
		// Q3 name projection — chora.identity.user.profile_updated.v1 inbox.
		IdentityProfileInboxPushHandler: identityProfilePushHandler,
		// W7 StudentModuleProgress projection (CHO-2074) — two completion inboxes.
		ModuleProgressAtomInboxPushHandler:   moduleProgressAtomPushHandler,
		ModuleProgressGradedInboxPushHandler: moduleProgressGradedPushHandler,
		// CHO-2157 — auto-issue certificates on a released, policy-satisfying grade.
		CompletionReleasedInboxPushHandler: completionReleasedPushHandler,
		// R+ Four-Mode DoD section 10.4: auto-issue a certificate on an exam PASS.
		ExamResultReleasedInboxPushHandler: examResultReleasedPushHandler,
		// W7 StudentModuleProgress read side — offering-nested progress GET.
		ModuleProgress: moduleProgressPort,
		// CHO-1827, ASYNC analytics: the self-paced progress projection fed by
		// chora-consumption's learning_path.{advanced,completed}.v1, plus its read
		// side behind the offering Analytics tab.
		CourseProgressAdvancedInboxPushHandler:  courseProgressAdvancedPushHandler,
		CourseProgressCompletedInboxPushHandler: courseProgressCompletedPushHandler,
		CourseProgress:                          courseProgressPort,
		// Lane 1c W4 — batch→test-set assembly inbox (CHO-1703 / ADR-180).
		BatchTestSetInboxPushHandler: batchTestSetPushHandler,
		// E2E-INFRA-COLD-START §B (Infra option 1 ack 2026-05-16).
		ReadyzHandler:  readyzHandler,
		HealthzHandler: healthzHandler,
	}

	// ADR-236 D5 — report-only runtime durability guard over the composition
	// root (W0-F1 gate, CHO-2198). Classifies each wired repository by SHAPE
	// (holds a live *pgxpool.Pool ⇒ DURABLE; a data map ⇒ IN_MEMORY) and logs a
	// structured, greppable report at boot. Report-only unless
	// CHORA_DURABILITY_GUARD=enforce AND the binding is allow-listed — nil
	// allow-list matches the chora-payments / chora-identity wirings. The legacy
	// in-memory attendance.Log is intentionally OMITTED (retired under D3).
	durabilityguard.Guard("chora-delivery", []durabilityguard.Binding{
		{Port: "courses", Adapter: courseRepo},
		{Port: "bookings", Adapter: bookings},
		{Port: "scheduled_classes", Adapter: scheduledClasses},
		{Port: "offerings", Adapter: offerings},
		{Port: "offering_sessions", Adapter: offeringSessions},
		{Port: "offering_attendance", Adapter: offeringAttendance},
		{Port: "certifications", Adapter: certRegistry},
		{Port: "enrollments", Adapter: enrollments},
		{Port: "catalogue", Adapter: catalogue},
		{Port: "applications", Adapter: applicationsRepo},
		{Port: "exams", Adapter: exams},
		{Port: "modules", Adapter: modules},
		{Port: "surveys", Adapter: surveys},
		{Port: "project_groups", Adapter: projectGroups},
		{Port: "skillsfutures", Adapter: skillsFutures},
		{Port: "credentials", Adapter: credentials},
		{Port: "rooms", Adapter: roomStore},
		// CHO-2293: campus was serving production writes from an in-memory
		// Registry while ABSENT from this slice, so the guard reported
		// in_memory=0 and was blind to it. Registering it makes the verdict honest.
		{Port: "campus", Adapter: campusStore},
		{Port: "closure", Adapter: closureRepo},
	}, nil)

	handler := httpapi.NewServer(deps)

	// ----------------------------------------------------------------------
	// gRPC server on :9090 (env CHORA_GRPC_PORT) — Wave-1 D-FULL.
	//
	// Source of truth: docs/m13/grpc-mass-remediation-2026-05-16.md.
	//
	// Closes the systemic ADR-140 violation where chora-gateway BFF dialed
	// chora-delivery over plain HTTP :8080. After Wave 2, the BFF switches
	// to dialing this gRPC listener; after Wave 3, the image roll cuts
	// traffic over. The HTTP server keeps running on :8080 through Wave 3
	// + 24h soak per the remediation doc — this gRPC server is additive.
	//
	// mTLS via Cloud Service Mesh sidecar; plain insecure creds inside the
	// mesh since istio-proxy terminates TLS. The scoped AuthorizationPolicy
	// (chora-infra/k8s/services/chora-delivery/authz-allow-delivery-grpc.yaml)
	// constrains which source namespaces can reach this server's RPCs.
	//
	// Fail-loud per `feedback_no_stubs_real_wiring`: no conditional
	// "if env unset { skip }" shim. The server registers unconditionally.
	// ----------------------------------------------------------------------
	grpcPort := strings.TrimSpace(os.Getenv("CHORA_GRPC_PORT"))
	if grpcPort == "" {
		grpcPort = strings.TrimSpace(os.Getenv("GRPC_PORT"))
	}
	if grpcPort == "" {
		grpcPort = "9090"
	}
	grpcLis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("delivery: gRPC net.Listen :%s: %v", grpcPort, err)
	}
	grpcSrv := grpc.NewServer()
	deliveryGRPC := deliverygrpc.NewDeliveryServer(
		courseRepo, catalogue, enrollments, certRegistry,
		// CHO-2040 ceremony learning-edges seam — the same submissionRepo
		// (pg-backed in prod; in-mem dev fallback) that serves the HTTP
		// result path backs ListLearnerGradedSubmissions, so the RELEASED
		// visibility gate is enforced by one shared query surface.
		submissionRepo,
	)
	deliveryv1.RegisterDeliveryServer(grpcSrv, deliveryGRPC)

	// gRPC health check — required for Cloud Service Mesh probe routing.
	// Mirrors chora-tenancy cmd/server/main.go lines 491-494.
	healthSrv := healthgrpc.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.delivery.v1.Delivery", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)

	go func() {
		log.Printf("service=%s grpc listening on :%s (Delivery + Health bound)", serviceName, grpcPort)
		if err := grpcSrv.Serve(grpcLis); err != nil && err != grpc.ErrServerStopped {
			log.Fatalf("delivery: gRPC Serve: %v", err)
		}
	}()

	addr := ":" + port
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("service=%s version=%s listening on %s", serviceName, serviceVersion, addr)

	// Run server in a goroutine so we can intercept SIGTERM cleanly —
	// Cloud Run sends SIGTERM during scale-to-zero and rolling updates.
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("server error: %v", err)
	case <-ctx.Done():
		log.Printf("received SIGTERM — shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}

		// Final outbox drain — flush in-flight pending rows before exit.
		if outboxDispatcher != nil {
			finalDrain, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer fcancel()
			if n, derr := outboxDispatcher.DrainOnce(finalDrain, 200); derr != nil {
				log.Printf("delivery: final outbox drain error: %v (drained %d)", derr, n)
			} else {
				log.Printf("delivery: final outbox drain published %d rows", n)
			}
		}
		if dispatcherDone != nil {
			select {
			case <-dispatcherDone:
			case <-time.After(5 * time.Second):
				log.Printf("delivery: outbox dispatcher shutdown timed out (5s)")
			}
		}

		// Drain gRPC server alongside HTTP — in-flight Delivery RPCs finish
		// + clients see EOF cleanly before the listener closes. Mirrors the
		// chora-identity shutdown order (services/chora-identity/cmd/server/
		// main.go lines 689-702).
		grpcShutdownDone := make(chan struct{})
		go func() {
			grpcSrv.GracefulStop()
			close(grpcShutdownDone)
		}()
		select {
		case <-grpcShutdownDone:
			log.Printf("delivery: grpc server drained")
		case <-time.After(10 * time.Second):
			log.Printf("delivery: grpc graceful-stop deadline exceeded — forcing stop")
			grpcSrv.Stop()
		}

		// Flush in-flight OTLP spans before exit — async-handle path.
		// WaitContext blocks until init settles (no-op by shutdown time
		// because pgx-pool init above already gave OTLP best-effort
		// wall-clock to land).
		otlpShutdownCtx, otlpCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer otlpCancel()
		res := otlpHandle.WaitContext(otlpShutdownCtx)
		if err := res.Shutdown(otlpShutdownCtx); err != nil {
			log.Printf("otlp shutdown error: %v", err)
		}
	}
}

// NOTE: seedPhyllisCatalogue (the inline hard-coded CSPO / CSM-Prep /
// PMP-Crash-Course demo seed) has been REMOVED. The public course catalogue
// is now backed by the real chora_delivery database and seeded durably via
// chora-infra/seed/phyllis/04_delivery.sql — no inline stub data in this
// binary (per feedback_no_inline_config + the Phyllis Step 5 directive).

// wireRosters builds an in-memory CourseRosterRepo if the runtime
// enrollments adapter satisfies the EnrollmentListByCoursePort port.
// Returns nil otherwise — the rosters route bails with 503 per
// feedback_no_stubs_real_wiring rather than serving fake data.
func wireRosters(enrollments domain.EnrollmentPort, userDirectory directory.UserDirectoryPort) rostering.CourseRosterRepo {
	listByCourse, ok := enrollments.(domain.EnrollmentListByCoursePort)
	if !ok {
		log.Printf("delivery: rosters NOT wired (enrollments adapter does not implement EnrollmentListByCoursePort yet)")
		return nil
	}
	log.Printf("delivery: rosters wired to in-memory CourseRosterRepo with user_directory name projection (Q3)")
	return inmem.NewCourseRosterRepoWithDirectory(listByCourse, userDirectory)
}

// firstNonEmptyEnv returns the value of the first set + non-blank env var in
// keys, or "" when none are set. Used to resolve the Secret-Manager project for
// the redis secrets from whichever project env the deployment provides.
func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// resolveRedisSecret resolves a redis credential (AUTH string / CA PEM):
//   - if secretIDEnv names a Secret Manager secret AND project is set, fetch it
//     via the shared secrets client (mirrors the DSN bootstrap path);
//   - else fall back to the literalEnv value (local dev / plaintext instances);
//   - "" when neither is set.
//
// Resolution failures are logged + downgraded to "" so a misconfig degrades to
// the POD-LOCAL fallback (loud warning at the call site) rather than crashing
// boot — per the realtime adapter's nil-guarded posture.
func resolveRedisSecret(ctx context.Context, project, secretIDEnv, literalEnv string) string {
	id := strings.TrimSpace(os.Getenv(secretIDEnv))
	if id == "" || project == "" {
		return strings.TrimSpace(os.Getenv(literalEnv))
	}
	c, err := cgcsecrets.NewClient(ctx, project)
	if err != nil {
		log.Printf("delivery: WARNING redis secret client for %s: %v", secretIDEnv, err)
		return ""
	}
	defer func() { _ = c.Close() }()
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	v, err := c.GetSecret(rctx, id)
	if err != nil {
		log.Printf("delivery: WARNING redis secret %q resolve failed: %v", id, err)
		return ""
	}
	return strings.TrimSpace(v)
}
