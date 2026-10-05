// testset_handler_test.go — HTTP-layer tests for A+ X.2 test-set authoring.
//
// Lane A (B-FE-X5). Exercises the 6 endpoints against an in-memory test-set
// store + in-memory publisher, asserting:
//
//   - happy-path 200/201/204 status codes
//   - state-guard 409 envelope on PUBLISHED mutations
//   - 404 envelope on unknown ids
//   - publish 409 envelopes (DELIVERY_TEST_SET_NO_QUESTIONS)
//   - publish 200 idempotent re-publish
//   - role-based authorisation (instructor / admin via x-mesh-user-roles)
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// newTestSetServer wires a server with the test-set port + an in-memory
// publisher. Other deps that are not exercised here remain unset.
func newTestSetServer() (http.Handler, *events.InMemoryPublisher) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		TestSets:  httpapi.NewInMemTestSetStore(),
		Publisher: pub,
	})
	return srv, pub
}

// reqInstructor sets the JWT-trust + role headers an instructor caller has.
func reqInstructor(t *testing.T, srv http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	req.Header.Set("x-mesh-user-roles", "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// reqLearner sets headers for a non-author caller without instructor role.
func reqLearner(t *testing.T, srv http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidB) // not the author
	// no x-mesh-user-roles header
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// decode returns the JSON map from a response body.
func decodeMap(t *testing.T, r *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	out := map[string]interface{}{}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode JSON: %v; body=%q", err, r.Body.String())
	}
	return out
}

// -----------------------------------------------------------------------------
// Create + Get
// -----------------------------------------------------------------------------

func TestCreateTestSet_HappyPath(t *testing.T) {
	srv, _ := newTestSetServer()
	w := reqInstructor(t, srv, http.MethodPost, "/api/v1/test-sets",
		map[string]interface{}{
			"title":       "Phyllis Math Test Set 1",
			"description": "Smoke test",
		})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201; got %d (body=%s)", w.Code, w.Body.String())
	}
	body := decodeMap(t, w)
	if id, _ := body["id"].(string); id == "" {
		t.Fatalf("expected id in response; body=%v", body)
	}
	if state, _ := body["state"].(string); state != "DRAFT" {
		t.Fatalf("expected state=DRAFT; got %q", state)
	}
}

func TestCreateTestSet_RejectsBlankTitle(t *testing.T) {
	srv, _ := newTestSetServer()
	w := reqInstructor(t, srv, http.MethodPost, "/api/v1/test-sets",
		map[string]interface{}{"title": "  "})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestCreateTestSet_LearnerWithoutRole_403(t *testing.T) {
	srv, _ := newTestSetServer()
	w := reqLearner(t, srv, http.MethodPost, "/api/v1/test-sets",
		map[string]interface{}{"title": "Should-Fail"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403; got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestGetTestSet_HappyPath(t *testing.T) {
	srv, _ := newTestSetServer()
	// Create.
	created := reqInstructor(t, srv, http.MethodPost, "/api/v1/test-sets",
		map[string]interface{}{"title": "Phyllis Test"})
	body := decodeMap(t, created)
	id, _ := body["id"].(string)
	// Get.
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets/"+id, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	if out["state"] != "DRAFT" {
		t.Fatalf("expected state=DRAFT; got %v", out["state"])
	}
	if _, ok := out["questions"]; !ok {
		t.Fatalf("expected questions field in detail DTO")
	}
}

func TestGetTestSet_UnknownID_404(t *testing.T) {
	srv, _ := newTestSetServer()
	w := reqInstructor(t, srv, http.MethodGet,
		"/api/v1/test-sets/01985e7f-0000-7000-8000-000000000999", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404; got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Add / Update / Remove question
// -----------------------------------------------------------------------------

func TestAddQuestion_HappyPath_201(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)

	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/questions",
		map[string]interface{}{
			"question_atom_id": "00000000-0000-7000-8000-00000000a0a2",
			"question_id":      "019e2ba6-7353-73f5-b517-dd627e76d450",
			"question_type":    "mcq",
			"points":           2.0,
		})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201; got %d (body=%s)", w.Code, w.Body.String())
	}
}

// LEG3-D R3 Option B — request schema requires explicit question_id.
//
// Per docs/m13/wave3-leg3d-round3-blocker-2026-05-16.md + user "go B"
// decision: chora-delivery does NOT resolve atom_id → question_id at
// add-question time (that's Option A). The caller (FE picker) extracts
// mcq_payload.question_id from the loaded atom payload before POST. The
// handler MUST reject 400 when question_id is missing so an absent picker
// resolution fails loud at add-time instead of bubbling up to a publish-
// time 502 from chora-creation.SnapshotQuestionByID.
func TestHandleAddTestSetQuestion_RejectsMissingQuestionID(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)

	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/questions",
		map[string]interface{}{
			"question_atom_id": "00000000-0000-7000-8000-00000000a0a2",
			// question_id intentionally omitted.
			"question_type": "mcq",
			"points":        2.0,
		})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when question_id missing; got %d (body=%s)",
			w.Code, w.Body.String())
	}
}

