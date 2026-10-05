// coverage_test.go — closes the remaining statement gaps in project_group.go:
// SoftDelete (previously 0%), NewProjectGroup's member-dup propagation, and
// AddMember's role-defaulting branch.
package project_group

import (
	"errors"
	"testing"
)

func TestProjectGroup_SoftDelete_MarksAndIdempotent(t *testing.T) {
	g := mustNewPG(t)
	if g.DeletedAt != nil {
		t.Fatalf("fresh group must not be deleted")
	}
	g.SoftDelete()
	if g.DeletedAt == nil {
		t.Fatalf("SoftDelete must stamp deleted_at")
	}
	if !g.UpdatedAt.After(g.CreatedAt) && !g.UpdatedAt.Equal(g.CreatedAt) {
		t.Errorf("UpdatedAt must be bumped")
	}
	// Idempotent: a second call returns without re-stamping.
	first := *g.DeletedAt
	g.SoftDelete()
	if !g.DeletedAt.Equal(first) {
		t.Errorf("SoftDelete must be idempotent")
	}
}

func TestNewProjectGroup_RejectsDuplicateMemberInput(t *testing.T) {
	_, err := NewProjectGroup(NewProjectGroupInput{
		TenantID: pgTestTenantID,
		CourseID: pgTestCourseID,
		Name:     "Team",
		Members: []Member{
			{GCID: pgTestLearner1, Role: RoleLeader},
			{GCID: pgTestLearner1, Role: RoleMember}, // duplicate gcid
		},
	})
	if !errors.Is(err, ErrMemberDuplicate) {
		t.Fatalf("err=%v want ErrMemberDuplicate (constructor must propagate AddMember errors)", err)
	}
}

func TestProjectGroup_AddMember_DefaultsEmptyRoleToMember(t *testing.T) {
	g := mustNewPG(t)
	if err := g.AddMember(Member{GCID: pgTestLearner1}); err != nil {
		t.Fatalf("AddMember err=%v", err)
	}
	if len(g.Members) != 1 || g.Members[0].Role != RoleMember {
		t.Errorf("members=%v want single entry with role=member default", g.Members)
	}
}
