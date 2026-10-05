// rollup_test.go - RED-first specification of the analytics roll-up that turns
// CourseLearnerProgress rows into the R+ ASYNC Analytics tab's avg_progress_pct
// and completion_rate (R+ Four-Mode DoD §10.3, epic CHO-1827).
//
// The two defects this file exists to prevent:
//
//  1. THE DENOMINATOR BUG. learning_path.advanced.v1 only fires once a learner
//     COMPLETES an atom, so an enrolled learner who never started has NO row.
//     Averaging over rows-that-exist would silently report the average of the
//     ACTIVE learners and call it the cohort's progress - a number that rises
//     when a struggling learner does nothing. The roster is the denominator.
//
//  2. THE FABRICATED ZERO. An offering with no enrolments has UNDEFINED average
//     progress, not 0%. "Nobody has made progress" and "there is nobody" are
//     different facts and must not render identically. nil → JSON null, exactly
//     as capacity_utilisation_pct already does when capacity is unbounded.
package courseprogress_test

import (
	"testing"
	"time"

	cp "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

// row builds a projection at a given progress for a learner (test fixture).
func row(t *testing.T, gcid string, completed, total int, complete bool) *cp.CourseLearnerProgress {
	t.Helper()
	p, err := cp.New(cp.NewParams{TenantID: cpTenant, GCID: gcid, CourseID: cpCourse, PathID: cpPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if total > 0 {
		if _, err := p.RecordAdvance(completed, total, cpT0); err != nil {
			t.Fatalf("RecordAdvance: %v", err)
		}
	}
	if complete {
		if _, err := p.RecordCompletion(cpT0); err != nil {
			t.Fatalf("RecordCompletion: %v", err)
		}
	}
	return p
}

func deref(t *testing.T, f *float64) float64 {
	t.Helper()
	if f == nil {
		t.Fatal("want a value, got nil")
	}
	return *f
}

// -----------------------------------------------------------------------------
// The denominator: enrolled learners, NOT rows-that-exist
// -----------------------------------------------------------------------------

// Two enrolled learners, only ONE has started (50%). The honest cohort average
// is 25% (50+0)/2, NOT 50% (the average over rows that happen to exist).
func TestRollCourse_UnstartedLearnerCountsAsZero(t *testing.T) {
	s := cp.RollCourse([]string{"g1", "g2"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 5, 10, false),
	})
	if s.Enrolled != 2 {
		t.Fatalf("Enrolled: want 2, got %d", s.Enrolled)
	}
	if got := deref(t, s.AvgProgressPct()); got != 25 {
		t.Fatalf("avg_progress_pct: want 25 (the unstarted learner counts as 0), got %v", got)
	}
}

func TestRollCourse_AveragesAcrossLearners(t *testing.T) {
	s := cp.RollCourse([]string{"g1", "g2", "g3", "g4"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 10, 10, true),
		row(t, "g2", 5, 10, false),
		row(t, "g3", 1, 10, false),
		row(t, "g4", 0, 10, false),
	})
	// (100 + 50 + 10 + 0) / 4 = 40
	if got := deref(t, s.AvgProgressPct()); got != 40 {
		t.Fatalf("avg_progress_pct: want 40, got %v", got)
	}
}

// A progress row for someone no longer on the roster (e.g. a since-removed
// enrolment) must not inflate the numerator past the denominator.
func TestRollCourse_IgnoresNonRosterRows(t *testing.T) {
	s := cp.RollCourse([]string{"g1"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 5, 10, false),
		row(t, "ghost", 10, 10, true), // not enrolled
	})
	if s.Enrolled != 1 {
		t.Fatalf("Enrolled: want 1, got %d", s.Enrolled)
	}
	if got := deref(t, s.AvgProgressPct()); got != 50 {
		t.Fatalf("avg_progress_pct: want 50 (ghost row ignored), got %v", got)
	}
	if got := deref(t, s.CompletionRatePct()); got != 0 {
		t.Fatalf("completion_rate: want 0 (ghost completion ignored), got %v", got)
	}
}

