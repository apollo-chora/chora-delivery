// candidate_repo.go — in-memory repository for the Candidate aggregate
// (ADR-190 D2 identity-verified exam allocation).
//
// Hexagonal layout: adapter depends on the pure domain package
// internal/domain/exam (the domain never imports this file). Add-only — the
// existing ExamRepo in exam_repo.go is untouched.
//
// Production wires pg.CandidateRepo (chora_delivery.exam_candidates, migration
// 0047 — durable + RLS-isolated). This dev/test store keeps the handler
// signatures stable behind the exam.CandidateStore port.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// CandidateRepo is an in-memory store for Candidate aggregates, keyed by id.
type CandidateRepo struct {
	mu sync.RWMutex
	by map[string]*exam.Candidate // key = candidate id
}

// NewCandidateRepo returns an empty repo.
func NewCandidateRepo() *CandidateRepo {
	return &CandidateRepo{by: make(map[string]*exam.Candidate)}
}

// Compile-time assertion: satisfies the domain port so it stays a drop-in for
// pg.CandidateRepo at cmd/server wiring.
var _ exam.CandidateStore = (*CandidateRepo)(nil)

// Save inserts or upserts a candidate by ID. ctx is accepted to satisfy the
// port (the pg adapter uses it for RLS); the in-memory store ignores it.
func (r *CandidateRepo) Save(_ context.Context, c *exam.Candidate) error {
	if c == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[c.ID] = c
	return nil
}

// GetByExamAndGCID resolves the active (non-soft-deleted) candidate for a
// (tenant, exam, gcid) triple.
func (r *CandidateRepo) GetByExamAndGCID(_ context.Context, tenantID, examID, gcid string) (*exam.Candidate, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.by {
		if c.DeletedAt != nil {
			continue
		}
		if c.TenantID == tenantID && c.ExamID == examID && c.GCID == gcid {
			return c, true, nil
		}
	}
	return nil, false, nil
}

// ListByExam returns the non-soft-deleted candidates for a (tenant, exam),
// sorted by ID (UUIDv7 ⇒ allocation order). The returned slice is a fresh
// copy — safe for the caller to mutate.
func (r *CandidateRepo) ListByExam(_ context.Context, tenantID, examID string) ([]*exam.Candidate, error) {
	r.mu.RLock()
	out := make([]*exam.Candidate, 0, len(r.by))
	for _, c := range r.by {
		if c.DeletedAt != nil || c.TenantID != tenantID || c.ExamID != examID {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}
