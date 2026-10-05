// project_group_test.go — RED-phase tests for the ProjectGroup aggregate (M15b).
//
// TDD: written FIRST then drove the implementation in project_group.go.
// Coverage target: ≥85% domain.
//
// Test matrix:
//
//	NewProjectGroup         — constructor guards + happy path
//	ProjectGroup.AddMember  — additive membership management
//	ProjectGroup.Activate   — FORMING → ACTIVE
//	ProjectGroup.Submit     — ACTIVE → SUBMITTED + submitted_at
//	ProjectGroup.Grade      — SUBMITTED → GRADED + score_pct + grader_gcid
//	ProjectState.IsValid    — enum exhaustiveness
package project_group

import (
	"errors"
	"testing"
)

const (
	pgTestTenantID   = "019e3000-0000-7000-8000-000000000001"
	pgTestCourseID   = "019e3000-0000-7000-8000-000000000002"
	pgTestAuthorGCID = "019e3000-0000-7000-9000-000000000001"
	pgTestLearner1   = "019e3000-0000-7000-9000-000000000002"
	pgTestLearner2   = "019e3000-0000-7000-9000-000000000003"
	pgTestGraderGCID = "019e3000-0000-7000-9000-000000000004"
)

// -----------------------------------------------------------------------------
// NewProjectGroup
// -----------------------------------------------------------------------------

func TestNewProjectGroup_OK_PopulatesFormingShell(t *testing.T) {
	g, err := NewProjectGroup(NewProjectGroupInput{
		TenantID: pgTestTenantID,
		CourseID: pgTestCourseID,
		Name:     "Team Atlas",
		Members: []Member{
			{GCID: pgTestLearner1, Role: RoleLeader},
			{GCID: pgTestLearner2, Role: RoleMember},
		},
	})
	if err != nil {
		t.Fatalf("NewProjectGroup err=%v want nil", err)
	}
	if g.ID == "" {
		t.Errorf("ID empty; want UUIDv7")
	}
	if g.State != StateForming {
		t.Errorf("State=%q want FORMING", g.State)
	}
	if g.Name != "Team Atlas" {
		t.Errorf("Name=%q want trimmed input", g.Name)
	}
	if len(g.Members) != 2 {
		t.Errorf("Members=%d want 2", len(g.Members))
	}
	if g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: created=%v updated=%v", g.CreatedAt, g.UpdatedAt)
	}
	if g.SubmittedAt != nil {
		t.Errorf("SubmittedAt set on FORMING; want nil")
	}
	if g.GradedAt != nil {
		t.Errorf("GradedAt set on FORMING; want nil")
	}
}

func TestNewProjectGroup_NameTrimmed(t *testing.T) {
	g, err := NewProjectGroup(NewProjectGroupInput{
		TenantID: pgTestTenantID,
		CourseID: pgTestCourseID,
		Name:     "   Trim Me   ",
	})
	if err != nil {
		t.Fatalf("NewProjectGroup err=%v", err)
	}
	if g.Name != "Trim Me" {
		t.Errorf("Name=%q want trimmed", g.Name)
	}
}

func TestNewProjectGroup_RejectsEmptyTenantID(t *testing.T) {
	_, err := NewProjectGroup(NewProjectGroupInput{
		TenantID: "  ",
		CourseID: pgTestCourseID,
		Name:     "x",
	})
	if !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("err=%v want ErrTenantRequired", err)
	}
}

func TestNewProjectGroup_RejectsEmptyCourseID(t *testing.T) {
	_, err := NewProjectGroup(NewProjectGroupInput{
		TenantID: pgTestTenantID,
		CourseID: "",
		Name:     "x",
	})
	if !errors.Is(err, ErrCourseIDRequired) {
		t.Fatalf("err=%v want ErrCourseIDRequired", err)
	}
}

func TestNewProjectGroup_RejectsEmptyName(t *testing.T) {
	_, err := NewProjectGroup(NewProjectGroupInput{
		TenantID: pgTestTenantID,
		CourseID: pgTestCourseID,
		Name:     "",
	})
	if !errors.Is(err, ErrNameRequired) {
		t.Fatalf("err=%v want ErrNameRequired", err)
	}
}

// -----------------------------------------------------------------------------
// AddMember
// -----------------------------------------------------------------------------

func TestProjectGroup_AddMember_OK(t *testing.T) {
	g := mustNewPG(t)
	if err := g.AddMember(Member{GCID: pgTestLearner1, Role: RoleLeader}); err != nil {
		t.Fatalf("AddMember err=%v", err)
	}
	if len(g.Members) != 1 || g.Members[0].GCID != pgTestLearner1 {
		t.Errorf("members=%v want one entry", g.Members)
	}
}

