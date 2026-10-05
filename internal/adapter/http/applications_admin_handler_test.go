// applications_admin_handler_test.go — TDD spec for the R+ admin
// Course-Application review queue.
//
// Endpoint set (admin scope — distinct from the learner-side
// /v1/me/applications routes in applications.go):
//
//	GET /api/v1/applications          — list ALL applications in the
//	                                    current tenant (optional ?state=
//	                                    filter), newest first.
//	GET /api/v1/applications/{id}     — detail (admin view; bypasses the
//	                                    owner-or-404 guard the learner
//	                                    surface enforces).
//
// RBAC (verified inside the handler, NOT at middleware):
//
//   - tenantRequired enforces X-Tenant-Id presence (400 if missing).
//   - gcid header required (401 if missing) — Bucket 4 servicemesh
//     propagation guarantees this when the caller's JWT is valid.
//   - Caller MUST carry the `training-admin` typed role in
//     `x-mesh-user-roles` (Bucket 4 servicemesh header — comma-separated
//     lowercase roles). `admin` is also accepted as a superset role.
//     Every other role (learner / instructor-only / auditor / empty)
//     resolves to 403 — the admin queue is gated to training-admins
//     only because it exposes every learner's application status
//     across the tenant.
//
// State filter values (per ADR-164 §1 / `application.Status`):
//
//	SUBMITTED | IN_REVIEW | OFFER_MADE | ACCEPTED | PAID | ENROLLED |
//	REJECTED | WITHDRAWN
//
// Note: the wire-level state names match the brief's UPPER_SNAKE_CASE
// convention. Internally the domain `application.Status` enum uses
// lowercase snake_case (`under_review` not `IN_REVIEW`). The handler
// normalises both forms — the canonical wire form is UPPER_SNAKE_CASE
// per the FE brief but lowercase `under_review`/`submitted`/... also
// match for parity with the existing applicationDTO output shape.
//
// Strict TDD per .claude/rules/development-execution.md: this test
// file is written BEFORE applications_admin_handler.go, so a `go test`
// run pre-implementation MUST fail with a compile or assertion error.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	adminTenantID = "01970000-0000-7000-8000-000000000001"
	adminGCID     = "01970000-0000-7000-9000-AAAAAAAAAAAA"
	learnerGCID1  = "01970000-0000-7000-9000-000000000001"
	learnerGCID2  = "01970000-0000-7000-9000-000000000002"
)

// reqAdminGET builds a GET with X-Tenant-Id + gcid + optional roles
// header. Empty roles == no header (learner default).
func reqAdminGET(t *testing.T, srv http.Handler, path, tenantID, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// seedAdminQueueApps creates a tenant-bound mix of applications across
// several states for the admin queue tests. Returns the seeded server
// + map of state → application id for assertion lookups.
func seedAdminQueueApps(t *testing.T) (http.Handler, map[application.Status]string) {
	t.Helper()
	srv, seeded := newAppTestServer(t)

	idByStatus := make(map[application.Status]string)
	ctx := context.Background()

	// Submitted (learner 1)
	apS, _, err := seeded.applications.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: adminTenantID,
		CourseID: courseID,
		GCID:     learnerGCID1,
	})
	if err != nil {
		t.Fatalf("seed submitted: %v", err)
	}
	idByStatus[application.StatusSubmitted] = apS.ID

	// Under review (learner 2): submitted → under_review.
	apUR, _, err := seeded.applications.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: adminTenantID,
		CourseID: courseID,
		GCID:     learnerGCID2,
	})
	if err != nil {
		t.Fatalf("seed under-review: %v", err)
	}
	if err := apUR.Transition(application.StatusUnderReview); err != nil {
		t.Fatalf("transition under_review: %v", err)
	}
	_ = seeded.applications.Save(ctx, apUR)
	idByStatus[application.StatusUnderReview] = apUR.ID

	return srv, idByStatus
}

// -----------------------------------------------------------------------------
// 400 tenantRequired — X-Tenant-Id missing
// -----------------------------------------------------------------------------

