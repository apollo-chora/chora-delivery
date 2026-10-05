// offering_courseid_guard_test.go: the HTTP boundary half of the Offering
// course_id UUID guard.
//
// The domain constructor refuses a non-UUID course (see
// internal/domain/delivery/offering_courseid_guard_test.go). These tests pin the
// STATUS the refusal surfaces as: a mistyped course is the caller's error, so it
// must be a 400 naming the field, never a 500 (a 5xx blames us for their typo
// and never tells them what to fix) and never a silent 201.
//
// Both spellings of the create contract are covered: the 1:N `course_ids` array
// and the legacy singular `course_id`, which is the exact field shape that
// accepted "course-cspo" on the exam aggregate.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestOfferings_Post_NonUUIDCourseIDs_400: a non-UUID inside the 1:N array.
func TestOfferings_Post_NonUUIDCourseIDs_400(t *testing.T) {
	srv, repo := newOfferingServer()
	body := `{
		"course_ids": ["course-cspo"],
		"delivery_type": "graduate",
		"label": "2026 Spring Cohort",
		"capacity": 30
	}`
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		body, offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "course_id") {
		t.Errorf("400 must name the offending field, got %s", got)
	}
	// Fail loud, not fail quiet: nothing may have been persisted.
	list, err := repo.ListByTenant(t.Context(), offTestTenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a rejected offering must not persist, got %d rows", len(list))
	}
}

// TestOfferings_Post_NonUUIDLegacyCourseID_400: the legacy single-course field
// funnels through the same constructor, so it must be guarded identically. This
// is the field that took "course-cspo" on the exam aggregate.
func TestOfferings_Post_NonUUIDLegacyCourseID_400(t *testing.T) {
	srv, _ := newOfferingServer()
	body := `{
		"course_id": "course-cspo",
		"delivery_type": "graduate",
		"label": "2026 Spring Cohort",
		"capacity": 30
	}`
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		body, offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// TestOfferings_Post_NonUUIDSecondCourse_400: the bomb the DB cannot catch. The
// `offerings.course_id UUID NOT NULL` extract column only sees the PRIMARY, so a
// valid first course + a mistyped second is accepted by every other guard and
// detonates later in the cohort-roster / assessment ::uuid casts.
func TestOfferings_Post_NonUUIDSecondCourse_400(t *testing.T) {
	srv, _ := newOfferingServer()
	body := `{
		"course_ids": ["` + offTestCourseID + `", "course-cspo"],
		"delivery_type": "graduate",
		"label": "2026 Spring Cohort",
		"capacity": 30
	}`
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		body, offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "course_ids[1]") {
		t.Errorf("400 must name the offending index, got %s", got)
	}
}

// TestOfferings_Post_CanonicalisesCourseID_201: a lenient-but-Postgres-hostile
// spelling (urn:uuid:) is accepted and stored CANONICAL, so the value the
// ::uuid casts later read is the one form Postgres accepts.
func TestOfferings_Post_CanonicalisesCourseID_201(t *testing.T) {
	srv, _ := newOfferingServer()
	body := `{
		"course_ids": ["urn:uuid:` + offTestCourseID + `"],
		"delivery_type": "graduate",
		"label": "2026 Spring Cohort",
		"capacity": 30
	}`
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		body, offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids, ok := got["course_ids"].([]interface{})
	if !ok || len(ids) != 1 {
		t.Fatalf("course_ids=%v want one entry", got["course_ids"])
	}
	if ids[0] != offTestCourseID {
		t.Fatalf("course_ids[0]=%v want canonical %s", ids[0], offTestCourseID)
	}
}