// LEG3-D R3 Option B — handler stores question_id distinct from atom_id and
// echoes both in the response DTO.
func TestHandleAddTestSetQuestion_AcceptsDistinctQuestionID(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)

	const (
		atomID = "00000000-0000-7000-8000-00000000a0a2"
		qID    = "019e2ba6-7353-73f5-b517-dd627e76d450"
	)

	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/questions",
		map[string]interface{}{
			"question_atom_id": atomID,
			"question_id":      qID,
			"question_type":    "mcq",
			"points":           2.0,
		})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201; got %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	if got, _ := out["question_atom_id"].(string); got != atomID {
		t.Errorf("question_atom_id: want %q; got %q", atomID, got)
	}
	if got, _ := out["question_id"].(string); got != qID {
		t.Errorf("question_id: want %q; got %q", qID, got)
	}
	if out["question_atom_id"] == out["question_id"] {
		t.Errorf("question_atom_id and question_id MUST echo distinct values; both = %q",
			out["question_atom_id"])
	}
}

func TestUpdateQuestion_HappyPath_200(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	qID := mustAddQuestion(t, srv, tsID)

	w := reqInstructor(t, srv, http.MethodPut,
		"/api/v1/test-sets/"+tsID+"/questions/"+qID,
		map[string]interface{}{
			"points": 7.0,
		})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	pts, _ := out["points"].(float64)
	if pts != 7.0 {
		t.Fatalf("expected points=7.0; got %v", out["points"])
	}
}

func TestRemoveQuestion_HappyPath_204(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	qID := mustAddQuestion(t, srv, tsID)

	w := reqInstructor(t, srv, http.MethodDelete,
		"/api/v1/test-sets/"+tsID+"/questions/"+qID, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204; got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Publish lifecycle
// -----------------------------------------------------------------------------

func TestPublish_HappyPath_200WithStatePublished(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)

	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	if out["state"] != "PUBLISHED" {
		t.Fatalf("expected state=PUBLISHED; got %v", out["state"])
	}
}

func TestPublish_EmptyDraft_409_NoQuestions(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)

	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409; got %d", w.Code)
	}
	out := decodeMap(t, w)
	errBody, _ := out["error"].(map[string]interface{})
	code, _ := errBody["code"].(string)
	if code != "DELIVERY_TEST_SET_NO_QUESTIONS" {
		t.Fatalf("expected DELIVERY_TEST_SET_NO_QUESTIONS; got %q", code)
	}
}

func TestPublish_Idempotent_RePublishReturns200(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)

	first := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first publish: expected 200; got %d", first.Code)
	}
	second := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second publish (idempotent): expected 200; got %d", second.Code)
	}
}

// -----------------------------------------------------------------------------
// Append-only-on-PUBLISHED
// -----------------------------------------------------------------------------

func TestAddQuestion_OnPublished_409(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)
	// Publish.
	pub := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if pub.Code != http.StatusOK {
		t.Fatalf("publish: expected 200; got %d", pub.Code)
	}
	// Try to add another question.
	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/questions",
		map[string]interface{}{
			"question_atom_id": "00000000-0000-7000-8000-00000000a0a5",
			"question_id":      "019e2ba6-9999-73f5-b517-dd627e76d451",
			"question_type":    "oe",
			"points":           5.0,
		})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409; got %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	errBody, _ := out["error"].(map[string]interface{})
	code, _ := errBody["code"].(string)
	if code != "DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE" {
		t.Fatalf("expected DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE; got %q", code)
	}
}

