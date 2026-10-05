// Package httpapi is the HTTP/JSON adapter for chora-delivery.
//
// Hexagonal layout: this layer depends on `internal/domain/delivery` (the
// pure domain) and on `internal/adapter/inmem` (the persistence adapter).
// The domain layer NEVER imports anything from this package.
//
// Endpoints (per the M11+ Phase D brief — strict subset of
// chora-contracts/openapi/delivery-admin.yaml):
//
//	GET  /healthz
//	GET  /readyz
//	POST /api/courses
//	GET  /api/courses
//	GET  /api/courses/{id}
//	GET  /api/bookings
//	POST /api/bookings
//	PATCH /api/bookings/{id}/status
//	POST /api/certifications
//	GET  /api/certifications/{id}
//
// Middleware extracts X-Tenant-Id + gcid from request headers and rejects
// requests missing X-Tenant-Id (gcid is optional for some admin paths so
// we log it but do not fail).
package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/invoice"
	"github.com/apollo-chora/chora-delivery/internal/adapter/payments"
	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	"github.com/apollo-chora/chora-delivery/internal/adapter/singpass"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
	"github.com/apollo-chora/chora-delivery/internal/domain/credential"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
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

// Deps wires the in-memory repos + cert registry into the HTTP layer.
//
// In M12+ this struct will be swapped to interfaces backed by Postgres;
// the handler signatures are stable.
type Deps struct {
	// Courses is the hexagonal domain.CourseRepo port (ADR-236 D1). Production
	// wires pg.CourseRepo (chora_delivery.courses — durable + RLS-isolated,
	// survives pod restart + multi-pod consistent); local dev / unit tests
	// wire inmem.NewCourseRepo() (which satisfies the same port). Before D1
	// this field was unconditionally bound to the concrete in-memory type
	// (no pool gate) — every composite-created course died on pod restart.
	Courses domain.CourseRepo
	// Bookings is the hexagonal domain.BookingPort. Production wires
	// pg.BookingRepo (chora_delivery.bookings — durable + RLS-isolated +
	// listable); local dev / unit tests wire inmem.NewBookingRepo() (which
	// satisfies the same port). HANDOFF_RPLUS §6 follow-up.
	Bookings domain.BookingPort
	// Certifications is the DURABLE certificate store (CHO-2157). It used to be
	// the concrete in-memory CertificationRegistry, so every certificate the
	// platform issued died with the pod. Production now wires
	// pg.CertificationRepo; tests/dev keep the registry.
	Certifications domain.CertificationStore
	// CourseCertLookup (optional, CHO-1795) lets the issuance chain CONSUME a
	// course's cert definition — when a course defines a cert, the issued
	// certificate reflects its cert_type. Nil-safe: unset ⇒ issuance unchanged.
	// Wired to the pg CJ#2 course port in cmd/server (CourseRepoCJ2Port).
	CourseCertLookup CourseCertLookupPort

	// Phyllis MVP additions (Comic Ch5 P10 — public catalogue + enrollment).
	//
	// Catalogue is the hexagonal port domain.CataloguePort — production
	// wires pg.CatalogueRepo (chora_delivery-backed, survives pod restarts);
	// local dev / unit tests wire domain.InMemCatalogue. The handlers depend
	// ONLY on the interface.
	Catalogue   domain.CataloguePort
	Enrollments domain.EnrollmentPort
	Publisher   events.Publisher

	// EnrollmentTxTee writes each new learner's chora.delivery.enrollment.created.v1
	// outbox row on the SAME transaction as the enrolment INSERT, for the ATOMIC
	// roster bulk-enrol path (POST /api/v1/offerings/{id}/roster/bulk). Production
	// wires deliveryoutbox.NewBulkEnrollTxTee(innerPublisher); nil ⇒ the bulk
	// endpoint falls back to per-learner after-commit publish (dev / in-mem, where
	// there is no transaction to tee onto). Satisfied structurally, so httpapi does
	// not import the outbox adapter. See offering_roster_bulk_handler.go.
	EnrollmentTxTee BulkEnrollTee

	// S4.3 BE-CO2: Campus Operations (CHO-19 successor).
	//
	// CHO-2293: Campus promoted OFF the in-memory campusops.Registry onto a
	// durable, tenant-scoped store (chora_delivery.campuses, mig 0058).
	// campusops.CampusStore: production wires pg.CampusRepo (durable +
	// RLS-isolated across pod restart); dev/tests wire repoinmem.NewCampusRepo().
	// Nil ⇒ the /v1/campus routes are not mounted (unchanged from the Registry
	// era). The handlers now surface a store error as 5xx rather than rendering
	// an empty list, so a dead backend can never masquerade as a tenant with no
	// campuses.
	CampusOps campusops.CampusStore

	// CHO-2191 SP1 — Room aggregate promoted to a durable, tenant-scoped store
	// (chora_delivery.rooms, mig 0051). The foundation for the ratified
	// room_id-keyed double-book/over-capacity gate (SP2). campusops.RoomStore:
	// production wires pg.RoomRepo (durable + RLS-isolated across pod restart);
	// dev/tests wire repoinmem.NewRoomRepo(). Nil ⇒ the /api/v1/rooms routes 503.
	Rooms campusops.RoomStore

	// R+ M6 scheduling — ScheduledClass aggregate (week-view list + the
	// Wave-2 create/reschedule/cancel write-path, CHO-1626). Hexagonal
	// scheduling.SchedulingStore: production wires pg.SchedulingRepo
	// (chora_delivery.scheduled_classes — durable + RLS-isolated across pod
	// restart); dev / unit tests wire repoinmem.NewSchedulingRepo(). Nil ⇒
	// the /v1/scheduling/classes routes return 503.
	Scheduling scheduling.SchedulingStore

	// R+ M7 exams — Exam aggregate (proctored sitting) admin surface.
	// Required by POST/GET /api/v1/exams + GET /api/v1/exams/{id}. Nil ⇒
	// handler returns 503 per feedback_no_stubs_real_wiring.
	//
	// exam.ExamStore port: production wires pg.ExamRepo (chora_delivery.exams —
	// durable + RLS-isolated, R+ durability sweep); dev/tests wire
	// inmem.NewExamRepo(). Handlers depend ONLY on the interface.
	Exams exam.ExamStore

	// W4 Brick-1 (ADR-190 D2) — Exam BC ExamForm + ExamResult stores.
	ExamForms   exam.ExamFormStore
	ExamResults exam.ExamResultStore

	// W5 FRANCHISE bypass-free slice (ADR-192 D1 + ADR-193 D1, CHO-2230):
	// FranchiseSatellites is the owner-to-satellite rollup-scope mapping
	// store (chora_delivery.franchise_satellite, mig 0056);
	// ExamWebhookEvents is the inbound satellite delivery dedup gate
	// (chora_delivery.exam_webhook_events, mig 0057, RLS-disabled);
	// ExamWebhookSecret is the inbound HMAC shared secret from
	// CHORA_EXAM_WEBHOOK_SECRET (Secret Manager-sourced env). Nil stores /
	// empty secret leave the routes mounted and 503ing fail-loud.
	FranchiseSatellites franchise.Store
	ExamWebhookEvents   examwebhook.Repo
	ExamWebhookSecret   string

	// R+ four-mode W1 (ADR-190) — Offering aggregate: the delivery INSTANCE of
	// a reusable Course, carrying delivery_type {graduate|short|async} + the
	// DRAFT→LAUNCHED→RUNNING→CONCLUDED→ARCHIVED FSM. Required by POST/GET
	// /api/v1/offerings + GET /api/v1/offerings/{id}. Nil ⇒ handler returns 503.
	//
	// domain.OfferingPort: production wires pg.OfferingRepo
	// (chora_delivery.offerings — durable + RLS-isolated); dev/tests wire
	// inmem.NewOfferingRepo(). Handlers depend ONLY on the interface.
	Offerings domain.OfferingPort

	// R+ Phase-2 W7 (WS-A) — Module course-structure aggregate: groups a
	// Course's flat course_content items into ordered, named modules each with
	// a completion Requirement. Surfaced through the offering-nested Curriculum
	// tab as a VALIDATED PROXY (course_id ∈ offering.CourseIDs). module.ModulePort:
	// production wires pg.NewModuleRepo (chora_delivery.course_modules(+_items),
	// migration 0039 — durable + RLS-isolated); dev/tests wire
	// module.NewInMemModuleStore(). Nil ⇒ the module routes return 503.
	Modules module.ModulePort

	// W7 StudentModuleProgress projection (CHO-2074) — per-learner, per-module
	// completion read side. moduleprogress.ProgressPort: production wires
	// pg.NewProgressRepo (chora_delivery.student_module_progress, migration 0045);
	// the write side is the event-fed projection. Nil ⇒ the progress read route
	// returns 503 (DB-gated — the projection requires pg).
	ModuleProgress moduleprogress.ProgressPort

	// CourseLearnerProgress projection (CHO-1827): per-learner, per-COURSE
	// self-paced traversal read side, fed by chora-consumption's
	// learning_path.{advanced,completed}.v1 via the course-progress inboxes.
	// Backs avg_progress_pct + completion_rate on the ASYNC Analytics tab.
	// Production wires pg.NewCourseProgressRepo (chora_delivery
	// .course_learner_progress, migration 0054). Nil ⇒ the analytics route
	// returns 503: an async Analytics tab that silently renders enrolment-only
	// looks identical to one where nobody is learning, and that ambiguity is the
	// whole defect this projection exists to remove.
	CourseProgress courseprogress.ProgressPort

	// R+ M13 wbl — WBL Placement (work-based learning) aggregate.
	// Required by /api/v1/wbl-placements CRUD. Nil ⇒ handler returns 503.
	Wbl wbl.WblStore

	// R+ M15b project_group — ProjectGroup aggregate
	// (FORMING → ACTIVE → SUBMITTED → GRADED). Nil ⇒ handler returns 503.
	ProjectGroups project_group.ProjectGroupStore

	// R+ M15c SkillsFutures Claims — SSG funding-request aggregate
	// (PENDING → APPROVED/REJECTED → DISBURSED). Nil ⇒ routes not mounted.
	SkillsFutures skillsfutures.SkillsFuturesStore

	// R+ M4 rosters — CourseRoster READ VIEW (per-course learner list).
	// Materialised on-demand from deps.Enrollments via
	// rostering.CourseRosterRepo. Nil ⇒ handler returns 503.
	Rosters rostering.CourseRosterRepo

	// R+ Four-Mode Schedule & Rooms — OfferingSession aggregate (scheduled
	// delivery sessions belonging to an Offering: title + room + time). The
	// stable anchor an Attendance record references. Nil ⇒ the offering-nested
	// schedule handler returns 503.
	OfferingSessions offeringsession.Store

	// R+ Four-Mode Attendance — session-scoped attendance Record (a learner's
	// present/absent/late/excused mark for an OfferingSession, idempotent on
	// (tenant,session,gcid)). Nil ⇒ the offering-nested attendance handler 503.
	OfferingAttendance offeringattendance.Store

	// ADR-216 WS-1 — operator-curated Credential-with-competencies catalogue
	// (chora_delivery.credentials JSONB-snapshot, RLS-isolated + durable, mig
	// 0038). A named credential (e.g. PMP) with a structured competency breakdown
	// (the shared-vocabulary seed, D2), decoupled from courses + learner-immutable
	// (D1). Nil ⇒ the /api/v1/credentials routes return 503.
	Credentials credential.Store

	// R+ Wave-5 M8 classroom-realtime sessions — the LiveQuizSession store.
	// ADR-168 gap 1: now an interface (classroom.SessionStore) so the session
	// state can be Postgres-backed for true multi-pod (a WS can land on any pod
	// and still resolve the session). inmem repo = dev/single-pod; pg = prod
	// multi-pod. Nil ⇒ snapshot + session routes return 503.
	ClassroomSessions classroom.SessionStore

	// R+ Wave-5 M9 LiveQuiz aggregate (DRAFT→PUBLISHED→ARMED→LIVE→CLOSED)
	// admin CRUD. ADR-168 gap 1: now classroom.QuizStore (inmem | pg). Nil ⇒
	// /api/v1/live-quizzes routes return 503.
	LiveQuizzes classroom.QuizStore

	// R+ Wave-5 surveys — Survey + SurveyResponse aggregates
	// (DRAFT→DISTRIBUTED→CLOSED). Hexagonal survey.SurveyStore: production
	// wires pg.SurveyRepo (chora_delivery.surveys + survey_responses —
	// durable + RLS-isolated across pod restart, R+ durability sweep Wave 2);
	// dev / unit tests wire inmem.NewSurveyRepo(). Nil ⇒ /api/v1/surveys
	// routes return 503.
	Surveys survey.SurveyStore

	// R+ Wave-5 M11 LivePoll aggregate (DRAFT→OPEN→CLOSED) — needed by the
	// live-poll WS fan-out handler (μ commit 7c0c82cc). ADR-168 gap-1b: now an
	// interface (classroom.PollStore) so the poll state can be Postgres-backed
	// for true multi-pod — a learner's vote/WS lands on any pod and still
	// resolves the poll (the unexported `voters` set survives via LivePoll's
	// lossless MarshalJSON). inmem = dev/single-pod; pg = prod multi-pod. Nil ⇒
	// /api/v1/live-polls routes return 503.
	LivePolls classroom.PollStore

	// R+ Wave-5 M10/M11 — classroom-realtime fan-out broker, the per-pod LEAF
	// that fans out to THIS pod's WS clients. Cross-pod fan-out is via the
	// realtime ports below (ADR-168 — supersedes the pod-local-only note).
	ClassroomRealtimeBroker *wsadapter.Broker

	// ADR-168 classroom-realtime hot path. When wired, the producer handlers
	// (advance / graded submit / vote / open / close) publish fan-out messages
	// onto the cross-pod Backplane, bump authoritative counts in the TallyStore,
	// and credit cumulative scores in the LeaderboardStore. Nil ⇒ the side
	// effects are skipped (response recording + durable events still happen).
	RealtimeBackplane   realtime.Backplane
	RealtimeTally       realtime.TallyStore
	RealtimeLeaderboard realtime.LeaderboardStore

	// S6.1 — Course Application aggregate (Payments gRPC + Singpass + invoice).
	//
	// Per ADR-164 Stage C (2026-05-24): inline Stripe SDK calls cut over to
	// the canonical chora-payments PaymentService gRPC. Payments is the
	// outbound write path the accept-offer handler calls when minting a
	// Stripe Checkout Session for a Course Application. The async payment
	// outcome (chora.payments.application_payment.payment_captured.v1) is
	// materialised back by events/payments_subscriber.go.
	//
	// Hexagonal application.ApplicationPort: production wires pg.ApplicationRepo
	// (chora_delivery.applications + application_state_history — durable +
	// RLS-isolated across pod restart, R+ durability sweep Wave 2); dev / unit
	// tests wire repoinmem.NewApplicationRepo().
	Applications application.ApplicationPort
	Payments     *payments.Client
	Singpass     singpass.MyInfoClient
	Invoice      *invoice.Issuer

	// AppCheckoutSuccessURLTemplate / AppCheckoutCancelURLTemplate — wired
	// at cmd/server boot. Each MUST include the literal placeholder
	// `{APPLICATION_ID}` (this handler substitutes it) and may include
	// `{CHECKOUT_SESSION_ID}` (Stripe substitutes that at redirect time).
	// Defaults to chora.site URLs when empty so unwired dev environments
	// still see a 502 from the live mesh path rather than a 501.
	AppCheckoutSuccessURLTemplate string
	AppCheckoutCancelURLTemplate  string

	// Lane A (B-FE-X5) — TestSet authoring surface (A+ X.2).
	// Production wires pg.TestSetRepo; local dev wires InMemTestSetStore.
	// The handlers depend ONLY on the TestSetPort interface.
	TestSets TestSetPort

	// Fix-F (Lane A snapshot debt close) — QuestionSnapshotter pulls
	// canonical MCQ + OE payloads from chora-creation at TestSet.Publish()
	// time. Production wires clients.QuestionClient (gRPC to chora-creation
	// `Creation` service per chora-contracts/proto/services/creation/v1/
	// creation.proto SnapshotQuestionByID RPC). Local dev / in-mem tests
	// can leave nil — the handler will fall back to Publish() without
	// snapshot capture (and grading will fail-loud per
	// `feedback_no_stubs_real_wiring`).
	QuestionSnapshotter domain.QuestionSnapshotter

	// Lane B (B-FE-X5 + ADR-155) — Assessments + Submissions + Grading.
	AssessmentDeps *AssessmentDeps

	// W4 Brick-3 (ADR-190 D2) — Exam BC candidate admission (nested deps;
	// nil ⇒ candidate routes unmounted).
	ExamCandidateDeps *ExamCandidateDeps

	// W4 follow-up B (ADR-190 D2 + ADR-191) — Exam BC operational sitting deps
	// (ExamSitting + ExamInvigilator + IncidentReport; nil ⇒ routes unmounted).
	ExamSittingDeps *ExamSittingDeps

	// Fix-E (ADR-155 §"Locked architectural rule") — Pub/Sub push handler
	// for the OE-batch completion-event inbox. Nil ⇒ /api/internal/pubsub/
	// grading-inbox is NOT registered. Wired at cmd/server boot when the
	// GradingInboxSubscriber is constructed.
	GradingInboxPushHandler http.Handler

	// BatchTestSetInboxPushHandler — Lane 1c W4 (CHO-1703 / ADR-180 D10)
	// Pub/Sub push handler for chora.creation.question_batch.accepted.v1
	// (BINARY, Schema-Registry-bound). Nil ⇒ /api/internal/pubsub/
	// batch-testset-inbox is NOT registered. Wired at cmd/server boot when
	// the BatchTestSetSubscriber is constructed. Subscription binding:
	// chora-delivery.creation-question_batch-accepted (push, OIDC audience
	// = the endpoint URL, via the chora-gateway internal-pubsub
	// passthrough's delivery-default routing).
	BatchTestSetInboxPushHandler http.Handler

	// PaymentsInboxPushHandler — Pub/Sub push handler for the 4 canonical
	// chora.payments.* events chora-delivery cares about (ADR-164 Stage C,
	// 2026-05-24). Nil ⇒ /api/internal/pubsub/payments-inbox is NOT
	// registered. Wired at cmd/server boot when the PaymentsSubscriber is
	// constructed.
	//
	// All 4 chora-payments → chora-delivery subscription bindings POST
	// here:
	//
	//	chora.payments.course_purchase.payment_captured.v1
	//	chora.payments.course_purchase.refunded.v1
	//	chora.payments.application_payment.payment_captured.v1
	//	chora.payments.application_payment.refunded.v1
	//
	// The handler dispatches by Pub/Sub `topic` attribute → typed
	// subscriber method. Subscription names follow the chora-delivery
	// convention `chora-delivery-payments-{aggregate}-{event_type}` — see
	// events/payments_subscriber.go::PaymentsSubscriptionName.
	PaymentsInboxPushHandler http.Handler

	// IdentityProfileInboxPushHandler — Pub/Sub push handler for the Q3 name
	// projection: chora.identity.user.profile_updated.v1 upserts the
	// (gcid → display_name) row into chora_delivery.user_directory. Nil ⇒
	// /api/internal/pubsub/identity-profile-inbox is NOT registered. Wired at
	// cmd/server boot when the IdentityProfileSubscriber is constructed. Same
	// mesh-internal + OIDC-verifier posture as the payments inbox.
	IdentityProfileInboxPushHandler http.Handler

	// W7 StudentModuleProgress projection (CHO-2074) — two single-topic Pub/Sub
	// push handlers feeding the per-learner module-completion projection. Nil ⇒
	// the respective /api/internal/pubsub/module-progress-*-inbox is NOT
	// registered. Wired at cmd/server boot when a DB pool is present (the
	// projection's enrolment-gated resolver is a SQL join — no in-mem fallback).
	//   - atom   ← chora.consumption.atom_session.completed.v1
	//   - graded ← chora.delivery.submission.graded.v1
	ModuleProgressAtomInboxPushHandler   http.Handler
	ModuleProgressGradedInboxPushHandler http.Handler
	// CompletionReleasedInboxPushHandler consumes chora.delivery.submission.
	// released.v1 and auto-issues certificates per the offering's
	// CompletionPolicy (CHO-2157). Nil ⇒ the endpoint is NOT registered.
	CompletionReleasedInboxPushHandler http.Handler
	// ExamResultReleasedInboxPushHandler consumes chora.delivery.exam_result.
	// released.v1 and auto-issues a certificate on an exam PASS, anchored on the
	// exam's course (R+ Four-Mode DoD §10.4 EXAM keystone). Nil ⇒ the endpoint is
	// NOT registered.
	ExamResultReleasedInboxPushHandler http.Handler
	// CourseProgress*InboxPushHandler consume chora-consumption's
	// learning_path.{advanced,completed}.v1 and project self-paced traversal onto
	// course_learner_progress, which backs avg_progress_pct + completion_rate on
	// the ASYNC Analytics tab (R+ Four-Mode DoD §10.3, CHO-1827). TWO single-topic
	// endpoints, not one topic-dispatching endpoint: chora-consumption's outbox
	// does not always stamp the `topic` attribute, so the ROUTE is the only
	// reliable discriminator between an advance and a completion. Nil ⇒ the
	// endpoint is NOT registered.
	CourseProgressAdvancedInboxPushHandler  http.Handler
	CourseProgressCompletedInboxPushHandler http.Handler

	// E2E-INFRA-COLD-START §B — canonical /readyz handler from
	// libs/chora-go-common/http (choraserver.ReadyzHandler). When
	// non-nil, REPLACES the simplistic always-200 readyHandler so
	// /readyz reflects real pool + outbox state. When nil, falls back
	// to the legacy always-200 handler (dev / unit-test mode).
	//
	// Wired at cmd/server boot once *pgxpool.Pool + outbox *sql.DB are
	// resolved, per
	// docs/m13/E2E-INFRA-COLD-START-be-svc-ack-2026-05-16.md.
	ReadyzHandler http.Handler

	// E2E-INFRA-COLD-START §B (companion) — canonical /healthz handler.
	// /healthz remains a pure "process alive" probe so the override here
	// is mostly to keep the response body shape consistent with the
	// shared library; the previous handler shape stays compatible.
	HealthzHandler http.Handler

	// E2E-BE-CJ2 — Customer Journey #2 (Course authoring + R+ review +
	// release flow). Production wires pg.CourseRepoCJ2Port + the chora-
	// delivery outbox publisher; local dev / unit tests wire
	// domain.InMemCourseCJ2Store + the in-memory publisher. Nil → the
	// 6 /api/v1/courses CJ#2 routes are NOT mounted.
	//
	// Per CJ#2 directive row at
	// `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3.
	CourseCJ2 *CourseCJ2Deps

	// CJ#2 Stripe webhook DELETED per ADR-164 Stage C (2026-05-24) — the
	// canonical webhook ingress is now chora-payments. Originating-service
	// notification arrives via Pub/Sub (chora.payments.course_purchase.*
	// + chora.payments.application_payment.*) wired by
	// events/payments_subscriber.go.
}

