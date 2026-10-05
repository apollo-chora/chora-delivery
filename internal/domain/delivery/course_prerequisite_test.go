// course_prerequisite_test.go — table-driven tests for the Course Prerequisite
// DAG (ADR-226): the structured, cycle-checked course→course edge that sits
// alongside the free-text PrerequisiteNotes.
//
// TDD: written FIRST (RED) then drove course_prerequisite.go.
// Coverage target: ≥85% domain.
//
// Pure-domain matrix (this file):
//
//	PrereqKind.IsValid                — kind enum guard
//	wouldCreatePrerequisiteCycle      — DFS acyclicity over the tenant edge set
//	ValidateNewPrerequisiteEdge       — kind / self / cap / cycle composition
//
// Edge semantics: an edge {CourseID:A, PrerequisiteCourseID:B} reads "A requires
// B" (A depends on B). Adding A→B forms a cycle iff B can already reach A.
package delivery

import (
	"errors"
	"testing"
)

const (
	preTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50a00"
	preTenantOther = "019e2f93-d586-71b5-8c3d-e2b0d0d50b00"
	preCourseA     = "019e2f93-d586-71b5-8c3d-e2b0d0d50a01"
	preCourseB     = "019e2f93-d586-71b5-8c3d-e2b0d0d50a02"
	preCourseC     = "019e2f93-d586-71b5-8c3d-e2b0d0d50a03"
	preCourseD     = "019e2f93-d586-71b5-8c3d-e2b0d0d50a04"
)

// -----------------------------------------------------------------------------
// PrereqKind.IsValid
// -----------------------------------------------------------------------------

func TestPrereqKind_IsValid(t *testing.T) {
	cases := []struct {
		kind PrereqKind
		want bool
	}{
		{PrereqKindHardGate, true},
		{PrereqKindAdvisory, true},
		{PrereqKind("HARD_GATE"), false}, // case-sensitive
		{PrereqKind("blocking"), false},
		{PrereqKind(""), false},
	}
	for _, tc := range cases {
		if got := tc.kind.IsValid(); got != tc.want {
			t.Errorf("PrereqKind(%q).IsValid() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// -----------------------------------------------------------------------------
// wouldCreatePrerequisiteCycle
// -----------------------------------------------------------------------------

func edge(course, prereq string) PrerequisiteEdge {
	return PrerequisiteEdge{CourseID: course, PrerequisiteCourseID: prereq, Kind: PrereqKindHardGate}
}

func TestWouldCreatePrerequisiteCycle(t *testing.T) {
	cases := []struct {
		name           string
		existing       []PrerequisiteEdge
		source, target string
		want           bool
	}{
		{
			name:   "empty graph never cycles",
			source: preCourseA, target: preCourseB,
			want: false,
		},
		{
			name:     "direct back-edge cycles (B requires A, add A requires B)",
			existing: []PrerequisiteEdge{edge(preCourseB, preCourseA)},
			source:   preCourseA, target: preCourseB,
			want: true,
		},
		{
			name: "transitive chain cycles (B->C->D, add D requires B)",
			existing: []PrerequisiteEdge{
				edge(preCourseB, preCourseC),
				edge(preCourseC, preCourseD),
			},
			source: preCourseD, target: preCourseB,
			want: true,
		},
		{
			name:     "sibling edge does not cycle",
			existing: []PrerequisiteEdge{edge(preCourseA, preCourseB)},
			source:   preCourseA, target: preCourseC,
			want: false,
		},
		{
			name: "diamond stays acyclic (A->B, B->C, add A requires C)",
			existing: []PrerequisiteEdge{
				edge(preCourseA, preCourseB),
				edge(preCourseB, preCourseC),
			},
			source: preCourseA, target: preCourseC,
			want: false,
		},
		{
			name: "soft-deleted edges are the caller's filter (only active passed in)",
			existing: []PrerequisiteEdge{
				edge(preCourseB, preCourseC), // A no longer requires B (removed)
			},
			source: preCourseA, target: preCourseB,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wouldCreatePrerequisiteCycle(tc.existing, tc.source, tc.target); got != tc.want {
				t.Errorf("wouldCreatePrerequisiteCycle() = %v, want %v", got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// ValidateNewPrerequisiteEdge
// -----------------------------------------------------------------------------

func TestValidateNewPrerequisiteEdge(t *testing.T) {
	cases := []struct {
		name           string
		source, target string
		kind           PrereqKind
		existing       []PrerequisiteEdge
		maxEdges       int
		wantErr        error
	}{
		{
			name:   "happy path",
			source: preCourseA, target: preCourseB, kind: PrereqKindHardGate,
			maxEdges: 32,
			wantErr:  nil,
		},
		{
			name:   "advisory kind also valid",
			source: preCourseA, target: preCourseB, kind: PrereqKindAdvisory,
			maxEdges: 32,
			wantErr:  nil,
		},
		{
			name:   "empty source rejected",
			source: "", target: preCourseB, kind: PrereqKindHardGate,
			maxEdges: 32,
			wantErr:  ErrPrerequisiteCourseRequired,
		},
		{
			name:   "empty target rejected",
			source: preCourseA, target: "", kind: PrereqKindHardGate,
			maxEdges: 32,
			wantErr:  ErrPrerequisiteCourseRequired,
		},
		{
			name:   "invalid kind rejected",
			source: preCourseA, target: preCourseB, kind: PrereqKind("blocking"),
			maxEdges: 32,
			wantErr:  ErrPrerequisiteKindInvalid,
		},
		{
			name:   "self edge rejected",
			source: preCourseA, target: preCourseA, kind: PrereqKindHardGate,
			maxEdges: 32,
			wantErr:  ErrPrerequisiteSelfEdge,
		},
		{
			name:   "cap exceeded rejected",
			source: preCourseA, target: preCourseD, kind: PrereqKindHardGate,
			existing: []PrerequisiteEdge{
				edge(preCourseA, preCourseB),
				edge(preCourseA, preCourseC),
			},
			maxEdges: 2,
			wantErr:  ErrPrerequisiteCapExceeded,
		},
		{
			name:   "cycle rejected",
			source: preCourseA, target: preCourseB, kind: PrereqKindHardGate,
			existing: []PrerequisiteEdge{edge(preCourseB, preCourseA)},
			maxEdges: 32,
			wantErr:  ErrPrerequisiteCycle,
		},
		{
			name:   "non-positive maxEdges falls back to default (no false cap)",
			source: preCourseA, target: preCourseB, kind: PrereqKindHardGate,
			existing: []PrerequisiteEdge{edge(preCourseA, preCourseC)},
			maxEdges: 0,
			wantErr:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNewPrerequisiteEdge(tc.source, tc.target, tc.kind, tc.existing, tc.maxEdges)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateNewPrerequisiteEdge() err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
