// store.go — the hexagonal persistence port for the ScheduledClass aggregate.
//
// Mirrors exam.ExamStore / survey.SurveyStore (R+ durability sweep): the HTTP
// handlers depend ONLY on this interface so the backing store swaps between the
// in-memory dev adapter (repo/inmem.SchedulingRepo) and repo/pg.SchedulingRepo
// (chora_delivery.scheduled_classes — durable + RLS-isolated across pod
// restart) at cmd/server wiring. ctx-threaded so the Postgres adapter can call
// rls.ApplySession before every query (RLS reads the tenant from
// tracing.TenantIDFromContext).
//
// Wave 2 follow-up (CHO-1626): ScheduledClass previously had NO write path —
// nothing constructed one, so the week-view list was always empty and pg-backing
// had no durability value. This port lands alongside the create / reschedule /
// cancel routes that make ScheduledClass a real, writable, durable aggregate.
package scheduling

import "context"

// SchedulingStore is the persistence port for ScheduledClass aggregates.
//
//   - Save upserts a scheduled class (create + every reschedule / cancel
//     re-Save it). Returns an error so a failed durable write is loud.
//   - Get resolves a class by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenantWeek returns the tenant's active classes whose StartsAt falls
//     in the given ISO (year, week) — the week-view calendar query.
//   - ListByTenant returns all of a tenant's active classes (no week filter).
type SchedulingStore interface {
	Save(ctx context.Context, c *ScheduledClass) error
	Get(ctx context.Context, id string) (*ScheduledClass, bool, error)
	ListByTenantWeek(ctx context.Context, tenantID string, year, week int) ([]*ScheduledClass, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*ScheduledClass, error)
}
