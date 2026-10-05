// exam_webhook_event_extra_test.go — extra pg.ExamWebhookEventRepo unit tests:
// finish partial statement coverage of exam_webhook_event.go.
//
// Extends exam_webhook_event_test.go (same pg_test package) — reuses
// newReceipt + the weEventID / weKey / weTenant consts + the shared
// stubQuerier / stubTxRunner / stubRow fixtures.
//
// New helpers/fillers/consts in this file are prefixed `ewx` to keep the
// package-level pg_test namespace unique across parallel agents.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/examwebhook"
)

func TestExamWebhookEventRepo_Insert_NilEvent_IsNoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	// A nil receipt is a no-op (the receive path never constructs one, but the
	// guard must not crash or emit SQL).
	if err := r.Insert(context.Background(), nil); err != nil {
		t.Fatalf("Insert nil: %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("nil event must not execute SQL; got %v", q.sqls)
	}
}

func TestExamWebhookEventRepo_Insert_NonUniqueError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("connection lost")}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	err := r.Insert(context.Background(), newReceipt(t))
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if errors.Is(err, examwebhook.ErrDuplicate) {
		t.Fatalf("a non-23505 error must NOT map to ErrDuplicate; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: insert exam_webhook_event") {
		t.Fatalf("insert error must be wrapped with context; got %v", err)
	}
	if !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("wrapped error must preserve the cause; got %v", err)
	}
}

func TestExamWebhookEventRepo_GetByIdempotencyKey_InfraError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("conn reset") }}
	}}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	// NOT a "no rows" error → LOUD (CHO-2184): an infra failure is not a miss.
	_, ok, err := r.GetByIdempotencyKey(context.Background(), weKey)
	if err == nil {
		t.Fatalf("expected an infra error to surface")
	}
	if ok {
		t.Fatalf("ok must stay false alongside the error")
	}
	if !strings.Contains(err.Error(), "pg: get exam_webhook_event") {
		t.Fatalf("get error must be wrapped with context; got %v", err)
	}
}

func TestExamWebhookEventRepo_MarkResultRecorded_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("write failed")}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	err := r.MarkResultRecorded(context.Background(), weKey, "res-1")
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: mark exam_webhook_event result recorded") {
		t.Fatalf("mark error must be wrapped; got %v", err)
	}
}

func TestExamWebhookEventRepo_MarkProcessed_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("write failed")}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	err := r.MarkProcessed(context.Background(), weKey, "res-1", time.Now().UTC())
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: mark exam_webhook_event processed") {
		t.Fatalf("mark error must be wrapped; got %v", err)
	}
}

func TestExamWebhookEventRepo_MarkFailed_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("write failed")}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	err := r.MarkFailed(context.Background(), weKey, "publish failed")
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: mark exam_webhook_event failed") {
		t.Fatalf("mark error must be wrapped; got %v", err)
	}
}
