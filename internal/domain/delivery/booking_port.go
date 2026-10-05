// booking_port.go — the hexagonal persistence port for the Booking aggregate.
//
// Mirrors CataloguePort (catalogue.go) + EnrollmentPort (enrollment.go): the
// HTTP handlers depend ONLY on this interface so the backing store can be
// swapped between the in-memory dev adapter and the pg.BookingRepo
// (chora_delivery-backed, durable across pod restart) at cmd/server wiring.
//
// ctx-threaded (unlike the pre-pg inmem.BookingRepo) so the Postgres adapter
// can call rls.ApplySession before every query — RLS reads the tenant from
// tracing.TenantIDFromContext, so callers MUST set it via
// tracing.WithTenantID(ctx, tenantID) before invoking. This closes the
// HANDOFF_RPLUS §6 follow-up: bookings was inmem-only (ephemeral) with no
// GET-list route; the FE (CHO-1620 T3) had to session-track created bookings.
package delivery

import "context"

// BookingPort is the persistence port for Booking aggregates.
//
//   - Save upserts a booking (create + status-transition both call it).
//     Returns an error so a failed durable write is loud — a dropped Save
//     loses a learner's seat reservation.
//   - Get resolves a booking by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenant returns the tenant's active (non-soft-deleted) bookings,
//     newest-first. Powers GET /api/bookings.
//   - ReserveSeatAndSave enforces the class-capacity invariant DURABLY: in one
//     serialised operation it counts the class's active bookings and inserts b
//     iff the live count is < maxCapacity, else returns ErrClassAtCapacity. The
//     pg adapter serialises concurrent reservations for the same class with a
//     transaction-scoped advisory lock (cross-pod-correct); the in-memory
//     adapter serialises with its mutex. This replaces the retired in-memory
//     Class.reserveSeat (single-pod only) — the D2 booking path MUST use this,
//     never a bare Save, so the gate fires (ADR-236 D2).
type BookingPort interface {
	Save(ctx context.Context, b *Booking) error
	Get(ctx context.Context, id string) (*Booking, bool, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*Booking, error)
	ReserveSeatAndSave(ctx context.Context, classID string, maxCapacity int, b *Booking) error
}
