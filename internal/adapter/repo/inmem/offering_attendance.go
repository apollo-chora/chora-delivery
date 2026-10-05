package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	offeringattendance "github.com/apollo-chora/chora-delivery/internal/domain/offering_attendance"
)

// OfferingAttendanceRepo stores session attendance marks, keyed by the natural
// key (tenant|session|gcid) so a re-mark upserts (latest wins). Drop-in for
// pg.OfferingAttendanceRepo at cmd/server wiring.
type OfferingAttendanceRepo struct {
	mu    sync.RWMutex
	byKey map[string]*offeringattendance.Record
}

// NewOfferingAttendanceRepo returns an empty repo.
func NewOfferingAttendanceRepo() *OfferingAttendanceRepo {
	return &OfferingAttendanceRepo{byKey: make(map[string]*offeringattendance.Record)}
}

// Compile-time assertion: satisfies the domain port.
var _ offeringattendance.Store = (*OfferingAttendanceRepo)(nil)

func natKey(tenantID, sessionID, gcid string) string {
	return tenantID + "|" + sessionID + "|" + gcid
}

// Upsert records a mark idempotently on (tenant, session, gcid): a re-mark
// replaces the prior mark (latest wins). ctx ignored (pg uses it for RLS).
func (r *OfferingAttendanceRepo) Upsert(_ context.Context, rec *offeringattendance.Record) error {
	if rec == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKey[natKey(rec.TenantID, rec.SessionID, rec.GCID)] = rec
	return nil
}

// ListBySession returns a tenant's marks for one session, ordered by gcid.
func (r *OfferingAttendanceRepo) ListBySession(_ context.Context, tenantID, sessionID string) ([]*offeringattendance.Record, error) {
	r.mu.RLock()
	out := make([]*offeringattendance.Record, 0, len(r.byKey))
	for _, rec := range r.byKey {
		if rec.TenantID != tenantID || rec.SessionID != sessionID {
			continue
		}
		out = append(out, rec)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].GCID, out[j].GCID) < 0 })
	return out, nil
}
