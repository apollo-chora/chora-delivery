// payments_subscriber.go — Pub/Sub subscribers for the 4 canonical
// chora.payments.* events chora-delivery cares about (per ADR-164 Stage C).
//
// Per ADR-164 D3 originating-service notification path:
//
//	chora.payments.course_purchase.payment_captured.v1
//	    → EnrollmentRegistry.Register(course_id, gcid) → emit
//	      chora.delivery.enrollment.created.v1
//
//	chora.payments.course_purchase.refunded.v1
//	    → EnrollmentRegistry.SoftDelete on the (course, learner) row
//	    → emit chora.delivery.enrollment.cancelled.v1
//
//	chora.payments.application_payment.payment_captured.v1
//	    → Application FSM: Accepted → Paid → Enrolled (per existing
//	      subscribers.go::HandlePaymentCaptured semantics, retopiced)
//	    → emit chora.delivery.application.paid.v1 + .enrolled.v1
//
//	chora.payments.application_payment.refunded.v1
//	    → if the FSM allows it, Application → Withdrawn (Accepted/Paid → Withdrawn)
//	      else stamp history + revoke enrollment as best-effort
//	    → revoke any provisioned enrollment row
//	    → emit chora.delivery.application.refunded.v1 (custom)
//
// Idempotency: at-least-once Pub/Sub delivery means every handler MUST
// be replayable. Each handler runs inside idempotent.Store.Process(...)
// keyed on the canonical event_id from the chora.common.v1.EventEnvelope
// (with the topic prefixed in to scope across the 4 different topic
// families). Replays short-circuit before any state-mutation work.
//
// Envelope validation: handlers reject events with missing event_id /
// tenant_id — the envelope is the trust anchor. The wire payload is a
// generated Go type so the fields themselves are typed; only the
// envelope-level checks need to be defensive.
//
// Hexagonal: ADAPTER. Imports the application domain + repo + the
// in-package Publisher. No domain code imports this file.
package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/payments/v1"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Canonical topic + subscription names (per ADR-164 D4 + CLAUDE.md §1).
// -----------------------------------------------------------------------------

const (
	// chora-payments outbound topics chora-delivery subscribes to.
	TopicCoursePurchasePaymentCaptured     = "chora.payments.course_purchase.payment_captured.v1"
	TopicCoursePurchaseRefunded            = "chora.payments.course_purchase.refunded.v1"
	TopicApplicationPaymentPaymentCaptured = "chora.payments.application_payment.payment_captured.v1"
	TopicApplicationPaymentRefunded        = "chora.payments.application_payment.refunded.v1"

	// Downstream topic for the application-refund audit emit.
	TopicApplicationRefunded = "chora.delivery.application.refunded.v1"

	// PaymentsInboxTTL — dedupe-key retention window for the 4 payment
	// subscribers. 24h covers Pub/Sub's max redelivery; Stripe redelivers
	// for up to 3 days post-event but the chora-payments dedup gate
	// upstream catches any later replays before they reach this service.
	PaymentsInboxTTL = 24 * time.Hour
)

