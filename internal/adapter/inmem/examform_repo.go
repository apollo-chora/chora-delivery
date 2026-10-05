// examform_repo.go — in-memory repository for the ExamForm aggregate (W4
// Brick-1). Mirrors exam_repo.go: a drop-in exam.ExamFormStore for dev / unit
// tests; production wires pg.ExamFormRepo (chora_delivery.exam_forms). The
// domain package never imports this file (hexagonal: adapter → domain only).
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// ExamFormRepo is an in-memory store for ExamForm aggregates, keyed by id.
type ExamFormRepo struct {
	mu sync.RWMutex
	by map[string]*exam.ExamForm
}

// NewExamFormRepo returns an empty repo.
func NewExamFormRepo() *ExamFormRepo {
	return &ExamFormRepo{by: make(map[string]*exam.ExamForm)}
}

// Compile-time assertion: drop-in for pg.ExamFormRepo at cmd/server wiring.
var _ exam.ExamFormStore = (*ExamFormRepo)(nil)

// Save inserts or upserts a form by ID. ctx is accepted to satisfy the port
// (the pg adapter uses it for RLS); the in-memory store ignores it.
func (r *ExamFormRepo) Save(_ context.Context, f *exam.ExamForm) error {
	if f == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[f.ID] = f
	return nil
}

// Get returns a form by ID and an ok flag.
func (r *ExamFormRepo) Get(_ context.Context, id string) (*exam.ExamForm, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return f, true, nil
}

// ListByExam returns the non-soft-deleted forms for (tenant, exam), sorted by
// ID (UUIDv7 ⇒ creation order). The returned slice is a fresh copy.
func (r *ExamFormRepo) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.ExamForm, error) {
	r.mu.RLock()
	out := make([]*exam.ExamForm, 0, len(r.by))
	for _, f := range r.by {
		if f.TenantID != tenantID || f.ExamID != examID || f.DeletedAt != nil {
			continue
		}
		out = append(out, f)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}
