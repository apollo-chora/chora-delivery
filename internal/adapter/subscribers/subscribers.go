// Package subscribers — Pub/Sub event-handler adapters for chora-delivery.
//
// chora-delivery subscribes to:
//
//   - chora.tenancy.payment.captured.v1 (from chora-billing-webhook S2)
//     → metadata.application_id present → transition Application
//     Accepted → Paid → Enrolled and emit application.paid.v1 +
//     application.enrolled.v1 events.
//
//   - chora.identity.kyc.verified.v1 (from A-Singpass S6.3)
//     → unblock Application UnderReview → OfferMade for KYC-required courses.
//     (For S6.1 we transition every UnderReview app for the (tenant, gcid)
//     tuple — production hooks gate on per-course KYC requirement.)
//
// Idempotency: handlers are no-ops when the aggregate is already past the
// expected source state. Re-firing the same event yields the same final
// state — no duplicate transitions, no duplicate events at the aggregate
// level (the bus deduplicates at envelope level via idempotency_key).
//
// Hexagonal: ADAPTER. Imports the application domain + repo + publisher.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// InboxTTL is the dedupe-key retention window for the subscribers.Handler
// inbox. 24h covers Pub/Sub max redelivery for tenancy + identity events
// landing in chora-delivery; production may tune per deployment via
// WithInboxTTL.
const InboxTTL = 24 * time.Hour

// PaymentCaptured mirrors the payload of chora.tenancy.payment.captured.v1
// (the subset chora-delivery cares about).
type PaymentCaptured struct {
	TenantID      string
	ApplicationID string // metadata field on the PaymentIntent
	PaymentIntent string
	AmountCents   int64
	Currency      string
	Traceparent   string
}

// KYCVerified mirrors the payload of chora.identity.kyc.verified.v1.
type KYCVerified struct {
	TenantID    string
	GCID        string
	Method      string // "singpass" | "manual"
	Traceparent string
}

// Handler wraps the repo + publisher.
//
// W1.8 (2026-05-12): the FSM-level idempotency (no-op when already past
// the expected source state) is now backed by a libs/chora-go-common/
// idempotent.Store inbox to prevent concurrent-redelivery races (two
// replicas reading at the same source state would both transition; the
// inbox short-circuits the second). Production wires PostgresStore; dev
// uses MemoryStore.
type Handler struct {
	apps  *repoinmem.ApplicationRepo
	pub   *events.InMemoryPublisher
	inbox idempotent.Store
	ttl   time.Duration
}

// NewHandler constructs a subscribers.Handler.
//
// inbox is OPTIONAL — nil triggers a defensive MemoryStore fallback for
// backward compatibility. Production callers SHOULD pass a PostgresStore-
// backed inbox so dedup survives pod restart + works across replicas.
func NewHandler(apps *repoinmem.ApplicationRepo, pub *events.InMemoryPublisher, inbox idempotent.Store) *Handler {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &Handler{apps: apps, pub: pub, inbox: inbox, ttl: InboxTTL}
}

// WithInboxTTL overrides the default dedupe-key retention window.
func (h *Handler) WithInboxTTL(ttl time.Duration) *Handler {
	if h == nil || ttl <= 0 {
		return h
	}
	h.ttl = ttl
	return h
}

// HandlePaymentCaptured advances an Accepted application to Paid then
// Enrolled, emitting both events.
//
// Dedup: keyed on (tenant_id, application_id, payment_intent) — concurrent
// redelivery from a flaky Pub/Sub Push retry hits the same key + short-
// circuits before reading the aggregate state, avoiding the read-then-
// transition race two replicas would otherwise hit.
//
// FSM idempotency: handler is a no-op when the aggregate is already past
// Accepted — preserved as a defence-in-depth against the rare case where
// the inbox TTL expires before redelivery (24h vs Pub/Sub's 7d max).
func (h *Handler) HandlePaymentCaptured(ctx context.Context, in PaymentCaptured) error {
	if h == nil || h.apps == nil {
		return errors.New("subscribers: nil handler")
	}
	key := "payment:" + in.TenantID + ":" + in.ApplicationID + ":" + in.PaymentIntent
	return h.inbox.Process(ctx, key, h.ttl, func() error {
		app, ok, err := h.apps.Get(ctx, in.TenantID, in.ApplicationID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("subscribers: application %s not found in tenant %s", in.ApplicationID, in.TenantID)
		}

		// FSM-level idempotency — if already past Accepted, do nothing.
		if app.Status == application.StatusPaid || app.Status == application.StatusEnrolled ||
			app.Status == application.StatusWithdrawn || app.Status == application.StatusRejected {
			return nil
		}
		if app.Status != application.StatusAccepted {
			// Not yet at Accepted — webhook delivered before user acceptance flow.
			// In production this would buffer / DLQ; for S6.1 we surface the error.
			return fmt.Errorf("subscribers: application %s in unexpected state %s for payment-captured",
				in.ApplicationID, app.Status)
		}

		if err := app.Transition(application.StatusPaid); err != nil {
			return fmt.Errorf("subscribers: transition Paid: %w", err)
		}
		if h.pub != nil {
			_, _ = h.pub.PublishApplicationStateChanged(app, in.Traceparent)
		}
		if err := app.Transition(application.StatusEnrolled); err != nil {
			return fmt.Errorf("subscribers: transition Enrolled: %w", err)
		}
		if h.pub != nil {
			_, _ = h.pub.PublishApplicationStateChanged(app, in.Traceparent)
		}
		return h.apps.Save(ctx, app)
	})
}

// HandleKYCVerified unblocks all UnderReview applications for (tenant_id,
// gcid) by advancing them to OfferMade.
//
// Dedup: keyed on (tenant_id, gcid, method) — KYC verification is a per-
// (tenant, gcid) event; the same Singpass / manual verification redelivered
// must not re-trigger the bulk OfferMade transition.
//
// FSM idempotency: handler is a no-op for applications already past
// UnderReview — preserved as defence-in-depth.
func (h *Handler) HandleKYCVerified(ctx context.Context, in KYCVerified) error {
	if h == nil || h.apps == nil {
		return errors.New("subscribers: nil handler")
	}
	if in.TenantID == "" || in.GCID == "" {
		return errors.New("subscribers: tenant_id + gcid required")
	}
	key := "kyc:" + in.TenantID + ":" + in.GCID + ":" + in.Method
	return h.inbox.Process(ctx, key, h.ttl, func() error {
		apps, _, err := h.apps.ListByGCID(ctx, in.TenantID, in.GCID, 0, 100)
		if err != nil {
			return err
		}
		for _, app := range apps {
			if app.Status != application.StatusUnderReview {
				continue
			}
			if err := app.Transition(application.StatusOfferMade); err != nil {
				return fmt.Errorf("subscribers: transition OfferMade: %w", err)
			}
			if h.pub != nil {
				_, _ = h.pub.PublishApplicationStateChanged(app, in.Traceparent)
			}
			if err := h.apps.Save(ctx, app); err != nil {
				return err
			}
		}
		return nil
	})
}
