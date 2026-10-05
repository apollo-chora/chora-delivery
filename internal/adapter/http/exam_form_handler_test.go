// exam_form_handler_test.go — TDD for the W4 Brick-1 ExamForm + ExamResult
// HTTP surface.
//
// Endpoints under test (nested under the gateway-proxied /api/v1/exams/ subtree
// via Go 1.22 method+wildcard patterns, which coexist with the existing
// /api/v1/exams/ handler):
//
//	POST /api/v1/exams/{examID}/forms                       → create ASSEMBLED form
//	GET  /api/v1/exams/{examID}/forms                       → list (tenant+exam scoped)
//	POST /api/v1/exams/{examID}/forms/{formID}/expose       → ASSEMBLED → EXPOSED
//	POST /api/v1/exams/{examID}/forms/{formID}/retire       → EXPOSED → RETIRED
//	POST /api/v1/exams/{examID}/results                     → record result → PASS/FAIL
//
// Reuses doExam + the examTest* consts from exam_handler_test.go (same
// package). The routes are registered on a private mux (RegisterExamFormRoutes),
// so this suite is self-contained and never depends on Deps wiring.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
)

const (
	formHandlerExamID = "019e2f93-d586-71b5-8c3d-e2b0d0d5e777"
	formHandlerBankID = "019e2f93-d586-71b5-8c3d-e2b0d0d5b777"
	formHandlerCand   = "019e2f93-d586-71b5-8c3d-e2b0d0d5c777"
)

func newFormServer() (http.Handler, *inmem.ExamFormRepo, *inmem.ExamResultRepo) {
	srv, forms, results, _ := newFormServerWithOutbox()
	return srv, forms, results
}

// newFormServerWithOutbox wires the ExamForm surface with the real outcome-event
// producer stack (InMemoryPublisher → TransactionalOutboxPublisher →
// ExamResultPublisher) so the result-finalize path exercises the outbox tee.
// The returned store lets a test assert the enqueued ExamResultReleased row.
func newFormServerWithOutbox() (http.Handler, *inmem.ExamFormRepo, *inmem.ExamResultRepo, *outbox.InMemoryStore) {
	forms := inmem.NewExamFormRepo()
	results := inmem.NewExamResultRepo()
	store := outbox.NewInMemoryStore()
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	txp := outbox.NewTransactionalPublisher(outbox.PublisherConfig{Inner: inner, Store: store})
	mux := http.NewServeMux()
	httpapi.RegisterExamFormRoutes(mux, httpapi.ExamFormDeps{
		Forms:   forms,
		Results: results,
		Events:  events.NewExamResultPublisher(txp),
	})
	return mux, forms, results, store
}

func formsPath() string { return "/api/v1/exams/" + formHandlerExamID + "/forms" }

// createFormBody returns a valid create-form body: 3 revision-pinned items +
// a RAW cut (max 100, pass mark 60).
func createFormBody() string {
	return `{
		"item_bank_id": "` + formHandlerBankID + `",
		"items": [
			{"item_id":"q1","atom_revision_id":"r1","position":0},
			{"item_id":"q2","atom_revision_id":"r2","position":1},
			{"item_id":"q3","atom_revision_id":"r3","position":2}
		],
		"cut_score": {"mode":"RAW","max_score":100,"raw_mark":60}
	}`
}

// seedExposedForm creates then exposes a form and returns its id.
func seedExposedForm(t *testing.T, srv http.Handler) string {
	t.Helper()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed create: %d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)
	exp := doExam(t, srv, "POST", formsPath()+"/"+id+"/expose", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if exp.Code != http.StatusOK {
		t.Fatalf("seed expose: %d body=%s", exp.Code, exp.Body.String())
	}
	return id
}

// -----------------------------------------------------------------------------
// POST /forms — create
// -----------------------------------------------------------------------------

func TestForms_Create_AsAdmin_201_Assembled(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "ASSEMBLED" {
		t.Errorf("state=%v want ASSEMBLED", got["state"])
	}
	if got["exam_id"] != formHandlerExamID {
		t.Errorf("exam_id=%v", got["exam_id"])
	}
	items, _ := got["items"].([]interface{})
	if len(items) != 3 {
		t.Errorf("items=%d want 3", len(items))
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Error("id missing")
	}
}

