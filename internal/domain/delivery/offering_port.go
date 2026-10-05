// offering_port.go — the hexagonal persistence port for the Offering aggregate
// (R+ four-delivery-mode refactor W1, per ADR-190).
//
// Mirrors BookingPort (booking_port.go): the HTTP handlers depend ONLY on this
// interface so the backing store swaps between the in-memory dev adapter
// (inmem.OfferingRepo) and the durable pg.OfferingRepo (chora_delivery-backed)
// at cmd/server wiring.
//
// ctx-threaded so the Postgres adapter can call rls.ApplySession before every
// query — RLS reads the tenant from tracing.TenantIDFromContext, so callers
// MUST set it via tracing.WithTenantID(ctx, tenantID) before invoking.
package delivery

import "context"

// OfferingPort is the persistence port for Offering aggregates.
//
//   - Save upserts an offering (create + every FSM transition call it).
//     Returns an error so a failed durable write is loud.
//   - Get resolves an offering by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenant returns the tenant's active (non-soft-deleted) offerings,
//     ordered by id (UUIDv7 ⇒ creation order). Powers GET /api/v1/offerings.
//   - Search runs the finder query (free-text + delivery_type/state filters,
//     server multi-sort, keyset cursor pagination, query-minus-self facets) for
//     GET /api/v1/search/offerings (W2.A). The query carries the tenant; the pg
//     adapter additionally RLS-scopes it. SearchOfferings is the pure reference.
type OfferingPort interface {
	Save(ctx context.Context, o *Offering) error
	Get(ctx context.Context, id string) (*Offering, bool, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*Offering, error)
	Search(ctx context.Context, q OfferingQuery) (*OfferingSearchPage, error)
}
