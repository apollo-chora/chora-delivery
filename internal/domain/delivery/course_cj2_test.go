// course_cj2_test.go — table-driven tests for the CJ#2 Course state-FSM.
//
// TDD: written FIRST then drove the implementation in course_cj2.go.
// Coverage target: ≥85% domain.
//
// Test matrix:
//
//	NewCJ2Course           — constructor guards + happy path
//	Course.UpdateDraftContent — partial update + state guard
//	Course.Publish         — DRAFT→AWAITING_REVIEW + validation
//	Course.Release         — AWAITING_REVIEW→PUBLISHED + RBAC-adjacent guards
//	Course.Reject          — AWAITING_REVIEW→DRAFT + review_notes
//	Course.VisibleToCaller — RBAC visibility matrix
//
// Per CJ#2 directive row at `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3.
package delivery

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	cjTestTenantID   = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	cjTestAuthorGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50101"
	cjTestAdminGCID  = "019e2f93-d586-71b5-8c3d-e2b0d0d50102"
	cjTestTestSetID  = "019e2f93-d586-71b5-8c3d-e2b0d0d50208"
	cjTestTestSetID2 = "019e2f93-d586-71b5-8c3d-e2b0d0d50209"
)

// -----------------------------------------------------------------------------
// NewCJ2Course
// -----------------------------------------------------------------------------

func TestNewCJ2Course_OK_PopulatesDraftShell(t *testing.T) {
	c, err := NewCJ2Course(NewCJ2CourseInput{
		TenantID:           cjTestTenantID,
		AuthorGCID:         cjTestAuthorGCID,
		Title:              "Algorithmic Thinking 101",
		Description:        "intro",
		LearningObjectives: []string{"LO1"},
		PrerequisiteNotes:  []string{},
		TestSetIDs:         []string{cjTestTestSetID},
	})
	if err != nil {
		t.Fatalf("NewCJ2Course err=%v want nil", err)
	}
	if c.ID == "" {
		t.Errorf("ID empty; want UUIDv7")
	}
	if c.State != CourseStateDraft {
		t.Errorf("State=%q want DRAFT", c.State)
	}
	if c.AuthorGCID != cjTestAuthorGCID {
		t.Errorf("AuthorGCID=%q want %q", c.AuthorGCID, cjTestAuthorGCID)
	}
	if c.InstructorGCID != cjTestAuthorGCID {
		t.Errorf("InstructorGCID legacy mirror=%q want %q", c.InstructorGCID, cjTestAuthorGCID)
	}
	if c.Title != "Algorithmic Thinking 101" {
		t.Errorf("Title=%q want trimmed input", c.Title)
	}
	if len(c.TestSetIDs) != 1 || c.TestSetIDs[0] != cjTestTestSetID {
		t.Errorf("TestSetIDs=%v want one entry", c.TestSetIDs)
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: created=%v updated=%v", c.CreatedAt, c.UpdatedAt)
	}
}

func TestNewCJ2Course_TitleTrimmed(t *testing.T) {
	c, err := NewCJ2Course(NewCJ2CourseInput{
		TenantID:   cjTestTenantID,
		AuthorGCID: cjTestAuthorGCID,
		Title:      "  Trim Me  ",
		TestSetIDs: []string{cjTestTestSetID},
	})
	if err != nil {
		t.Fatalf("NewCJ2Course err=%v", err)
	}
	if c.Title != "Trim Me" {
		t.Errorf("Title=%q want 'Trim Me'", c.Title)
	}
}