// NewServer builds the routed http.Handler.
func NewServer(deps Deps) http.Handler {
	mux := http.NewServeMux()

	// Health/readiness — no tenant/gcid required.
	//
	// /healthz is registered per the brief, but Cloud Run's GFE intercepts
	// the exact bare path /healthz before the request reaches the
	// container — clients hitting Cloud Run get a Google 404 HTML page.
	// /healthz/ (trailing slash) and /health both reach the container, so
	// we register all three aliases (same pattern as chora-aplus-api-hello).
	//
	// E2E-INFRA-COLD-START §B: when deps.ReadyzHandler is non-nil, /readyz
	// goes through the canonical chora-go-common/http handler that checks
	// pool ping + outbox reachability. Otherwise the simplistic
	// always-200 fallback below is registered (dev / unit-test mode).
	var healthHandlerFn http.HandlerFunc = healthHandler
	if deps.HealthzHandler != nil {
		healthHandlerFn = deps.HealthzHandler.ServeHTTP
	}
	var readyHandlerFn http.HandlerFunc = readyHandler
	if deps.ReadyzHandler != nil {
		readyHandlerFn = deps.ReadyzHandler.ServeHTTP
	}
	mux.HandleFunc("/healthz", logging(healthHandlerFn))
	mux.HandleFunc("/healthz/", logging(healthHandlerFn))
	mux.HandleFunc("/health", logging(healthHandlerFn))
	mux.HandleFunc("/readyz", logging(readyHandlerFn))
	mux.HandleFunc("/readyz/", logging(readyHandlerFn))

	// /api/courses — list + create
	mux.HandleFunc("/api/courses", logging(tenantRequired(coursesHandler(deps))))
	// /api/courses/{id}
	mux.HandleFunc("/api/courses/", logging(tenantRequired(courseSubHandler(deps))))

	// /api/bookings — GET list (tenant-scoped) + POST create
	mux.HandleFunc("/api/bookings", logging(tenantRequired(bookingsHandler(deps))))
	// /api/bookings/{id}/status — patch
	mux.HandleFunc("/api/bookings/", logging(tenantRequired(bookingSubHandler(deps))))

	// /api/certifications — issue + list
	mux.HandleFunc("/api/certifications", logging(tenantRequired(certificationsHandler(deps))))
	// /api/certifications/{id} — get
	mux.HandleFunc("/api/certifications/", logging(tenantRequired(certificationSubHandler(deps))))

	// R+ M12 (2026-05-26) — GET /api/v1/certifications LIST. Parallel to
	// the legacy POST /api/certifications + GET /api/certifications/{id};
	// the v1 path serves the R+ /r/certifications screen with per-tenant
	// + optional learner_gcid + course_id filters. See
	// certifications_list_handler.go.
	mux.HandleFunc("/api/v1/certifications", logging(tenantRequired(CertificationsListHandler(deps))))

	// R+ M6 (2026-05-26) — GET /v1/scheduling/classes?week_iso=YYYY-MM-DD
	// (week-view) + POST (create). CHO-1626 Wave-2 follow-up adds the write
	// path; the /{id} subtree serves detail + reschedule + cancel. Surfaces
	// ScheduledClass aggregates owned by internal/domain/scheduling for the
	// R+ /r/scheduling calendar. See scheduling_handler.go.
	mux.HandleFunc("/v1/scheduling/classes", logging(tenantRequired(schedulingClassesHandler(deps))))
	mux.HandleFunc("/v1/scheduling/classes/", logging(tenantRequired(schedulingClassByIDHandler(deps))))

	// R+ M7 (2026-05-26) — /api/v1/exams CRUD for proctored sittings admin.
	// See exam_handler.go. examsSubHandler also dispatches DELETE/PUT
	// hooks that return 405 until the corresponding agent waves land.
	mux.HandleFunc("/api/v1/exams", logging(tenantRequired(examsRootHandler(deps))))
	mux.HandleFunc("/api/v1/exams/", logging(tenantRequired(examsSubHandler(deps))))

	// W4 Brick-1 + Brick-3 (ADR-190 D2) — Exam BC ExamForm/Result + Candidate
	// surfaces. Go 1.22 method+wildcard patterns are more-specific than the
	// /api/v1/exams/ subtree above, so they coexist without a mux conflict.
	examFormDeps := ExamFormDeps{Forms: deps.ExamForms, Results: deps.ExamResults}
	if deps.Publisher != nil {
		// W4 outcome-event seam (ADR-190 D1): tee ExamResultReleased into the
		// outbox. deps.Publisher is the TransactionalOutboxPublisher (main.go),
		// so PublishExamResultReleased enqueues a durable, idempotent outbox row.
		examFormDeps.Events = events.NewExamResultPublisher(deps.Publisher)
	}
	RegisterExamFormRoutes(mux, examFormDeps)

	// W5 FRANCHISE bypass-free slice (ADR-193 D1 + ADR-192 D1, CHO-2230):
	// the inbound satellite exam-result HMAC receiver + the write-restricted
	// franchise_satellite admin path. Both mount unconditionally and 503
	// fail-loud when unwired (the payments-receiver precedent). The receiver
	// does its OWN HMAC verify and takes the tenant from the SIGNED payload,
	// so it deliberately sits outside tenantRequired; it tees the same
	// ExamResultReleased outbox seam the internal record-result path uses.
	RegisterExamWebhookRoutes(mux, ExamWebhookDeps{
		Secret:  deps.ExamWebhookSecret,
		Events:  deps.ExamWebhookEvents,
		Forms:   deps.ExamForms,
		Results: deps.ExamResults,
		Publish: examFormDeps.Events,
	})
	RegisterFranchiseSatelliteRoutes(mux, FranchiseSatelliteDeps{Store: deps.FranchiseSatellites})

	if deps.ExamCandidateDeps != nil && deps.ExamCandidateDeps.Candidates != nil {
		RegisterExamCandidateRoutes(mux, deps.ExamCandidateDeps)
	}
	// W4 follow-up B (ADR-190 D2 + ADR-191) — Exam BC operational sitting surface.
	if deps.ExamSittingDeps != nil {
		RegisterExamSittingRoutes(mux, deps.ExamSittingDeps)
	}

	// R+ four-mode W1 (ADR-190, CHO-1849) — /api/v1/offerings CRUD for the
	// Offering aggregate (delivery_type instance). See offering_handler.go.
	mux.HandleFunc("/api/v1/offerings", logging(tenantRequired(offeringsRootHandler(deps))))
	mux.HandleFunc("/api/v1/offerings/", logging(tenantRequired(offeringsSubHandler(deps))))

	// R+ four-mode W2.A (ADR-190, CHO-1850) — universal-finder search for
	// offerings: keyset cursor + server multi-sort + facet counts + free-text.
	// See offering_handler.go (searchOfferingsHandler).
	mux.HandleFunc("/api/v1/search/offerings", logging(tenantRequired(searchOfferingsHandler(deps))))

	// ADR-216 WS-1 (CHO-1996) — /api/v1/credentials operator-curated Credential-
	// with-competencies catalogue: POST create (admin-gated) + GET list/read
	// (tenant-scoped, decoupled from courses). See credential_handler.go.
	mux.HandleFunc("/api/v1/credentials", logging(tenantRequired(credentialsRootHandler(deps))))
	mux.HandleFunc("/api/v1/credentials/", logging(tenantRequired(credentialsSubHandler(deps))))

	// R+ M13 (2026-05-26) — /api/v1/wbl-placements CRUD for work-based
	// learning placements. See wbl_handler.go. wblSubHandler dispatches
	// GET / PATCH / DELETE on /api/v1/wbl-placements/{id}.
	mux.HandleFunc("/api/v1/wbl-placements", logging(tenantRequired(wblRootHandler(deps.Wbl))))
	mux.HandleFunc("/api/v1/wbl-placements/", logging(tenantRequired(wblSubHandler(deps.Wbl))))

	// R+ (2026-05-26) — /api/v1/applications admin LIST + GET. Coexists
	// with /v1/me/applications (user-side); the admin variant is RBAC-gated
	// inside applicationsAdminHandler (training-admin / admin / instructor).
	mux.HandleFunc("/api/v1/applications", logging(tenantRequired(applicationsAdminHandler(deps))))
	mux.HandleFunc("/api/v1/applications/", logging(tenantRequired(applicationsAdminHandler(deps))))

	// R+ M15b (2026-05-26) — /api/v1/project-groups CRUD with action
	// dispatch (/submit + /grade). FSM state guards return 409. See
	// project_group_handler.go.
	mux.HandleFunc("/api/v1/project-groups", logging(tenantRequired(projectGroupsRootHandler(deps))))
	mux.HandleFunc("/api/v1/project-groups/", logging(tenantRequired(projectGroupsSubHandler(deps))))

	// R+ M15c (2026-05-26) — /api/v1/skillsfutures-claims CRUD with
	// approve/reject sub-actions. Mounted only when deps.SkillsFutures
	// is wired. See skillsfutures_handler.go.
	if deps.SkillsFutures != nil {
		mux.HandleFunc("/api/v1/skillsfutures-claims", logging(tenantRequired(skillsFuturesRootHandler(deps.SkillsFutures))))
		mux.HandleFunc("/api/v1/skillsfutures-claims/", logging(tenantRequired(skillsFuturesSubHandler(deps.SkillsFutures))))
	}

	// R+ M4 (2026-05-26) — GET /api/v1/rosters/{courseId} course-centric
	// learner roster READ VIEW. Both exact + subtree so the bare collection
	// 404s cleanly via the dispatcher's leaf-extraction guard. See
	// roster_handler.go.
	if deps.Rosters != nil {
		mux.HandleFunc("/api/v1/rosters", logging(tenantRequired(rostersSubHandler(deps.Rosters))))
		mux.HandleFunc("/api/v1/rosters/", logging(tenantRequired(rostersSubHandler(deps.Rosters))))
	}

	// R+ Wave-6 (2026-05-26) — /api/v1/live-quizzes COMPOSITE dispatcher.
	// THREE handlers share the subtree:
	//   κ liveQuizzesSubHandler        — GET/PATCH/POST/publish on {id}
	//   ι liveQuizSessionsRootHandler  — POST {quizId}/sessions
	//   μ LiveQuizWSHandler             — WS upgrade on {sessionId}/ws
	// WS path branches BEFORE tenantRequired (WS upgrade conflicts with
	// the JSON Content-Type middleware sets per μ's manifest note).
	if deps.LiveQuizzes != nil {
		mux.HandleFunc("/api/v1/live-quizzes", logging(tenantRequired(liveQuizzesRootHandler(deps))))
	}
	mux.HandleFunc("/api/v1/live-quizzes/", logging(liveQuizzesUnifiedDispatcher(deps)))

	// R+ Wave-5 M8 (ι 0d98d779) — /api/v1/classroom-sessions sub-handler.
	if deps.ClassroomSessions != nil {
		mux.HandleFunc("/api/v1/classroom-sessions", logging(tenantRequired(classroomSessionsSubHandler(deps))))
		mux.HandleFunc("/api/v1/classroom-sessions/", logging(tenantRequired(classroomSessionsSubHandler(deps))))
	}

	// R+ Wave-5 λ (2c029837) — /api/v1/surveys CRUD + responses.
	if deps.Surveys != nil {
		mux.HandleFunc("/api/v1/surveys", logging(tenantRequired(surveysRootHandler(deps))))
		mux.HandleFunc("/api/v1/surveys/", logging(tenantRequired(surveysSubHandler(deps))))
	}

	// R+ Wave-5/6 (μ 7c0c82cc + ADR-168) — /api/v1/live-polls surface:
	//   POST /api/v1/live-polls               — create DRAFT poll (instructor)
	//   GET  /api/v1/live-polls/{id}           — poll snapshot
	//   POST /api/v1/live-polls/{id}/open      — DRAFT → OPEN (instructor)
	//   POST /api/v1/live-polls/{id}/votes     — CastVote + fan-out (learner)
	//   POST /api/v1/live-polls/{id}/close     — OPEN → CLOSED (instructor)
	//   GET  /api/v1/live-polls/{id}/ws        — WS fan-out (bypasses tenantRequired)
	// The unified dispatcher branches the WS path BEFORE tenantRequired.
	if deps.LivePolls != nil {
		mux.HandleFunc("/api/v1/live-polls", logging(tenantRequired(livePollsRootHandler(deps))))
		mux.HandleFunc("/api/v1/live-polls/", logging(livePollsUnifiedDispatcher(deps)))
	}

	// ---- Phyllis MVP routes (Comic Ch5 P10 — Udemy moment) ------------
	// These coexist with /api/courses and use the public-catalogue
	// projection. Kept on the root path per docs/m13/phyllis-mvp-2026-05-08.md.
	if deps.Catalogue != nil && deps.Enrollments != nil {
		// /courses — public catalogue list + create (DEPRECATED: prefer /v1/courses)
		mux.HandleFunc("/courses", logging(tenantRequired(deprecated("/courses", "/v1/courses", catalogueHandler(deps)))))
		// /courses/{id} — single course detail (DEPRECATED)
		mux.HandleFunc("/courses/", logging(tenantRequired(deprecated("/courses/", "/v1/courses/", catalogueByIDHandler(deps)))))
		// /enrollments — same-identity enroll (Comic Ch5 P10 P3) (DEPRECATED)
		mux.HandleFunc("/enrollments", logging(tenantRequired(deprecated("/enrollments", "/v1/courses/{id}/enrolments", enrollmentsHandler(deps)))))
		// /me/enrollments — list current user's enrollments (Comic Ch4 P3) (DEPRECATED)
		mux.HandleFunc("/me/enrollments", logging(tenantRequired(deprecated("/me/enrollments", "/v1/me/enrolments", meEnrollmentsHandler(deps)))))

		// ---- S4.3: /v1/ path-prefix consolidation -----------------------
		// Per S4.3 brief — all routes consolidate under /v1/ for platform
		// consistency. Legacy paths above continue to respond with a
		// Deprecation header for one release window per
		// .claude/rules/git-workflow.md.

		// /v1/courses — POST create / GET list (Relay cursor pagination).
		// The list GET is exempt from tenantRequired for an anonymous
		// public-visibility browse (Phyllis Step 5 PublicDiscovery —
		// GET /api/catalog UNAUTHED); POST create stays tenant-gated.
		mux.HandleFunc("/v1/courses", logging(publicBrowseOrTenantRequired(v1CoursesHandler(deps))))
		// /v1/courses/{id} or /v1/courses/{id}/enrolments[/{enrolment_id}].
		// The single-course detail GET is exempt from tenantRequired (the
		// handler 404s non-public cross-tenant rows itself); PATCH +
		// enrolment sub-routes stay tenant-gated.
		mux.HandleFunc("/v1/courses/", logging(publicCourseDetailOrTenantRequired(v1CoursesSubHandler(deps))))
		// /v1/me/enrolments — list current user's enrolments
		mux.HandleFunc("/v1/me/enrolments", logging(tenantRequired(meEnrollmentsHandler(deps))))

		// /api/v1/instructors/{instructor_gcid}/courses — list courses
		// authored by an instructor (FE A6 instructor-roster surface; closes
		// debt #4). tenantRequired enforces X-Tenant-Id; the handler does its
		// own gcid + role gate. Cross-tenant rows are filtered by RLS in the
		// pg adapter; the in-memory adapter filters in-process.
		mux.HandleFunc("/api/v1/instructors/", logging(tenantRequired(instructorCoursesHandler(deps))))
	}

	// ---- S4.3: BE-CO2 Campus Operations -------------------------------
	if deps.CampusOps != nil {
		// R+ M15a (2026-05-26): campusRootHandler dispatches by method —
		// GET → campusListHandler (new R+ list surface for /r/campusops),
		// POST → campusHandler (legacy create, behaviour-preserving).
		// See campus_list_handler.go.
		mux.HandleFunc("/v1/campus", logging(tenantRequired(campusRootHandler(deps))))
		mux.HandleFunc("/v1/campus/", logging(tenantRequired(campusByIDHandler(deps))))
	}

	// ---- CHO-2191 SP1: Rooms (durable, tenant-scoped) -----------------
	// POST create + GET list. Admin-gated (instructor / admin / training-admin).
	// Always mounted; roomsRootHandler returns 503 when deps.Rooms is nil (the
	// route must exist so the FE gets a clean 503, not a 404, pre-wiring).
	mux.HandleFunc("/api/v1/rooms", logging(tenantRequired(roomsRootHandler(deps))))
	// CHO-2332: item route - PATCH/PUT (edit) + DELETE (soft-delete). Same
	// middleware + admin gate as the collection route; roomByIDHandler 503s when
	// deps.Rooms is nil and 405s any non-PUT/PATCH/DELETE method.
	mux.HandleFunc("/api/v1/rooms/", logging(tenantRequired(roomByIDHandler(deps))))

	// ---- S6.1: Course Application UX ----------------------------------
	// Routes per docs/design/ux_course_application.md (7 endpoints, 1 form).
	if deps.Applications != nil {
		// /v1/me/applications — list (GET) + submit (POST, idempotent)
		mux.HandleFunc("/v1/me/applications", logging(tenantRequired(meApplicationsHandler(deps))))
		// /v1/me/applications/{id} | …/accept-offer | …/withdraw | …/invoice
		mux.HandleFunc("/v1/me/applications/", logging(tenantRequired(meApplicationsSubHandler(deps))))
	}

	// ---- Lane A (B-FE-X5): A+ X.2 test-set authoring -------------------
	// Routes per chora-contracts/openapi/delivery-test-sets.yaml.
	if deps.TestSets != nil {
		// POST /api/v1/test-sets — createTestSet
		mux.HandleFunc("/api/v1/test-sets", logging(tenantRequired(testSetsRootHandler(deps))))
		// GET /{id} + question CRUD + /publish
		mux.HandleFunc("/api/v1/test-sets/", logging(tenantRequired(testSetsSubHandler(deps))))
	}

	// ---- Lane B (B-FE-X5 + ADR-155): assessments + submissions --------
	// Routes per chora-contracts/openapi/delivery-assessments.yaml. The
	// AssessmentDeps must be non-nil + carry both repos for the 11 routes
	// to mount.
	if deps.AssessmentDeps != nil && deps.AssessmentDeps.Assessments != nil && deps.AssessmentDeps.Submissions != nil {
		// Instructor surface.
		// Method-dispatcher root: GET → listAssessments (E2E-BE-2),
		// POST → createAssessment. See assessmentsRootHandler.
		mux.HandleFunc("/api/v1/assessments", logging(tenantRequired(assessmentsRootHandler(deps.AssessmentDeps))))
		mux.HandleFunc("/api/v1/assessments/", logging(tenantRequired(assessmentSubHandler(deps.AssessmentDeps))))
		// Learner surface.
		mux.HandleFunc("/api/v1/me/assessments", logging(tenantRequired(myAssessmentsHandler(deps.AssessmentDeps))))
		mux.HandleFunc("/api/v1/me/assessments/", logging(tenantRequired(myAssessmentSubHandler(deps.AssessmentDeps))))
	}

	// W7 StudentModuleProgress — A+ learner course-only self view (CHO-2074).
	//   GET /api/v1/me/module-progress?course_id=X  (enrolment-gated, own rows)
	mux.HandleFunc("/api/v1/me/module-progress", logging(tenantRequired(func(w http.ResponseWriter, r *http.Request) {
		handleMeModuleProgress(deps, w, r)
	})))

	// ---- Fix-E (ADR-155 Lane B completion-event) -----------------------
	// Pub/Sub push subscription endpoint for
	// chora.delivery.grading.oe_batch_completed.v1. Restricted to
	// mesh-internal callers via NetworkPolicy + Istio authz; the handler
	// itself enforces OIDC via the eventpush.Verifier wired at boot.
	if deps.GradingInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/grading-inbox", deps.GradingInboxPushHandler)
	}

	// Lane 1c W4 (CHO-1703 / ADR-180 D10) — Pub/Sub push subscription
	// endpoint for chora.creation.question_batch.accepted.v1. The
	// BatchTestSetSubscriber assembles ONE DRAFT test set per batch job
	// (idempotent on test_sets.source_job_id). Same mesh-internal +
	// OIDC-verifier posture as the grading inbox.
	if deps.BatchTestSetInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/batch-testset-inbox", deps.BatchTestSetInboxPushHandler)
	}

	// Pub/Sub push subscription endpoint for the 4 chora.payments.* events
	// chora-delivery subscribes to (ADR-164 Stage C, 2026-05-24). All 4
	// subscription bindings POST here; the handler routes by the Pub/Sub
	// `topic` attribute to the typed PaymentsSubscriber method. Restricted
	// to mesh-internal callers via NetworkPolicy + Istio authz; OIDC
	// enforcement via the eventpush.Verifier wired at boot.
	if deps.PaymentsInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/payments-inbox", deps.PaymentsInboxPushHandler)
	}

	// Pub/Sub push subscription endpoint for the Q3 name projection
	// (chora.identity.user.profile_updated.v1 → chora_delivery.user_directory).
	// Same mesh-internal + OIDC-verifier posture as the payments inbox.
	if deps.IdentityProfileInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/identity-profile-inbox", deps.IdentityProfileInboxPushHandler)
	}

	// W7 StudentModuleProgress projection (CHO-2074) — two single-topic push
	// endpoints, one per completion event, both feeding the same projection.
	// Same mesh-internal + OIDC-verifier posture as the identity-profile inbox.
	if deps.ModuleProgressAtomInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/module-progress-atom-inbox", deps.ModuleProgressAtomInboxPushHandler)
	}
	if deps.ModuleProgressGradedInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/module-progress-graded-inbox", deps.ModuleProgressGradedInboxPushHandler)
	}
	if deps.CompletionReleasedInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/completion-released-inbox", deps.CompletionReleasedInboxPushHandler)
	}
	if deps.ExamResultReleasedInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/exam-result-released-inbox", deps.ExamResultReleasedInboxPushHandler)
	}
	// ASYNC-analytics progress projection (CHO-1827): two single-topic Pub/Sub
	// push endpoints feeding the same CourseLearnerProgress projection.
	//
	// The paths are literals, matching every sibling mount above, because the
	// registry guard (pubsubinbox/mounts_contract_test.go) is a STATIC SOURCE SCAN
	// for `.Handle("/api/internal/pubsub/{inbox}"`, it cannot resolve a constant.
	// Routing them via pubsubinbox.Path() would read as tidier and make the guard
	// blind to exactly these two routes, which is how the chora-sharing lanes
	// dead-lettered for two weeks. The literal is safe BECAUSE that test compares
	// it against the registry, in both directions.
	if deps.CourseProgressAdvancedInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/course-progress-advanced-inbox", deps.CourseProgressAdvancedInboxPushHandler)
	}
	if deps.CourseProgressCompletedInboxPushHandler != nil {
		mux.Handle("/api/internal/pubsub/course-progress-completed-inbox", deps.CourseProgressCompletedInboxPushHandler)
	}

	// ---- E2E-BE-CJ2: Course authoring → review → release ---------------
	// Routes per chora-contracts/openapi/delivery-courses.yaml. The CJ#2
	// port must be non-nil + carry a Courses adapter for the 6 routes to
	// mount. tenantRequired enforces X-Tenant-Id; per-handler RBAC checks
	// gcid + x-mesh-user-roles for instructor / training-admin / author.
	if deps.CourseCJ2 != nil && deps.CourseCJ2.Courses != nil {
		mux.HandleFunc("/api/v1/courses", logging(tenantRequired(cj2CoursesRootHandler(deps.CourseCJ2))))
		mux.HandleFunc("/api/v1/courses/", logging(tenantRequired(cj2CoursesSubHandler(deps.CourseCJ2))))
	}

	// ADR-185 — learner-scoped, enrolment-gated signed-GET for course media.
	// GET /v1/me/courses/{course_id}/content/media-urls. The BFF
	// (chora-gateway MeCourseContent) calls this in parallel with the
	// chora-consumption curriculum projection and merges by item_id (consumption
	// cannot sign delivery's bucket — cross-DB forbidden). Mounted only when the
	// course-content service + signer + enrolment port are all wired.
	if deps.Enrollments != nil && deps.CourseCJ2 != nil && deps.CourseCJ2.Content != nil &&
		deps.CourseCJ2.Content.Svc != nil {
		// CHO-2351 - the same seam resolves `assessment` curriculum refs
		// (test_set_id) to the assessment id this learner may open. Nil when the
		// assessment deps are unwired: assessment refs then stay raw, which
		// leaves the A+ CTA visibly broken rather than silently wrong.
		var learnerAssessments LearnerAssessmentLister
		if deps.AssessmentDeps != nil && deps.AssessmentDeps.Assessments != nil {
			learnerAssessments = deps.AssessmentDeps.Assessments
		}
		mux.HandleFunc("/v1/me/courses/", logging(tenantRequired(
			meCourseMediaHandler(deps.Enrollments, deps.CourseCJ2.Content, learnerAssessments))))
	}

	// CJ#2 Stripe webhook DELETED per ADR-164 Stage C (2026-05-24) — the
	// canonical webhook ingress moved to chora-payments. /v1/webhooks/stripe
	// is no longer mounted by chora-delivery. Cloud Armor priority-101 allow
	// rule for `/v1/webhooks/stripe/course-checkout` is dead code (Infra
	// removes it post-cutover).

	return mux
}

