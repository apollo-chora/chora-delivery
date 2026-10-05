// offering_completion_policy_test.go — R+ Phase-2 S2: offering-level completion
// policy (makes the Certification tab actionable). The per-course cert config
// stays read-only (DRAFT-locked, authored in A+); this is the editable delivery
// "cert = policy on top". PATCH /api/v1/offerings/{id}/certification.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func newOfferingCompletionPolicyServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
	})
	return srv, oRepo, courseStore
}

func patchCertification(t *testing.T, srv http.Handler, offeringID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+offeringID+"/certification", b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestOfferingCert_SetCompletionPolicy_200AndGetReflectsIt(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCompletionPolicyServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // graduate offering
	seedCJ2Course(t, courseStore, curCourseA, "Course")

	w := patchCertification(t, srv, curOfferingID, "instructor", map[string]any{
		"awards_certificate": true,
		"passing_score_pct":  70,
		"cert_title":         "  Certificate of Completion  ",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var pol map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &pol)
	if pol["awards_certificate"] != true || pol["passing_score_pct"] != float64(70) || pol["cert_title"] != "Certificate of Completion" {
		t.Fatalf("policy DTO: %#v", pol)
	}

	// GET reflects the policy alongside the (read-only) course cert config.
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+curOfferingID+"/certification", nil, instructor, "instructor")
	gw := httptest.NewRecorder()
	srv.ServeHTTP(gw, r)
	if gw.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d body=%s", gw.Code, gw.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(gw.Body.Bytes(), &got)
	cp, ok := got["completion_policy"].(map[string]any)
	if !ok || cp["passing_score_pct"] != float64(70) || cp["awards_certificate"] != true {
		t.Fatalf("GET completion_policy missing/wrong: %#v", got)
	}
}

func TestOfferingCert_SetCompletionPolicy_400WhenScoreOutOfRange(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCompletionPolicyServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course")

	w := patchCertification(t, srv, curOfferingID, "instructor", map[string]any{"passing_score_pct": 150})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("score>100: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCert_SetCompletionPolicy_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newOfferingCompletionPolicyServer(t)
	w := patchCertification(t, srv, curOfferingID, "instructor", map[string]any{"awards_certificate": true})
	if w.Code != http.StatusNotFound {
		t.Fatalf("offering missing: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCert_SetCompletionPolicy_403WithoutRole(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCompletionPolicyServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course")
	w := patchCertification(t, srv, curOfferingID, "", map[string]any{"awards_certificate": true})
	if w.Code != http.StatusForbidden {
		t.Fatalf("no role: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}
