// store.go — the hexagonal persistence port for the Credential aggregate.
//
// Mirrors offering_session.Store: the HTTP handlers depend ONLY on this
// interface so the backing store swaps between the in-memory dev adapter
// (repo/inmem.CredentialRepo) and repo/pg.CredentialRepo (chora_delivery.
// credentials — durable + RLS-isolated across pod restart) at cmd/server
// wiring. ctx-threaded so the Postgres adapter can call rls.ApplySession
// before every query — RLS reads the tenant from tracing.TenantIDFromContext,
// so callers MUST set it via tracing.WithTenantID(ctx, tenantID) first.
package credential

import "context"

// Store is the persistence port for Credential aggregates.
//
//   - Save upserts a credential (create + Archive re-Save it). Returns an error
//     so a failed durable write is loud.
//   - Get resolves a credential by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenant returns a tenant's active (non-soft-deleted) credentials —
//     the catalogue, decoupled from courses — ordered by Title (then id). Powers
//     GET /api/v1/credentials.
type Store interface {
	Save(ctx context.Context, c *Credential) error
	Get(ctx context.Context, id string) (*Credential, bool, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*Credential, error)
}
