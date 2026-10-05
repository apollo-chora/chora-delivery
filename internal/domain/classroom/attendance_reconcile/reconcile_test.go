// Package attendance_reconcile_test pins the contract for late-arrival +
// post-class attendance reconciliation per CHO-13 + docs/design/
// ux_classroom_experience.md edge cases (learner joins mid-session;
// projector disconnect; instructor accidentally ends).
//
// The reconciliation aggregate sits NEXT TO (not inside) the existing
// internal/domain/attendance package — it owns post-hoc adjustments, not
// real-time recording.
//
// Tests written FIRST per .claude/rules/development-execution.md TDD.
package attendance_reconcile_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom/attendance_reconcile"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	classA  = "01970000-0000-7000-7100-000000000001"
	gcid1   = "01970000-0000-7000-A000-000000000001"
	gcid2   = "01970000-0000-7000-A000-000000000002"
	gcid3   = "01970000-0000-7000-A000-000000000003"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// -----------------------------------------------------------------------------
// NewReconciler — guards
// -----------------------------------------------------------------------------

func TestNewReconciler_TableDriven(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	cases := []struct {
		name    string
		tenant  string
		class   string
		starts  time.Time
		ends    time.Time
		grace   time.Duration
		wantErr error
	}{
		{"happy", tenantA, classA, starts, ends, 15 * time.Minute, nil},
		{"missing tenant", "", classA, starts, ends, 15 * time.Minute, attendance_reconcile.ErrInvalidArgument},
		{"missing class", tenantA, "", starts, ends, 15 * time.Minute, attendance_reconcile.ErrInvalidArgument},
		{"end before start", tenantA, classA, ends, starts, 15 * time.Minute, attendance_reconcile.ErrInvalidArgument},
		{"negative grace", tenantA, classA, starts, ends, -1 * time.Minute, attendance_reconcile.ErrInvalidArgument},
		{"zero grace ok", tenantA, classA, starts, ends, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := attendance_reconcile.NewReconciler(c.tenant, c.class, c.starts, c.ends, c.grace)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got %v want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if r.State != attendance_reconcile.StateOpen {
				t.Errorf("expected Open, got %s", r.State)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Late-arrival classification: present / late / absent
// -----------------------------------------------------------------------------

func TestClassify_PresentLateAbsent(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	r, err := attendance_reconcile.NewReconciler(tenantA, classA, starts, ends, 15*time.Minute)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	cases := []struct {
		name    string
		arrival time.Time
		want    attendance_reconcile.Verdict
	}{
		{"on time", starts, attendance_reconcile.VerdictPresent},
		{"5 min early ok", starts.Add(-5 * time.Minute), attendance_reconcile.VerdictPresent},
		{"5 min after = present (within grace)", starts.Add(5 * time.Minute), attendance_reconcile.VerdictPresent},
		{"15 min after = present (boundary)", starts.Add(15 * time.Minute), attendance_reconcile.VerdictPresent},
		{"16 min after = late", starts.Add(16 * time.Minute), attendance_reconcile.VerdictLate},
		{"60 min after still late", starts.Add(60 * time.Minute), attendance_reconcile.VerdictLate},
		{"after class end = absent", ends.Add(time.Minute), attendance_reconcile.VerdictAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Classify(c.arrival)
			if got != c.want {
				t.Errorf("got %s want %s", got, c.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AdjustLateArrival — instructor adds a learner mid-class
// -----------------------------------------------------------------------------

func TestAdjustLateArrival_Idempotent(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	r, _ := attendance_reconcile.NewReconciler(tenantA, classA, starts, ends, 15*time.Minute)

	// First adjustment
	if err := r.AdjustLateArrival(gcid1, starts.Add(20*time.Minute), "doctor's note"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if got := len(r.Adjustments()); got != 1 {
		t.Fatalf("len=%d want 1", got)
	}

	// Second call same gcid — overwrites (preserves first arrival but
	// updates note + verdict)
	if err := r.AdjustLateArrival(gcid1, starts.Add(25*time.Minute), "updated note"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := len(r.Adjustments()); got != 1 {
		t.Errorf("len after re-adjust=%d want 1", got)
	}

	// Different gcid — separate row
	if err := r.AdjustLateArrival(gcid2, starts.Add(60*time.Minute), "transit delay"); err != nil {
		t.Fatalf("g2: %v", err)
	}
	if got := len(r.Adjustments()); got != 2 {
		t.Errorf("len=%d want 2", got)
	}

	// Empty gcid rejected
	if err := r.AdjustLateArrival("", starts, ""); !errors.Is(err, attendance_reconcile.ErrInvalidArgument) {
		t.Errorf("empty gcid: got %v", err)
	}
}

func TestAdjustLateArrival_AfterCloseRejected(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	r, _ := attendance_reconcile.NewReconciler(tenantA, classA, starts, ends, 15*time.Minute)

	if err := r.Finalize(); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Cannot adjust after Finalize
	if err := r.AdjustLateArrival(gcid1, starts.Add(20*time.Minute), "n"); !errors.Is(err, attendance_reconcile.ErrInvalidTransition) {
		t.Errorf("got %v want ErrInvalidTransition", err)
	}
}

// -----------------------------------------------------------------------------
// State machine: Open → Reconciling → Finalized (+ Reopen for grace)
// -----------------------------------------------------------------------------

func TestStateMachine(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	r, _ := attendance_reconcile.NewReconciler(tenantA, classA, starts, ends, 15*time.Minute)

	if r.State != attendance_reconcile.StateOpen {
		t.Errorf("init: got %s want Open", r.State)
	}

	// Open → Reconciling
	if err := r.BeginReconciling(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if r.State != attendance_reconcile.StateReconciling {
		t.Errorf("got %s want Reconciling", r.State)
	}

	// Re-Begin from Reconciling rejected
	if err := r.BeginReconciling(); !errors.Is(err, attendance_reconcile.ErrInvalidTransition) {
		t.Errorf("re-begin: got %v", err)
	}

	// Reconciling → Finalized
	if err := r.Finalize(); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if r.State != attendance_reconcile.StateFinalized {
		t.Errorf("got %s want Finalized", r.State)
	}

	// Re-Finalize rejected
	if err := r.Finalize(); !errors.Is(err, attendance_reconcile.ErrInvalidTransition) {
		t.Errorf("re-finalize: got %v", err)
	}
}

func TestFinalizeFromOpenAlsoOK(t *testing.T) {
	// Edge case: instructor finalizes without entering Reconciling step
	// (e.g., perfect attendance — no adjustments needed). Open→Finalized
	// is a permitted shortcut.
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	r, _ := attendance_reconcile.NewReconciler(tenantA, classA, starts, ends, 15*time.Minute)

	if err := r.Finalize(); err != nil {
		t.Fatalf("open→finalize shortcut: %v", err)
	}
	if r.State != attendance_reconcile.StateFinalized {
		t.Errorf("got %s want Finalized", r.State)
	}
}

// -----------------------------------------------------------------------------
// Summary — totals after reconcile
// -----------------------------------------------------------------------------

func TestSummary(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	r, _ := attendance_reconcile.NewReconciler(tenantA, classA, starts, ends, 15*time.Minute)

	_ = r.AdjustLateArrival(gcid1, starts, "on time")                        // present
	_ = r.AdjustLateArrival(gcid2, starts.Add(20*time.Minute), "5min late")  // late
	_ = r.AdjustLateArrival(gcid3, ends.Add(time.Minute), "missed entirely") // absent

	s := r.Summary()
	if s.Present != 1 {
		t.Errorf("present: got %d want 1", s.Present)
	}
	if s.Late != 1 {
		t.Errorf("late: got %d want 1", s.Late)
	}
	if s.Absent != 1 {
		t.Errorf("absent: got %d want 1", s.Absent)
	}
	if s.Total != 3 {
		t.Errorf("total: got %d want 3", s.Total)
	}
}
