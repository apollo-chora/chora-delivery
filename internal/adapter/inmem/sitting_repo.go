// sitting_repo.go — in-memory store for the ExamSitting aggregate (W4 Brick-B).
//
// Hexagonal layout: adapter depends on the pure domain package
// internal/domain/exam (the domain never imports this file). Production wires
// pg.SittingRepo (chora_delivery.exam_sittings, migration 0048 — durable +
// RLS-isolated). This dev/test store keeps the handler signatures stable behind
// the exam.ExamSittingStore port.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// SittingRepo is an in-memory store for ExamSitting aggregates, keyed by id.
type SittingRepo struct {
	mu sync.RWMutex
	by map[string]*exam.ExamSitting
}

// NewSittingRepo returns an empty repo.
func NewSittingRepo() *SittingRepo {
	return &SittingRepo{by: make(map[string]*exam.ExamSitting)}
}

// Compile-time assertion: satisfies the domain port.
var _ exam.ExamSittingStore = (*SittingRepo)(nil)

// Save inserts or upserts a sitting by ID. ctx is accepted to satisfy the port
// (the pg adapter uses it for RLS); the in-memory store ignores it.
func (r *SittingRepo) Save(_ context.Context, s *exam.ExamSitting) error {
	if s == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[s.ID] = s
	return nil
}

// Get resolves a sitting by id.
func (r *SittingRepo) Get(_ context.Context, id string) (*exam.ExamSitting, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.by[id]
	return s, ok, nil
}

// ListByExam returns the non-soft-deleted sittings for a (tenant, exam), sorted
// by ID (UUIDv7 ⇒ creation order).
func (r *SittingRepo) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.ExamSitting, error) {
	r.mu.RLock()
	out := make([]*exam.ExamSitting, 0, len(r.by))
	for _, s := range r.by {
		if s.DeletedAt != nil || s.TenantID != tenantID || s.ExamID != examID {
			continue
		}
		out = append(out, s)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}
