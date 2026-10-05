// examresult_repo.go — in-memory repository for the ExamResult aggregate (W4
// Brick-1). Mirrors exam_repo.go: a drop-in exam.ExamResultStore for dev / unit
// tests; production wires pg.ExamResultRepo (chora_delivery.exam_results). The
// durable row is the source of truth; this store is dev-only (lost on restart).
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// ExamResultRepo is an in-memory store for ExamResult aggregates, keyed by id.
type ExamResultRepo struct {
	mu sync.RWMutex
	by map[string]*exam.ExamResult
}

// NewExamResultRepo returns an empty repo.
func NewExamResultRepo() *ExamResultRepo {
	return &ExamResultRepo{by: make(map[string]*exam.ExamResult)}
}

// Compile-time assertion: drop-in for pg.ExamResultRepo at cmd/server wiring.
var _ exam.ExamResultStore = (*ExamResultRepo)(nil)

// Save records a result by ID. Write-once semantics mirror the pg adapter: an
// existing id is not overwritten (a scored result is immutable). ctx is
// accepted to satisfy the port.
func (r *ExamResultRepo) Save(_ context.Context, res *exam.ExamResult) error {
	if res == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.by[res.ID]; exists {
		return nil // write-once: do not overwrite an existing result
	}
	r.by[res.ID] = res
	return nil
}

// Get returns a result by ID and an ok flag.
func (r *ExamResultRepo) Get(_ context.Context, id string) (*exam.ExamResult, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return res, true, nil
}

// ListByForm returns the non-soft-deleted results for (tenant, exam form),
// sorted by ID. The returned slice is a fresh copy.
func (r *ExamResultRepo) ListByForm(_ context.Context, tenantID, examFormID string) ([]*exam.ExamResult, error) {
	r.mu.RLock()
	out := make([]*exam.ExamResult, 0, len(r.by))
	for _, res := range r.by {
		if res.TenantID != tenantID || res.ExamFormID != examFormID || res.DeletedAt != nil {
			continue
		}
		out = append(out, res)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}

// ListByExam returns the tenant's non-soft-deleted results across ALL forms of
// one exam, most-recent first (CHO-2104). The returned slice is a fresh copy.
func (r *ExamResultRepo) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.ExamResult, error) {
	r.mu.RLock()
	out := make([]*exam.ExamResult, 0, len(r.by))
	for _, res := range r.by {
		if res.TenantID != tenantID || res.ExamID != examID || res.DeletedAt != nil {
			continue
		}
		out = append(out, res)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ScoredAt.After(out[j].ScoredAt) })
	return out, nil
}
