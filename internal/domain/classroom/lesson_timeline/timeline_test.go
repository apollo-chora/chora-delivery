// Package lesson_timeline_test pins the contract for minute-by-minute lesson
// plan timelines per CHO-13 + docs/design/ux_classroom_experience.md
// QuizPrep step (instructor sets up live session from atom pool with
// per-segment timing) extended to a full lesson plan.
//
// Tests are written FIRST per .claude/rules/development-execution.md TDD.
package lesson_timeline_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom/lesson_timeline"
)

const (
	tenantA  = "01970000-0000-7000-8000-000000000001"
	classA   = "01970000-0000-7000-7100-000000000001"
	atomA    = "atom://0xK3-NETWORK-DEFENSES"
	atomB    = "atom://0xK3-CRYPTO-INTRO"
	atomC    = "atom://0xK3-DEFENSE-IN-DEPTH"
	teacherA = "01970000-0000-7000-9000-000000000001"
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
// NewTimeline + AddSegment guards
// -----------------------------------------------------------------------------

func TestNewTimeline_TableDriven(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T11:00:00Z")
	cases := []struct {
		name    string
		tenant  string
		class   string
		instr   string
		starts  time.Time
		ends    time.Time
		wantErr error
	}{
		{"happy", tenantA, classA, teacherA, starts, ends, nil},
		{"missing tenant", "", classA, teacherA, starts, ends, lesson_timeline.ErrInvalidArgument},
		{"missing class", tenantA, "", teacherA, starts, ends, lesson_timeline.ErrInvalidArgument},
		{"missing instructor", tenantA, classA, "", starts, ends, lesson_timeline.ErrInvalidArgument},
		{"end-before-start", tenantA, classA, teacherA, ends, starts, lesson_timeline.ErrInvalidArgument},
		{"end-equals-start", tenantA, classA, teacherA, starts, starts, lesson_timeline.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tl, err := lesson_timeline.NewTimeline(c.tenant, c.class, c.instr, c.starts, c.ends)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got err %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if tl == nil || tl.ID == "" {
				t.Fatalf("nil timeline or empty ID")
			}
			if tl.State != lesson_timeline.StateDraft {
				t.Errorf("expected initial state Draft, got %s", tl.State)
			}
			if len(tl.Segments) != 0 {
				t.Errorf("expected zero segments, got %d", len(tl.Segments))
			}
		})
	}
}

func TestAddSegment_Append_Validate(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	tl, err := lesson_timeline.NewTimeline(tenantA, classA, teacherA, starts, ends)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// First segment 0..30
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomA, "Network defenses intro", 0, 30); err != nil {
		t.Fatalf("seg-1: %v", err)
	}
	// Second segment 30..50
	if err := tl.AddSegment(lesson_timeline.SegmentTypeQuiz, atomB, "Mid quiz", 30, 50); err != nil {
		t.Fatalf("seg-2: %v", err)
	}
	if got := len(tl.Segments); got != 2 {
		t.Fatalf("len(segments)=%d, want 2", got)
	}
	if tl.Segments[0].StartMinute != 0 || tl.Segments[0].EndMinute != 30 {
		t.Errorf("seg-1 minutes wrong")
	}

	// Overlap rejected
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomC, "overlap", 25, 40); !errors.Is(err, lesson_timeline.ErrSegmentOverlap) {
		t.Errorf("overlap: got %v want ErrSegmentOverlap", err)
	}
	// Out-of-bounds (lesson length is 90 min)
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomC, "oob", 80, 100); !errors.Is(err, lesson_timeline.ErrSegmentOutOfBounds) {
		t.Errorf("oob: got %v want ErrSegmentOutOfBounds", err)
	}
	// end <= start
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomC, "bad", 50, 50); !errors.Is(err, lesson_timeline.ErrInvalidArgument) {
		t.Errorf("end<=start: got %v want ErrInvalidArgument", err)
	}
	// Missing atom
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, "", "no atom", 50, 60); !errors.Is(err, lesson_timeline.ErrInvalidArgument) {
		t.Errorf("missing atom: got %v want ErrInvalidArgument", err)
	}
}

