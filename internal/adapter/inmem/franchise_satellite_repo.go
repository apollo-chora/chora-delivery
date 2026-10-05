// franchise_satellite_repo.go: in-memory franchise.Store for dev / unit tests
// (ADR-192 D1 mapping). Production wires pg.FranchiseSatelliteRepo
// (chora_delivery.franchise_satellite, durable + RLS-scoped); this store is
// dev-only and lost on restart. Tenant scoping is enforced at the handler
// level (owner check after Get), mirroring how the exam inmem repos pair with
// their handlers; the pg adapter adds RLS defence-in-depth.
package inmem

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

// FranchiseSatelliteRepo is an in-memory store for owner-satellite mappings.
type FranchiseSatelliteRepo struct {
	mu sync.RWMutex
	by map[string]*franchise.FranchiseSatellite
}

// NewFranchiseSatelliteRepo returns an empty repo.
func NewFranchiseSatelliteRepo() *FranchiseSatelliteRepo {
	return &FranchiseSatelliteRepo{by: make(map[string]*franchise.FranchiseSatellite)}
}

// Compile-time assertion: drop-in for pg.FranchiseSatelliteRepo.
var _ franchise.Store = (*FranchiseSatelliteRepo)(nil)

// Save inserts a live mapping; a live duplicate (owner, satellite) pair
// returns franchise.ErrDuplicateMapping (the pg partial-unique-index mirror).
func (r *FranchiseSatelliteRepo) Save(_ context.Context, m *franchise.FranchiseSatellite) error {
	if m == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.by {
		if existing.DeletedAt == nil &&
			existing.OwnerTenantID == m.OwnerTenantID &&
			existing.SatelliteTenantID == m.SatelliteTenantID {
			return franchise.ErrDuplicateMapping
		}
	}
	cp := *m
	r.by[m.ID] = &cp
	return nil
}

// Get resolves a LIVE mapping by id (revoked rows read as a genuine miss,
// matching the pg adapter's deleted_at IS NULL filter).
func (r *FranchiseSatelliteRepo) Get(_ context.Context, id string) (*franchise.FranchiseSatellite, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.by[id]
	if !ok || m.DeletedAt != nil {
		return nil, false, nil
	}
	cp := *m
	return &cp, true, nil
}

// ListByOwner returns the owner's live mappings, oldest first.
func (r *FranchiseSatelliteRepo) ListByOwner(_ context.Context, ownerTenantID string) ([]*franchise.FranchiseSatellite, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*franchise.FranchiseSatellite
	for _, m := range r.by {
		if m.DeletedAt == nil && m.OwnerTenantID == ownerTenantID {
			cp := *m
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Revoke soft-deletes a live mapping. ok=false is a genuine miss.
func (r *FranchiseSatelliteRepo) Revoke(_ context.Context, id string, when time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.by[id]
	if !ok || m.DeletedAt != nil {
		return false, nil
	}
	if err := m.Revoke(when); err != nil {
		return false, err
	}
	return true, nil
}
