// course_cj2_port.go — Port + in-memory adapter for the CJ#2 Course
// aggregate.
//
// Hexagonal: the HTTP handler depends on this interface; production wires
// pg.CourseRepo's SaveCJ2 / GetCJ2 / ListByState methods. Local dev + unit
// tests wire InMemCourseCJ2Store.
//
// Per CJ#2 directive row at `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3.
package delivery

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// CourseCJ2Port is the hexagonal contract over the Course aggregate's
// CJ#2 state-FSM persistence concerns. Read/write methods return an error
// so DB failures propagate; the in-memory adapter always returns nil.
type CourseCJ2Port interface {
	// Save persists a CJ#2 Course (UPSERT on course_id). Idempotent.
	Save(ctx context.Context, c *Course) error
	// Get fetches a CJ#2 Course by (tenantID, courseID). ok=false when no
	// matching row exists (or soft-deleted).
	Get(ctx context.Context, tenantID, courseID string) (*Course, bool, error)
	// ListByState returns the CJ#2 courses in the given state for the
	// tenant, offset+limit paginated, ordered by created_at DESC. query, when
	// non-empty, filters to titles containing it (case-insensitive substring)
	// — backs the R+ course entity-picker search (GET /api/v1/courses?q=);
	// empty query applies no filter (additive, backward compatible).
	ListByState(ctx context.Context, tenantID string, state CourseState, query string, offset, limit int) ([]*Course, error)
	// ListByStateAndAuthor returns the CJ#2 courses in the given state for
	// the tenant that were authored by authorGCID, offset+limit paginated,
	// ordered by created_at DESC. Same RLS/tenant scope as ListByState —
	// authorship is an ADDITIONAL filter, never a cross-tenant broadening.
	// query has the same case-insensitive title-substring semantics as
	// ListByState.
	//
	// Backs the ONBOARD-UI F1 fix: an ADR-182 `author` (A+ Creator) holds no
	// training-admin role, so the training-admin list scopes them to their
	// OWN authored courses regardless of lifecycle state (another author's
	// non-PUBLISHED courses never leak).
	ListByStateAndAuthor(ctx context.Context, tenantID string, state CourseState, authorGCID string, query string, offset, limit int) ([]*Course, error)
}

// -----------------------------------------------------------------------------
// InMemCourseCJ2Store — in-memory adapter satisfying CourseCJ2Port.
// -----------------------------------------------------------------------------

// InMemCourseCJ2Store keeps CJ#2 Courses keyed by course_id. Goroutine-safe
// via a sync.RWMutex.
type InMemCourseCJ2Store struct {
	mu       sync.RWMutex
	byID     map[string]*Course
	byTState map[string][]string // tenant_id|state → []course_id (insertion order)
}

// NewInMemCourseCJ2Store returns an empty store.
func NewInMemCourseCJ2Store() *InMemCourseCJ2Store {
	return &InMemCourseCJ2Store{
		byID:     make(map[string]*Course),
		byTState: make(map[string][]string),
	}
}

// Save upserts the course. Mutates only the store's mirror, NOT the
// caller-supplied pointer (we keep a defensive copy of slice fields).
func (s *InMemCourseCJ2Store) Save(_ context.Context, c *Course) error {
	if c == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, existed := s.byID[c.ID]
	// Defensive shallow copy of the aggregate so the caller can mutate its
	// pointer without affecting our stored value.
	cp := *c
	cp.AtomIDs = append([]string(nil), c.AtomIDs...)
	cp.LearningObjectives = append([]string(nil), c.LearningObjectives...)
	cp.PrerequisiteNotes = append([]string(nil), c.PrerequisiteNotes...)
	cp.TestSetIDs = append([]string(nil), c.TestSetIDs...)
	cp.InstructorGCIDs = append([]string(nil), c.InstructorGCIDs...)
	s.byID[c.ID] = &cp

	// If state changed, remove from old bucket + insert into new.
	if existed && prev.State != c.State {
		oldKey := prev.TenantID + "|" + string(prev.State)
		s.byTState[oldKey] = removeID(s.byTState[oldKey], c.ID)
	}
	newKey := c.TenantID + "|" + string(c.State)
	if !existed || prev.State != c.State {
		s.byTState[newKey] = append(s.byTState[newKey], c.ID)
	}
	return nil
}

// Get returns the course, RLS-scoped equivalent (tenant_id check).
func (s *InMemCourseCJ2Store) Get(_ context.Context, tenantID, courseID string) (*Course, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.byID[courseID]
	if !ok || c.TenantID != tenantID || c.DeletedAt != nil {
		return nil, false, nil
	}
	// Return defensive copy so the caller can mutate freely.
	cp := *c
	return &cp, true, nil
}

// ListByState returns courses in the given state, ordered DESC by created_at.
// query, when non-empty, filters to titles containing it (case-insensitive
// substring) — mirrors the pg adapter's `title ILIKE` clause.
func (s *InMemCourseCJ2Store) ListByState(_ context.Context, tenantID string, state CourseState, query string, offset, limit int) ([]*Course, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := append([]string(nil), s.byTState[tenantID+"|"+string(state)]...)
	out := make([]*Course, 0, len(ids))
	for _, id := range ids {
		c, ok := s.byID[id]
		if !ok || c.DeletedAt != nil {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(c.Title), strings.ToLower(query)) {
			continue
		}
		cp := *c
		out = append(out, &cp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if offset >= len(out) {
		return nil, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

// ListByStateAndAuthor returns courses in the given state authored by
// authorGCID, ordered DESC by created_at. tenant + author both required —
// an empty author would conflate with "any", which the SQL repo rejects;
// mirror that here so the in-memory adapter matches. query, when non-empty,
// filters to titles containing it (case-insensitive substring).
func (s *InMemCourseCJ2Store) ListByStateAndAuthor(_ context.Context, tenantID string, state CourseState, authorGCID string, query string, offset, limit int) ([]*Course, error) {
	if tenantID == "" || authorGCID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := append([]string(nil), s.byTState[tenantID+"|"+string(state)]...)
	out := make([]*Course, 0, len(ids))
	for _, id := range ids {
		c, ok := s.byID[id]
		if !ok || c.DeletedAt != nil || c.AuthorGCID != authorGCID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(c.Title), strings.ToLower(query)) {
			continue
		}
		cp := *c
		out = append(out, &cp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if offset >= len(out) {
		return nil, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

// removeID returns ids without the matching element. Preserves order.
func removeID(ids []string, target string) []string {
	out := ids[:0]
	for _, id := range ids {
		if id != target {
			out = append(out, id)
		}
	}
	return out
}

// Compile-time guard: both adapters satisfy the port.
var _ CourseCJ2Port = (*InMemCourseCJ2Store)(nil)
