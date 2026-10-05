// rollup.go - the pure roll-up that turns CourseLearnerProgress rows into the R+
// ASYNC Analytics numbers (avg_progress_pct, completion_rate).
//
// Pure and I/O-free on purpose: this is the arithmetic the Analytics tab is
// judged on, so it is domain logic under the 85% domain gate, not something
// improvised inside an HTTP handler.
//
// # The two mistakes this file is shaped to refuse
//
//  1. THE DENOMINATOR. learning_path.advanced.v1 fires only when a learner
//     COMPLETES an atom, so an enrolled learner who never started HAS NO ROW.
//     Averaging over the rows that exist would silently measure only the learners
//     who are already doing well, and the cohort average would RISE as more
//     learners stalled. The roster is the denominator; a missing row is a real
//     0%, not an absent data point.
//
//  2. THE FABRICATED ZERO. Zero enrolments means the average is UNDEFINED, not
//     0%. "Nobody has made progress" and "there is nobody" must not render
//     identically, so the result is a nil pointer (→ JSON null), exactly as
//     capacity_utilisation_pct already does for an unbounded offering.
package courseprogress

import "math"

// Stats is one accumulation of progress over a set of enrolments. It composes
// (see Add) so an offering spanning several courses is the sum of its courses,
// with the division performed exactly once, at the end.
type Stats struct {
	// Enrolled is the number of (learner, course) enrolments measured - the
	// DENOMINATOR. Distinct per course; summed across courses.
	Enrolled int
	// Completed is how many of those enrolments finished the course.
	Completed int
	// ProgressSum is the sum of per-learner traversal fractions in [0.0, 1.0].
	// Kept as a sum (not an average) so Add stays associative.
	ProgressSum float64
}

// RollCourse accumulates one course's progress over its enrolled roster.
//
// enrolledGCIDs is the course's roster and is de-duplicated here: it is the
// denominator, and counting one learner twice would quietly halve the average.
// Rows for learners NOT on the roster are ignored - a stale projection for a
// removed enrolment must never push the numerator above the denominator (an
// average over 100% is how a nonsense metric announces itself too late).
// Soft-deleted rows are ignored for the same reason.
func RollCourse(enrolledGCIDs []string, rows []*CourseLearnerProgress) Stats {
	enrolled := make(map[string]bool, len(enrolledGCIDs))
	for _, g := range enrolledGCIDs {
		if g != "" {
			enrolled[g] = true
		}
	}

	// One fraction per enrolled learner. The DB holds at most one active row per
	// (tenant, gcid, course) via a partial UNIQUE index, but the map also makes
	// this correct if a caller ever passes duplicates.
	fraction := make(map[string]float64, len(enrolled))
	complete := make(map[string]bool, len(enrolled))
	for _, r := range rows {
		if r == nil || !r.IsActive() || !enrolled[r.GCID] {
			continue
		}
		fraction[r.GCID] = r.ProgressFraction()
		complete[r.GCID] = r.IsComplete
	}

	s := Stats{Enrolled: len(enrolled)}
	for g := range enrolled {
		s.ProgressSum += fraction[g] // absent ⇒ 0.0: enrolled but never started
		if complete[g] {
			s.Completed++
		}
	}
	return s
}

// Add returns the union of two accumulations, so an offering's number is the
// average over every (learner, course) enrolment it contains.
func (s Stats) Add(o Stats) Stats {
	return Stats{
		Enrolled:    s.Enrolled + o.Enrolled,
		Completed:   s.Completed + o.Completed,
		ProgressSum: s.ProgressSum + o.ProgressSum,
	}
}

// AvgProgressPct is mean traversal across enrolments, 0-100, rounded to 1dp.
// nil (→ JSON null) when there are no enrolments: undefined, not zero.
func (s Stats) AvgProgressPct() *float64 {
	if s.Enrolled <= 0 {
		return nil
	}
	return round1(100 * s.ProgressSum / float64(s.Enrolled))
}

// CompletionRatePct is the share of enrolments that finished, 0-100, rounded to
// 1dp. nil (→ JSON null) when there are no enrolments: undefined, not zero.
func (s Stats) CompletionRatePct() *float64 {
	if s.Enrolled <= 0 {
		return nil
	}
	return round1(100 * float64(s.Completed) / float64(s.Enrolled))
}

// round1 rounds to one decimal place and returns a pointer (the DTO renders nil
// as null). One decimal is enough to see a cohort move without implying a
// precision the underlying atom counts do not have.
func round1(v float64) *float64 {
	r := math.Round(v*10) / 10
	return &r
}
