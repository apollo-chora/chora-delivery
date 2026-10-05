// store.go — the hexagonal persistence port for the Wbl Placement aggregate.
//
// Mirrors exam.ExamStore (the R+ durability sweep template): the HTTP handlers
// depend ONLY on this interface so the backing store swaps between the
// in-memory dev adapter and pg.WblRepo (chora_delivery.wbl_placements — durable
// across pod restart) at cmd/server wiring. ctx-threaded so the Postgres
// adapter can call rls.ApplySession before every query (RLS reads the tenant
// from tracing.TenantIDFromContext). This closes the R+ durability debt: WBL
// placements were inmem-only (ephemeral, lost on pod restart).
package wbl

import "context"

// WblStore is the persistence port for Wbl Placement aggregates.
//
//   - Save upserts a placement (create + every FSM transition / RecordHours /
//     SetEvaluatorNotes / Withdraw re-Save call it). Returns an error so a
//     failed durable write is loud.
//   - Get resolves a placement by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenant returns the tenant's placements, optionally filtered by
//     state. Empty state excludes WITHDRAWN (the default scope); an explicit
//     state scopes to exactly that state — matching inmem.WblRepo semantics.
type WblStore interface {
	Save(ctx context.Context, p *Placement) error
	Get(ctx context.Context, id string) (*Placement, bool, error)
	ListByTenant(ctx context.Context, tenantID string, state PlacementState) ([]*Placement, error)
}
