// offering_roster_handler_test.go — handler-level verification of the R+
// offering-nested Roster surface: GET /api/v1/offerings/{id}/roster.
//
// A READ-ONLY per-course roster of the learners enrolled across an Offering's
// attached courses. Intra-chora_delivery (Offering + CJ#2 Course + Enrollment
// share the DB per ddd-enforcement #3) — NO cross-DB query, NO new aggregate,
// NO migration, NO new event. Learners come from the SAME
// rostering.CourseRosterRepo the standalone /api/v1/rosters/{courseId} view
// uses (a projection over the canonical EnrollmentPort); titles from the SAME
// deps.CourseCJ2.Courses port the Curriculum + Certification surfaces use.
// Mirrors offering_certification_handler_test.go's in-mem harness +
// reqWithHeaders, and the enrolment-seeding idiom of roster_handler_test.go.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	orostOfferingID  = "01985e7f-4444-7abc-8def-0000000000f1"
	orostCourseA     = "01985e7f-4444-7abc-8def-000000000a01"
	orostCourseB     = "01985e7f-4444-7abc-8def-000000000a02"
	orostGCID1       = "00000000-0000-7000-9000-00000000d001"
	orostGCID2       = "00000000-0000-7000-9000-00000000d002"
	orostGCID3       = "00000000-0000-7000-9000-00000000d003"
	orostOtherTenant = "22222222-2222-7222-8222-222222222222"
)

// orostLearner is the wire shape of one roster row.
type orostLearner struct {
	GCID        string `json:"gcid"`
	DisplayName string `json:"display_name"`
	ProgressPct int    `json:"progress_pct"`
	EnrolledAt  string `json:"enrolled_at"`
}

// orostCourseRow is the wire shape of one attached course + its learner roster.
type orostCourseRow struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Learners     []orostLearner `json:"learners"`
	LearnerCount int            `json:"learner_count"`
}

// orostResp is the wire envelope returned by GET .../roster.
type orostResp struct {
	Courses              []orostCourseRow `json:"courses"`
	DistinctLearnerCount int              `json:"distinct_learner_count"`
}

// newOfferingRosterTestServer wires Offerings + CourseCJ2.Courses + a
// CourseRosterRepo bound to a fresh in-mem enrolment store so the roster route
// resolves DB-free. Returns the server + the seedable repos/port.
func newOfferingRosterTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store, delivery.EnrollmentPort) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	enrollments := delivery.NewInMemEnrollmentStore()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
		Rosters:   inmem.NewCourseRosterRepo(enrollments),
	})
	return srv, oRepo, courseStore, enrollments
}

// seedOfferingRoster stores a DRAFT offering over the given courses (order
// preserved) under the given tenant.
func seedOfferingRoster(t *testing.T, oRepo *inmem.OfferingRepo, id, tenant string, courseIDs ...string) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenant,
		CourseIDs:    courseIDs,
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "W7 Roster Run",
		Capacity:     0,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
}

// seedOfferingRosterCourse stores a CJ#2 Course shell carrying a title (the
// roster read needs only the title; no cert config).
func seedOfferingRosterCourse(t *testing.T, store *delivery.InMemCourseCJ2Store, id, title string) {
	t.Helper()
	c, err := delivery.NewCJ2Course(delivery.NewCJ2CourseInput{
		TenantID:   tenantID,
		AuthorGCID: instructor,
		Title:      title,
		TestSetIDs: []string{testSetID},
	})
	if err != nil {
		t.Fatalf("NewCJ2Course: %v", err)
	}
	c.ID = id
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatalf("Save course: %v", err)
	}
}

// seedRosterEnrolment registers one learner on (tenant, courseID) so the
// CourseRosterRepo materialises them into the course roster.
func seedRosterEnrolment(t *testing.T, enr delivery.EnrollmentPort, tenant, courseID, gcid string) {
	t.Helper()
	if _, err := enr.Register(context.Background(), tenant, courseID, gcid); err != nil {
		t.Fatalf("Register(%s): %v", gcid, err)
	}
}