func TestProjectGroup_AddMember_DuplicateGCID(t *testing.T) {
	g := mustNewPG(t)
	_ = g.AddMember(Member{GCID: pgTestLearner1, Role: RoleLeader})
	err := g.AddMember(Member{GCID: pgTestLearner1, Role: RoleMember})
	if !errors.Is(err, ErrMemberDuplicate) {
		t.Fatalf("err=%v want ErrMemberDuplicate", err)
	}
}

func TestProjectGroup_AddMember_RejectsEmptyGCID(t *testing.T) {
	g := mustNewPG(t)
	err := g.AddMember(Member{GCID: "", Role: RoleMember})
	if !errors.Is(err, ErrMemberGCIDRequired) {
		t.Fatalf("err=%v want ErrMemberGCIDRequired", err)
	}
}

// -----------------------------------------------------------------------------
// Activate (FORMING → ACTIVE)
// -----------------------------------------------------------------------------

func TestProjectGroup_Activate_OK(t *testing.T) {
	g := mustNewPGWithMembers(t)
	if err := g.Activate(); err != nil {
		t.Fatalf("Activate err=%v", err)
	}
	if g.State != StateActive {
		t.Errorf("State=%q want ACTIVE", g.State)
	}
}

func TestProjectGroup_Activate_RequiresMembers(t *testing.T) {
	g := mustNewPG(t)
	err := g.Activate()
	if !errors.Is(err, ErrMembersRequired) {
		t.Fatalf("err=%v want ErrMembersRequired", err)
	}
}

func TestProjectGroup_Activate_RejectsNonForming(t *testing.T) {
	g := mustNewPGWithMembers(t)
	_ = g.Activate()
	err := g.Activate()
	if !errors.Is(err, ErrNotForming) {
		t.Fatalf("err=%v want ErrNotForming", err)
	}
}

// -----------------------------------------------------------------------------
// Submit (ACTIVE → SUBMITTED)
// -----------------------------------------------------------------------------

func TestProjectGroup_Submit_OK(t *testing.T) {
	g := mustActivatePG(t)
	if err := g.Submit(); err != nil {
		t.Fatalf("Submit err=%v", err)
	}
	if g.State != StateSubmitted {
		t.Errorf("State=%q want SUBMITTED", g.State)
	}
	if g.SubmittedAt == nil {
		t.Errorf("SubmittedAt nil; want set")
	}
}

func TestProjectGroup_Submit_RejectsNonActive(t *testing.T) {
	g := mustNewPGWithMembers(t)
	// state = FORMING
	err := g.Submit()
	if !errors.Is(err, ErrNotActive) {
		t.Fatalf("err=%v want ErrNotActive", err)
	}
}

// -----------------------------------------------------------------------------
// Grade (SUBMITTED → GRADED)
// -----------------------------------------------------------------------------

func TestProjectGroup_Grade_OK(t *testing.T) {
	g := mustSubmitPG(t)
	if err := g.Grade(GradeInput{ScorePct: 85.0, GraderGCID: pgTestGraderGCID, Feedback: "good work"}); err != nil {
		t.Fatalf("Grade err=%v", err)
	}
	if g.State != StateGraded {
		t.Errorf("State=%q want GRADED", g.State)
	}
	if g.ScorePct != 85.0 {
		t.Errorf("ScorePct=%v want 85.0", g.ScorePct)
	}
	if g.GraderGCID != pgTestGraderGCID {
		t.Errorf("GraderGCID=%q want %q", g.GraderGCID, pgTestGraderGCID)
	}
	if g.GradedAt == nil {
		t.Errorf("GradedAt nil; want set")
	}
	if g.Feedback != "good work" {
		t.Errorf("Feedback=%q want set", g.Feedback)
	}
}

func TestProjectGroup_Grade_RejectsNonSubmitted(t *testing.T) {
	g := mustActivatePG(t)
	err := g.Grade(GradeInput{ScorePct: 80, GraderGCID: pgTestGraderGCID})
	if !errors.Is(err, ErrNotSubmitted) {
		t.Fatalf("err=%v want ErrNotSubmitted", err)
	}
}

func TestProjectGroup_Grade_RejectsScoreBelowZero(t *testing.T) {
	g := mustSubmitPG(t)
	err := g.Grade(GradeInput{ScorePct: -1, GraderGCID: pgTestGraderGCID})
	if !errors.Is(err, ErrScoreOutOfRange) {
		t.Fatalf("err=%v want ErrScoreOutOfRange", err)
	}
}