// PaymentsSubscriptionName returns the canonical chora-delivery subscription
// name for a chora.payments.* topic — formatted as
// `chora-delivery-payments-{aggregate}-{event_type}` per the existing
// chora-delivery subscription naming convention.
func PaymentsSubscriptionName(topic string) string {
	// topic format: chora.payments.{aggregate}.{event_type}.v{N}
	const prefix = "chora.payments."
	if !strings.HasPrefix(topic, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(topic, prefix)
	parts := strings.Split(rest, ".")
	if len(parts) < 3 {
		return ""
	}
	// drop the trailing version segment (e.g. "v1")
	aggregate := parts[0]
	eventType := strings.Join(parts[1:len(parts)-1], "_")
	return fmt.Sprintf("chora-delivery-payments-%s-%s", aggregate, eventType)
}

// -----------------------------------------------------------------------------
// Ports — EnrollmentTracker decouples the subscriber from the registry's
// concrete shape so tests + production share the same wiring.
// -----------------------------------------------------------------------------

// EnrollmentTracker wraps an EnrollmentPort so the subscriber can use a
// typed Register / Revoke surface that surfaces fresh-insert vs existing-
// row outcomes (PaymentsSubscriber emits the creation event only on fresh).
//
// Production (CJ#2 Stripe path) wires this around pg.EnrollmentRepo so
// Enrolment rows persist to chora_delivery.course_enrollments and survive
// pod restart. Dev / unit tests can pass domain.NewInMemEnrollmentStore()
// to keep the in-memory legacy behaviour.
//
// Revoke drives the port's Cancel (deleted_at=now, status=cancelled) so the
// soft-delete PERSISTS on both the in-memory store and pg.EnrollmentRepo —
// the refund → un-enrol path is durable on Postgres, not just an in-struct
// mutation. The chora.delivery.enrollment.cancelled.v1 event still emits.
type EnrollmentTracker struct {
	store domain.EnrollmentPort
}

// NewEnrollmentTracker constructs an EnrollmentTracker around an
// EnrollmentPort. The wrapping layer is what distinguishes "was this a
// fresh row?" — the underlying port's Register is idempotent and returns
// the same row on replay.
func NewEnrollmentTracker(store domain.EnrollmentPort) *EnrollmentTracker {
	return &EnrollmentTracker{store: store}
}

// Register inserts (or returns the existing) enrollment for the natural
// key (tenant_id, course_id, gcid). Returns (enrollment, created) — the
// boolean is true only when a fresh row was inserted.
//
// The "created?" derivation peeks via GetByCourseAndGCID first so the
// caller can decide whether to emit the creation event. The peek is
// inside the same logical operation but a separate ctx-bound call —
// race-safe under at-least-once Pub/Sub because the idempotency layer
// upstream already dedup'd the event (see s.inbox.Process).
func (t *EnrollmentTracker) Register(ctx context.Context, tenantID, courseID, gcid string) (*domain.Enrollment, bool, error) {
	if t == nil || t.store == nil {
		return nil, false, errors.New("enrollment tracker: store nil")
	}
	_, existed, err := t.store.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
	if err != nil {
		return nil, false, fmt.Errorf("enrollment tracker: peek: %w", err)
	}
	enr, err := t.store.Register(ctx, tenantID, courseID, gcid)
	if err != nil {
		return nil, false, err
	}
	return enr, !existed, nil
}

// Revoke soft-deletes the enrollment for (tenant_id, course_id, gcid)
// if one exists. Returns (enrollment, revoked) — revoked is false if the
// row is already gone or never existed.
//
// The soft-delete is PERSISTED via the port's Cancel (deleted_at=now,
// status=cancelled), RLS/tenant-scoped inside the pg adapter's RunInTx. The
// old path SoftDelete()'d the loaded aggregate only — a silent no-op on
// Postgres, where GetByCourseAndGCID materialises a throw-away struct that is
// never written back (the in-memory store shared the live pointer, which
// masked the gap). ctx already carries the tenant (the caller decorates it via
// tracing.WithTenantID before Revoke), so Cancel's SET LOCAL chora.tenant_id
// fires. Fail loud on error per feedback_no_stubs_real_wiring.
func (t *EnrollmentTracker) Revoke(ctx context.Context, tenantID, courseID, gcid string) (*domain.Enrollment, bool, error) {
	if t == nil || t.store == nil {
		return nil, false, errors.New("enrollment tracker: store nil")
	}
	enr, ok, err := t.store.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
	if err != nil {
		return nil, false, fmt.Errorf("enrollment tracker: peek: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	if err := t.store.Cancel(ctx, tenantID, enr.ID); err != nil {
		return nil, false, fmt.Errorf("enrollment tracker: cancel: %w", err)
	}
	// Reflect the persisted cancellation on the returned aggregate so the
	// caller's downstream enrollment.cancelled.v1 event carries a consistent
	// (deleted) view. Idempotent — SoftDelete no-ops when already deleted.
	enr.SoftDelete()
	return enr, true, nil
}

// -----------------------------------------------------------------------------
// PaymentsSubscriber — the adapter.
// -----------------------------------------------------------------------------

// PaymentsSubscriberDeps wires the 4 dependencies.
type PaymentsSubscriberDeps struct {
	// Enrollments — wraps an EnrollmentPort (pg.EnrollmentRepo in
	// production; domain.InMemEnrollmentStore in tests). Required.
	Enrollments *EnrollmentTracker
	// Applications — the chora-delivery Application aggregate repository
	// (application.ApplicationPort: pg.ApplicationRepo in production —
	// durable across pod restart, R+ durability sweep Wave 2; repoinmem in
	// dev/tests). Required.
	Applications application.ApplicationPort
	// Publisher — emits the chora.delivery.* downstream events. Required.
	Publisher Publisher
	// Inbox — idempotent.Store for replay dedup. Defaults to MemoryStore
	// when nil so dev / unit tests work without a Postgres pool; production
	// wires the Postgres-backed store.
	Inbox idempotent.Store
	// TTL — inbox dedup-key retention window. Defaults to PaymentsInboxTTL.
	TTL time.Duration
}

// PaymentsSubscriber processes the 4 canonical chora.payments.* events.
type PaymentsSubscriber struct {
	enrollments  *EnrollmentTracker
	applications application.ApplicationPort
	pub          Publisher
	inbox        idempotent.Store
	ttl          time.Duration
}

// NewPaymentsSubscriber constructs a PaymentsSubscriber.
func NewPaymentsSubscriber(deps PaymentsSubscriberDeps) *PaymentsSubscriber {
	inbox := deps.Inbox
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	ttl := deps.TTL
	if ttl <= 0 {
		ttl = PaymentsInboxTTL
	}
	return &PaymentsSubscriber{
		enrollments:  deps.Enrollments,
		applications: deps.Applications,
		pub:          deps.Publisher,
		inbox:        inbox,
		ttl:          ttl,
	}
}

// -----------------------------------------------------------------------------
// course_purchase.payment_captured.v1
// -----------------------------------------------------------------------------

// HandleCoursePurchasePaymentCaptured processes a successful CJ#2 course
// purchase by registering the enrolment + emitting the downstream
// chora.delivery.enrollment.created.v1 event.
func (s *PaymentsSubscriber) HandleCoursePurchasePaymentCaptured(ctx context.Context, ev *paymentsv1.CoursePurchasePaymentCaptured) error {
	if s == nil {
		return errors.New("payments subscriber: nil receiver")
	}
	if ev == nil {
		return errors.New("payments subscriber: nil event")
	}
	env := ev.GetEnvelope()
	if err := validateEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(ev.GetCourseId()) == "" {
		return errors.New("payments subscriber: course_purchase.payment_captured: course_id required")
	}
	if strings.TrimSpace(ev.GetLearnerGcid()) == "" {
		return errors.New("payments subscriber: course_purchase.payment_captured: learner_gcid required")
	}
	// rls.ApplySession inside pg.EnrollmentRepo reads tenant from
	// tracing.TenantIDFromContext — decorate ctx with the envelope's
	// tenant_id before any repo call so SET LOCAL chora.tenant_id fires
	// (without this, the DB write would error ErrNoTenantContext).
	ctx = tracing.WithTenantID(ctx, env.GetTenantId())
	key := TopicCoursePurchasePaymentCaptured + ":" + env.GetEventId()
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		enr, created, err := s.enrollments.Register(ctx, env.GetTenantId(), ev.GetCourseId(), ev.GetLearnerGcid())
		if err != nil {
			return fmt.Errorf("payments subscriber: enrollment register: %w", err)
		}
		if !created || s.pub == nil {
			// Replay-through-FSM-idempotency: row already existed, skip emit.
			return nil
		}
		if _, err := s.pub.PublishEnrollmentCreated(EnrollmentCreated{
			TenantID:     env.GetTenantId(),
			GCID:         ev.GetLearnerGcid(),
			EnrollmentID: enr.ID,
			CourseID:     enr.CourseID,
			LearnerGCID:  ev.GetLearnerGcid(),
			Traceparent:  env.GetTraceparent(),
			Tracestate:   env.GetTracestate(),
		}); err != nil {
			return fmt.Errorf("payments subscriber: emit enrollment.created: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// course_purchase.refunded.v1
// -----------------------------------------------------------------------------

// HandleCoursePurchaseRefunded soft-deletes the enrolment for the (course,
// learner) pair + emits the downstream chora.delivery.enrollment.cancelled.v1
// event. Idempotent on event_id.
func (s *PaymentsSubscriber) HandleCoursePurchaseRefunded(ctx context.Context, ev *paymentsv1.CoursePurchaseRefunded) error {
	if s == nil {
		return errors.New("payments subscriber: nil receiver")
	}
	if ev == nil {
		return errors.New("payments subscriber: nil event")
	}
	env := ev.GetEnvelope()
	if err := validateEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(ev.GetCourseId()) == "" {
		return errors.New("payments subscriber: course_purchase.refunded: course_id required")
	}
	if strings.TrimSpace(ev.GetLearnerGcid()) == "" {
		return errors.New("payments subscriber: course_purchase.refunded: learner_gcid required")
	}
	// Same tenant decoration as payment_captured — required for RLS.
	ctx = tracing.WithTenantID(ctx, env.GetTenantId())
	key := TopicCoursePurchaseRefunded + ":" + env.GetEventId()
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		enr, revoked, err := s.enrollments.Revoke(ctx, env.GetTenantId(), ev.GetCourseId(), ev.GetLearnerGcid())
		if err != nil {
			return fmt.Errorf("payments subscriber: enrollment revoke: %w", err)
		}
		if !revoked || s.pub == nil {
			// Defence-in-depth: nothing to revoke + nothing to emit. The
			// upstream chora-payments dedup gate already filters Stripe-side
			// duplicates; reaching here on a no-op replay is safe.
			return nil
		}
		if _, err := s.pub.PublishEnrollmentCancelled(EnrollmentCancelled{
			TenantID:     env.GetTenantId(),
			GCID:         ev.GetLearnerGcid(),
			EnrollmentID: enr.ID,
			CourseID:     enr.CourseID,
			LearnerGCID:  ev.GetLearnerGcid(),
			Reason:       "refund:" + ev.GetStripeRefundId(),
			Traceparent:  env.GetTraceparent(),
			Tracestate:   env.GetTracestate(),
		}); err != nil {
			return fmt.Errorf("payments subscriber: emit enrollment.cancelled: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// application_payment.payment_captured.v1
// -----------------------------------------------------------------------------

// HandleApplicationPaymentCaptured advances the Application FSM from
// Accepted → Paid → Enrolled (mirrors the existing
// subscribers.go::HandlePaymentCaptured handler retopiced for the canonical
// chora.payments.* topic family). Idempotent on event_id with FSM-level
// defence-in-depth so an inbox-TTL-expired replay re-discovering the
// terminal Enrolled state is a no-op.
func (s *PaymentsSubscriber) HandleApplicationPaymentCaptured(ctx context.Context, ev *paymentsv1.ApplicationPaymentPaymentCaptured) error {
	if s == nil {
		return errors.New("payments subscriber: nil receiver")
	}
	if ev == nil {
		return errors.New("payments subscriber: nil event")
	}
	env := ev.GetEnvelope()
	if err := validateEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(ev.GetApplicationId()) == "" {
		return errors.New("payments subscriber: application_payment.payment_captured: application_id required")
	}
	// rls.ApplySession inside pg.ApplicationRepo reads tenant from
	// tracing.TenantIDFromContext — decorate ctx with the envelope tenant_id
	// BEFORE any repo call so SET LOCAL chora.tenant_id fires (without this,
	// the durable Get/Save would error rls.ErrNoTenantContext and the event
	// would redeliver forever once Applications is pg-backed). Tenant only —
	// this is a system path, so user_isolation stays permissive (Get-by-id).
	ctx = tracing.WithTenantID(ctx, env.GetTenantId())
	key := TopicApplicationPaymentPaymentCaptured + ":" + env.GetEventId()
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		app, ok, err := s.applications.Get(ctx, env.GetTenantId(), ev.GetApplicationId())
		if err != nil {
			return fmt.Errorf("payments subscriber: application get: %w", err)
		}
		if !ok {
			return fmt.Errorf("payments subscriber: application %s not found in tenant %s",
				ev.GetApplicationId(), env.GetTenantId())
		}
		// FSM-level idempotency — already past Accepted, do nothing.
		if app.Status == application.StatusPaid ||
			app.Status == application.StatusEnrolled ||
			app.Status == application.StatusWithdrawn ||
			app.Status == application.StatusRejected {
			return nil
		}
		if app.Status != application.StatusAccepted {
			return fmt.Errorf("payments subscriber: application %s in unexpected state %s for payment-captured",
				ev.GetApplicationId(), app.Status)
		}
		// Attach the Stripe handle for audit + idempotent replay protection.
		if pi := strings.TrimSpace(ev.GetStripePaymentIntentId()); pi != "" {
			app.AttachPaymentIntent(pi)
		}
		// Paid transition + emit.
		if err := app.Transition(application.StatusPaid); err != nil {
			return fmt.Errorf("payments subscriber: transition Paid: %w", err)
		}
		if s.pub != nil {
			_, _ = s.pub.PublishApplicationStateChanged(app, env.GetTraceparent())
		}
		// Enrolled transition + emit.
		if err := app.Transition(application.StatusEnrolled); err != nil {
			return fmt.Errorf("payments subscriber: transition Enrolled: %w", err)
		}
		if s.pub != nil {
			_, _ = s.pub.PublishApplicationStateChanged(app, env.GetTraceparent())
		}
		return s.applications.Save(ctx, app)
	})
}

// -----------------------------------------------------------------------------
// application_payment.refunded.v1
// -----------------------------------------------------------------------------

// HandleApplicationPaymentRefunded cancels the Application (when the FSM
// allows it — Accepted/Paid → Withdrawn) and revokes any provisioned
// enrolment row. Always emits a downstream chora.delivery.application.refunded.v1
// custom event for audit. Idempotent on event_id.
//
// For terminal-state Applications (Enrolled), the FSM transition is skipped
// — the application stays in Enrolled state but the enrolment is revoked
// and the audit event still fires. This matches the federated saga design
// where chora-payments is the canonical source of truth for the financial
// reversal; chora-delivery materialises the side-effects it owns.
func (s *PaymentsSubscriber) HandleApplicationPaymentRefunded(ctx context.Context, ev *paymentsv1.ApplicationPaymentRefunded) error {
	if s == nil {
		return errors.New("payments subscriber: nil receiver")
	}
	if ev == nil {
		return errors.New("payments subscriber: nil event")
	}
	env := ev.GetEnvelope()
	if err := validateEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(ev.GetApplicationId()) == "" {
		return errors.New("payments subscriber: application_payment.refunded: application_id required")
	}
	// Decorate ctx with tenant BEFORE any repo call (same RLS requirement as
	// HandleApplicationPaymentCaptured — see that handler's note). Tenant only.
	ctx = tracing.WithTenantID(ctx, env.GetTenantId())
	key := TopicApplicationPaymentRefunded + ":" + env.GetEventId()
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		app, ok, err := s.applications.Get(ctx, env.GetTenantId(), ev.GetApplicationId())
		if err != nil {
			return fmt.Errorf("payments subscriber: application get: %w", err)
		}
		if !ok {
			return fmt.Errorf("payments subscriber: application %s not found in tenant %s",
				ev.GetApplicationId(), env.GetTenantId())
		}
		// Cancel the Application (FSM-permitting). Reason carries the Stripe
		// refund handle for cross-correlation in the audit log.
		reason := "refund:" + strings.TrimSpace(ev.GetStripeRefundId())
		switch app.Status {
		case application.StatusSubmitted, application.StatusUnderReview,
			application.StatusOfferMade, application.StatusAccepted:
			if err := app.TransitionWithReason(application.StatusWithdrawn, reason); err != nil {
				return fmt.Errorf("payments subscriber: transition Withdrawn: %w", err)
			}
			if s.pub != nil {
				_, _ = s.pub.PublishApplicationStateChanged(app, env.GetTraceparent())
			}
			if err := s.applications.Save(ctx, app); err != nil {
				return fmt.Errorf("payments subscriber: save withdrawn: %w", err)
			}
		case application.StatusPaid, application.StatusEnrolled:
			// FSM doesn't allow Withdrawn from terminal-ish states (Paid →
			// Enrolled only; Enrolled terminal). The refund is still
			// authoritative on the chora-payments side; we revoke the
			// enrollment + emit the audit event below.
		}
		// Revoke the enrollment row if it exists. ctx already carries the
		// tenant (decorated above) for any DB-backed EnrollmentPort.
		_, _, _ = s.enrollments.Revoke(ctx, env.GetTenantId(), app.CourseID, app.GCID)

		// Always emit the canonical refund audit event so observability +
		// downstream sagas see the financial reversal regardless of which
		// FSM branch fired.
		if s.pub != nil {
			emitCustomEvent(s.pub, TopicApplicationRefunded, env.GetTenantId(), ev.GetLearnerGcid(), map[string]any{
				"application_id":        app.ID,
				"course_id":             app.CourseID,
				"learner_gcid":          ev.GetLearnerGcid(),
				"purchase_id":           ev.GetPurchaseId(),
				"stripe_charge_id":      ev.GetStripeChargeId(),
				"stripe_refund_id":      ev.GetStripeRefundId(),
				"amount_cents_refunded": ev.GetAmountCentsRefunded(),
				"currency":              ev.GetCurrency(),
				"refunded_at":           time.Now().UTC().Format(time.RFC3339Nano),
			})
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func validateEnvelope(env *commonv1.EventEnvelope) error {
	if env == nil {
		return errors.New("payments subscriber: envelope required")
	}
	if strings.TrimSpace(env.GetEventId()) == "" {
		return errors.New("payments subscriber: envelope event_id required")
	}
	if strings.TrimSpace(env.GetTenantId()) == "" {
		return errors.New("payments subscriber: envelope tenant_id required")
	}
	return nil
}

// emitCustomEvent — mirror of course_cj2_handler.go::publishCourseEvent.
// Uses the optional PublishCustom interface so the InMemoryPublisher
// captures the audit event for tests + the CloudPublisher pipes it
// through the outbox dispatcher.
func emitCustomEvent(pub Publisher, topic, tenantID, gcid string, payload map[string]any) {
	type publishCustomer interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (PublishedEvent, error)
	}
	if pc, ok := pub.(publishCustomer); ok {
		_, _ = pc.PublishCustom(topic, tenantID, gcid, payload)
	}
}
