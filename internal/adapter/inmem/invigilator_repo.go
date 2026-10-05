// invigilator_repo.go — in-memory store for the ExamInvigilator aggregate
// (W4 Brick-B). Adapter depends on the pure domain package
// internal/domain/exam. Production wires pg.InvigilatorRepo
// (chora_delivery.exam_invigilators, migration 0048).
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// InvigilatorRepo is an in-memory store for ExamInvigilator assignments, keyed
// by id.
type InvigilatorRepo struct {
	mu sync.RWMutex
	by map[string]*exam.ExamInvigilator
}

// NewInvigilatorRepo returns an empty repo.
func NewInvigilatorRepo() *InvigilatorRepo {
	return &InvigilatorRepo{by: make(map[string]*exam.ExamInvigilator)}
}

// Compile-time assertion: satisfies the domain port.
var _ exam.ExamInvigilatorStore = (*InvigilatorRepo)(nil)

// Save inserts or upserts an assignment by ID.
func (r *InvigilatorRepo) Save(_ context.Context, iv *exam.ExamInvigilator) error {
	if iv == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[iv.ID] = iv
	return nil
}

// Get resolves an assignment by id.
func (r *InvigilatorRepo) Get(_ context.Context, id string) (*exam.ExamInvigilator, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	iv, ok := r.by[id]
	return iv, ok, nil
}

// ListBySitting returns the non-soft-deleted (active) invigilators for a
// (tenant, sitting), sorted by ID — the roster + the input to
// exam.EnsureSingleChief.
func (r *InvigilatorRepo) ListBySitting(_ context.Context, tenantID, sittingID string) ([]*exam.ExamInvigilator, error) {
	r.mu.RLock()
	out := make([]*exam.ExamInvigilator, 0, len(r.by))
	for _, iv := range r.by {
		if iv.DeletedAt != nil || iv.TenantID != tenantID || iv.SittingID != sittingID {
			continue
		}
		out = append(out, iv)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}
