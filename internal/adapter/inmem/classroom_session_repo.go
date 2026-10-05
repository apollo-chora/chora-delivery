// classroom_session_repo.go — in-memory repository for the LiveQuizSession
// aggregate (R+ Stage C-lite Wave-5 M8 — classroom-realtime snapshot wire-up).
//
// Hexagonal layout: adapter depends on the pure domain package
// internal/domain/classroom (the domain layer never imports this file).
//
// Add-only: collides with no existing repo. The R+ classroom-session track
// lives entirely in this new file so parallel domain edits don't conflict.
//
// Production (M12+) will swap in a Postgres adapter against chora_delivery
// via PgBouncer; the handler signatures stay stable. Until then this store
// is shared across HTTP (handler) and (future) gRPC paths via the same
// Deps pointer the cmd/server bootstrap wires.
//
// Per `feedback_no_stubs_real_wiring`: when Deps.ClassroomSessions is nil
// the handler returns 503 — this repo is the real wiring (not a stub).
package inmem

import (
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// ClassroomSessionRepo is an in-memory store for LiveQuizSession aggregates,
// keyed by session id (UUIDv7). Concurrent-safe via RWMutex.
type ClassroomSessionRepo struct {
	mu sync.RWMutex
	by map[string]*classroom.LiveQuizSession
}

// Compile-time assertion: in-mem repo satisfies the ADR-168 gap-1 domain port
// (keeps it drop-in interchangeable with the pg store).
var _ classroom.SessionStore = (*ClassroomSessionRepo)(nil)

// NewClassroomSessionRepo returns an empty repo.
func NewClassroomSessionRepo() *ClassroomSessionRepo {
	return &ClassroomSessionRepo{by: make(map[string]*classroom.LiveQuizSession)}
}

// Save inserts or upserts a session by ID. No-op when s is nil. Never errors
// (in-memory) — the error return satisfies classroom.SessionStore.
func (r *ClassroomSessionRepo) Save(s *classroom.LiveQuizSession) error {
	if s == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[s.ID] = s
	return nil
}

// Get returns a session by ID and a bool ok flag.
func (r *ClassroomSessionRepo) Get(id string) (*classroom.LiveQuizSession, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return s, true, nil
}

// GetByJoinCode resolves a tenant's ACTIVE (ARMED|LIVE) session by join code
// (already-normalised), newest ID first (UUIDv7 ⇒ creation order) on the
// improbable collision.
func (r *ClassroomSessionRepo) GetByJoinCode(tenantID, code string) (*classroom.LiveQuizSession, bool, error) {
	if code == "" {
		return nil, false, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best *classroom.LiveQuizSession
	for _, s := range r.by {
		if s.TenantID != tenantID || s.JoinCode != code {
			continue
		}
		if s.State != classroom.LiveQuizSessionStateArmed && s.State != classroom.LiveQuizSessionStateLive {
			continue
		}
		if best == nil || strings.Compare(s.ID, best.ID) > 0 {
			best = s
		}
	}
	return best, best != nil, nil
}

// Mutate runs fn against the stored aggregate under the repo write-lock so
// concurrent submits/joins serialise (the inmem analogue of the pg
// SELECT … FOR UPDATE). fn errors propagate; unknown id returns
// classroom.ErrSessionNotFound.
func (r *ClassroomSessionRepo) Mutate(id string, fn func(*classroom.LiveQuizSession) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.by[id]
	if !ok || s == nil {
		return classroom.ErrSessionNotFound
	}
	return fn(s)
}

// ListByTenant returns sessions for a tenant sorted by ID (UUIDv7 ⇒
// creation order). The returned slice is a fresh copy — safe for the
// caller to mutate without affecting the store.
func (r *ClassroomSessionRepo) ListByTenant(tenantID string) []*classroom.LiveQuizSession {
	r.mu.RLock()
	out := make([]*classroom.LiveQuizSession, 0, len(r.by))
	for _, s := range r.by {
		if s.TenantID != tenantID {
			continue
		}
		out = append(out, s)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out
}
