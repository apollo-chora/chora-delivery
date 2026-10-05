// testset_handler_ts3_test.go — statement-coverage top-up for
// testset_handler.go. Exercises every reachable branch the earlier lanes
// skipped: in-memory store edge cases (nil aggregates, empty source_job_id,
// filter families, tie-break ordering, page truncation), the root/sub
// handler 405 dispatch guards, PATCH update-set lifecycle (404 / 403 / 409 /
// 400 / 500), question mutation error mapping (422 / 404 / 409 / 500),
// publish snapshot-not-found + archived mapping, list-filter variants,
// authorisation role branches, and the nil-publisher guards on every
// publish* helper.
package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const ts3JobID = "01985e7f-0000-7000-8000-00000000aaaa"

// ts3Req issues a request with the standard authoring headers (tenantA +
// gcidA + instructor role). body=="" sends no body.
func ts3Req(t *testing.T, srv http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return ts3ReqH(t, srv, method, path, body, nil)
}

// ts3ReqH is ts3Req with per-header overrides; a value of "" deletes the
// header (used to assert the missing-gcid / role-less branches).
func ts3ReqH(t *testing.T, srv http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	req.Header.Set("x-mesh-user-roles", "instructor")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// ts3ErrCode extracts the discriminated error code from a 4xx/5xx envelope.
func ts3ErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	m := decodeMap(t, w)
	errBody, _ := m["error"].(map[string]interface{})
	code, _ := errBody["code"].(string)
	return code
}

// ts3NewDraft builds a DRAFT TestSet for tenantA / gcidA, optionally mutated.
func ts3NewDraft(t *testing.T, mut func(*domain.TestSet)) *domain.TestSet {
	t.Helper()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantA, AuthorGCID: gcidA, Title: "ts3 draft",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if mut != nil {
		mut(ts)
	}
	return ts
}

// ts3AddQuestionTo appends one MCQ inclusion directly on the aggregate.
func ts3AddQuestionTo(t *testing.T, ts *domain.TestSet) *domain.TestSetQuestion {
	t.Helper()
	q, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "00000000-0000-7000-8000-00000000a0a2",
		QuestionID:     "019e2ba6-7353-73f5-b517-dd627e76d450",
		QuestionType:   "mcq",
		Points:         2.0,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	return q
}

// ts3SeedServer wires an in-memory store + publisher, seeds one test-set
// (mutated via mut, e.g. Archive()) and returns everything the test needs.
func ts3SeedServer(t *testing.T, mut func(*domain.TestSet)) (http.Handler, httpapi.TestSetPort, *events.InMemoryPublisher, *domain.TestSet) {
	t.Helper()
	store := httpapi.NewInMemTestSetStore()
	ts := ts3NewDraft(t, mut)
	if err := store.Save(t.Context(), ts); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: store, Publisher: pub})
	return srv, store, pub, ts
}

// ts3ErrStore is a TestSetPort that fails the selected method, so the 500
// branches of every handler are reachable without touching Postgres.
type ts3ErrStore struct {
	row        *domain.TestSet
	getErr     error
	saveErr    error
	saveRemErr error
	listErr    error
}

func (s *ts3ErrStore) Save(_ context.Context, _ *domain.TestSet) error { return s.saveErr }
func (s *ts3ErrStore) SaveQuestionRemoval(_ context.Context, _ *domain.TestSet, _ string) error {
	return s.saveRemErr
}
func (s *ts3ErrStore) Get(_ context.Context, _, _ string) (*domain.TestSet, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	return s.row, s.row != nil, nil
}
func (s *ts3ErrStore) GetBySourceJobID(_ context.Context, _, _ string) (*domain.TestSet, bool, error) {
	return nil, false, nil
}
func (s *ts3ErrStore) List(_ context.Context, _ string, _ domain.TestSetListFilter) ([]*domain.TestSet, string, error) {
	return nil, "", s.listErr
}

// ts3ErrSnapshotter fails PublishWithSnapshot with a fixed sentinel.
type ts3ErrSnapshotter struct{ err error }

func (s *ts3ErrSnapshotter) SnapshotQuestion(_ context.Context, _, _, _ string) (domain.QuestionPayloadSnapshot, error) {
	return domain.QuestionPayloadSnapshot{}, s.err
}

// -----------------------------------------------------------------------------
// InMemTestSetStore edge branches
// -----------------------------------------------------------------------------

