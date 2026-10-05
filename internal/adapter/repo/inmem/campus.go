package inmem

import (
	"context"
	"sort"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

// CampusRepo stores Campus aggregates, scoped by tenant. Drop-in for
// pg.CampusRepo at cmd/server wiring (dev / single-pod only). CHO-2293.
//
// This adapter is the CHORA_DB_DSN-unset fallback. It is deliberately shaped so
// the ADR-236 D5 durability guard classifies it as IN_MEMORY (it holds a data
// map and no *pgxpool.Pool), which is the negative control proving the guard can
// still report the bad state now that campus is a registered binding.
type CampusRepo struct {
	mu sync.RWMutex
	by map[string]*campusops.Campus // campus_id -> *Campus
}

// NewCampusRepo returns an empty repo.
func NewCampusRepo() *CampusRepo {
	return &CampusRepo{by: make(map[string]*campusops.Campus)}
}

// Compile-time assertion: satisfies the domain port.
var _ campusops.CampusStore = (*CampusRepo)(nil)

// Save upserts a campus. ctx is accepted (the pg adapter uses it for RLS); the
// in-memory store ignores it. Never errors.
func (r *CampusRepo) Save(_ context.Context, c *campusops.Campus) error {
	if c == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[c.ID] = c
	return nil
}

// GetForTenant returns a campus by id, scoped to tenantID. ok=false on absent /
// soft-deleted / other-tenant row (a genuine miss); never errors.
func (r *CampusRepo) GetForTenant(_ context.Context, tenantID, id string) (*campusops.Campus, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.by[id]
	if !ok || c.DeletedAt != nil || c.TenantID != tenantID {
		return nil, false, nil
	}
	return c, true, nil
}

// ListByTenant returns a tenant's non-soft-deleted campuses, ordered by
// CreatedAt then ID (UUIDv7 gives a creation-order tiebreak). The empty result
// is a non-nil empty slice so the HTTP layer renders [] and never null.
func (r *CampusRepo) ListByTenant(_ context.Context, tenantID string) ([]*campusops.Campus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*campusops.Campus, 0, len(r.by))
	for _, c := range r.by {
		if c.TenantID != tenantID || c.DeletedAt != nil {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