// -----------------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------------

// logging wraps a handler with structured-log emission per request.
func logging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tc := observability.FromRequest(r)
		// Echo traceparent so callers can correlate.
		w.Header().Set("traceparent", tc.TraceparentHeader())
		sw := &statusWriter{ResponseWriter: w}
		next(sw, r)
		observability.LogRequest(
			tc,
			r.Header.Get("X-Tenant-Id"),
			r.Header.Get("gcid"),
			r.Method,
			r.URL.Path,
			sw.status,
			time.Since(start),
		)
	}
}

// tenantRequired enforces presence of X-Tenant-Id and stores it in the
// request context via the path of least resistance — header passthrough.
// We deliberately do NOT pull in pkg/context for this skeleton.
func tenantRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-Tenant-Id")) == "" {
			writeError(w, http.StatusBadRequest, "X-Tenant-Id header required")
			return
		}
		next(w, r)
	}
}

// isPublicVisibilityBrowse reports whether the request is an anonymous
// public-catalogue browse: a GET whose `visibility` query param is `public`
// or `tenant_or_public`. These two filters surface only public-visibility
// rows when no tenant scope is supplied (see domain.visibilityMatch), so the
// browse is safe to serve cross-tenant + unauthenticated. Every other
// visibility value — and every non-GET method — is a tenant-scoped operation.
func isPublicVisibilityBrowse(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Query().Get("visibility") {
	case "public", "tenant_or_public":
		return true
	default:
		return false
	}
}