func TestTs3InMemStore_SaveNilAndHappy(t *testing.T) {
	store := httpapi.NewInMemTestSetStore()
	if err := store.Save(t.Context(), nil); err == nil {
		t.Fatal("Save(nil) must error")
	}
	ts := ts3NewDraft(t, nil)
	if err := store.Save(t.Context(), ts); err != nil {
		t.Fatalf("Save(happy): %v", err)
	}
	if err := store.SaveQuestionRemoval(t.Context(), nil, "q1"); err == nil {
		t.Fatal("SaveQuestionRemoval(nil) must error")
	}
	if err := store.SaveQuestionRemoval(t.Context(), ts, "q1"); err != nil {
		t.Fatalf("SaveQuestionRemoval(happy): %v", err)
	}
}

func TestTs3InMemStore_GetBySourceJobIDBranches(t *testing.T) {
	store := httpapi.NewInMemTestSetStore()
	sj := ts3JobID
	withJob := ts3NewDraft(t, func(ts *domain.TestSet) { ts.SourceJobID = &sj })
	if err := store.Save(t.Context(), withJob); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Empty source_job_id → miss immediately.
	if _, ok, err := store.GetBySourceJobID(t.Context(), tenantA, ""); err != nil || ok {
		t.Fatalf("empty source_job_id: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.GetBySourceJobID(t.Context(), tenantA, "   "); err != nil || ok {
		t.Fatalf("whitespace source_job_id: ok=%v err=%v", ok, err)
	}
	// Not-found.
	if _, ok, _ := store.GetBySourceJobID(t.Context(), tenantA, "01985e7f-ffff-7000-8000-00000000ffff"); ok {
		t.Fatal("unknown job id must miss")
	}
	// Found.
	got, ok, err := store.GetBySourceJobID(t.Context(), tenantA, ts3JobID)
	if err != nil || !ok || got.ID != withJob.ID {
		t.Fatalf("known job id: ok=%v err=%v got=%v", ok, err, got)
	}
	// Cross-tenant.
	if _, ok, _ := store.GetBySourceJobID(t.Context(), "99999999-9999-7999-8999-999999999999", ts3JobID); ok {
		t.Fatal("cross-tenant must miss")
	}
}

func TestTs3InMemStore_ListFilterBranches(t *testing.T) {
	store := httpapi.NewInMemTestSetStore()
	sj := ts3JobID
	draft := ts3NewDraft(t, nil) // DRAFT, gcidA, "ts3 draft"
	pub := ts3NewDraft(t, func(ts *domain.TestSet) {
		ts.State = domain.TestSetStatePublished
		ts.Title = "Phyllis Beta"
		ts.SourceJobID = &sj
		ts.AuthorGCID = gcidB
	})
	// ARCHIVED state without DeletedAt — listable ONLY when the caller opts
	// into the ARCHIVED stateSet (in-memory Archive() also sets DeletedAt and
	// is therefore always excluded; this row isolates the stateSet branch).
	archState := ts3NewDraft(t, func(ts *domain.TestSet) {
		ts.Title = "Gamma archived"
		ts.State = domain.TestSetStateArchived
	})
	// DeletedAt set → excluded under every filter (DeletedAt skip branch).
	deleted := ts3NewDraft(t, func(ts *domain.TestSet) { ts.Archive() })
	for _, ts := range []*domain.TestSet{draft, pub, archState, deleted} {
		if err := store.Save(t.Context(), ts); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Default stateSet {DRAFT, PUBLISHED}: draft+pub; archState misses the
	// default stateSet, deleted misses on DeletedAt.
	items, next, err := store.List(t.Context(), tenantA, domain.TestSetListFilter{})
	if err != nil || next != "" {
		t.Fatalf("list default: err=%v next=%q", err, next)
	}
	if len(items) != 2 {
		t.Fatalf("default states: want 2 got %d", len(items))
	}

	// Explicit ARCHIVED stateSet → the archState row only (stateSet hit).
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{States: []domain.TestSetState{domain.TestSetStateArchived}})
	if len(items) != 1 || items[0].ID != archState.ID {
		t.Fatalf("archived filter: got %d rows", len(items))
	}
	// Explicit DRAFT stateSet → draft only (pub + archState hit the stateSet
	// miss path; deleted still excluded).
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{States: []domain.TestSetState{domain.TestSetStateDraft}})
	if len(items) != 1 || items[0].ID != draft.ID {
		t.Fatalf("draft filter: got %d rows", len(items))
	}

	// Author GCID family.
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{AuthorGCIDs: []string{gcidA}})
	if len(items) != 1 || items[0].ID != draft.ID {
		t.Fatalf("author gcidA: got %d rows", len(items))
	}
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{AuthorGCIDs: []string{gcidA, gcidB}})
	if len(items) != 2 {
		t.Fatalf("author gcidA+B: got %d rows", len(items))
	}

	// Title needle (case-insensitive substring).
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{TitleQuery: "phyllis beta"})
	if len(items) != 1 || items[0].ID != pub.ID {
		t.Fatalf("title needle: got %d rows", len(items))
	}
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{TitleQuery: "nope"})
	if len(items) != 0 {
		t.Fatalf("title miss: got %d rows", len(items))
	}

	// SourceJobID exact match + mismatch (nil SourceJobID row skipped).
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{SourceJobID: ts3JobID})
	if len(items) != 1 || items[0].ID != pub.ID {
		t.Fatalf("source job match: got %d rows", len(items))
	}
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{SourceJobID: "01985e7f-ffff-7000-8000-00000000ffff"})
	if len(items) != 0 {
		t.Fatalf("source job miss: got %d rows", len(items))
	}

	// Truncation: 2 matching rows, page_size 1.
	items, _, _ = store.List(t.Context(), tenantA, domain.TestSetListFilter{PageSize: 1})
	if len(items) != 1 {
		t.Fatalf("page truncate: got %d rows", len(items))
	}
}

