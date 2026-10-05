// Package inmem holds in-memory repository adapters for the chora-delivery
// skeleton. Production implementations (M12+) will swap in a Postgres adapter
// against chora_delivery via PgBouncer; the domain layer is unchanged.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// CourseRepo is an in-memory store for Course aggregates, scoped by tenant.
//
// ADR-236 D1 — this is the no-pool dev fallback for the canonical durable
// store (pg.CourseRepo); production wires pg.CourseRepo behind the pool
// gate (cmd/server/main.go). Satisfies domain.CourseRepo.
type CourseRepo struct {
	mu sync.RWMutex
	by map[string]*domain.Course // key = course_id
}

// NewCourseRepo returns an empty repo.
func NewCourseRepo() *CourseRepo { return &CourseRepo{by: make(map[string]*domain.Course)} }

// Compile-time assertion: inmem.CourseRepo satisfies the domain-owned
// delivery.CourseRepo port (ADR-236 D1) — the same port pg.CourseRepo
// satisfies, so cmd/server can swap between them behind the pool gate.
var _ domain.CourseRepo = (*CourseRepo)(nil)

// Save inserts or upserts a course. ctx is accepted to satisfy the (ctx,
// error) persistence port (the pg adapter uses it for RLS); the in-memory
// store ignores it. Never errors.
func (r *CourseRepo) Save(_ context.Context, c *domain.Course) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[c.ID] = c
	return nil
}

// Get returns a course by (tenantID, id) and a bool ok flag. ok=false is a
// GENUINE MISS — no matching row, wrong tenant, or soft-deleted; the
// in-memory store never returns an infra error (CHO-2201 shape parity).
//
// ADR-236 D1 — widened from Get(ctx, id) (no tenant param at all: any
// caller holding a course_id could read another tenant's course out of the
// shared map). Now tenant-scoped like pg.CourseRepo.Get, and excludes
// soft-deleted rows like pg's `deleted_at IS NULL` filter (and this
// package's sibling InMemCourseCJ2Store.Get).
func (r *CourseRepo) Get(_ context.Context, tenantID, id string) (*domain.Course, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.by[id]
	if !ok || c.TenantID != tenantID || c.DeletedAt != nil {
		return nil, false, nil
	}
	return c, true, nil
}

// ListByTenant returns the non-soft-deleted courses for a tenant, sorted
// by ID (UUIDv7 ⇒ creation order), plus the pre-pagination total count.
// ctx is accepted to satisfy the domain.CourseRepo port; the in-memory
// store ignores it and never errors.
func (r *CourseRepo) ListByTenant(_ context.Context, tenantID string, offset, limit int) ([]*domain.Course, int, error) {
	r.mu.RLock()
	out := make([]*domain.Course, 0, len(r.by))
	for _, c := range r.by {
		if c.TenantID != tenantID || c.DeletedAt != nil {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	total := len(out)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return out[offset:end], total, nil
}

// BookingRepo is an in-memory store for Booking aggregates.
type BookingRepo struct {
	mu sync.RWMutex
	by map[string]*domain.Booking
}

// NewBookingRepo returns an empty repo.
func NewBookingRepo() *BookingRepo { return &BookingRepo{by: make(map[string]*domain.Booking)} }

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for the pg.BookingRepo at cmd/server wiring.
var _ domain.BookingPort = (*BookingRepo)(nil)

// Save inserts or upserts a booking. ctx is accepted to satisfy
// domain.BookingPort (the pg adapter uses it for RLS); the in-memory store
// ignores it. Never errors.
func (r *BookingRepo) Save(_ context.Context, b *domain.Booking) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[b.ID] = b
	return nil
}

// Get returns a booking by ID and a bool ok flag.
func (r *BookingRepo) Get(_ context.Context, id string) (*domain.Booking, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return b, true, nil
}

// ListByTenant returns the tenant's active (non-soft-deleted) bookings,
// newest-first. Mirrors the pg.BookingRepo.ListByTenant ordering so the
// dev + prod GET /api/bookings responses are shape-identical.
func (r *BookingRepo) ListByTenant(_ context.Context, tenantID string) ([]*domain.Booking, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*domain.Booking
	for _, b := range r.by {
		if b.TenantID == tenantID && b.DeletedAt == nil {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ReserveSeatAndSave enforces the class-capacity invariant under the repo's
// write lock — the in-memory analogue of the pg adapter's advisory-lock txn.
// It counts the class's active bookings and stores b iff the count is below
// maxCapacity, else returns ErrClassAtCapacity. Holding the lock across the
// count AND the store makes it atomic, so a concurrent stampede lets exactly
// maxCapacity reservations win — no over-booking (ADR-236 D2).
func (r *BookingRepo) ReserveSeatAndSave(_ context.Context, classID string, maxCapacity int, b *domain.Booking) error {
	if b == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, existing := range r.by {
		if existing.ClassID == classID && existing.DeletedAt == nil {
			count++
		}
	}
	if count >= maxCapacity {
		return domain.ErrClassAtCapacity
	}
	r.by[b.ID] = b
	return nil
}
