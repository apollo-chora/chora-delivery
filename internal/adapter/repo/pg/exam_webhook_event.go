// exam_webhook_event.go: Postgres adapter for
// chora_delivery.exam_webhook_events (ADR-193 D1 dedup gate).
//
// SCHEMA: migrations/0057_exam_webhook_events.up.sql. RLS-DISABLED
// operational table (mirrors chora_payments.stripe_webhook_events): a
// satellite delivery arrives with NO Chora session and the target tenant
// lives INSIDE the HMAC-verified payload, so this adapter deliberately does
// NOT call rls.ApplySession (it would refuse the bare inbound context).
// Tenant isolation is enforced where it matters: on the RLS-scoped
// exam_results write that follows the dedup gate.
//
// The dedup contract: event_id is the PRIMARY KEY (envelope UUIDv7),
// idempotency_key is UNIQUE (the producer dedup key the receive path gates
// on). Insert maps 23505 to examwebhook.ErrDuplicate; the receive path then
// splits processed-replay from unprocessed-re-dispatch.
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

// -----------------------------------------------------------------------------
// SQL templates
// -----------------------------------------------------------------------------

const (
	SQLInsertExamWebhookEvent = `
INSERT INTO exam_webhook_events (event_id, idempotency_key, tenant_id, received_at)
VALUES ($1, $2, $3, $4)`

	SQLGetExamWebhookEventByKey = `
SELECT event_id, idempotency_key, tenant_id, received_at, processed_at, processing_error, result_id
FROM exam_webhook_events
WHERE idempotency_key = $1`

	SQLMarkExamWebhookResultRecorded = `
UPDATE exam_webhook_events
SET result_id = $2
WHERE idempotency_key = $1`

	SQLMarkExamWebhookProcessed = `
UPDATE exam_webhook_events
SET processed_at = $3, result_id = $2, processing_error = ''
WHERE idempotency_key = $1`

	SQLMarkExamWebhookFailed = `
UPDATE exam_webhook_events
SET processing_error = $2
WHERE idempotency_key = $1`
)

// -----------------------------------------------------------------------------
// ExamWebhookEventRepo
// -----------------------------------------------------------------------------

// ExamWebhookEventRepo is the Postgres-backed examwebhook.Repo.
type ExamWebhookEventRepo struct {
	tx TxRunner
}

// NewExamWebhookEventRepo constructs the repo around a TxRunner. A nil
// TxRunner makes every method return ErrNotImplemented (an unwired dedup
// gate is a WIRING BUG: it must never quietly pass deliveries through).
func NewExamWebhookEventRepo(tx TxRunner) *ExamWebhookEventRepo {
	return &ExamWebhookEventRepo{tx: tx}
}

// Compile-time assertion: satisfies the domain port.
var _ examwebhook.Repo = (*ExamWebhookEventRepo)(nil)

// Insert records a fresh receipt; 23505 surfaces as ErrDuplicate.
func (r *ExamWebhookEventRepo) Insert(ctx context.Context, ev *examwebhook.WebhookEvent) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if ev == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if _, err := q.Exec(ctx, SQLInsertExamWebhookEvent,
			ev.EventID, ev.IdempotencyKey, ev.TenantID, ev.ReceivedAt,
		); err != nil {
			if isFranchiseUniqueViolation(err) {
				return examwebhook.ErrDuplicate
			}
			return fmt.Errorf("pg: insert exam_webhook_event: %w", err)
		}
		return nil
	})
}

// GetByIdempotencyKey resolves a receipt; ok=false is a genuine miss.
func (r *ExamWebhookEventRepo) GetByIdempotencyKey(ctx context.Context, key string) (*examwebhook.WebhookEvent, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *examwebhook.WebhookEvent
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		var ev examwebhook.WebhookEvent
		if err := q.QueryRow(ctx, SQLGetExamWebhookEventByKey, key).Scan(
			&ev.EventID, &ev.IdempotencyKey, &ev.TenantID, &ev.ReceivedAt,
			&ev.ProcessedAt, &ev.ProcessingError, &ev.ResultID,
		); err != nil {
			if isNoRows(err) {
				return nil // genuine miss
			}
			return fmt.Errorf("pg: get exam_webhook_event: %w", err)
		}
		out = &ev
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: an infra failure is NOT a miss (CHO-2184)
	}
	if out == nil {
		return nil, false, nil
	}
	return out, true, nil
}

// MarkResultRecorded persists the resume marker (durable result exists).
func (r *ExamWebhookEventRepo) MarkResultRecorded(ctx context.Context, idempotencyKey, resultID string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if _, err := q.Exec(ctx, SQLMarkExamWebhookResultRecorded, idempotencyKey, resultID); err != nil {
			return fmt.Errorf("pg: mark exam_webhook_event result recorded: %w", err)
		}
		return nil
	})
}

// MarkProcessed completes the receipt and clears any stale error.
func (r *ExamWebhookEventRepo) MarkProcessed(ctx context.Context, idempotencyKey, resultID string, when time.Time) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if _, err := q.Exec(ctx, SQLMarkExamWebhookProcessed, idempotencyKey, resultID, when.UTC()); err != nil {
			return fmt.Errorf("pg: mark exam_webhook_event processed: %w", err)
		}
		return nil
	})
}

// MarkFailed records the failure reason; the receipt stays re-dispatchable.
func (r *ExamWebhookEventRepo) MarkFailed(ctx context.Context, idempotencyKey, errMsg string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if _, err := q.Exec(ctx, SQLMarkExamWebhookFailed, idempotencyKey, errMsg); err != nil {
			return fmt.Errorf("pg: mark exam_webhook_event failed: %w", err)
		}
		return nil
	})
}
