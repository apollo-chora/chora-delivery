package offering_attendance

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
)

func validRecordInput() NewRecordInput {
	return NewRecordInput{
		TenantID:  "11111111-1111-7111-8111-111111111111",
		SessionID: "01985e7f-6666-7abc-8def-0000000000f1",
		GCID:      "00000000-0000-7000-9000-00000000d001",
		Status:    attendance.StatusPresent,
		Source:    attendance.SourceManual,
		Now:       time.Date(2026, 9, 1, 9, 5, 0, 0, time.UTC),
	}
}

func TestNewRecord_OK(t *testing.T) {
	r, err := NewRecord(validRecordInput())
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if r.ID == "" {
		t.Fatal("ID not generated")
	}
	if r.Status != attendance.StatusPresent || r.Source != attendance.SourceManual {
		t.Fatalf("fields: %+v", r)
	}
	if r.RecordedAt.Location() != time.UTC {
		t.Fatalf("RecordedAt not UTC: %v", r.RecordedAt)
	}
}

func TestNewRecord_SourceDefaultsToManual(t *testing.T) {
	in := validRecordInput()
	in.Source = ""
	r, err := NewRecord(in)
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if r.Source != attendance.SourceManual {
		t.Fatalf("source: want manual default, got %q", r.Source)
	}
}

func TestNewRecord_Guards(t *testing.T) {
	cases := map[string]func(*NewRecordInput){
		"missing tenant":  func(in *NewRecordInput) { in.TenantID = "" },
		"missing session": func(in *NewRecordInput) { in.SessionID = " " },
		"missing gcid":    func(in *NewRecordInput) { in.GCID = "" },
		"invalid status":  func(in *NewRecordInput) { in.Status = attendance.Status("bogus") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validRecordInput()
			mutate(&in)
			if _, err := NewRecord(in); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("want ErrInvalidArgument, got %v", err)
			}
		})
	}
}
