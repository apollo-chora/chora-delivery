// offering_analytics_handler_test.go — handler-level verification of the R+
// offering-nested Analytics surface: GET /api/v1/offerings/{id}/analytics.
//
// A READ-ONLY delivery-local analytics roll-up for an Offering (the async
// self-paced tab). Every metric is intra-chora_delivery (Offering + CJ#2
// Course + the roster projection over enrolments + the offering's assessments)
// — NO cross-DB query, NO new aggregate, NO migration, NO event. Learner
// COUNTS come from the SAME rostering.CourseRosterRepo the roster tab uses;
// titles from deps.CourseCJ2.Courses; assessment count from
// deps.AssessmentDeps.Assessments.ListByOffering.
//
// avg_progress_pct + completion_rate_pct are NO LONGER deferred (CHO-1827): they
// roll up the CourseLearnerProgress projection, which chora-consumption's
// learning_path.{advanced,completed}.v1 feed via Pub/Sub. Still intra-delivery at
// READ time: the event is the bridge, never a cross-DB join.
//
// Still deferred (flagged, NOT faked): pass_rate (needs a graded-outcome port).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	oanaOfferingID = "01985e7f-5555-7abc-8def-0000000000f1"
	oanaCourseA    = "01985e7f-5555-7abc-8def-000000000a01"
	oanaCourseB    = "01985e7f-5555-7abc-8def-000000000a02"
	oanaG1         = "00000000-0000-7000-9000-00000000e001"
	oanaG2         = "00000000-0000-7000-9000-00000000e002"
	oanaG3         = "00000000-0000-7000-9000-00000000e003"
)

// oanaCourseRow is the wire shape of one attached-course analytics row.
type oanaCourseRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Enrollments int    `json:"enrollments"`
}

// oanaResp is the wire envelope returned by GET .../analytics.
type oanaResp struct {
	State                  string          `json:"state"`
	Capacity               int             `json:"capacity"`
	CapacityUnbounded      bool            `json:"capacity_unbounded"`
	CapacityUtilisationPct *int            `json:"capacity_utilisation_pct"`
	TotalEnrollments       int             `json:"total_enrollments"`
	DistinctLearners       int             `json:"distinct_learners"`
	AssessmentCount        int             `json:"assessment_count"`
	AvgProgressPct         *float64        `json:"avg_progress_pct"`
	CompletionRatePct      *float64        `json:"completion_rate_pct"`
	Courses                []oanaCourseRow `json:"courses"`
}

// newOfferingAnalyticsProgressServer wires the full analytics surface INCLUDING
// the CourseLearnerProgress projection, and hands back the progress store so a
// test can seed real learner progress.
func newOfferingAnalyticsProgressServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store, delivery.EnrollmentPort, *courseprogress.InMemProgressStore) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	enrollments := delivery.NewInMemEnrollmentStore()
	progress := courseprogress.NewInMemProgressStore()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:      oRepo,
		CourseCJ2:      &httpapi.CourseCJ2Deps{Courses: courseStore},
		Rosters:        inmem.NewCourseRosterRepo(enrollments),
		AssessmentDeps: &httpapi.AssessmentDeps{Assessments: delivery.NewInMemAssessmentRepo()},
		CourseProgress: progress,
	})
	return srv, oRepo, courseStore, enrollments, progress
}

func newOfferingAnalyticsTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store, delivery.EnrollmentPort) {
	t.Helper()
	srv, oRepo, courseStore, enrollments, _ := newOfferingAnalyticsProgressServer(t)
	return srv, oRepo, courseStore, enrollments
}

// seedProgress writes one learner's course-progress projection directly through
// the port, exactly as the push inbox would.
func seedProgress(t *testing.T, store *courseprogress.InMemProgressStore, gcid, courseID string, completed, total int, complete bool) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	if _, err := store.Advance(ctx, tenantID, gcid, courseID, "path-"+gcid, completed, total, at); err != nil {
		t.Fatalf("seed Advance: %v", err)
	}
	if complete {
		if _, err := store.Complete(ctx, tenantID, gcid, courseID, "path-"+gcid, at); err != nil {
			t.Fatalf("seed Complete: %v", err)
		}
	}
}

// seedAnalyticsOffering stores an offering with the given capacity over the
// courses (order preserved) under the standard tenant.
func seedAnalyticsOffering(t *testing.T, oRepo *inmem.OfferingRepo, id string, capacity int, courseIDs ...string) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    courseIDs,
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "Analytics Run",
		Capacity:     capacity,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
}

