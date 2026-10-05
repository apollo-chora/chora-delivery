// course_progress_pubsub_handler_test.go - the two ASYNC-analytics push
// endpoints must decode the REAL consumption wire format (schemaless JSON, not
// protobuf) into the projection, and fail loud (non-2xx → DLQ) on a topic
// misroute or a missing envelope attribute.
//
// The payload fixtures below are byte-for-byte the maps chora-consumption's
// session_completion.go actually publishes - including `progress_percent`, which
// the projection deliberately IGNORES because it is a 0..1 FRACTION despite its
// name. A test that asserted against a payload we invented would prove nothing
// about the topic we actually consume.
package httpapi_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"
	httpadapter "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

const (
	cppTenant = "11111111-1111-7111-8111-111111111111"
	cppGCID   = "00000000-0000-7000-9000-0000000000a1"
	cppCourse = "01985e7f-5555-7abc-8def-000000000c01"
	cppPath   = "01985e7f-5555-7abc-8def-000000000d01"
	cppOccur  = "2026-07-16T10:00:00Z"
)

func newCPHandlers(t *testing.T) (http.Handler, http.Handler, *courseprogress.InMemProgressStore) {
	t.Helper()
	store := courseprogress.NewInMemProgressStore()
	sub := subscribers.NewCourseProgressSubscriber(store, nil)
	verifier := eventpush.NewVerifier(eventpush.VerifierConfig{}) // disabled (dev): accepts all
	adv := httpadapter.NewCourseProgressAdvancedPushHandler(httpadapter.CourseProgressPushDeps{Subscriber: sub, Verifier: verifier})
	done := httpadapter.NewCourseProgressCompletedPushHandler(httpadapter.CourseProgressPushDeps{Subscriber: sub, Verifier: verifier})
	return adv, done, store
}

func buildCPPush(t *testing.T, attrs map[string]string, payload map[string]any) string {
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
			PublishTime: "2026-07-16T10:00:00Z",
			Attributes:  attrs,
		},
		Subscription: "projects/chora-489812/subscriptions/chora-delivery.course-progress-advanced",
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func cpAttrs(eventID, topic string) map[string]string {
	return map[string]string{
		"event_id":    eventID,
		"tenant_id":   cppTenant,
		"gcid":        cppGCID,
		"topic":       topic,
		"occurred_at": cppOccur,
	}
}

// advancedBody mirrors chora-consumption session_completion.go publishAdvanced.
func advancedBody() map[string]any {
	return map[string]any{
		"path_id":          cppPath,
		"course_id":        cppCourse,
		"learner_gcid":     cppGCID,
		"atom_id":          "01985e7f-5555-7abc-8def-000000000a01",
		"current_index":    3,
		"total_atoms":      10,
		"progress_percent": 0.3, // a 0..1 FRACTION - deliberately not consumed
	}
}

// completedBody mirrors chora-consumption session_completion.go publishCompleted.
func completedBody() map[string]any {
	return map[string]any{
		"path_id":      cppPath,
		"course_id":    cppCourse,
		"learner_gcid": cppGCID,
		"atom_count":   10,
		"completed_at": cppOccur,
	}
}

func postCP(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/internal/pubsub/course-progress-advanced-inbox", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// The JSON wire format decodes and the projection lands
// -----------------------------------------------------------------------------

func TestCourseProgressPush_AdvancedProjectsProgress(t *testing.T) {
	adv, _, store := newCPHandlers(t)
	w := postCP(t, adv, buildCPPush(t, cpAttrs("evt-1", subscribers.TopicLearningPathAdvanced), advancedBody()))
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("status: want 2xx (ack), got %d body=%s", w.Code, w.Body.String())
	}
	p, ok, err := store.GetByLearnerCourse(t.Context(), cppTenant, cppGCID, cppCourse)
	if err != nil || !ok {
		t.Fatalf("no projection row written: ok=%v err=%v", ok, err)
	}
	// 3/10 - derived from the INTEGERS, not from progress_percent (0.3 would be
	// 0.3% if the misnamed field were trusted as a percent).
	if p.CompletedAtoms != 3 || p.TotalAtoms != 10 {
		t.Fatalf("counts: want 3/10, got %d/%d", p.CompletedAtoms, p.TotalAtoms)
	}
	if got := p.ProgressFraction(); got != 0.3 {
		t.Fatalf("progress: want 0.3, got %v", got)
	}
	if p.LastAdvanceAt == nil {
		t.Fatal("last_advance_at must be stamped from the occurred_at attribute (the reorder watermark)")
	}
}

func TestCourseProgressPush_CompletedMarksComplete(t *testing.T) {
	_, done, store := newCPHandlers(t)
	w := postCP(t, done, buildCPPush(t, cpAttrs("evt-2", subscribers.TopicLearningPathCompleted), completedBody()))
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("status: want 2xx (ack), got %d body=%s", w.Code, w.Body.String())
	}
	p, ok, err := store.GetByLearnerCourse(t.Context(), cppTenant, cppGCID, cppCourse)
	if err != nil || !ok {
		t.Fatalf("no projection row written: ok=%v err=%v", ok, err)
	}
	if !p.IsComplete {
		t.Fatal("is_complete: want true")
	}
	if p.ProgressFraction() != 1.0 {
		t.Fatalf("a completed course must read as fully traversed, got %v", p.ProgressFraction())
	}
}

