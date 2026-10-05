// Package inmem (repo/inmem) — Roster repository.
package inmem

import (
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

// RosterRepo stores Roster aggregates indexed by class_id (1:1 with class).
type RosterRepo struct {
	mu      sync.RWMutex
	byClass map[string]*rostering.Roster // class_id -> *Roster
	byID    map[string]*rostering.Roster // roster_id -> *Roster
}

// NewRosterRepo returns an empty repo.
func NewRosterRepo() *RosterRepo {
	return &RosterRepo{
		byClass: make(map[string]*rostering.Roster),
		byID:    make(map[string]*rostering.Roster),
	}
}

// Save inserts or upserts a roster.
func (r *RosterRepo) Save(roster *rostering.Roster) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byClass[roster.ClassID] = roster
	r.byID[roster.ID] = roster
}

// GetByClass returns the roster (if any) for a class.
func (r *RosterRepo) GetByClass(classID string) (*rostering.Roster, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rs, ok := r.byClass[classID]
	return rs, ok
}

// Get returns the roster by id.
func (r *RosterRepo) Get(id string) (*rostering.Roster, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rs, ok := r.byID[id]
	return rs, ok
}
