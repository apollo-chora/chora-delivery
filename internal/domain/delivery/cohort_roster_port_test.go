// cohort_roster_port_test.go — CHO-2153 / ADR-234.
//
// ResolveCohortFacts is the seam where an authz gate meets its source of truth,
// so its FAILURE modes matter more than its happy path. Three of them are
// asserted here, because each one, got wrong, reproduces the original bug:
//
//   - roster nil          → refuse (never "everyone is eligible")
//   - roster errors       → refuse, and propagate the error (an outage is not a
//     denial, and must not be silently rendered as one)
//   - lookups not needed  → do NO IO at all (an EXPLICIT assessment must not
//     hit the database to decide something its invite list already answers)
package delivery

import (
	"context"
	"errors"
	"testing"
)

// countingRoster records how many lookups were actually performed, so a test
// can assert that no needless query was issued.
type countingRoster struct {
	enrolled     bool
	booked       bool
	err          error
	enrolCalls   int
	bookingCalls int
}

func (r *countingRoster) EnrolledInOffering(_ context.Context, _, _, _ string) (bool, error) {
	r.enrolCalls++
	if r.err != nil {
		return false, r.err
	}
	return r.enrolled, nil
}

func (r *countingRoster) BookedOnClass(_ context.Context, _, _, _ string) (bool, error) {
	r.bookingCalls++
	if r.err != nil {
		return false, r.err
	}
	return r.booked, nil
}

func offeringAttachedAssessment(t *testing.T) *Assessment {
	t.Helper()
	in := validAssessmentInput()
	in.OfferingID = "019f3c77-c003-7611-a468-8a8f0e836401"
	a, err := NewAssessment(in)
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	return a
}

// A nil roster must REFUSE, not admit. If ResolveCohortFacts quietly returned
// zero-valued facts here, a service booted without the roster wired would treat
// every learner as ineligible — which is safe — but if it returned "eligible"
// it would reproduce CHO-2153 exactly. It must do neither: it must error, so the
// misconfiguration is loud.
func TestResolveCohortFacts_NilRoster_Refuses(t *testing.T) {
	a := offeringAttachedAssessment(t)

	_, err := ResolveCohortFacts(context.Background(), nil, a, "tenant", "gcid")
	if !errors.Is(err, ErrCohortRosterUnavailable) {
		t.Fatalf("a nil roster must return ErrCohortRosterUnavailable, got %v", err)
	}
}

// A roster OUTAGE must propagate. Swallowing the error into `false, nil` would
// render an outage as a denial: every learner locked out of every assessment,
// with nothing in the logs to say why.
func TestResolveCohortFacts_RosterError_Propagates(t *testing.T) {
	a := offeringAttachedAssessment(t)
	boom := errors.New("cloud sql: connection refused")
	roster := &countingRoster{err: boom}

	facts, err := ResolveCohortFacts(context.Background(), roster, a, "tenant", "gcid")
	if !errors.Is(err, boom) {
		t.Fatalf("a roster error must propagate, got %v", err)
	}
	if facts.EnrolledInOffering || facts.BookedOnClass {
		t.Error("an errored lookup must yield NO positive grant")
	}
}

// An assessment whose cohort mode needs no roster facts must perform NO roster
// IO. EXPLICIT is decided entirely by the invite list; a freestanding open-link
// row has no cohort to scope to. Querying anyway is wasted latency on the
// learner's hot list path (85 of the 88 live assessments are EXPLICIT).
func TestResolveCohortFacts_NoLookupsWhenNotNeeded(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		in := validAssessmentInput()
		in.InvitedGCIDs = []string{"00000000-0000-7000-8000-000000001999"}
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		roster := &countingRoster{}
		if _, err := ResolveCohortFacts(context.Background(), roster, a, "tenant", "gcid"); err != nil {
			t.Fatalf("ResolveCohortFacts: %v", err)
		}
		if roster.enrolCalls != 0 || roster.bookingCalls != 0 {
			t.Errorf("EXPLICIT must issue no roster query: enrol=%d booking=%d",
				roster.enrolCalls, roster.bookingCalls)
		}
	})

	t.Run("freestanding open-link", func(t *testing.T) {
		a, err := NewAssessment(validAssessmentInput())
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		roster := &countingRoster{}
		if _, err := ResolveCohortFacts(context.Background(), roster, a, "tenant", "gcid"); err != nil {
			t.Fatalf("ResolveCohortFacts: %v", err)
		}
		if roster.enrolCalls != 0 || roster.bookingCalls != 0 {
			t.Errorf("freestanding OPEN_LINK must issue no roster query: enrol=%d booking=%d",
				roster.enrolCalls, roster.bookingCalls)
		}
	})

	// ...and a nil roster is FINE for those modes — no lookup is needed, so
	// there is nothing to be unavailable. Erroring here would break the 85 live
	// EXPLICIT assessments for no reason.
	t.Run("explicit tolerates a nil roster", func(t *testing.T) {
		in := validAssessmentInput()
		in.InvitedGCIDs = []string{"00000000-0000-7000-8000-000000001999"}
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		if _, err := ResolveCohortFacts(context.Background(), nil, a, "tenant", "gcid"); err != nil {
			t.Errorf("EXPLICIT needs no roster, so a nil roster must not error: %v", err)
		}
	})
}