func getOfferingRoster(t *testing.T, srv http.Handler, offeringID, gcid, role string) (*httptest.ResponseRecorder, orostResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/roster", nil, gcid, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp orostResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/roster — happy paths
// -----------------------------------------------------------------------------

func TestOfferingRoster_Get_ReturnsPerCourseLearners(t *testing.T) {
	srv, oRepo, courseStore, enr := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)
	seedOfferingRosterCourse(t, courseStore, orostCourseA, "Calculus I")
	seedRosterEnrolment(t, enr, tenantID, orostCourseA, orostGCID1)
	seedRosterEnrolment(t, enr, tenantID, orostCourseA, orostGCID2)

	w, resp := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d body=%s", len(resp.Courses), w.Body.String())
	}
	got := resp.Courses[0]
	if got.ID != orostCourseA {
		t.Fatalf("course id: want %s, got %s", orostCourseA, got.ID)
	}
	if got.Title != "Calculus I" {
		t.Fatalf("course title: want Calculus I, got %q", got.Title)
	}
	if got.LearnerCount != 2 || len(got.Learners) != 2 {
		t.Fatalf("learners: want 2 (count=2), got count=%d len=%d body=%s", got.LearnerCount, len(got.Learners), w.Body.String())
	}
	if resp.DistinctLearnerCount != 2 {
		t.Fatalf("distinct_learner_count: want 2, got %d", resp.DistinctLearnerCount)
	}
	// A learner row carries GCID + display_name (= GCID fallback, unwired
	// identity projection) + progress_pct (= 0 default) + enrolled_at (RFC3339).
	l0 := got.Learners[0]
	if l0.GCID == "" {
		t.Fatalf("first learner missing gcid; body=%s", w.Body.String())
	}
	if l0.DisplayName != l0.GCID {
		t.Fatalf("display_name fallback: want = gcid %q, got %q", l0.GCID, l0.DisplayName)
	}
	if l0.ProgressPct != 0 {
		t.Fatalf("progress_pct: want 0 (unwired projection), got %d", l0.ProgressPct)
	}
	if l0.EnrolledAt == "" {
		t.Fatalf("enrolled_at missing; body=%s", w.Body.String())
	}
}

func TestOfferingRoster_Get_EmptyLearnersWhenCourseHasNoEnrolments(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)
	seedOfferingRosterCourse(t, courseStore, orostCourseA, "Empty Cohort")
	// No enrolments registered ⇒ an EMPTY learners list (200, learners: []).

	w, resp := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d", len(resp.Courses))
	}
	if resp.Courses[0].LearnerCount != 0 || len(resp.Courses[0].Learners) != 0 {
		t.Fatalf("learners: want empty, got %#v", resp.Courses[0].Learners)
	}
	// Each course's learners must marshal as `[]` not `null`.
	if !strings.Contains(w.Body.String(), `"learners":[]`) {
		t.Fatalf("body must contain learners=[], got %s", w.Body.String())
	}
}

func TestOfferingRoster_Get_PreservesCourseOrder(t *testing.T) {
	srv, oRepo, courseStore, enr := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA, orostCourseB)
	seedOfferingRosterCourse(t, courseStore, orostCourseA, "First")
	seedOfferingRosterCourse(t, courseStore, orostCourseB, "Second")
	seedRosterEnrolment(t, enr, tenantID, orostCourseA, orostGCID1)
	seedRosterEnrolment(t, enr, tenantID, orostCourseB, orostGCID2)

	w, resp := getOfferingRoster(t, srv, orostOfferingID, instructor, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 2 {
		t.Fatalf("courses: want 2, got %d", len(resp.Courses))
	}
	if resp.Courses[0].ID != orostCourseA || resp.Courses[1].ID != orostCourseB {
		t.Fatalf("order: want [%s,%s], got [%s,%s]", orostCourseA, orostCourseB, resp.Courses[0].ID, resp.Courses[1].ID)
	}
	if resp.Courses[0].LearnerCount != 1 || resp.Courses[1].LearnerCount != 1 {
		t.Fatalf("per-course counts: want [1,1], got [%d,%d]", resp.Courses[0].LearnerCount, resp.Courses[1].LearnerCount)
	}
}

