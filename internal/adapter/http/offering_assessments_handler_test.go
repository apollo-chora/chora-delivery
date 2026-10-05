// offering_assessments_handler_test.go — handler-level verification of the
// W3.A offering-nested assessment surface (making an Offering's assessments
// addressable): GET + POST /api/v1/offerings/{id}/assessments.
//
// Intra-chora_delivery (Offering + Assessment + TestSet share the DB) — no
// cross-DB query, no new event. Mirrors assessment_handler_test.go's in-mem
// harness + reqWithHeaders + seedPublishedTestSetWithMCQ.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	offeringA   = "01985e7f-1234-7abc-8def-0000000000a1"
	offeringB   = "01985e7f-1234-7abc-8def-0000000000b2"
	offCourseID = "01985e7f-1234-7abc-8def-0000000000c3"
)

// newOfferingAssessmentTestServer wires Offerings + AssessmentDeps + TestSets
// with in-mem repos so the offering-nested routes resolve DB-free.
func newOfferingAssessmentTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *domain.InMemAssessmentRepo, *httpapi.InMemTestSetStore) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	tsStore := httpapi.NewInMemTestSetStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		TestSets:  tsStore,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments:     aRepo,
			Submissions:     sRepo,
			OutboxPublisher: pub,
			TestSets:        tsStore,
		},
	})
	return srv, oRepo, aRepo, tsStore
}

// seedOffering stores a LAUNCHED offering under the standard tenant.
func seedOffering(t *testing.T, oRepo *inmem.OfferingRepo, id string) *domain.Offering {
	t.Helper()
	o, err := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{offCourseID},
		DeliveryType: domain.DeliveryTypeShort,
		Label:        "W3.A Run",
		Capacity:     0,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
	return o
}

// seedPublishedTestSet seeds a single-MCQ test-set forced to PUBLISHED state
// (the W3.A attach gate requires PUBLISHED).
func seedPublishedTestSet(t *testing.T, tsStore *httpapi.InMemTestSetStore, id string) *domain.TestSet {
	t.Helper()
	ts := seedPublishedTestSetWithMCQ(t, tsStore, id)
	ts.State = domain.TestSetStatePublished
	if err := tsStore.Save(context.Background(), ts); err != nil {
		t.Fatalf("re-Save published test_set: %v", err)
	}
	return ts
}

// seedScopedAssessment stores an OPEN assessment scoped to an offering.
func seedScopedAssessment(t *testing.T, aRepo *domain.InMemAssessmentRepo, offeringID string) *domain.Assessment {
	t.Helper()
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		OfferingID:       offeringID,
		Title:            "Scoped",
		ScheduledOpenAt:  time.Now().Add(-1 * time.Minute),
		ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts:      1,
		TotalPoints:      100,
		QuestionCount:    1,
		GradingConfigSnapshot: domain.GradingConfigSnapshot{
			MCQDispatch:             "DETERMINISTIC",
			PassingThresholdPercent: 70,
		},
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	a.State = domain.AssessmentStateOpen
	if err := aRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}
	return a
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/assessments
// -----------------------------------------------------------------------------

func TestOfferingAssessments_Post_CreatesScopedAssessment(t *testing.T) {
	srv, oRepo, _, tsStore := newOfferingAssessmentTestServer(t)
	seedOffering(t, oRepo, offeringA)
	seedPublishedTestSet(t, tsStore, testSetID)

	body, _ := json.Marshal(map[string]any{
		"test_set_id":    testSetID,
		"title_override": "Estimation — Offering A",
	})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringA+"/assessments", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if resp["offering_id"] != offeringA {
		t.Fatalf("offering_id: want %s, got %v", offeringA, resp["offering_id"])
	}
	if resp["test_set_id"] != testSetID {
		t.Fatalf("test_set_id: want %s, got %v", testSetID, resp["test_set_id"])
	}
	// Test-set totals reused (single MCQ worth 100 points).
	if tp, _ := resp["total_points"].(float64); tp != 100 {
		t.Fatalf("total_points: want 100, got %v", resp["total_points"])
	}
	if qc, _ := resp["question_count"].(float64); qc != 1 {
		t.Fatalf("question_count: want 1, got %v", resp["question_count"])
	}
	// Omitted window ⇒ auto-publish FSM lands the row in OPEN.
	if resp["state"] != "OPEN" {
		t.Fatalf("state: want OPEN (auto-publish), got %v", resp["state"])
	}
}

func TestOfferingAssessments_Post_404WhenOfferingMissing(t *testing.T) {
	srv, _, _, tsStore := newOfferingAssessmentTestServer(t)
	// Offering NOT seeded.
	seedPublishedTestSet(t, tsStore, testSetID)

	body, _ := json.Marshal(map[string]any{"test_set_id": testSetID})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringA+"/assessments", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAssessments_Post_400WhenTestSetMissing(t *testing.T) {
	srv, oRepo, _, _ := newOfferingAssessmentTestServer(t)
	seedOffering(t, oRepo, offeringA)
	// Test-set NOT seeded.

	body, _ := json.Marshal(map[string]any{"test_set_id": testSetID})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringA+"/assessments", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAssessments_Post_400WhenTestSetNotPublished(t *testing.T) {
	srv, oRepo, _, tsStore := newOfferingAssessmentTestServer(t)
	seedOffering(t, oRepo, offeringA)
	// DRAFT test-set (seedPublishedTestSetWithMCQ does NOT publish).
	seedPublishedTestSetWithMCQ(t, tsStore, testSetID)

	body, _ := json.Marshal(map[string]any{"test_set_id": testSetID})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringA+"/assessments", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (test-set not PUBLISHED), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAssessments_Post_403WithoutRole(t *testing.T) {
	srv, oRepo, _, tsStore := newOfferingAssessmentTestServer(t)
	seedOffering(t, oRepo, offeringA)
	seedPublishedTestSet(t, tsStore, testSetID)

	body, _ := json.Marshal(map[string]any{"test_set_id": testSetID})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringA+"/assessments", body, instructor, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/assessments
// -----------------------------------------------------------------------------

func TestOfferingAssessments_Get_ReturnsOnlyThisOfferingsAssessments(t *testing.T) {
	srv, oRepo, aRepo, _ := newOfferingAssessmentTestServer(t)
	seedOffering(t, oRepo, offeringA)
	seedOffering(t, oRepo, offeringB)
	aA := seedScopedAssessment(t, aRepo, offeringA)
	_ = seedScopedAssessment(t, aRepo, offeringB)

	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringA+"/assessments", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items         []map[string]any `json:"items"`
		NextPageToken any              `json:"next_page_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items: want 1 (offering A only), got %d body=%s", len(resp.Items), w.Body.String())
	}
	if resp.Items[0]["assessment_id"] != aA.ID {
		t.Fatalf("assessment_id: want %s (A), got %v", aA.ID, resp.Items[0]["assessment_id"])
	}
	if resp.Items[0]["offering_id"] != offeringA {
		t.Fatalf("offering_id: want %s, got %v", offeringA, resp.Items[0]["offering_id"])
	}
}

func TestOfferingAssessments_Get_403WithoutRole(t *testing.T) {
	srv, oRepo, aRepo, _ := newOfferingAssessmentTestServer(t)
	seedOffering(t, oRepo, offeringA)
	_ = seedScopedAssessment(t, aRepo, offeringA)

	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringA+"/assessments", nil, instructor, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}
