// testset_handler_sourcejob_test.go — Lane 1c W4 (CHO-1703 / ADR-180 D10)
// list-filter tests for GET /api/v1/test-sets?source_job_id={uuid}.
//
// TDD RED phase: written FIRST. Reuses newTestSetServer/reqInstructor from
// testset_handler_test.go (same httpapi_test package). Per
// chora-contracts/openapi/delivery-test-sets.yaml v1.1.0:
//
//   - `source_job_id` query param — exact-match on test_sets.source_job_id
//     (the batch-authoring FE discovery poll; at most ONE row matches).
//   - The TestSet response object carries nullable `source_job_id`.
//   - Malformed (non-UUID) values → 400 (contract: format uuid).
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const sjTestJobID = "01985e7f-0000-7000-8000-00000000aaaa"

// newSourceJobServer seeds an in-memory store with one batch-assembled and
// one hand-authored test set for tenantA.
func newSourceJobServer(t *testing.T) (http.Handler, *httpapi.InMemTestSetStore) {
	t.Helper()
	store := httpapi.NewInMemTestSetStore()
	assembled, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:    tenantA,
		AuthorGCID:  gcidA,
		Title:       "Batch-assembled set",
		SourceJobID: sjTestJobID,
	})
	if err != nil {
		t.Fatalf("NewTestSet assembled: %v", err)
	}
	hand, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:   tenantA,
		AuthorGCID: gcidA,
		Title:      "Hand-authored set",
	})
	if err != nil {
		t.Fatalf("NewTestSet hand: %v", err)
	}
	if err := store.Save(t.Context(), assembled); err != nil {
		t.Fatalf("seed assembled: %v", err)
	}
	if err := store.Save(t.Context(), hand); err != nil {
		t.Fatalf("seed hand: %v", err)
	}
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{TestSets: store, Publisher: pub})
	return srv, store
}

func decodeListItems(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode list body: %v (%s)", err, body)
	}
	return out.Items
}

func TestListTestSets_SourceJobIDFilter_ExactMatch(t *testing.T) {
	srv, _ := newSourceJobServer(t)
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets?source_job_id="+sjTestJobID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body %s", w.Code, w.Body.String())
	}
	items := decodeListItems(t, w.Body.Bytes())
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 match; got %d (%s)", len(items), w.Body.String())
	}
	if got := items[0]["title"]; got != "Batch-assembled set" {
		t.Fatalf("wrong row matched: %v", got)
	}
	if got := items[0]["source_job_id"]; got != sjTestJobID {
		t.Fatalf("response must carry source_job_id; got %v", got)
	}
}

func TestListTestSets_SourceJobIDFilter_NoMatchIsEmpty(t *testing.T) {
	srv, _ := newSourceJobServer(t)
	w := reqInstructor(t, srv, http.MethodGet,
		"/api/v1/test-sets?source_job_id=01985e7f-ffff-7000-8000-00000000ffff", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body %s", w.Code, w.Body.String())
	}
	if items := decodeListItems(t, w.Body.Bytes()); len(items) != 0 {
		t.Fatalf("expected 0 matches; got %d", len(items))
	}
}

func TestListTestSets_SourceJobIDFilter_MalformedIs400(t *testing.T) {
	srv, _ := newSourceJobServer(t)
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets?source_job_id=not-a-uuid", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed uuid: got %d want 400 (body %s)", w.Code, w.Body.String())
	}
}

func TestListTestSets_NoFilter_SourceJobIDNullable(t *testing.T) {
	srv, _ := newSourceJobServer(t)
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	items := decodeListItems(t, w.Body.Bytes())
	if len(items) != 2 {
		t.Fatalf("expected both rows without the filter; got %d", len(items))
	}
	// Per the v1.1.0 contract source_job_id is nullable — the hand-authored
	// row must surface an explicit null (key present, value nil).
	for _, item := range items {
		v, present := item["source_job_id"]
		if !present {
			t.Fatalf("source_job_id key must be present (nullable) on %v", item["title"])
		}
		switch item["title"] {
		case "Batch-assembled set":
			if v != sjTestJobID {
				t.Fatalf("assembled row: got %v", v)
			}
		case "Hand-authored set":
			if v != nil {
				t.Fatalf("hand-authored row must be null; got %v", v)
			}
		}
	}
}

func TestGetTestSet_CarriesSourceJobID(t *testing.T) {
	srv, store := newSourceJobServer(t)
	// Resolve the assembled row's id via the port (test-side peek).
	ts, ok, err := store.GetBySourceJobID(t.Context(), tenantA, sjTestJobID)
	if err != nil || !ok {
		t.Fatalf("GetBySourceJobID seed lookup: ok=%v err=%v", ok, err)
	}
	w := reqInstructor(t, srv, http.MethodGet, "/api/v1/test-sets/"+ts.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body %s", w.Code, w.Body.String())
	}
	var dto map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := dto["source_job_id"]; got != sjTestJobID {
		t.Fatalf("get DTO must carry source_job_id; got %v", got)
	}
}

// InMemTestSetStore must satisfy the same Lane-1c port surface the pg repo
// gains (GetBySourceJobID) so the subscriber + dev wiring stay symmetric.
func TestInMemTestSetStore_GetBySourceJobID(t *testing.T) {
	_, store := newSourceJobServer(t)

	ts, ok, err := store.GetBySourceJobID(t.Context(), tenantA, sjTestJobID)
	if err != nil {
		t.Fatalf("GetBySourceJobID: %v", err)
	}
	if !ok || ts == nil {
		t.Fatal("expected the assembled row")
	}
	if ts.SourceJobID == nil || *ts.SourceJobID != sjTestJobID {
		t.Fatalf("SourceJobID: got %v", ts.SourceJobID)
	}

	// Tenant isolation: other tenant sees nothing.
	if _, ok, _ := store.GetBySourceJobID(t.Context(), "99999999-9999-7999-8999-999999999999", sjTestJobID); ok {
		t.Fatal("cross-tenant GetBySourceJobID must miss")
	}
	// Unknown job id misses.
	if _, ok, _ := store.GetBySourceJobID(t.Context(), tenantA, "01985e7f-ffff-7000-8000-00000000ffff"); ok {
		t.Fatal("unknown job id must miss")
	}
}