func TestUpdateQuestion_OnPublished_409(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	qID := mustAddQuestion(t, srv, tsID)
	pub := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if pub.Code != http.StatusOK {
		t.Fatalf("publish: expected 200; got %d", pub.Code)
	}
	w := reqInstructor(t, srv, http.MethodPut,
		"/api/v1/test-sets/"+tsID+"/questions/"+qID,
		map[string]interface{}{"points": 9.0})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409; got %d", w.Code)
	}
}

func TestRemoveQuestion_OnPublished_409(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	qID := mustAddQuestion(t, srv, tsID)
	pub := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if pub.Code != http.StatusOK {
		t.Fatalf("publish: expected 200; got %d", pub.Code)
	}
	w := reqInstructor(t, srv, http.MethodDelete,
		"/api/v1/test-sets/"+tsID+"/questions/"+qID, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409; got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Event publication
// -----------------------------------------------------------------------------

func TestCreateTestSet_EmitsCreatedEvent(t *testing.T) {
	srv, pub := newTestSetServer()
	w := reqInstructor(t, srv, http.MethodPost, "/api/v1/test-sets",
		map[string]interface{}{"title": "ev-test"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}
	hist := pub.History()
	if len(hist) == 0 {
		t.Fatalf("expected at least 1 event; got 0")
	}
	if hist[0].Topic != "chora.delivery.test_set.created.v1" {
		t.Fatalf("expected created event; got %q", hist[0].Topic)
	}
}

func TestPublish_EmitsPublishedEvent(t *testing.T) {
	srv, pub := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)
	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("publish: %d", w.Code)
	}
	// Find the test_set.published event.
	found := false
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.test_set.published.v1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected chora.delivery.test_set.published.v1 in history; got %v", pub.History())
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func mustCreateTestSet(t *testing.T, srv http.Handler) string {
	t.Helper()
	w := reqInstructor(t, srv, http.MethodPost, "/api/v1/test-sets",
		map[string]interface{}{"title": "Phyllis Test"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create test-set: %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("missing id in create response")
	}
	return id
}

// -----------------------------------------------------------------------------
// E2E-BE-1 — listTestSets (GET /api/v1/test-sets)
// -----------------------------------------------------------------------------

func TestListTestSets_EmptyTenant(t *testing.T) {
	srv, _ := newTestSetServer()
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets?page_size=20", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", w.Code, w.Body.String())
	}
	body := decodeMap(t, w)
	items, ok := body["items"].([]interface{})
	if !ok {
		t.Fatalf("items missing or wrong shape; body=%v", body)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items; got %d", len(items))
	}
	if body["next_page_token"] != nil {
		t.Fatalf("expected next_page_token=null on empty page; got %v", body["next_page_token"])
	}
}

func TestListTestSets_ReturnsAuthoredItems(t *testing.T) {
	srv, _ := newTestSetServer()
	id1 := mustCreateTestSet(t, srv)
	id2 := mustCreateTestSet(t, srv)
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets?page_size=20", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", w.Code, w.Body.String())
	}
	body := decodeMap(t, w)
	items, _ := body["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("expected 2 items; got %d (body=%s)", len(items), w.Body.String())
	}
	seen := map[string]bool{}
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		if id, _ := m["id"].(string); id != "" {
			seen[id] = true
		}
	}
	if !seen[id1] || !seen[id2] {
		t.Fatalf("expected both ids in items; got seen=%v", seen)
	}
}

func TestListTestSets_RejectsLearnerRole(t *testing.T) {
	srv, _ := newTestSetServer()
	w := reqLearner(t, srv, http.MethodGet, "/api/v1/test-sets?page_size=20", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for learner; got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestListTestSets_StateFilterArchived(t *testing.T) {
	srv, _ := newTestSetServer()
	mustCreateTestSet(t, srv)
	// Default filter excludes ARCHIVED — current created item is DRAFT so
	// it must appear; filtering to state=ARCHIVED must return empty.
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets?state=ARCHIVED", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", w.Code, w.Body.String())
	}
	body := decodeMap(t, w)
	items, _ := body["items"].([]interface{})
	if len(items) != 0 {
		t.Fatalf("expected 0 ARCHIVED items; got %d", len(items))
	}
}