func TestProjectGroup_Grade_RejectsScoreAbove100(t *testing.T) {
	g := mustSubmitPG(t)
	err := g.Grade(GradeInput{ScorePct: 100.5, GraderGCID: pgTestGraderGCID})
	if !errors.Is(err, ErrScoreOutOfRange) {
		t.Fatalf("err=%v want ErrScoreOutOfRange", err)
	}
}

func TestProjectGroup_Grade_RejectsEmptyGraderGCID(t *testing.T) {
	g := mustSubmitPG(t)
	err := g.Grade(GradeInput{ScorePct: 80, GraderGCID: ""})
	if !errors.Is(err, ErrGraderRequired) {
		t.Fatalf("err=%v want ErrGraderRequired", err)
	}
}

// -----------------------------------------------------------------------------
// MembersMutable + RemoveMember (roster editing — CHO-2131)
// -----------------------------------------------------------------------------

func TestProjectGroup_MembersMutable_ByState(t *testing.T) {
	if g := mustNewPGWithMembers(t); !g.MembersMutable() {
		t.Errorf("FORMING MembersMutable=false want true")
	}
	if g := mustActivatePG(t); !g.MembersMutable() {
		t.Errorf("ACTIVE MembersMutable=false want true")
	}
	if g := mustSubmitPG(t); g.MembersMutable() {
		t.Errorf("SUBMITTED MembersMutable=true want false")
	}
	g := mustSubmitPG(t)
	if err := g.Grade(GradeInput{ScorePct: 80, GraderGCID: pgTestGraderGCID}); err != nil {
		t.Fatalf("setup Grade err=%v", err)
	}
	if g.MembersMutable() {
		t.Errorf("GRADED MembersMutable=true want false")
	}
}

func TestProjectGroup_RemoveMember_OK(t *testing.T) {
	g := mustNewPGWithMembers(t) // learner1 (leader) + learner2 (member)
	if err := g.RemoveMember(pgTestLearner1); err != nil {
		t.Fatalf("RemoveMember err=%v", err)
	}
	if len(g.Members) != 1 || g.Members[0].GCID != pgTestLearner2 {
		t.Errorf("members=%v want only learner2", g.Members)
	}
}

func TestProjectGroup_RemoveMember_TrimsAndRejectsEmptyGCID(t *testing.T) {
	g := mustNewPGWithMembers(t)
	if err := g.RemoveMember("   "); !errors.Is(err, ErrMemberGCIDRequired) {
		t.Fatalf("err=%v want ErrMemberGCIDRequired", err)
	}
}

func TestProjectGroup_RemoveMember_NotFound(t *testing.T) {
	g := mustNewPGWithMembers(t)
	if err := g.RemoveMember(pgTestGraderGCID); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("err=%v want ErrMemberNotFound", err)
	}
}

func TestProjectGroup_RemoveMember_RejectsWhenLocked(t *testing.T) {
	g := mustSubmitPG(t) // SUBMITTED — roster frozen
	if err := g.RemoveMember(pgTestLearner1); !errors.Is(err, ErrMembersLocked) {
		t.Fatalf("err=%v want ErrMembersLocked", err)
	}
}

// -----------------------------------------------------------------------------
// ProjectState.IsValid
// -----------------------------------------------------------------------------

func TestProjectState_IsValid(t *testing.T) {
	for _, s := range []ProjectState{StateForming, StateActive, StateSubmitted, StateGraded} {
		if !s.IsValid() {
			t.Errorf("IsValid(%q) = false; want true", s)
		}
	}
	if ProjectState("BOGUS").IsValid() {
		t.Errorf("IsValid(BOGUS) = true; want false")
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func mustNewPG(t *testing.T) *ProjectGroup {
	t.Helper()
	g, err := NewProjectGroup(NewProjectGroupInput{
		TenantID: pgTestTenantID,
		CourseID: pgTestCourseID,
		Name:     "Team",
	})
	if err != nil {
		t.Fatalf("setup NewProjectGroup err=%v", err)
	}
	return g
}

func mustNewPGWithMembers(t *testing.T) *ProjectGroup {
	t.Helper()
	g := mustNewPG(t)
	_ = g.AddMember(Member{GCID: pgTestLearner1, Role: RoleLeader})
	_ = g.AddMember(Member{GCID: pgTestLearner2, Role: RoleMember})
	return g
}

func mustActivatePG(t *testing.T) *ProjectGroup {
	t.Helper()
	g := mustNewPGWithMembers(t)
	if err := g.Activate(); err != nil {
		t.Fatalf("setup Activate err=%v", err)
	}
	return g
}

func mustSubmitPG(t *testing.T) *ProjectGroup {
	t.Helper()
	g := mustActivatePG(t)
	if err := g.Submit(); err != nil {
		t.Fatalf("setup Submit err=%v", err)
	}
	return g
}
