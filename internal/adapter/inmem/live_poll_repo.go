// live_poll_repo.go — in-memory repository for the LivePoll aggregate
// (R+ Stage C-lite Wave-6 M11 — live-poll WS fan-out wiring).
//
// Hexagonal layout: adapter depends on the pure domain package
// internal/domain/classroom (the domain layer never imports this file).
//
// Add-only: collides with no existing repo. The R+ live-poll track lives
// entirely in this file so parallel domain edits don't conflict.
//
// Production (M12+) will swap in a Postgres adapter against chora_delivery
// via PgBouncer; the handler signatures stay stable.
//
// Per feedback_no_stubs_real_wiring: when Deps.LivePolls is nil the WS
// handler returns 503 — this repo is the real wiring (not a stub).
package inmem

import (
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// LivePollRepo is an in-memory store for LivePoll aggregates, keyed by
// poll id (UUIDv7). Concurrent-safe via RWMutex.
type LivePollRepo struct {
	mu sync.RWMutex
	by map[string]*classroom.LivePoll
}

// NewLivePollRepo returns an empty repo.
func NewLivePollRepo() *LivePollRepo {
	return &LivePollRepo{by: make(map[string]*classroom.LivePoll)}
}

// Compile-time assertion: satisfies the domain port (ADR-168 gap-1b).
var _ classroom.PollStore = (*LivePollRepo)(nil)

// Save inserts or upserts a poll by ID. No-op when p is nil. Returns an error
// to satisfy classroom.PollStore (the pg adapter fails loud on a dropped Save);
// the in-memory impl never errors.
func (r *LivePollRepo) Save(p *classroom.LivePoll) error {
	if p == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[p.ID] = p
	return nil
}

// Get returns a poll by ID and a bool ok flag.
func (r *LivePollRepo) Get(id string) (*classroom.LivePoll, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.by[id]
	if !ok {
		return nil, false, nil
	}
	return p, true, nil
}

// ListByTenant returns polls for a tenant sorted by ID (UUIDv7 ⇒
// creation order). Returned slice is a fresh copy.
func (r *LivePollRepo) ListByTenant(tenantID string) []*classroom.LivePoll {
	r.mu.RLock()
	out := make([]*classroom.LivePoll, 0, len(r.by))
	for _, p := range r.by {
		if p.TenantID != tenantID {
			continue
		}
		out = append(out, p)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out
}
