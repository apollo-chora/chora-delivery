package delivery

import (
	"context"
	"testing"
	"time"
)

func validAssessmentInput() NewAssessmentInput {
	return NewAssessmentInput{
		TenantID:                "11111111-1111-7111-8111-111111111111",
		InstructorGCID:          "00000000-0000-7000-8000-000000001999",
		TestSetID:               "01985e7f-1234-7abc-8def-000000000a01",
		TestSetRevisionSnapshot: 1,
		Title:                   "Phyllis Practice",
		ScheduledOpenAt:         time.Now().Add(1 * time.Hour),
		ScheduledCloseAt:        time.Now().Add(3 * time.Hour),
		MaxAttempts:             1,
		ShuffleQuestions:        true,
		ShuffleMCQOptions:       true,
		TotalPoints:             100,
		QuestionCount:           10,
		GradingConfigSnapshot: GradingConfigSnapshot{
			MCQDispatch:             "DETERMINISTIC",
			OEDispatch:              "LLM_EVALUATOR_AGENT",
			LLMEvaluatorModelTier:   "T1",
			PassingThresholdPercent: 70,
		},
	}
}

func TestNewAssessment_HappyPath(t *testing.T) {
	in := validAssessmentInput()
	a, err := NewAssessment(in)
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	if a.State != AssessmentStateDraft {
		t.Fatalf("state: want DRAFT, got %s", a.State)
	}
	if a.ID == "" {
		t.Fatalf("ID empty")
	}
	if a.TestSetRevisionSnapshot != 1 {
		t.Fatalf("revision snapshot: want 1, got %d", a.TestSetRevisionSnapshot)
	}
	if a.Cohort() != CohortOpenLink {
		t.Fatalf("cohort: want OPEN_LINK (no class, no invited), got %s", a.Cohort())
	}
}

// TestNewAssessment_CarriesOfferingID asserts the optional OfferingID is
// threaded through NewAssessment, trim-normalised, and stays empty when
// omitted (W3.A — making an Offering's assessments addressable). OfferingID
// is nullable like ClassID (no required-guard).
func TestNewAssessment_CarriesOfferingID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{"plain id carried", "01985e7f-1234-7abc-8def-000000000aff", "01985e7f-1234-7abc-8def-000000000aff"},
		{"whitespace trimmed", "  01985e7f-1234-7abc-8def-000000000aff  ", "01985e7f-1234-7abc-8def-000000000aff"},
		{"only-whitespace becomes empty", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validAssessmentInput()
			in.OfferingID = tc.in
			a, err := NewAssessment(in)
			if err != nil {
				t.Fatalf("NewAssessment: %v", err)
			}
			if a.OfferingID != tc.want {
				t.Fatalf("OfferingID: want %q, got %q", tc.want, a.OfferingID)
			}
		})
	}
}

