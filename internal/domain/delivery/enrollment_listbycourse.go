// enrollment_listbycourse.go — ADD-ONLY extension to EnrollmentRegistry +
// InMemEnrollmentStore that surfaces a per-course iterator. Used by the
// course-centric Roster READ VIEW (R+ M4).
//
// Separate file so the M4 add doesn't perturb the existing enrollment.go
// (which carries the Phyllis MVP invariants + Comic anchors). ADD-ONLY per
// the R+ /r/roster track brief.
//
// Method choice: `ListByCourse` mirrors `ListByGCID` (which already exists)
// — same tenant-scoped, soft-delete-aware iteration; the access key differs.
// The pg adapter ships its own (RLS-scoped) implementation as part of the
// Stage C / D pg-rollout per the R+ build-out plan.
package delivery

import "context"

// ListByCourse returns every active enrolment for a course inside a tenant.
//
// Soft-delete aware: rows with DeletedAt != nil are skipped. Cross-tenant
// rows are skipped (defence in depth — production RLS enforces the same
// at the SQL boundary). Order is stable but unspecified — callers MUST
// sort if presentation order matters.
//
// Used by inmem.CourseRosterRepo to materialise the course Roster view.
func (r *EnrollmentRegistry) ListByCourse(tenantID, courseID string) []*Enrollment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Enrollment, 0, 4)
	for _, e := range r.byID {
		if e.TenantID != tenantID || e.CourseID != courseID || e.DeletedAt != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ListByCourse satisfies the EnrollmentListByCoursePort surface for the
// InMemEnrollmentStore.
func (a *InMemEnrollmentStore) ListByCourse(_ context.Context, tenantID, courseID string) ([]*Enrollment, error) {
	return a.r.ListByCourse(tenantID, courseID), nil
}

// EnrollmentListByCoursePort is the by-course iterator port. Kept separate
// from the existing 5-method EnrollmentPort so the production pg adapter
// can opt into the new method on its own cutover schedule.
//
// The course-roster READ VIEW (inmem.CourseRosterRepo) type-asserts the
// supplied EnrollmentPort to this surface and gracefully degrades to an
// empty roster when the port doesn't implement it.
type EnrollmentListByCoursePort interface {
	// ListByCourse returns every active enrolment for a course inside a tenant.
	// Same tenant-scoping + soft-delete semantics as ListByGCID.
	ListByCourse(ctx context.Context, tenantID, courseID string) ([]*Enrollment, error)
}
