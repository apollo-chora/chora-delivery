// exam_sitting_handler_test.go — TDD (RED-first) for the W4 Brick-B operational
// exam-sitting HTTP surface (ExamSitting + ExamInvigilator + IncidentReport;
// ADR-190 D2 + ADR-191).
//
// Endpoints under test (mounted by RegisterExamSittingRoutes):
//
//	POST   /api/v1/exams/{examID}/sittings                                  → create (SCHEDULED)
//	GET    /api/v1/exams/{examID}/sittings                                  → list by exam
//	POST   /api/v1/exams/exam-x/sittings/{sittingID}/{open|begin|close|cancel}     → transition
//	POST   /api/v1/exams/exam-x/sittings/{sittingID}/invigilators                  → assign
//	GET    /api/v1/exams/exam-x/sittings/{sittingID}/invigilators                  → roster
//	DELETE /api/v1/exams/exam-x/sittings/{sittingID}/invigilators/{invigilatorID}  → unassign
//	POST   /api/v1/exams/exam-x/sittings/{sittingID}/incidents                     → file (append-only)
//	GET    /api/v1/exams/exam-x/sittings/{sittingID}/incidents                     → audit trail
//
// Coverage target: >=60% adapter (per .claude/rules/development-execution.md).
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

const (
	stTenantID  = "019e2f93-d586-71b5-8c3d-e2b0d0d5f100"
	stAdminGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d5f101"
	stExamID    = "019e2f93-d586-71b5-8c3d-e2b0d0d5f200"
	stInvigGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d5f300"
	stInvig2    = "019e2f93-d586-71b5-8c3d-e2b0d0d5f301"
	stAdminRole = "training-admin"
)

func sitServer() (http.Handler, *httpapi.ExamSittingDeps) {
	d := &httpapi.ExamSittingDeps{
		Sittings:     inmem.NewSittingRepo(),
		Invigilators: inmem.NewInvigilatorRepo(),
		Incidents:    inmem.NewIncidentRepo(),
	}
	mux := http.NewServeMux()
	httpapi.RegisterExamSittingRoutes(mux, d)
	return mux, d
}

// TestExamRoutes_NoMuxConflict_AllGroups is the regression guard for the
// 2026-07-09 delivery boot panic: the sitting item routes must not alias the
// candidate routes on a shared mux (a Go 1.22 ServeMux registration-time
// panic). sitServer() alone cannot catch it — the conflict only appears when
// the candidate + form groups are registered on the SAME mux, as NewServer does.
func TestExamRoutes_NoMuxConflict_AllGroups(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("exam route groups conflict on a shared mux: %v", r)
		}
	}()
	mux := http.NewServeMux()
	httpapi.RegisterExamFormRoutes(mux, httpapi.ExamFormDeps{
		Forms: inmem.NewExamFormRepo(), Results: inmem.NewExamResultRepo(),
	})
	httpapi.RegisterExamCandidateRoutes(mux, &httpapi.ExamCandidateDeps{
		Candidates: inmem.NewCandidateRepo(),
	})
	httpapi.RegisterExamSittingRoutes(mux, &httpapi.ExamSittingDeps{
		Sittings:     inmem.NewSittingRepo(),
		Invigilators: inmem.NewInvigilatorRepo(),
		Incidents:    inmem.NewIncidentRepo(),
	})
}

