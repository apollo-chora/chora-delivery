// campus_store.go - the hexagonal persistence port for the Campus aggregate
// (CHO-2293).
//
// Campus was the last in-memory binding in chora-delivery serving production
// reads and writes: cmd/server wired campusops.NewRegistry() unconditionally,
// with no pool gate and no Postgres alternative, then seeded a hardcoded demo
// row. Every campus a real tenant created died with the pod and the fake row
// resurrected on every boot. Worse, the binding was never registered with the
// ADR-236 D5 durability guard, so the boot log reported in_memory=0 while an
// in-memory store was demonstrably serving writes: a clean verdict that was
// true for the 18 ports it checked and blind to the 19th.
//
// This port mirrors RoomStore exactly (see store.go). The HTTP handlers depend
// ONLY on this interface so the backing store swaps between the in-memory dev
// adapter (repo/inmem.CampusRepo) and repo/pg.CampusRepo (chora_delivery.campuses,
// durable + RLS-isolated across pod restart) at cmd/server wiring. ctx-threaded
// so the Postgres adapter can call rls.ApplySession before every query.
//
// Scope note: Branch and Room on the legacy Registry are NOT promoted here.
// Room already has its own durable lane (RoomStore, CHO-2191 SP1), and Branch
// has no HTTP surface and no writer, so giving it a table would create a
// relation nothing ever writes. Reconciling the two room concepts is CHO-2294.
package campusops

import "context"

// CampusStore is the persistence port for Campus aggregates.
//
//   - Save upserts a campus (create re-Saves it). Returns an error so a failed
//     durable write is loud, never a fabricated success.
//   - GetForTenant resolves a campus by id, scoped to tenantID. ok=false is a
//     GENUINE MISS (absent / soft-deleted / other tenant's row); an infra or RLS
//     failure returns a non-nil error. CHO-2184: these must never collapse to
//     the same answer, because a dead DB read must not read as an absent row.
//   - ListByTenant returns a tenant's active (non-soft-deleted) campuses,
//     ordered by CreatedAt then id. The empty result is a non-nil empty slice so
//     the HTTP layer renders [] and never null.
type CampusStore interface {
	Save(ctx context.Context, c *Campus) error
	GetForTenant(ctx context.Context, tenantID, id string) (*Campus, bool, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*Campus, error)
}
