// coverage_tiny_test.go — final small top-ups for cheap remaining statements
// (constructor guard paths + paging defaults + sort tiebreaks).
package delivery

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCertificationRegistry_Issue_ValidationError(t *testing.T) {
	reg := NewCertificationRegistry()
	if _, err := reg.Issue("", "", "", nil); err == nil {
		t.Fatal("blank issue args must error")
	}
}

func TestInMemCourseCJ2Store_Save_NilIsNoOp(t *testing.T) {
	store := NewInMemCourseCJ2Store()
	if err := store.Save(context.Background(), nil); err != nil {
		t.Fatalf("nil save: %v", err)
	}
}

func TestInMemCourseCJ2Store_ListByStateAndAuthor_QueryFilter(t *testing.T) {
	ctx := context.Background()
	store := NewInMemCourseCJ2Store()
	c1 := &Course{ID: "c-1", TenantID: "t", Title: "Roadmap to Excellence", State: CourseStateDraft, AuthorGCID: "author", CreatedAt: time.Now().UTC()}
	c2 := &Course{ID: "c-2", TenantID: "t", Title: "Other Course", State: CourseStateDraft, AuthorGCID: "author", CreatedAt: time.Now().UTC()}
	for _, c := range []*Course{c1, c2} {
		if err := store.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	got, err := store.ListByStateAndAuthor(ctx, "t", CourseStateDraft, "author", "road", 0, 20)
	if err != nil {
		t.Fatalf("ListByStateAndAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ID != "c-1" {
		t.Fatalf("query filter: want [c-1], got %+v", idsOfCourses(got))
	}
}

func idsOfCourses(cs []*Course) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestInMemAssessmentRepo_ListVisibleToLearner_Paging(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemAssessmentRepo()
	tenant := validAssessmentInput().TenantID
	learner := "00000000-0000-7000-8000-000000001999"
	const offering = "019f3c77-c003-7611-a468-8a8f0e836401"

	roster := NewInMemCohortRoster()
	roster.Enrol(offering, learner)
	repo.SetRosterLink(roster)

	for i := 0; i < 3; i++ {
		in := validAssessmentInput()
		in.OfferingID = offering
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		a.State = AssessmentStateOpen
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// pageSize <= 0 ⇒ default 20 → all 3.
	got, _, err := repo.ListVisibleToLearner(ctx, tenant, learner, 0, "")
	if err != nil || len(got) != 3 {
		t.Fatalf("default pageSize: len=%d err=%v", len(got), err)
	}
	// pageSize truncates.
	got, _, err = repo.ListVisibleToLearner(ctx, tenant, learner, 1, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("truncation: len=%d err=%v", len(got), err)
	}
}

func TestInMemSubmissionRepo_ListReleased_DefaultsAndTiebreak(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	learner := validSubmissionInput().LearnerGCID
	tenant := validSubmissionInput().TenantID

	// Two RELEASED submissions with the SAME GradedAt — tie breaks by ID desc.
	equal := time.Now().UTC().Add(-1 * time.Hour)
	for _, id := range []string{"id-aaa", "id-zzz"} {
		s, err := NewSubmission(validSubmissionInput())
		if err != nil {
			t.Fatalf("NewSubmission: %v", err)
		}
		s.ID = id
		s.LearnerGCID = learner
		s.State = SubmissionStateReleased
		s.GradedAt = &equal
		if err := repo.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// limit<=0 default 20, offset<0 clamped to 0.
	got, err := repo.ListReleasedByLearner(ctx, tenant, learner, 0, -1)
	if err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	if len(got) != 2 || got[0].ID != "id-zzz" || got[1].ID != "id-aaa" {
		t.Fatalf("tiebreak: want [id-zzz id-aaa], got %v", idsStr(got))
	}
}

func TestTestSet_Publish_NilSnapshotterSkipsFetch(t *testing.T) {
	ts := covTestSet(t)
	if _, err := ts.AddQuestion(AddQuestionInput{QuestionAtomID: "a", QuestionID: "q-9", QuestionType: "mcq", Points: 1}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if err := ts.PublishWithSnapshot(context.Background(), nil, "actor"); err != nil {
		t.Fatalf("nil snapshotter publish: %v", err)
	}
	if ts.State != TestSetStatePublished {
		t.Fatalf("state: want PUBLISHED, got %s", ts.State)
	}
}

func TestTestSet_Questions_CreatedAtTiebreak(t *testing.T) {
	ts := covTestSet(t)
	if _, err := ts.AddQuestion(AddQuestionInput{QuestionAtomID: "a", QuestionID: "a", QuestionType: "mcq", Points: 1, DisplayOrder: 3}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if _, err := ts.AddQuestion(AddQuestionInput{QuestionAtomID: "b", QuestionID: "b", QuestionType: "mcq", Points: 1, DisplayOrder: 3}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	// Equal display_order → tie-break by CreatedAt ascending.
	ts.questions[0].CreatedAt = time.Unix(2, 0)
	ts.questions[1].CreatedAt = time.Unix(1, 0)
	all := ts.Questions()
	if len(all) != 2 || all[0].ID != ts.questions[1].ID {
		t.Fatalf("created-at tiebreak: got %v", idsOf(all))
	}
}

func TestCoursePrerequisiteService_Add_ListForTenantError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	failingEdges := &failListPort{err: boom}
	ok := &okCoursePort{found: map[string]bool{"c1": true, "c2": true}}
	svc := NewCoursePrerequisiteService(failingEdges, ok, 32)
	if _, err := svc.Add(ctx, "t", "c1", "c2", PrereqKindHardGate); !errors.Is(err, boom) {
		t.Fatalf("ListForTenant outage: want boom, got %v", err)
	}
}

type failListPort struct {
	err error
}

func (e *failListPort) ListForCourse(context.Context, string, string) ([]CoursePrerequisite, error) {
	return nil, e.err
}
func (e *failListPort) ListForTenant(context.Context, string) ([]PrerequisiteEdge, error) {
	return nil, e.err
}
func (e *failListPort) Upsert(context.Context, string, PrerequisiteEdge) error { return e.err }
func (e *failListPort) Remove(context.Context, string, string, string) error   { return e.err }
