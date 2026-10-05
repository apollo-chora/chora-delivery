// roster_repo.go — in-memory CourseRosterRepo (R+ M4).
//
// The course-roster READ VIEW joins three sources today:
//
//  1. EnrollmentPort      — membership set (canonical, durable in prod);
//     the M4 add `EnrollmentListByCoursePort` is the
//     iterator surface used here.
//  2. display-name        — chora_delivery.user_directory projection (Q3),
//     resolved via directory.UserDirectoryPort.LookupNames;
//     empty/absent → GCID fallback. Optional (nil port →
//     GCID for all, per `feedback_no_stubs_real_wiring`).
//  3. <progress-pct>      - TODO: AtomAttempt proxy via projection from
//     chora-consumption; unwired → 0.
//
// Production swaps this adapter for a pg materialised view; the domain
// interface (`rostering.CourseRosterRepo`) stays stable.
//
// Hexagonal: this adapter depends on EnrollmentListByCoursePort (a domain
// port from the same service) — adapter→domain is allowed; the reverse is
// forbidden per hexagonal rules.
package inmem

import (
	"context"
	"sort"
	"strings"

	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

// Compile-time interface assertion — CourseRosterRepo MUST satisfy the
// domain-side CourseRosterRepo port. A signature drift on either side
// surfaces at build time rather than at first wire-up.
var _ rostering.CourseRosterRepo = (*CourseRosterRepo)(nil)

// CourseRosterRepo is the in-memory adapter that materialises a
// `rostering.CourseRoster` by joining over the chora-delivery
// EnrollmentListByCoursePort. One repo instance is shared with the HTTP
// handler via Deps wiring.
type CourseRosterRepo struct {
	enrollments delivery.EnrollmentListByCoursePort
	// directory resolves GCID → display_name from the chora_delivery.user_directory
	// projection (Q3). Optional: nil → every learner falls back to its GCID
	// (the pre-Q3 behaviour), so the roster route works before the directory
	// adapter is wired.
	directory directory.UserDirectoryPort
}

// NewCourseRosterRepo returns a repo bound to the supplied
// EnrollmentListByCoursePort with NO name projection — display names fall back
// to the GCID. The enrollments argument MUST be non-nil — wiring callers should
// fail loud at boot rather than letting a nil-deref surface mid-request.
func NewCourseRosterRepo(enrollments delivery.EnrollmentListByCoursePort) *CourseRosterRepo {
	return NewCourseRosterRepoWithDirectory(enrollments, nil)
}

// NewCourseRosterRepoWithDirectory is NewCourseRosterRepo plus the Q3 name
// projection: enrolled GCIDs are resolved to display names via the supplied
// directory port. A nil dir degrades to the GCID fallback.
func NewCourseRosterRepoWithDirectory(enrollments delivery.EnrollmentListByCoursePort, dir directory.UserDirectoryPort) *CourseRosterRepo {
	if enrollments == nil {
		panic("inmem.NewCourseRosterRepo: enrollments port required")
	}
	return &CourseRosterRepo{enrollments: enrollments, directory: dir}
}

// ListByCourse satisfies `rostering.CourseRosterRepo`.
//
// Membership: scans ENROLLMENT rows for the (tenantID, courseID) tuple
// and builds the Roster shape. When the EnrollmentPort returns nothing,
// the result is a non-nil `CourseRoster` with a zero-length Learners
// slice — empty is a valid state, NOT a 404 (per the M4 fail-loud rule:
// "if no enrollments → empty learners array, NOT a placeholder").
//
// Display name is the Q3 user_directory projection (GCID fallback when the
// directory has no non-empty name, or when no directory port is wired).
// Progress is still a placeholder:
//   - DisplayName  = user_directory.display_name, else GCID
//   - ProgressPct  = 0 (until chora-consumption projection lands)
//
// The GCID + 0% fallbacks are explicit per `feedback_no_stubs_real_wiring` —
// the FE renders them visibly so a missing projection is observable, not
// hidden behind faked data.
//
// Order: sorted by EnrolledAt ascending (oldest enrolment first), then by
// GCID as a deterministic tiebreaker. Matches the chora-delivery
// pagination convention (UUIDv7-ordered).
func (r *CourseRosterRepo) ListByCourse(ctx context.Context, tenantID, courseID string) (*rostering.CourseRoster, error) {
	roster, err := rostering.NewCourseRoster(tenantID, courseID)
	if err != nil {
		return nil, err
	}
	// Thread the caller's ctx (carrying the tenant via tracing.WithTenantID)
	// straight through to the EnrollmentListByCoursePort — the production pg
	// adapter reads the tenant off ctx for its RLS SET LOCAL. Passing
	// context.Background() here strips the tenant and 500s in prod.
	rows, err := r.enrollments.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		// FAIL LOUD per `feedback_no_stubs_real_wiring`: bubble the
		// EnrollmentPort error up so the HTTP layer can 5xx with the
		// real cause instead of pretending we got zero rows.
		return nil, err
	}
	// Q3 name projection: resolve display names for the enrolled GCIDs from the
	// chora_delivery.user_directory read-model (kept fresh via
	// chora.identity.user.profile_updated.v1). This is a LEFT-JOIN-style stitch
	// — intra-chora_delivery, so it is allowed (cross-DB is forbidden, but this
	// is same-DB). A caller only resolves names for GCIDs already present in
	// their own tenant's RLS-scoped roster rows, so the tenant-agnostic
	// directory never surfaces a name outside the caller's tenant.
	var names map[string]string
	if r.directory != nil && len(rows) > 0 {
		gcids := make([]string, 0, len(rows))
		for _, e := range rows {
			gcids = append(gcids, e.GCID)
		}
		names, err = r.directory.LookupNames(ctx, gcids)
		if err != nil {
			// FAIL LOUD — surface the real cause rather than silently
			// degrading to GCID-only rendering.
			return nil, err
		}
	}
	for _, e := range rows {
		displayName := e.GCID // GCID fallback (empty/absent projection)
		if n, ok := names[e.GCID]; ok && strings.TrimSpace(n) != "" {
			displayName = n
		}
		roster.Learners = append(roster.Learners, rostering.RosterLearner{
			GCID:        e.GCID,
			DisplayName: displayName,
			ProgressPct: 0, // TODO: chora-consumption projection
			EnrolledAt:  e.EnrolledAt,
		})
	}
	sort.SliceStable(roster.Learners, func(i, j int) bool {
		if !roster.Learners[i].EnrolledAt.Equal(roster.Learners[j].EnrolledAt) {
			return roster.Learners[i].EnrolledAt.Before(roster.Learners[j].EnrolledAt)
		}
		return roster.Learners[i].GCID < roster.Learners[j].GCID
	})
	return roster, nil
}