func getOfferingAnalytics(t *testing.T, srv http.Handler, offeringID, gcid, role string) (*httptest.ResponseRecorder, oanaResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/analytics", nil, gcid, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp oanaResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/analytics
// -----------------------------------------------------------------------------

func TestOfferingAnalytics_Get_ReturnsRollup(t *testing.T) {
	srv, oRepo, courseStore, enr := newOfferingAnalyticsTestServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 10, oanaCourseA, oanaCourseB)
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Course A")
	seedOfferingRosterCourse(t, courseStore, oanaCourseB, "Course B")
	// courseA: g1,g2 ; courseB: g2,g3  → 4 total enrolments, 3 distinct people.
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG1)
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG2)
	seedRosterEnrolment(t, enr, tenantID, oanaCourseB, oanaG2)
	seedRosterEnrolment(t, enr, tenantID, oanaCourseB, oanaG3)

	w, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.TotalEnrollments != 4 {
		t.Fatalf("total_enrollments: want 4, got %d body=%s", resp.TotalEnrollments, w.Body.String())
	}
	if resp.DistinctLearners != 3 {
		t.Fatalf("distinct_learners: want 3 (g2 deduped), got %d", resp.DistinctLearners)
	}
	if resp.State != "DRAFT" {
		t.Fatalf("state: want DRAFT, got %q", resp.State)
	}
	if resp.Capacity != 10 || resp.CapacityUnbounded {
		t.Fatalf("capacity: want 10/bounded, got %d/unbounded=%v", resp.Capacity, resp.CapacityUnbounded)
	}
	// utilisation = distinct(3)/capacity(10) = 30%.
	if resp.CapacityUtilisationPct == nil || *resp.CapacityUtilisationPct != 30 {
		t.Fatalf("capacity_utilisation_pct: want 30, got %v", resp.CapacityUtilisationPct)
	}
	if resp.AssessmentCount != 0 {
		t.Fatalf("assessment_count: want 0, got %d", resp.AssessmentCount)
	}
	if len(resp.Courses) != 2 {
		t.Fatalf("courses: want 2, got %d", len(resp.Courses))
	}
	if resp.Courses[0].ID != oanaCourseA || resp.Courses[0].Title != "Course A" || resp.Courses[0].Enrollments != 2 {
		t.Fatalf("course[0]: want {A,Course A,2}, got %+v", resp.Courses[0])
	}
	if resp.Courses[1].ID != oanaCourseB || resp.Courses[1].Enrollments != 2 {
		t.Fatalf("course[1]: want {B,_,2}, got %+v", resp.Courses[1])
	}
}

func TestOfferingAnalytics_Get_UnboundedCapacityNullUtilisation(t *testing.T) {
	srv, oRepo, courseStore, enr := newOfferingAnalyticsTestServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA) // capacity 0 = unbounded (async)
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Self-Paced")
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG1)

	w, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !resp.CapacityUnbounded {
		t.Fatalf("capacity_unbounded: want true for capacity 0, got false")
	}
	if resp.CapacityUtilisationPct != nil {
		t.Fatalf("capacity_utilisation_pct: want null when unbounded, got %v", *resp.CapacityUtilisationPct)
	}
	if resp.TotalEnrollments != 1 || resp.DistinctLearners != 1 {
		t.Fatalf("counts: want 1/1, got %d/%d", resp.TotalEnrollments, resp.DistinctLearners)
	}
}

func TestOfferingAnalytics_Get_EmptyRostersZeroCounts(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingAnalyticsTestServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 5, oanaCourseA)
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Empty")

	w, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.TotalEnrollments != 0 || resp.DistinctLearners != 0 {
		t.Fatalf("counts: want 0/0, got %d/%d", resp.TotalEnrollments, resp.DistinctLearners)
	}
	if resp.CapacityUtilisationPct == nil || *resp.CapacityUtilisationPct != 0 {
		t.Fatalf("utilisation: want 0 (0/5), got %v", resp.CapacityUtilisationPct)
	}
	if len(resp.Courses) != 1 || resp.Courses[0].Enrollments != 0 {
		t.Fatalf("courses: want 1 course w/ 0 enrolments, got %+v", resp.Courses)
	}
}

func TestOfferingAnalytics_Get_404WhenMissing(t *testing.T) {
	srv, _, _, _ := newOfferingAnalyticsTestServer(t)
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor") // not seeded
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAnalytics_Get_403WithoutRole(t *testing.T) {
	srv, oRepo, _, _ := newOfferingAnalyticsTestServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAnalytics_Post_405(t *testing.T) {
	srv, oRepo, _, _ := newOfferingAnalyticsTestServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+oanaOfferingID+"/analytics", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// ASYNC progress roll-up (CHO-1827): the gap this closes
// -----------------------------------------------------------------------------

// The headline: an async offering must report REAL learner progress, not just
// who enrolled. g1 is 5/10 done, g2 enrolled and never started ⇒ (50+0)/2 = 25.
func TestOfferingAnalytics_Get_ReportsAvgProgressFromProjection(t *testing.T) {
	srv, oRepo, courseStore, enr, prog := newOfferingAnalyticsProgressServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA) // capacity 0 = async
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Self-Paced")
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG1)
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG2)
	seedProgress(t, prog, oanaG1, oanaCourseA, 5, 10, false)

	w, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.AvgProgressPct == nil {
		t.Fatal("avg_progress_pct: want 25, got null. The ASYNC progress gap is still open")
	}
	if *resp.AvgProgressPct != 25 {
		t.Fatalf("avg_progress_pct: want 25 (the unstarted learner counts as 0), got %v", *resp.AvgProgressPct)
	}
	if resp.CompletionRatePct == nil || *resp.CompletionRatePct != 0 {
		t.Fatalf("completion_rate_pct: want 0, got %v", resp.CompletionRatePct)
	}
}