func TestListTestSets_TenantIsolation(t *testing.T) {
	srv, _ := newTestSetServer()
	mustCreateTestSet(t, srv)
	// Switch tenant via direct request — caller is still an instructor but
	// in a different tenant; the list must be empty (RLS-like isolation in
	// the in-memory adapter).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test-sets?page_size=20", nil)
	req.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-000000000002") // tenantB
	req.Header.Set("gcid", gcidA)
	req.Header.Set("x-mesh-user-roles", "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", w.Code, w.Body.String())
	}
	body := decodeMap(t, w)
	items, _ := body["items"].([]interface{})
	if len(items) != 0 {
		t.Fatalf("expected 0 items for tenantB; got %d", len(items))
	}
}

func mustAddQuestion(t *testing.T, srv http.Handler, tsID string) string {
	t.Helper()
	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/questions",
		map[string]interface{}{
			"question_atom_id": "00000000-0000-7000-8000-00000000a0a2",
			"question_id":      "019e2ba6-7353-73f5-b517-dd627e76d450",
			"question_type":    "mcq",
			"points":           2.0,
		})
	if w.Code != http.StatusCreated {
		t.Fatalf("add question: %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("missing id in add-question response")
	}
	return id
}

// -----------------------------------------------------------------------------
// ADR-229 WS-2 (CHO-2133) — the publish handler threads the gateway-injected
// `gcid` header (the publish actor) into every snapshot call so
// chora-creation's consent gate evaluates the REAL caller.
// -----------------------------------------------------------------------------

type captureSnapshotter struct {
	callers []string
}

func (c *captureSnapshotter) SnapshotQuestion(_ context.Context, _, _, callerGCID string) (domain.QuestionPayloadSnapshot, error) {
	c.callers = append(c.callers, callerGCID)
	return domain.QuestionPayloadSnapshot{QuestionType: "mcq", PayloadJSON: `{"stem":"s"}`}, nil
}

func TestPublish_ThreadsActorGcidToSnapshotter(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	snap := &captureSnapshotter{}
	srv := httpapi.NewServer(httpapi.Deps{
		TestSets:            httpapi.NewInMemTestSetStore(),
		Publisher:           pub,
		QuestionSnapshotter: snap,
	})
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)

	w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("publish: %d (body=%s)", w.Code, w.Body.String())
	}
	if len(snap.callers) != 1 || snap.callers[0] != gcidA {
		t.Fatalf("snapshotter callers = %v; want [%s] (the gateway gcid header)", snap.callers, gcidA)
	}
}

// -----------------------------------------------------------------------------
// ADR-229 WS-2 (CHO-2139) — publish-time snapshot consent-refusal HTTP mapping.
// A reuse denial from chora-creation's SnapshotQuestionByID gate is a terminal
// client-side 4xx, NOT a retryable 502. errSnapshotter drives the publish
// handler with a fixed snapshotter error (as the real gRPC client returns after
// translating the upstream gRPC status into a domain sentinel); the domain
// PublishWithSnapshot wraps it with %w, so the handler's errors.Is unwraps it.
// -----------------------------------------------------------------------------

type errSnapshotter struct{ err error }

func (e *errSnapshotter) SnapshotQuestion(_ context.Context, _, _, _ string) (domain.QuestionPayloadSnapshot, error) {
	return domain.QuestionPayloadSnapshot{}, e.err
}

// publishWithSnapshotErr wires a server whose snapshotter always fails with
// snapErr, creates a DRAFT test-set with one question, and POSTs /publish.
func publishWithSnapshotErr(t *testing.T, snapErr error) *httptest.ResponseRecorder {
	t.Helper()
	srv := httpapi.NewServer(httpapi.Deps{
		TestSets:            httpapi.NewInMemTestSetStore(),
		Publisher:           events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
		QuestionSnapshotter: &errSnapshotter{err: snapErr},
	})
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)
	return reqInstructor(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/publish", nil)
}