func TestNewCJ2Course_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   NewCJ2CourseInput
		want error
	}{
		{
			name: "empty tenant_id",
			in: NewCJ2CourseInput{
				AuthorGCID: cjTestAuthorGCID,
				Title:      "T",
				TestSetIDs: []string{cjTestTestSetID},
			},
			want: ErrAssessmentTenantRequired,
		},
		{
			name: "empty author_gcid",
			in: NewCJ2CourseInput{
				TenantID:   cjTestTenantID,
				Title:      "T",
				TestSetIDs: []string{cjTestTestSetID},
			},
			want: ErrCourseAuthorRequired,
		},
		{
			name: "empty title",
			in: NewCJ2CourseInput{
				TenantID:   cjTestTenantID,
				AuthorGCID: cjTestAuthorGCID,
				Title:      "   ",
				TestSetIDs: []string{cjTestTestSetID},
			},
			want: ErrCourseTitleRequired,
		},
		{
			name: "no test-sets",
			in: NewCJ2CourseInput{
				TenantID:   cjTestTenantID,
				AuthorGCID: cjTestAuthorGCID,
				Title:      "T",
				TestSetIDs: []string{},
			},
			want: ErrCourseTestSetIDsRequired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCJ2Course(tc.in)
			if !errors.Is(err, tc.want) {
				t.Errorf("err=%v want %v", err, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// UpdateDraftContent
// -----------------------------------------------------------------------------

func newDraftForTest(t *testing.T) *Course {
	t.Helper()
	c, err := NewCJ2Course(NewCJ2CourseInput{
		TenantID:           cjTestTenantID,
		AuthorGCID:         cjTestAuthorGCID,
		Title:              "Original",
		LearningObjectives: []string{"LO1"},
		TestSetIDs:         []string{cjTestTestSetID},
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return c
}

func TestCourse_UpdateDraftContent_OK_AllFields(t *testing.T) {
	c := newDraftForTest(t)
	origUpdated := c.UpdatedAt
	time.Sleep(2 * time.Millisecond)

	newTitle := "Updated Title"
	newDesc := "Updated description"
	err := c.UpdateDraftContent(UpdateCourseInput{
		Title:              &newTitle,
		Description:        &newDesc,
		LearningObjectives: []string{"LO1", "LO2"},
		PrerequisiteNotes:  []string{"P1"},
		TestSetIDs:         []string{cjTestTestSetID, cjTestTestSetID2},
	})
	if err != nil {
		t.Fatalf("Update err=%v", err)
	}
	if c.Title != "Updated Title" {
		t.Errorf("Title=%q want 'Updated Title'", c.Title)
	}
	if c.Description != "Updated description" {
		t.Errorf("Description=%q", c.Description)
	}
	if len(c.LearningObjectives) != 2 {
		t.Errorf("len(LO)=%d want 2", len(c.LearningObjectives))
	}
	if len(c.TestSetIDs) != 2 {
		t.Errorf("len(TestSetIDs)=%d want 2", len(c.TestSetIDs))
	}
	if !c.UpdatedAt.After(origUpdated) {
		t.Errorf("UpdatedAt did not advance: orig=%v new=%v", origUpdated, c.UpdatedAt)
	}
}

func TestCourse_UpdateDraftContent_FailsWhenNotDraft(t *testing.T) {
	c := newDraftForTest(t)
	if err := c.Publish(); err != nil {
		t.Fatalf("setup Publish: %v", err)
	}
	newTitle := "Should Fail"
	err := c.UpdateDraftContent(UpdateCourseInput{Title: &newTitle})
	if !errors.Is(err, ErrCourseNotDraft) {
		t.Errorf("err=%v want ErrCourseNotDraft", err)
	}
}

func TestCourse_UpdateDraftContent_RejectsEmptyTitle(t *testing.T) {
	c := newDraftForTest(t)
	empty := "   "
	err := c.UpdateDraftContent(UpdateCourseInput{Title: &empty})
	if !errors.Is(err, ErrCourseTitleRequired) {
		t.Errorf("err=%v want ErrCourseTitleRequired", err)
	}
}

// -----------------------------------------------------------------------------
// Publish
// -----------------------------------------------------------------------------

func TestCourse_Publish_DraftToAwaitingReview(t *testing.T) {
	c := newDraftForTest(t)
	if err := c.Publish(); err != nil {
		t.Fatalf("Publish err=%v", err)
	}
	if c.State != CourseStateAwaitingReview {
		t.Errorf("State=%q want AWAITING_REVIEW", c.State)
	}
}

func TestCourse_Publish_FailsIfNotDraft(t *testing.T) {
	c := newDraftForTest(t)
	_ = c.Publish() // → AWAITING_REVIEW
	err := c.Publish()
	if !errors.Is(err, ErrCourseNotDraft) {
		t.Errorf("err=%v want ErrCourseNotDraft", err)
	}
}

func TestCourse_Publish_FailsWithoutTestSets(t *testing.T) {
	c := newDraftForTest(t)
	c.TestSetIDs = nil
	if err := c.Publish(); !errors.Is(err, ErrCourseTestSetIDsRequired) {
		t.Errorf("err=%v want ErrCourseTestSetIDsRequired", err)
	}
}

func TestCourse_Publish_FailsWithoutLearningObjectives(t *testing.T) {
	c := newDraftForTest(t)
	c.LearningObjectives = nil
	if err := c.Publish(); !errors.Is(err, ErrCourseLearningObjectivesRequired) {
		t.Errorf("err=%v want ErrCourseLearningObjectivesRequired", err)
	}
}

func TestCourse_Publish_FailsWithoutTitle(t *testing.T) {
	c := newDraftForTest(t)
	c.Title = "   "
	if err := c.Publish(); !errors.Is(err, ErrCourseTitleRequired) {
		t.Errorf("err=%v want ErrCourseTitleRequired", err)
	}
}

// -----------------------------------------------------------------------------
// Release
// -----------------------------------------------------------------------------

func newAwaitingReviewForTest(t *testing.T) *Course {
	t.Helper()
	c := newDraftForTest(t)
	if err := c.Publish(); err != nil {
		t.Fatalf("setup Publish: %v", err)
	}
	return c
}

func TestCourse_Release_AwaitingReviewToPublished(t *testing.T) {
	c := newAwaitingReviewForTest(t)
	scheduledAt := time.Now().UTC().Add(24 * time.Hour)
	err := c.Release(ReleaseInput{
		PriceSGDCents:   99900,
		SFEligible:      true,
		InstructorGCIDs: []string{cjTestAuthorGCID, cjTestAdminGCID},
		ScheduledOpenAt: &scheduledAt,
	})
	if err != nil {
		t.Fatalf("Release err=%v", err)
	}
	if c.State != CourseStatePublished {
		t.Errorf("State=%q want PUBLISHED", c.State)
	}
	if c.PriceSGDCents != 99900 {
		t.Errorf("PriceSGDCents=%d want 99900", c.PriceSGDCents)
	}
	if !c.SFEligible {
		t.Errorf("SFEligible=false want true")
	}
	if len(c.InstructorGCIDs) != 2 {
		t.Errorf("InstructorGCIDs len=%d want 2", len(c.InstructorGCIDs))
	}
	if c.InstructorGCID != cjTestAuthorGCID {
		t.Errorf("legacy InstructorGCID=%q want %q", c.InstructorGCID, cjTestAuthorGCID)
	}
	if c.PublishedAt == nil {
		t.Errorf("PublishedAt=nil want non-nil")
	}
	if c.ScheduledOpenAt == nil {
		t.Errorf("ScheduledOpenAt=nil want non-nil")
	}
}

func TestCourse_Release_FailsWhenNotAwaitingReview(t *testing.T) {
	c := newDraftForTest(t) // DRAFT
	err := c.Release(ReleaseInput{
		PriceSGDCents:   0,
		InstructorGCIDs: []string{cjTestAuthorGCID},
	})
	if !errors.Is(err, ErrCourseNotAwaitingReview) {
		t.Errorf("err=%v want ErrCourseNotAwaitingReview", err)
	}
}

func TestCourse_Release_FailsOnNegativePrice(t *testing.T) {
	c := newAwaitingReviewForTest(t)
	err := c.Release(ReleaseInput{
		PriceSGDCents:   -1,
		InstructorGCIDs: []string{cjTestAuthorGCID},
	})
	if !errors.Is(err, ErrCoursePriceNegative) {
		t.Errorf("err=%v want ErrCoursePriceNegative", err)
	}
}

func TestCourse_Release_FailsOnEmptyInstructorRoster(t *testing.T) {
	c := newAwaitingReviewForTest(t)
	err := c.Release(ReleaseInput{
		PriceSGDCents:   100,
		InstructorGCIDs: nil,
	})
	if !errors.Is(err, ErrCourseInstructorRosterRequired) {
		t.Errorf("err=%v want ErrCourseInstructorRosterRequired", err)
	}
}

// -----------------------------------------------------------------------------
// Reject
// -----------------------------------------------------------------------------

func TestCourse_Reject_AwaitingReviewBackToDraft(t *testing.T) {
	c := newAwaitingReviewForTest(t)
	err := c.Reject("Please add more practice exercises.")
	if err != nil {
		t.Fatalf("Reject err=%v", err)
	}
	if c.State != CourseStateDraft {
		t.Errorf("State=%q want DRAFT", c.State)
	}
	if c.ReviewNotes != "Please add more practice exercises." {
		t.Errorf("ReviewNotes=%q want set", c.ReviewNotes)
	}
}

func TestCourse_Reject_FailsWhenNotAwaitingReview(t *testing.T) {
	c := newDraftForTest(t)
	err := c.Reject("notes")
	if !errors.Is(err, ErrCourseNotAwaitingReview) {
		t.Errorf("err=%v want ErrCourseNotAwaitingReview", err)
	}
}

func TestCourse_Reject_FailsOnEmptyNotes(t *testing.T) {
	c := newAwaitingReviewForTest(t)
	err := c.Reject("   ")
	if !errors.Is(err, ErrCourseReviewNotesRequired) {
		t.Errorf("err=%v want ErrCourseReviewNotesRequired", err)
	}
}

// -----------------------------------------------------------------------------
// VisibleToCaller
// -----------------------------------------------------------------------------

func TestCourse_VisibleToCaller_PublishedToEveryone(t *testing.T) {
	c := newDraftForTest(t)
	_ = c.Publish()
	_ = c.Release(ReleaseInput{
		PriceSGDCents:   0,
		InstructorGCIDs: []string{cjTestAuthorGCID},
	})

	// Random learner with no roles.
	if !c.VisibleToCaller("019e2f93-d586-71b5-8c3d-e2b0d0d50999", map[string]bool{}) {
		t.Errorf("PUBLISHED course should be visible to any in-tenant caller")
	}
}

func TestCourse_VisibleToCaller_DraftAuthorOnly(t *testing.T) {
	c := newDraftForTest(t)
	// Author sees it.
	if !c.VisibleToCaller(cjTestAuthorGCID, map[string]bool{}) {
		t.Errorf("author should see DRAFT")
	}
	// Random learner doesn't.
	if c.VisibleToCaller("019e2f93-d586-71b5-8c3d-e2b0d0d50999", map[string]bool{}) {
		t.Errorf("random learner should NOT see DRAFT")
	}
	// Training-admin sees it.
	if !c.VisibleToCaller("019e2f93-d586-71b5-8c3d-e2b0d0d50888", map[string]bool{"training-admin": true}) {
		t.Errorf("training-admin should see DRAFT")
	}
}

func TestCourse_VisibleToCaller_AwaitingReviewAdminOrAuthor(t *testing.T) {
	c := newAwaitingReviewForTest(t)
	if !c.VisibleToCaller(cjTestAuthorGCID, map[string]bool{}) {
		t.Errorf("author should see AWAITING_REVIEW")
	}
	if !c.VisibleToCaller("any-gcid", map[string]bool{"training-admin": true}) {
		t.Errorf("training-admin should see AWAITING_REVIEW")
	}
	if c.VisibleToCaller("019e2f93-d586-71b5-8c3d-e2b0d0d50999", map[string]bool{"instructor": true}) {
		t.Errorf("regular instructor (not author) should NOT see other AWAITING_REVIEW")
	}
}

// TestCourse_VisibleToCaller_CanonicalStaffTokens pins the staff vocabulary to
// the CANONICAL Identity tokens the gateway mint stamps. mint_handler.go emits
// training_admin (UNDERSCORE) — never the legacy hyphen — so a reviewer's
// x-mesh-user-roles is "instructor,training_admin". Before the fix
// VisibleToCaller matched only the hyphen "training-admin"/"admin", so a
// training_admin reviewer 404'd on every non-own non-published course the R+
// catalog listed (CHO-2256 follow-up projection mismatch). The gate must match
// the state-aware RLS policy (mig 0043/0044: admin|training-admin|training_admin|
// tenant_admin) and hasTrainingAdminRole — same class as the CHO-2233 dead-branch
// defect on hasInstructorOrAdmin. The bare-instructor case is the negative
// control: RLS excludes a bare instructor from staff, so the fix must NOT
// over-broaden past it.
func TestCourse_VisibleToCaller_CanonicalStaffTokens(t *testing.T) {
	const nonAuthor = "019e2f93-d586-71b5-8c3d-e2b0d0d50999"
	awaiting := newAwaitingReviewForTest(t)
	draft := newDraftForTest(t)

	cases := []struct {
		name  string
		c     *Course
		roles map[string]bool
		want  bool
	}{
		{"awaiting / training_admin (underscore, canonical mint)", awaiting, map[string]bool{"training_admin": true}, true},
		{"awaiting / tenant_admin", awaiting, map[string]bool{"tenant_admin": true}, true},
		{"awaiting / instructor+training_admin (exact live mint shape)", awaiting, map[string]bool{"instructor": true, "training_admin": true}, true},
		{"draft / training_admin (underscore)", draft, map[string]bool{"training_admin": true}, true},
		{"awaiting / training-admin (legacy hyphen, no regression)", awaiting, map[string]bool{"training-admin": true}, true},
		{"awaiting / bare instructor, not author (negative control)", awaiting, map[string]bool{"instructor": true}, false},
	}
	for _, tc := range cases {
		if got := tc.c.VisibleToCaller(nonAuthor, tc.roles); got != tc.want {
			t.Errorf("%s: VisibleToCaller=%v want %v", tc.name, got, tc.want)
		}
	}
}

// -----------------------------------------------------------------------------
// CourseState helpers
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// InMemCourseCJ2Store.ListByStateAndAuthor — ONBOARD-UI F1 author scoping
// -----------------------------------------------------------------------------

func seedInMemDraft(t *testing.T, s *InMemCourseCJ2Store, author, title string) *Course {
	t.Helper()
	c, err := NewCJ2Course(NewCJ2CourseInput{
		TenantID:           cjTestTenantID,
		AuthorGCID:         author,
		Title:              title,
		LearningObjectives: []string{"LO1"},
		TestSetIDs:         []string{cjTestTestSetID},
	})
	if err != nil {
		t.Fatalf("NewCJ2Course: %v", err)
	}
	if err := s.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return c
}

func TestInMemCJ2_ListByStateAndAuthor_ScopesToAuthor(t *testing.T) {
	s := NewInMemCourseCJ2Store()
	const authorB = "019e2f93-d586-71b5-8c3d-e2b0d0d50104"
	a := seedInMemDraft(t, s, cjTestAuthorGCID, "A draft")
	_ = seedInMemDraft(t, s, authorB, "B draft")

	got, err := s.ListByStateAndAuthor(context.Background(), cjTestTenantID, CourseStateDraft, cjTestAuthorGCID, "", 0, 20)
	if err != nil {
		t.Fatalf("ListByStateAndAuthor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected only author A's 1 draft, got %d", len(got))
	}
	if got[0].ID != a.ID {
		t.Errorf("returned course id=%s want %s", got[0].ID, a.ID)
	}
}

func TestInMemCJ2_ListByStateAndAuthor_EmptyAuthorOrTenant(t *testing.T) {
	s := NewInMemCourseCJ2Store()
	_ = seedInMemDraft(t, s, cjTestAuthorGCID, "A draft")
	if got, _ := s.ListByStateAndAuthor(context.Background(), cjTestTenantID, CourseStateDraft, "", "", 0, 20); got != nil {
		t.Errorf("empty author must return nil; got %v", got)
	}
	if got, _ := s.ListByStateAndAuthor(context.Background(), "", CourseStateDraft, cjTestAuthorGCID, "", 0, 20); got != nil {
		t.Errorf("empty tenant must return nil; got %v", got)
	}
}

func TestInMemCJ2_ListByStateAndAuthor_ExcludesSoftDeletedAndPaginates(t *testing.T) {
	s := NewInMemCourseCJ2Store()
	live := seedInMemDraft(t, s, cjTestAuthorGCID, "live")
	gone := seedInMemDraft(t, s, cjTestAuthorGCID, "deleted")
	now := time.Now().UTC()
	gone.DeletedAt = &now
	if err := s.Save(context.Background(), gone); err != nil {
		t.Fatalf("Save soft-deleted: %v", err)
	}
	got, err := s.ListByStateAndAuthor(context.Background(), cjTestTenantID, CourseStateDraft, cjTestAuthorGCID, "", 0, 20)
	if err != nil {
		t.Fatalf("ListByStateAndAuthor: %v", err)
	}
	if len(got) != 1 || got[0].ID != live.ID {
		t.Fatalf("soft-deleted course must be excluded; got %d items", len(got))
	}
	// offset past the end → empty.
	if past, _ := s.ListByStateAndAuthor(context.Background(), cjTestTenantID, CourseStateDraft, cjTestAuthorGCID, "", 5, 20); len(past) != 0 {
		t.Errorf("offset past end must be empty; got %d", len(past))
	}
}

// TestInMemCJ2_ListByStateAndAuthor_FiltersByQuery covers the entity-picker
// search: a non-empty query filters to courses whose title contains the term
// (case-insensitive substring), same author + state scope preserved. Backs
// the R+ prerequisite-editor course picker (kills the single-page course
// dropdown) — mirrors chora-creation's atom ?q= pattern (commit 85b2bae14).
func TestInMemCJ2_ListByStateAndAuthor_FiltersByQuery(t *testing.T) {
	s := NewInMemCourseCJ2Store()
	road := seedInMemDraft(t, s, cjTestAuthorGCID, "Road Safety Basics")
	_ = seedInMemDraft(t, s, cjTestAuthorGCID, "Photosynthesis 101")

	got, err := s.ListByStateAndAuthor(context.Background(), cjTestTenantID, CourseStateDraft, cjTestAuthorGCID, "road", 0, 20)
	if err != nil {
		t.Fatalf("ListByStateAndAuthor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("q=road expected exactly 1 course; got %d", len(got))
	}
	if got[0].ID != road.ID {
		t.Errorf("q=road returned wrong course id=%s want %s", got[0].ID, road.ID)
	}
}

func TestCourseState_IsValid(t *testing.T) {
	for _, s := range []CourseState{CourseStateDraft, CourseStateAwaitingReview, CourseStatePublished, CourseStateArchived} {
		if !s.IsValid() {
			t.Errorf("IsValid(%q)=false want true", s)
		}
	}
	if CourseState("INVALID").IsValid() {
		t.Errorf("IsValid(INVALID)=true want false")
	}
}
