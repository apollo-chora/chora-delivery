// offering_module_progress_handler_test.go — the W7 progress read surface:
// a learner sees own (enrolment-gated), an instructor sees the cohort.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	inmem "github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

const mpCID = "01970000-0000-7000-9000-0000000000a1"

// seedProgressReadServer wires the four deps + a course module (with one atom
// item) whose CONTENT the learner has completed, and enrols the learner.
func seedProgressReadServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	modStore := module.NewInMemModuleStore()
	progStore := moduleprogress.NewInMemProgressStore()
	enroll := domain.NewInMemEnrollmentStore()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo, Modules: modStore, ModuleProgress: progStore, Enrollments: enroll,
	})
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	ctx := context.Background()
	m, err := module.New(module.NewParams{TenantID: tenantID, CourseID: curCourseA, Title: "M1"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	item, err := m.AddItem(mpCID)
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if _, err := modStore.Create(ctx, m); err != nil {
		t.Fatalf("modStore.Create: %v", err)
	}
	if _, err := enroll.Register(ctx, tenantID, curCourseA, learner); err != nil {
		t.Fatalf("enroll.Register: %v", err)
	}
	changed, err := progStore.Advance(ctx, tenantID, learner, m.ID, curCourseA, item.ContentItemID, m)
	if err != nil || !changed {
		t.Fatalf("Advance: changed=%v err=%v", changed, err)
	}
	return srv, m.ID
}

func getProgress(t *testing.T, srv http.Handler, gcid, role, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+curOfferingID+"/modules/progress"+query, nil, gcid, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestOfferingModuleProgress_Learner_SeesOwnComplete(t *testing.T) {
	srv, _ := seedProgressReadServer(t)
	w := getProgress(t, srv, learner, "learner", "?course_id="+curCourseA)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ViewerRole string `json:"viewer_role"`
		Modules    []struct {
			Total          int  `json:"total"`
			CompletedCount int  `json:"completed_count"`
			IsComplete     bool `json:"is_complete"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ViewerRole != "learner" || len(resp.Modules) != 1 {
		t.Fatalf("want learner view + 1 module, got %s + %d", resp.ViewerRole, len(resp.Modules))
	}
	mod := resp.Modules[0]
	if mod.Total != 1 || mod.CompletedCount != 1 || !mod.IsComplete {
		t.Fatalf("want 1/1 complete, got total=%d completed=%d complete=%v", mod.Total, mod.CompletedCount, mod.IsComplete)
	}
}

func TestOfferingModuleProgress_Instructor_SeesCohort(t *testing.T) {
	srv, _ := seedProgressReadServer(t)
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ViewerRole string `json:"viewer_role"`
		Modules    []struct {
			Learners []struct {
				GCID           string `json:"gcid"`
				CompletedCount int    `json:"completed_count"`
				IsComplete     bool   `json:"is_complete"`
			} `json:"learners"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ViewerRole != "instructor" || len(resp.Modules) != 1 || len(resp.Modules[0].Learners) != 1 {
		t.Fatalf("want instructor cohort with 1 learner, got %s / %d modules", resp.ViewerRole, len(resp.Modules))
	}
	l := resp.Modules[0].Learners[0]
	if l.GCID != learner || l.CompletedCount != 1 || !l.IsComplete {
		t.Fatalf("cohort row mismatch: %+v", l)
	}
}

func TestOfferingModuleProgress_NonEnrolledLearner_403(t *testing.T) {
	srv, _ := seedProgressReadServer(t)
	other := "00000000-0000-7000-8000-0000000000ff"
	w := getProgress(t, srv, other, "learner", "?course_id="+curCourseA)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-enrolled learner: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingModuleProgress_MissingCourseID_400(t *testing.T) {
	srv, _ := seedProgressReadServer(t)
	w := getProgress(t, srv, instructor, "instructor", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing course_id: want 400, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// A+ learner course-only self view: GET /api/v1/me/module-progress?course_id=X
// -----------------------------------------------------------------------------

func getMeProgress(t *testing.T, srv http.Handler, gcid, role, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/me/module-progress"+query, nil, gcid, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestMeModuleProgress_Learner_SeesOwn(t *testing.T) {
	srv, _ := seedProgressReadServer(t) // enrols `learner` + advances 1/1 in curCourseA
	w := getMeProgress(t, srv, learner, "learner", "?course_id="+curCourseA)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		CourseID string `json:"course_id"`
		Modules  []struct {
			Total          int  `json:"total"`
			CompletedCount int  `json:"completed_count"`
			IsComplete     bool `json:"is_complete"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CourseID != curCourseA || len(resp.Modules) != 1 {
		t.Fatalf("want course + 1 module, got %s / %d", resp.CourseID, len(resp.Modules))
	}
	if m := resp.Modules[0]; m.Total != 1 || m.CompletedCount != 1 || !m.IsComplete {
		t.Fatalf("want 1/1 complete, got %+v", m)
	}
}

func TestMeModuleProgress_NonEnrolled_403(t *testing.T) {
	srv, _ := seedProgressReadServer(t)
	other := "00000000-0000-7000-8000-0000000000ff"
	w := getMeProgress(t, srv, other, "learner", "?course_id="+curCourseA)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-enrolled learner: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMeModuleProgress_MissingCourseID_400(t *testing.T) {
	srv, _ := seedProgressReadServer(t)
	w := getMeProgress(t, srv, learner, "learner", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing course_id: want 400, got %d", w.Code)
	}
}