func TestForms_Create_NoTenant_400(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), "", examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestForms_Create_NoGCID_401(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, "", "training-admin")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestForms_Create_AsLearner_403(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestForms_Create_EmptyItems_400(t *testing.T) {
	srv, _, _ := newFormServer()
	body := `{"item_bank_id":"` + formHandlerBankID + `","items":[],"cut_score":{"mode":"RAW","max_score":100,"raw_mark":60}}`
	rec := doExam(t, srv, "POST", formsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (cannot assemble empty) body=%s", rec.Code, rec.Body.String())
	}
}

func TestForms_Create_BadCut_400(t *testing.T) {
	srv, _, _ := newFormServer()
	// raw_mark 150 > max_score 100 violates 0 <= cut <= max.
	body := `{"item_bank_id":"` + formHandlerBankID + `","items":[{"item_id":"q1","atom_revision_id":"r1","position":0}],"cut_score":{"mode":"RAW","max_score":100,"raw_mark":150}}`
	rec := doExam(t, srv, "POST", formsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestForms_Create_MissingRevisionPin_400(t *testing.T) {
	srv, _, _ := newFormServer()
	body := `{"item_bank_id":"` + formHandlerBankID + `","items":[{"item_id":"q1","atom_revision_id":"","position":0}],"cut_score":{"mode":"RAW","max_score":100,"raw_mark":60}}`
	rec := doExam(t, srv, "POST", formsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (revision-pin required) body=%s", rec.Code, rec.Body.String())
	}
}

func TestForms_Create_BadJSON_400(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), `{nope`, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /forms — list
// -----------------------------------------------------------------------------

func TestForms_List_Empty_200(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "GET", formsPath(), "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, ok := got["items"].([]interface{})
	if !ok || len(items) != 0 {
		t.Errorf("items=%v want []", got["items"])
	}
}

func TestForms_List_ReturnsCreated(t *testing.T) {
	srv, _, _ := newFormServer()
	_ = doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	_ = doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	rec := doExam(t, srv, "GET", formsPath(), "", examTestTenantID, examTestAdminGCID, "training-admin")
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("items=%d want 2 body=%s", len(items), rec.Body.String())
	}
}

func TestForms_List_ScopesByTenant(t *testing.T) {
	srv, _, _ := newFormServer()
	_ = doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	rec := doExam(t, srv, "GET", formsPath(), "", examTestOtherTenant, examTestAdminGCID, "training-admin")
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("cross-tenant leak: items=%d want 0", len(items))
	}
}

// -----------------------------------------------------------------------------
// expose / retire transitions
// -----------------------------------------------------------------------------

func TestForms_Expose_HappyThenDouble_409(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	exp := doExam(t, srv, "POST", formsPath()+"/"+id+"/expose", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if exp.Code != http.StatusOK {
		t.Fatalf("expose status=%d want 200 body=%s", exp.Code, exp.Body.String())
	}
	var exposed map[string]interface{}
	_ = json.Unmarshal(exp.Body.Bytes(), &exposed)
	if exposed["state"] != "EXPOSED" {
		t.Errorf("state=%v want EXPOSED", exposed["state"])
	}
	// second expose is a state conflict
	exp2 := doExam(t, srv, "POST", formsPath()+"/"+id+"/expose", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if exp2.Code != http.StatusConflict {
		t.Errorf("double expose status=%d want 409", exp2.Code)
	}
}

func TestForms_Expose_NotFound_404(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath()+"/does-not-exist/expose", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestForms_Expose_AsLearner_403(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath()+"/whatever/expose", "", examTestTenantID, examTestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestForms_Retire_AfterExpose_200(t *testing.T) {
	srv, _, _ := newFormServer()
	id := seedExposedForm(t, srv)
	rec := doExam(t, srv, "POST", formsPath()+"/"+id+"/retire", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("retire status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "RETIRED" {
		t.Errorf("state=%v want RETIRED", got["state"])
	}
}

func TestForms_Retire_NotExposed_409(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)
	// retiring an ASSEMBLED (never-exposed) form is a conflict
	ret := doExam(t, srv, "POST", formsPath()+"/"+id+"/retire", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if ret.Code != http.StatusConflict {
		t.Errorf("status=%d want 409", ret.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /results — the cut-score → PASS/FAIL path
// -----------------------------------------------------------------------------

func resultsPath() string { return "/api/v1/exams/" + formHandlerExamID + "/results" }

func TestResults_Pass_201(t *testing.T) {
	srv, _, results := newFormServer()
	formID := seedExposedForm(t, srv)
	body := `{"form_id":"` + formID + `","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["outcome"] != "PASS" {
		t.Errorf("outcome=%v want PASS (raw==cut)", got["outcome"])
	}
	if got["max_score"].(float64) != 100 || got["raw_score"].(float64) != 60 {
		t.Errorf("scores wrong: %v", got)
	}
	// durable row persisted (source of truth)
	if list, _ := results.ListByForm(nil, examTestTenantID, formID); len(list) != 1 {
		t.Errorf("expected 1 durable result row; got %d", len(list))
	}
}

// TestResults_Create_TeesOutcomeEvent asserts the finalize path enqueues the
// ExamResultReleased outcome event into the outbox (ADR-190 D1 seam) — exactly
// one row on the canonical topic, keyed by the exam_result aggregate.
func TestResults_Create_TeesOutcomeEvent(t *testing.T) {
	srv, _, _, store := newFormServerWithOutbox()
	formID := seedExposedForm(t, srv)
	body := `{"form_id":"` + formID + `","candidate_ref":"` + formHandlerCand + `","raw_score":72}`
	rec := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	rows, err := store.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 outcome-event outbox row, got %d", len(rows))
	}
	if rows[0].Topic != "chora.delivery.exam_result.released.v1" {
		t.Errorf("topic = %q", rows[0].Topic)
	}
	if rows[0].AggregateType != "exam_result" {
		t.Errorf("aggregate_type = %q want exam_result", rows[0].AggregateType)
	}
	if rows[0].TenantID != examTestTenantID {
		t.Errorf("tenant_id = %q want %q", rows[0].TenantID, examTestTenantID)
	}
}

func TestResults_Fail_201(t *testing.T) {
	srv, _, _ := newFormServer()
	formID := seedExposedForm(t, srv)
	body := `{"form_id":"` + formID + `","candidate_ref":"` + formHandlerCand + `","raw_score":59}`
	rec := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["outcome"] != "FAIL" {
		t.Errorf("outcome=%v want FAIL (raw<cut)", got["outcome"])
	}
}

// CHO-2104 — the R+ Results roster (GET list by exam).
func TestResults_List_200(t *testing.T) {
	srv, _, _ := newFormServer()
	formID := seedExposedForm(t, srv)
	for _, cand := range []string{formHandlerCand, "00000000-0000-7000-8000-0000000c0002"} {
		body := `{"form_id":"` + formID + `","candidate_ref":"` + cand + `","raw_score":60}`
		if rec := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin"); rec.Code != http.StatusCreated {
			t.Fatalf("seed result: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	rec := doExam(t, srv, "GET", resultsPath(), "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string][]map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got["items"]) != 2 {
		t.Fatalf("items=%d want 2", len(got["items"]))
	}
	// The roster DTO omits pass_mark (a grading-event value, not persisted).
	if _, hasPass := got["items"][0]["pass_mark"]; hasPass {
		t.Errorf("list DTO must NOT carry pass_mark: %v", got["items"][0])
	}
	if got["items"][0]["outcome"] != "PASS" {
		t.Errorf("outcome=%v want PASS", got["items"][0]["outcome"])
	}
}

func TestResults_List_Empty_200(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "GET", resultsPath(), "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string][]map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got["items"]) != 0 {
		t.Errorf("want items:[], got %v", got["items"])
	}
}

func TestResults_List_AsLearner_403(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "GET", resultsPath(), "", examTestTenantID, examTestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 (grades are admin-only)", rec.Code)
	}
}

func TestResults_AgainstUnexposedForm_409(t *testing.T) {
	srv, _, _ := newFormServer()
	rec := doExam(t, srv, "POST", formsPath(), createFormBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	formID := created["id"].(string) // ASSEMBLED, not exposed
	body := `{"form_id":"` + formID + `","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec2 := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec2.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 (form not exposed)", rec2.Code)
	}
}

func TestResults_RawOutOfRange_400(t *testing.T) {
	srv, _, _ := newFormServer()
	formID := seedExposedForm(t, srv)
	body := `{"form_id":"` + formID + `","candidate_ref":"` + formHandlerCand + `","raw_score":101}`
	rec := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (raw>max)", rec.Code)
	}
}

func TestResults_FormNotFound_404(t *testing.T) {
	srv, _, _ := newFormServer()
	body := `{"form_id":"nope","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestResults_AsLearner_403(t *testing.T) {
	srv, _, _ := newFormServer()
	body := `{"form_id":"x","candidate_ref":"` + formHandlerCand + `","raw_score":60}`
	rec := doExam(t, srv, "POST", resultsPath(), body, examTestTenantID, examTestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}