func TestTs3InMemStore_ListOrderingTieBreak(t *testing.T) {
	store := httpapi.NewInMemTestSetStore()
	now := time.Now().UTC()
	seed := func(id string, at time.Time) *domain.TestSet {
		ts := ts3NewDraft(t, nil)
		ts.ID = id
		ts.CreatedAt = at
		return ts
	}
	for _, ts := range []*domain.TestSet{
		seed("z-id", now),
		seed("a-id", now),                 // same CreatedAt as z-id → ID DESC tie-break
		seed("m-id", now.Add(-time.Hour)), // older → last
	} {
		if err := store.Save(t.Context(), ts); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	items, _, _ := store.List(t.Context(), tenantA, domain.TestSetListFilter{})
	if len(items) != 3 {
		t.Fatalf("want 3 rows; got %d", len(items))
	}
	want := []string{"z-id", "a-id", "m-id"}
	for i, id := range want {
		if items[i].ID != id {
			t.Fatalf("order[%d]: want %s got %s (full: %v %v %v)",
				i, id, items[i].ID, items[0].ID, items[1].ID, items[2].ID)
		}
	}
}

// -----------------------------------------------------------------------------
// Root + sub handler method dispatch
// -----------------------------------------------------------------------------

func TestTs3RootHandler_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestSetServer()
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		w := ts3Req(t, srv, method, "/api/v1/test-sets", `{"title":"x"}`)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s root: want 405 got %d", method, w.Code)
		}
	}
}

func TestTs3SubHandler_DispatchBranches(t *testing.T) {
	srv, _ := newTestSetServer()
	created := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets", `{"title":"dispatch"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("seed create: %d", created.Code)
	}
	id := decodeMap(t, created)["id"].(string)

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"empty rest 404", http.MethodGet, "/api/v1/test-sets/", http.StatusNotFound}, // parts[0]==""
		{"publish non-post 405", http.MethodGet, "/api/v1/test-sets/" + id + "/publish", http.StatusMethodNotAllowed},
		{"questions non-post 405", http.MethodGet, "/api/v1/test-sets/" + id + "/questions", http.StatusMethodNotAllowed},
		{"question row bad method 405", http.MethodPost, "/api/v1/test-sets/" + id + "/questions/" + id, http.StatusMethodNotAllowed},
		{"single id bad method 405", http.MethodPost, "/api/v1/test-sets/" + id, http.StatusMethodNotAllowed},
		{"unknown subpath 404", http.MethodGet, "/api/v1/test-sets/" + id + "/unknown", http.StatusNotFound},
		{"too deep 404", http.MethodGet, "/api/v1/test-sets/" + id + "/questions/" + id + "/x", http.StatusNotFound},
	}
	for _, tc := range cases {
		w := ts3Req(t, srv, tc.method, tc.path, "")
		if w.Code != tc.want {
			t.Errorf("%s: want %d got %d (body=%s)", tc.name, tc.want, w.Code, w.Body.String())
		}
	}
}

// -----------------------------------------------------------------------------
// handleCreateTestSet
// -----------------------------------------------------------------------------

func TestTs3CreateTestSet_MissingGcid_401(t *testing.T) {
	srv, _ := newTestSetServer()
	w := ts3ReqH(t, srv, http.MethodPost, "/api/v1/test-sets", `{"title":"x"}`, map[string]string{"gcid": ""})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", w.Code)
	}
}