func TestOfferingAnalytics_Get_ReportsCompletionRate(t *testing.T) {
	srv, oRepo, courseStore, enr, prog := newOfferingAnalyticsProgressServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Self-Paced")
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG1)
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG2)
	seedProgress(t, prog, oanaG1, oanaCourseA, 10, 10, true)
	seedProgress(t, prog, oanaG2, oanaCourseA, 2, 10, false)

	_, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if resp.CompletionRatePct == nil || *resp.CompletionRatePct != 50 {
		t.Fatalf("completion_rate_pct: want 50 (1 of 2), got %v", resp.CompletionRatePct)
	}
	// (100 + 20) / 2 = 60
	if resp.AvgProgressPct == nil || *resp.AvgProgressPct != 60 {
		t.Fatalf("avg_progress_pct: want 60, got %v", resp.AvgProgressPct)
	}
}

// An offering with no enrolments has UNDEFINED progress. Rendering 0 would say
// "nobody is learning" when the truth is "nobody is here".
func TestOfferingAnalytics_Get_NoEnrolmentsProgressIsNull(t *testing.T) {
	srv, oRepo, courseStore, _, _ := newOfferingAnalyticsProgressServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Empty")

	w, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if resp.AvgProgressPct != nil {
		t.Fatalf("avg_progress_pct: want null with no enrolments, got %v", *resp.AvgProgressPct)
	}
	if resp.CompletionRatePct != nil {
		t.Fatalf("completion_rate_pct: want null with no enrolments, got %v", *resp.CompletionRatePct)
	}
}

// The offering-level number averages over every (learner, course) enrolment.
func TestOfferingAnalytics_Get_ProgressSpansCourses(t *testing.T) {
	srv, oRepo, courseStore, enr, prog := newOfferingAnalyticsProgressServer(t)
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA, oanaCourseB)
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "A")
	seedOfferingRosterCourse(t, courseStore, oanaCourseB, "B")
	seedRosterEnrolment(t, enr, tenantID, oanaCourseA, oanaG1)
	seedRosterEnrolment(t, enr, tenantID, oanaCourseB, oanaG1)
	seedProgress(t, prog, oanaG1, oanaCourseA, 10, 10, true) // 100%
	seedProgress(t, prog, oanaG1, oanaCourseB, 0, 10, false) // 0%

	_, resp := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if resp.AvgProgressPct == nil || *resp.AvgProgressPct != 50 {
		t.Fatalf("avg_progress_pct: want 50 across 2 enrolments, got %v", resp.AvgProgressPct)
	}
	if resp.CompletionRatePct == nil || *resp.CompletionRatePct != 50 {
		t.Fatalf("completion_rate_pct: want 50 (1 of 2 enrolments), got %v", resp.CompletionRatePct)
	}
}

// Progress is a distinct dependency: the tab must not silently render
// enrolment-only when the projection is unwired. Fail loud, like every other dep.
func TestOfferingAnalytics_Get_503WhenProgressUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	enrollments := delivery.NewInMemEnrollmentStore()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:      oRepo,
		CourseCJ2:      &httpapi.CourseCJ2Deps{Courses: courseStore},
		Rosters:        inmem.NewCourseRosterRepo(enrollments),
		AssessmentDeps: &httpapi.AssessmentDeps{Assessments: delivery.NewInMemAssessmentRepo()},
		// CourseProgress intentionally nil.
	})
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	seedOfferingRosterCourse(t, courseStore, oanaCourseA, "Self-Paced")

	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503 (course-progress unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingAnalytics_Get_503WhenRostersUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:      oRepo,
		CourseCJ2:      &httpapi.CourseCJ2Deps{Courses: courseStore},
		AssessmentDeps: &httpapi.AssessmentDeps{Assessments: delivery.NewInMemAssessmentRepo()},
		// Rosters intentionally nil.
	})
	seedAnalyticsOffering(t, oRepo, oanaOfferingID, 0, oanaCourseA)
	w, _ := getOfferingAnalytics(t, srv, oanaOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503 (rosters unwired), got %d body=%s", w.Code, w.Body.String())
	}
}
