// Package attendance_reconcile owns post-hoc attendance reconciliation for
// the Classroom Experience inside chora-delivery.
//
// Per CHO-13 + docs/design/ux_classroom_experience.md edge-case rows
// ("learner joins mid-session", "instructor accidentally ends session
// early") this aggregate handles late-arrival classification + post-class
// reconciliation. It sits NEXT TO (not inside) the existing
// internal/domain/attendance package — that one owns real-time QR + manual
// recording; this one owns the post-hoc adjust-and-finalize step.
//
// Concept mapping:
//   - On-time arrival (within grace window) → VerdictPresent
//   - Late arrival (after grace, before class end) → VerdictLate
//   - Arrival after class end → VerdictAbsent
//
// State machine: Open → Reconciling → Finalized.
// Open→Finalized is permitted as a shortcut (no adjustments needed).
//
// Hexagonal: pure domain. No HTTP, no persistence.
package attendance_reconcile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrInvalidTransition = errors.New("invalid state transition")
)

// -----------------------------------------------------------------------------
// Verdict + State enums
// -----------------------------------------------------------------------------

// Verdict is the per-learner reconciliation outcome.
type Verdict string

const (
	// VerdictPresent — arrived within grace window (or before class).
	VerdictPresent Verdict = "present"
	// VerdictLate — arrived after grace but before class ended.
	VerdictLate Verdict = "late"
	// VerdictAbsent — arrived after class ended (or never arrived).
	VerdictAbsent Verdict = "absent"
)

// State is the reconciliation lifecycle.
type State string

const (
	// StateOpen — class is in flight; adjustments accepted in real-time.
	StateOpen State = "open"
	// StateReconciling — class ended; instructor reviewing roster.
	StateReconciling State = "reconciling"
	// StateFinalized — terminal; record locked.
	StateFinalized State = "finalized"
)

// -----------------------------------------------------------------------------
// Adjustment — per-learner reconciliation row
// -----------------------------------------------------------------------------

// Adjustment is one learner's reconciled attendance.
type Adjustment struct {
	GCID      string
	ArrivedAt time.Time
	Verdict   Verdict
	Note      string
	UpdatedAt time.Time
}

// -----------------------------------------------------------------------------
// Summary — output of Summary()
// -----------------------------------------------------------------------------

// Summary is the aggregate counts after reconciliation.
type Summary struct {
	Total   int
	Present int
	Late    int
	Absent  int
}

// -----------------------------------------------------------------------------
// Reconciler aggregate root
// -----------------------------------------------------------------------------

// Reconciler is the post-class reconciliation aggregate, keyed by
// (tenant, class).
type Reconciler struct {
	ID          string
	TenantID    string
	ClassID     string
	StartsAt    time.Time
	EndsAt      time.Time
	Grace       time.Duration
	State       State
	FinalizedAt *time.Time
	adj         map[string]*Adjustment // by gcid
	CreatedAt   time.Time
	UpdatedAt   time.Time

	mu sync.Mutex
}

// NewReconciler constructs a Reconciler in StateOpen.
func NewReconciler(tenantID, classID string, starts, ends time.Time, grace time.Duration) (*Reconciler, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(classID) == "" {
		return nil, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	if !ends.After(starts) {
		return nil, fmt.Errorf("%w: ends_at must be after starts_at", ErrInvalidArgument)
	}
	if grace < 0 {
		return nil, fmt.Errorf("%w: grace must be >= 0", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Reconciler{
		ID:        NewUUIDv7(),
		TenantID:  tenantID,
		ClassID:   classID,
		StartsAt:  starts.UTC(),
		EndsAt:    ends.UTC(),
		Grace:     grace,
		State:     StateOpen,
		adj:       make(map[string]*Adjustment),
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Classify returns the Verdict for an arrival timestamp.
//
// Boundaries (all inclusive on the present-side):
//   - arrival ≤ StartsAt+Grace → Present
//   - StartsAt+Grace < arrival ≤ EndsAt → Late
//   - arrival > EndsAt → Absent.
func (r *Reconciler) Classify(arrival time.Time) Verdict {
	graceEnd := r.StartsAt.Add(r.Grace)
	if !arrival.After(graceEnd) {
		return VerdictPresent
	}
	if !arrival.After(r.EndsAt) {
		return VerdictLate
	}
	return VerdictAbsent
}

// AdjustLateArrival records (or updates) a per-learner reconciliation row.
// Same-gcid second call is an upsert (preserves first-call CreatedAt
// implicitly via UpdatedAt always being current).
func (r *Reconciler) AdjustLateArrival(gcid string, arrivedAt time.Time, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.State == StateFinalized {
		return fmt.Errorf("%w: AdjustLateArrival from %s not permitted", ErrInvalidTransition, r.State)
	}
	if strings.TrimSpace(gcid) == "" {
		return fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	verdict := r.Classify(arrivedAt)
	now := time.Now().UTC()
	r.adj[gcid] = &Adjustment{
		GCID:      gcid,
		ArrivedAt: arrivedAt.UTC(),
		Verdict:   verdict,
		Note:      note,
		UpdatedAt: now,
	}
	r.UpdatedAt = now
	return nil
}

// BeginReconciling transitions Open → Reconciling.
func (r *Reconciler) BeginReconciling() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.State != StateOpen {
		return fmt.Errorf("%w: BeginReconciling requires Open, got %s", ErrInvalidTransition, r.State)
	}
	r.State = StateReconciling
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// Finalize transitions {Open, Reconciling} → Finalized.
func (r *Reconciler) Finalize() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.State == StateFinalized {
		return fmt.Errorf("%w: already Finalized", ErrInvalidTransition)
	}
	stamp := time.Now().UTC()
	r.FinalizedAt = &stamp
	r.State = StateFinalized
	r.UpdatedAt = stamp
	return nil
}

// Adjustments returns a snapshot copy of all per-learner rows.
func (r *Reconciler) Adjustments() []*Adjustment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Adjustment, 0, len(r.adj))
	for _, a := range r.adj {
		out = append(out, a)
	}
	return out
}

// Summary returns aggregate counts.
func (r *Reconciler) Summary() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Summary{}
	for _, a := range r.adj {
		s.Total++
		switch a.Verdict {
		case VerdictPresent:
			s.Present++
		case VerdictLate:
			s.Late++
		case VerdictAbsent:
			s.Absent++
		}
	}
	return s
}

// -----------------------------------------------------------------------------
// UUIDv7 — local generator (deps-free)
// -----------------------------------------------------------------------------

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7.
func NewUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