// -----------------------------------------------------------------------------
// State machine: Draft → Published → Live → Complete (+ Cancel)
// -----------------------------------------------------------------------------

func TestTimelineStateMachine(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	tl, err := lesson_timeline.NewTimeline(tenantA, classA, teacherA, starts, ends)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomA, "intro", 0, 30); err != nil {
		t.Fatalf("seg: %v", err)
	}

	// Draft → Published
	if err := tl.Publish(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if tl.State != lesson_timeline.StatePublished {
		t.Errorf("expected Published, got %s", tl.State)
	}

	// Published → Live
	now := mustTime(t, "2026-06-01T09:00:00Z")
	if err := tl.Start(now); err != nil {
		t.Fatalf("start: %v", err)
	}
	if tl.State != lesson_timeline.StateLive {
		t.Errorf("expected Live, got %s", tl.State)
	}

	// Live → Complete
	if err := tl.Complete(); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if tl.State != lesson_timeline.StateComplete {
		t.Errorf("expected Complete, got %s", tl.State)
	}
}

func TestTimelineStateMachine_InvalidTransitions(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	tl, _ := lesson_timeline.NewTimeline(tenantA, classA, teacherA, starts, ends)

	// Cannot publish without segments
	if err := tl.Publish(); !errors.Is(err, lesson_timeline.ErrEmptyTimeline) {
		t.Errorf("publish empty: got %v want ErrEmptyTimeline", err)
	}
	// Cannot start a Draft (must publish first)
	if err := tl.Start(starts); !errors.Is(err, lesson_timeline.ErrInvalidTransition) {
		t.Errorf("start draft: got %v want ErrInvalidTransition", err)
	}
	// Cannot complete a Draft
	if err := tl.Complete(); !errors.Is(err, lesson_timeline.ErrInvalidTransition) {
		t.Errorf("complete draft: got %v want ErrInvalidTransition", err)
	}
	// Cannot AddSegment after publish
	_ = tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomA, "intro", 0, 30)
	_ = tl.Publish()
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomB, "late", 30, 60); !errors.Is(err, lesson_timeline.ErrInvalidTransition) {
		t.Errorf("addseg after publish: got %v want ErrInvalidTransition", err)
	}
}

func TestTimelineCancel(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	tl, _ := lesson_timeline.NewTimeline(tenantA, classA, teacherA, starts, ends)
	_ = tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomA, "intro", 0, 30)
	_ = tl.Publish()

	if err := tl.Cancel("instructor changed mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if tl.State != lesson_timeline.StateCancelled {
		t.Errorf("expected Cancelled, got %s", tl.State)
	}
	// Cancel is terminal — cannot re-cancel or re-publish
	if err := tl.Publish(); !errors.Is(err, lesson_timeline.ErrInvalidTransition) {
		t.Errorf("publish after cancel: got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Current segment lookup at a given lesson minute
// -----------------------------------------------------------------------------

func TestCurrentSegmentAt(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	tl, _ := lesson_timeline.NewTimeline(tenantA, classA, teacherA, starts, ends)
	_ = tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomA, "intro", 0, 20)
	_ = tl.AddSegment(lesson_timeline.SegmentTypeQuiz, atomB, "midq", 25, 35)
	_ = tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomC, "wrap", 60, 80)

	cases := []struct {
		minute int
		want   string
	}{
		{0, atomA}, {19, atomA},
		{22, ""}, // gap
		{25, atomB}, {34, atomB},
		{40, ""}, {59, ""},
		{60, atomC}, {79, atomC},
		{80, ""}, {89, ""},
	}
	for _, c := range cases {
		seg := tl.CurrentSegmentAt(c.minute)
		got := ""
		if seg != nil {
			got = seg.AtomID
		}
		if got != c.want {
			t.Errorf("minute=%d: got %q want %q", c.minute, got, c.want)
		}
	}
}

func TestTotalMinutes(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T10:30:00Z")
	tl, _ := lesson_timeline.NewTimeline(tenantA, classA, teacherA, starts, ends)
	if got := tl.TotalMinutes(); got != 90 {
		t.Errorf("TotalMinutes: got %d want 90", got)
	}
}
