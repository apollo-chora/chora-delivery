// store.go — the hexagonal persistence port for the Course Application
// aggregate.
//
// Mirrors exam.ExamStore (R+ durability sweep): the HTTP handlers + the
// PaymentsSubscriber depend ONLY on this interface so the backing store
// swaps between the in-memory dev adapter (repo/inmem.ApplicationRepo) and
// repo/pg.ApplicationRepo (chora_delivery.applications +
// application_state_history — durable + RLS-isolated across pod restart) at
// cmd/server wiring. ctx-threaded so the Postgres adapter can call
// rls.ApplySession before every query (RLS reads tenant from
// tracing.TenantIDFromContext + gcid from tracing.GCIDFromContext).
//
// This closes the R+ durability debt: Applications were inmem-only
// (ephemeral, lost on pod restart) even though pg.ApplicationRepo +
// migration 0002_applications.sql already existed — the wiring + the port
// extraction land here in Wave 2 (CHO-1580).
package application

import "context"

// SubmitInput is the natural key for the idempotent submit
// (tenant_id, course_id, gcid; class_id optional). Lives in the domain so
// both the inmem + pg adapters share one input type and the handler can
// depend on the port rather than a concrete repo's value-bag.
type SubmitInput struct {
	TenantID string
	CourseID string
	ClassID  string // optional — chosen class instance
	GCID     string
}

// ListByTenantInput is the value-bag for the admin queue's ListByTenant.
// Carries an optional status filter (empty == no filter) + pagination.
type ListByTenantInput struct {
	TenantID string
	Status   Status // optional — empty == no filter
	Offset   int
	Limit    int
}

// ApplicationPort is the persistence port for Course Application aggregates.
//
//   - SubmitOrGet is the idempotent learner submit (INSERT … ON CONFLICT on
//     the (tenant, course, gcid) natural key; returns the existing aggregate
//     with created=false on conflict).
//   - Get resolves one application by id, tenant-scoped.
//   - ListByGCID returns a learner's own applications (paginated).
//   - ListByTenant returns the admin queue's tenant-wide list (optional
//     status filter + pagination) — the R+ training-admin review surface.
//   - Save persists the aggregate's mutable state (every FSM transition +
//     payment/invoice attach re-Save it) and appends new history rows.
type ApplicationPort interface {
	SubmitOrGet(ctx context.Context, in SubmitInput) (*Application, bool, error)
	Get(ctx context.Context, tenantID, applicationID string) (*Application, bool, error)
	ListByGCID(ctx context.Context, tenantID, gcid string, offset, limit int) ([]*Application, int, error)
	ListByTenant(ctx context.Context, in ListByTenantInput) ([]*Application, int, error)
	Save(ctx context.Context, app *Application) error
}