func TestTs3CreateTestSet_LearnerRole_403(t *testing.T) {
	srv, _ := newTestSetServer()
	w := ts3ReqH(t, srv, http.MethodPost, "/api/v1/test-sets", `{"title":"x"}`,
		map[string]string{"x-mesh-user-roles": "learner"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3CreateTestSet_BadJSON_400(t *testing.T) {
	srv, _ := newTestSetServer()
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets", "{not-json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", w.Code)
	}
}

func TestTs3CreateTestSet_SaveErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		TestSets:  &ts3ErrStore{saveErr: errors.New("db down")},
		Publisher: pub,
	})
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets", `{"title":"x"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// handleGetTestSet
// -----------------------------------------------------------------------------

func TestTs3GetTestSet_GetErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{getErr: errors.New("read fail")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets/any-id", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// handleUpdateTestSet (PATCH)
// -----------------------------------------------------------------------------

func TestTs3UpdateTestSet_HappyRename(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/"+ts.ID,
		`{"title":"  Renamed  ","description":"Fresh description"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH happy: want 200 got %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	if out["title"] != "Renamed" {
		t.Errorf("title: want Renamed got %v", out["title"])
	}
	if out["description"] != "Fresh description" {
		t.Errorf("description: want Fresh description got %v", out["description"])
	}
}

func TestTs3UpdateTestSet_Unknown_404(t *testing.T) {
	srv, _, _, _ := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/01985e7f-0000-7000-8000-000000000999", `{"title":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_NOT_FOUND" {
		t.Fatalf("want DELIVERY_TEST_SET_NOT_FOUND got %q", code)
	}
}

func TestTs3UpdateTestSet_Published_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	ts3AddQuestionTo(t, ts)
	if w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", ""); w.Code != http.StatusOK {
		t.Fatalf("publish seed: %d", w.Code)
	}
	w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/"+ts.ID, `{"title":"x"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE" {
		t.Fatalf("want DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE got %q", code)
	}
}

func TestTs3UpdateTestSet_Archived_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, func(ts *domain.TestSet) { ts.Archive() })
	// PATCH must be authorized — seed keeps gcidA, which is not the author
	// of an archived set? It IS the author (gcidA), so a no-op is fine.
	_ = ts
	w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/"+ts.ID, `{"title":"x"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d (body=%s)", w.Code, w.Body.String())
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_ARCHIVED" {
		t.Fatalf("want DELIVERY_TEST_SET_ARCHIVED got %q", code)
	}
}

func TestTs3UpdateTestSet_Learner_403(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := reqLearner(t, srv, http.MethodPatch, "/api/v1/test-sets/"+ts.ID, map[string]interface{}{"title": "x"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3UpdateTestSet_BadJSON_400(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/"+ts.ID, "{not-json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", w.Code)
	}
}

func TestTs3UpdateTestSet_GetErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{getErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/x", `{"title":"x"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

func TestTs3UpdateTestSet_SaveErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	row := ts3NewDraft(t, nil)
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{row: row, saveErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/"+row.ID, `{"title":"x"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// handleAddTestSetQuestion
// -----------------------------------------------------------------------------

func TestTs3AddQuestion_UnknownSet_404(t *testing.T) {
	srv, _, _, _ := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/01985e7f-0000-7000-8000-000000000999/questions",
		`{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"mcq","points":2}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
}

func TestTs3AddQuestion_Learner_403(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := reqLearner(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions",
		map[string]interface{}{"question_atom_id": "00000000-0000-7000-8000-00000000a0a2", "question_id": "019e2ba6-7353-73f5-b517-dd627e76d450", "question_type": "mcq", "points": 2.0})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3AddQuestion_Archived_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, func(ts *domain.TestSet) { ts.Archive() })
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions",
		`{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"mcq","points":2}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_ARCHIVED" {
		t.Fatalf("want DELIVERY_TEST_SET_ARCHIVED got %q", code)
	}
}

func TestTs3AddQuestion_MissingAtom_422(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions",
		`{"question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"mcq","points":2}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestTs3AddQuestion_UnsupportedType_422(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	// "essay" is the canonical alias for "oe" — accepted (201), stored as oe.
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions",
		`{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"essay","points":2}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("essay alias: want 201 got %d (body=%s)", w.Code, w.Body.String())
	}
	// Truly unknown types are rejected.
	w2 := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions",
		`{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"survey","points":2}`)
	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported type: want 422 got %d (body=%s)", w2.Code, w2.Body.String())
	}
}

func TestTs3AddQuestion_BadJSON_400(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions", "{bad")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", w.Code)
	}
}

func TestTs3AddQuestion_ExplicitDuplicateOrder(t *testing.T) {
	// DisplayOrder is not unique — two inclusions may share it (both 201).
	srv, _, _, ts := ts3SeedServer(t, nil)
	body := `{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"mcq","points":2,"display_order":5}`
	for i := 0; i < 2; i++ {
		w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("dup order add #%d: want 201 got %d (body=%s)", i, w.Code, w.Body.String())
		}
		out := decodeMap(t, w)
		if out["display_order"].(float64) != 5 {
			t.Fatalf("dup order add #%d: display_order want 5 got %v", i, out["display_order"])
		}
	}
}