// The learner_gcid may be absent from the body and present only on the envelope.
func TestCourseProgressPush_FallsBackToGcidAttribute(t *testing.T) {
	adv, _, store := newCPHandlers(t)
	body := advancedBody()
	delete(body, "learner_gcid")
	w := postCP(t, adv, buildCPPush(t, cpAttrs("evt-3", subscribers.TopicLearningPathAdvanced), body))
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("status: want 2xx, got %d body=%s", w.Code, w.Body.String())
	}
	if _, ok, _ := store.GetByLearnerCourse(t.Context(), cppTenant, cppGCID, cppCourse); !ok {
		t.Fatal("projection must fall back to the gcid attribute")
	}
}

// -----------------------------------------------------------------------------
// Fail loud: a misroute or a missing envelope must NOT ack
// -----------------------------------------------------------------------------

// The ROUTE is the topic discriminator here (consumption does not always stamp
// the topic attribute), so a WRONG stamped topic is a real misroute and must
// dead-letter rather than be folded in as the wrong kind of event.
func TestCourseProgressPush_MisroutedTopicFailsLoud(t *testing.T) {
	adv, _, store := newCPHandlers(t)
	w := postCP(t, adv, buildCPPush(t, cpAttrs("evt-4", subscribers.TopicLearningPathCompleted), advancedBody()))
	if w.Code >= 200 && w.Code <= 299 {
		t.Fatalf("a misrouted topic must fail loud (non-2xx → DLQ), got %d", w.Code)
	}
	if _, ok, _ := store.GetByLearnerCourse(t.Context(), cppTenant, cppGCID, cppCourse); ok {
		t.Fatal("a misrouted event must not write a projection")
	}
}

// An ABSENT topic attribute is tolerated: the subscription targets this exact
// route, so the topic is known from the route itself.
func TestCourseProgressPush_AbsentTopicAttributeIsTolerated(t *testing.T) {
	adv, _, store := newCPHandlers(t)
	attrs := cpAttrs("evt-5", "")
	delete(attrs, "topic")
	w := postCP(t, adv, buildCPPush(t, attrs, advancedBody()))
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("an absent topic attribute must still process, got %d body=%s", w.Code, w.Body.String())
	}
	if _, ok, _ := store.GetByLearnerCourse(t.Context(), cppTenant, cppGCID, cppCourse); !ok {
		t.Fatal("projection must be written when the topic attribute is absent")
	}
}

func TestCourseProgressPush_MissingEventIDFailsLoud(t *testing.T) {
	adv, _, _ := newCPHandlers(t)
	attrs := cpAttrs("", subscribers.TopicLearningPathAdvanced)
	delete(attrs, "event_id")
	if w := postCP(t, adv, buildCPPush(t, attrs, advancedBody())); w.Code >= 200 && w.Code <= 299 {
		t.Fatalf("a missing event_id must fail loud, got %d", w.Code)
	}
}

func TestCourseProgressPush_MissingTenantFailsLoud(t *testing.T) {
	adv, _, _ := newCPHandlers(t)
	attrs := cpAttrs("evt-6", subscribers.TopicLearningPathAdvanced)
	delete(attrs, "tenant_id")
	if w := postCP(t, adv, buildCPPush(t, attrs, advancedBody())); w.Code >= 200 && w.Code <= 299 {
		t.Fatalf("a missing tenant_id must fail loud, got %d", w.Code)
	}
}

func TestCourseProgressPush_UndecodableBodyFailsLoud(t *testing.T) {
	adv, _, _ := newCPHandlers(t)
	// Binary protobuf bytes on a JSON topic: the exact regression that silently
	// dead-lettered the graded lane, in reverse.
	type msg struct {
		Data       string            `json:"data"`
		MessageID  string            `json:"messageId"`
		Attributes map[string]string `json:"attributes,omitempty"`
	}
	env := struct {
		Message      msg    `json:"message"`
		Subscription string `json:"subscription"`
	}{Message: msg{
		Data:       base64.StdEncoding.EncodeToString([]byte{0x08, 0x96, 0x01, 0xFF}),
		MessageID:  "mid-bad",
		Attributes: cpAttrs("evt-7", subscribers.TopicLearningPathAdvanced),
	}}
	b, _ := json.Marshal(env)
	if w := postCP(t, adv, string(b)); w.Code >= 200 && w.Code <= 299 {
		t.Fatalf("an undecodable body must fail loud, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// A path with no course is not a delivery target (ack, write nothing)
// -----------------------------------------------------------------------------

func TestCourseProgressPush_CourselessPathAcksWithoutWriting(t *testing.T) {
	adv, _, store := newCPHandlers(t)
	body := advancedBody()
	delete(body, "course_id") // a study-list / collection-derived path
	w := postCP(t, adv, buildCPPush(t, cpAttrs("evt-8", subscribers.TopicLearningPathAdvanced), body))
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("a course-less path must ACK (it can never gain a course), got %d", w.Code)
	}
	rows, err := store.ListByCourseIDs(t.Context(), cppTenant, []string{cppCourse})
	if err != nil {
		t.Fatalf("ListByCourseIDs: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a course-less path must write no projection, got %d rows", len(rows))
	}
}