func doSit(t *testing.T, h http.Handler, method, path, body, tenantID, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeSitMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

const validWindow = `"starts_at":"2026-08-01T09:00:00Z","ends_at":"2026-08-01T12:00:00Z"`

func createSitting(t *testing.T, h http.Handler) string {
	t.Helper()
	body := `{"room_id":"019e2f93-d586-71b5-8c3d-e2b0d0d5f400",` + validWindow + `,"capacity":30}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusCreated {
		t.Fatalf("createSitting: code=%d body=%s", rec.Code, rec.Body.String())
	}
	return decodeSitMap(t, rec)["id"].(string)
}

// -----------------------------------------------------------------------------
// Sitting — create / list / get
// -----------------------------------------------------------------------------

func TestSitting_Create_Happy(t *testing.T) {
	h, _ := sitServer()
	body := `{` + validWindow + `,"capacity":30}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	m := decodeSitMap(t, rec)
	if m["state"] != "SCHEDULED" || m["exam_id"] != stExamID {
		t.Fatalf("unexpected sitting DTO: %+v", m)
	}
}

func TestSitting_Create_RequiresAdminRole(t *testing.T) {
	h, _ := sitServer()
	body := `{` + validWindow + `,"capacity":30}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body, stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-role code=%d want 403", rec.Code)
	}
}

func TestSitting_Create_BadWindow(t *testing.T) {
	h, _ := sitServer()
	body := `{"starts_at":"2026-08-01T12:00:00Z","ends_at":"2026-08-01T09:00:00Z","capacity":30}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad-window code=%d want 400", rec.Code)
	}
}