// publicBrowseOrTenantRequired gates a handler with tenantRequired EXCEPT
// when the request is an anonymous public-visibility browse (Phyllis Step 5
// PublicDiscovery — GET /api/catalog UNAUTHED fans out here with no
// X-Tenant-Id). Public courses are cross-tenant readable; the wrapped
// handlers still enforce tenant isolation for tenant_only + private rows on
// their own (handleV1CourseList passes the empty TenantID straight into the
// visibility filter; handleV1CourseDetail 404s non-public cross-tenant rows).
//
// All write paths (POST create, PATCH update, POST/DELETE enrolments) stay
// fully tenantRequired-gated — the exemption is read-only.
func publicBrowseOrTenantRequired(next http.HandlerFunc) http.HandlerFunc {
	guarded := tenantRequired(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if isPublicVisibilityBrowse(r) {
			next(w, r)
			return
		}
		guarded(w, r)
	}
}

// isCourseDetailLookup reports whether the request is a single-course detail
// GET — exactly `/v1/courses/{id}` with no sub-resource segment. Used to
// exempt anonymous public-course detail lookups from tenantRequired: the
// /v1/courses/{id} handler is self-gating (it 404s any non-public row whose
// tenant_id does not match the caller's), so a missing X-Tenant-Id simply
// means only public rows resolve. Sub-resources (`…/enrolments`,
// `…/application-form`) are tenant-scoped and stay gated.
func isCourseDetailLookup(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/courses/")
	rest = strings.Trim(rest, "/")
	return rest != "" && !strings.Contains(rest, "/")
}

