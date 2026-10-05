// exam_result_pubsub_handler_test.go - the EXAM-mode auto-cert push inbox must
// decode the REAL wire format (Schema-Registry BINARY protobuf, NOT JSON),
// dispatch a PASS into a durable certificate, and fail loud (non-2xx -> DLQ) on
// a topic misroute or a missing envelope attribute.
//
// It is tested THROUGH THE MUX (never by calling the handler directly) and
// asserts the EFFECT (a certificate exists), not merely a 200 - the CHO-2195 /
// F3 lesson: a constructed-but-unmounted handler is indistinguishable from no
// handler, and a decoder-only fix can return 200 and still leave the store empty.
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

	"github.com/apollo-chora/chora-common/tracing"
	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	deliverydomain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	erpTenant  = "11111111-1111-7111-8111-111111111111"
	erpLearner = "00000000-0000-7000-8000-000000004888"
	erpCourse  = "019edf3e-b405-707a-b3ef-851fd4e38e78"
	erpTitle   = "PMP Proctored Exam"
)

// newExamResultPushWorld seeds a real exam in the inmem repo (no fake), wires the
// engine + push handler, and mounts it on a mux at the canonical inbox path.
func newExamResultPushWorld(t *testing.T) (http.Handler, *deliverydomain.CertificationRegistry, string) {
	t.Helper()
	exams := inmem.NewExamRepo()
	ex, err := exam.NewExam(exam.NewExamInput{
		TenantID: erpTenant, CourseID: erpCourse, Title: erpTitle,
		DurationMinutes: 60, Capacity: 5,
	})
	if err != nil {
		t.Fatalf("NewExam: %v", err)
	}
	if err := exams.Save(tracing.WithTenantID(context.Background(), erpTenant), ex); err != nil {
		t.Fatalf("seed exam: %v", err)
	}
	certs := deliverydomain.NewCertificationRegistry()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	sub := subscribers.NewExamResultCertSubscriber(exams, certs, pub, nil)
	h := httpapi.NewExamResultReleasedPushHandler(httpapi.ExamResultPushDeps{
		Subscriber: sub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}), // disabled (dev): accepts all
	})
	mux := http.NewServeMux()
	mux.Handle("/api/internal/pubsub/exam-result-released-inbox", h)
	return mux, certs, ex.ID
}

