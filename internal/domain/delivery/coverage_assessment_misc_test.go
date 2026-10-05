// coverage_assessment_misc_test.go — statement-coverage top-up for the
// Assessment FSM helpers, cohort-fact resolution, course-prerequisite service
// error paths, and the remaining in-memory repo windows.
package delivery

import (
	"context"
	"errors"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// AssessmentState — validity/editable helpers + internal lifecycle flips
// -----------------------------------------------------------------------------

func TestAssessmentState_IsValid_IsEditable(t *testing.T) {
	for _, s := range []AssessmentState{
		AssessmentStateDraft, AssessmentStateScheduled, AssessmentStateOpen,
		AssessmentStateClosed, AssessmentStateGrading, AssessmentStateGraded,
		AssessmentStateReleased, AssessmentStateArchived,
	} {
		if !s.IsValid() {
			t.Errorf("state %s must be valid", s)
		}
	}
	if AssessmentState("LIMBO").IsValid() {
		t.Fatal("unknown state must not be valid")
	}
	if !AssessmentStateDraft.IsEditable() || !AssessmentStateScheduled.IsEditable() {
		t.Fatal("DRAFT + SCHEDULED must be editable")
	}
	if AssessmentStateOpen.IsEditable() || AssessmentStateReleased.IsEditable() || AssessmentStateArchived.IsEditable() {
		t.Fatal("OPEN/RELEASED/ARCHIVED must not be editable")
	}
}

func TestAssessment_MarkGrading_MarkGraded(t *testing.T) {
	a, _ := NewAssessment(validAssessmentInput())
	// MarkGrading only flips CLOSED → GRADING.
	a.State = AssessmentStateClosed
	a.MarkGrading(time.Now())
	if a.State != AssessmentStateGrading {
		t.Fatalf("CLOSED → GRADING failed: %s", a.State)
	}
	// MarkGrading on a non-CLOSED state is a no-op.
	a.State = AssessmentStateOpen
	a.MarkGrading(time.Now())
	if a.State != AssessmentStateOpen {
		t.Fatalf("OPEN must not grad: %s", a.State)
	}
	// MarkGraded only flips GRADING → GRADED.
	a.State = AssessmentStateGrading
	a.MarkGraded(time.Now())
	if a.State != AssessmentStateGraded {
		t.Fatalf("GRADING → GRADED failed: %s", a.State)
	}
	a.State = AssessmentStateScheduled
	a.MarkGraded(time.Now())
	if a.State != AssessmentStateScheduled {
		t.Fatalf("SCHEDULED must not grade-flip: %s", a.State)
	}
}

func TestAssessment_Publish_InvalidWindow(t *testing.T) {
	// Zero open bound → invalid window.
	a, _ := NewAssessment(validAssessmentInput())
	a.ScheduledOpenAt = time.Time{}
	if err := a.Publish(time.Now()); err != ErrAssessmentInvalidWindow {
		t.Fatalf("zero open: want ErrAssessmentInvalidWindow, got %v", err)
	}
	// close <= open → invalid window.
	b, _ := NewAssessment(validAssessmentInput())
	b.ScheduledOpenAt = time.Now().Add(2 * time.Hour)
	b.ScheduledCloseAt = time.Now().Add(1 * time.Hour)
	if err := b.Publish(time.Now()); err != ErrAssessmentInvalidWindow {
		t.Fatalf("close<=open: want ErrAssessmentInvalidWindow, got %v", err)
	}
}

func TestAssessment_Archive_IdempotentAndDeleted(t *testing.T) {
	a, _ := NewAssessment(validAssessmentInput())
	a.Archive(time.Now())
	stamp := a.UpdatedAt
	a.Archive(time.Now())
	if a.State != AssessmentStateArchived || !a.UpdatedAt.Equal(stamp) {
		t.Fatalf("idempotent archive: %s", a.State)
	}
	// DeletedAt makes an assessment invisible even in a visible state.
	b, _ := NewAssessment(validAssessmentInput())
	b.State = AssessmentStateOpen
	now := time.Now().UTC()
	b.DeletedAt = &now
	if b.IsLearnerVisible("00000000-0000-7000-8000-000000001999", LearnerCohortFacts{}) {
		t.Fatal("soft-deleted assessment must be invisible")
	}
	// A cohort-less open assessment is visible to any member (freestanding).
	c, _ := NewAssessment(validAssessmentInput())
	c.State = AssessmentStateOpen
	if !c.IsLearnerVisible("00000000-0000-7000-8000-000000001999", LearnerCohortFacts{}) {
		t.Fatal("freestanding OPEN assessment must be visible")
	}
}

// -----------------------------------------------------------------------------
// ResolveCohortFacts — the shared roster-fact resolution rule
// -----------------------------------------------------------------------------

func TestResolveCohortFacts_Paths(t *testing.T) {
	ctx := context.Background()
	alice := "00000000-0000-7000-8000-000000001999"
	const offering = "019f3c77-c003-7611-a468-8a8f0e836401"
	const class = "01985e7f-1234-7abc-8def-000000000c10"

	// Nil assessment — refuse.
	if _, err := ResolveCohortFacts(ctx, NewInMemCohortRoster(), nil, "t", alice); err != ErrCohortRosterUnavailable {
		t.Fatalf("nil assessment: want ErrCohortRosterUnavailable, got %v", err)
	}

	// Explicit invite list — zero roster IO regardless of roster wiring.
	invited := func() *Assessment {
		in := validAssessmentInput()
		in.InvitedGCIDs = []string{alice}
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		return a
	}()
	facts, err := ResolveCohortFacts(ctx, nil, invited, "t", alice)
	if err != nil || facts.EnrolledInOffering || facts.BookedOnClass {
		t.Fatalf("explicit must skip roster IO: %+v err=%v", facts, err)
	}

	// Offering-attached with a NIL roster — refuse (fail closed).
	attached := func() *Assessment {
		in := validAssessmentInput()
		in.OfferingID = offering
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		return a
	}()
	if _, err := ResolveCohortFacts(ctx, nil, attached, "t", alice); err != ErrCohortRosterUnavailable {
		t.Fatalf("nil roster: want ErrCohortRosterUnavailable, got %v", err)
	}

	// Roster lookup errors propagate (never fail open).
	roster := NewInMemCohortRoster()
	roster.Err = errors.New("roster boom")
	if _, err := ResolveCohortFacts(ctx, roster, attached, "t", alice); err == nil {
		t.Fatal("enrolment outage must propagate")
	}
	classBound := func() *Assessment {
		in := validAssessmentInput()
		in.ClassID = class
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		return a
	}()
	if _, err := ResolveCohortFacts(ctx, roster, classBound, "t", alice); err == nil {
		t.Fatal("booking outage must propagate")
	}

	// Healthy roster resolves the positive facts.
	roster.Err = nil
	roster.Enrol(offering, alice)
	roster.Book(class, alice)
	offFacts, err := ResolveCohortFacts(ctx, roster, attached, "t", alice)
	if err != nil || !offFacts.EnrolledInOffering {
		t.Fatalf("enrolment fact: %+v err=%v", offFacts, err)
	}
	classFacts, err := ResolveCohortFacts(ctx, roster, classBound, "t", alice)
	if err != nil || !classFacts.BookedOnClass {
		t.Fatalf("booking fact: %+v err=%v", classFacts, err)
	}
	_ = InMemCohortRoster{Err: errors.New("x")}
	if _, err := NewInMemCohortRoster().EnrolledInOffering(ctx, "t", offering, alice); err != nil {
		t.Fatalf("empty roster EnrolledInOffering: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Course prerequisite service — error paths
// -----------------------------------------------------------------------------

type errEdgesPort struct{ err error }

func (e *errEdgesPort) ListForCourse(context.Context, string, string) ([]CoursePrerequisite, error) {
	return nil, e.err
}
func (e *errEdgesPort) ListForTenant(context.Context, string) ([]PrerequisiteEdge, error) {
	return nil, e.err
}
func (e *errEdgesPort) Upsert(context.Context, string, PrerequisiteEdge) error { return e.err }
func (e *errEdgesPort) Remove(context.Context, string, string, string) error   { return e.err }

type errCoursesPort struct{ err error }

func (e *errCoursesPort) Save(context.Context, *Course) error { return e.err }
func (e *errCoursesPort) Get(context.Context, string, string) (*Course, bool, error) {
	return nil, false, e.err
}
func (e *errCoursesPort) ListByState(context.Context, string, CourseState, string, int, int) ([]*Course, error) {
	return nil, e.err
}
func (e *errCoursesPort) ListByStateAndAuthor(context.Context, string, CourseState, string, string, int, int) ([]*Course, error) {
	return nil, e.err
}

func TestCoursePrerequisiteService_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	badEdges := &errEdgesPort{err: boom}
	badCourses := &errCoursesPort{err: boom}

	// UnmetHardGates: empty course id.
	svc := NewCoursePrerequisiteService(&errEdgesPort{}, &errCoursesPort{}, 32)
	if _, err := svc.UnmetHardGates(ctx, "t", "", nil); err != ErrPrerequisiteCourseRequired {
		t.Fatalf("empty course: want ErrPrerequisiteCourseRequired, got %v", err)
	}
	// UnmetHardGates: edge-port outage propagates.
	svcBoom := NewCoursePrerequisiteService(badEdges, badCourses, 32)
	if _, err := svcBoom.UnmetHardGates(ctx, "t", "c1", nil); !errors.Is(err, boom) {
		t.Fatalf("edges outage: want boom, got %v", err)
	}
	// List: empty course id.
	if _, err := svc.List(ctx, "t", ""); err != ErrPrerequisiteCourseRequired {
		t.Fatalf("List empty: want ErrPrerequisiteCourseRequired, got %v", err)
	}
	// Add: missing ids / invalid kind / self edge.
	if _, err := svc.Add(ctx, "t", "", "c2", PrereqKindHardGate); err != ErrPrerequisiteCourseRequired {
		t.Fatalf("Add empty source: want ErrPrerequisiteCourseRequired, got %v", err)
	}
	if _, err := svc.Add(ctx, "t", "c1", "c2", PrereqKind("BOGUS")); err != ErrPrerequisiteKindInvalid {
		t.Fatalf("Add invalid kind: want ErrPrerequisiteKindInvalid, got %v", err)
	}
	if _, err := svc.Add(ctx, "t", "c1", "c1", PrereqKindHardGate); err != ErrPrerequisiteSelfEdge {
		t.Fatalf("Add self: want ErrPrerequisiteSelfEdge, got %v", err)
	}
	// requireCourse: catalogue lookup outage.
	if _, err := svcBoom.Add(ctx, "t", "c1", "c2", PrereqKindHardGate); !errors.Is(err, boom) {
		t.Fatalf("Add catalogue outage: want boom, got %v", err)
	}
	// requireCourse: unknown course.
	ok := &okCoursePort{found: map[string]bool{"c1": true}}
	svc2 := NewCoursePrerequisiteService(&errEdgesPort{}, ok, 32)
	if _, err := svc2.Add(ctx, "t", "c1", "c2", PrereqKindHardGate); err != ErrPrerequisiteUnknownCourse {
		t.Fatalf("Add unknown target: want ErrPrerequisiteUnknownCourse, got %v", err)
	}
	// Remove: empty ids.
	if _, err := svc.Remove(ctx, "t", "", "c2"); err != ErrPrerequisiteCourseRequired {
		t.Fatalf("Remove empty: want ErrPrerequisiteCourseRequired, got %v", err)
	}
	// Remove: edge-port outage propagates.
	svcBoom2 := NewCoursePrerequisiteService(badEdges, &errCoursesPort{}, 32)
	if _, err := svcBoom2.Remove(ctx, "t", "c1", "c2"); !errors.Is(err, boom) {
		t.Fatalf("Remove outage: want boom, got %v", err)
	}
}

type okCoursePort struct{ found map[string]bool }

func (e *okCoursePort) Save(context.Context, *Course) error { return nil }
func (e *okCoursePort) Get(_ context.Context, _, courseID string) (*Course, bool, error) {
	return &Course{ID: courseID}, e.found[courseID], nil
}
func (e *okCoursePort) ListByState(context.Context, string, CourseState, string, int, int) ([]*Course, error) {
	return nil, nil
}
func (e *okCoursePort) ListByStateAndAuthor(context.Context, string, CourseState, string, string, int, int) ([]*Course, error) {
	return nil, nil
}

func TestWouldCreatePrerequisiteCycle_VisitedBranch(t *testing.T) {
	// B→C and C→C (existing edges); adding A→B: the DFS from B walks C twice,
	// hitting the visited-map early-exit without finding A.
	edges := []PrerequisiteEdge{
		{CourseID: "B", PrerequisiteCourseID: "C"},
		{CourseID: "C", PrerequisiteCourseID: "C"},
	}
	if wouldCreatePrerequisiteCycle(edges, "A", "B") {
		t.Fatal("A→B must not be a cycle here")
	}
	// Genuine cycle: A→B reaches A through B→C, C→A.
	edges = []PrerequisiteEdge{
		{CourseID: "B", PrerequisiteCourseID: "C"},
		{CourseID: "C", PrerequisiteCourseID: "A"},
	}
	if !wouldCreatePrerequisiteCycle(edges, "A", "B") {
		t.Fatal("A→B must be a cycle (B reaches A)")
	}
}

func TestInMemCoursePrerequisiteStore_ListForTenant_SortTie(t *testing.T) {
	ctx := context.Background()
	store := NewInMemCoursePrerequisiteStore()
	if err := store.Upsert(ctx, "t", PrerequisiteEdge{CourseID: "b", PrerequisiteCourseID: "x", Kind: PrereqKindAdvisory}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Upsert(ctx, "t", PrerequisiteEdge{CourseID: "a", PrerequisiteCourseID: "z", Kind: PrereqKindHardGate}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Upsert(ctx, "t", PrerequisiteEdge{CourseID: "a", PrerequisiteCourseID: "y", Kind: PrereqKindHardGate}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	edges, err := store.ListForTenant(ctx, "t")
	if err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("edges: want 3, got %d", len(edges))
	}
	want := []string{"a|y", "a|z", "b|x"}
	for i, e := range edges {
		if got := e.CourseID + "|" + e.PrerequisiteCourseID; got != want[i] {
			t.Fatalf("sort[%d]: want %s, got %s", i, want[i], got)
		}
	}
}

// -----------------------------------------------------------------------------
// Catalogue helpers + repo windows
// -----------------------------------------------------------------------------

func TestMatchQ_BlankNeedle(t *testing.T) {
	pc, _ := NewPublicCourse(NewPublicCourseInput{
		TenantID: "t", Title: "Anything", InstructorGCID: "i",
	})
	if !matchQ(pc, "   ") {
		t.Fatal("blank query must match everything")
	}
	if matchQ(pc, "nothing-here") {
		t.Fatal("non-matching query must miss")
	}
}

func TestVisibilityMatch_Default(t *testing.T) {
	pc, _ := NewPublicCourse(NewPublicCourseInput{
		TenantID: "t", Title: "x", InstructorGCID: "i", Public: true,
	})
	if !visibilityMatch(pc, CatalogueQuery{Visibility: VisibilityFilter(99)}) {
		t.Fatal("unrecognised visibility filter must default to allow")
	}
}

func TestCatalogue_ListByInstructor_DefaultPaging(t *testing.T) {
	cat := NewCatalogue()
	for i := 0; i < 3; i++ {
		pc, _ := NewPublicCourse(NewPublicCourseInput{
			TenantID: "t", Title: "x", InstructorGCID: "i",
		})
		cat.Save(pc)
	}
	out, total := cat.ListByInstructor("t", "i", 0, 0) // page<1, per<1 → defaults
	if total != 3 || len(out) != 3 {
		t.Fatalf("default paging: total=%d len=%d", total, len(out))
	}
}

func TestInMemAssessmentRepo_ListByOffering_Truncates(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemAssessmentRepo()
	in := validAssessmentInput()
	for i := 0; i < 3; i++ {
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	got, _, err := repo.ListByOffering(ctx, in.TenantID, in.OfferingID, 2, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("truncation: %d err=%v", len(got), err)
	}
	all, _, _ := repo.ListByOffering(ctx, in.TenantID, in.OfferingID, 0, "")
	if len(all) != 3 {
		t.Fatalf("default pageSize: want 3, got %d", len(all))
	}
	// Unknown offering → empty.
	if empty, _, _ := repo.ListByOffering(ctx, in.TenantID, "nope", 20, ""); len(empty) != 0 {
		t.Fatalf("unknown offering: want empty, got %d", len(empty))
	}
}

func TestInMemSubmissionRepo_ListByAssessment_Window(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	s1, _ := NewSubmission(validSubmissionInput())
	s2, _ := NewSubmission(validSubmissionInput())
	if err := repo.Save(ctx, s1); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := repo.Save(ctx, s2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	all, _, err := repo.ListByAssessment(ctx, s1.TenantID, s1.AssessmentID, 0, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("default pageSize: len=%d err=%v", len(all), err)
	}
	one, _, _ := repo.ListByAssessment(ctx, s1.TenantID, s1.AssessmentID, 1, "")
	if len(one) != 1 {
		t.Fatalf("truncation: want 1, got %d", len(one))
	}
	// Foreign tenant / assessment → empty.
	if empty, _, _ := repo.ListByAssessment(ctx, "other-tenant", s1.AssessmentID, 20, ""); len(empty) != 0 {
		t.Fatalf("tenant isolation: want empty, got %d", len(empty))
	}
}

func TestInMemSubmissionRepo_FindLatest_SkipsDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	s1, _ := NewSubmission(validSubmissionInput())
	s1.AttemptNumber = 1
	s3, _ := NewSubmission(validSubmissionInput())
	s3.AttemptNumber = 3
	now := time.Now().UTC()
	s3.DeletedAt = &now
	for _, s := range []*Submission{s1, s3} {
		if err := repo.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	latest, ok, _ := repo.FindLatestByLearner(ctx, s1.TenantID, s1.AssessmentID, s1.LearnerGCID)
	if !ok || latest.AttemptNumber != 1 {
		t.Fatalf("deleted attempt must be skipped: ok=%v attempt=%d", ok, latest.AttemptNumber)
	}
}

func TestInMemSubmissionRepo_ListReleased_NilGradedAtTiebreaks(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	learner := validSubmissionInput().LearnerGCID
	tenant := validSubmissionInput().TenantID

	mk := func(t *testing.T, id string, gradedAt *time.Time) *Submission {
		t.Helper()
		s, err := NewSubmission(validSubmissionInput())
		if err != nil {
			t.Fatalf("NewSubmission: %v", err)
		}
		s.ID = id
		s.LearnerGCID = learner
		s.State = SubmissionStateReleased
		s.GradedAt = gradedAt
		if err := repo.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return s
	}
	t1 := time.Now().UTC().Add(-1 * time.Hour)
	t2 := time.Now().UTC().Add(-2 * time.Hour)
	latest := mk(t, "id-zzz", &t1)
	older := mk(t, "id-aaa", &t2)
	ungraded1 := mk(t, "id-ccc", nil)
	ungraded2 := mk(t, "id-ddd", nil)
	_ = older

	got, err := repo.ListReleasedByLearner(ctx, tenant, learner, 20, 0)
	if err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	// Newest GradedAt first; nils sort last, ID desc among themselves.
	if len(got) != 4 || got[0].ID != latest.ID {
		t.Fatalf("order[0]: want %s first, got %v", latest.ID, idsStr(got))
	}
	if got[3].ID != ungraded1.ID || got[2].ID != ungraded2.ID {
		t.Fatalf("nil-GradedAt ordering: want [%s %s] last, got %v", ungraded1.ID, ungraded2.ID, idsStr(got))
	}
}

func idsStr(subs []*Submission) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.ID)
	}
	return out
}

func TestNewSubmission_AttemptNumberDefaultsToOne(t *testing.T) {
	in := validSubmissionInput()
	in.AttemptNumber = 0
	s, err := NewSubmission(in)
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if s.AttemptNumber != 1 {
		t.Fatalf("attempt: want 1, got %d", s.AttemptNumber)
	}
	// Zero OpensAt/ClosesAt → TimeLimitSecs untouched.
	in2 := validSubmissionInput()
	in2.OpensAt = time.Time{}
	in2.ClosesAt = time.Time{}
	s2, _ := NewSubmission(in2)
	if s2.TimeLimitSecs != 0 {
		t.Fatalf("time_limit: want 0, got %d", s2.TimeLimitSecs)
	}
}

func TestAmendModelAnswer_RejectedAfterApproval(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	if err := s.ApproveAsIs(time.Now(), "i"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := s.AmendModelAnswer(time.Now(), "i", "oe-1", "new"); err != ErrSubmissionAlreadyApproved {
		t.Fatalf("want ErrSubmissionAlreadyApproved, got %v", err)
	}
}
