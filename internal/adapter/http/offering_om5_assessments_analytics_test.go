// offering_om5_assessments_analytics_test.go — statement-coverage battery
// (TestOm5*) for handleOfferingListAssessments / handleOfferingCreateAssessment
// (offering_assessments_handler.go) + handleOfferingGetAnalytics /
// countOfferingAssessments / rfc3339OrNil (offering_analytics_handler.go): the
// wiring 503s, the pagination loops the real in-mem repo never yields, and the
// repo-error 500s the sibling suites leave out.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// om5GetAssessments GETs the offering-nested assessment list (instructor role).
func om5GetAssessments(t *testing.T, srv http.Handler, offeringID, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/assessments"+query, nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// om5PostAssessment POSTs an offering-nested assessment create (instructor role).
func om5PostAssessment(t *testing.T, srv http.Handler, offeringID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringID+"/assessments", []byte(body), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// om5SeedAssessmentDeps wires the AssessmentDeps the offering-nested surfaces
// need (Assessments + Submissions + TestSets + publisher), the same shape as
// newOfferingAssessmentTestServer but with injectable repos.
func om5SeedAssessmentDeps(t *testing.T, aRepo delivery.AssessmentRepo) (*delivery.InMemSubmissionRepo, *httpapi.InMemTestSetStore, *events.InMemoryPublisher) {
	t.Helper()
	sRepo := delivery.NewInMemSubmissionRepo()
	tsStore := httpapi.NewInMemTestSetStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	return sRepo, tsStore, pub
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/assessments — wiring + list/pagination branches
// -----------------------------------------------------------------------------

func TestOm5Assessments_Get_503WhenAssessmentDepsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOffering(t, oRepo, offeringA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // AssessmentDeps nil
	w := om5GetAssessments(t, srv, offeringA, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (assessment deps unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Assessments_Get_503WhenAssessmentsRepoUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOffering(t, oRepo, offeringA)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:      oRepo,
		AssessmentDeps: &httpapi.AssessmentDeps{}, // Assessments nil
	})
	w := om5GetAssessments(t, srv, offeringA, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (assessments repo unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Assessments_Get_500OnListError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOffering(t, oRepo, offeringA)
	sRepo, _, _ := om5SeedAssessmentDeps(t, nil)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments: &om5ErrAssessmentRepo{failList: true},
			Submissions: sRepo,
		},
	})
	w := om5GetAssessments(t, srv, offeringA, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (assessment list failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// The real in-mem repo never returns a page token, so the paginated tail of
// the envelope (a non-null next_page_token + the follow-up page) is exercised
// through a token-returning double.
func TestOm5Assessments_Get_PaginatedToken(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOffering(t, oRepo, offeringA)
	sRepo, _, _ := om5SeedAssessmentDeps(t, nil)
	inner := delivery.NewInMemAssessmentRepo()
	a1 := seedScopedAssessment(t, inner, offeringA)
	a2 := seedScopedAssessment(t, inner, offeringA)
	paging := &om5PagingAssessmentRepo{InMemAssessmentRepo: inner, page1: []*delivery.Assessment{a1}, page2: []*delivery.Assessment{a2}}
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments: paging,
			Submissions: sRepo,
		},
	})

	first := om5GetAssessments(t, srv, offeringA, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first page: want 200, got %d body=%s", first.Code, first.Body.String())
	}
	var f struct {
		Items         []map[string]any `json:"items"`
		NextPageToken any              `json:"next_page_token"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &f); err != nil {
		t.Fatalf("decode first: %v body=%s", err, first.Body.String())
	}
	if len(f.Items) != 1 || f.NextPageToken != "om5-next-token" {
		t.Fatalf("first page: want 1 item + token, got %d items token=%v", len(f.Items), f.NextPageToken)
	}

	second := om5GetAssessments(t, srv, offeringA, "?page_token=om5-next-token")
	if second.Code != http.StatusOK {
		t.Fatalf("second page: want 200, got %d body=%s", second.Code, second.Body.String())
	}
	var s struct {
		Items         []map[string]any `json:"items"`
		NextPageToken any              `json:"next_page_token"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode second: %v body=%s", err, second.Body.String())
	}
	if len(s.Items) != 1 || s.NextPageToken != nil {
		t.Fatalf("second page: want 1 item + nil token, got %d items token=%v", len(s.Items), s.NextPageToken)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/assessments — wiring + port/save branches
// -----------------------------------------------------------------------------

func TestOm5Assessments_Post_503WhenOfferingsUnwired(t *testing.T) {
	aRepo := delivery.NewInMemAssessmentRepo()
	sRepo, _, _ := om5SeedAssessmentDeps(t, aRepo)
	srv := httpapi.NewServer(httpapi.Deps{
		AssessmentDeps: &httpapi.AssessmentDeps{Assessments: aRepo, Submissions: sRepo},
	})
	w := om5PostAssessment(t, srv, offeringA, `{"test_set_id":"`+testSetID+`"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Assessments_Post_503WhenAssessmentDepsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOffering(t, oRepo, offeringA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // AssessmentDeps nil
	w := om5PostAssessment(t, srv, offeringA, `{"test_set_id":"`+testSetID+`"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (assessment deps unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

// The offering attach path REQUIRES the test-set port to verify the PUBLISHED
// gate — an unwired port is a fail-loud 503, never a silent skip.
func TestOm5Assessments_Post_503WhenTestSetPortMissing(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOffering(t, oRepo, offeringA)
	aRepo := delivery.NewInMemAssessmentRepo()
	sRepo, _, _ := om5SeedAssessmentDeps(t, aRepo)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments: aRepo,
			Submissions: sRepo,
			// TestSets intentionally nil.
		},
	})
	w := om5PostAssessment(t, srv, offeringA, `{"test_set_id":"`+testSetID+`"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (test-set port missing), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Assessments_Post_500OnSaveError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOffering(t, oRepo, offeringA)
	sRepo := delivery.NewInMemSubmissionRepo()
	tsStore := httpapi.NewInMemTestSetStore()
	seedPublishedTestSet(t, tsStore, testSetID)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	failRepo := &om5ErrAssessmentRepo{InMemAssessmentRepo: delivery.NewInMemAssessmentRepo(), failSave: true}
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		TestSets:  tsStore,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments:     failRepo,
			Submissions:     sRepo,
			TestSets:        tsStore,
			OutboxPublisher: pub,
		},
	})
	w := om5PostAssessment(t, srv, offeringA, `{"test_set_id":"`+testSetID+`"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (assessment save failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/analytics — wiring + repo-error branches
// -----------------------------------------------------------------------------

func om5RealAnalyticsParts(t *testing.T) (*delivery.InMemCourseCJ2Store, *delivery.InMemEnrollmentStore, *courseprogress.InMemProgressStore) {
	t.Helper()
	courseStore := delivery.NewInMemCourseCJ2Store()
	enrollments := delivery.NewInMemEnrollmentStore()
	progress := courseprogress.NewInMemProgressStore()
	return courseStore, enrollments, progress
}

func TestOm5Analytics_Get_503WhenOfferingsUnwired(t *testing.T) {
	courseStore, enrollments, progress := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(nil, courseStore, enrollments, delivery.NewInMemAssessmentRepo(), progress, inmem.NewCourseRosterRepo(enrollments))
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Analytics_Get_503WhenCourseCJ2Unwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	_, enrollments, progress := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(oRepo, nil, enrollments, delivery.NewInMemAssessmentRepo(), progress, inmem.NewCourseRosterRepo(enrollments))
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (course port unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Analytics_Get_503WhenAssessmentDepsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	courseStore, enrollments, progress := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(oRepo, courseStore, enrollments, nil, progress, inmem.NewCourseRosterRepo(enrollments))
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (assessment repo unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Analytics_Get_500OnProgressError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	courseStore, enrollments, _ := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(oRepo, courseStore, enrollments, delivery.NewInMemAssessmentRepo(), &om5ErrProgressPort{}, inmem.NewCourseRosterRepo(enrollments))
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (progress lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Analytics_Get_500OnCourseError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	_, enrollments, progress := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(oRepo, &om5ErrCourseStore{failGet: true}, enrollments, delivery.NewInMemAssessmentRepo(), progress, inmem.NewCourseRosterRepo(enrollments))
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (course lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Analytics_Get_500OnRosterError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	courseStore, enrollments, progress := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(oRepo, courseStore, enrollments, delivery.NewInMemAssessmentRepo(), progress, &om5ErrRosterRepo{})
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (roster lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// rfc3339OrNil's non-nil branch: a lifecycle-stamped offering renders real
// RFC3339 timestamps, not nulls.
func TestOm5Analytics_Get_RendersLifecycleTimestamps(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	enrollments := delivery.NewInMemEnrollmentStore()
	progress := courseprogress.NewInMemProgressStore()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{oanaCourseA},
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "TS Run",
		Capacity:     5,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	o.LaunchedAt = &now
	o.ConcludedAt = &now
	o.ID = oanaOfferingID
	if err := oRepo.Save(t.Context(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Timestamped")
	srv := om5AnalyticsServer(oRepo, courseStore, enrollments, delivery.NewInMemAssessmentRepo(), progress, inmem.NewCourseRosterRepo(enrollments))

	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, key := range []string{`"launched_at"`, `"concluded_at"`} {
		if !strings.Contains(body, key) {
			t.Fatalf("lifecycle timestamp %s missing from analytics body: %s", key, body)
		}
	}
	if !strings.Contains(body, "2026-08-01T12:00:00Z") {
		t.Fatalf("timestamps must render as UTC RFC3339 (2026-08-01T12:00:00Z), got %s", body)
	}
}

// countOfferingAssessments' bounded loop CONTINUES past page 1 only when the
// repo returns a next token — impossible with the real in-mem repo, so a
// token-returning double drives both the second iteraction and the exact count.
func TestOm5Analytics_Get_AssessmentCountPaginates(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	courseStore := delivery.NewInMemCourseCJ2Store()
	enrollments := delivery.NewInMemEnrollmentStore()
	progress := courseprogress.NewInMemProgressStore()
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "A")
	seedRosterEnrolment(t, enrollments, tenantID, oanaCourseA, oanaG1)

	inner := delivery.NewInMemAssessmentRepo()
	a1 := seedScopedAssessment(t, inner, oanaOfferingID)
	a2 := seedScopedAssessment(t, inner, oanaOfferingID)
	paging := &om5PagingAssessmentRepo{InMemAssessmentRepo: inner, page1: []*delivery.Assessment{a1}, page2: []*delivery.Assessment{a2}}
	srv := om5AnalyticsServer(oRepo, courseStore, enrollments, paging, progress, inmem.NewCourseRosterRepo(enrollments))

	w, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	// One item on each of two pages → the loop must fold both into the count.
	if resp.AssessmentCount != 2 {
		t.Fatalf("assessment_count: want 2 (two pages), got %d body=%s", resp.AssessmentCount, w.Body.String())
	}
}

func TestOm5Analytics_Get_500OnAssessmentCountError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	courseStore, enrollments, progress := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(oRepo, courseStore, enrollments, &om5ErrAssessmentRepo{failList: true}, progress, inmem.NewCourseRosterRepo(enrollments))
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (assessment count failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Assessments_Post_500OnOfferingLookup(t *testing.T) {
	aRepo := delivery.NewInMemAssessmentRepo()
	sRepo, _, _ := om5SeedAssessmentDeps(t, aRepo)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: &om5ErrOfferingRepo{failGet: true},
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments: aRepo,
			Submissions: sRepo,
		},
	})
	w := om5PostAssessment(t, srv, offeringA, `{"test_set_id":"`+testSetID+`"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (POST offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Analytics_Get_500OnOfferingLookup(t *testing.T) {
	courseStore, enrollments, progress := om5RealAnalyticsParts(t)
	srv := om5AnalyticsServer(&om5ErrOfferingRepo{failGet: true}, courseStore, enrollments, delivery.NewInMemAssessmentRepo(), progress, inmem.NewCourseRosterRepo(enrollments))
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}