func TestTs3AddQuestion_GetErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{getErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/x/questions",
		`{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"mcq","points":2}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

func TestTs3AddQuestion_SaveErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	row := ts3NewDraft(t, nil)
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{row: row, saveErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+row.ID+"/questions",
		`{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"mcq","points":2}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// handleUpdateTestSetQuestion
// -----------------------------------------------------------------------------

func TestTs3UpdateQuestion_UnknownQ_404(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	ts3AddQuestionTo(t, ts)
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/01985e7f-0000-7000-8000-000000000999",
		`{"points":5}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_QUESTION_NOT_FOUND" {
		t.Fatalf("want DELIVERY_TEST_SET_QUESTION_NOT_FOUND got %q", code)
	}
}

func TestTs3UpdateQuestion_BadJSON_400(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID, "{bad")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", w.Code)
	}
}

func TestTs3UpdateQuestion_Learner_403(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	w := reqLearner(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID,
		map[string]interface{}{"points": 5.0})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3UpdateQuestion_Archived_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, func(ts *domain.TestSet) { ts.Archive() })
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/q",
		`{"points":5}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d", w.Code)
	}
}

func TestTs3UpdateQuestion_PointsZero_422(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID, `{"points":0}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestTs3UpdateQuestion_OrderNegative_422(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID, `{"display_order":-1}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestTs3UpdateQuestion_GetErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{getErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/x/questions/q", `{"points":5}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

func TestTs3UpdateQuestion_SaveErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	row := ts3NewDraft(t, nil)
	q := ts3AddQuestionTo(t, row)
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{row: row, saveErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+row.ID+"/questions/"+q.ID, `{"points":5}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// handleRemoveTestSetQuestion
// -----------------------------------------------------------------------------

func TestTs3RemoveQuestion_UnknownQ_404(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	ts3AddQuestionTo(t, ts)
	w := ts3Req(t, srv, http.MethodDelete, "/api/v1/test-sets/"+ts.ID+"/questions/01985e7f-0000-7000-8000-000000000999", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_QUESTION_NOT_FOUND" {
		t.Fatalf("want DELIVERY_TEST_SET_QUESTION_NOT_FOUND got %q", code)
	}
}

func TestTs3RemoveQuestion_Learner_403(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	w := reqLearner(t, srv, http.MethodDelete, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3RemoveQuestion_Archived_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, func(ts *domain.TestSet) { ts.Archive() })
	w := ts3Req(t, srv, http.MethodDelete, "/api/v1/test-sets/"+ts.ID+"/questions/q", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestTs3RemoveQuestion_GetErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{getErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodDelete, "/api/v1/test-sets/x/questions/q", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

func TestTs3RemoveQuestion_SaveRemovalErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	row := ts3NewDraft(t, nil)
	q := ts3AddQuestionTo(t, row)
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{row: row, saveRemErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodDelete, "/api/v1/test-sets/"+row.ID+"/questions/"+q.ID, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// handlePublishTestSet
// -----------------------------------------------------------------------------

func TestTs3Publish_Unknown_404(t *testing.T) {
	srv, _, _, _ := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/01985e7f-0000-7000-8000-000000000999/publish", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
}

func TestTs3Publish_Learner_403(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	ts3AddQuestionTo(t, ts)
	w := reqLearner(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3Publish_Archived_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, func(ts *domain.TestSet) { ts.Archive() })
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d (body=%s)", w.Code, w.Body.String())
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_ARCHIVED" {
		t.Fatalf("want DELIVERY_TEST_SET_ARCHIVED got %q", code)
	}
}

func TestTs3Publish_SnapshotNotFound_409(t *testing.T) {
	store := httpapi.NewInMemTestSetStore()
	ts := ts3NewDraft(t, nil)
	ts3AddQuestionTo(t, ts)
	if err := store.Save(t.Context(), ts); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		TestSets:            store,
		Publisher:           pub,
		QuestionSnapshotter: &ts3ErrSnapshotter{err: domain.ErrQuestionSnapshotNotFound},
	})
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d (body=%s)", w.Code, w.Body.String())
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_QUESTION_SNAPSHOT_NOT_FOUND" {
		t.Fatalf("want DELIVERY_TEST_SET_QUESTION_SNAPSHOT_NOT_FOUND got %q", code)
	}
}

func TestTs3Publish_GetErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{getErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/x/publish", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

func TestTs3Publish_SaveErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	row := ts3NewDraft(t, nil)
	ts3AddQuestionTo(t, row)
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{row: row, saveErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+row.ID+"/publish", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

func TestTs3Publish_RepublishEmitsOnce(t *testing.T) {
	srv, _, pub, ts := ts3SeedServer(t, nil)
	ts3AddQuestionTo(t, ts)
	if w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", ""); w.Code != http.StatusOK {
		t.Fatalf("first publish: %d (body=%s)", w.Code, w.Body.String())
	}
	if w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", ""); w.Code != http.StatusOK {
		t.Fatalf("re-publish: %d", w.Code)
	}
	n := 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.test_set.published.v1" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("idempotent re-publish must emit published once; got %d", n)
	}
}

// -----------------------------------------------------------------------------
// handleListTestSets
// -----------------------------------------------------------------------------

func TestTs3List_MissingGcid_401(t *testing.T) {
	srv, _ := newTestSetServer()
	w := ts3ReqH(t, srv, http.MethodGet, "/api/v1/test-sets", "", map[string]string{"gcid": ""})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", w.Code)
	}
}

func TestTs3List_StateFilters(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	ts3AddQuestionTo(t, ts)
	if w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", ""); w.Code != http.StatusOK {
		t.Fatalf("publish seed: %d", w.Code)
	}
	// state=PUBLISHED opt-in.
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?state=PUBLISHED", "")
	if w.Code != http.StatusOK {
		t.Fatalf("state=published: %d", w.Code)
	}
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 1 {
		t.Fatalf("state=published: want 1 got %d (%s)", len(items), w.Body.String())
	}
	// state=DRAFT now empty.
	w = ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?state=DRAFT", "")
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("state=draft: want 0 got %d", len(items))
	}
	// Invalid state values are ignored (default DRAFT+PUBLISHED still applies).
	w = ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?state=BOGUS", "")
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 1 {
		t.Fatalf("state=bogus: want 1 got %d (%s)", len(items), w.Body.String())
	}
	// Mixed valid+invalid.
	w = ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?state=DRAFT&state=BOGUS", "")
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("state=draft+bogus: want 0 got %d", len(items))
	}
}

func TestTs3List_AuthorFilter(t *testing.T) {
	srv, _, _, _ := ts3SeedServer(t, nil) // authored by gcidA
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?author_gcid="+gcidA, "")
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 1 {
		t.Fatalf("author gcidA: want 1 got %d", len(items))
	}
	w = ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?author_gcid="+gcidB, "")
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("author gcidB: want 0 got %d", len(items))
	}
	// Blank author values are skipped.
	w = ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?author_gcid=", "")
	if len(decodeListItems(t, w.Body.Bytes())) != 1 {
		t.Fatalf("blank author: want 1 got %s", w.Body.String())
	}
}

func TestTs3List_TitleQuery(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	_ = ts
	// Rename via PATCH so the title differs from the default.
	if w := ts3Req(t, srv, http.MethodPatch, "/api/v1/test-sets/"+ts.ID, `{"title":"Phyllis Math Set 9000"}`); w.Code != http.StatusOK {
		t.Fatalf("rename: %d", w.Code)
	}
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?q=phyllis", "")
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 1 {
		t.Fatalf("q=phyllis: want 1 got %d (%s)", len(items), w.Body.String())
	}
	w = ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?q=ZZZ", "")
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("q=ZZZ: want 0 got %d", len(items))
	}
}

func TestTs3List_PageSizeVariants(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	_ = ts
	for _, ps := range []string{"10", "20", "50", "100", "15", ""} {
		path := "/api/v1/test-sets"
		if ps != "" {
			path += "?page_size=" + ps
		}
		w := ts3Req(t, srv, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("page_size=%q: want 200 got %d", ps, w.Code)
		}
	}
}

func TestTs3List_ListErr_500(t *testing.T) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: &ts3ErrStore{listErr: errors.New("boom")}, Publisher: pub})
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// DTO field branches via HTTP
// -----------------------------------------------------------------------------

func TestTs3DTO_OptionalFields(t *testing.T) {
	// published_at present on a published set; question_count + total_points
	// reflect the live inclusions; description echoed.
	srv, _, _, ts := ts3SeedServer(t, nil)
	ts3AddQuestionTo(t, ts)
	ts3AddQuestionTo(t, ts)
	if w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", ""); w.Code != http.StatusOK {
		t.Fatalf("publish: %d", w.Code)
	}
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets/"+ts.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
	out := decodeMap(t, w)
	if _, ok := out["published_at"]; !ok {
		t.Fatal("published set must carry published_at")
	}
	if out["question_count"].(float64) != 2 {
		t.Fatalf("question_count: want 2 got %v", out["question_count"])
	}
	if out["total_points"].(float64) != 4 {
		t.Fatalf("total_points: want 4 got %v", out["total_points"])
	}
	qs, _ := out["questions"].([]interface{})
	if len(qs) != 2 {
		t.Fatalf("questions: want 2 got %d", len(qs))
	}
}

func TestTs3DTO_ArchivedGetHasDeletedAt(t *testing.T) {
	// Get has no DeletedAt filter, so an archived set is retrievable and its
	// DTO must carry deleted_at.
	srv, _, _, ts := ts3SeedServer(t, func(ts *domain.TestSet) { ts.Archive() })
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets/"+ts.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get archived: %d", w.Code)
	}
	if _, ok := decodeMap(t, w)["deleted_at"]; !ok {
		t.Fatal("archived set must carry deleted_at")
	}
}

// -----------------------------------------------------------------------------
// publish* helpers — nil-publisher guard
// -----------------------------------------------------------------------------

func TestTs3PublishHelpers_NilPublisher(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{TestSets: httpapi.NewInMemTestSetStore(), Publisher: nil})
	created := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets", `{"title":"nil pub"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d", created.Code)
	}
	tsID := decodeMap(t, created)["id"].(string)
	qBody := `{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_id":"019e2ba6-7353-73f5-b517-dd627e76d450","question_type":"mcq","points":2}`
	added := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/questions", qBody)
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d", added.Code)
	}
	qID := decodeMap(t, added)["id"].(string)
	if w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+tsID+"/questions/"+qID, `{"points":7}`); w.Code != http.StatusOK {
		t.Fatalf("update q: %d", w.Code)
	}
	if w := ts3Req(t, srv, http.MethodDelete, "/api/v1/test-sets/"+tsID+"/questions/"+qID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("remove q: %d", w.Code)
	}
	// Re-add then publish so PublishWithSnapshot has a live question.
	ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/questions", qBody)
	if w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/publish", ""); w.Code != http.StatusOK {
		t.Fatalf("publish: %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Top-up branches: happy paths + 400/403/404 edges not covered by the 500-focus
// tests above, so every remaining statement is hit within the TestTs3 run.
// -----------------------------------------------------------------------------

func TestTs3CreateTestSet_BlankTitle_400(t *testing.T) {
	srv, _ := newTestSetServer()
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets", `{"title":"   "}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestTs3GetTestSet_Unknown_404(t *testing.T) {
	srv, _ := newTestSetServer()
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets/01985e7f-0000-7000-8000-000000000999", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
}

func TestTs3GetTestSet_Happy(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets/"+ts.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	out := decodeMap(t, w)
	if out["state"] != "DRAFT" {
		t.Fatalf("state: want DRAFT got %v", out["state"])
	}
	if _, ok := out["questions"]; !ok {
		t.Fatal("detail DTO must carry questions")
	}
}

func TestTs3AddQuestion_MissingQuestionID_400(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/questions",
		`{"question_atom_id":"00000000-0000-7000-8000-00000000a0a2","question_type":"mcq","points":2}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestTs3UpdateQuestion_Happy_200(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID, `{"points":7,"display_order":9}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d (body=%s)", w.Code, w.Body.String())
	}
	out := decodeMap(t, w)
	if out["points"].(float64) != 7 {
		t.Fatalf("points: want 7 got %v", out["points"])
	}
	if out["display_order"].(float64) != 9 {
		t.Fatalf("display_order: want 9 got %v", out["display_order"])
	}
}

func TestTs3UpdateQuestion_Published_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	if w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", ""); w.Code != http.StatusOK {
		t.Fatalf("publish seed: %d", w.Code)
	}
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID, `{"points":9}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE" {
		t.Fatalf("want DELIVERY_TEST_SET_PUBLISHED_IMMUTABLE got %q", code)
	}
}

func TestTs3RemoveQuestion_Happy_204(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	q := ts3AddQuestionTo(t, ts)
	w := ts3Req(t, srv, http.MethodDelete, "/api/v1/test-sets/"+ts.ID+"/questions/"+q.ID, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204 got %d", w.Code)
	}
}

func TestTs3List_RejectsLearnerRole_403(t *testing.T) {
	srv, _ := newTestSetServer()
	w := reqLearner(t, srv, http.MethodGet, "/api/v1/test-sets", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3List_MalformedSourceJobID_400(t *testing.T) {
	srv, _ := newTestSetServer()
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?source_job_id=not-a-uuid", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", w.Code)
	}
}

func TestTs3Publish_EmptySet_409(t *testing.T) {
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+ts.ID+"/publish", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_NO_QUESTIONS" {
		t.Fatalf("want DELIVERY_TEST_SET_NO_QUESTIONS got %q", code)
	}
}

func TestTs3Authz_MissingGcid_403(t *testing.T) {
	// The mutate handlers route through authorisedForTestSet, whose first
	// guard is the empty-caller check → 403.
	srv, _, _, ts := ts3SeedServer(t, nil)
	w := ts3ReqH(t, srv, http.MethodPatch, "/api/v1/test-sets/"+ts.ID, `{"title":"x"}`, map[string]string{"gcid": ""})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}

func TestTs3UpdateQuestion_UnknownSet_404(t *testing.T) {
	srv, _, _, _ := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodPut, "/api/v1/test-sets/01985e7f-0000-7000-8000-000000000999/questions/q", `{"points":5}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_NOT_FOUND" {
		t.Fatalf("want DELIVERY_TEST_SET_NOT_FOUND got %q", code)
	}
}

