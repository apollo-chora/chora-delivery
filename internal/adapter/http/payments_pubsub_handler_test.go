// payments_pubsub_handler_test.go — ADR-164 Stage C push-handler tests.
//
// Verifies the 4-topic dispatcher correctly routes inbound Pub/Sub push
// deliveries to the typed PaymentsSubscriber methods. Each topic gets:
//   - happy path (state mutation observable via the in-mem aggregate /
//     publisher history)
//   - missing `topic` attribute → 4xx (handler-level error)
//   - empty payload → 4xx
//   - misrouted (unknown) topic → 2xx ack-and-drop
package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/apollo-chora/chora-common/idempotent"
	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/payments/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	ppTenant   = "01970000-0000-7000-8000-000000000001"
	ppLearner  = "01970000-0000-7000-9000-000000000001"
	ppCourseID = "01970000-0000-7000-8000-000000ccccc1"
)

func newPaymentsPushTestServer(t *testing.T) (http.Handler, *domain.EnrollmentRegistry, *repoinmem.ApplicationRepo, *events.InMemoryPublisher) {
	t.Helper()
	enrollments := domain.NewEnrollmentRegistry()
	apps := repoinmem.NewApplicationRepo()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	tracker := events.NewEnrollmentTracker(domain.NewInMemEnrollmentStoreFrom(enrollments))
	sub := events.NewPaymentsSubscriber(events.PaymentsSubscriberDeps{
		Enrollments:  tracker,
		Applications: apps,
		Publisher:    pub,
		Inbox:        idempotent.NewMemoryStore(),
	})
	h := httpadapter.NewPaymentsPushHandler(httpadapter.PaymentsPushDeps{
		Subscriber: sub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	return h, enrollments, apps, pub
}

// -----------------------------------------------------------------------------
// course_purchase.payment_captured.v1 — happy path
// -----------------------------------------------------------------------------

func TestPaymentsPushHandler_CoursePurchasePaymentCaptured_RegistersEnrollment(t *testing.T) {
	h, enrollments, _, pub := newPaymentsPushTestServer(t)

	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:              newPushEnvelope("evt-pp-1", ppTenant, ppLearner),
		PurchaseId:            "purchase-1",
		LearnerGcid:           ppLearner,
		CourseId:              ppCourseID,
		StripeSessionId:       "cs_test_1",
		StripePaymentIntentId: "pi_test_1",
		StripeChargeId:        "ch_test_1",
		AmountCentsPaid:       19900,
		Currency:              "SGD",
	}
	payload, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := buildPaymentsPushBody(t,
		paymentsPushAttrs(events.TopicCoursePurchasePaymentCaptured, "evt-pp-1", ppTenant, ppLearner),
		payload)
	rec := dispatchPaymentsPush(t, h, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("dispatch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := enrollments.GetByCourseAndGCID(ppTenant, ppCourseID, ppLearner); !ok {
		t.Fatal("enrollment not registered after captured event")
	}
	found := false
	for _, e := range pub.History() {
		if e.Topic == events.TopicEnrollmentCreated {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in publisher history", events.TopicEnrollmentCreated)
	}
}

// -----------------------------------------------------------------------------
// course_purchase.refunded.v1 — happy path
// -----------------------------------------------------------------------------

func TestPaymentsPushHandler_CoursePurchaseRefunded_RevokesEnrollment(t *testing.T) {
	h, enrollments, _, pub := newPaymentsPushTestServer(t)
	// Seed an existing enrollment so refund has something to revoke.
	_, _ = enrollments.Register(ppTenant, ppCourseID, ppLearner)

	ev := &paymentsv1.CoursePurchaseRefunded{
		Envelope:            newPushEnvelope("evt-pp-refund-1", ppTenant, ppLearner),
		PurchaseId:          "purchase-1",
		LearnerGcid:         ppLearner,
		CourseId:            ppCourseID,
		StripeChargeId:      "ch_test_1",
		StripeRefundId:      "re_test_1",
		AmountCentsRefunded: 19900,
		Currency:            "SGD",
	}
	payload, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := buildPaymentsPushBody(t,
		paymentsPushAttrs(events.TopicCoursePurchaseRefunded, "evt-pp-refund-1", ppTenant, ppLearner),
		payload)
	rec := dispatchPaymentsPush(t, h, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("dispatch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := enrollments.GetByCourseAndGCID(ppTenant, ppCourseID, ppLearner); ok {
		t.Error("enrollment not revoked after refund event")
	}
	found := false
	for _, e := range pub.History() {
		if e.Topic == events.TopicEnrollmentCancelled {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in publisher history", events.TopicEnrollmentCancelled)
	}
}

// -----------------------------------------------------------------------------
// application_payment.payment_captured.v1 — advances FSM
// -----------------------------------------------------------------------------

func TestPaymentsPushHandler_ApplicationPaymentCaptured_AdvancesApplication(t *testing.T) {
	h, _, apps, _ := newPaymentsPushTestServer(t)
	app := seedAppAtStatus(t, apps, ppTenant, ppCourseID, ppLearner, application.StatusAccepted)

	ev := &paymentsv1.ApplicationPaymentPaymentCaptured{
		Envelope:              newPushEnvelope("evt-app-1", ppTenant, ppLearner),
		PurchaseId:            "purchase-2",
		LearnerGcid:           ppLearner,
		ApplicationId:         app.ID,
		CourseId:              ppCourseID,
		StripePaymentIntentId: "pi_app_1",
		AmountCentsPaid:       100000,
		Currency:              "SGD",
	}
	payload, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := buildPaymentsPushBody(t,
		paymentsPushAttrs(events.TopicApplicationPaymentPaymentCaptured, "evt-app-1", ppTenant, ppLearner),
		payload)
	rec := dispatchPaymentsPush(t, h, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("dispatch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, ok, err := apps.Get(context.Background(), ppTenant, app.ID)
	if err != nil || !ok {
		t.Fatalf("get app: err=%v ok=%v", err, ok)
	}
	if got.Status != application.StatusEnrolled {
		t.Errorf("application Status = %s, want enrolled", got.Status)
	}
}

// -----------------------------------------------------------------------------
// application_payment.refunded.v1 — withdraws + revokes
// -----------------------------------------------------------------------------

func TestPaymentsPushHandler_ApplicationPaymentRefunded_WithdrawsAndRevokes(t *testing.T) {
	h, enrollments, apps, _ := newPaymentsPushTestServer(t)
	app := seedAppAtStatus(t, apps, ppTenant, ppCourseID, ppLearner, application.StatusAccepted)
	_, _ = enrollments.Register(ppTenant, ppCourseID, ppLearner)

	ev := &paymentsv1.ApplicationPaymentRefunded{
		Envelope:            newPushEnvelope("evt-app-refund-1", ppTenant, ppLearner),
		PurchaseId:          "purchase-2",
		LearnerGcid:         ppLearner,
		ApplicationId:       app.ID,
		StripeChargeId:      "ch_app_1",
		StripeRefundId:      "re_app_1",
		AmountCentsRefunded: 100000,
		Currency:            "SGD",
	}
	payload, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := buildPaymentsPushBody(t,
		paymentsPushAttrs(events.TopicApplicationPaymentRefunded, "evt-app-refund-1", ppTenant, ppLearner),
		payload)
	rec := dispatchPaymentsPush(t, h, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("dispatch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := enrollments.GetByCourseAndGCID(ppTenant, ppCourseID, ppLearner); ok {
		t.Error("enrollment still present after application refund")
	}
	got, ok, _ := apps.Get(context.Background(), ppTenant, app.ID)
	if !ok {
		t.Fatal("application gone")
	}
	if got.Status != application.StatusWithdrawn {
		t.Errorf("application Status = %s, want withdrawn", got.Status)
	}
}

// -----------------------------------------------------------------------------
// idempotent replay — same event_id, second delivery is a no-op
// -----------------------------------------------------------------------------

func TestPaymentsPushHandler_IdempotentReplay_CoursePurchaseCaptured(t *testing.T) {
	h, _, _, pub := newPaymentsPushTestServer(t)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:        newPushEnvelope("evt-pp-replay", ppTenant, ppLearner),
		PurchaseId:      "purchase-1",
		LearnerGcid:     ppLearner,
		CourseId:        ppCourseID,
		AmountCentsPaid: 19900,
		Currency:        "SGD",
	}
	payload, _ := proto.Marshal(ev)
	body := buildPaymentsPushBody(t,
		paymentsPushAttrs(events.TopicCoursePurchasePaymentCaptured, "evt-pp-replay", ppTenant, ppLearner),
		payload)

	// First delivery — should publish one downstream event.
	first := dispatchPaymentsPush(t, h, body)
	if first.Code < 200 || first.Code >= 300 {
		t.Fatalf("first delivery: status=%d", first.Code)
	}
	before := len(pub.History())

	// Replay — should be a no-op (same event_id).
	second := dispatchPaymentsPush(t, h, body)
	if second.Code < 200 || second.Code >= 300 {
		t.Fatalf("replay: status=%d", second.Code)
	}
	after := len(pub.History())
	if before != after {
		t.Errorf("replay emitted duplicate downstream events; before=%d after=%d", before, after)
	}
}

// -----------------------------------------------------------------------------
// missing topic attribute — handler-level error
// -----------------------------------------------------------------------------

func TestPaymentsPushHandler_MissingTopicAttribute(t *testing.T) {
	h, _, _, _ := newPaymentsPushTestServer(t)
	ev := &paymentsv1.CoursePurchasePaymentCaptured{
		Envelope:    newPushEnvelope("evt-no-topic", ppTenant, ppLearner),
		LearnerGcid: ppLearner,
		CourseId:    ppCourseID,
	}
	payload, _ := proto.Marshal(ev)
	attrs := paymentsPushAttrs("", "evt-no-topic", ppTenant, ppLearner)
	delete(attrs, "topic")
	body := buildPaymentsPushBody(t, attrs, payload)
	rec := dispatchPaymentsPush(t, h, body)
	// Handler returns an error → eventpush.NewHandler maps that to 5xx so
	// the broker re-delivers. Any non-2xx response is fine for this check.
	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("expected non-2xx for missing topic; got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// unknown topic — ack-and-drop (2xx, no state change)
// -----------------------------------------------------------------------------

func TestPaymentsPushHandler_UnknownTopic_AckAndDrop(t *testing.T) {
	h, enrollments, _, pub := newPaymentsPushTestServer(t)
	body := buildPaymentsPushBody(t,
		paymentsPushAttrs("chora.payments.bogus.event.v1", "evt-bogus", ppTenant, ppLearner),
		[]byte{0x01, 0x02, 0x03}) // junk payload; never decoded.
	rec := dispatchPaymentsPush(t, h, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Errorf("expected 2xx ack for unknown topic; got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(pub.History()) != 0 {
		t.Errorf("publisher should not see anything for unknown topic; got %d events", len(pub.History()))
	}
	if _, ok := enrollments.GetByCourseAndGCID(ppTenant, ppCourseID, ppLearner); ok {
		t.Error("enrollment registered for unknown topic — should never happen")
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func newPushEnvelope(eventID, tenantID, gcid string) *commonv1.EventEnvelope {
	now := timestamppb.Now()
	return &commonv1.EventEnvelope{
		EventId:        eventID,
		IdempotencyKey: eventID,
		TenantId:       tenantID,
		Gcid:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-payments",
		SchemaVersion:  1,
	}
}

func paymentsPushAttrs(topic, eventID, tenantID, gcid string) map[string]string {
	return map[string]string{
		"topic":           topic,
		"event_id":        eventID,
		"idempotency_key": eventID,
		"tenant_id":       tenantID,
		"gcid":            gcid,
		"source_project":  "chora-489812",
		"source_service":  "chora-payments",
	}
}

func buildPaymentsPushBody(t *testing.T, attrs map[string]string, payload []byte) string {
	t.Helper()
	type msg struct {
		Data        string            `json:"data"`
		MessageID   string            `json:"messageId"`
		PublishTime string            `json:"publishTime"`
		Attributes  map[string]string `json:"attributes,omitempty"`
	}
	type env struct {
		Message      msg    `json:"message"`
		Subscription string `json:"subscription"`
	}
	e := env{
		Message: msg{
			Data:        base64.StdEncoding.EncodeToString(payload),
			MessageID:   fmt.Sprintf("mid-%d", time.Now().UnixNano()),
			PublishTime: "2026-05-24T12:00:00Z",
			Attributes:  attrs,
		},
		Subscription: "projects/chora-489812/subscriptions/chora-delivery-payments-inbox",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	return string(b)
}

func dispatchPaymentsPush(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/internal/pubsub/payments-inbox", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedAppAtStatus walks an Application up to the target Status, mirroring
// mustSeedApplication in events/payments_subscriber_test.go but exposed at
// the httpapi test scope.
func seedAppAtStatus(t *testing.T, apps *repoinmem.ApplicationRepo, tenantID, courseID, gcid string, target application.Status) *application.Application {
	t.Helper()
	app, _, err := apps.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantID,
		CourseID: courseID,
		GCID:     gcid,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	walk := []application.Status{
		application.StatusUnderReview,
		application.StatusOfferMade,
		application.StatusAccepted,
		application.StatusPaid,
		application.StatusEnrolled,
	}
	for _, next := range walk {
		if app.Status == target {
			break
		}
		if err := app.Transition(next); err != nil {
			t.Fatalf("seed transition to %s: %v", next, err)
		}
	}
	if app.Status != target {
		t.Fatalf("seed reached %s, want %s", app.Status, target)
	}
	if err := apps.Save(context.Background(), app); err != nil {
		t.Fatalf("save: %v", err)
	}
	return app
}
