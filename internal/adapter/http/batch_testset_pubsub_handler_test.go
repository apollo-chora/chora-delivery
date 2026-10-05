// batch_testset_pubsub_handler_test.go — Lane 1c W4 (CHO-1703 / ADR-180 D10)
// push-handler coverage for /api/internal/pubsub/batch-testset-inbox.
//
// TDD RED phase: written FIRST. Exercises the full inbound chain: Pub/Sub
// push envelope (base64 BINARY protobuf data + envelope attributes) →
// pubsubpush decode → protodecode binary unmarshal → subscriber assembly →
// InMemTestSetStore row + test_set.created.v1 emit. Mirrors
// grading_pubsub_handler_test.go.
package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	bpJobID  = "01985e7f-0000-7000-8000-00000000bbbb"
	bpTenant = tenantA
	bpAuthor = gcidA
)

func newBatchTestSetPush(t *testing.T) (http.Handler, *httpadapter.InMemTestSetStore, *events.InMemoryPublisher) {
	t.Helper()
	store := httpadapter.NewInMemTestSetStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	sub := subscribers.NewBatchTestSetSubscriber(store, nil, pub, idempotent.NewMemoryStore())
	h := httpadapter.NewBatchTestSetInboxPushHandler(httpadapter.BatchTestSetInboxPushDeps{
		Subscriber: sub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	return h, store, pub
}

func buildBatchAcceptedWire(t *testing.T) []byte {
	t.Helper()
	msg := &creationv1.QuestionBatchAccepted{
		Envelope: &commonv1.EventEnvelope{
			EventId:     "01985e7f-eeee-7000-8000-000000000099",
			TenantId:    bpTenant,
			Gcid:        bpAuthor,
			Traceparent: "00-abc-def-01",
		},
		JobId:      bpJobID,
		HostAtomId: "01985e7f-cccc-7000-8000-000000000099",
		TenantId:   bpTenant,
		AuthorGcid: bpAuthor,
		TestSet: &creationv1.QuestionBatchAccepted_TestSetSpec{
			Title:       "Pushed batch set",
			Description: "from push",
		},
		Items: []*creationv1.QuestionBatchAccepted_Item{
			{
				QuestionAtomId: "01985e7f-aaaa-7000-8000-000000000011",
				QuestionId:     "01985e7f-bbbb-7000-8000-000000000011",
				QuestionType:   "mcq",
				Points:         5,
				DisplayOrder:   1,
			},
			{
				QuestionAtomId: "01985e7f-aaaa-7000-8000-000000000012",
				QuestionId:     "01985e7f-bbbb-7000-8000-000000000012",
				QuestionType:   "oe",
				Points:         15,
				DisplayOrder:   2,
			},
		},
		AcceptedAt: timestamppb.Now(),
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return raw
}

func postBatchPush(t *testing.T, h http.Handler, data []byte, attrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	envelope := map[string]any{
		"message": map[string]any{
			"data":        base64.StdEncoding.EncodeToString(data),
			"messageId":   "m-1",
			"publishTime": "2026-06-10T03:04:05Z",
			"attributes":  attrs,
		},
		"subscription": "projects/chora-489812/subscriptions/chora-delivery.creation-question_batch-accepted",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal push envelope: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/internal/pubsub/batch-testset-inbox", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func batchPushAttrs() map[string]string {
	return map[string]string{
		"topic":           "chora.creation.question_batch.accepted.v1",
		"event_id":        "01985e7f-eeee-7000-8000-000000000099",
		"idempotency_key": "idem-99",
		"tenant_id":       bpTenant,
		"gcid":            bpAuthor,
		"traceparent":     "00-abc-def-01",
		"source_service":  "chora-creation",
		"schema_version":  "1",
	}
}

func TestBatchTestSetPush_BinaryEvent_AssemblesDraft(t *testing.T) {
	h, store, pub := newBatchTestSetPush(t)

	w := postBatchPush(t, h, buildBatchAcceptedWire(t), batchPushAttrs())
	if w.Code != http.StatusOK {
		t.Fatalf("push status: got %d body %s", w.Code, w.Body.String())
	}

	ts, ok, err := store.GetBySourceJobID(context.Background(), bpTenant, bpJobID)
	if err != nil || !ok {
		t.Fatalf("assembled test set not found: ok=%v err=%v", ok, err)
	}
	if ts.Title != "Pushed batch set" || ts.AuthorGCID != bpAuthor {
		t.Fatalf("assembled fields: %+v", ts)
	}
	qs := ts.Questions()
	if len(qs) != 2 || qs[0].QuestionType != "mcq" || qs[1].Points != 15 {
		t.Fatalf("assembled questions: %+v", qs)
	}

	found := false
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.test_set.created.v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected test_set.created.v1 emit")
	}
}

func TestBatchTestSetPush_DuplicateDelivery_200NoSecondSet(t *testing.T) {
	h, store, _ := newBatchTestSetPush(t)
	wire := buildBatchAcceptedWire(t)

	if w := postBatchPush(t, h, wire, batchPushAttrs()); w.Code != http.StatusOK {
		t.Fatalf("first push: %d", w.Code)
	}
	// Redelivery with a different broker event id attribute — the
	// source_job_id existence check must no-op it.
	attrs := batchPushAttrs()
	attrs["event_id"] = "01985e7f-eeee-7000-8000-000000000100"
	if w := postBatchPush(t, h, wire, attrs); w.Code != http.StatusOK {
		t.Fatalf("duplicate push must 200 (ack): %d", w.Code)
	}

	count := 0
	for _, ts := range storeRows(t, store) {
		if ts.SourceJobID != nil && *ts.SourceJobID == bpJobID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 assembled set; got %d", count)
	}
}

// storeRows peeks the in-memory store via List (default states).
func storeRows(t *testing.T, store *httpadapter.InMemTestSetStore) []*domain.TestSet {
	t.Helper()
	items, _, err := store.List(context.Background(), bpTenant, domain.TestSetListFilter{PageSize: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return items
}

func TestBatchTestSetPush_WrongTopic_AckDrop(t *testing.T) {
	h, store, _ := newBatchTestSetPush(t)
	attrs := batchPushAttrs()
	attrs["topic"] = "chora.creation.atom.created.v1"

	w := postBatchPush(t, h, buildBatchAcceptedWire(t), attrs)
	if w.Code != http.StatusOK {
		t.Fatalf("foreign topic must ack-drop with 200; got %d", w.Code)
	}
	if _, ok, _ := store.GetBySourceJobID(context.Background(), bpTenant, bpJobID); ok {
		t.Fatal("foreign topic must not assemble")
	}
}

func TestBatchTestSetPush_EventTopicAttrFallback(t *testing.T) {
	h, store, _ := newBatchTestSetPush(t)
	attrs := batchPushAttrs()
	delete(attrs, "topic")
	attrs["event_topic"] = "chora.creation.question_batch.accepted.v1"

	w := postBatchPush(t, h, buildBatchAcceptedWire(t), attrs)
	if w.Code != http.StatusOK {
		t.Fatalf("event_topic fallback: %d", w.Code)
	}
	if _, ok, _ := store.GetBySourceJobID(context.Background(), bpTenant, bpJobID); !ok {
		t.Fatal("event_topic-routed delivery must assemble")
	}
}

func TestBatchTestSetPush_JSONFallbackProducer(t *testing.T) {
	// Transition-window JSON producer (or DLQ replay tooling): same shape,
	// JSON-encoded. The decode chain must land the same assembly.
	h, store, _ := newBatchTestSetPush(t)
	body := map[string]any{
		"job_id":       bpJobID,
		"host_atom_id": "01985e7f-cccc-7000-8000-000000000099",
		"tenant_id":    bpTenant,
		"author_gcid":  bpAuthor,
		"test_set":     map[string]any{"title": "JSON batch set", "description": ""},
		"items": []map[string]any{
			{
				"question_atom_id": "01985e7f-aaaa-7000-8000-000000000011",
				"question_id":      "01985e7f-bbbb-7000-8000-000000000011",
				"question_type":    "mcq",
				"points":           5,
				"display_order":    1,
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal json payload: %v", err)
	}
	w := postBatchPush(t, h, raw, batchPushAttrs())
	if w.Code != http.StatusOK {
		t.Fatalf("json fallback push: %d body %s", w.Code, w.Body.String())
	}
	ts, ok, _ := store.GetBySourceJobID(context.Background(), bpTenant, bpJobID)
	if !ok || ts.Title != "JSON batch set" || len(ts.Questions()) != 1 {
		t.Fatalf("json fallback assembly: ok=%v ts=%+v", ok, ts)
	}
}

func TestNewBatchTestSetInboxPushHandler_PanicsOnNilSubscriber(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil Subscriber")
		}
	}()
	_ = httpadapter.NewBatchTestSetInboxPushHandler(httpadapter.BatchTestSetInboxPushDeps{})
}
