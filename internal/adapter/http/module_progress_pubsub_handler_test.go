// module_progress_pubsub_handler_test.go — the two W7 StudentModuleProgress push
// endpoints must decode their distinct payloads into the right (kind, ref) and
// dispatch to the projection, and fail loud (non-2xx → DLQ) on a topic misroute
// or a missing envelope attribute.
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

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	httpadapter "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// capturingResolver records the (kind, ref, gcid) the projection asked to
// resolve, returning no targets (the handler decode/route is what's under test).
type capturingResolver struct {
	kind, ref, gcid, tenant string
	calls                   int
}

func (c *capturingResolver) ResolveEnrolledTargets(_ context.Context, tenantID, gcid, kind, ref string) ([]moduleprogress.CompletionTarget, error) {
	c.calls++
	c.tenant, c.gcid, c.kind, c.ref = tenantID, gcid, kind, ref
	return nil, nil
}

func newMPHandlers(t *testing.T) (http.Handler, http.Handler, *capturingResolver) {
	t.Helper()
	res := &capturingResolver{}
	proj := moduleprogress.NewProjector(res, module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore())
	sub := subscribers.NewModuleProgressInboxSubscriber(proj, nil)
	verifier := eventpush.NewVerifier(eventpush.VerifierConfig{}) // disabled (dev): accepts all
	atom := httpadapter.NewModuleProgressAtomPushHandler(httpadapter.ModuleProgressPushDeps{Subscriber: sub, Verifier: verifier})
	graded := httpadapter.NewModuleProgressGradedPushHandler(httpadapter.ModuleProgressPushDeps{Subscriber: sub, Verifier: verifier})
	return atom, graded, res
}

func buildMPPush(t *testing.T, attrs map[string]string, payload map[string]any) string {
	t.Helper()
	data, _ := json.Marshal(payload)
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
			PublishTime: "2026-07-08T12:00:00Z",
			Attributes:  attrs,
		},
		Subscription: "projects/chora-489812/subscriptions/chora-delivery-module-progress",
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// buildMPPushBinary mirrors buildMPPush but carries raw (binary-protobuf) bytes
// as the message DATA — the real wire format for Schema-Registry topics like
// chora.delivery.submission.graded.v1 (deliveryv1.SubmissionGraded), which is
// NOT JSON on the wire.
func buildMPPushBinary(t *testing.T, attrs map[string]string, data []byte) string {
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
			PublishTime: "2026-07-08T12:00:00Z",
			Attributes:  attrs,
		},
		Subscription: "projects/chora-489812/subscriptions/chora-delivery.module-progress-graded",
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func postMP(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestModuleProgressAtomPush_DecodesAtomRef(t *testing.T) {
	atom, _, res := newMPHandlers(t)
	attrs := map[string]string{
		"topic": subscribers.TopicAtomSessionCompleted, "event_id": "evt-atom-1", "tenant_id": "ten-1",
	}
	body := buildMPPush(t, attrs, map[string]any{"atom_id": "atom-42", "learner_gcid": "gcid-9"})
	rec := postMP(t, atom, "/api/internal/pubsub/module-progress-atom-inbox", body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("expected 2xx ack; got %d", rec.Code)
	}
	if res.calls != 1 {
		t.Fatalf("expected the projection to resolve once; got %d", res.calls)
	}
	if res.kind != subscribers.ModuleProgressContentAtom || res.ref != "atom-42" || res.gcid != "gcid-9" || res.tenant != "ten-1" {
		t.Fatalf("bad resolve args: kind=%q ref=%q gcid=%q tenant=%q", res.kind, res.ref, res.gcid, res.tenant)
	}
}

// TestModuleProgressGradedPush_DecodesAssessmentRef feeds the REAL wire format:
// chora.delivery.submission.graded.v1 is a Schema-Registry BINARY protobuf topic
// (deliveryv1.SubmissionGraded), NOT JSON. This is a regression guard for the
// defect where the graded handler json.Unmarshal'd a protobuf body — every real
// graded event failed to decode → NACK → DLQ, so the graded→StudentModuleProgress
// projection never advanced (only the unschema'd atom topic worked).
func TestModuleProgressGradedPush_DecodesAssessmentRef(t *testing.T) {
	_, graded, res := newMPHandlers(t)
	attrs := map[string]string{
		"topic": subscribers.TopicSubmissionGraded, "event_id": "evt-grade-1", "tenant_id": "ten-1",
	}
	raw, err := proto.Marshal(&deliveryv1.SubmissionGraded{
		Envelope:     &commonv1.EventEnvelope{EventId: "evt-grade-1", TenantId: "ten-1", Gcid: "gcid-3"},
		AssessmentId: "asmt-7",
		LearnerGcid:  "gcid-3",
	})
	if err != nil {
		t.Fatalf("marshal SubmissionGraded: %v", err)
	}
	body := buildMPPushBinary(t, attrs, raw)
	rec := postMP(t, graded, "/api/internal/pubsub/module-progress-graded-inbox", body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("expected 2xx ack; got %d", rec.Code)
	}
	if res.kind != subscribers.ModuleProgressContentAssess || res.ref != "asmt-7" || res.gcid != "gcid-3" {
		t.Fatalf("bad resolve args: kind=%q ref=%q gcid=%q", res.kind, res.ref, res.gcid)
	}
}

func TestModuleProgressPush_WrongTopic_FailsLoud(t *testing.T) {
	atom, _, res := newMPHandlers(t)
	attrs := map[string]string{
		"topic":    subscribers.TopicSubmissionGraded, // graded topic on the atom endpoint
		"event_id": "evt-x", "tenant_id": "ten-1",
	}
	body := buildMPPush(t, attrs, map[string]any{"atom_id": "atom-1", "learner_gcid": "g"})
	rec := postMP(t, atom, "/api/internal/pubsub/module-progress-atom-inbox", body)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("expected non-2xx for a topic misroute; got %d", rec.Code)
	}
	if res.calls != 0 {
		t.Fatalf("a misrouted delivery must not reach the projection; got %d calls", res.calls)
	}
}

func TestModuleProgressPush_MissingEventID_FailsLoud(t *testing.T) {
	atom, _, res := newMPHandlers(t)
	attrs := map[string]string{"topic": subscribers.TopicAtomSessionCompleted, "tenant_id": "ten-1"}
	body := buildMPPush(t, attrs, map[string]any{"atom_id": "atom-1", "learner_gcid": "g"})
	rec := postMP(t, atom, "/api/internal/pubsub/module-progress-atom-inbox", body)
	if rec.Code >= 200 && rec.Code < 300 {
		t.Fatalf("expected non-2xx for missing event_id; got %d", rec.Code)
	}
	if res.calls != 0 {
		t.Fatalf("no dispatch expected on envelope failure; got %d calls", res.calls)
	}
}