// The happy paths: exactly the needed lookup runs, and its answer lands on the
// right field.
func TestResolveCohortFacts_ResolvesOnlyWhatIsNeeded(t *testing.T) {
	t.Run("offering-attached open-link resolves enrolment only", func(t *testing.T) {
		a := offeringAttachedAssessment(t)
		roster := &countingRoster{enrolled: true, booked: true}

		facts, err := ResolveCohortFacts(context.Background(), roster, a, "tenant", "gcid")
		if err != nil {
			t.Fatalf("ResolveCohortFacts: %v", err)
		}
		if !facts.EnrolledInOffering {
			t.Error("EnrolledInOffering must carry the roster's answer")
		}
		if facts.BookedOnClass {
			t.Error("BookedOnClass must stay false — it was never asked for")
		}
		if roster.enrolCalls != 1 || roster.bookingCalls != 0 {
			t.Errorf("want exactly 1 enrolment lookup and 0 booking lookups: enrol=%d booking=%d",
				roster.enrolCalls, roster.bookingCalls)
		}
	})

	t.Run("class-bound resolves booking only", func(t *testing.T) {
		in := validAssessmentInput()
		in.ClassID = "01985e7f-1234-7abc-8def-000000000c10"
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		roster := &countingRoster{enrolled: true, booked: true}

		facts, err := ResolveCohortFacts(context.Background(), roster, a, "tenant", "gcid")
		if err != nil {
			t.Fatalf("ResolveCohortFacts: %v", err)
		}
		if !facts.BookedOnClass {
			t.Error("BookedOnClass must carry the roster's answer")
		}
		if facts.EnrolledInOffering {
			t.Error("EnrolledInOffering must stay false — it was never asked for")
		}
		if roster.enrolCalls != 0 || roster.bookingCalls != 1 {
			t.Errorf("want exactly 0 enrolment lookups and 1 booking lookup: enrol=%d booking=%d",
				roster.enrolCalls, roster.bookingCalls)
		}
	})

	t.Run("nil assessment refuses", func(t *testing.T) {
		if _, err := ResolveCohortFacts(context.Background(), &countingRoster{}, nil, "t", "g"); !errors.Is(err, ErrCohortRosterUnavailable) {
			t.Errorf("a nil assessment must refuse, got %v", err)
		}
	})
}

// -----------------------------------------------------------------------------
// InMemCohortRoster — the test double must itself default to CLOSED.
// -----------------------------------------------------------------------------

func TestInMemCohortRoster(t *testing.T) {
	ctx := context.Background()
	const (
		offering = "019f3c77-c003-7611-a468-8a8f0e836401"
		class    = "01985e7f-1234-7abc-8def-000000000c10"
		learner  = "00000000-0000-7000-8000-000000002999"
	)
	r := NewInMemCohortRoster()

	// Empty ⇒ nobody is in. A test double that admitted by default would make
	// every cohort test vacuous.
	if ok, _ := r.EnrolledInOffering(ctx, "t", offering, learner); ok {
		t.Error("an empty roster must not report anyone as enrolled")
	}
	if ok, _ := r.BookedOnClass(ctx, "t", class, learner); ok {
		t.Error("an empty roster must not report anyone as booked")
	}

	r.Enrol(offering, learner)
	r.Book(class, learner)

	if ok, _ := r.EnrolledInOffering(ctx, "t", offering, learner); !ok {
		t.Error("Enrol must make the learner enrolled")
	}
	if ok, _ := r.BookedOnClass(ctx, "t", class, learner); !ok {
		t.Error("Book must put the learner on the class roster")
	}
	// Grants are scoped — enrolling on one offering does not admit another.
	if ok, _ := r.EnrolledInOffering(ctx, "t", "other-offering", learner); ok {
		t.Error("an enrolment must not leak across offerings")
	}

	r.Err = errors.New("boom")
	if _, err := r.EnrolledInOffering(ctx, "t", offering, learner); err == nil {
		t.Error("Err must surface from EnrolledInOffering")
	}
	if _, err := r.BookedOnClass(ctx, "t", class, learner); err == nil {
		t.Error("Err must surface from BookedOnClass")
	}
}