func TestSitting_Create_MissingTenant(t *testing.T) {
	h, _ := sitServer()
	body := `{` + validWindow + `,"capacity":30}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/"+stExamID+"/sittings", body, "", stAdminGCID, stAdminRole)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing-tenant code=%d want 400", rec.Code)
	}
}

func TestSitting_List(t *testing.T) {
	h, _ := sitServer()
	id := createSitting(t, h)
	rec := doSit(t, h, http.MethodGet, "/api/v1/exams/"+stExamID+"/sittings", "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list code=%d", rec.Code)
	}
	items := decodeSitMap(t, rec)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list len=%d want 1", len(items))
	}
	if got := items[0].(map[string]any)["id"]; got != id {
		t.Fatalf("listed sitting id=%v want %s", got, id)
	}
}

// -----------------------------------------------------------------------------
// Sitting — transitions
// -----------------------------------------------------------------------------

func TestSitting_Transitions_HappyPath(t *testing.T) {
	h, _ := sitServer()
	id := createSitting(t, h)
	for _, tc := range []struct{ action, want string }{
		{"open", "OPEN"}, {"begin", "IN_PROGRESS"}, {"close", "CLOSED"},
	} {
		rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+id+"/"+tc.action, "", stTenantID, stAdminGCID, stAdminRole)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s code=%d body=%s", tc.action, rec.Code, rec.Body.String())
		}
		if decodeSitMap(t, rec)["state"] != tc.want {
			t.Fatalf("after %s state=%v want %s", tc.action, decodeSitMap(t, rec)["state"], tc.want)
		}
	}
}

func TestSitting_Transition_IllegalIs409(t *testing.T) {
	h, _ := sitServer()
	id := createSitting(t, h)
	// begin before open → 409
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+id+"/begin", "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusConflict {
		t.Fatalf("begin-before-open code=%d want 409", rec.Code)
	}
}

func TestSitting_CancelThenOpenIs409(t *testing.T) {
	h, _ := sitServer()
	id := createSitting(t, h)
	if rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+id+"/cancel", "", stTenantID, stAdminGCID, stAdminRole); rec.Code != http.StatusOK {
		t.Fatalf("cancel code=%d", rec.Code)
	}
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+id+"/open", "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusConflict {
		t.Fatalf("open-cancelled code=%d want 409", rec.Code)
	}
}

func TestSitting_UnknownActionIs400(t *testing.T) {
	h, _ := sitServer()
	id := createSitting(t, h)
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+id+"/frobnicate", "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown-action code=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Invigilator — assign / single-chief / roster / unassign
// -----------------------------------------------------------------------------

func TestInvigilator_Assign_Happy(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"chief_invigilator"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusCreated {
		t.Fatalf("assign code=%d body=%s", rec.Code, rec.Body.String())
	}
	if decodeSitMap(t, rec)["rank"] != "chief_invigilator" {
		t.Fatalf("unexpected rank: %+v", decodeSitMap(t, rec))
	}
}

func TestInvigilator_SecondChiefIs409(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body1 := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"chief_invigilator"}`
	if rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body1, stTenantID, stAdminGCID, stAdminRole); rec.Code != http.StatusCreated {
		t.Fatalf("first chief code=%d", rec.Code)
	}
	body2 := `{"invigilator_gcid":"` + stInvig2 + `","rank":"chief_invigilator"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body2, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second chief code=%d want 409", rec.Code)
	}
}

func TestInvigilator_NonChiefAlongsideChiefOK(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body1 := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"chief_invigilator"}`
	doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body1, stTenantID, stAdminGCID, stAdminRole)
	body2 := `{"invigilator_gcid":"` + stInvig2 + `","rank":"observer"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body2, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusCreated {
		t.Fatalf("observer-alongside-chief code=%d want 201", rec.Code)
	}
}

func TestInvigilator_BadRankIs400(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"boss"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad-rank code=%d want 400", rec.Code)
	}
}

func TestInvigilator_ListAndUnassign(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body := `{"invigilator_gcid":"` + stInvigGCID + `","rank":"invigilator"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", body, stTenantID, stAdminGCID, stAdminRole)
	ivID := decodeSitMap(t, rec)["id"].(string)
	// roster
	rec = doSit(t, h, http.MethodGet, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", "", stTenantID, stAdminGCID, "")
	if len(decodeSitMap(t, rec)["items"].([]any)) != 1 {
		t.Fatalf("roster should have 1")
	}
	// unassign
	rec = doSit(t, h, http.MethodDelete, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators/"+ivID, "", stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("unassign code=%d", rec.Code)
	}
	// roster now empty; a NEW chief is allowed (freed slot)
	rec = doSit(t, h, http.MethodGet, "/api/v1/exams/exam-x/sittings/"+sittingID+"/invigilators", "", stTenantID, stAdminGCID, "")
	if len(decodeSitMap(t, rec)["items"].([]any)) != 0 {
		t.Fatalf("roster should be empty after unassign")
	}
}

// -----------------------------------------------------------------------------
// Incident — file (append-only) / list
// -----------------------------------------------------------------------------

func TestIncident_FileAndList(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body := `{"kind":"device_violation","narrative":"smartwatch detected on wrist"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/incidents", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusCreated {
		t.Fatalf("file incident code=%d body=%s", rec.Code, rec.Body.String())
	}
	m := decodeSitMap(t, rec)
	// reporter defaults to the caller gcid when omitted.
	if m["reported_by_gcid"] != stAdminGCID {
		t.Fatalf("reporter=%v want caller gcid", m["reported_by_gcid"])
	}
	rec = doSit(t, h, http.MethodGet, "/api/v1/exams/exam-x/sittings/"+sittingID+"/incidents", "", stTenantID, stAdminGCID, "")
	if len(decodeSitMap(t, rec)["items"].([]any)) != 1 {
		t.Fatalf("incident trail should have 1")
	}
}

func TestIncident_BadKindIs400(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body := `{"kind":"cheating","narrative":"x"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/incidents", body, stTenantID, stAdminGCID, stAdminRole)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad-kind code=%d want 400", rec.Code)
	}
}

func TestIncident_RequiresAdminRole(t *testing.T) {
	h, _ := sitServer()
	sittingID := createSitting(t, h)
	body := `{"kind":"other","narrative":"disturbance"}`
	rec := doSit(t, h, http.MethodPost, "/api/v1/exams/exam-x/sittings/"+sittingID+"/incidents", body, stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-role code=%d want 403", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Unwired deps → 503
// -----------------------------------------------------------------------------

func TestExamSitting_NilRepos_503(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterExamSittingRoutes(mux, &httpapi.ExamSittingDeps{})
	rec := doSit(t, mux, http.MethodGet, "/api/v1/exams/"+stExamID+"/sittings", "", stTenantID, stAdminGCID, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil sittings repo code=%d want 503", rec.Code)
	}
}
