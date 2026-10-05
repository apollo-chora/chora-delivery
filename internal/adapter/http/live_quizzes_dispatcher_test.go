// live_quizzes_dispatcher_test.go — routing coverage for the composite
// /api/v1/live-quizzes/ dispatcher (R+ Wave-6). TDD backfill per
// feedback_strict_tdd. Asserts the dispatcher's OWN responsibility: leaf-shape
// routing + fail-loud 503 when the target sub-handler's deps are unwired.
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	wsadapter "github.com/apollo-chora/chora-delivery/internal/adapter/ws"
)

func dispatch(t *testing.T, deps Deps, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	liveQuizzesUnifiedDispatcher(deps).ServeHTTP(rec, req)
	return rec
}

// dispatchAuthed mirrors dispatch but stamps the gateway-supplied caller
// identity (CHO-1616) so a WS-branch probe clears the WS handler's auth gate
// and exercises the routing/lookup path rather than the 401 identity gate.
func dispatchAuthed(t *testing.T, deps Deps, method, path, tenant string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Tenant-Id", tenant)
	req.Header.Set("gcid", "01970000-0000-7000-8000-00000000ca11")
	liveQuizzesUnifiedDispatcher(deps).ServeHTTP(rec, req)
	return rec
}

func TestDispatcher_WSPath_503WhenUnwired(t *testing.T) {
	rec := dispatch(t, Deps{}, http.MethodGet, "/api/v1/live-quizzes/sess-1/ws")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 for unwired WS; got %d", rec.Code)
	}
}

func TestDispatcher_SessionsPath_503WhenUnwired(t *testing.T) {
	rec := dispatch(t, Deps{}, http.MethodPost, "/api/v1/live-quizzes/quiz-1/sessions")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 for unwired sessions; got %d", rec.Code)
	}
}

func TestDispatcher_CRUDPath_503WhenUnwired(t *testing.T) {
	rec := dispatch(t, Deps{}, http.MethodGet, "/api/v1/live-quizzes/quiz-1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 for unwired CRUD; got %d", rec.Code)
	}
}

func TestDispatcher_WSPath_RoutesPastGateWhenWired(t *testing.T) {
	deps := Deps{
		ClassroomSessions:       inmem.NewClassroomSessionRepo(),
		ClassroomRealtimeBroker: wsadapter.NewBroker(8),
	}
	// Session absent in repo ⇒ WS handler answers 404 — proves the dispatcher
	// routed to the WS handler (NOT a 503 gate, NOT the CRUD/sessions branch).
	// Identity headers supplied so the request clears the WS auth gate
	// (CHO-1616) and reaches the repo lookup.
	rec := dispatchAuthed(t, deps, http.MethodGet, "/api/v1/live-quizzes/missing-sess/ws", "any-tenant")
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("WS request should route past the 503 gate when wired; got 503")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 from WS handler for missing session; got %d", rec.Code)
	}
}

func TestDispatcher_SessionsPath_RoutesPastGateWhenWired(t *testing.T) {
	deps := Deps{ClassroomSessions: inmem.NewClassroomSessionRepo()}
	// No tenant context ⇒ tenantRequired rejects (401/400) — proves routing
	// reached the sessions branch rather than the 503 gate.
	rec := dispatch(t, deps, http.MethodPost, "/api/v1/live-quizzes/quiz-1/sessions")
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("sessions request should route past the 503 gate when wired; got 503")
	}
}

func TestDispatcher_CRUDPath_RoutesPastGateWhenWired(t *testing.T) {
	deps := Deps{LiveQuizzes: inmem.NewLiveQuizRepo()}
	rec := dispatch(t, deps, http.MethodGet, "/api/v1/live-quizzes/quiz-1")
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("CRUD request should route past the 503 gate when wired; got 503")
	}
}
