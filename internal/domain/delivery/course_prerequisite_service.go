// course_prerequisite_service.go — application service for the Course
// Prerequisite DAG (ADR-226). Composes the pure validator + cycle detector
// (course_prerequisite.go) with catalogue existence checks (CourseCJ2Port) and
// the edge persistence port (CoursePrerequisitePort).
//
// The service holds NO graph rules itself — acyclicity + cap + kind live in the
// pure domain; the service adds the impure concerns: does the referenced course
// exist in the tenant catalogue, and idempotent kind-update on re-add.
package delivery

import "context"

// CoursePrerequisiteService orchestrates prerequisite-edge authoring.
type CoursePrerequisiteService struct {
	edges    CoursePrerequisitePort
	courses  CourseCJ2Port
	maxEdges int
}

// NewCoursePrerequisiteService wires the edge port + catalogue port. A
// non-positive maxEdges falls back to CourseMaxPrereqEdgesDefault.
func NewCoursePrerequisiteService(edges CoursePrerequisitePort, courses CourseCJ2Port, maxEdges int) *CoursePrerequisiteService {
	if maxEdges <= 0 {
		maxEdges = CourseMaxPrereqEdgesDefault
	}
	return &CoursePrerequisiteService{edges: edges, courses: courses, maxEdges: maxEdges}
}

// List returns the active prerequisites of a course.
func (s *CoursePrerequisiteService) List(ctx context.Context, tenantID, courseID string) ([]CoursePrerequisite, error) {
	if courseID == "" {
		return nil, ErrPrerequisiteCourseRequired
	}
	return s.edges.ListForCourse(ctx, tenantID, courseID)
}

// UnmetHardGates returns the hard_gate prerequisites of courseID that the
// learner has NOT completed — the enrol-blocking set (empty ⇒ enrol allowed).
// advisory prerequisites are never returned (they inform, never block).
//
// completedCourseIDs is the set of course ids the learner holds a COMPLETED
// enrolment for; passing it in (rather than having this service reach into an
// Enrollment port) keeps the prerequisite aggregate decoupled from the
// Enrollment aggregate — the caller composes the two. ADR-226 §4 enforcement.
func (s *CoursePrerequisiteService) UnmetHardGates(ctx context.Context, tenantID, courseID string, completedCourseIDs map[string]bool) ([]CoursePrerequisite, error) {
	if courseID == "" {
		return nil, ErrPrerequisiteCourseRequired
	}
	prereqs, err := s.edges.ListForCourse(ctx, tenantID, courseID)
	if err != nil {
		return nil, err
	}
	var unmet []CoursePrerequisite
	for _, p := range prereqs {
		if p.Kind != PrereqKindHardGate {
			continue
		}
		if !completedCourseIDs[p.PrerequisiteCourseID] {
			unmet = append(unmet, p)
		}
	}
	return unmet, nil
}

// Add declares "courseID requires prerequisiteCourseID" with the given kind.
//
// Fail-loud order: required → kind → self → catalogue-existence (source, then
// target) → [idempotent kind-update when the edge already exists] → cap → cycle.
// Returns the course's full updated prerequisite list on success.
func (s *CoursePrerequisiteService) Add(ctx context.Context, tenantID, courseID, prerequisiteCourseID string, kind PrereqKind) ([]CoursePrerequisite, error) {
	if courseID == "" || prerequisiteCourseID == "" {
		return nil, ErrPrerequisiteCourseRequired
	}
	if !kind.IsValid() {
		return nil, ErrPrerequisiteKindInvalid
	}
	if courseID == prerequisiteCourseID {
		return nil, ErrPrerequisiteSelfEdge
	}
	// Catalogue existence — both endpoints must be real courses in the tenant.
	if err := s.requireCourse(ctx, tenantID, courseID); err != nil {
		return nil, err
	}
	if err := s.requireCourse(ctx, tenantID, prerequisiteCourseID); err != nil {
		return nil, err
	}
	all, err := s.edges.ListForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	edgeFrom := PrerequisiteEdge{CourseID: courseID, PrerequisiteCourseID: prerequisiteCourseID, Kind: kind}
	if edgeExists(all, courseID, prerequisiteCourseID) {
		// Edge already present — this is a kind change. The graph is unchanged,
		// so cap + cycle checks do not apply (they would false-reject a course
		// already at cap, or a valid edge). Idempotent upsert of the kind.
		if err := s.edges.Upsert(ctx, tenantID, edgeFrom); err != nil {
			return nil, err
		}
		return s.edges.ListForCourse(ctx, tenantID, courseID)
	}
	if err := ValidateNewPrerequisiteEdge(courseID, prerequisiteCourseID, kind, all, s.maxEdges); err != nil {
		return nil, err
	}
	if err := s.edges.Upsert(ctx, tenantID, edgeFrom); err != nil {
		return nil, err
	}
	return s.edges.ListForCourse(ctx, tenantID, courseID)
}

// Remove drops the edge "courseID requires prerequisiteCourseID" (idempotent —
// removing a non-existent edge is a no-op). Returns the updated list.
func (s *CoursePrerequisiteService) Remove(ctx context.Context, tenantID, courseID, prerequisiteCourseID string) ([]CoursePrerequisite, error) {
	if courseID == "" || prerequisiteCourseID == "" {
		return nil, ErrPrerequisiteCourseRequired
	}
	if err := s.edges.Remove(ctx, tenantID, courseID, prerequisiteCourseID); err != nil {
		return nil, err
	}
	return s.edges.ListForCourse(ctx, tenantID, courseID)
}

// requireCourse returns ErrPrerequisiteUnknownCourse when courseID is not an
// existing (non-soft-deleted) course in the tenant catalogue.
func (s *CoursePrerequisiteService) requireCourse(ctx context.Context, tenantID, courseID string) error {
	_, ok, err := s.courses.Get(ctx, tenantID, courseID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPrerequisiteUnknownCourse
	}
	return nil
}

// edgeExists reports whether an active (course requires prereq) edge is present.
func edgeExists(edges []PrerequisiteEdge, courseID, prerequisiteCourseID string) bool {
	for _, e := range edges {
		if e.CourseID == courseID && e.PrerequisiteCourseID == prerequisiteCourseID {
			return true
		}
	}
	return false
}