// buildExamResultPush wraps raw binary-protobuf bytes in the Pub/Sub push
// envelope (base64 data + attributes), the real wire shape.
func buildExamResultPush(t *testing.T, attrs map[string]string, data []byte) string {
	t.Helper()
	type msg struct {
		Data        string            `json:"data"`
		MessageID   string            `json:"messageId"`
		PublishTime string            `json:"publishTime"`
		Attributes  map[string]string `json:"attributes,omitempty"`
	}
	env := struct {
		Message      msg    `json:"message"`
		Subscription string `json:"subscription"`
	}{
		Message: msg{
			Data:        base64.StdEncoding.EncodeToString(data),
			MessageID:   fmt.Sprintf("mid-%d", time.Now().UnixNano()),
			PublishTime: "2026-07-16T12:00:00Z",
			Attributes:  attrs,
		},
		Subscription: "projects/chora-489812/subscriptions/chora-delivery.exam_result-released",
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func postExamResult(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/internal/pubsub/exam-result-released-inbox", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func marshalExamResult(t *testing.T, examID string, outcome deliveryv1.ExamResultOutcome) []byte {
	t.Helper()
	raw, err := proto.Marshal(&deliveryv1.ExamResultReleased{
		Envelope:     &commonv1.EventEnvelope{EventId: "evt-er-1", TenantId: erpTenant, Gcid: erpLearner},
		ResultId:     "019f6a03-3333-7aaa-8bbb-000000000009",
		ExamId:       examID,
		ExamFormId:   "019f6a02-2222-7aaa-8bbb-00000000000a",
		CandidateRef: erpLearner,
		TenantId:     erpTenant,
		Outcome:      outcome,
		RawScore:     42,
		MaxScore:     50,
		CutScore:     35,
	})
	if err != nil {
		t.Fatalf("marshal ExamResultReleased: %v", err)
	}
	return raw
}

// TestExamResultPush_Pass_IssuesCertificate_ThroughMux - a PASS posted through
// the mux issues a durable certificate anchored on the exam's course.
func TestExamResultPush_Pass_IssuesCertificate_ThroughMux(t *testing.T) {
	mux, certs, examID := newExamResultPushWorld(t)
	attrs := map[string]string{
		"topic":     events.TopicExamResultReleased,
		"event_id":  "evt-er-1",
		"tenant_id": erpTenant,
	}
	body := buildExamResultPush(t, attrs, marshalExamResult(t, examID, deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_PASS))
	rec := postExamResult(t, mux, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("expected 2xx ack; got %d (%s)", rec.Code, rec.Body.String())
	}
	cert, ok, err := certs.GetByLearnerCourseCtx(tracing.WithTenantID(context.Background(), erpTenant), erpTenant, erpLearner, erpCourse)
	if err != nil || !ok || cert == nil {
		t.Fatalf("expected a certificate through the mux; ok=%v err=%v", ok, err)
	}
	if len(cert.Accomplishments) == 0 || cert.Accomplishments[0] != "Passed: "+erpTitle {
		t.Fatalf("expected accomplishment %q; got %v", "Passed: "+erpTitle, cert.Accomplishments)
	}
}

// TestExamResultPush_Fail_NoCertificate_ThroughMux - a FAIL acks (2xx) but mints
// nothing.
func TestExamResultPush_Fail_NoCertificate_ThroughMux(t *testing.T) {
	mux, certs, examID := newExamResultPushWorld(t)
	attrs := map[string]string{
		"topic":     events.TopicExamResultReleased,
		"event_id":  "evt-er-fail",
		"tenant_id": erpTenant,
	}
	body := buildExamResultPush(t, attrs, marshalExamResult(t, examID, deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_FAIL))
	rec := postExamResult(t, mux, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("expected 2xx ack for FAIL; got %d", rec.Code)
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(tracing.WithTenantID(context.Background(), erpTenant), erpTenant, erpLearner, erpCourse); ok {
		t.Fatalf("a FAIL must not issue a certificate")
	}
}

// TestExamResultPush_WrongTopic_FailsLoud - a misrouted event must NOT issue a
// certificate off the wrong wire; it fails loud (non-2xx -> DLQ).
func TestExamResultPush_WrongTopic_FailsLoud(t *testing.T) {
	mux, certs, examID := newExamResultPushWorld(t)
	attrs := map[string]string{
		"topic":     "chora.delivery.submission.released.v1", // wrong topic
		"event_id":  "evt-er-x",
		"tenant_id": erpTenant,
	}
	body := buildExamResultPush(t, attrs, marshalExamResult(t, examID, deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_PASS))
	rec := postExamResult(t, mux, body)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("expected non-2xx for a topic misroute; got %d", rec.Code)
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(tracing.WithTenantID(context.Background(), erpTenant), erpTenant, erpLearner, erpCourse); ok {
		t.Fatalf("a misrouted event must not issue a certificate")
	}
}

// TestExamResultPush_MissingEventID_FailsLoud - no event_id ⇒ no idempotency
// anchor ⇒ fail loud rather than risk a double-issue on redelivery.
func TestExamResultPush_MissingEventID_FailsLoud(t *testing.T) {
	mux, _, examID := newExamResultPushWorld(t)
	attrs := map[string]string{
		"topic":     events.TopicExamResultReleased,
		"tenant_id": erpTenant,
	}
	body := buildExamResultPush(t, attrs, marshalExamResult(t, examID, deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_PASS))
	rec := postExamResult(t, mux, body)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("expected non-2xx for missing event_id; got %d", rec.Code)
	}
}
