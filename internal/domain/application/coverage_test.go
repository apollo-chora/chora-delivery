// coverage_test.go — closes the remaining statement gaps in the application
// package: the public NewUUIDv7 wrapper and TransitionWithReason's
// Accepted/Paid/Enrolled re-stamp branches (the reason-stamping path is shared
// with the already-covered Rejected/Withdrawn tests).
package application_test

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

func TestNewUUIDv7_Wrapper(t *testing.T) {
	t.Parallel()
	id := application.NewUUIDv7()
	if len(id) != 36 || id[14] != '7' {
		t.Fatalf("NewUUIDv7() = %q, want a 36-char UUIDv7", id)
	}
}

func TestApplication_TransitionWithReason_AcceptedStamps(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusOfferMade)
	if err := app.TransitionWithReason(application.StatusAccepted, "offer accepted"); err != nil {
		t.Fatalf("accept with reason: %v", err)
	}
	if app.AcceptedAt.IsZero() {
		t.Fatal("AcceptedAt must be stamped")
	}
	if app.Status != application.StatusAccepted {
		t.Fatalf("status: got %q", app.Status)
	}
	// The reason lands on the newest history entry.
	h := app.History()
	last := h[len(h)-1]
	if last.To != application.StatusAccepted || last.Reason != "offer accepted" {
		t.Fatalf("history tail: %+v", last)
	}
	if last.From != application.StatusOfferMade {
		t.Fatalf("history from: got %q want offer_made", last.From)
	}
}

func TestApplication_TransitionWithReason_PaidStamps(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusAccepted)
	if err := app.TransitionWithReason(application.StatusPaid, "card charged"); err != nil {
		t.Fatalf("pay with reason: %v", err)
	}
	if app.PaidAt.IsZero() {
		t.Fatal("PaidAt must be stamped")
	}
}

func TestApplication_TransitionWithReason_EnrolledStamps(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusPaid)
	if err := app.TransitionWithReason(application.StatusEnrolled, "seated"); err != nil {
		t.Fatalf("enrol with reason: %v", err)
	}
	if app.EnrolledAt.IsZero() {
		t.Fatal("EnrolledAt must be stamped")
	}
	if got := application.EventTopicFor(app.Status); !strings.Contains(got, "enrolled") {
		t.Fatalf("topic: got %q", got)
	}
	if got := application.IMDADimensionFor(app.Status); got != "transparency" {
		t.Fatalf("dimension: got %q want transparency", got)
	}
}

func TestApplication_TransitionWithReason_TimestampSnapshot(t *testing.T) {
	t.Parallel()
	app := buildAppAtStatus(t, application.StatusOfferMade)
	before := app.UpdatedAt
	time.Sleep(time.Millisecond)
	if err := app.TransitionWithReason(application.StatusAccepted, ""); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !app.UpdatedAt.After(before) {
		t.Fatal("UpdatedAt must advance on transition")
	}
	if !app.AcceptedAt.Equal(app.UpdatedAt) {
		t.Fatalf("AcceptedAt must equal the transition instant")
	}
}