// publicCourseDetailOrTenantRequired is the /v1/courses/{id} counterpart of
// publicBrowseOrTenantRequired: it exempts the self-gating single-course
// detail GET from tenantRequired so anonymous callers can resolve a public
// course by id. PATCH + enrolment sub-routes remain tenantRequired-gated.
func publicCourseDetailOrTenantRequired(next http.HandlerFunc) http.HandlerFunc {
	guarded := tenantRequired(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if isCourseDetailLookup(r) {
			next(w, r)
			return
		}
		guarded(w, r)
	}
}

// statusWriter captures the response status for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader stamps the status code and forwards.
func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// Write defaults status to 200 if not yet set.
func (sw *statusWriter) Write(p []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	return sw.ResponseWriter.Write(p)
}

// Hijack delegates to the wrapped ResponseWriter so the logging middleware
// does not break WebSocket upgrades (ADR-168 live-quiz/live-poll /ws routes).
// golang.org/x/net/websocket requires the ResponseWriter to implement
// http.Hijacker; without this, the upgrade panics with
// "*statusWriter is not http.Hijacker". Returns http.ErrNotSupported when the
// underlying writer can't hijack (e.g. HTTP/2) so callers fail cleanly.
func (sw *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := sw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// -----------------------------------------------------------------------------
// JSON helpers
// -----------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{
		"error":   http.StatusText(status),
		"message": msg,
	})
}

