// skillsfutures_repo.go — in-memory repository adapter for the
// SkillsFuturesClaim aggregate. Production (M12.3+) swaps in pg.SkillsFuturesRepo
// against chora_delivery.skillsfutures_claims with the same surface, so the
// http handler depends only on the skillsfutures.SkillsFuturesStore port. The
// domain layer never imports this package.
//
// Tenant scoping: ListByTenant filters in-process; the pg adapter leans on RLS
// via rls.ApplySession. Get returns the row regardless of tenant — the http
// handler enforces a tenant + visibility check on top.
//
// ctx is accepted on every method to satisfy skillsfutures.SkillsFuturesStore
// (the pg adapter uses it for RLS); the in-memory store ignores it.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/skillsfutures"
)

// SkillsFuturesRepo is an in-memory store for SkillsFuturesClaim aggregates.
type SkillsFuturesRepo struct {
	mu sync.RWMutex
	by map[string]*skillsfutures.SkillsFuturesClaim // key = claim_id
}

// NewSkillsFuturesRepo returns an empty repo.
func NewSkillsFuturesRepo() *SkillsFuturesRepo {
	return &SkillsFuturesRepo{by: make(map[string]*skillsfutures.SkillsFuturesClaim)}
}

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for pg.SkillsFuturesRepo at cmd/server wiring.
var _ skillsfutures.SkillsFuturesStore = (*SkillsFuturesRepo)(nil)

// Save inserts or upserts a claim. Mirrors the other in-mem repos —
// no optimistic-concurrency or version check (forward work). Never errors.
func (r *SkillsFuturesRepo) Save(_ context.Context, c *skillsfutures.SkillsFuturesClaim) error {
	if c == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[c.ID] = c
	return nil
}

// Get returns a claim by ID and a bool ok flag. The caller is responsible
// for enforcing tenant scope BEFORE returning the row to the wire.
func (r *SkillsFuturesRepo) Get(_ context.Context, id string) (*skillsfutures.SkillsFuturesClaim, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return c, true, nil
}

// ListByTenant returns claims for the given tenant, optionally filtered by
// state. Empty stateFilter returns all states. Results sorted by ID
// (UUIDv7 ⇒ creation order). The pg adapter equivalent is a single
// SELECT … WHERE tenant_id = $1 [AND state = $2] ORDER BY id.
func (r *SkillsFuturesRepo) ListByTenant(
	_ context.Context,
	tenantID string,
	stateFilter skillsfutures.ClaimState,
) ([]*skillsfutures.SkillsFuturesClaim, error) {
	r.mu.RLock()
	out := make([]*skillsfutures.SkillsFuturesClaim, 0, len(r.by))
	for _, c := range r.by {
		if c.TenantID != tenantID {
			continue
		}
		if stateFilter != "" && c.State != stateFilter {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out, nil
}