// TestInMemAssessmentRepo_ListByOffering covers the in-mem ListByOffering
// (W3.A): offering isolation, tenant scoping, and the soft-delete filter.
func TestInMemAssessmentRepo_ListByOffering(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemAssessmentRepo()

	const (
		offA     = "01985e7f-1234-7abc-8def-0000000000a1"
		offB     = "01985e7f-1234-7abc-8def-0000000000b2"
		otherTen = "22222222-2222-7222-8222-222222222222"
	)
	mk := func(t *testing.T, tenant, offering string, deleted bool) *Assessment {
		t.Helper()
		in := validAssessmentInput()
		in.TenantID = tenant
		in.OfferingID = offering
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		if deleted {
			now := time.Now().UTC()
			a.DeletedAt = &now
		}
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return a
	}

	want := mk(t, validAssessmentInput().TenantID, offA, false)
	_ = mk(t, validAssessmentInput().TenantID, offB, false) // other offering
	_ = mk(t, otherTen, offA, false)                        // other tenant, same offering
	_ = mk(t, validAssessmentInput().TenantID, offA, true)  // soft-deleted, same offering

	got, next, err := repo.ListByOffering(ctx, validAssessmentInput().TenantID, offA, 20, "")
	if err != nil {
		t.Fatalf("ListByOffering: %v", err)
	}
	if next != "" {
		t.Fatalf("next_page_token: want empty, got %q", next)
	}
	if len(got) != 1 {
		t.Fatalf("items: want 1 (offering A, this tenant, not deleted), got %d", len(got))
	}
	if got[0].ID != want.ID {
		t.Fatalf("item id: want %s, got %s", want.ID, got[0].ID)
	}

	// Unknown offering ⇒ empty.
	empty, _, err := repo.ListByOffering(ctx, validAssessmentInput().TenantID, "no-such-offering", 20, "")
	if err != nil {
		t.Fatalf("ListByOffering(unknown): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("unknown offering: want 0 items, got %d", len(empty))
	}
}

func TestNewAssessment_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *NewAssessmentInput)
		want   error
	}{
		{"no tenant", func(i *NewAssessmentInput) { i.TenantID = "" }, ErrAssessmentTenantRequired},
		{"no instructor", func(i *NewAssessmentInput) { i.InstructorGCID = "" }, ErrAssessmentInstructorRequired},
		{"no test_set_id", func(i *NewAssessmentInput) { i.TestSetID = "" }, ErrAssessmentTestSetIDRequired},
		{"no title", func(i *NewAssessmentInput) { i.Title = "" }, ErrAssessmentTitleRequired},
		{"max attempts 0", func(i *NewAssessmentInput) { i.MaxAttempts = 0 }, ErrAssessmentMaxAttemptsOutOfRange},
		{"max attempts 4", func(i *NewAssessmentInput) { i.MaxAttempts = 4 }, ErrAssessmentMaxAttemptsOutOfRange},
		{
			"close before open",
			func(i *NewAssessmentInput) {
				i.ScheduledOpenAt = time.Now().Add(2 * time.Hour)
				i.ScheduledCloseAt = time.Now().Add(1 * time.Hour)
			},
			ErrAssessmentInvalidWindow,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validAssessmentInput()
			c.mutate(&in)
			_, err := NewAssessment(in)
			if err != c.want {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

// TestAssessment_Cohort_Modes pins the cohort → eligibility rule of ADR-155 §D9
// **as amended by ADR-234** (CHO-2153).
//
// This test previously asserted `class-bound → eligible: true // upstream-
// bookings gates`. That is the shape of a test PINNING A BUG: it encoded the
// broken *mechanism* ("something else will check the roster") as the *intent*,
// so the gate could return true for every learner in the tenant and the suite
// stayed green. No upstream check existed. The cases below are retargeted onto
// the invariant — "an ineligible learner is refused" — rather than deleted, so
// the regression they failed to catch cannot come back.
func TestAssessment_Cohort_Modes(t *testing.T) {
	const (
		alice = "00000000-0000-7000-8000-000000001999"
		bob   = "00000000-0000-7000-8000-000000002999"
		class = "01985e7f-1234-7abc-8def-000000000c10"
		offer = "019f3c77-c003-7611-a468-8a8f0e836401"
	)
	cases := []struct {
		name       string
		classID    string
		offeringID string
		invited    []string
		want       CohortMode
		// eligible maps a learner to their eligibility GIVEN the roster facts.
		eligible map[string]bool
		facts    LearnerCohortFacts
	}{
		{
			// The live hole: offering-attached, no class, no invites. Before
			// ADR-234 this was eligible to ANY tenant member.
			name:       "open-link offering-attached, learner NOT enrolled → refused",
			offeringID: offer,
			want:       CohortOpenLink,
			facts:      LearnerCohortFacts{EnrolledInOffering: false},
			eligible:   map[string]bool{alice: false, bob: false},
		},
		{
			name:       "open-link offering-attached, learner enrolled → eligible",
			offeringID: offer,
			want:       CohortOpenLink,
			facts:      LearnerCohortFacts{EnrolledInOffering: true},
			eligible:   map[string]bool{alice: true, bob: true},
		},
		{
			// No offering, no class, no invites ⇒ no cohort to scope to. Stays
			// tenant-open by construction (ADR-234 documents it as discouraged);
			// zero such rows exist live.
			name:     "open-link freestanding → tenant-open",
			want:     CohortOpenLink,
			eligible: map[string]bool{alice: true, bob: true},
		},
		{
			// Was: `true // upstream-bookings gates`. The bookings check is now
			// performed, not promised.
			name:     "class-bound, NOT on the class roster → refused",
			classID:  class,
			want:     CohortClassBound,
			facts:    LearnerCohortFacts{BookedOnClass: false},
			eligible: map[string]bool{alice: false, bob: false},
		},
		{
			name:     "class-bound, on the class roster → eligible",
			classID:  class,
			want:     CohortClassBound,
			facts:    LearnerCohortFacts{BookedOnClass: true},
			eligible: map[string]bool{alice: true, bob: true},
		},
		{
			// An explicit invite is a deliberate grant and stands on its own —
			// it does not additionally require enrolment. 85 live rows.
			name:     "explicit → invite list only",
			invited:  []string{alice},
			want:     CohortExplicit,
			eligible: map[string]bool{alice: true, bob: false},
		},
		{
			name:     "intersection, invited but NOT on the class roster → refused",
			classID:  class,
			invited:  []string{alice},
			want:     CohortIntersection,
			facts:    LearnerCohortFacts{BookedOnClass: false},
			eligible: map[string]bool{alice: false, bob: false},
		},
		{
			name:     "intersection, invited AND on the class roster → eligible",
			classID:  class,
			invited:  []string{alice},
			want:     CohortIntersection,
			facts:    LearnerCohortFacts{BookedOnClass: true},
			eligible: map[string]bool{alice: true, bob: false},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validAssessmentInput()
			in.ClassID = c.classID
			in.OfferingID = c.offeringID
			in.InvitedGCIDs = c.invited
			a, err := NewAssessment(in)
			if err != nil {
				t.Fatalf("NewAssessment: %v", err)
			}
			if a.Cohort() != c.want {
				t.Fatalf("cohort: want %s, got %s", c.want, a.Cohort())
			}
			for gcid, want := range c.eligible {
				if got := a.IsLearnerEligible(gcid, c.facts); got != want {
					t.Errorf("eligible %s: want %v, got %v", gcid, want, got)
				}
			}
		})
	}
}

// An empty GCID is never eligible, in any mode — including the freestanding
// open-link mode that otherwise admits the whole tenant.
func TestAssessment_IsLearnerEligible_EmptyGCID_AlwaysRefused(t *testing.T) {
	a, err := NewAssessment(validAssessmentInput())
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	if a.IsLearnerEligible("", LearnerCohortFacts{EnrolledInOffering: true, BookedOnClass: true}) {
		t.Error("an empty GCID must never be eligible")
	}
	if a.IsLearnerEligible("   ", LearnerCohortFacts{}) {
		t.Error("a blank GCID must never be eligible")
	}
}

// RequiresOfferingEnrolment / RequiresClassBooking tell the adapter which roster
// lookups to run. Getting these wrong is silent: too few ⇒ the gate decides on
// zero-valued facts (refusing a legitimate learner); too many ⇒ wasted queries.
func TestAssessment_RequiresRosterLookups(t *testing.T) {
	const (
		class = "01985e7f-1234-7abc-8def-000000000c10"
		offer = "019f3c77-c003-7611-a468-8a8f0e836401"
		alice = "00000000-0000-7000-8000-000000001999"
	)
	cases := []struct {
		name                  string
		classID, offeringID   string
		invited               []string
		wantEnrol, wantBooked bool
	}{
		{name: "open-link offering-attached", offeringID: offer, wantEnrol: true},
		{name: "open-link freestanding"},
		{name: "class-bound", classID: class, wantBooked: true},
		{name: "explicit", invited: []string{alice}},
		{name: "intersection", classID: class, invited: []string{alice}, wantBooked: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validAssessmentInput()
			in.ClassID = c.classID
			in.OfferingID = c.offeringID
			in.InvitedGCIDs = c.invited
			a, err := NewAssessment(in)
			if err != nil {
				t.Fatalf("NewAssessment: %v", err)
			}
			if got := a.RequiresOfferingEnrolment(); got != c.wantEnrol {
				t.Errorf("RequiresOfferingEnrolment: want %v, got %v", c.wantEnrol, got)
			}
			if got := a.RequiresClassBooking(); got != c.wantBooked {
				t.Errorf("RequiresClassBooking: want %v, got %v", c.wantBooked, got)
			}
		})
	}
}

func TestAssessment_PublishLifecycle(t *testing.T) {
	a, err := NewAssessment(validAssessmentInput())
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	if err := a.Publish(time.Now()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if a.State != AssessmentStateScheduled {
		t.Fatalf("state after publish: want SCHEDULED, got %s", a.State)
	}
	if a.PublishedAt == nil {
		t.Fatalf("published_at not set")
	}
	// Idempotent re-publish.
	if err := a.Publish(time.Now()); err != nil {
		t.Fatalf("idempotent re-publish: %v", err)
	}
	// Not-draft transition rejected.
	a.State = AssessmentStateOpen
	if err := a.Publish(time.Now()); err != ErrAssessmentNotDraft {
		t.Fatalf("re-publish from OPEN: want ErrAssessmentNotDraft, got %v", err)
	}
}

func TestAssessment_ReleaseResults(t *testing.T) {
	a, _ := NewAssessment(validAssessmentInput())
	a.State = AssessmentStateGraded
	if err := a.ReleaseResults(time.Now(), "well done"); err != nil {
		t.Fatalf("release from GRADED: %v", err)
	}
	if a.State != AssessmentStateReleased {
		t.Fatalf("state: want RELEASED, got %s", a.State)
	}
	if a.ResultsReleasedAt == nil {
		t.Fatalf("results_released_at not set")
	}
	if a.ReleaseAnnouncement != "well done" {
		t.Fatalf("announcement not persisted")
	}
	// Idempotent.
	if err := a.ReleaseResults(time.Now(), "again"); err != nil {
		t.Fatalf("idempotent re-release: %v", err)
	}

	// Release from OPEN (demo simplification — MCQ-only fast path).
	a2, _ := NewAssessment(validAssessmentInput())
	a2.State = AssessmentStateOpen
	if err := a2.ReleaseResults(time.Now(), ""); err != nil {
		t.Fatalf("release from OPEN (demo): %v", err)
	}
	if a2.State != AssessmentStateReleased {
		t.Fatalf("state from OPEN release: want RELEASED, got %s", a2.State)
	}

	// Release from DRAFT rejected.
	a3, _ := NewAssessment(validAssessmentInput())
	if err := a3.ReleaseResults(time.Now(), ""); err != ErrAssessmentNotGraded {
		t.Fatalf("release from DRAFT: want ErrAssessmentNotGraded, got %v", err)
	}
}

func TestAssessment_Archive(t *testing.T) {
	a, _ := NewAssessment(validAssessmentInput())
	a.Archive(time.Now())
	if a.State != AssessmentStateArchived {
		t.Fatalf("state: want ARCHIVED, got %s", a.State)
	}
	if a.DeletedAt == nil {
		t.Fatalf("deleted_at not set")
	}
	if a.ArchivedAt == nil {
		t.Fatalf("archived_at not set")
	}
}

func TestAssessment_ForceClose(t *testing.T) {
	a, _ := NewAssessment(validAssessmentInput())
	if err := a.ForceClose(time.Now()); err != ErrAssessmentNotOpen {
		t.Fatalf("force-close from DRAFT: want ErrAssessmentNotOpen, got %v", err)
	}
	a.State = AssessmentStateOpen
	if err := a.ForceClose(time.Now()); err != nil {
		t.Fatalf("force-close from OPEN: %v", err)
	}
	if a.State != AssessmentStateClosed {
		t.Fatalf("state: want CLOSED, got %s", a.State)
	}
}

func TestAssessment_LearnerVisibility(t *testing.T) {
	var noFacts LearnerCohortFacts
	in := validAssessmentInput()
	in.InvitedGCIDs = []string{"00000000-0000-7000-8000-000000001999"}
	a, _ := NewAssessment(in)
	// DRAFT — not visible
	if a.IsLearnerVisible("00000000-0000-7000-8000-000000001999", noFacts) {
		t.Errorf("DRAFT should be invisible")
	}
	a.State = AssessmentStateScheduled
	if !a.IsLearnerVisible("00000000-0000-7000-8000-000000001999", noFacts) {
		t.Errorf("SCHEDULED + invited should be visible")
	}
	if a.IsLearnerVisible("00000000-0000-7000-8000-000000002999", noFacts) {
		t.Errorf("non-invited learner should not see assessment")
	}
	a.State = AssessmentStateArchived
	if a.IsLearnerVisible("00000000-0000-7000-8000-000000001999", noFacts) {
		t.Errorf("ARCHIVED should be invisible")
	}
}

// Visibility and attemptability are ONE decision. A learner outside the cohort
// must not see the card either — a listed assessment they cannot start is both
// a broken UI and an information leak.
func TestAssessment_LearnerVisibility_TracksCohort(t *testing.T) {
	in := validAssessmentInput()
	in.OfferingID = "019f3c77-c003-7611-a468-8a8f0e836401"
	a, err := NewAssessment(in)
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	a.State = AssessmentStateOpen
	const learner = "00000000-0000-7000-8000-000000002999"

	if a.IsLearnerVisible(learner, LearnerCohortFacts{EnrolledInOffering: false}) {
		t.Error("a learner not enrolled in the offering must not see the assessment")
	}
	if !a.IsLearnerVisible(learner, LearnerCohortFacts{EnrolledInOffering: true}) {
		t.Error("an enrolled learner must see the assessment")
	}
}

// AutoFlipToClosed is the transition that did not exist (CHO-2153 F6): the only
// OPEN → CLOSED path was an instructor's manual ForceClose, so an assessment
// whose window elapsed stayed OPEN — and startable — forever.
func TestAssessment_AutoFlipToClosed(t *testing.T) {
	newOpen := func(t *testing.T, closeAt time.Time) *Assessment {
		t.Helper()
		in := validAssessmentInput()
		in.ScheduledOpenAt = time.Now().Add(-2 * time.Hour)
		in.ScheduledCloseAt = closeAt
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		a.State = AssessmentStateOpen
		return a
	}

	t.Run("past close → CLOSED, stamps ClosedAt", func(t *testing.T) {
		a := newOpen(t, time.Now().Add(-1*time.Hour))
		a.AutoFlipToClosed(time.Now())
		if a.State != AssessmentStateClosed {
			t.Fatalf("state: want CLOSED, got %s", a.State)
		}
		if a.ClosedAt == nil {
			t.Error("ClosedAt must be stamped on the auto-close")
		}
	})

	t.Run("window still open → stays OPEN", func(t *testing.T) {
		a := newOpen(t, time.Now().Add(1*time.Hour))
		a.AutoFlipToClosed(time.Now())
		if a.State != AssessmentStateOpen {
			t.Fatalf("state: want OPEN, got %s", a.State)
		}
	})

	// The trap: `now` is ALWAYS at or after the zero time, so without an
	// explicit IsZero guard a windowless assessment would slam shut on the
	// first read.
	t.Run("zero close time → never auto-closes", func(t *testing.T) {
		a := newOpen(t, time.Now().Add(1*time.Hour))
		a.ScheduledCloseAt = time.Time{}
		a.AutoFlipToClosed(time.Now())
		if a.State != AssessmentStateOpen {
			t.Fatalf("a windowless assessment must not auto-close: got %s", a.State)
		}
	})

	t.Run("only OPEN flips — a RELEASED assessment is not reopened or re-closed", func(t *testing.T) {
		a := newOpen(t, time.Now().Add(-1*time.Hour))
		a.State = AssessmentStateReleased
		a.AutoFlipToClosed(time.Now())
		if a.State != AssessmentStateReleased {
			t.Fatalf("state: want RELEASED untouched, got %s", a.State)
		}
	})
}

// StartWindowError enforces the window at BOTH ends. Before CHO-2153 the START
// handler accepted state SCHEDULED as well as OPEN, so an assessment could be
// sat before its window opened as well as after it closed.
func TestAssessment_StartWindowError(t *testing.T) {
	mk := func(t *testing.T, openAt, closeAt time.Time) *Assessment {
		t.Helper()
		in := validAssessmentInput()
		in.ScheduledOpenAt = openAt
		in.ScheduledCloseAt = closeAt
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		return a
	}
	now := time.Now()

	if err := mk(t, now.Add(1*time.Hour), now.Add(2*time.Hour)).StartWindowError(now); err != ErrAssessmentNotYetOpen {
		t.Errorf("before the window opens: want ErrAssessmentNotYetOpen, got %v", err)
	}
	if err := mk(t, now.Add(-2*time.Hour), now.Add(-1*time.Hour)).StartWindowError(now); err != ErrAssessmentWindowClosed {
		t.Errorf("after the window closes: want ErrAssessmentWindowClosed, got %v", err)
	}
	if err := mk(t, now.Add(-1*time.Hour), now.Add(1*time.Hour)).StartWindowError(now); err != nil {
		t.Errorf("inside the window: want nil, got %v", err)
	}
	// The close bound is exclusive: at exactly scheduled_close_at the window is
	// over. A learner must not be able to start on the closing tick.
	closeAt := now.Add(-1 * time.Nanosecond)
	if err := mk(t, now.Add(-1*time.Hour), closeAt).StartWindowError(closeAt); err != ErrAssessmentWindowClosed {
		t.Errorf("at exactly scheduled_close_at: want ErrAssessmentWindowClosed, got %v", err)
	}
}

func TestAssessment_AutoFlipToOpen(t *testing.T) {
	a, _ := NewAssessment(validAssessmentInput())
	a.State = AssessmentStateScheduled
	a.ScheduledOpenAt = time.Now().Add(-1 * time.Minute) // in the past
	a.AutoFlipToOpen(time.Now())
	if a.State != AssessmentStateOpen {
		t.Fatalf("state: want OPEN after auto-flip, got %s", a.State)
	}

	a2, _ := NewAssessment(validAssessmentInput())
	a2.State = AssessmentStateScheduled
	a2.ScheduledOpenAt = time.Now().Add(1 * time.Hour) // future
	a2.AutoFlipToOpen(time.Now())
	if a2.State != AssessmentStateScheduled {
		t.Fatalf("state: want still SCHEDULED, got %s", a2.State)
	}
}

func TestSanitizeInvitedGCIDs_DropsEmpties(t *testing.T) {
	out := sanitizeInvitedGCIDs([]string{"a", "", "  ", "b", "a"})
	if len(out) != 2 || out[0] != "a" || out[1] != "b" {
		t.Fatalf("want [a b], got %v", out)
	}
}
