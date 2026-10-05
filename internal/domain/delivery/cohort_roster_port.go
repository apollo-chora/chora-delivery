// cohort_roster_port.go — the roster seam behind assessment cohort authz
// (CHO-2153 / ADR-234).
//
// An eligibility decision needs two facts the Assessment aggregate cannot hold:
// whether the learner is enrolled in the assessment's OFFERING, and whether
// they are on its CLASS roster. Both live in chora_delivery (course_enrollments
// and bookings) — same database, so this is an intra-domain read, not a
// cross-domain one, and no event is involved.
//
// This port exists because ADR-155 §D9 promised these checks were "delegated
// upstream" and no upstream ever performed them. The gate therefore returned
// true for every learner in the tenant. Naming the seam is what makes the
// omission impossible to repeat: a nil CohortRoster now DENIES (see
// http.resolveCohortFacts) rather than silently admitting everyone.
package delivery

import (
	"context"
	"errors"
)

// ErrCohortRosterUnavailable — an eligibility decision was requested for an
// assessment whose cohort mode needs roster facts, but no roster is wired.
// Returned (never swallowed) so a misconfiguration fails LOUD and CLOSED: the
// request is refused rather than silently admitting the whole tenant, which is
// exactly the failure mode CHO-2153 was.
var ErrCohortRosterUnavailable = errors.New("delivery: cohort roster unavailable — refusing to evaluate eligibility")

// ResolveCohortFacts resolves exactly the roster facts this assessment's cohort
// mode requires, and no more — an EXPLICIT or freestanding-open-link assessment
// performs zero roster IO.
//
// Both the pg-backed HTTP path and the in-memory repo call THIS function, so
// there is one resolution rule rather than two that can drift.
func ResolveCohortFacts(ctx context.Context, roster CohortRoster, a *Assessment, tenantID, learnerGCID string) (LearnerCohortFacts, error) {
	var facts LearnerCohortFacts
	if a == nil {
		return facts, ErrCohortRosterUnavailable
	}
	needEnrolment := a.RequiresOfferingEnrolment()
	needBooking := a.RequiresClassBooking()
	if !needEnrolment && !needBooking {
		return facts, nil // explicit invite list, or freestanding open-link
	}
	if roster == nil {
		return facts, ErrCohortRosterUnavailable
	}
	if needEnrolment {
		ok, err := roster.EnrolledInOffering(ctx, tenantID, a.OfferingID, learnerGCID)
		if err != nil {
			return LearnerCohortFacts{}, err
		}
		facts.EnrolledInOffering = ok
	}
	if needBooking {
		ok, err := roster.BookedOnClass(ctx, tenantID, a.ClassID, learnerGCID)
		if err != nil {
			return LearnerCohortFacts{}, err
		}
		facts.BookedOnClass = ok
	}
	return facts, nil
}

// CohortRoster resolves the roster facts for an assessment's cohort gates.
//
// Implementations MUST fail loud: a lookup that errors returns the error, and
// callers must refuse the request. They must NEVER report `true` on error, and
// must never swallow an error into `false, nil` — a denial and an outage are
// different things, and conflating them hides the outage.
type CohortRoster interface {
	// EnrolledInOffering reports whether the learner holds a live enrolment in
	// at least ONE course of the offering.
	//
	// An offering's course set is `data->'CourseIDs'` (a JSONB array), NOT the
	// `course_id` column alone — offerings are multi-course, and the column
	// carries only the first. Live: offering 019f3c77 spans courses 019eb059 +
	// 019edf1c, so a check joining on `course_id` would lock out every learner
	// enrolled solely in the second.
	EnrolledInOffering(ctx context.Context, tenantID, offeringID, learnerGCID string) (bool, error)

	// BookedOnClass reports whether the learner holds a live booking on the
	// class. Any non-soft-deleted booking counts, in any status
	// (pending / confirmed / attended / no-show): each of them means "was
	// rostered onto this class". A cancelled booking is soft-deleted and so is
	// already excluded.
	BookedOnClass(ctx context.Context, tenantID, classID, learnerGCID string) (bool, error)
}

// InMemCohortRoster is the unit-test / local-dev roster. Empty ⇒ nobody is
// enrolled and nobody is booked, which is the correct default for an authz
// gate: a test that forgets to enrol its learner gets a denial, not a pass.
type InMemCohortRoster struct {
	enrolled map[string]map[string]bool // offeringID → gcid → true
	booked   map[string]map[string]bool // classID    → gcid → true
	// Err, when set, is returned by both lookups — used to prove that a roster
	// outage refuses the request instead of failing open.
	Err error
}

// NewInMemCohortRoster constructs an empty roster.
func NewInMemCohortRoster() *InMemCohortRoster {
	return &InMemCohortRoster{
		enrolled: map[string]map[string]bool{},
		booked:   map[string]map[string]bool{},
	}
}

// Enrol records a live enrolment linking the learner to the offering.
func (r *InMemCohortRoster) Enrol(offeringID, learnerGCID string) {
	if r.enrolled[offeringID] == nil {
		r.enrolled[offeringID] = map[string]bool{}
	}
	r.enrolled[offeringID][learnerGCID] = true
}

// Book records a live booking putting the learner on the class roster.
func (r *InMemCohortRoster) Book(classID, learnerGCID string) {
	if r.booked[classID] == nil {
		r.booked[classID] = map[string]bool{}
	}
	r.booked[classID][learnerGCID] = true
}

// EnrolledInOffering implements CohortRoster.
func (r *InMemCohortRoster) EnrolledInOffering(_ context.Context, _, offeringID, learnerGCID string) (bool, error) {
	if r.Err != nil {
		return false, r.Err
	}
	return r.enrolled[offeringID][learnerGCID], nil
}

// BookedOnClass implements CohortRoster.
func (r *InMemCohortRoster) BookedOnClass(_ context.Context, _, classID, learnerGCID string) (bool, error) {
	if r.Err != nil {
		return false, r.Err
	}
	return r.booked[classID][learnerGCID], nil
}
