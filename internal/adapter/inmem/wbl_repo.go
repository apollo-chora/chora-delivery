// wbl_repo.go — in-memory repository adapter for the WblPlacement
// aggregate. Production wires a Postgres adapter against chora_delivery
// via PgBouncer; this in-memory store keeps the M15c add-only build
// dependency-free and is hot-swapped for the pg adapter when
// CHORA_DB_DSN is set (mirrors the CJ#2 CourseCJ2Port wiring in
// services/chora-delivery/cmd/server/main.go lines 519-525).
//
// The repo is tenant-scoped + soft-delete-aware:
//   - ListByTenant filters out rows whose State is WITHDRAWN by default;
//     callers wanting the WITHDRAWN slice supply state=WITHDRAWN explicitly
//     through the handler's `state` query param.
//   - Get returns the row regardless of state so handlers can resolve a
//     specific id and decide visibility themselves.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	wbl "github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

// WblRepo is an in-memory store for WblPlacement aggregates, scoped by id.
// Tenant filtering happens at query time so cross-tenant rows are never
// returned to the wrong caller (defence in depth alongside the handler
// gate).
type WblRepo struct {
	mu sync.RWMutex
	by map[string]*wbl.Placement // key = placement_id
}

// NewWblRepo returns an empty repo.
func NewWblRepo() *WblRepo { return &WblRepo{by: make(map[string]*wbl.Placement)} }

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for pg.WblRepo at cmd/server wiring.
var _ wbl.WblStore = (*WblRepo)(nil)

// Save inserts or upserts a placement keyed by ID. ctx is accepted to satisfy
// wbl.WblStore (the pg adapter uses it for RLS); the in-memory store ignores
// it. Never errors.
func (r *WblRepo) Save(_ context.Context, p *wbl.Placement) error {
	if p == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[p.ID] = p
	return nil
}

// Get returns a placement by ID and an ok flag.
func (r *WblRepo) Get(_ context.Context, id string) (*wbl.Placement, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return p, true, nil
}

// ListByTenant returns the placements for a tenant, optionally filtered
// by state. Pass the empty string to fetch every NON-withdrawn placement;
// pass a specific state (including WITHDRAWN) to scope the read.
//
// Results are sorted by ID ascending (UUIDv7 ⇒ creation order). The
// caller is expected to handle pagination at the handler layer.
//
// ctx is accepted to satisfy wbl.WblStore (the pg adapter uses it for RLS);
// the in-memory store ignores it. Never errors.
func (r *WblRepo) ListByTenant(_ context.Context, tenantID string, state wbl.PlacementState) ([]*wbl.Placement, error) {
	r.mu.RLock()
	out := make([]*wbl.Placement, 0)
	for _, p := range r.by {
		if p.TenantID != tenantID {
			continue
		}
		if state == "" {
			// Default scope excludes WITHDRAWN — handler can opt in via
			// explicit ?state=WITHDRAWN.
			if p.State == wbl.PlacementStateWithdrawn {
				continue
			}
		} else if p.State != state {
			continue
		}
		out = append(out, p)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out, nil
}
