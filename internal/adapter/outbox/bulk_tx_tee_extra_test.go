// bulk_tx_tee_extra_test.go — additional BulkEnrollTxTee coverage: the nil-
// minter panic, the mint-error branch, and the buildOutboxRow error branch of
// RecordEnrollmentCreated — all driven through minter stubs that embed the
// InMemoryPublisher (same reuse pattern as the publisher tests' noCustomPublisher).
package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	deliveryoutbox "github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
)

func TestBulkEnrollTxTee_New_PanicsOnNilMinter(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("NewBulkEnrollTxTee with nil minter should panic")
		}
	}()
	deliveryoutbox.NewBulkEnrollTxTee(nil)
}

// errEnrollMinter fails the enrollment mint — exercises the mint-error branch.
type errEnrollMinter struct {
	*events.InMemoryPublisher
}

func (m *errEnrollMinter) PublishEnrollmentCreated(events.EnrollmentCreated) (events.PublishedEvent, error) {
	return events.PublishedEvent{}, errors.New("mint boom")
}

func TestBulkEnrollTxTee_RecordEnrollmentCreated_SurfacesMintError(t *testing.T) {
	t.Parallel()
	tee := deliveryoutbox.NewBulkEnrollTxTee(&errEnrollMinter{
		InMemoryPublisher: events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
	})
	cap := &capturedExec{}
	err := tee.RecordEnrollmentCreated(context.Background(), cap.exec, events.EnrollmentCreated{
		TenantID: teeTenant, GCID: teeLearner, EnrollmentID: teeEnrolID,
		CourseID: teeCourse, LearnerGCID: teeLearner,
	})
	if err == nil {
		t.Fatal("expected mint error")
	}
	if !strings.Contains(err.Error(), "mint enrollment.created") {
		t.Errorf("err = %v; want mint-error wrapper", err)
	}
	if cap.calls != 0 {
		t.Errorf("exec calls = %d; want 0 (no INSERT after mint failure)", cap.calls)
	}
}

// badPayloadMinter mints an enrollment.created event whose payload cannot be
// JSON-marshalled — the topic has no binary encoder, so encodeOutboxPayload
// falls back to JSON and must surface the marshal error via buildOutboxRow.
type badPayloadMinter struct {
	*events.InMemoryPublisher
}

func (m *badPayloadMinter) PublishEnrollmentCreated(events.EnrollmentCreated) (events.PublishedEvent, error) {
	return events.PublishedEvent{
		Topic: events.TopicEnrollmentCreated,
		Envelope: events.EventEnvelope{
			EventID:        "evt-badtx",
			IdempotencyKey: "idem-badtx",
			TenantID:       teeTenant,
			GCID:           teeLearner,
			OccurredAt:     time.Now().UTC(),
			PublishedAt:    time.Now().UTC(),
			SourceProject:  "chora-489812",
			SourceService:  "chora-delivery",
			SchemaVersion:  1,
		},
		Payload: map[string]any{"broken": make(chan int)},
	}, nil
}

func TestBulkEnrollTxTee_RecordEnrollmentCreated_SurfacesBuildRowError(t *testing.T) {
	t.Parallel()
	tee := deliveryoutbox.NewBulkEnrollTxTee(&badPayloadMinter{
		InMemoryPublisher: events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
	})
	cap := &capturedExec{}
	err := tee.RecordEnrollmentCreated(context.Background(), cap.exec, events.EnrollmentCreated{
		TenantID: teeTenant, GCID: teeLearner, EnrollmentID: teeEnrolID,
		CourseID: teeCourse, LearnerGCID: teeLearner,
	})
	if err == nil {
		t.Fatal("expected row-build error from un-marshalable payload")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("err = %v; want marshal error", err)
	}
	if cap.calls != 0 {
		t.Errorf("exec calls = %d; want 0 (no INSERT after row-build failure)", cap.calls)
	}
}
