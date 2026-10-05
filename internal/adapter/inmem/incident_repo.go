// incident_repo.go — in-memory store for the IncidentReport aggregate
// (W4 Brick-B). APPEND-ONLY: only Append + reads, never update/delete. Adapter
// depends on the pure domain package internal/domain/exam. Production wires
// pg.IncidentRepo (chora_delivery.incident_reports, migration 0048).
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// IncidentRepo is an in-memory append-only store for IncidentReport records,
// keyed by id.
type IncidentRepo struct {
	mu sync.RWMutex
	by map[string]*exam.IncidentReport
}

// NewIncidentRepo returns an empty repo.
func NewIncidentRepo() *IncidentRepo {
	return &IncidentRepo{by: make(map[string]*exam.IncidentReport)}
}

// Compile-time assertion: satisfies the domain port.
var _ exam.IncidentReportStore = (*IncidentRepo)(nil)

// Append inserts a new immutable record. A duplicate id is a programming error
// (UUIDv7 is fresh per record); the append is idempotent on id.
func (r *IncidentRepo) Append(_ context.Context, ir *exam.IncidentReport) error {
	if ir == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[ir.ID] = ir
	return nil
}

// Get resolves a record by id.
func (r *IncidentRepo) Get(_ context.Context, id string) (*exam.IncidentReport, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ir, ok := r.by[id]
	return ir, ok, nil
}

// ListBySitting returns the incident records for a (tenant, sitting), sorted by
// ID (UUIDv7 ⇒ chronological append order) — the audit trail.
func (r *IncidentRepo) ListBySitting(_ context.Context, tenantID, sittingID string) ([]*exam.IncidentReport, error) {
	r.mu.RLock()
	out := make([]*exam.IncidentReport, 0, len(r.by))
	for _, ir := range r.by {
		if ir.TenantID != tenantID || ir.SittingID != sittingID {
			continue
		}
		out = append(out, ir)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].ID, out[j].ID) < 0 })
	return out, nil
}