// distinct_learner_count counts PEOPLE, not enrolment rows: a learner enrolled
// in two attached courses must count once at the offering level even though
// each course's learner_count includes them.
func TestOfferingRoster_Get_DistinctLearnerCountDedupesAcrossCourses(t *testing.T) {
	srv, oRepo, courseStore, enr := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA, orostCourseB)
	seedOfferingRosterCourse(t, courseStore, orostCourseA, "Shared A")
	seedOfferingRosterCourse(t, courseStore, orostCourseB, "Shared B")
	// Same learner (GCID1) enrolled in BOTH courses; GCID2 only in A.
	seedRosterEnrolment(t, enr, tenantID, orostCourseA, orostGCID1)
	seedRosterEnrolment(t, enr, tenantID, orostCourseA, orostGCID2)
	seedRosterEnrolment(t, enr, tenantID, orostCourseB, orostGCID1)

	w, resp := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	// Per-course counts sum to 3 (A: GCID1+GCID2, B: GCID1)…
	if resp.Courses[0].LearnerCount != 2 || resp.Courses[1].LearnerCount != 1 {
		t.Fatalf("per-course counts: want [2,1], got [%d,%d]", resp.Courses[0].LearnerCount, resp.Courses[1].LearnerCount)
	}
	// …but only 2 distinct people (GCID1 counted once).
	if resp.DistinctLearnerCount != 2 {
		t.Fatalf("distinct_learner_count: want 2 (GCID1 deduped), got %d", resp.DistinctLearnerCount)
	}
}

// -----------------------------------------------------------------------------
// Isolation + guards
// -----------------------------------------------------------------------------

func TestOfferingRoster_Get_TenantIsolation_FiltersCrossTenantEnrolments(t *testing.T) {
	srv, oRepo, courseStore, enr := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)
	seedOfferingRosterCourse(t, courseStore, orostCourseA, "Guarded")
	// One enrolment for this tenant + one on the SAME course for a DIFFERENT
	// tenant — the cross-tenant row must be invisible to a tenantID caller.
	seedRosterEnrolment(t, enr, tenantID, orostCourseA, orostGCID1)
	seedRosterEnrolment(t, enr, orostOtherTenant, orostCourseA, orostGCID3)

	w, resp := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.Courses[0].LearnerCount != 1 {
		t.Fatalf("learner_count: want 1 (cross-tenant row filtered), got %d body=%s", resp.Courses[0].LearnerCount, w.Body.String())
	}
	if resp.DistinctLearnerCount != 1 {
		t.Fatalf("distinct_learner_count: want 1, got %d", resp.DistinctLearnerCount)
	}
}

func TestOfferingRoster_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _, courseStore, _ := newOfferingRosterTestServer(t)
	seedOfferingRosterCourse(t, courseStore, orostCourseA, "Orphan") // offering NOT seeded

	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Get_404WhenOfferingCrossTenant(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingRosterTestServer(t)
	// Offering owned by ANOTHER tenant — a tenantID caller must not see it.
	seedOfferingRoster(t, oRepo, orostOfferingID, orostOtherTenant, orostCourseA)
	seedOfferingRosterCourse(t, courseStore, orostCourseA, "Other-tenant course")

	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 (cross-tenant offering), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Get_403WithoutRole(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)

	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "") // no instructor/admin role
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Get_401WithoutGCID(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)

	w, _ := getOfferingRoster(t, srv, orostOfferingID, "", "instructor") // gcid header empty
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", w.Code, w.Body.String())
	}
}

// GET reads the roster, POST enrols (S3); PUT is unsupported → 405.
func TestOfferingRoster_Put_405(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRosterTestServer(t)
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)

	r := reqWithHeaders(http.MethodPut, "/api/v1/offerings/"+orostOfferingID+"/roster", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Get_503WhenRostersUnwired(t *testing.T) {
	// Offerings + CourseCJ2 wired but NO Rosters repo — the handler must
	// fail loud (503), never fabricate an empty roster.
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
	})
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)

	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503 (rosters unwired), got %d body=%s", w.Code, w.Body.String())
	}
}