func TestTs3RemoveQuestion_UnknownSet_404(t *testing.T) {
	srv, _, _, _ := ts3SeedServer(t, nil)
	w := ts3Req(t, srv, http.MethodDelete, "/api/v1/test-sets/01985e7f-0000-7000-8000-000000000999/questions/q", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_NOT_FOUND" {
		t.Fatalf("want DELIVERY_TEST_SET_NOT_FOUND got %q", code)
	}
}

func TestTs3List_ValidSourceJobIDFilter(t *testing.T) {
	srv, _, _, _ := ts3SeedServer(t, func(ts *domain.TestSet) {
		sj := ts3JobID
		ts.SourceJobID = &sj
	})
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets?source_job_id="+ts3JobID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	items := decodeListItems(t, w.Body.Bytes())
	if len(items) != 1 {
		t.Fatalf("want 1 match got %d (%s)", len(items), w.Body.String())
	}
	if got := items[0]["source_job_id"]; got != ts3JobID {
		t.Fatalf("source_job_id: want %s got %v", ts3JobID, got)
	}
}

// ts3SnapshotServer wires a real in-memory store (so Get succeeds) seeded with
// a DRAFT set carrying one question, plus a snapshotter that always returns
// snapErr — the caller then drives the subject publish error mappings against
// the seeded set's id.
func ts3SnapshotServer(t *testing.T, snapErr error) (http.Handler, string) {
	t.Helper()
	store := httpapi.NewInMemTestSetStore()
	ts := ts3NewDraft(t, nil)
	ts3AddQuestionTo(t, ts)
	if err := store.Save(t.Context(), ts); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		TestSets:            store,
		Publisher:           pub,
		QuestionSnapshotter: &ts3ErrSnapshotter{err: snapErr},
	})
	return srv, ts.ID
}