func TestRollCourse_SoftDeletedRowIgnored(t *testing.T) {
	deleted := row(t, "g1", 10, 10, true)
	deleted.SoftDelete()
	s := cp.RollCourse([]string{"g1", "g2"}, []*cp.CourseLearnerProgress{
		deleted,
		row(t, "g2", 5, 10, false),
	})
	// g1's row is soft-deleted ⇒ counts as 0; (0 + 50) / 2 = 25.
	if got := deref(t, s.AvgProgressPct()); got != 25 {
		t.Fatalf("avg_progress_pct: want 25 (soft-deleted row ignored), got %v", got)
	}
}

// -----------------------------------------------------------------------------
// Honest nulls: absence of learners is not 0% progress
// -----------------------------------------------------------------------------

func TestRollCourse_NoEnrolmentsIsNullNotZero(t *testing.T) {
	s := cp.RollCourse(nil, nil)
	if s.AvgProgressPct() != nil {
		t.Fatalf("avg_progress_pct: want nil (no enrolments ⇒ undefined), got %v", *s.AvgProgressPct())
	}
	if s.CompletionRatePct() != nil {
		t.Fatalf("completion_rate: want nil (no enrolments ⇒ undefined), got %v", *s.CompletionRatePct())
	}
}

// -----------------------------------------------------------------------------
// completion_rate
// -----------------------------------------------------------------------------

func TestRollCourse_CompletionRate(t *testing.T) {
	s := cp.RollCourse([]string{"g1", "g2", "g3", "g4"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 10, 10, true),
		row(t, "g2", 10, 10, true),
		row(t, "g3", 3, 10, false),
	})
	if got := deref(t, s.CompletionRatePct()); got != 50 {
		t.Fatalf("completion_rate: want 50 (2 of 4 enrolled), got %v", got)
	}
}

func TestRollCourse_CompletionRateZeroWhenNoneComplete(t *testing.T) {
	s := cp.RollCourse([]string{"g1", "g2"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 3, 10, false),
	})
	if got := deref(t, s.CompletionRatePct()); got != 0 {
		t.Fatalf("completion_rate: want 0 (enrolled but none complete), got %v", got)
	}
}

// -----------------------------------------------------------------------------
// Rounding + composition across an offering's courses
// -----------------------------------------------------------------------------

func TestRollCourse_RoundsToOneDecimal(t *testing.T) {
	// 1/3 of one learner ⇒ 33.333...% ⇒ 33.3
	s := cp.RollCourse([]string{"g1"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 1, 3, false),
	})
	if got := deref(t, s.AvgProgressPct()); got != 33.3 {
		t.Fatalf("avg_progress_pct: want 33.3, got %v", got)
	}
}

// An offering spans several courses; the offering-level number is the average
// over every (learner, course) enrolment, so per-course stats must compose.
func TestStats_AddComposesAcrossCourses(t *testing.T) {
	a := cp.RollCourse([]string{"g1", "g2"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 10, 10, true),
		row(t, "g2", 0, 10, false),
	})
	b := cp.RollCourse([]string{"g3", "g4"}, []*cp.CourseLearnerProgress{
		row(t, "g3", 5, 10, false),
		row(t, "g4", 5, 10, false),
	})
	total := a.Add(b)
	if total.Enrolled != 4 {
		t.Fatalf("Enrolled: want 4, got %d", total.Enrolled)
	}
	// (100 + 0 + 50 + 50) / 4 = 50
	if got := deref(t, total.AvgProgressPct()); got != 50 {
		t.Fatalf("avg_progress_pct: want 50, got %v", got)
	}
	// 1 of 4 complete = 25
	if got := deref(t, total.CompletionRatePct()); got != 25 {
		t.Fatalf("completion_rate: want 25, got %v", got)
	}
}

// A course with a roster but zero atoms yields 0% progress, which is a real
// measurement (the learner has completed none of nothing) - but it must never
// produce a NaN.
func TestRollCourse_ZeroAtomCourseIsNotNaN(t *testing.T) {
	s := cp.RollCourse([]string{"g1"}, []*cp.CourseLearnerProgress{
		row(t, "g1", 0, 0, false),
	})
	got := deref(t, s.AvgProgressPct())
	if got != got { // NaN check
		t.Fatal("avg_progress_pct is NaN")
	}
	if got != 0 {
		t.Fatalf("avg_progress_pct: want 0, got %v", got)
	}
}

var _ = time.Now // keep the time import honest across edits
