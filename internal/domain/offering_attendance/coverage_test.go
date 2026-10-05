// coverage_test.go — closes the remaining statement gap in record.go:
// NewRecord's Now-is-zero default-to-time.Now branch.
package offering_attendance

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
)

func TestNewRecord_ZeroNowDefaultsToClock(t *testing.T) {
	in := validRecordInput()
	in.Now = time.Time{}
	r, err := NewRecord(in)
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if r.RecordedAt.IsZero() {
		t.Fatal("RecordedAt must be stamped when Now is zero")
	}
	if r.RecordedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("RecordedAt should be near the clock, got %v", r.RecordedAt)
	}
	if r.Status != attendance.StatusPresent {
		t.Fatalf("status: got %q", r.Status)
	}
}
