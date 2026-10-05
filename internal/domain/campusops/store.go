// store.go — the hexagonal persistence port for the Room aggregate (CHO-2191
// SP1). The HTTP handlers depend ONLY on this interface so the backing store
// swaps between the in-memory dev adapter (repo/inmem.RoomRepo) and
// repo/pg.RoomRepo (chora_delivery.rooms — durable + RLS-isolated across pod
// restart) at cmd/server wiring. ctx-threaded so the Postgres adapter can call
// rls.ApplySession before every query.
//
// This is the durable foundation for the ratified room_id-keyed
// double-book/over-capacity gate (SP2 builds the gate; SP1 only promotes Room to
// Postgres + exposes create/list). The in-memory Registry above stays for the
// legacy S4.3 Campus/Branch skeleton; Room persistence moves to RoomStore.
package campusops

import "context"

// RoomStore is the persistence port for Room aggregates.
//
//   - Save upserts a room (create re-Saves it). Returns an error so a failed
//     durable write is loud.
//   - GetForTenant resolves a room by id, scoped to tenantID. ok=false is a
//     GENUINE MISS (absent / soft-deleted / other tenant's row); an infra/RLS
//     failure returns a non-nil error. CHO-2184: these must never collapse to
//     the same answer — a dead DB read must not read as an absent row.
//   - ListByTenant returns a tenant's active (non-soft-deleted) rooms, ordered
//     by CreatedAt (then id) — the rooms list.
type RoomStore interface {
	Save(ctx context.Context, r *Room) error
	GetForTenant(ctx context.Context, tenantID, id string) (*Room, bool, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*Room, error)
}
