// Package project_group is the ProjectGroup domain aggregate for the
// chora-delivery service (M15b R+ build-out).
//
// A ProjectGroup is a group of learners collaborating on a project assigned
// within a Course. The aggregate carries:
//   - tenant_id / course_id    : cross-aggregate UUID references (no FK)
//   - name                     : human-readable group label
//   - members                  : []{gcid, role} — leader / member
//   - state                    : FORMING / ACTIVE / SUBMITTED / GRADED
//   - submitted_at             : timestamp when Submit() ran
//   - score_pct + grader_gcid  : populated at Grade()
//   - graded_at + feedback     : populated at Grade()
//
// State machine:
//
//	FORMING    → ACTIVE      via Activate() (requires ≥1 member)
//	ACTIVE     → SUBMITTED   via Submit()   (sets submitted_at)
//	SUBMITTED  → GRADED      via Grade()    (sets score_pct + grader_gcid + graded_at)
//
// All state transitions live on the aggregate and are pure; persistence +
// event emission concerns live in the http handler / repo layer. Per
// ddd-enforcement.md aggregate invariants the soft-delete via deleted_at
// is orthogonal to the FSM (not a state transition).
//
// Per .claude/rules/ddd-enforcement.md HARD RULE — cross-database queries
// FORBIDDEN. Inter-domain references (course_id) are UUIDs without FK
// constraint; validation by the http handler / domain service via Pub/Sub.
package project_group

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProjectState models the ProjectGroup lifecycle FSM.
type ProjectState string

const (
	// StateForming — group exists but is still recruiting members.
	StateForming ProjectState = "FORMING"
	// StateActive — group is working on the project.
	StateActive ProjectState = "ACTIVE"
	// StateSubmitted — group has submitted the project deliverable.
	StateSubmitted ProjectState = "SUBMITTED"
	// StateGraded — instructor has graded the submission.
	StateGraded ProjectState = "GRADED"
)

// IsValid reports whether s is one of the canonical FSM states.
func (s ProjectState) IsValid() bool {
	switch s {
	case StateForming, StateActive, StateSubmitted, StateGraded:
		return true
	}
	return false
}

// MemberRole captures the per-member role inside a ProjectGroup.
type MemberRole string

const (
	// RoleLeader — the group leader (one or more allowed).
	RoleLeader MemberRole = "leader"
	// RoleMember — regular contributing member.
	RoleMember MemberRole = "member"
)

// Member is a single learner + role binding inside a ProjectGroup.
type Member struct {
	GCID string     `json:"gcid"`
	Role MemberRole `json:"role"`
}

// Domain errors.
var (
	// ErrTenantRequired — NewProjectGroup rejects empty tenant_id.
	ErrTenantRequired = errors.New("project_group: tenant_id required")
	// ErrCourseIDRequired — NewProjectGroup rejects empty course_id.
	ErrCourseIDRequired = errors.New("project_group: course_id required")
	// ErrNameRequired — NewProjectGroup rejects empty name.
	ErrNameRequired = errors.New("project_group: name required")
	// ErrMemberGCIDRequired — AddMember rejects empty gcid.
	ErrMemberGCIDRequired = errors.New("project_group: member gcid required")
	// ErrMemberDuplicate — AddMember rejects an already-present gcid.
	ErrMemberDuplicate = errors.New("project_group: member already exists")
	// ErrMembersRequired — Activate requires ≥1 member.
	ErrMembersRequired = errors.New("project_group: at least one member required")
	// ErrNotForming — Activate requires FORMING state.
	ErrNotForming = errors.New("project_group: not in FORMING state")
	// ErrNotActive — Submit requires ACTIVE state.
	ErrNotActive = errors.New("project_group: not in ACTIVE state")
	// ErrNotSubmitted — Grade requires SUBMITTED state.
	ErrNotSubmitted = errors.New("project_group: not in SUBMITTED state")
	// ErrGraderRequired — Grade requires non-empty grader_gcid.
	ErrGraderRequired = errors.New("project_group: grader_gcid required")
	// ErrScoreOutOfRange — Grade requires 0 ≤ score_pct ≤ 100.
	ErrScoreOutOfRange = errors.New("project_group: score_pct must be in [0,100]")
	// ErrMemberNotFound — RemoveMember rejects a gcid not in the group.
	ErrMemberNotFound = errors.New("project_group: member not found")
	// ErrMembersLocked — roster edits (add/remove) are rejected once the group
	// is SUBMITTED or GRADED (the grade attaches to a fixed roster).
	ErrMembersLocked = errors.New("project_group: roster is locked (submitted/graded)")
)

