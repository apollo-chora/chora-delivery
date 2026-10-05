// live_quiz_repo.go — in-memory repository for the classroom-realtime
// LiveQuiz aggregate (R+ M9, wave-5).
//
// Hexagonal layout: adapter depends on the pure domain package
// internal/domain/classroom (the domain layer never imports this file).
//
// Add-only: existing repos in inmem.go / exam_repo.go / etc. are untouched.
// The LiveQuiz aggregate (DRAFT → PUBLISHED → ARMED → LIVE → CLOSED) lives
// at services/chora-delivery/internal/domain/classroom/live_quiz.go (wave-2a
// commit c924152e). This repo backs the M9 /api/v1/live-quizzes CRUD; the
// downstream LiveQuizSession surface (M10) will get its own repo.
//
// Production (M12+) will swap a Postgres adapter against chora_delivery via
// PgBouncer; handler signatures stay stable. Until then this store is shared
// across HTTP and (future) gRPC paths via the same Deps pointer the
// cmd/server bootstrap wires.
package inmem

import (
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// LiveQuizRepo is an in-memory store for LiveQuiz aggregates, keyed by quiz id.
type LiveQuizRepo struct {
	mu sync.RWMutex
	by map[string]*classroom.LiveQuiz // key = live_quiz.id
}

// Compile-time assertion: in-mem repo satisfies the ADR-168 gap-1 domain port.
var _ classroom.QuizStore = (*LiveQuizRepo)(nil)

// NewLiveQuizRepo returns an empty repo.
func NewLiveQuizRepo() *LiveQuizRepo {
	return &LiveQuizRepo{by: make(map[string]*classroom.LiveQuiz)}
}

// Save inserts or upserts a live quiz by ID. Nil input is a no-op. Never
// errors (in-memory) — the error return satisfies classroom.QuizStore.
func (r *LiveQuizRepo) Save(q *classroom.LiveQuiz) error {
	if q == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[q.ID] = q
	return nil
}

// Get returns a live quiz by ID and an ok flag.
func (r *LiveQuizRepo) Get(id string) (*classroom.LiveQuiz, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	q, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return q, true, nil
}

// ListByTenant returns the live quizzes for a tenant sorted by ID
// (UUIDv7 ⇒ creation order). The returned slice is a fresh copy — safe for
// the caller to mutate without affecting the store.
func (r *LiveQuizRepo) ListByTenant(tenantID string) []*classroom.LiveQuiz {
	r.mu.RLock()
	out := make([]*classroom.LiveQuiz, 0, len(r.by))
	for _, q := range r.by {
		if q.TenantID != tenantID {
			continue
		}
		out = append(out, q)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out
}
