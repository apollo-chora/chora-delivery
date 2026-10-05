package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

// RoomRepo stores Room aggregates, scoped by tenant. Drop-in for pg.RoomRepo at
// cmd/server wiring (dev/single-pod). CHO-2191 SP1.
type RoomRepo struct {
	mu sync.RWMutex
	by map[string]*campusops.Room // room_id -> *Room
}

// NewRoomRepo returns an empty repo.
func NewRoomRepo() *RoomRepo {
	return &RoomRepo{by: make(map[string]*campusops.Room)}
}

// Compile-time assertion: satisfies the domain port.
var _ campusops.RoomStore = (*RoomRepo)(nil)

// Save upserts a room. ctx is accepted (the pg adapter uses it for RLS); the
// in-memory store ignores it. Never errors.
func (r *RoomRepo) Save(_ context.Context, rm *campusops.Room) error {
	if rm == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[rm.ID] = rm
	return nil
}

// GetForTenant returns a room by id, scoped to tenantID. ok=false on absent /
// soft-deleted / other-tenant row (a genuine miss); never errors.
func (r *RoomRepo) GetForTenant(_ context.Context, tenantID, id string) (*campusops.Room, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rm, ok := r.by[id]
	if !ok || rm.DeletedAt != nil || rm.TenantID != tenantID {
		return nil, false, nil
	}
	return rm, true, nil
}

// ListByTenant returns a tenant's non-soft-deleted rooms, ordered by CreatedAt
// then ID (UUIDv7 ⇒ creation-order tiebreak).
func (r *RoomRepo) ListByTenant(_ context.Context, tenantID string) ([]*campusops.Room, error) {
	r.mu.RLock()
	out := make([]*campusops.Room, 0, len(r.by))
	for _, rm := range r.by {
		if rm.TenantID != tenantID || rm.DeletedAt != nil {
			continue
		}
		out = append(out, rm)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out, nil
}
