// course_prerequisite_port.go — hexagonal port + in-memory adapter for the
// Course Prerequisite DAG edges (ADR-226).
//
// Production wires pg.CoursePrerequisiteRepo (chora_delivery.course_prerequisites
// — durable, RLS-isolated, soft-delete-aware). Local dev + unit tests wire
// InMemCoursePrerequisiteStore. Every method is tenant-scoped (the pg adapter
// SET LOCAL chora.tenant_id + RLS; the in-mem adapter filters by the tenant arg).
package delivery

import (
	"context"
	"sort"
	"sync"
)

// CoursePrerequisitePort is the persistence contract over the tenant's
// course→course prerequisite edge set.
type CoursePrerequisitePort interface {
	// ListForCourse returns the active prerequisites of a single course
	// (ordered by prerequisite course id for determinism).
	ListForCourse(ctx context.Context, tenantID, courseID string) ([]CoursePrerequisite, error)
	// ListForTenant returns ALL active edges in the tenant — the graph the
	// service reads for cycle detection + reverse ("what requires X") lookups.
	ListForTenant(ctx context.Context, tenantID string) ([]PrerequisiteEdge, error)
	// Upsert adds an edge, or updates its kind when the (tenant, course,
	// prerequisite) edge already exists. Idempotent.
	Upsert(ctx context.Context, tenantID string, e PrerequisiteEdge) error
	// Remove soft-deletes the (tenant, course, prerequisite) edge. Removing a
	// non-existent edge is a no-op (idempotent).
	Remove(ctx context.Context, tenantID, courseID, prerequisiteCourseID string) error
}

// -----------------------------------------------------------------------------
// InMemCoursePrerequisiteStore — in-memory adapter satisfying the port.
// -----------------------------------------------------------------------------

// InMemCoursePrerequisiteStore keeps active edges keyed by
// tenant|course|prerequisite. Goroutine-safe via a sync.RWMutex.
type InMemCoursePrerequisiteStore struct {
	mu     sync.RWMutex
	active map[string]storedPrereqEdge
}

type storedPrereqEdge struct {
	tenantID string
	edge     PrerequisiteEdge
}

func prereqKey(tenantID, courseID, prereqID string) string {
	return tenantID + "|" + courseID + "|" + prereqID
}

// NewInMemCoursePrerequisiteStore returns an empty store.
func NewInMemCoursePrerequisiteStore() *InMemCoursePrerequisiteStore {
	return &InMemCoursePrerequisiteStore{active: make(map[string]storedPrereqEdge)}
}

// Upsert stores/updates the edge under the tenant scope.
func (s *InMemCoursePrerequisiteStore) Upsert(_ context.Context, tenantID string, e PrerequisiteEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[prereqKey(tenantID, e.CourseID, e.PrerequisiteCourseID)] = storedPrereqEdge{tenantID: tenantID, edge: e}
	return nil
}

// Remove deletes the active edge (idempotent).
func (s *InMemCoursePrerequisiteStore) Remove(_ context.Context, tenantID, courseID, prereqID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, prereqKey(tenantID, courseID, prereqID))
	return nil
}

// ListForCourse returns the active prerequisites of courseID in the tenant.
func (s *InMemCoursePrerequisiteStore) ListForCourse(_ context.Context, tenantID, courseID string) ([]CoursePrerequisite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []CoursePrerequisite
	for _, se := range s.active {
		if se.tenantID == tenantID && se.edge.CourseID == courseID {
			out = append(out, CoursePrerequisite{
				PrerequisiteCourseID: se.edge.PrerequisiteCourseID,
				Kind:                 se.edge.Kind,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PrerequisiteCourseID < out[j].PrerequisiteCourseID })
	return out, nil
}

// ListForTenant returns every active edge in the tenant.
func (s *InMemCoursePrerequisiteStore) ListForTenant(_ context.Context, tenantID string) ([]PrerequisiteEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PrerequisiteEdge
	for _, se := range s.active {
		if se.tenantID == tenantID {
			out = append(out, se.edge)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CourseID != out[j].CourseID {
			return out[i].CourseID < out[j].CourseID
		}
		return out[i].PrerequisiteCourseID < out[j].PrerequisiteCourseID
	})
	return out, nil
}

// Compile-time guard: the in-mem adapter satisfies the port.
var _ CoursePrerequisitePort = (*InMemCoursePrerequisiteStore)(nil)