// ProjectGroup is the aggregate root.
type ProjectGroup struct {
	ID          string       `json:"id"`
	TenantID    string       `json:"tenant_id"`
	CourseID    string       `json:"course_id"`
	Name        string       `json:"name"`
	Members     []Member     `json:"members"`
	State       ProjectState `json:"state"`
	SubmittedAt *time.Time   `json:"submitted_at,omitempty"`
	ScorePct    float64      `json:"score_pct,omitempty"`
	GraderGCID  string       `json:"grader_gcid,omitempty"`
	GradedAt    *time.Time   `json:"graded_at,omitempty"`
	Feedback    string       `json:"feedback,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	DeletedAt   *time.Time   `json:"deleted_at,omitempty"`
}

// NewProjectGroupInput captures fields the constructor accepts.
type NewProjectGroupInput struct {
	TenantID string
	CourseID string
	Name     string
	Members  []Member
}

// NewProjectGroup constructs a FORMING-state group shell.
//
// Validation guards:
//   - tenant_id non-empty (ErrTenantRequired)
//   - course_id non-empty (ErrCourseIDRequired)
//   - name trim-non-empty (ErrNameRequired)
//
// Members supplied via input are added one-by-one (duplicates rejected via
// ErrMemberDuplicate). On any AddMember error the constructor returns it
// verbatim so callers can detect the cause.
func NewProjectGroup(in NewProjectGroupInput) (*ProjectGroup, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrTenantRequired
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return nil, ErrCourseIDRequired
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	now := time.Now().UTC()
	g := &ProjectGroup{
		ID:        newUUIDv7(),
		TenantID:  in.TenantID,
		CourseID:  in.CourseID,
		Name:      name,
		Members:   make([]Member, 0, len(in.Members)),
		State:     StateForming,
		CreatedAt: now,
		UpdatedAt: now,
	}
	for _, m := range in.Members {
		if err := g.AddMember(m); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// AddMember adds a member to the group. Rejects duplicates by gcid +
// rejects empty gcid. The role defaults to RoleMember when empty.
func (g *ProjectGroup) AddMember(m Member) error {
	gcid := strings.TrimSpace(m.GCID)
	if gcid == "" {
		return ErrMemberGCIDRequired
	}
	for _, existing := range g.Members {
		if existing.GCID == gcid {
			return ErrMemberDuplicate
		}
	}
	role := m.Role
	if role == "" {
		role = RoleMember
	}
	g.Members = append(g.Members, Member{GCID: gcid, Role: role})
	g.UpdatedAt = time.Now().UTC()
	return nil
}

// MembersMutable reports whether the roster may still be edited (add/remove).
// Membership is mutable only while the group is FORMING or ACTIVE; once it is
// SUBMITTED or GRADED the roster freezes — the grade attaches to a fixed set of
// members, so a later change would silently alter who was assessed.
func (g *ProjectGroup) MembersMutable() bool {
	return g.State == StateForming || g.State == StateActive
}

// RemoveMember drops the member with the given gcid. Guards:
//   - gcid trim-non-empty (ErrMemberGCIDRequired)
//   - roster still mutable (ErrMembersLocked once SUBMITTED/GRADED)
//   - gcid is a current member (ErrMemberNotFound)
func (g *ProjectGroup) RemoveMember(gcid string) error {
	gcid = strings.TrimSpace(gcid)
	if gcid == "" {
		return ErrMemberGCIDRequired
	}
	if !g.MembersMutable() {
		return ErrMembersLocked
	}
	idx := -1
	for i, m := range g.Members {
		if m.GCID == gcid {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrMemberNotFound
	}
	g.Members = append(g.Members[:idx], g.Members[idx+1:]...)
	g.UpdatedAt = time.Now().UTC()
	return nil
}

// Activate transitions FORMING → ACTIVE. Requires at least one member.
func (g *ProjectGroup) Activate() error {
	if g.State != StateForming {
		return ErrNotForming
	}
	if len(g.Members) == 0 {
		return ErrMembersRequired
	}
	g.State = StateActive
	g.UpdatedAt = time.Now().UTC()
	return nil
}

// Submit transitions ACTIVE → SUBMITTED. Sets SubmittedAt = now().
func (g *ProjectGroup) Submit() error {
	if g.State != StateActive {
		return ErrNotActive
	}
	now := time.Now().UTC()
	g.State = StateSubmitted
	g.SubmittedAt = &now
	g.UpdatedAt = now
	return nil
}

// GradeInput captures fields the Grade() transition needs.
type GradeInput struct {
	ScorePct   float64
	GraderGCID string
	Feedback   string
}

// Grade transitions SUBMITTED → GRADED.
//
// Validates:
//   - 0 ≤ score_pct ≤ 100 (ErrScoreOutOfRange)
//   - grader_gcid non-empty (ErrGraderRequired)
//
// Sets ScorePct + GraderGCID + GradedAt + Feedback atomically.
func (g *ProjectGroup) Grade(in GradeInput) error {
	if g.State != StateSubmitted {
		return ErrNotSubmitted
	}
	if in.ScorePct < 0 || in.ScorePct > 100 {
		return ErrScoreOutOfRange
	}
	if strings.TrimSpace(in.GraderGCID) == "" {
		return ErrGraderRequired
	}
	now := time.Now().UTC()
	g.State = StateGraded
	g.ScorePct = in.ScorePct
	g.GraderGCID = in.GraderGCID
	g.GradedAt = &now
	g.Feedback = in.Feedback
	g.UpdatedAt = now
	return nil
}

// SoftDelete marks the group deleted-at = now(). Idempotent.
func (g *ProjectGroup) SoftDelete() {
	if g.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	g.DeletedAt = &now
	g.UpdatedAt = now
}

// -----------------------------------------------------------------------------
// UUIDv7 — minimal local generator (deps-free, mirrors delivery.NewUUIDv7)
// -----------------------------------------------------------------------------

// newUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7.
func newUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		// rand.Read failure is exceedingly rare; degrade rather than panic.
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
