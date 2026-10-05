// project_group_repo.go — in-memory repository adapter for the ProjectGroup
// aggregate (M15b R+ build-out).
//
// Production wiring swaps in pg.ProjectGroupRepo against chora_delivery via
// PgBouncer (chora_delivery.project_groups — durable + RLS-isolated); the
// domain layer is unchanged and the handler depends ONLY on the
// project_group.ProjectGroupStore port. ctx is accepted to satisfy that port
// (the pg adapter uses it for rls.ApplySession); the in-memory store ignores
// it.
//
// Per .claude/rules/ddd-enforcement.md HARD RULE — cross-database queries
// FORBIDDEN. ProjectGroup rows live in chora_delivery; cross-aggregate
// references (course_id) are UUIDs without FK constraint.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	pgdomain "github.com/apollo-chora/chora-delivery/internal/domain/project_group"
)

// ProjectGroupRepo is an in-memory store for ProjectGroup aggregates, scoped
// by tenant. Goroutine-safe via sync.RWMutex.
type ProjectGroupRepo struct {
	mu sync.RWMutex
	by map[string]*pgdomain.ProjectGroup // key = group_id
}

// NewProjectGroupRepo returns an empty repo.
func NewProjectGroupRepo() *ProjectGroupRepo {
	return &ProjectGroupRepo{by: make(map[string]*pgdomain.ProjectGroup)}
}

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for pg.ProjectGroupRepo at cmd/server wiring.
var _ pgdomain.ProjectGroupStore = (*ProjectGroupRepo)(nil)

// Save inserts or upserts a project group. Defensive shallow copy keeps the
// caller's pointer decoupled from the stored mirror. ctx is accepted to
// satisfy pgdomain.ProjectGroupStore (the pg adapter uses it for RLS); the
// in-memory store ignores it. Never errors.
func (r *ProjectGroupRepo) Save(_ context.Context, g *pgdomain.ProjectGroup) error {
	if g == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *g
	cp.Members = append([]pgdomain.Member(nil), g.Members...)
	r.by[g.ID] = &cp
	return nil
}

// Get returns a group by ID and a bool ok flag. ok=false when the group is
// soft-deleted (deleted_at non-nil).
func (r *ProjectGroupRepo) Get(_ context.Context, id string) (*pgdomain.ProjectGroup, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.by[id]
	if !ok || g.DeletedAt != nil {
		return nil, false, nil
	}
	// Return defensive copy.
	cp := *g
	cp.Members = append([]pgdomain.Member(nil), g.Members...)
	return &cp, true, nil
}

// ListByTenant returns the non-soft-deleted groups for a tenant, sorted by
// created_at DESC (newest first).
func (r *ProjectGroupRepo) ListByTenant(_ context.Context, tenantID string) ([]*pgdomain.ProjectGroup, error) {
	r.mu.RLock()
	out := make([]*pgdomain.ProjectGroup, 0, len(r.by))
	for _, g := range r.by {
		if g.TenantID != tenantID || g.DeletedAt != nil {
			continue
		}
		cp := *g
		cp.Members = append([]pgdomain.Member(nil), g.Members...)
		out = append(out, &cp)
	}
	r.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// ListByCourse returns the non-soft-deleted groups for a tenant+course pair,
// sorted by created_at DESC.
func (r *ProjectGroupRepo) ListByCourse(_ context.Context, tenantID, courseID string) ([]*pgdomain.ProjectGroup, error) {
	r.mu.RLock()
	out := make([]*pgdomain.ProjectGroup, 0, len(r.by))
	for _, g := range r.by {
		if g.TenantID != tenantID || g.CourseID != courseID || g.DeletedAt != nil {
			continue
		}
		cp := *g
		cp.Members = append([]pgdomain.Member(nil), g.Members...)
		out = append(out, &cp)
	}
	r.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		// Stable sort by created_at DESC, falling back to ID for tied
		// timestamps (UUIDv7 carries unix_ts_ms so tied stamps are rare).
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return strings.Compare(out[i].ID, out[j].ID) < 0
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
