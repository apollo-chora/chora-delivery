package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

// OfferingSessionRepo stores OfferingSession aggregates, scoped by tenant.
// Drop-in for pg.OfferingSessionRepo at cmd/server wiring (dev/single-pod).
type OfferingSessionRepo struct {
	mu sync.RWMutex
	by map[string]*offeringsession.OfferingSession // session_id -> *OfferingSession
}

// NewOfferingSessionRepo returns an empty repo.
func NewOfferingSessionRepo() *OfferingSessionRepo {
	return &OfferingSessionRepo{by: make(map[string]*offeringsession.OfferingSession)}
}

// Compile-time assertion: satisfies the domain port.
var _ offeringsession.Store = (*OfferingSessionRepo)(nil)

// Save upserts a session. ctx is accepted (the pg adapter uses it for RLS);
// the in-memory store ignores it. Never errors.
func (r *OfferingSessionRepo) Save(_ context.Context, s *offeringsession.OfferingSession) error {
	if s == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Mirror the DB EXCLUDE gate (mig 0052, ratified CHO-2191): no OTHER active
	// session shares this tenant+room_id over an overlapping window. Roomless
	// (blank room_id) / soft-deleted are exempt. The DB constraint is the source
	// of truth; this keeps the dev/test adapter shape-parity so the handler's 409
	// is testable. Keyed on room_id — NOT the free-text name (slice-1's key).
	if strings.TrimSpace(s.RoomID) != "" && s.DeletedAt == nil {
		for id, ex := range r.by {
			if id == s.ID || ex.DeletedAt != nil {
				continue
			}
			if ex.TenantID == s.TenantID && ex.RoomID == s.RoomID && overlaps(ex.StartsAt, ex.EndsAt, s.StartsAt, s.EndsAt) {
				return offeringsession.ErrRoomDoubleBooked
			}
		}
	}
	r.by[s.ID] = s
	return nil
}

// overlaps reports whether the half-open intervals [aStart,aEnd) and
// [bStart,bEnd) intersect — abutting sessions (aEnd==bStart) do NOT overlap,
// matching Postgres tstzrange('[)') '&&'.
func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// Get returns a session by ID and ok flag.
func (r *OfferingSessionRepo) Get(_ context.Context, id string) (*offeringsession.OfferingSession, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.by[id]
	return s, ok, nil
}

// ListByOffering returns non-soft-deleted sessions for (tenant, offering),
// ordered by StartsAt then ID (UUIDv7 ⇒ creation order tiebreak).
func (r *OfferingSessionRepo) ListByOffering(_ context.Context, tenantID, offeringID string) ([]*offeringsession.OfferingSession, error) {
	r.mu.RLock()
	out := make([]*offeringsession.OfferingSession, 0, len(r.by))
	for _, s := range r.by {
		if s.TenantID != tenantID || s.OfferingID != offeringID || s.DeletedAt != nil {
			continue
		}
		out = append(out, s)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartsAt.Equal(out[j].StartsAt) {
			return out[i].StartsAt.Before(out[j].StartsAt)
		}
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out, nil
}