// persistOK reports whether a store Save succeeded; on error it writes a 500
// and returns false so the caller returns. ADR-168 gap 1: a Postgres-backed
// SessionStore/QuizStore can fail, and a silently-dropped Save would lose a
// live session — so every Save call site checks this (the inmem impl never
// errors, so it's a no-op there).
func persistOK(w http.ResponseWriter, err error) bool {
	if err != nil {
		writeError(w, http.StatusInternalServerError, "persist failed: "+err.Error())
		return false
	}
	return true
}

func decodeBody(r *http.Request, v interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------------

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": observability.ServiceName,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func readyHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ready",
		"service": observability.ServiceName,
	})
}

// -----------------------------------------------------------------------------
// Courses
// -----------------------------------------------------------------------------

type createCourseReq struct {
	Title       string   `json:"title"`
	AtomIDs     []string `json:"atom_ids"`
	MaxCapacity int      `json:"max_capacity"`
}

func coursesHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var req createCourseReq
			if err := decodeBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			capacity := req.MaxCapacity
			if capacity == 0 {
				capacity = 1 // skeleton default — keeps demo curl happy
			}
			tenantID := r.Header.Get("X-Tenant-Id")
			c, err := domain.NewCourse(tenantID, req.Title, req.AtomIDs, capacity)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			// ADR-236 D1 — decorate ctx with tenant_id so a pg CourseRepo's
			// rls.ApplySession picks it up via tracing.TenantIDFromContext (the
			// GUC comes from the validated session context, never a param —
			// rls.ApplySession ignores anything passed positionally).
			ctx := tracing.WithTenantID(r.Context(), tenantID)
			if err := deps.Courses.Save(ctx, c); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, courseDTO(c))

		case http.MethodGet:
			offset, limit := paging(r)
			tenantID := r.Header.Get("X-Tenant-Id")
			ctx := tracing.WithTenantID(r.Context(), tenantID)
			items, total, err := deps.Courses.ListByTenant(ctx, tenantID, offset, limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			out := make([]map[string]interface{}, 0, len(items))
			for _, c := range items {
				out = append(out, courseDTO(c))
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"items": out,
				"total": total,
			})

		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// courseSubHandler dispatches /api/courses/{id}.
func courseSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/courses/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		courseID := parts[0]

		// /api/courses/{id}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				// ADR-236 D1 — Get is now tenant-scoped (ctx + explicit tenantID
				// param); the pg adapter enforces it via SQL WHERE tenant_id=$2 +
				// RLS, the in-memory adapter via an explicit compare. The manual
				// post-fetch `c.TenantID != header` compare this replaced is now
				// redundant (a hit can only come back for the caller's own
				// tenant) — dropped. c.DeletedAt is kept as defence-in-depth.
				tenantID := r.Header.Get("X-Tenant-Id")
				ctx := tracing.WithTenantID(r.Context(), tenantID)
				c, ok, err := deps.Courses.Get(ctx, tenantID, courseID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				if !ok || c.DeletedAt != nil {
					writeError(w, http.StatusNotFound, "course not found")
					return
				}
				writeJSON(w, http.StatusOK, courseDTO(c))
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	}
}

// -----------------------------------------------------------------------------
// Bookings
// -----------------------------------------------------------------------------

type createBookingReq struct {
	ClassID     string `json:"class_id"`
	LearnerGCID string `json:"learner_gcid"`
}

func bookingsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listBookings(deps, w, r)
		case http.MethodPost:
			createBooking(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// listBookings serves GET /api/bookings — the tenant's active bookings as a
// {items,total} envelope (BookingList contract). HANDOFF_RPLUS §6 follow-up:
// the FE (CHO-1620 T3) previously session-tracked created bookings because no
// GET-list route existed.
func listBookings(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Bookings == nil {
		writeError(w, http.StatusServiceUnavailable, "bookings store not wired")
		return
	}
	tenantID := r.Header.Get("X-Tenant-Id")
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	bookings, err := deps.Bookings.ListByTenant(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]interface{}, 0, len(bookings))
	for _, b := range bookings {
		items = append(items, bookingDTO(b))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

func createBooking(deps Deps, w http.ResponseWriter, r *http.Request) {
	var req createBookingReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if deps.Scheduling == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduling repo not wired")
		return
	}
	tenantID := r.Header.Get("X-Tenant-Id")
	// RLS: the durable ScheduledClass + booking stores read the tenant from
	// ctx (tracing.TenantIDFromContext), so stamp it BEFORE either query, or
	// rls.ApplySession binds an empty tenant (the ctx-vs-param RLS trap).
	ctx := tracing.WithTenantID(r.Context(), tenantID)

	// ADR-236 D2: resolve the class from the DURABLE ScheduledClass store (the
	// canonical store the R+ calendar writes), not the retired ephemeral
	// in-memory delivery.Class. Get is RLS-scoped in prod; the explicit
	// TenantID check is defence-in-depth (and the tenant filter for the
	// in-memory dev store).
	cls, ok, err := deps.Scheduling.Get(ctx, req.ClassID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || cls.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "class not found")
		return
	}

	b, err := domain.NewBookingForClass(cls.ID, cls.CourseID, cls.TenantID, req.LearnerGCID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Durable, cross-pod-correct capacity gate: counts the class's active
	// bookings under a per-class advisory lock and inserts iff under capacity
	// — replaces the retired single-pod in-memory Class.reserveSeat mutex.
	if err := deps.Bookings.ReserveSeatAndSave(ctx, cls.ID, cls.MaxCapacity, b); err != nil {
		if errors.Is(err, domain.ErrClassAtCapacity) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, bookingDTO(b))
}

type updateBookingStatusReq struct {
	Status string `json:"status"`
}

func bookingSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/bookings/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[1] != "status" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		bookingID := parts[0]
		tenantID := r.Header.Get("X-Tenant-Id")
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		b, ok, err := deps.Bookings.Get(ctx, bookingID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "booking lookup failed: "+err.Error())
			return
		}
		if !ok || b.TenantID != tenantID {
			writeError(w, http.StatusNotFound, "booking not found")
			return
		}
		var req updateBookingStatusReq
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		next, err := parseBookingStatus(req.Status)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := b.TransitionStatus(next); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err := deps.Bookings.Save(ctx, b); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Emit booking.confirmed.v1 when the transition is INTO confirmed.
		// AsyncAPI contract: chora-contracts/asyncapi/delivery/booking.confirmed.v1.yaml.
		if next == domain.BookingStatusConfirmed && deps.Publisher != nil {
			_, _ = deps.Publisher.PublishBookingConfirmed(events.BookingConfirmed{
				TenantID:    b.TenantID,
				GCID:        b.LearnerID,
				BookingID:   b.ID,
				CourseID:    b.CourseID,
				ClassID:     b.ClassID,
				LearnerGCID: b.LearnerID,
				Traceparent: r.Header.Get("traceparent"),
			})
		}

		writeJSON(w, http.StatusOK, bookingDTO(b))
	}
}

func parseBookingStatus(s string) (domain.BookingStatus, error) {
	switch domain.BookingStatus(s) {
	case domain.BookingStatusPending,
		domain.BookingStatusConfirmed,
		domain.BookingStatusAttended,
		domain.BookingStatusNoShow:
		return domain.BookingStatus(s), nil
	default:
		return "", fmt.Errorf("unknown status %q (allowed: pending, confirmed, attended, no-show)", s)
	}
}

// -----------------------------------------------------------------------------
// Certifications
// -----------------------------------------------------------------------------

type issueCertReq struct {
	LearnerGCID     string   `json:"learner_gcid"`
	CourseID        string   `json:"course_id"`
	Accomplishments []string `json:"accomplishments"`
}

// CourseCertLookupPort reads a course (incl. its CertDefinition) so the
// issuance chain can consume the course-defined cert criteria. Satisfied by
// the pg CJ#2 course port (CourseRepoCJ2Port).
type CourseCertLookupPort interface {
	Get(ctx context.Context, tenantID, courseID string) (*domain.Course, bool, error)
}

// consumeCourseCertType returns accomplishments enriched with the course's
// defined cert_type when the course defines an enabled cert — the issuance
// chain CONSUMING the course's cert definition (CHO-1795). Nil lookup / no
// cert / lookup error ⇒ accomplishments returned unchanged (fail-soft: a
// missing definition never blocks an otherwise-valid issuance).
func consumeCourseCertType(ctx context.Context, deps Deps, tenantID, courseID string, accomplishments []string) []string {
	if deps.CourseCertLookup == nil {
		return accomplishments
	}
	c, ok, err := deps.CourseCertLookup.Get(tracing.WithTenantID(ctx, tenantID), tenantID, courseID)
	if err != nil || !ok || c == nil || !c.Certification.Enabled || c.Certification.CertType == "" {
		return accomplishments
	}
	marker := "cert_type:" + string(c.Certification.CertType)
	for _, a := range accomplishments {
		if a == marker {
			return accomplishments // idempotent
		}
	}
	return append([]string{marker}, accomplishments...)
}

func certificationsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req issueCertReq
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		tenantID := r.Header.Get("X-Tenant-Id")
		// CHO-1795 — consume the course's cert definition (stamp cert_type).
		accomplishments := consumeCourseCertType(r.Context(), deps, tenantID, req.CourseID, req.Accomplishments)
		cert, err := deps.Certifications.IssueCtx(
			r.Context(),
			tenantID,
			req.LearnerGCID,
			req.CourseID,
			nil, // manual issue records no score
			accomplishments,
		)
		if err != nil {
			if errors.Is(err, domain.ErrCertAlreadyIssued) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Emit certification.issued.v1 — AsyncAPI contract:
		// chora-contracts/asyncapi/delivery/certification.issued.v1.yaml.
		if deps.Publisher != nil {
			_, _ = deps.Publisher.PublishCertificationIssued(events.CertificationIssued{
				TenantID:        cert.TenantID,
				GCID:            cert.LearnerID,
				CertificationID: cert.ID,
				CourseID:        cert.CourseID,
				LearnerGCID:     cert.LearnerID,
				Hash:            cert.Hash,
				Traceparent:     r.Header.Get("traceparent"),
			})
		}

		writeJSON(w, http.StatusCreated, certDTO(cert))
	}
}

func certificationSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		certID := strings.TrimPrefix(r.URL.Path, "/api/certifications/")
		if certID == "" || strings.Contains(certID, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		cert, ok, _ := deps.Certifications.GetCtx(r.Context(), r.Header.Get("X-Tenant-Id"), certID)
		if !ok || cert.TenantID != r.Header.Get("X-Tenant-Id") {
			writeError(w, http.StatusNotFound, "certification not found")
			return
		}
		writeJSON(w, http.StatusOK, certDTO(cert))
	}
}

// -----------------------------------------------------------------------------
// DTOs (JSON shape)
// -----------------------------------------------------------------------------

func courseDTO(c *domain.Course) map[string]interface{} {
	out := map[string]interface{}{
		"id":           c.ID,
		"tenant_id":    c.TenantID,
		"title":        c.Title,
		"atom_ids":     c.AtomIDs,
		"max_capacity": c.MaxCapacity,
		"created_at":   c.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":   c.UpdatedAt.Format(time.RFC3339Nano),
	}
	if c.DeletedAt != nil {
		out["deleted_at"] = c.DeletedAt.Format(time.RFC3339Nano)
	}
	if c.AtomIDs == nil {
		out["atom_ids"] = []string{}
	}
	return out
}

func bookingDTO(b *domain.Booking) map[string]interface{} {
	out := map[string]interface{}{
		"id":           b.ID,
		"class_id":     b.ClassID,
		"course_id":    b.CourseID,
		"tenant_id":    b.TenantID,
		"learner_gcid": b.LearnerID,
		"status":       string(b.Status),
		"created_at":   b.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":   b.UpdatedAt.Format(time.RFC3339Nano),
	}
	if b.DeletedAt != nil {
		out["deleted_at"] = b.DeletedAt.Format(time.RFC3339Nano)
	}
	return out
}

func certDTO(c *domain.Certification) map[string]interface{} {
	return map[string]interface{}{
		"id":              c.ID,
		"tenant_id":       c.TenantID,
		"learner_gcid":    c.LearnerID,
		"course_id":       c.CourseID,
		"accomplishments": c.Accomplishments,
		"hash":            c.Hash,
		"issued_at":       c.IssuedAt.Format(time.RFC3339Nano),
	}
}

// -----------------------------------------------------------------------------
// Misc
// -----------------------------------------------------------------------------

func paging(r *http.Request) (offset, limit int) {
	limit = 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 200 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return offset, limit
}

// -----------------------------------------------------------------------------
// Phyllis MVP — public catalogue + enrollment (Comic Ch5 P10)
// -----------------------------------------------------------------------------

type createPublicCourseReq struct {
	Title           string   `json:"title"`
	SyllabusOutline []string `json:"syllabus_outline"`
	Tags            []string `json:"tags"`
	PriceSGDCents   int32    `json:"price_sgd_cents"`
	Public          bool     `json:"public"`
	SFEligible      bool     `json:"sf_eligible"`
	InstructorName  string   `json:"instructor_name"`
}

