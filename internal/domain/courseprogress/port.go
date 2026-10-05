// port.go - the hexagonal port for the CourseLearnerProgress projection, plus
// the InMemProgressStore dev/test impl.
//
// Every method takes ctx: the pg adapter reads the tenant off the CONTEXT to
// drive rls.ApplySession, and none of these methods take a tenant argument for
// that purpose. A port without ctx cannot be made durable under FORCE-RLS - the
// adapter would be forced to swallow, or to write 0 rows silently.
//
// Every method returns an error. A (T, bool) shape would conflate "this learner
// has no progress yet" with "the read failed", and the caller (a Pub/Sub
// subscriber deciding ACK vs NACK) would then destroy the event on an infra
// blip. Absence and breakage are different answers.
package courseprogress

import (
	"context"
	"sort"
	"sync"
	"time"
)

// ProgressPort persists + reads CourseLearnerProgress projections.
type ProgressPort interface {
	// Advance atomically loads-or-creates the learner's projection for the course
	// and folds in one learning_path.advanced.v1 - under a row lock in pg, so two
	// advances for the same (learner, course) cannot race and drop one. Returns
	// changed=true iff stored state moved (false on an idempotent/stale redelivery).
	Advance(ctx context.Context, tenantID, gcid, courseID, pathID string, completedAtoms, totalAtoms int, occurredAt time.Time) (bool, error)

	// Complete atomically loads-or-creates the learner's projection and marks the
	// course complete. Load-or-create (not update-only) because completed.v1 may
	// arrive before any advance.v1 for the same path - the completion is still
	// true, and dropping it would lose a finished learner from completion_rate.
	Complete(ctx context.Context, tenantID, gcid, courseID, pathID string, occurredAt time.Time) (bool, error)

	// GetByLearnerCourse loads a learner's active projection for one course.
	// A genuine miss is (nil, false, nil); a failure is a non-nil error.
	GetByLearnerCourse(ctx context.Context, tenantID, gcid, courseID string) (*CourseLearnerProgress, bool, error)

	// ListByCourseIDs returns active projections for the given courses
	// (tenant-scoped). The analytics roll-up intersects these with the roster.
	ListByCourseIDs(ctx context.Context, tenantID string, courseIDs []string) ([]*CourseLearnerProgress, error)
}

// -----------------------------------------------------------------------------
// InMemProgressStore - dev/test ProgressPort impl
// -----------------------------------------------------------------------------

// InMemProgressStore is a mutex-guarded, map-backed ProgressPort for dev + unit
// tests. Tenant scoping is enforced in-process (the pg adapter defers to RLS).
type InMemProgressStore struct {
	mu   sync.Mutex
	byID map[string]*CourseLearnerProgress
}

// NewInMemProgressStore returns an empty store.
func NewInMemProgressStore() *InMemProgressStore {
	return &InMemProgressStore{byID: make(map[string]*CourseLearnerProgress)}
}

// findLocked returns the active projection for (tenant, gcid, course). Caller
// holds the mutex.
func (s *InMemProgressStore) findLocked(tenantID, gcid, courseID string) *CourseLearnerProgress {
	for _, p := range s.byID {
		if p.TenantID == tenantID && p.GCID == gcid && p.CourseID == courseID && p.DeletedAt == nil {
			return p
		}
	}
	return nil
}

// loadOrCreateLocked is the in-process analogue of the pg row lock.
func (s *InMemProgressStore) loadOrCreateLocked(tenantID, gcid, courseID, pathID string) (*CourseLearnerProgress, error) {
	if p := s.findLocked(tenantID, gcid, courseID); p != nil {
		return p, nil
	}
	return New(NewParams{TenantID: tenantID, GCID: gcid, CourseID: courseID, PathID: pathID})
}

// Advance load-or-creates then folds in one advance.
func (s *InMemProgressStore) Advance(_ context.Context, tenantID, gcid, courseID, pathID string, completedAtoms, totalAtoms int, occurredAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.loadOrCreateLocked(tenantID, gcid, courseID, pathID)
	if err != nil {
		return false, err
	}
	changed, err := p.RecordAdvance(completedAtoms, totalAtoms, occurredAt)
	if err != nil {
		return false, err
	}
	if changed {
		s.byID[p.ID] = p
	}
	return changed, nil
}

// Complete load-or-creates then marks the course complete.
func (s *InMemProgressStore) Complete(_ context.Context, tenantID, gcid, courseID, pathID string, occurredAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.loadOrCreateLocked(tenantID, gcid, courseID, pathID)
	if err != nil {
		return false, err
	}
	changed, err := p.RecordCompletion(occurredAt)
	if err != nil {
		return false, err
	}
	if changed {
		s.byID[p.ID] = p
	}
	return changed, nil
}

// GetByLearnerCourse returns the active projection for (tenant, gcid, course).
func (s *InMemProgressStore) GetByLearnerCourse(_ context.Context, tenantID, gcid, courseID string) (*CourseLearnerProgress, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.findLocked(tenantID, gcid, courseID); p != nil {
		return p, true, nil
	}
	return nil, false, nil
}

// ListByCourseIDs returns active projections for the given courses, tenant-scoped.
func (s *InMemProgressStore) ListByCourseIDs(_ context.Context, tenantID string, courseIDs []string) ([]*CourseLearnerProgress, error) {
	want := make(map[string]bool, len(courseIDs))
	for _, id := range courseIDs {
		want[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*CourseLearnerProgress, 0)
	for _, p := range s.byID {
		if p.TenantID == tenantID && p.DeletedAt == nil && want[p.CourseID] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CourseID != out[j].CourseID {
			return out[i].CourseID < out[j].CourseID
		}
		return out[i].GCID < out[j].GCID
	})
	return out, nil
}

// Compile-time assertion.
var _ ProgressPort = (*InMemProgressStore)(nil)
