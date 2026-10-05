// store.go: the hexagonal persistence port for FranchiseSatellite mappings.
//
// Mirrors exam.ExamFormStore: HTTP handlers depend ONLY on this interface so
// the backing store swaps between inmem (dev/tests) and pg (prod,
// chora_delivery.franchise_satellite) at cmd/server wiring. ctx-threaded so
// the Postgres adapter calls rls.ApplySession before every query (RLS reads
// the tenant, the OWNER tenant for this table, from
// tracing.TenantIDFromContext).
//
// Revoke is an explicit UPDATE, never an upsert: an upsert cannot soft-delete
// (the Save path never writes deleted_at), so revocation gets its own verb.
package franchise

import (
	"context"
	"time"
)

// Store is the persistence port for FranchiseSatellite mappings.
//
//   - Save inserts a new live mapping. A live duplicate (owner, satellite)
//     returns ErrDuplicateMapping; any other error is loud.
//   - Get resolves a live (non-revoked) mapping by id. ok=false is a GENUINE
//     MISS; an infra/RLS failure returns a non-nil error (CHO-2184: a dead
//     read must never masquerade as an absent row).
//   - ListByOwner returns the owner's live mappings.
//   - Revoke soft-deletes by id. ok=false means no live row matched (genuine
//     miss); an infra failure returns a non-nil error.
type Store interface {
	Save(ctx context.Context, m *FranchiseSatellite) error
	Get(ctx context.Context, id string) (*FranchiseSatellite, bool, error)
	ListByOwner(ctx context.Context, ownerTenantID string) ([]*FranchiseSatellite, error)
	Revoke(ctx context.Context, id string, when time.Time) (bool, error)
}
