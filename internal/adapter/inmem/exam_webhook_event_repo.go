// exam_webhook_event_repo.go: in-memory examwebhook.Repo for dev / unit tests
// (ADR-193 D1 dedup gate). Production wires pg.ExamWebhookEventRepo
// (chora_delivery.exam_webhook_events, durable + RLS-disabled operational
// table); this store is dev-only and lost on restart, which means a pod
// restart in dev forgets receipts: acceptable for dev, exactly why prod MUST
// wire the pg adapter (the F1 durability gate).
package inmem

import (
	"context"
	"sync"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

// ExamWebhookEventRepo is an in-memory dedup store for satellite deliveries.
type ExamWebhookEventRepo struct {
	mu      sync.Mutex
	byKey   map[string]*examwebhook.WebhookEvent
	eventID map[string]string // event_id -> idempotency_key (PK mirror)
}

// NewExamWebhookEventRepo returns an empty repo.
func NewExamWebhookEventRepo() *ExamWebhookEventRepo {
	return &ExamWebhookEventRepo{
		byKey:   make(map[string]*examwebhook.WebhookEvent),
		eventID: make(map[string]string),
	}
}

// Compile-time assertion: drop-in for pg.ExamWebhookEventRepo.
var _ examwebhook.Repo = (*ExamWebhookEventRepo)(nil)

// Insert records a fresh receipt; a duplicate idempotency_key or event_id
// returns examwebhook.ErrDuplicate (the UNIQUE-violation mirror).
func (r *ExamWebhookEventRepo) Insert(_ context.Context, ev *examwebhook.WebhookEvent) error {
	if ev == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byKey[ev.IdempotencyKey]; dup {
		return examwebhook.ErrDuplicate
	}
	if _, dup := r.eventID[ev.EventID]; dup {
		return examwebhook.ErrDuplicate
	}
	cp := *ev
	r.byKey[ev.IdempotencyKey] = &cp
	r.eventID[ev.EventID] = ev.IdempotencyKey
	return nil
}

// GetByIdempotencyKey resolves a receipt; ok=false is a genuine miss.
func (r *ExamWebhookEventRepo) GetByIdempotencyKey(_ context.Context, key string) (*examwebhook.WebhookEvent, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, ok := r.byKey[key]
	if !ok {
		return nil, false, nil
	}
	cp := *ev
	return &cp, true, nil
}

// MarkResultRecorded stores the durable result id (resume marker).
func (r *ExamWebhookEventRepo) MarkResultRecorded(_ context.Context, idempotencyKey, resultID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev, ok := r.byKey[idempotencyKey]; ok {
		ev.MarkResultRecorded(resultID)
	}
	return nil
}

// MarkProcessed completes the receipt.
func (r *ExamWebhookEventRepo) MarkProcessed(_ context.Context, idempotencyKey, resultID string, when time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev, ok := r.byKey[idempotencyKey]; ok {
		ev.MarkProcessed(resultID, when)
	}
	return nil
}

// MarkFailed records the failure reason; the receipt stays re-dispatchable.
func (r *ExamWebhookEventRepo) MarkFailed(_ context.Context, idempotencyKey, errMsg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev, ok := r.byKey[idempotencyKey]; ok {
		ev.MarkFailed(errMsg)
	}
	return nil
}
