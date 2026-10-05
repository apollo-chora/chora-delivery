// exam_repo.go — in-memory repository for the Exam aggregate.
//
// Hexagonal layout: adapter depends on the pure domain package
// internal/domain/exam (the domain layer never imports this file).
//
// Add-only: the existing CourseRepo / BookingRepo in inmem.go
// are untouched. The R+ exams track lives entirely in this new file so
// parallel domain edits don't collide.
//
// Production (M12+) will swap in a Postgres adapter against chora_delivery
// via PgBouncer; the handler signatures stay stable. Until then, this
// store is shared across HTTP and (future) gRPC paths via the same Deps
// pointer the cmd/server bootstrap wires.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// ExamRepo is an in-memory store for Exam aggregates, keyed by exam_id.
type ExamRepo struct {
	mu sync.RWMutex
	by map[string]*exam.Exam // key = exam_id
}

// NewExamRepo returns an empty repo.
func NewExamRepo() *ExamRepo {
	return &ExamRepo{by: make(map[string]*exam.Exam)}
}

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for pg.ExamRepo at cmd/server wiring.
var _ exam.ExamStore = (*ExamRepo)(nil)

// Save inserts or upserts an exam by ID. ctx is accepted to satisfy
// exam.ExamStore (the pg adapter uses it for RLS); the in-memory store
// ignores it. Never errors.
func (r *ExamRepo) Save(_ context.Context, e *exam.Exam) error {
	if e == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[e.ID] = e
	return nil
}

// Get returns an exam by ID and a bool ok flag.
func (r *ExamRepo) Get(_ context.Context, id string) (*exam.Exam, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return e, true, nil
}

// ListByTenant returns the non-soft-deleted exams for a tenant, sorted by
// ID (UUIDv7 ⇒ creation order). The returned slice is a fresh copy — safe
// for the caller to mutate without affecting the store.
func (r *ExamRepo) ListByTenant(_ context.Context, tenantID string) ([]*exam.Exam, error) {
	r.mu.RLock()
	out := make([]*exam.Exam, 0, len(r.by))
	for _, e := range r.by {
		if e.TenantID != tenantID || e.DeletedAt != nil {
			continue
		}
		out = append(out, e)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}
