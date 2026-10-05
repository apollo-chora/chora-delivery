// application_publisher.go — S6.1 application-event emit hook.
//
// Application state transitions emit a per-state Pub/Sub event with the
// canonical envelope + IMDA dimension per ADR-141:
//
//	StatusSubmitted   → chora.delivery.application.submitted.v1   (D1 accountability)
//	StatusUnderReview → chora.delivery.application.under_review.v1 (D1)
//	StatusOfferMade   → chora.delivery.application.offer_made.v1   (D1)
//	StatusAccepted    → chora.delivery.application.accepted.v1     (D1)
//	StatusWithdrawn   → chora.delivery.application.withdrawn.v1    (D1)
//	StatusRejected    → chora.delivery.application.rejected.v1     (D1)
//	StatusPaid        → chora.delivery.application.paid.v1         (D2 transparency)
//	StatusEnrolled    → chora.delivery.application.enrolled.v1     (D2)
//
// Idempotency_key = `application:{app_id}:{status}` (the bus dedupes on this).
// CorrelationID = the application_id so subscribers can join state events.
//
// Hexagonal: ADAPTER. Imports the application domain (canonical payload helper).
package events

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// PublishApplicationStateChanged emits the per-state Pub/Sub event for a
// course application. Returns ErrIllegalState when status==Draft (no topic).
//
// The optional `traceparent` arg lets callers pin a W3C trace context to the
// emit; empty values get a freshly minted traceparent per CLAUDE.md §6.
func (p *InMemoryPublisher) PublishApplicationStateChanged(app *application.Application, traceparent string) (PublishedEvent, error) {
	if app == nil {
		return PublishedEvent{}, errors.New("publisher: nil application")
	}
	if strings.TrimSpace(app.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	topic := application.EventTopicFor(app.Status)
	if topic == "" {
		return PublishedEvent{}, fmt.Errorf("publisher: no Pub/Sub topic for status %q (draft is private)", app.Status)
	}

	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: application.IdempotencyKey(app, app.Status),
		TenantID:       app.TenantID,
		GCID:           app.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(traceparent),
		Tracestate:     "",
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
		CorrelationID:  app.ID,
	}
	payload := application.CanonicalEventPayload(app)
	payload["chora_imda_dimension"] = application.IMDADimensionFor(app.Status)

	ev := PublishedEvent{Topic: topic, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}