func TestTs3Publish_SnapshotReuseDenied_403(t *testing.T) {
	srv, tsID := ts3SnapshotServer(t, domain.ErrQuestionReuseDenied)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/publish", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d (body=%s)", w.Code, w.Body.String())
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_QUESTION_REUSE_DENIED" {
		t.Fatalf("want REUSE_DENIED got %q", code)
	}
}

func TestTs3Publish_SnapshotInvalidArgument_400(t *testing.T) {
	srv, tsID := ts3SnapshotServer(t, domain.ErrQuestionSnapshotInvalidArgument)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/publish", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_QUESTION_SNAPSHOT_INVALID" {
		t.Fatalf("want SNAPSHOT_INVALID got %q", code)
	}
}

func TestTs3Publish_SnapshotPreconditionFailed_409(t *testing.T) {
	srv, tsID := ts3SnapshotServer(t, domain.ErrQuestionSnapshotPreconditionFailed)
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/publish", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d", w.Code)
	}
	if code := ts3ErrCode(t, w); code != "DELIVERY_TEST_SET_QUESTION_SNAPSHOT_PRECONDITION_FAILED" {
		t.Fatalf("want PRECONDITION_FAILED got %q", code)
	}
}

func TestTs3Publish_SnapshotTransport_502(t *testing.T) {
	srv, tsID := ts3SnapshotServer(t, errors.New("rpc error: code = Unavailable desc = connection refused"))
	w := ts3Req(t, srv, http.MethodPost, "/api/v1/test-sets/"+tsID+"/publish", "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502 got %d", w.Code)
	}
}

func TestTs3DTO_SourceJobIDNonNull(t *testing.T) {
	sj := ts3JobID
	srv, _, _, ts := ts3SeedServer(t, func(ts *domain.TestSet) { ts.SourceJobID = &sj })
	w := ts3Req(t, srv, http.MethodGet, "/api/v1/test-sets/"+ts.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
	if got := decodeMap(t, w)["source_job_id"]; got != ts3JobID {
		t.Fatalf("source_job_id: want %s got %v", ts3JobID, got)
	}
}
