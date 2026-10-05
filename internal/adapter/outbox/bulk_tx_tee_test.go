// bulk_tx_tee_test.go - BulkEnrollTxTee writes the enrollment.created.v1
// transactional-outbox row on a caller-supplied tx (the offering roster
// bulk-enrol path). The row MUST carry the SAME column shape the live
// deliveryoutbox dispatcher (PostgresStore.FetchPending) reads - top-level
// tenant_id / gcid / idempotency_key - because a row missing those is a
// poison-pill the dispatcher cannot scan. The INSERT is tx-safe: ON CONFLICT
// (idempotency_key) DO NOTHING, so a duplicate (revive-after-cancel) is a no-op
// that does NOT abort the enclosing pgx transaction.
package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	deliveryoutbox "github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
)

const (
	teeTenant  = "01970000-0000-7000-8000-0000000000c1"
	teeCourse  = "01970000-0000-7000-8000-0000000000c9"
	teeLearner = "01970000-0000-7000-9000-0000000000c5"
	teeEnrolID = "01970000-0000-7000-9000-0000000000ee"
)

type capturedExec struct {
	query string
	args  []any
	calls int
}

func (c *capturedExec) exec(_ context.Context, query string, args ...any) error {
	c.calls++
	c.query = query
	c.args = args
	return nil
}

func TestBulkEnrollTxTee_RecordEnrollmentCreated_WritesDispatcherShapedRow(t *testing.T) {
	minter := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	tee := deliveryoutbox.NewBulkEnrollTxTee(minter)

	cap := &capturedExec{}
	err := tee.RecordEnrollmentCreated(context.Background(), cap.exec, events.EnrollmentCreated{
		TenantID:     teeTenant,
		GCID:         teeLearner,
		EnrollmentID: teeEnrolID,
		CourseID:     teeCourse,
		LearnerGCID:  teeLearner,
		Traceparent:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if err != nil {
		t.Fatalf("RecordEnrollmentCreated: %v", err)
	}
	if cap.calls != 1 {
		t.Fatalf("expected exactly one outbox INSERT; got %d", cap.calls)
	}
	// SQL must target the live dispatcher's table + be tx-safe on a dup key.
	for _, frag := range []string{
		"INSERT INTO outbox_events", "tenant_id", "gcid", "idempotency_key",
		"ON CONFLICT", "DO NOTHING", "'pending'",
	} {
		if !strings.Contains(cap.query, frag) {
			t.Fatalf("outbox INSERT must contain %q; got %q", frag, cap.query)
		}
	}
	// 11 bind args (status literal 'pending' in the VALUES list):
	// id, tenant_id, gcid, aggregate_type, aggregate_id, event_type, topic,
	// payload, envelope, idempotency_key, occurred_at.
	if len(cap.args) != 11 {
		t.Fatalf("expected 11 bind args; got %d (%v)", len(cap.args), cap.args)
	}
	if got, _ := cap.args[1].(string); got != teeTenant {
		t.Errorf("arg[1] tenant_id: want %q, got %v", teeTenant, cap.args[1])
	}
	if got, _ := cap.args[2].(string); got != teeLearner {
		t.Errorf("arg[2] gcid: want %q, got %v", teeLearner, cap.args[2])
	}
	if got, _ := cap.args[3].(string); got != "enrollment" {
		t.Errorf("arg[3] aggregate_type: want %q, got %v", "enrollment", cap.args[3])
	}
	if got, _ := cap.args[4].(string); got != teeEnrolID {
		t.Errorf("arg[4] aggregate_id: want %q, got %v", teeEnrolID, cap.args[4])
	}
	if got, _ := cap.args[6].(string); got != events.TopicEnrollmentCreated {
		t.Errorf("arg[6] topic: want %q, got %v", events.TopicEnrollmentCreated, cap.args[6])
	}
	if want := "enrollment:" + teeCourse + ":" + teeLearner; cap.args[9] != want {
		t.Errorf("arg[9] idempotency_key: want %q, got %v", want, cap.args[9])
	}
	// The event was also minted through the publisher (in-mem history).
	if len(minter.History()) != 1 {
		t.Fatalf("minter must record exactly 1 enrollment.created event; got %d", len(minter.History()))
	}
}

// A propagated exec error must surface (fail-loud) so the enclosing tx rolls
// back - never swallow the outbox write failure.
func TestBulkEnrollTxTee_RecordEnrollmentCreated_PropagatesExecError(t *testing.T) {
	minter := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	tee := deliveryoutbox.NewBulkEnrollTxTee(minter)

	boom := errors.New("tx exec failed")
	failExec := func(_ context.Context, _ string, _ ...any) error { return boom }
	err := tee.RecordEnrollmentCreated(context.Background(), failExec, events.EnrollmentCreated{
		TenantID: teeTenant, GCID: teeLearner, EnrollmentID: teeEnrolID,
		CourseID: teeCourse, LearnerGCID: teeLearner,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("exec error must propagate; got %v", err)
	}
}
