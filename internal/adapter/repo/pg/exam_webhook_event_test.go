// exam_webhook_event_test.go: unit tests for pg.ExamWebhookEventRepo
// (ADR-193 D1 dedup gate, chora_delivery.exam_webhook_events, CHO-2230).
//
// The table is RLS-DISABLED (operational, mirrors stripe_webhook_events): a
// satellite delivery arrives with NO Chora session and its tenant is inside
// the signed payload, so the adapter deliberately does NOT call
// rls.ApplySession. Tenant scoping happens on the exam_results write (which
// IS RLS-scoped), not on the receipt.
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

const (
	weEventID = "019e2f93-d586-71b5-8c3d-e2b0d0d5f001"
	weKey     = "satellite-a:sitting-9:cand-1"
	weTenant  = "019e2f93-d586-71b5-8c3d-e2b0d0d5aa01"
)

func newReceipt(t *testing.T) *examwebhook.WebhookEvent {
	t.Helper()
	ev, err := examwebhook.New(weEventID, weKey, weTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("examwebhook.New: %v", err)
	}
	return ev
}

func TestExamWebhookEventRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewExamWebhookEventRepo(nil)
	ctx := context.Background()
	if err := r.Insert(ctx, newReceipt(t)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Insert: want ErrNotImplemented, got %v", err)
	}
	if _, _, err := r.GetByIdempotencyKey(ctx, weKey); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Get: want ErrNotImplemented, got %v", err)
	}
	if err := r.MarkResultRecorded(ctx, weKey, "res"); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("MarkResultRecorded: want ErrNotImplemented, got %v", err)
	}
	if err := r.MarkProcessed(ctx, weKey, "res", time.Now()); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("MarkProcessed: want ErrNotImplemented, got %v", err)
	}
	if err := r.MarkFailed(ctx, weKey, "boom"); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("MarkFailed: want ErrNotImplemented, got %v", err)
	}
}

func TestExamWebhookEventRepo_Insert_NoRLSSession(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	// Bare context on purpose: the receipt table is RLS-disabled and the
	// handler has no tenant session at insert time. ApplySession would refuse
	// a bare ctx, so its absence is load-bearing and asserted here.
	if err := r.Insert(context.Background(), newReceipt(t)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	for _, sql := range q.sqls {
		if strings.Contains(sql, "chora.tenant_id") {
			t.Fatalf("receipt insert must NOT apply an RLS session (RLS-disabled table): %q", sql)
		}
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO exam_webhook_events") {
		t.Errorf("insert SQL wrong: %q", last)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 4 {
		t.Fatalf("insert args=%d want 4 (%v)", len(args), args)
	}
	if args[0] != weEventID || args[1] != weKey || args[2] != weTenant {
		t.Errorf("insert args wrong: %v", args)
	}
}

func TestExamWebhookEventRepo_Insert_UniqueViolation_IsDuplicate(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New(`ERROR: duplicate key value violates unique constraint "exam_webhook_events_idempotency_key_key" (SQLSTATE 23505)`)}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	if err := r.Insert(context.Background(), newReceipt(t)); !errors.Is(err, examwebhook.ErrDuplicate) {
		t.Fatalf("Insert: want ErrDuplicate, got %v", err)
	}
}

func TestExamWebhookEventRepo_GetByIdempotencyKey(t *testing.T) {
	t.Parallel()
	received := time.Now().UTC().Truncate(time.Microsecond)
	processed := received.Add(2 * time.Second)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = weEventID
				*(dest[1].(*string)) = weKey
				*(dest[2].(*string)) = weTenant
				*(dest[3].(*time.Time)) = received
				*(dest[4].(**time.Time)) = &processed
				*(dest[5].(*string)) = ""
				*(dest[6].(*string)) = "res-1"
				return nil
			}}
		},
	}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	ev, ok, err := r.GetByIdempotencyKey(context.Background(), weKey)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if !ev.IsProcessed() || ev.ResultID != "res-1" || ev.TenantID != weTenant {
		t.Fatalf("Get: %+v", ev)
	}
}

func TestExamWebhookEventRepo_Get_NoRows_IsGenuineMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	_, ok, err := r.GetByIdempotencyKey(context.Background(), "absent")
	if err != nil {
		t.Fatalf("Get miss: err=%v", err)
	}
	if ok {
		t.Fatal("Get miss: ok must be false")
	}
}

func TestExamWebhookEventRepo_Marks(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewExamWebhookEventRepo(&stubTxRunner{q: q})
	if err := r.MarkResultRecorded(context.Background(), weKey, "res-1"); err != nil {
		t.Fatalf("MarkResultRecorded: %v", err)
	}
	if err := r.MarkProcessed(context.Background(), weKey, "res-1", time.Now().UTC()); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if err := r.MarkFailed(context.Background(), weKey, "publish failed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	joined := strings.Join(q.sqls, " | ")
	if !strings.Contains(joined, "UPDATE exam_webhook_events") {
		t.Errorf("marks must UPDATE the receipt: %q", joined)
	}
	if !strings.Contains(joined, "processed_at") || !strings.Contains(joined, "result_id") || !strings.Contains(joined, "processing_error") {
		t.Errorf("marks must touch processed_at + result_id + processing_error: %q", joined)
	}
}
