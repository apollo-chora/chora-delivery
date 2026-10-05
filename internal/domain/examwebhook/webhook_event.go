// Package examwebhook is the inbound satellite exam-result webhook receipt
// entity (ADR-193 D1). Backs the chora_delivery.exam_webhook_events table
// (RLS-DISABLED operational dedup, mirroring chora-payments'
// stripe_webhook_events per ADR-164/ADR-188).
//
// Lifecycle: received -> processed | failed, with one refinement over the
// payments original: ResultID is recorded the moment the durable exam_results
// row exists (MarkResultRecorded), BEFORE processed. A retried delivery that
// finds ResultID set resumes at the publish step instead of grading again, so
// one idempotency_key can never mint two exam_results rows.
//
// The dedup gate is the UNIQUE constraint on idempotency_key (the satellite's
// producer-supplied dedup key, per the event-envelope contract); event_id
// (the envelope UUIDv7) is the primary key. Pure domain: no http, no
// persistence imports.
package examwebhook

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	// ErrEventIDRequired: the envelope event_id must be non-empty.
	ErrEventIDRequired = errors.New("examwebhook: event_id required")
	// ErrIdempotencyKeyRequired: the producer dedup key must be non-empty.
	ErrIdempotencyKeyRequired = errors.New("examwebhook: idempotency_key required")
	// ErrTenantRequired: the target tenant must be non-empty.
	ErrTenantRequired = errors.New("examwebhook: tenant_id required")
	// ErrDuplicate is returned by repos when the idempotency_key (or
	// event_id) was seen before. The receive path then distinguishes a
	// processed replay from an unprocessed re-dispatch.
	ErrDuplicate = errors.New("examwebhook: duplicate delivery (idempotent gate)")
)

// WebhookEvent is one inbound satellite exam-result delivery receipt.
type WebhookEvent struct {
	EventID         string // envelope UUIDv7 from the satellite (PRIMARY KEY)
	IdempotencyKey  string // producer dedup key (UNIQUE, the dedup gate)
	TenantID        string // target chora-main tenant resolved from the signed payload
	ReceivedAt      time.Time
	ProcessedAt     *time.Time // NULL = received-not-processed (re-dispatch on retry)
	ProcessingError string
	ResultID        string // durable exam_results row id, set as soon as the row exists
}

// New constructs a receipt in received state.
func New(eventID, idempotencyKey, tenantID string, now time.Time) (*WebhookEvent, error) {
	if strings.TrimSpace(eventID) == "" {
		return nil, ErrEventIDRequired
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, ErrIdempotencyKeyRequired
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrTenantRequired
	}
	return &WebhookEvent{
		EventID:        strings.TrimSpace(eventID),
		IdempotencyKey: strings.TrimSpace(idempotencyKey),
		TenantID:       strings.TrimSpace(tenantID),
		ReceivedAt:     now,
	}, nil
}

// MarkResultRecorded records that the durable exam_results row exists. NOT
// the same as processed: the released-event publish may still be owed.
func (w *WebhookEvent) MarkResultRecorded(resultID string) {
	w.ResultID = resultID
}

// MarkProcessed transitions to processed and clears any stale error.
func (w *WebhookEvent) MarkProcessed(resultID string, now time.Time) {
	w.ResultID = resultID
	w.ProcessedAt = &now
	w.ProcessingError = ""
}

// MarkFailed records a processing error; the receipt stays re-dispatchable.
func (w *WebhookEvent) MarkFailed(errMsg string) {
	w.ProcessingError = errMsg
}

// IsProcessed reports whether the delivery completed.
func (w *WebhookEvent) IsProcessed() bool { return w.ProcessedAt != nil }

// Repo is the persistence port for webhook receipts.
//
//   - Insert records a fresh receipt; a duplicate idempotency_key (or
//     event_id) returns ErrDuplicate.
//   - GetByIdempotencyKey resolves a receipt. ok=false is a GENUINE MISS; an
//     infra failure returns a non-nil error (CHO-2184).
//   - MarkResultRecorded / MarkProcessed / MarkFailed persist the lifecycle
//     transitions keyed by idempotency_key.
type Repo interface {
	Insert(ctx context.Context, ev *WebhookEvent) error
	GetByIdempotencyKey(ctx context.Context, key string) (*WebhookEvent, bool, error)
	MarkResultRecorded(ctx context.Context, idempotencyKey, resultID string) error
	MarkProcessed(ctx context.Context, idempotencyKey, resultID string, when time.Time) error
	MarkFailed(ctx context.Context, idempotencyKey, errMsg string) error
}
