// booking_scheduledclass_test.go — RED specs for ADR-236 D2.
//
// NewBookingForClass builds a pending Booking from the PRIMITIVE identity of a
// durable scheduled class (id / course / tenant) rather than a *Class pointer.
// Capacity is NOT reserved here — under D2 it is enforced durably at the
// persistence boundary (BookingPort.ReserveSeatAndSave), replacing the legacy
// in-memory Class.reserveSeat mutex (single-pod only). This keeps the delivery
// aggregate decoupled from the scheduling package.
package delivery_test

import (
	"errors"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestNewBookingForClass_BuildsPending(t *testing.T) {
	b, err := domain.NewBookingForClass("class-1", "course-1", tenantA, gcidB)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if b.ClassID != "class-1" {
		t.Errorf("ClassID = %q, want class-1", b.ClassID)
	}
	if b.CourseID != "course-1" {
		t.Errorf("CourseID = %q, want course-1", b.CourseID)
	}
	if b.TenantID != tenantA {
		t.Errorf("TenantID = %q, want %s", b.TenantID, tenantA)
	}
	if b.LearnerID != gcidB {
		t.Errorf("LearnerID = %q, want %s", b.LearnerID, gcidB)
	}
	if b.Status != domain.BookingStatusPending {
		t.Errorf("Status = %q, want pending", b.Status)
	}
	if b.ID == "" {
		t.Errorf("expected a generated id")
	}
}

func TestNewBookingForClass_RejectsBlankFields(t *testing.T) {
	cases := []struct {
		name              string
		cls, crs, ten, gc string
	}{
		{"blank class", "", "course-1", tenantA, gcidB},
		{"blank course", "class-1", "", tenantA, gcidB},
		{"blank tenant", "class-1", "course-1", "", gcidB},
		{"blank learner", "class-1", "course-1", tenantA, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewBookingForClass(tc.cls, tc.crs, tc.ten, tc.gc)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument, got %v", err)
			}
		})
	}
}
