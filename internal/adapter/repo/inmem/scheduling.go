// Package inmem (repo/inmem) holds in-memory adapters for the new
// rostering / scheduling / attendance / certification sub-domains.
//
// Distinct from the existing internal/adapter/inmem (Course/Class/Booking
// repos owned by the in-flight catalogue+enrollment agent). Production
// (M12+) will swap each of these for Postgres-backed adapters reading
// from chora_delivery via PgBouncer.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

// SchedulingRepo stores ScheduledClass aggregates, scoped by tenant.
type SchedulingRepo struct {
	mu sync.RWMutex
	by map[string]*scheduling.ScheduledClass // class_id -> *ScheduledClass
}

// NewSchedulingRepo returns an empty repo.
func NewSchedulingRepo() *SchedulingRepo {
	return &SchedulingRepo{by: make(map[string]*scheduling.ScheduledClass)}
}

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for pg.SchedulingRepo at cmd/server wiring.
var _ scheduling.SchedulingStore = (*SchedulingRepo)(nil)

// Save inserts or upserts a scheduled class. ctx is accepted to satisfy
// scheduling.SchedulingStore (the pg adapter uses it for RLS); the in-memory
// store ignores it. Never errors.
func (r *SchedulingRepo) Save(_ context.Context, c *scheduling.ScheduledClass) error {
	if c == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[c.ID] = c
	return nil
}

// Get returns a class by ID and ok flag.
func (r *SchedulingRepo) Get(_ context.Context, id string) (*scheduling.ScheduledClass, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.by[id]
	return c, ok, nil
}

// ListByTenantWeek returns non-soft-deleted classes for tenant whose
// StartsAt falls in (year, week). Sorted by ID (UUIDv7 ⇒ creation order).
func (r *SchedulingRepo) ListByTenantWeek(_ context.Context, tenantID string, year, week int) ([]*scheduling.ScheduledClass, error) {
	r.mu.RLock()
	out := make([]*scheduling.ScheduledClass, 0, len(r.by))
	for _, c := range r.by {
		if c.TenantID != tenantID || c.DeletedAt != nil {
			continue
		}
		if !scheduling.InISOWeek(c, year, week) {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}

// ListByTenant returns all non-soft-deleted classes for tenant. Used when
// no week filter is supplied.
func (r *SchedulingRepo) ListByTenant(_ context.Context, tenantID string) ([]*scheduling.ScheduledClass, error) {
	r.mu.RLock()
	out := make([]*scheduling.ScheduledClass, 0, len(r.by))
	for _, c := range r.by {
		if c.TenantID != tenantID || c.DeletedAt != nil {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}
