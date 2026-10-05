// offering_launch_readiness_test.go — R+ Phase-2 S5: launch readiness gate.
//
// A GRADUATE offering must have at least one curriculum content item across its
// attached courses before it can launch (you build the curriculum via the
// Curriculum tab, S1). This closes the owner-flagged gap "an empty offering
// Launches freely" (live walk 2026-07-07). Short / async offerings have no
// Curriculum tab, so the gate does not apply to them; and the gate is skipped
// when the course-content service is unwired (dev / unit harnesses) so it never
// blocks a launch on a readiness-check outage.
//
// Uses the curriculum in-mem harness (newOfferingCurriculumTestServer) because
// it wires the CourseContent service the gate consults. PATCH .../launch.
package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// seedShortOffering stores a SHORT-delivery DRAFT offering (the graduate-only
// readiness gate must NOT fire for it).
func seedShortOffering(t *testing.T, oRepo *inmem.OfferingRepo, id, courseID string) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{courseID},
		DeliveryType: delivery.DeliveryTypeShort,
		Label:        "S5 Short Run",
		Capacity:     0,
	})
	if err != nil {
		t.Fatalf("NewOffering(short): %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save short offering: %v", err)
	}
}

// patchOfferingLaunch PATCHes /api/v1/offerings/{id}/launch as the given role.
func patchOfferingLaunch(t *testing.T, srv http.Handler, offeringID, role string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+offeringID+"/launch", nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestOfferingLaunch_422_WhenGraduateHasNoCurriculum(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	// seedCurriculumOffering creates a graduate DRAFT offering over curCourseA.
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Empty Course") // no content items

	w := patchOfferingLaunch(t, srv, curOfferingID, "instructor")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("launch empty graduate: want 422, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingLaunch_200_WhenGraduateHasCurriculum(t *testing.T) {
	srv, oRepo, courseStore, contentSvc := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course With Content")
	seedCourseContentItem(t, contentSvc, curCourseA, cc.KindAtom, curAtomRef, "Lesson 1")

	w := patchOfferingLaunch(t, srv, curOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("launch graduate with curriculum: want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// The gate is graduate-only: a short offering with no curriculum still launches
// (it has no Curriculum tab). Uses the content-wired harness to prove the gate
// deliberately does NOT fire for non-graduate delivery types.
func TestOfferingLaunch_200_ShortCourseNoCurriculumGateSkipped(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	// A short DRAFT offering (seedCurriculumOffering hard-codes graduate, so
	// build one directly here via the repo helper).
	seedShortOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Short Course")

	w := patchOfferingLaunch(t, srv, curOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("launch short w/o curriculum: want 200 (gate graduate-only), got %d body=%s", w.Code, w.Body.String())
	}
}
