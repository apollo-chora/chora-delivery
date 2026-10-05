// exam_webhook_event_repo_test.go: contract tests for the in-memory
// examwebhook.Repo (CHO-2230). Must mirror the pg adapter: duplicate
// idempotency_key OR event_id refused with ErrDuplicate; lifecycle marks
// persist by idempotency_key.
package inmem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

const (
	weEvt    = "019e2f93-d586-71b5-8c3d-e2b0d0d5f001"
	weEvt2   = "019e2f93-d586-71b5-8c3d-e2b0d0d5f002"
	weKey    = "satellite-a:sitting-9:cand-1"
	weTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d5aa01"
)

func mustReceipt(t *testing.T, eventID, key string) *examwebhook.WebhookEvent {
	t.Helper()
	ev, err := examwebhook.New(eventID, key, weTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("examwebhook.New: %v", err)
	}
	return ev
}

func TestExamWebhookEventRepo_InsertAndDuplicates(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamWebhookEventRepo()
	ctx := context.Background()
	if err := r.Insert(ctx, mustReceipt(t, weEvt, weKey)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Same idempotency_key under a NEW event id: still a duplicate.
	if err := r.Insert(ctx, mustReceipt(t, weEvt2, weKey)); !errors.Is(err, examwebhook.ErrDuplicate) {
		t.Fatalf("Insert dup key: want ErrDuplicate, got %v", err)
	}
	// Same event id under a new key: duplicate too (PK mirror).
	if err := r.Insert(ctx, mustReceipt(t, weEvt, "other-key")); !errors.Is(err, examwebhook.ErrDuplicate) {
		t.Fatalf("Insert dup event id: want ErrDuplicate, got %v", err)
	}
}

func TestExamWebhookEventRepo_LifecycleMarks(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamWebhookEventRepo()
	ctx := context.Background()
	if err := r.Insert(ctx, mustReceipt(t, weEvt, weKey)); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := r.MarkResultRecorded(ctx, weKey, "res-1"); err != nil {
		t.Fatalf("MarkResultRecorded: %v", err)
	}
	ev, ok, err := r.GetByIdempotencyKey(ctx, weKey)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if ev.ResultID != "res-1" || ev.IsProcessed() {
		t.Fatalf("after MarkResultRecorded: %+v", ev)
	}

	if err := r.MarkFailed(ctx, weKey, "publish failed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	ev, _, _ = r.GetByIdempotencyKey(ctx, weKey)
	if ev.ProcessingError != "publish failed" {
		t.Fatalf("after MarkFailed: %+v", ev)
	}

	when := time.Now().UTC()
	if err := r.MarkProcessed(ctx, weKey, "res-1", when); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	ev, _, _ = r.GetByIdempotencyKey(ctx, weKey)
	if !ev.IsProcessed() || ev.ProcessingError != "" || ev.ResultID != "res-1" {
		t.Fatalf("after MarkProcessed: %+v", ev)
	}
}

func TestExamWebhookEventRepo_GetMiss(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamWebhookEventRepo()
	if _, ok, err := r.GetByIdempotencyKey(context.Background(), "absent"); ok || err != nil {
		t.Fatalf("Get absent: ok=%v err=%v", ok, err)
	}
}
