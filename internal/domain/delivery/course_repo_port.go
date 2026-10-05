// course_repo_port.go — the hexagonal persistence port for the legacy
// (pre-CJ#2) Course aggregate shape.
//
// ADR-236 D1 (Content Delivery Course/Class/Scheduling Durability): the
// `courses` table carries two Go-side interfaces onto one row —
// CourseCJ2Port (course_cj2_port.go; the state-FSM authoring/release shape
// used by /api/v1/courses) and this port (the original Save/Get/ListByTenant
// shape used by the legacy /api/courses + gRPC CreateCourse). This ADR does
// NOT merge the two — it only makes THIS shape's storage durable + wires it
// everywhere, mirroring BookingPort (booking_port.go) + CataloguePort
// (catalogue.go): the HTTP + gRPC adapters depend ONLY on this interface so
// the backing store can be swapped between the in-memory dev adapter
// (internal/adapter/inmem.CourseRepo) and pg.CourseRepo (internal/adapter/
// repo/pg — chora_delivery-backed, durable across pod restart + multi-pod
// consistent) at cmd/server wiring.
//
// Before ADR-236 D1, Deps.Courses / the gRPC CourseRepo were bound
// UNCONDITIONALLY to inmem.CourseRepo — no pool gate, no boot log — even
// though pg.CourseRepo already carried the identical Save/Get/ListByTenant
// methods (dead code, zero callers). Every course created through the live
// POST /api/courses route (phyllis composite-create) or gRPC CreateCourse
// died on pod restart and was invisible to sibling pods.
//
// ctx-threaded so the Postgres adapter can call rls.ApplySession before
// every query — RLS reads the tenant from tracing.TenantIDFromContext, so
// callers MUST set it via tracing.WithTenantID(ctx, tenantID) before
// invoking (rls.ApplySession fails loud with ErrNoTenantContext otherwise;
// see reusable_gotcha_rls_tenant_from_ctx_not_param — the GUC comes from the
// validated session context, never from a caller-supplied parameter).
package delivery

import "context"

// CourseRepo is the persistence port for the legacy (pre-CJ#2) Course
// aggregate shape.
//
//   - Save upserts a Course (create + update both call it). Returns an
//     error so a failed durable write is loud — a swallowed Save reported a
//     CreateCourse success on the wire while the row was lost.
//   - Get resolves a Course by (tenantID, courseID). ok=false is a GENUINE
//     MISS — no matching row, wrong tenant, or soft-deleted; an infra/RLS
//     failure returns a non-nil error instead. The tenant scoping is
//     enforced by the adapter (pg: SQL WHERE tenant_id=$2 + RLS; in-memory:
//     an explicit tenant compare) — callers must NOT additionally trust an
//     unscoped Get to filter by tenant themselves.
//   - ListByTenant returns the tenant's active (non-soft-deleted) courses,
//     offset+limit paginated, plus the pre-pagination total count. Powers
//     GET /api/courses.
type CourseRepo interface {
	Save(ctx context.Context, c *Course) error
	Get(ctx context.Context, tenantID, courseID string) (*Course, bool, error)
	ListByTenant(ctx context.Context, tenantID string, offset, limit int) ([]*Course, int, error)
}
