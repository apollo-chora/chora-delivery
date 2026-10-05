// courseprogress_test.go - RED-first specification of the CourseLearnerProgress
// aggregate: the per-learner, per-COURSE self-paced traversal projection that
// backs the R+ ASYNC Analytics tab (R+ Four-Mode DoD §10.3 step 3, epic CHO-1827).
//
// Every assertion is on the EFFECT (the projected counts / flags / stamps), never
// on a call shape - a test pinned to today's call graph would pass over a
// projection that stores the wrong number.
package courseprogress_test

import (
	"testing"
	"time"

	cp "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

const (
	cpTenant = "11111111-1111-7111-8111-111111111111"
	cpGCID   = "00000000-0000-7000-9000-0000000000a1"
	cpCourse = "01985e7f-5555-7abc-8def-000000000c01"
	cpPath   = "01985e7f-5555-7abc-8def-000000000d01"
)

var cpT0 = time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)

func mustNew(t *testing.T) *cp.CourseLearnerProgress {
	t.Helper()
	p, err := cp.New(cp.NewParams{TenantID: cpTenant, GCID: cpGCID, CourseID: cpCourse, PathID: cpPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// -----------------------------------------------------------------------------
// Constructor guards
// -----------------------------------------------------------------------------

func TestNew_RequiresTenantGCIDCourse(t *testing.T) {
	cases := []struct {
		name string
		in   cp.NewParams
	}{
		{"no tenant", cp.NewParams{GCID: cpGCID, CourseID: cpCourse}},
		{"no gcid", cp.NewParams{TenantID: cpTenant, CourseID: cpCourse}},
		{"no course", cp.NewParams{TenantID: cpTenant, GCID: cpGCID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := cp.New(c.in); err == nil {
				t.Fatalf("New(%+v): want error, got nil", c.in)
			}
		})
	}
}

func TestNew_StartsAtZeroAndIncomplete(t *testing.T) {
	p := mustNew(t)
	if p.CompletedAtoms != 0 || p.TotalAtoms != 0 {
		t.Fatalf("counts: want 0/0, got %d/%d", p.CompletedAtoms, p.TotalAtoms)
	}
	if p.IsComplete || p.CompletedAt != nil {
		t.Fatalf("fresh projection must be incomplete, got IsComplete=%v CompletedAt=%v", p.IsComplete, p.CompletedAt)
	}
	if p.ID == "" {
		t.Fatal("ID must be assigned")
	}
}

// -----------------------------------------------------------------------------
// RecordAdvance
// -----------------------------------------------------------------------------

func TestRecordAdvance_StoresCounts(t *testing.T) {
	p := mustNew(t)
	changed, err := p.RecordAdvance(3, 10, cpT0)
	if err != nil {
		t.Fatalf("RecordAdvance: %v", err)
	}
	if !changed {
		t.Fatal("changed: want true on first advance")
	}
	if p.CompletedAtoms != 3 || p.TotalAtoms != 10 {
		t.Fatalf("counts: want 3/10, got %d/%d", p.CompletedAtoms, p.TotalAtoms)
	}
	if got := p.ProgressFraction(); got != 0.3 {
		t.Fatalf("ProgressFraction: want 0.3, got %v", got)
	}
}

// A redelivery of the SAME event must not change state (at-least-once delivery).
func TestRecordAdvance_IdempotentRedelivery(t *testing.T) {
	p := mustNew(t)
	if _, err := p.RecordAdvance(3, 10, cpT0); err != nil {
		t.Fatalf("first: %v", err)
	}
	changed, err := p.RecordAdvance(3, 10, cpT0)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if changed {
		t.Fatal("changed: want false on identical redelivery")
	}
	if p.CompletedAtoms != 3 || p.TotalAtoms != 10 {
		t.Fatalf("counts must be unchanged, got %d/%d", p.CompletedAtoms, p.TotalAtoms)
	}
}

// Pub/Sub is UNORDERED: an older advance arriving after a newer one must NOT
// rewind the projection. Assert the stored counts, not the return value alone.
func TestRecordAdvance_StaleEventDoesNotRewind(t *testing.T) {
	p := mustNew(t)
	if _, err := p.RecordAdvance(7, 10, cpT0.Add(time.Minute)); err != nil {
		t.Fatalf("newer: %v", err)
	}
	changed, err := p.RecordAdvance(2, 10, cpT0) // older producer clock
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	if changed {
		t.Fatal("changed: want false for a stale event")
	}
	if p.CompletedAtoms != 7 {
		t.Fatalf("stale event REWOUND progress: want 7, got %d", p.CompletedAtoms)
	}
}

// The atom list can grow retroactively (AppendAtom), so the denominator may rise
// and the fraction may legitimately fall. That is a real update, not a rewind.
func TestRecordAdvance_TotalMayGrow(t *testing.T) {
	p := mustNew(t)
	if _, err := p.RecordAdvance(5, 10, cpT0); err != nil {
		t.Fatalf("first: %v", err)
	}
	changed, err := p.RecordAdvance(5, 20, cpT0.Add(time.Minute))
	if err != nil {
		t.Fatalf("grow: %v", err)
	}
	if !changed {
		t.Fatal("changed: want true when the denominator grows")
	}
	if p.TotalAtoms != 20 {
		t.Fatalf("TotalAtoms: want 20, got %d", p.TotalAtoms)
	}
	if got := p.ProgressFraction(); got != 0.25 {
		t.Fatalf("ProgressFraction: want 0.25 after the bar rose, got %v", got)
	}
}

func TestRecordAdvance_RejectsInvalidCounts(t *testing.T) {
	cases := []struct {
		name             string
		completed, total int
	}{
		{"negative completed", -1, 10},
		{"negative total", 3, -1},
		{"completed exceeds total", 11, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := mustNew(t)
			if _, err := p.RecordAdvance(c.completed, c.total, cpT0); err == nil {
				t.Fatalf("RecordAdvance(%d,%d): want error, got nil", c.completed, c.total)
			}
		})
	}
}

func TestRecordAdvance_RefusesOnSoftDeleted(t *testing.T) {
	p := mustNew(t)
	p.SoftDelete()
	if _, err := p.RecordAdvance(1, 10, cpT0); err == nil {
		t.Fatal("RecordAdvance on a soft-deleted projection: want error, got nil")
	}
}

// -----------------------------------------------------------------------------
// ProgressFraction
// -----------------------------------------------------------------------------

func TestProgressFraction_ZeroTotalIsZero(t *testing.T) {
	p := mustNew(t)
	if got := p.ProgressFraction(); got != 0 {
		t.Fatalf("ProgressFraction with no atoms: want 0, got %v", got)
	}
}

// advanced.v1 and completed.v1 are separate messages with NO relative ordering.
// A completion that lands before the advance that would have filled the counters
// must still read as fully traversed, or a finished learner drags the cohort
// average down until the broker happens to deliver the other message.
func TestProgressFraction_CompleteIsFullEvenWhenCountsLag(t *testing.T) {
	p := mustNew(t)
	if _, err := p.RecordCompletion(cpT0); err != nil {
		t.Fatalf("RecordCompletion: %v", err)
	}
	if got := p.ProgressFraction(); got != 1.0 {
		t.Fatalf("ProgressFraction of a COMPLETE path: want 1.0, got %v", got)
	}
}

// -----------------------------------------------------------------------------
// RecordCompletion
// -----------------------------------------------------------------------------

func TestRecordCompletion_SetsFlagAndStamp(t *testing.T) {
	p := mustNew(t)
	changed, err := p.RecordCompletion(cpT0)
	if err != nil {
		t.Fatalf("RecordCompletion: %v", err)
	}
	if !changed {
		t.Fatal("changed: want true on first completion")
	}
	if !p.IsComplete {
		t.Fatal("IsComplete: want true")
	}
	if p.CompletedAt == nil || !p.CompletedAt.Equal(cpT0) {
		t.Fatalf("CompletedAt: want %v, got %v", cpT0, p.CompletedAt)
	}
}

func TestRecordCompletion_Idempotent(t *testing.T) {
	p := mustNew(t)
	if _, err := p.RecordCompletion(cpT0); err != nil {
		t.Fatalf("first: %v", err)
	}
	changed, err := p.RecordCompletion(cpT0.Add(time.Hour))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if changed {
		t.Fatal("changed: want false on a repeat completion")
	}
	if !p.CompletedAt.Equal(cpT0) {
		t.Fatalf("CompletedAt must keep the FIRST stamp, got %v", p.CompletedAt)
	}
}

// A path completion is terminal (consumption stamps CompletedAt once). A later
// advance must never un-complete it.
func TestRecordCompletion_StickyAcrossLaterAdvance(t *testing.T) {
	p := mustNew(t)
	if _, err := p.RecordCompletion(cpT0); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := p.RecordAdvance(5, 20, cpT0.Add(time.Hour)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !p.IsComplete {
		t.Fatal("a later advance UN-COMPLETED a completed path")
	}
}

func TestRecordCompletion_RefusesOnSoftDeleted(t *testing.T) {
	p := mustNew(t)
	p.SoftDelete()
	if _, err := p.RecordCompletion(cpT0); err == nil {
		t.Fatal("RecordCompletion on a soft-deleted projection: want error, got nil")
	}
}
