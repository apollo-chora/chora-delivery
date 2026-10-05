// timeline_topup_test.go — tops up lesson_timeline coverage: SegmentType
// validation, Segment.Duration, AddSegment remaining guards (invalid type,
// negative start) and Cancel-from-Complete.
package lesson_timeline_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom/lesson_timeline"
)

func TestSegmentType_Valid(t *testing.T) {
	t.Parallel()
	for _, s := range []lesson_timeline.SegmentType{
		lesson_timeline.SegmentTypeAtom, lesson_timeline.SegmentTypeQuiz,
		lesson_timeline.SegmentTypeJamBoard, lesson_timeline.SegmentTypeBreak,
	} {
		if !s.Valid() {
			t.Fatalf("SegmentType(%q).Valid() = false", s)
		}
	}
	if lesson_timeline.SegmentType("").Valid() {
		t.Fatal("empty SegmentType must be invalid")
	}
	if lesson_timeline.SegmentType("BOGUS").Valid() {
		t.Fatal("bogus SegmentType must be invalid")
	}
}

func TestSegment_Duration(t *testing.T) {
	t.Parallel()
	s := &lesson_timeline.Segment{StartMinute: 0, EndMinute: 30}
	if got := s.Duration(); got != 30 {
		t.Fatalf("Duration = %d, want 30", got)
	}
}

func newTimelineForTopup(t *testing.T) *lesson_timeline.Timeline {
	t.Helper()
	tl, err := lesson_timeline.NewTimeline(
		tenantA, classA, teacherA,
		mustTime(t, "2026-06-01T09:00:00Z"),
		mustTime(t, "2026-06-01T09:50:00Z"),
	)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	return tl
}

func TestAddSegment_InvalidType(t *testing.T) {
	t.Parallel()
	tl := newTimelineForTopup(t)
	err := tl.AddSegment(lesson_timeline.SegmentType("nope"), "", "x", 0, 10)
	if !errors.Is(err, lesson_timeline.ErrInvalidArgument) {
		t.Fatalf("invalid type: want ErrInvalidArgument, got %v", err)
	}
}

func TestAddSegment_NegativeStart(t *testing.T) {
	t.Parallel()
	tl := newTimelineForTopup(t)
	err := tl.AddSegment(lesson_timeline.SegmentTypeBreak, "", "b", -1, 10)
	if !errors.Is(err, lesson_timeline.ErrInvalidArgument) {
		t.Fatalf("negative start: want ErrInvalidArgument, got %v", err)
	}
}

func TestCancel_FromComplete(t *testing.T) {
	t.Parallel()
	tl := newTimelineForTopup(t)
	if err := tl.AddSegment(lesson_timeline.SegmentTypeAtom, atomA, "intro", 0, 30); err != nil {
		t.Fatalf("AddSegment: %v", err)
	}
	if err := tl.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := tl.Start(time.Now()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tl.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := tl.Cancel("too late"); !errors.Is(err, lesson_timeline.ErrInvalidTransition) {
		t.Fatalf("Cancel from Complete: want ErrInvalidTransition, got %v", err)
	}
}