func TestAdminApplications_List_MissingTenant_400(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications", "", adminGCID, "training-admin")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (tenantRequired); body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 401 gcid header missing
// -----------------------------------------------------------------------------

func TestAdminApplications_List_MissingGCID_401(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, "", "training-admin")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 403 caller lacks training-admin role
// -----------------------------------------------------------------------------

func TestAdminApplications_List_NonAdmin_403(t *testing.T) {
	cases := []struct {
		name  string
		roles string
	}{
		{"no_roles", ""},
		{"learner_only", "learner"},
		{"instructor_only", "instructor"},
		{"auditor_only", "auditor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := seedAdminQueueApps(t)
			w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, adminGCID, c.roles)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (admin gate); body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// -----------------------------------------------------------------------------
// 200 training-admin role — full list (no state filter)
// -----------------------------------------------------------------------------

func TestAdminApplications_List_TrainingAdminRole_ReturnsAll(t *testing.T) {
	srv, idByStatus := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want 2 (seed); body=%s", len(resp.Items), w.Body.String())
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2", resp.Total)
	}
	// Every row must carry the seeded tenant_id (cross-tenant guard).
	for _, item := range resp.Items {
		if item["tenant_id"] != adminTenantID {
			t.Fatalf("cross-tenant leak: row tenant_id=%v want %s", item["tenant_id"], adminTenantID)
		}
	}
	// Spot-check: both seeded ids surface.
	found := map[string]bool{}
	for _, item := range resp.Items {
		if id, ok := item["id"].(string); ok {
			found[id] = true
		}
	}
	if !found[idByStatus[application.StatusSubmitted]] || !found[idByStatus[application.StatusUnderReview]] {
		t.Fatalf("missing seeded application(s); got=%v want both", found)
	}
}

// -----------------------------------------------------------------------------
// 200 admin role accepted as superset
// -----------------------------------------------------------------------------

func TestAdminApplications_List_AdminRole_AlsoAccepted(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications", adminTenantID, adminGCID, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (admin is superset of training-admin); body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 list with ?state=SUBMITTED filter (upper-snake wire form)
// -----------------------------------------------------------------------------

func TestAdminApplications_List_FilterState_Submitted(t *testing.T) {
	srv, idByStatus := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications?state=SUBMITTED",
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d, want 1 (only submitted seed should match); body=%s",
			len(resp.Items), w.Body.String())
	}
	if got := resp.Items[0]["id"]; got != idByStatus[application.StatusSubmitted] {
		t.Fatalf("id = %v, want submitted seed %s", got, idByStatus[application.StatusSubmitted])
	}
	if got := resp.Items[0]["status"]; got != string(application.StatusSubmitted) {
		t.Fatalf("status = %v, want %q", got, application.StatusSubmitted)
	}
}

// -----------------------------------------------------------------------------
// 200 list with ?state=IN_REVIEW filter — wire-form alias for under_review
// -----------------------------------------------------------------------------

func TestAdminApplications_List_FilterState_InReview_AliasUnderReview(t *testing.T) {
	srv, idByStatus := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications?state=IN_REVIEW",
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d, want 1 (under_review seed); body=%s",
			len(resp.Items), w.Body.String())
	}
	if got := resp.Items[0]["id"]; got != idByStatus[application.StatusUnderReview] {
		t.Fatalf("id = %v, want under_review seed %s", got, idByStatus[application.StatusUnderReview])
	}
}

// -----------------------------------------------------------------------------
// 200 list with ?state=ENROLLED — empty (no enrolled seed)
// -----------------------------------------------------------------------------

func TestAdminApplications_List_FilterState_Enrolled_Empty(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications?state=ENROLLED",
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items = %d, want 0 (no enrolled seed)", len(resp.Items))
	}
	if resp.Total != 0 {
		t.Fatalf("total = %d, want 0", resp.Total)
	}
}

// -----------------------------------------------------------------------------
// 400 invalid ?state= value
// -----------------------------------------------------------------------------

func TestAdminApplications_List_FilterState_Invalid_400(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications?state=NOT_A_STATE",
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown state); body=%s",
			w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 405 non-GET method
// -----------------------------------------------------------------------------

