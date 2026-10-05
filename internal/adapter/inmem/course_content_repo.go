// course_content_repo.go — in-memory repository for the CourseContent
// curriculum aggregate (CHO-1612). Production swaps in a pg adapter against
// chora_delivery; the domain + service layers are unchanged.
package inmem

import (
	"context"
	"sync"

	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

// CourseContentRepo is an in-memory store keyed by (tenant_id, course_id).
type CourseContentRepo struct {
	mu sync.RWMutex
	by map[string]*cc.CourseContent
}

// NewCourseContentRepo returns an empty repo.
func NewCourseContentRepo() *CourseContentRepo {
	return &CourseContentRepo{by: make(map[string]*cc.CourseContent)}
}

func key(tenantID, courseID string) string { return tenantID + "/" + courseID }

// Get returns the curriculum or cc.ErrNotFound (soft-deleted treated as absent).
func (r *CourseContentRepo) Get(_ context.Context, tenantID, courseID string) (*cc.CourseContent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	got, ok := r.by[key(tenantID, courseID)]
	if !ok || got.DeletedAt != nil {
		return nil, cc.ErrNotFound
	}
	return got, nil
}

// Save upserts the aggregate.
func (r *CourseContentRepo) Save(_ context.Context, c *cc.CourseContent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[key(c.TenantID, c.CourseID)] = c
	return nil
}