// catalogueHandler dispatches GET (list) + POST (create) on /courses.
//
// GET supports query params:
//   - public=true|false (default: any)
//   - q=<substring> (case-insensitive title + tags)
//   - page=N (1-indexed; default 1)
//   - per=M (default 20)
//
// POST creates a course; the gcid header is required (instructor identity).
func catalogueHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleCatalogueList(deps, w, r)
		case http.MethodPost:
			handleCatalogueCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleCatalogueList(deps Deps, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pf := domain.PublicFilterAny
	switch q.Get("public") {
	case "true":
		pf = domain.PublicFilterTrue
	case "false":
		pf = domain.PublicFilterFalse
	}
	page := 1
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			page = n
		}
	}
	per := 20
	if v := q.Get("per"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 200 {
			per = n
		}
	}
	items, total, err := deps.Catalogue.Search(r.Context(), domain.CatalogueQuery{
		TenantID: r.Header.Get("X-Tenant-Id"),
		Public:   pf,
		Q:        q.Get("q"),
		Page:     page,
		Per:      per,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue read failed")
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, pc := range items {
		// Refresh enrolled count from the live registry projection.
		// Display count degrades gracefully to 0 on DB error so the
		// catalogue list still renders.
		// pg.EnrollmentRepo's RLS read needs tenant in ctx (rls.ApplySession
		// reads tracing.TenantIDFromContext); use the course's tenant so
		// COUNT(*) returns rows for that tenant rather than failing silently
		// with ErrNoTenantContext + degrading to 0 — surfaced 2026-05-26
		// during the CJ#2 e2e smoke as a stale "0 enrolled" badge.
		countCtx := tracing.WithTenantID(r.Context(), pc.TenantID)
		n, _ := deps.Enrollments.CountByCourse(countCtx, pc.TenantID, pc.ID)
		count := int32(n)
		out = append(out, publicCourseDTO(pc, count))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
		"total": total,
		"page":  page,
		"per":   per,
	})
}

func handleCatalogueCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	gcid := strings.TrimSpace(r.Header.Get("gcid"))
	if gcid == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required (instructor identity)")
		return
	}
	var req createPublicCourseReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:        r.Header.Get("X-Tenant-Id"),
		Title:           req.Title,
		InstructorGCID:  gcid,
		InstructorName:  req.InstructorName,
		PriceSGDCents:   req.PriceSGDCents,
		Public:          req.Public,
		SFEligible:      req.SFEligible,
		Tags:            req.Tags,
		SyllabusOutline: req.SyllabusOutline,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.Catalogue.Save(r.Context(), pc); err != nil {
		writeError(w, http.StatusInternalServerError, "catalogue write failed")
		return
	}

	// Best-effort event publish; the in-memory publisher never fails for
	// well-formed inputs but we still log the outcome.
	if deps.Publisher != nil {
		if _, perr := deps.Publisher.PublishCourseCreated(events.CourseCreated{
			TenantID:       pc.TenantID,
			GCID:           gcid,
			CourseID:       pc.ID,
			Title:          pc.Title,
			InstructorGCID: gcid,
			Public:         pc.Public,
			PriceSGDCents:  pc.PriceSGDCents,
			Traceparent:    r.Header.Get("traceparent"),
		}); perr != nil {
			// Production fallback: outbox-pattern at M12+. Surface in logs only.
			_ = perr
		}
	}
	writeJSON(w, http.StatusCreated, publicCourseDTO(pc, 0))
}

// catalogueByIDHandler serves GET /courses/{id} — single course detail.
func catalogueByIDHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/courses/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		pc, ok, err := deps.Catalogue.Get(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "catalogue read failed")
			return
		}
		if !ok || pc.TenantID != r.Header.Get("X-Tenant-Id") {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		// pg.EnrollmentRepo's RLS read needs tenant in ctx (rls.ApplySession
		// reads tracing.TenantIDFromContext); use the course's tenant so
		// COUNT(*) returns rows for that tenant rather than failing silently
		// with ErrNoTenantContext + degrading to 0 — surfaced 2026-05-26
		// during the CJ#2 e2e smoke as a stale "0 enrolled" badge.
		countCtx := tracing.WithTenantID(r.Context(), pc.TenantID)
		n, _ := deps.Enrollments.CountByCourse(countCtx, pc.TenantID, pc.ID)
		count := int32(n)
		writeJSON(w, http.StatusOK, publicCourseDTO(pc, count))
	}
}

type createEnrollmentReq struct {
	CourseID string `json:"course_id"`
	GCID     string `json:"gcid"`
}

// enrollmentsHandler serves POST /enrollments — same-identity enrol
// (Comic Ch5 P10 P3 "I just enrolled. As me. Same session.").
//
// Idempotent on (course_id, gcid): a duplicate POST returns 200 with the
// existing enrollment row (NOT a 409, NOT a fresh row).
func enrollmentsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req createEnrollmentReq
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Resolve gcid: body wins; fall back to header (auth-derived).
		gcid := strings.TrimSpace(req.GCID)
		if gcid == "" {
			gcid = strings.TrimSpace(r.Header.Get("gcid"))
		}
		if gcid == "" {
			writeError(w, http.StatusBadRequest, "gcid required (body or header)")
			return
		}
		tenantID := r.Header.Get("X-Tenant-Id")

		// Verify the course exists in this tenant — same-identity invariant
		// only applies to real courses.
		pc, ok, err := deps.Catalogue.Get(r.Context(), req.CourseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "catalogue read failed")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}

		// Decorate ctx with tenant_id so pg.EnrollmentRepo's
		// rls.ApplySession picks it up via tracing.TenantIDFromContext.
		ctx := tracing.WithTenantID(r.Context(), tenantID)

		// ADR-226 §4 — block self-enrol while a hard_gate prerequisite is unmet
		// (same-tenant only; cross-tenant public enrol is a tracked follow-up).
		// This deprecated path is a passthrough (not a redirect), so it must gate
		// too — else it is a bypass of the v1 gate.
		if pc.TenantID == tenantID {
			unmet, gerr := unmetEnrolHardGates(deps, ctx, tenantID, req.CourseID, gcid)
			if gerr != nil {
				writeError(w, http.StatusInternalServerError, "prerequisite check failed")
				return
			}
			if len(unmet) > 0 {
				writeEnrolHardGateBlocked(w, unmet)
				return
			}
		}

		// Detect existing enrollment to choose status code (201 vs 200).
		_, existed, err := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, req.CourseID, gcid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "enrollment lookup failed")
			return
		}

		e, err := deps.Enrollments.Register(ctx, tenantID, req.CourseID, gcid)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Bump enrolled count + publish event only on a fresh insert.
		status := http.StatusOK
		if !existed {
			if pc, ok, gerr := deps.Catalogue.Get(r.Context(), req.CourseID); gerr == nil && ok {
				pc.IncrementEnrolled()
				_ = deps.Catalogue.Save(r.Context(), pc)
			}
			if deps.Publisher != nil {
				if _, perr := deps.Publisher.PublishEnrollmentCreated(events.EnrollmentCreated{
					TenantID:     tenantID,
					GCID:         gcid,
					EnrollmentID: e.ID,
					CourseID:     e.CourseID,
					LearnerGCID:  gcid,
					Traceparent:  r.Header.Get("traceparent"),
				}); perr != nil {
					_ = perr
				}
			}
			status = http.StatusCreated
		}
		writeJSON(w, status, enrollmentDTO(e))
	}
}

// meEnrollmentsHandler serves GET /me/enrollments — list current learner's
// enrolments. The gcid is read from the `gcid` header (auth-derived).
func meEnrollmentsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		gcid := strings.TrimSpace(r.Header.Get("gcid"))
		if gcid == "" {
			writeError(w, http.StatusUnauthorized, "gcid header required")
			return
		}
		tenantID := r.Header.Get("X-Tenant-Id")
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		entries, err := deps.Enrollments.ListByGCID(ctx, tenantID, gcid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "enrollment list failed")
			return
		}
		out := make([]map[string]interface{}, 0, len(entries))
		for _, e := range entries {
			out = append(out, enrollmentDTO(e))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items": out,
			"total": len(out),
		})
	}
}

// -----------------------------------------------------------------------------
// DTOs (Phyllis MVP)
// -----------------------------------------------------------------------------

func publicCourseDTO(pc *domain.PublicCourse, enrolledCount int32) map[string]interface{} {
	out := map[string]interface{}{
		"id":                     pc.ID,
		"tenant_id":              pc.TenantID,
		"title":                  pc.Title,
		"instructor_gcid":        pc.InstructorGCID,
		"instructor_name":        pc.InstructorName,
		"price_sgd_cents":        pc.PriceSGDCents,
		"is_free":                pc.IsFree(),
		"public":                 pc.Public,
		"sf_eligible":            pc.SFEligible,
		"tags":                   pc.Tags,
		"syllabus_outline_count": pc.SyllabusOutlineCount(),
		"enrolled_count":         enrolledCount,
		"created_at":             pc.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":             pc.UpdatedAt.Format(time.RFC3339Nano),
	}
	if pc.Tags == nil {
		out["tags"] = []string{}
	}
	if pc.DeletedAt != nil {
		out["deleted_at"] = pc.DeletedAt.Format(time.RFC3339Nano)
	}
	return out
}

func enrollmentDTO(e *domain.Enrollment) map[string]interface{} {
	out := map[string]interface{}{
		"id":          e.ID,
		"tenant_id":   e.TenantID,
		"course_id":   e.CourseID,
		"gcid":        e.GCID,
		"enrolled_at": e.EnrolledAt.Format(time.RFC3339Nano),
	}
	if e.Status != "" {
		out["status"] = string(e.Status)
	}
	if e.CompletedAt != nil {
		out["completed_at"] = e.CompletedAt.Format(time.RFC3339Nano)
	}
	if e.Passed != nil {
		out["passed"] = *e.Passed
	}
	if e.DeletedAt != nil {
		out["deleted_at"] = e.DeletedAt.Format(time.RFC3339Nano)
	}
	return out
}
