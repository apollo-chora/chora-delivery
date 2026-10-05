// webhook_event_test.go: RED-first unit tests for the inbound satellite exam
// webhook receipt entity (ADR-193 D1, W5 bypass-free slice, CHO-2230).
//
// Lifecycle mirrors chora-payments' webhook_event (received -> processed |
// failed) with one addition: ResultID is recorded the moment the durable
// exam_results row exists (result-recorded), BEFORE processed. That split is
// what makes a retried delivery resume instead of grading twice: a redelivery
// that finds ResultID set skips the grade + save and only re-publishes +
// marks processed, so an idempotency_key can never mint two result rows.
package examwebhook_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

const (
	evtID   = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee01"
	idemKey = "sat-exam-result:form-1:cand-1"
	tenant  = "019e2f93-d586-71b5-8c3d-e2b0d0d5aa01"
)

func TestNew_Valid(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	ev, err := examwebhook.New(evtID, idemKey, tenant, now)
	if err != nil {
		t.Fatalf("New: unexpected error %v", err)
	}
	if ev.EventID != evtID || ev.IdempotencyKey != idemKey || ev.TenantID != tenant {
		t.Fatalf("New: fields not carried: %+v", ev)
	}
	if !ev.ReceivedAt.Equal(now) {
		t.Fatalf("New: ReceivedAt not stamped: %v", ev.ReceivedAt)
	}
	if ev.IsProcessed() {
		t.Fatal("New: fresh receipt must not be processed")
	}
	if ev.ResultID != "" {
		t.Fatal("New: fresh receipt must carry no result id")
	}
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := []struct {
		name                 string
		eventID, key, tenant string
		want                 error
	}{
		{"empty event id", "", idemKey, tenant, examwebhook.ErrEventIDRequired},
		{"empty idempotency key", evtID, "", tenant, examwebhook.ErrIdempotencyKeyRequired},
		{"empty tenant", evtID, idemKey, "", examwebhook.ErrTenantRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := examwebhook.New(tc.eventID, tc.key, tc.tenant, now); !errors.Is(err, tc.want) {
				t.Fatalf("New: want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	ev, err := examwebhook.New(evtID, idemKey, tenant, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// received -> result recorded (durable row exists, dispatch not complete)
	ev.MarkResultRecorded("res-1")
	if ev.ResultID != "res-1" {
		t.Fatalf("MarkResultRecorded: result id not stored: %+v", ev)
	}
	if ev.IsProcessed() {
		t.Fatal("MarkResultRecorded alone must NOT mean processed")
	}

	// a transient failure keeps the receipt retryable + names the reason
	ev.MarkFailed("store write failed")
	if ev.ProcessingError != "store write failed" {
		t.Fatalf("MarkFailed: error not stored: %+v", ev)
	}
	if ev.IsProcessed() {
		t.Fatal("MarkFailed must not mean processed")
	}

	// -> processed clears the error and stamps the completion
	done := now.Add(time.Second)
	ev.MarkProcessed("res-1", done)
	if !ev.IsProcessed() {
		t.Fatal("MarkProcessed: receipt must be processed")
	}
	if ev.ProcessedAt == nil || !ev.ProcessedAt.Equal(done) {
		t.Fatalf("MarkProcessed: ProcessedAt not stamped: %+v", ev.ProcessedAt)
	}
	if ev.ProcessingError != "" {
		t.Fatalf("MarkProcessed: stale error must be cleared: %q", ev.ProcessingError)
	}
	if ev.ResultID != "res-1" {
		t.Fatalf("MarkProcessed: result id lost: %+v", ev)
	}
}