// A consent refusal is TERMINAL 403 (not a retryable 502), and the
// discriminated ADR229_REUSE_DENIED reason must survive into the envelope.
func TestPublish_ConsentRefusal_MapsTo403WithDiscriminatedMessage(t *testing.T) {
	snapErr := fmt.Errorf("ADR229_REUSE_DENIED question q-1 not reusable by caller: %w",
		domain.ErrQuestionReuseDenied)
	w := publishWithSnapshotErr(t, snapErr)
	if w.Code != http.StatusForbidden {
		t.Fatalf("consent refusal: want 403; got %d (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ADR229_REUSE_DENIED") {
		t.Errorf("want discriminated ADR229_REUSE_DENIED message preserved in 403 body, got %s", w.Body.String())
	}
}

func TestPublish_SnapshotInvalidArgument_MapsTo400(t *testing.T) {
	w := publishWithSnapshotErr(t, domain.ErrQuestionSnapshotInvalidArgument)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid argument: want 400; got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestPublish_SnapshotPreconditionFailed_MapsTo409(t *testing.T) {
	w := publishWithSnapshotErr(t, domain.ErrQuestionSnapshotPreconditionFailed)
	if w.Code != http.StatusConflict {
		t.Fatalf("precondition failed: want 409; got %d (body=%s)", w.Code, w.Body.String())
	}
}

// A genuine transport outage must stay a retryable 502 (test-set stays DRAFT).
func TestPublish_SnapshotTransportOutage_StaysBadGateway502(t *testing.T) {
	w := publishWithSnapshotErr(t,
		errors.New("question client: snapshot rpc: rpc error: code = Unavailable desc = connection refused"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("transport outage: want 502 (retryable); got %d (body=%s)", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// CHO-2402 — the pinned payload snapshot must reach the API.
//
// TestSet.PublishWithSnapshot writes test_set_questions.payload_snapshot and
// the repo selects it back onto the aggregate, but testSetQuestionDTO dropped
// both fields. The A+ test-set editor therefore had nothing to render a
// published set from and fell through to the LIVE atom, showing an author a
// stem the learners sitting that set never see.
// -----------------------------------------------------------------------------

// pinningSnapshotter returns a fixed payload so the assertion can name it.
type pinningSnapshotter struct{}

func (pinningSnapshotter) SnapshotQuestion(_ context.Context, _, _, _ string) (domain.QuestionPayloadSnapshot, error) {
	return domain.QuestionPayloadSnapshot{
		QuestionType: "mcq",
		PayloadJSON:  `{"stem":"the wording pinned at publish","options":[{"label":"A","is_correct":true}]}`,
	}, nil
}

func TestGetTestSet_PublishedQuestionCarriesPinnedSnapshot(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		TestSets:            httpapi.NewInMemTestSetStore(),
		Publisher:           events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
		QuestionSnapshotter: pinningSnapshotter{},
	})
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)
	if w := reqInstructor(t, srv, http.MethodPost,
		"/api/v1/test-sets/"+tsID+"/publish", nil); w.Code != http.StatusOK {
		t.Fatalf("publish: %d (body=%s)", w.Code, w.Body.String())
	}

	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets/"+tsID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d (body=%s)", w.Code, w.Body.String())
	}
	body := decodeMap(t, w)
	qs, ok := body["questions"].([]interface{})
	if !ok || len(qs) != 1 {
		t.Fatalf("questions: want 1, got %#v", body["questions"])
	}
	q, _ := qs[0].(map[string]interface{})

	snap, ok := q["payload_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload_snapshot missing or not an object: %#v", q["payload_snapshot"])
	}
	if got, _ := snap["stem"].(string); got != "the wording pinned at publish" {
		t.Errorf("payload_snapshot.stem = %q; want the pinned wording", got)
	}
	if _, ok := snap["options"].([]interface{}); !ok {
		t.Errorf("payload_snapshot.options missing; the answer key must ride with the stem")
	}
	if _, ok := q["snapshot_at"].(string); !ok {
		t.Errorf("snapshot_at missing; a reader cannot tell when the copy was pinned")
	}
}

func TestGetTestSet_DraftQuestionOmitsSnapshotFields(t *testing.T) {
	srv, _ := newTestSetServer()
	tsID := mustCreateTestSet(t, srv)
	_ = mustAddQuestion(t, srv, tsID)

	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets/"+tsID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d (body=%s)", w.Code, w.Body.String())
	}
	qs, _ := decodeMap(t, w)["questions"].([]interface{})
	if len(qs) != 1 {
		t.Fatalf("questions: want 1, got %d", len(qs))
	}
	q, _ := qs[0].(map[string]interface{})
	if _, present := q["payload_snapshot"]; present {
		t.Errorf("draft row carries payload_snapshot; a draft follows the live copy and must not pin one")
	}
	if _, present := q["snapshot_at"]; present {
		t.Errorf("draft row carries snapshot_at")
	}
}
