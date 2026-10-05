// store.go — the hexagonal persistence port for the Exam aggregate.
//
// Mirrors delivery.BookingPort (CHO-1622): the HTTP handlers depend ONLY on
// this interface so the backing store swaps between the in-memory dev adapter
// and pg.ExamRepo (chora_delivery.exams — durable across pod restart) at
// cmd/server wiring. ctx-threaded so the Postgres adapter can call
// rls.ApplySession before every query (RLS reads the tenant from
// tracing.TenantIDFromContext). This closes the R+ durability debt: Exams was
// inmem-only (ephemeral, lost on pod restart).
package exam

import "context"

// ExamStore is the persistence port for Exam aggregates.
//
//   - Save upserts an exam (create + every FSM transition re-Save call it).
//     Returns an error so a failed durable write is loud.
//   - Get resolves an exam by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenant returns the tenant's active (non-soft-deleted) exams.
type ExamStore interface {
	Save(ctx context.Context, e *Exam) error
	Get(ctx context.Context, id string) (*Exam, bool, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*Exam, error)
}
