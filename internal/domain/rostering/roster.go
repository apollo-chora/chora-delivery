// roster.go — course-centric Roster READ VIEW aggregate (R+ M4).
//
// Distinct from the existing class-centric `Roster` aggregate in rostering.go
// (which assigns learners to a Class with capacity / dedupe). Because the
// rostering package already owns the type name `Roster`, this course-scoped
// projection is named `CourseRoster` — same package (rostering owns the
// bounded-context boundary "who is on this thing") but a different
// aggregate type. ADD-ONLY per the M4 directive; the legacy class-centric
// Roster is untouched.
//
// Shape:
//
//	CourseRoster = the set of learners enrolled in a Course
//	               (course_id, tenant_id)
//	             + per-learner progress proxy (atomic-session-pct or 0
//	               if absent)
//	             + enrolled-at timestamp.
//
// Source of truth for the membership set is the chora-delivery
// EnrollmentPort (chora_delivery.course_enrollments). Display name + progress
// proxy come from upstream domains via Pub/Sub-projected reads (not yet wired
// — defaults to GCID + 0 per `feedback_no_stubs_real_wiring`).
//
// Hexagonal: this is pure domain. The adapter (adapter/inmem/roster_repo.go)
// is the one that joins EnrollmentPort → CourseRoster shape.
//
// Cross-domain references travel as opaque UUIDs (course_id, gcid) without
// FK constraints, per .claude/rules/ddd-enforcement.md aggregate-invariant #3.
package rostering

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrCourseIDRequired is returned when a CourseRoster is built without a course_id.
var ErrCourseIDRequired = errors.New("course_id required")

// ErrTenantIDRequired is returned when a CourseRoster is built without a tenant_id.
var ErrTenantIDRequired = errors.New("tenant_id required")

// RosterLearner is one row in a course Roster.
//
// `DisplayName` falls back to the GCID when no projection is available
// (per `feedback_no_stubs_real_wiring` — fail visible, not pretend).
// `ProgressPct` is the AtomAttempt proxy in [0, 100]; defaults to 0
// when no progress source is wired.
type RosterLearner struct {
	GCID        string
	DisplayName string
	ProgressPct int
	EnrolledAt  time.Time
}

// CourseRoster is the projected list-of-learners view for one Course.
//
// This is a READ aggregate — not a write target. The `Learners` slice is
// freshly materialised on each `ListByCourse` call by the repository.
type CourseRoster struct {
	CourseID string
	TenantID string
	Learners []RosterLearner
}

// NewCourseRoster constructs a CourseRoster with up-front validation. The
// Learners slice is initialised to a zero-length non-nil slice so JSON
// marshalling yields `[]` not `null` when the course has zero enrolments
// (FE contract: empty learners array, NOT a placeholder).
func NewCourseRoster(tenantID, courseID string) (*CourseRoster, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w", ErrTenantIDRequired)
	}
	if strings.TrimSpace(courseID) == "" {
		return nil, fmt.Errorf("%w", ErrCourseIDRequired)
	}
	return &CourseRoster{
		TenantID: tenantID,
		CourseID: courseID,
		Learners: []RosterLearner{},
	}, nil
}

// LearnerCount returns the count of learners on the roster. Named distinctly
// from the existing `Roster.Size()` method so the two aggregates' method
// sets don't visually collide in IDE jump-to-symbol lists.
func (r *CourseRoster) LearnerCount() int { return len(r.Learners) }

// CourseRosterRepo is the port for the course-roster view. Implementations
// materialise the view from upstream sources — production wires a pg view;
// local dev wires the in-memory adapter that joins EnrollmentPort.
type CourseRosterRepo interface {
	// ListByCourse returns the CourseRoster for (tenantID, courseID). When
	// no enrolments exist, the returned CourseRoster has a zero-length
	// Learners slice (NEVER nil) — empty is a valid state, not a 404.
	//
	// ctx carries the tenant (set by the handler via tracing.WithTenantID)
	// so the production pg-backed adapter can SET LOCAL chora.tenant_id for
	// the RLS-scoped SELECT. Implementations MUST thread ctx to their
	// underlying EnrollmentListByCoursePort — a tenant-less ctx makes the
	// pg adapter fail with ErrNoTenantContext.
	ListByCourse(ctx context.Context, tenantID, courseID string) (*CourseRoster, error)
}
