// store.go — the hexagonal persistence port for the OfferingSession aggregate.
//
// Mirrors scheduling.SchedulingStore: the HTTP handlers depend ONLY on this
// interface so the backing store swaps between the in-memory dev adapter
// (repo/inmem.OfferingSessionRepo) and repo/pg.OfferingSessionRepo
// (chora_delivery.offering_sessions — durable + RLS-isolated across pod
// restart) at cmd/server wiring. ctx-threaded so the Postgres adapter can call
// rls.ApplySession before every query.
package offering_session

import "context"

// Store is the persistence port for OfferingSession aggregates.
//
//   - Save upserts a session (create re-Saves it). Returns an error so a
//     failed durable write is loud.
//   - Get resolves a session by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByOffering returns a tenant's active (non-soft-deleted) sessions for
//     one offering, ordered by StartsAt (then id) — the schedule list.
type Store interface {
	Save(ctx context.Context, s *OfferingSession) error
	Get(ctx context.Context, id string) (*OfferingSession, bool, error)
	ListByOffering(ctx context.Context, tenantID, offeringID string) ([]*OfferingSession, error)
}
