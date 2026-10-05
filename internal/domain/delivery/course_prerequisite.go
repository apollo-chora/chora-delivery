// course_prerequisite.go — the structured, cycle-checked course→course
// prerequisite edge (ADR-226), a first-class Value Object on the Course
// aggregate's graph view that sits ALONGSIDE the free-text PrerequisiteNotes.
//
// A prerequisite edge {CourseID:A, PrerequisiteCourseID:B} reads "course A
// requires completion of course B" (A depends on B). The edges of a tenant form
// a directed graph over its courses; this file keeps that graph ACYCLIC so the
// catalogue can never present an impossible loop.
//
// Per ADR-226 the model + cycle-safety ship now; ENFORCEMENT (hard_gate
// actually blocking enrol) is a separate, deferred decision — these types are
// authored + validated + displayed, never gated here.
//
// Intra-chora_delivery only: PrerequisiteCourseID is a course-ID reference
// within this DB (no cross-DB FK). Existence of the referenced course is
// validated by the application service against the tenant catalogue, NOT here
// (this file is pure + storage-agnostic).
package delivery

import "errors"

// PrereqKind classifies a prerequisite edge.
type PrereqKind string

const (
	// PrereqKindHardGate — intended to block enrol/start until the
	// prerequisite course is completed. NOT enforced yet (ADR-226 §4).
	PrereqKindHardGate PrereqKind = "hard_gate"
	// PrereqKindAdvisory — shown to the learner, never blocks.
	PrereqKindAdvisory PrereqKind = "advisory"
)

// IsValid reports whether k is one of the canonical prerequisite kinds.
func (k PrereqKind) IsValid() bool {
	switch k {
	case PrereqKindHardGate, PrereqKindAdvisory:
		return true
	}
	return false
}

// CourseMaxPrereqEdgesDefault caps the number of prerequisite edges a single
// course may declare (fail-loud on the 33rd). Overridable via
// COURSE_MAX_PREREQ_EDGES; a non-positive override falls back to this.
const CourseMaxPrereqEdgesDefault = 32

// CoursePrerequisite is a single prerequisite of a course (the aggregate-facing
// VO: the owning course is implicit in the query that returns it).
type CoursePrerequisite struct {
	PrerequisiteCourseID string
	Kind                 PrereqKind
}

// PrerequisiteEdge is the full (course requires prerequisite) triple — the
// graph + persistence form, carrying the source CourseID explicitly.
type PrerequisiteEdge struct {
	CourseID             string
	PrerequisiteCourseID string
	Kind                 PrereqKind
}

// Course Prerequisite domain errors (all map to HTTP 422 at the edge — a
// well-formed request the catalogue refuses on graph-integrity grounds).
var (
	// ErrPrerequisiteCourseRequired — source/target course IDs both required.
	ErrPrerequisiteCourseRequired = errors.New("delivery: course_id and prerequisite_course_id required")
	// ErrPrerequisiteKindInvalid — kind must be hard_gate or advisory.
	ErrPrerequisiteKindInvalid = errors.New("delivery: prerequisite kind must be hard_gate or advisory")
	// ErrPrerequisiteSelfEdge — a course cannot be its own prerequisite.
	ErrPrerequisiteSelfEdge = errors.New("delivery: a course cannot be its own prerequisite")
	// ErrPrerequisiteCycle — the edge would close a cycle in the tenant DAG.
	ErrPrerequisiteCycle = errors.New("delivery: prerequisite would create a cycle")
	// ErrPrerequisiteCapExceeded — the source course is at its edge cap.
	ErrPrerequisiteCapExceeded = errors.New("delivery: prerequisite edge cap exceeded for course")
	// ErrPrerequisiteUnknownCourse — a referenced course is not in the tenant
	// catalogue (raised by the application service, which owns catalogue lookup).
	ErrPrerequisiteUnknownCourse = errors.New("delivery: prerequisite references a course not in the tenant catalogue")
)

// ValidateNewPrerequisiteEdge validates a proposed NEW edge (source requires
// target) against the tenant's existing ACTIVE edge set. Pure: existence of the
// two courses in the catalogue is the caller's responsibility (needs the repo).
//
// Order (fail-loud, most-specific first): required → kind → self → cap → cycle.
// maxEdges ≤ 0 falls back to CourseMaxPrereqEdgesDefault.
func ValidateNewPrerequisiteEdge(source, target string, kind PrereqKind, existing []PrerequisiteEdge, maxEdges int) error {
	if source == "" || target == "" {
		return ErrPrerequisiteCourseRequired
	}
	if !kind.IsValid() {
		return ErrPrerequisiteKindInvalid
	}
	if source == target {
		return ErrPrerequisiteSelfEdge
	}
	if maxEdges <= 0 {
		maxEdges = CourseMaxPrereqEdgesDefault
	}
	if countEdgesForCourse(existing, source) >= maxEdges {
		return ErrPrerequisiteCapExceeded
	}
	if wouldCreatePrerequisiteCycle(existing, source, target) {
		return ErrPrerequisiteCycle
	}
	return nil
}

// countEdgesForCourse counts the active edges whose source is courseID.
func countEdgesForCourse(existing []PrerequisiteEdge, courseID string) int {
	n := 0
	for _, e := range existing {
		if e.CourseID == courseID {
			n++
		}
	}
	return n
}

// wouldCreatePrerequisiteCycle reports whether adding source→target (source
// requires target) to the existing edge set would form a cycle.
//
// Adding A→B closes a cycle iff B can already reach A by following existing
// "requires" edges. We DFS forward from target over the adjacency
// (course → its prerequisites); reaching source ⇒ cycle. Callers pass only
// ACTIVE (non-soft-deleted) edges.
func wouldCreatePrerequisiteCycle(existing []PrerequisiteEdge, source, target string) bool {
	// adjacency: course → list of the courses it requires.
	adj := make(map[string][]string, len(existing))
	for _, e := range existing {
		adj[e.CourseID] = append(adj[e.CourseID], e.PrerequisiteCourseID)
	}
	visited := make(map[string]bool)
	var reaches func(node string) bool
	reaches = func(node string) bool {
		if node == source {
			return true
		}
		if visited[node] {
			return false
		}
		visited[node] = true
		for _, next := range adj[node] {
			if reaches(next) {
				return true
			}
		}
		return false
	}
	return reaches(target)
}
