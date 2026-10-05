// Application state-machine tests — S6 PREP only.
//
// Per the S4.3 brief (A-Content-Delivery): the Course Application aggregate
// is REQUIRED at S6 (course application UX backend with Stripe + Singpass).
// This stage stubs the state machine ONLY — Stripe / Singpass / persistence
// are S6's job.
//
// State machine (per docs/design/ux_course_application.md §UX revisions):
//
//	Draft → Submitted → UnderReview → OfferMade → Accepted → Paid → Enrolled
//	                                           \→ Withdrawn (at any point post-Submitted)
//	                                           \→ Rejected
//
// Anti-S6 promise: the package owns the lifecycle / state-transition logic
// only; no Stripe SDK import, no Singpass call, no DB. The S6 stage layers
// adapters on top.
package application_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	tenantA   = "01970000-0000-7000-8000-000000000001"
	courseA   = "01970000-0000-7000-8000-000000000099"
	gcidA     = "01970000-0000-7000-9000-000000000001"
	classroom = "01970000-0000-7000-8000-AAAAAAAAAAAA"
)

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func TestNewApplication_AssignsDraftStatus(t *testing.T) {
	t.Parallel()
	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA,
		CourseID: courseA,
		GCID:     gcidA,
	})
	if err != nil {
		t.Fatalf("NewApplication: %v", err)
	}
	if app.Status != application.StatusDraft {
		t.Fatalf("expected status=draft, got %q", app.Status)
	}
	if app.ID == "" {
		t.Fatalf("expected non-empty UUIDv7 id")
	}
	if app.CreatedAt.IsZero() {
		t.Fatalf("created_at must be set")
	}
}

func TestNewApplication_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	_, err := application.NewApplication(application.NewApplicationInput{
		CourseID: courseA,
		GCID:     gcidA,
	})
	if err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}

func TestNewApplication_RejectsEmptyCourse(t *testing.T) {
	t.Parallel()
	_, err := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA,
		GCID:     gcidA,
	})
	if err == nil {
		t.Fatalf("expected error for empty course")
	}
}

func TestNewApplication_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	_, err := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA,
		CourseID: courseA,
	})
	if err == nil {
		t.Fatalf("expected error for empty gcid")
	}
}

// -----------------------------------------------------------------------------
// Happy-path transitions (the canonical 7-state walk)
// -----------------------------------------------------------------------------

func TestStatusTransitions_HappyPath(t *testing.T) {
	t.Parallel()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})

	steps := []struct {
		name string
		next application.Status
	}{
		{"submit", application.StatusSubmitted},
		{"begin review", application.StatusUnderReview},
		{"offer", application.StatusOfferMade},
		{"accept", application.StatusAccepted},
		{"pay", application.StatusPaid},
		{"enrol", application.StatusEnrolled},
	}
	for _, s := range steps {
		if err := app.Transition(s.next); err != nil {
			t.Fatalf("%s: unexpected error: %v", s.name, err)
		}
		if app.Status != s.next {
			t.Fatalf("%s: status not advanced: got %q", s.name, app.Status)
		}
	}
}

// -----------------------------------------------------------------------------
// Withdraw + Reject branches
// -----------------------------------------------------------------------------

func TestStatusTransitions_WithdrawFromAnyPostSubmittedState(t *testing.T) {
	t.Parallel()
	cases := []application.Status{
		application.StatusSubmitted,
		application.StatusUnderReview,
		application.StatusOfferMade,
		application.StatusAccepted,
	}
	for _, from := range cases {
		from := from
		t.Run(string(from), func(t *testing.T) {
			t.Parallel()
			app := buildAppAtStatus(t, from)
			if err := app.Transition(application.StatusWithdrawn); err != nil {
				t.Fatalf("withdraw from %s: %v", from, err)
			}
			if app.Status != application.StatusWithdrawn {
				t.Fatalf("expected withdrawn, got %s", app.Status)
			}
		})
	}
}

func TestStatusTransitions_RejectFromUnderReview(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusUnderReview)
	if err := app.Transition(application.StatusRejected); err != nil {
		t.Fatalf("reject from under-review: %v", err)
	}
	if app.Status != application.StatusRejected {
		t.Fatalf("expected rejected, got %s", app.Status)
	}
}

// -----------------------------------------------------------------------------
// Illegal transitions
// -----------------------------------------------------------------------------

func TestStatusTransitions_IllegalSkipping(t *testing.T) {
	t.Parallel()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	// Draft → Paid (skipping submit/review/offer/accept) is illegal.
	err := app.Transition(application.StatusPaid)
	if err == nil {
		t.Fatalf("expected error skipping draft→paid")
	}
}

func TestStatusTransitions_IllegalReverse(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusOfferMade)
	if err := app.Transition(application.StatusDraft); err == nil {
		t.Fatalf("expected error reversing offer→draft")
	}
}

func TestStatusTransitions_NoExitFromTerminal(t *testing.T) {
	t.Parallel()
	terminals := []application.Status{
		application.StatusEnrolled,
		application.StatusWithdrawn,
		application.StatusRejected,
	}
	for _, term := range terminals {
		term := term
		t.Run(string(term), func(t *testing.T) {
			t.Parallel()
			app := buildAppAtStatus(t, term)
			if err := app.Transition(application.StatusSubmitted); err == nil {
				t.Fatalf("expected error transitioning out of terminal %s", term)
			}
		})
	}
}

func TestStatusTransitions_UnknownStatusRejected(t *testing.T) {
	t.Parallel()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	err := app.Transition("not-a-real-status")
	if err == nil {
		t.Fatalf("expected error for unknown status")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unknown") &&
		!strings.Contains(strings.ToLower(err.Error()), "illegal") {
		t.Fatalf("error wording: got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Helper — walk through the state machine to a target status.
// -----------------------------------------------------------------------------

func buildAppAtStatus(t *testing.T, target application.Status) *application.Application {
	t.Helper()
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	walk := map[application.Status][]application.Status{
		application.StatusDraft:       {},
		application.StatusSubmitted:   {application.StatusSubmitted},
		application.StatusUnderReview: {application.StatusSubmitted, application.StatusUnderReview},
		application.StatusOfferMade:   {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade},
		application.StatusAccepted:    {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade, application.StatusAccepted},
		application.StatusPaid:        {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade, application.StatusAccepted, application.StatusPaid},
		application.StatusEnrolled:    {application.StatusSubmitted, application.StatusUnderReview, application.StatusOfferMade, application.StatusAccepted, application.StatusPaid, application.StatusEnrolled},
		application.StatusWithdrawn:   {application.StatusSubmitted, application.StatusWithdrawn},
		application.StatusRejected:    {application.StatusSubmitted, application.StatusUnderReview, application.StatusRejected},
	}
	for _, step := range walk[target] {
		if err := app.Transition(step); err != nil {
			t.Fatalf("seed walk %s -> %s: %v", app.Status, step, err)
		}
	}
	return app
}
