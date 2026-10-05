// exam_sf_eligible_test.go — the exam list DTO must SERVE SkillsFuture
// eligibility rather than let the frontend invent it (R6 D1, the backend half).
//
// The A+ / R+ exams list painted a "SkillsFuture" badge on every sitting in
// every tenant, because the frontend mapper hardcoded `skillsFutureAligned:
// true`. The flag is real and lives on the course (`delivery.Course.SFEligible`,
// set at /release), but no read joined it onto an exam, so the frontend had
// nothing to read and faked it. The badge is now removed; this puts it back as
// a SERVED value.
//
// The third case is the one that decides the wire shape. When an exam's course
// cannot be resolved, the honest answer is not `false` — that is a claim that
// the course is not SkillsFuture funded, which we do not know. The field is
// OMITTED instead, so absent means unknown and the badge stays off. A metric is
// served or absent, never faked, and `false` here would be faking it.
//
// TDD: written FIRST, RED before the join existed.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// newExamServerWithCourses wires the exam routes plus a course repo the test
// can seed, so the join has something to join to.
func newExamServerWithCourses() (http.Handler, *inmem.CourseRepo) {
	courses := inmem.NewCourseRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:  courses,
		Bookings: inmem.NewBookingRepo(),
		Exams:    inmem.NewExamRepo(),
	})
	return srv, courses
}

// seedCourse puts a course in the repo with the given SkillsFuture eligibility.
func seedCourse(t *testing.T, courses *inmem.CourseRepo, id string, sf bool) {
	t.Helper()
	err := courses.Save(context.Background(), &delivery.Course{
		ID:          id,
		TenantID:    examTestTenantID,
		Title:       "Certified Scrum Product Owner",
		MaxCapacity: 40,
		SFEligible:  sf,
	})
	if err != nil {
		t.Fatalf("seed course: %v", err)
	}
}

// firstExamItem returns the single exam row from a list response.
func firstExamItem(t *testing.T, srv http.Handler) map[string]interface{} {
	t.Helper()
	rec := doExam(t, srv, "GET", "/api/v1/exams",
		"", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, ok := got["items"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("want exactly 1 item, got %v", got["items"])
	}
	row, ok := items[0].(map[string]interface{})
	if !ok {
		t.Fatalf("item is not an object: %v", items[0])
	}
	return row
}

func TestExamList_ServesSFEligible_True(t *testing.T) {
	srv, courses := newExamServerWithCourses()
	seedCourse(t, courses, examTestCourseID, true)
	_ = doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")

	row := firstExamItem(t, srv)

	got, present := row["sf_eligible"]
	if !present {
		t.Fatalf("sf_eligible absent; the frontend cannot read what is not served: %v", row)
	}
	if got != true {
		t.Errorf("sf_eligible = %v; want true (the course is SF eligible)", got)
	}
}

func TestExamList_ServesSFEligible_False(t *testing.T) {
	// The half that matters most: a NON-eligible course must come back false,
	// not absent, or the frontend cannot tell "not funded" from "unknown".
	srv, courses := newExamServerWithCourses()
	seedCourse(t, courses, examTestCourseID, false)
	_ = doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")

	row := firstExamItem(t, srv)

	got, present := row["sf_eligible"]
	if !present {
		t.Fatalf("sf_eligible absent for a resolved course; want an explicit false: %v", row)
	}
	if got != false {
		t.Errorf("sf_eligible = %v; want false", got)
	}
}

func TestExamList_OmitsSFEligible_WhenCourseUnresolved(t *testing.T) {
	// No course seeded. `false` here would assert the course is not SF funded,
	// which is not known. Absent means unknown and the badge stays off.
	srv, _ := newExamServerWithCourses()
	_ = doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")

	row := firstExamItem(t, srv)

	if v, present := row["sf_eligible"]; present {
		t.Errorf("sf_eligible = %v; want ABSENT when the course cannot be resolved", v)
	}
}

func TestExamList_SFEligible_DoesNotDisturbTheRestOfTheDTO(t *testing.T) {
	// A positive control on the join: adding a field must not drop the ones the
	// list already served, and must not turn the row into a different shape.
	srv, courses := newExamServerWithCourses()
	seedCourse(t, courses, examTestCourseID, true)
	_ = doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")

	row := firstExamItem(t, srv)

	for _, key := range []string{
		"id", "tenant_id", "course_id", "title", "scheduled_at",
		"duration_minutes", "capacity", "enrolled_count", "state",
		"proctor_method", "created_at", "updated_at",
	} {
		if _, ok := row[key]; !ok {
			t.Errorf("the join dropped %q from the exam DTO", key)
		}
	}
}
