// scheduling_double_book_test.go - a room clash must surface as 409, not 5xx
// (CHO-2299).
//
// WHY THIS EXISTS. The live probe after deploying the gate returned 502
// GATEWAY_UPSTREAM_5XX for a genuine double-book. The DB constraint fired and
// the write WAS blocked, so the invariant held, but the handler had no mapping
// for scheduling.ErrRoomDoubleBooked and fell through to a 500, which the
// gateway masks. A policy DENY must be 4xx: a 502'd deny erases its own reason,
// and the caller cannot tell "you double-booked this room" from "the backend is
// broken".
//
// The unit tests missed it because they covered the domain guards and the room
// lookup, but never drove the Save error path.
package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

// doubleBookingSchedulingRepo fails Save exactly the way the pg adapter does
// when the mig-0059 EXCLUDE constraint rejects an overlap.
type doubleBookingSchedulingRepo struct{ calls int }

func (r *doubleBookingSchedulingRepo) Save(context.Context, *scheduling.ScheduledClass) error {
	r.calls++
	return scheduling.ErrRoomDoubleBooked
}

func (r *doubleBookingSchedulingRepo) Get(context.Context, string) (*scheduling.ScheduledClass, bool, error) {
	return nil, false, nil
}

func (r *doubleBookingSchedulingRepo) ListByTenantWeek(context.Context, string, int, int) ([]*scheduling.ScheduledClass, error) {
	return nil, nil
}

func (r *doubleBookingSchedulingRepo) ListByTenant(context.Context, string) ([]*scheduling.ScheduledClass, error) {
	return nil, nil
}

var _ scheduling.SchedulingStore = (*doubleBookingSchedulingRepo)(nil)
var _ scheduling.SchedulingStore = (*failingSchedulingRepo)(nil)

func TestSchedulingCreate_RoomDoubleBooked_Is409NotA5xx(t *testing.T) {
	repo := &doubleBookingSchedulingRepo{}
	srv := newSchedulingServerWithRepo(t, repo)

	w := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), schedInstructor, "instructor")

	if w.Code >= 500 {
		t.Fatalf("a room clash is a POLICY DENY and must be 4xx; got %d body=%s. "+
			"The gateway masks every upstream 5xx, so a 502'd deny erases its own reason",
			w.Code, w.Body.String())
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict; got %d body=%s", w.Code, w.Body.String())
	}
	if repo.calls == 0 {
		t.Fatalf("the handler never reached Save, so this test proved nothing")
	}
	// The wire code must be stable and specific enough to act on.
	if body := w.Body.String(); !strings.Contains(body, "room_double_booked") {
		t.Fatalf("expected the room_double_booked code so a caller can react; got %s", body)
	}
}

// Guard: the mapping must be specific. An unrelated store failure stays a 5xx,
// so a broken backend is never mislabelled as a scheduling conflict.
func TestSchedulingCreate_OtherSaveError_StaysA5xx(t *testing.T) {
	srv := newSchedulingServerWithRepo(t, &failingSchedulingRepo{})

	w := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), schedInstructor, "instructor")

	if w.Code == http.StatusConflict {
		t.Fatalf("a generic store failure must NOT be reported as a double-book; got 409")
	}
	if w.Code < 500 {
		t.Fatalf("a generic store failure must stay a 5xx; got %d", w.Code)
	}
}

type failingSchedulingRepo struct{}

func (r *failingSchedulingRepo) ListByTenant(context.Context, string) ([]*scheduling.ScheduledClass, error) {
	return nil, nil
}

func (r *failingSchedulingRepo) Save(context.Context, *scheduling.ScheduledClass) error {
	return errBoomSched
}

func (r *failingSchedulingRepo) Get(context.Context, string) (*scheduling.ScheduledClass, bool, error) {
	return nil, false, nil
}

func (r *failingSchedulingRepo) ListByTenantWeek(context.Context, string, int, int) ([]*scheduling.ScheduledClass, error) {
	return nil, nil
}

var errBoomSched = &schedBoom{}

type schedBoom struct{}

func (e *schedBoom) Error() string { return "boom: connection reset" }