func TestAdminApplications_List_NonGET_405(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/applications", nil)
	req.Header.Set("X-Tenant-Id", adminTenantID)
	req.Header.Set("gcid", adminGCID)
	req.Header.Set("x-mesh-user-roles", "training-admin")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 detail — admin can read any application's detail (no owner-gate)
// -----------------------------------------------------------------------------

func TestAdminApplications_Detail_TrainingAdmin_ReturnsAnyApplication(t *testing.T) {
	srv, idByStatus := seedAdminQueueApps(t)
	// Admin (adminGCID) reads learner 1's submitted application.
	target := idByStatus[application.StatusSubmitted]
	w := reqAdminGET(t, srv, "/api/v1/applications/"+target,
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp["id"] != target {
		t.Fatalf("id = %v, want %s", resp["id"], target)
	}
	// History must surface — the admin detail uses the existing
	// applicationDetailDTO shape (id + status + history + ...).
	if _, ok := resp["history"]; !ok {
		t.Fatalf("missing history in detail response; body=%s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 404 detail — id does not exist in this tenant
// -----------------------------------------------------------------------------

func TestAdminApplications_Detail_NotFound_404(t *testing.T) {
	srv, _ := seedAdminQueueApps(t)
	w := reqAdminGET(t, srv, "/api/v1/applications/01970000-0000-7000-8000-DEADBEEFDEAD",
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 403 detail — caller lacks the admin role (gate must apply on detail too)
// -----------------------------------------------------------------------------

func TestAdminApplications_Detail_NonAdmin_403(t *testing.T) {
	srv, idByStatus := seedAdminQueueApps(t)
	target := idByStatus[application.StatusSubmitted]
	w := reqAdminGET(t, srv, "/api/v1/applications/"+target,
		adminTenantID, adminGCID, "learner")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 500 diagnosability (CHO-2337): the repo error MUST be logged, never
// swallowed. The reported symptom was an intermittent 500 on
// GET /api/v1/applications?state=SUBMITTED that recorded NOTHING about why.
// -----------------------------------------------------------------------------

// failingAppRepo satisfies application.ApplicationPort and fails every read
// with a fixed cause, so the admin handler's 500 branches can be exercised
// without a live Postgres. The cause string mimics the kind of transient
// connection error the pg adapter surfaces from RunInTx / ApplySession.
type failingAppRepo struct{ err error }

func (f failingAppRepo) SubmitOrGet(context.Context, application.SubmitInput) (*application.Application, bool, error) {
	return nil, false, f.err
}
func (f failingAppRepo) Get(context.Context, string, string) (*application.Application, bool, error) {
	return nil, false, f.err
}
func (f failingAppRepo) ListByGCID(context.Context, string, string, int, int) ([]*application.Application, int, error) {
	return nil, 0, f.err
}
func (f failingAppRepo) ListByTenant(context.Context, application.ListByTenantInput) ([]*application.Application, int, error) {
	return nil, 0, f.err
}
func (f failingAppRepo) Save(context.Context, *application.Application) error { return f.err }

// captureServerLog redirects the package logger into a buffer for the life of
// the test, restoring the prior writer afterwards. The service logs via the
// standard log package (observability.LogRequest + handler error logs), so the
// buffer receives both the request line and any handler-emitted cause.
func captureServerLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

func TestAdminApplications_List_RepoError_LogsCause_500(t *testing.T) {
	logbuf := captureServerLog(t)

	cause := errors.New("pg: count applications by tenant: acquire connection: conn closed")
	srv := httpapi.NewServer(httpapi.Deps{Applications: failingAppRepo{err: cause}})

	w := reqAdminGET(t, srv, "/api/v1/applications?state=SUBMITTED",
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
	logged := logbuf.String()
	// The underlying cause MUST be recorded (this is the whole point of the
	// fix: the request line already carries status=500 but never the why).
	if !strings.Contains(logged, "conn closed") {
		t.Fatalf("500 cause was swallowed, not logged; log=%q", logged)
	}
	// Triage context: tenant + path must accompany the cause.
	if !strings.Contains(logged, adminTenantID) {
		t.Fatalf("tenant missing from 500 log; log=%q", logged)
	}
	if !strings.Contains(logged, "/api/v1/applications") {
		t.Fatalf("path missing from 500 log; log=%q", logged)
	}
}

func TestAdminApplications_Detail_RepoError_LogsCause_500(t *testing.T) {
	logbuf := captureServerLog(t)

	cause := errors.New("pg: apply rls session: SET LOCAL chora.tenant_id failed: conn busy")
	srv := httpapi.NewServer(httpapi.Deps{Applications: failingAppRepo{err: cause}})

	target := "01970000-0000-7000-8000-DEADBEEFDEAD"
	w := reqAdminGET(t, srv, "/api/v1/applications/"+target,
		adminTenantID, adminGCID, "training-admin")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
	logged := logbuf.String()
	if !strings.Contains(logged, "conn busy") {
		t.Fatalf("detail 500 cause was swallowed, not logged; log=%q", logged)
	}
	if !strings.Contains(logged, target) {
		t.Fatalf("application_id missing from detail 500 log; log=%q", logged)
	}
}
