// offering_repo.go — in-memory repository for the Offering aggregate
// (R+ four-delivery-mode refactor W1).
//
// Hexagonal layout: adapter depends on the pure domain package
// internal/domain/delivery (the domain never imports this file). Mirrors
// exam_repo.go — a drop-in for pg.OfferingRepo at cmd/server wiring; used by
// handler tests so they run with zero DB.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// OfferingRepo is an in-memory store for Offering aggregates, keyed by id.
type OfferingRepo struct {
	mu sync.RWMutex
	by map[string]*domain.Offering // key = offering id
}

// NewOfferingRepo returns an empty repo.
func NewOfferingRepo() *OfferingRepo {
	return &OfferingRepo{by: make(map[string]*domain.Offering)}
}

// Compile-time assertion: satisfies the domain port so it stays a drop-in for
// pg.OfferingRepo at cmd/server wiring.
var _ domain.OfferingPort = (*OfferingRepo)(nil)

// Save inserts or upserts an offering by ID. ctx is accepted to satisfy the
// port (the pg adapter uses it for RLS); the in-memory store ignores it.
func (r *OfferingRepo) Save(_ context.Context, o *domain.Offering) error {
	if o == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[o.ID] = o
	return nil
}

// Get returns an offering by ID and a bool ok flag.
func (r *OfferingRepo) Get(_ context.Context, id string) (*domain.Offering, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return o, true, nil
}

// ListByTenant returns the non-soft-deleted offerings for a tenant, sorted by
// ID (UUIDv7 ⇒ creation order). The returned slice is a fresh copy.
func (r *OfferingRepo) ListByTenant(_ context.Context, tenantID string) ([]*domain.Offering, error) {
	r.mu.RLock()
	out := make([]*domain.Offering, 0, len(r.by))
	for _, o := range r.by {
		if o.TenantID != tenantID || o.DeletedAt != nil {
			continue
		}
		out = append(out, o)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}

// Search delegates to the pure domain engine over a snapshot of every row;
// SearchOfferings applies tenant + soft-delete scoping, filters, sort, keyset,
// and facets. Mirrors the pg adapter's SQL semantics so handler tests run
// DB-free with identical results.
func (r *OfferingRepo) Search(_ context.Context, q domain.OfferingQuery) (*domain.OfferingSearchPage, error) {
	r.mu.RLock()
	all := make([]*domain.Offering, 0, len(r.by))
	for _, o := range r.by {
		all = append(all, o)
	}
	r.mu.RUnlock()
	return domain.SearchOfferings(all, q), nil
}
