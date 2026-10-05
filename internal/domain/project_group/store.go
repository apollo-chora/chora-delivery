// store.go — the hexagonal persistence port for the ProjectGroup aggregate.
//
// Mirrors exam.ExamStore (CHO-1580 R+ durability sweep): the HTTP handlers
// depend ONLY on this interface so the backing store swaps between the
// in-memory dev adapter and pg.ProjectGroupRepo
// (chora_delivery.project_groups — durable across pod restart) at cmd/server
// wiring. ctx-threaded so the Postgres adapter can call rls.ApplySession
// before every query (RLS reads the tenant from tracing.TenantIDFromContext).
// This closes the R+ durability debt: ProjectGroups was inmem-only (ephemeral,
// lost on pod restart).
package project_group

import "context"

// ProjectGroupStore is the persistence port for ProjectGroup aggregates.
//
//   - Save upserts a group (create + every FSM transition re-Save call it).
//     Returns an error so a failed durable write is loud.
//   - Get resolves a group by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenant returns the tenant's active (non-soft-deleted) groups.
//   - ListByCourse returns the tenant+course pair's active groups.
type ProjectGroupStore interface {
	Save(ctx context.Context, g *ProjectGroup) error
	Get(ctx context.Context, id string) (*ProjectGroup, bool, error)
	ListByTenant(ctx context.Context, tenantID string) ([]*ProjectGroup, error)
	ListByCourse(ctx context.Context, tenantID, courseID string) ([]*ProjectGroup, error)
}
