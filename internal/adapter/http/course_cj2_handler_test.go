// course_cj2_handler_test.go — table-driven tests for the CJ#2 Course
// HTTP surface per chora-contracts/openapi/delivery-courses.yaml.
//
// Coverage target: ≥60% adapter (per .claude/rules/development-execution.md).
//
// TDD: written FIRST then drove the handler in course_cj2_handler.go.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	cj2TestTenantID         = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	cj2TestAuthorGCID       = "019e2f93-d586-71b5-8c3d-e2b0d0d50101"
	cj2TestAdminGCID        = "019e2f93-d586-71b5-8c3d-e2b0d0d50102"
	cj2TestLearnerGCID      = "019e2f93-d586-71b5-8c3d-e2b0d0d50103"
	cj2TestSecondAuthorGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50104"
	cj2TestTestSetID        = "019e2f93-d586-71b5-8c3d-e2b0d0d50208"
)

// newCJ2Server wires the CJ#2 routes against fresh in-mem adapters per test.
func newCJ2Server() (http.Handler, *events.InMemoryPublisher, domain.CourseCJ2Port) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	store := domain.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		CourseCJ2: &httpapi.CourseCJ2Deps{
			Courses:         store,
			OutboxPublisher: pub,
		},
	})
	return srv, pub, store
}

// doCJ2 issues a request with the standard CJ#2 headers.
func doCJ2(t *testing.T, h http.Handler, method, path, body, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("X-Tenant-Id", cj2TestTenantID)
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

// -----------------------------------------------------------------------------
// POST /api/v1/courses (create DRAFT)
// -----------------------------------------------------------------------------

func TestCJ2_PostCourse_AsInstructor_201(t *testing.T) {
	srv, _, _ := newCJ2Server()
	body := `{
		"title": "Algorithmic Thinking 101",
		"description": "intro",
		"learning_objectives": ["LO1"],
		"prerequisites": [],
		"test_set_ids": ["` + cj2TestTestSetID + `"]
	}`
	rec := doCJ2(t, srv, "POST", "/api/v1/courses", body, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT", got["state"])
	}
	if got["author_gcid"] != cj2TestAuthorGCID {
		t.Errorf("author_gcid=%v want %s", got["author_gcid"], cj2TestAuthorGCID)
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
}

func TestCJ2_PostCourse_NoTenantHeader_400(t *testing.T) {
	srv, _, _ := newCJ2Server()
	req := httptest.NewRequest("POST", "/api/v1/courses", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("gcid", cj2TestAuthorGCID)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCJ2_PostCourse_NoGCIDHeader_401(t *testing.T) {
	srv, _, _ := newCJ2Server()
	rec := doCJ2(t, srv, "POST", "/api/v1/courses", `{"title":"T","test_set_ids":["a"]}`, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestCJ2_PostCourse_AsLearner_403(t *testing.T) {
	srv, _, _ := newCJ2Server()
	rec := doCJ2(t, srv, "POST", "/api/v1/courses",
		`{"title":"T","test_set_ids":["`+cj2TestTestSetID+`"]}`,
		cj2TestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/courses/{id}/publish
// -----------------------------------------------------------------------------

func TestCJ2_Publish_DraftToAwaitingReview(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	rec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "AWAITING_REVIEW" {
		t.Errorf("state=%v want AWAITING_REVIEW", got["state"])
	}
}

func TestCJ2_Publish_RejectedAsLearner_403(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	rec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/courses/{id}/release
// -----------------------------------------------------------------------------

func TestCJ2_Release_AsTrainingAdmin_PublishedAndEvent(t *testing.T) {
	srv, pub, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	// Move to AWAITING_REVIEW.
	publishRec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	if publishRec.Code != http.StatusOK {
		t.Fatalf("publish failed: %d", publishRec.Code)
	}
	// Release as training-admin.
	body := `{
		"price_sgd_cents": 99900,
		"sf_eligible": true,
		"instructor_gcids": ["` + cj2TestAuthorGCID + `"]
	}`
	rec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/release", body, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("release status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "PUBLISHED" {
		t.Errorf("state=%v want PUBLISHED", got["state"])
	}
	if int64(got["price_sgd_cents"].(float64)) != 99900 {
		t.Errorf("price_sgd_cents=%v want 99900", got["price_sgd_cents"])
	}
	if got["sf_eligible"] != true {
		t.Errorf("sf_eligible=%v want true", got["sf_eligible"])
	}
	if got["published_at"] == nil {
		t.Errorf("published_at missing")
	}
	// Event published?
	found := false
	for _, evt := range pub.History() {
		if evt.Topic == "chora.delivery.course.released.v1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("chora.delivery.course.released.v1 event not published; history=%d", len(pub.History()))
	}
}

func TestCJ2_Release_AsLearner_403(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	_ = doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	rec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/release",
		`{"price_sgd_cents":100,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`,
		cj2TestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestCJ2_Release_NotAwaitingReview_409(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv) // DRAFT
	rec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/release",
		`{"price_sgd_cents":100,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`,
		cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/courses/{id}/reject
// -----------------------------------------------------------------------------

func TestCJ2_Reject_AwaitingReviewBackToDraft(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	_ = doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	rec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/reject",
		`{"review_notes":"please add more LOs"}`,
		cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT after reject", got["state"])
	}
	if got["review_notes"] != "please add more LOs" {
		t.Errorf("review_notes=%v", got["review_notes"])
	}
}

func TestCJ2_Reject_AsLearner_403(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	rec := doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/reject",
		`{"review_notes":"x"}`, cj2TestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/courses (list)
// -----------------------------------------------------------------------------

func TestCJ2_ListAwaitingReview_RBAC(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	_ = doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")

	// Learner — 403.
	rec := doCJ2(t, srv, "GET", "/api/v1/courses?state=AWAITING_REVIEW", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("learner list status=%d want 403", rec.Code)
	}
	// Training-admin — 200 with the course.
	rec = doCJ2(t, srv, "GET", "/api/v1/courses?state=AWAITING_REVIEW", "", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 1 {
		t.Errorf("items=%d want 1", len(items))
	}
}

func TestCJ2_ListPublished_AnyCaller(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	_ = doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	_ = doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/release",
		`{"price_sgd_cents":0,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`,
		cj2TestAdminGCID, "training-admin")
	rec := doCJ2(t, srv, "GET", "/api/v1/courses?state=PUBLISHED", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
}

// seedCJ2Draft inserts a DRAFT course authored by authorGCID straight into the
// store (bypassing the create handler's role gate) so the list-authorship
// tests can control author_gcid precisely.
func seedCJ2Draft(t *testing.T, store domain.CourseCJ2Port, authorGCID, title string) *domain.Course {
	t.Helper()
	c, err := domain.NewCJ2Course(domain.NewCJ2CourseInput{
		TenantID:           cj2TestTenantID,
		AuthorGCID:         authorGCID,
		Title:              title,
		LearningObjectives: []string{"LO1"},
		TestSetIDs:         []string{cj2TestTestSetID},
	})
	if err != nil {
		t.Fatalf("seed NewCJ2Course: %v", err)
	}
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	return c
}

// listItems decodes the {"items":[...]} envelope.
func listItems(t *testing.T, rec *httptest.ResponseRecorder) []interface{} {
	t.Helper()
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list body: %v (%s)", err, rec.Body.String())
	}
	items, _ := got["items"].([]interface{})
	return items
}

// ONBOARD-UI F1 — the training-admin course list must include the caller's OWN
// authored courses regardless of lifecycle state. An ADR-182 `author` (A+
// Creator) holds NO training-admin role, so the previous gate 403'd them and
// they could never list their own DRAFT / AWAITING_REVIEW courses.
//
// (a) author SEES their own DRAFT; (b) a different author does NOT see it.
func TestCJ2_ListDraft_AuthorSeesOnlyOwn(t *testing.T) {
	srv, _, store := newCJ2Server()
	courseA := seedCJ2Draft(t, store, cj2TestAuthorGCID, "Author A draft")
	courseB := seedCJ2Draft(t, store, cj2TestSecondAuthorGCID, "Author B draft")

	// (a) Author A (author role) lists DRAFT → sees ONLY their own course.
	rec := doCJ2(t, srv, "GET", "/api/v1/courses?state=DRAFT", "", cj2TestAuthorGCID, "author")
	if rec.Code != http.StatusOK {
		t.Fatalf("author A list status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	items := listItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("author A should see exactly 1 (own) DRAFT, got %d", len(items))
	}
	if got := items[0].(map[string]interface{})["id"]; got != courseA.ID {
		t.Errorf("author A saw course id=%v want own %s", got, courseA.ID)
	}

	// (b) Author B (author role) sees ONLY their own — never author A's draft.
	recB := doCJ2(t, srv, "GET", "/api/v1/courses?state=DRAFT", "", cj2TestSecondAuthorGCID, "author")
	if recB.Code != http.StatusOK {
		t.Fatalf("author B list status=%d want 200 body=%s", recB.Code, recB.Body.String())
	}
	itemsB := listItems(t, recB)
	if len(itemsB) != 1 {
		t.Fatalf("author B should see exactly 1 (own) DRAFT, got %d", len(itemsB))
	}
	if got := itemsB[0].(map[string]interface{})["id"]; got != courseB.ID {
		t.Errorf("author B saw course id=%v want own %s (author A's draft leaked!)", got, courseB.ID)
	}
}

// Training-admin still sees the FULL review queue (both authors' drafts);
// a pure learner (no role) is still 403 for a non-PUBLISHED state.
func TestCJ2_ListDraft_AdminSeesAll_LearnerForbidden(t *testing.T) {
	srv, _, store := newCJ2Server()
	_ = seedCJ2Draft(t, store, cj2TestAuthorGCID, "Author A draft")
	_ = seedCJ2Draft(t, store, cj2TestSecondAuthorGCID, "Author B draft")

	// Training-admin → sees ALL drafts in the tenant.
	rec := doCJ2(t, srv, "GET", "/api/v1/courses?state=DRAFT", "", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if items := listItems(t, rec); len(items) != 2 {
		t.Fatalf("training-admin should see all 2 drafts, got %d", len(items))
	}

	// Learner (no role) → 403 for non-PUBLISHED.
	recL := doCJ2(t, srv, "GET", "/api/v1/courses?state=DRAFT", "", cj2TestLearnerGCID, "")
	if recL.Code != http.StatusForbidden {
		t.Errorf("learner list status=%d want 403", recL.Code)
	}
}

// TestCJ2_ListDraft_FiltersByQuery covers the entity-picker search: GET
// /api/v1/courses?state=<state>&q=<term> returns only courses whose title
// contains the term (case-insensitive substring). Backs the R+ prerequisite
// -editor course picker (kills the single-page course dropdown) — mirrors
// chora-creation's atom ?q= pattern (commit 85b2bae14). `state` stays
// required; `q` is additive.
func TestCJ2_ListDraft_FiltersByQuery(t *testing.T) {
	srv, _, store := newCJ2Server()
	_ = seedCJ2Draft(t, store, cj2TestAuthorGCID, "Road Safety Basics")
	_ = seedCJ2Draft(t, store, cj2TestAuthorGCID, "Photosynthesis 101")

	// q=road → only the Road course (case-insensitive; "road" ⊂ "Road Safety").
	rec := doCJ2(t, srv, "GET", "/api/v1/courses?state=DRAFT&q=road", "", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	items := listItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("q=road expected exactly 1 course; got %d (%v)", len(items), items)
	}
	if got := items[0].(map[string]interface{})["title"]; got != "Road Safety Basics" {
		t.Errorf("q=road returned wrong course: %v", got)
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/v1/courses/{id}
// -----------------------------------------------------------------------------

func TestCJ2_Patch_DraftOK(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	rec := doCJ2(t, srv, "PATCH", "/api/v1/courses/"+id,
		`{"description":"new"}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["description"] != "new" {
		t.Errorf("description=%v", got["description"])
	}
}

func TestCJ2_Patch_NotDraft_409(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv)
	_ = doCJ2(t, srv, "POST", "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	rec := doCJ2(t, srv, "PATCH", "/api/v1/courses/"+id,
		`{"description":"x"}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/courses/{id}
// -----------------------------------------------------------------------------

func TestCJ2_Get_VisibilityRBAC(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv) // DRAFT
	// Random learner cannot see DRAFT.
	rec := doCJ2(t, srv, "GET", "/api/v1/courses/"+id, "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("learner get DRAFT status=%d want 404", rec.Code)
	}
	// Author can.
	rec = doCJ2(t, srv, "GET", "/api/v1/courses/"+id, "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Errorf("author get DRAFT status=%d want 200", rec.Code)
	}
	// Admin can.
	rec = doCJ2(t, srv, "GET", "/api/v1/courses/"+id, "", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Errorf("admin get DRAFT status=%d want 200", rec.Code)
	}
}

func TestCJ2_Get_NotFound_404(t *testing.T) {
	srv, _, _ := newCJ2Server()
	rec := doCJ2(t, srv, "GET", "/api/v1/courses/00000000-0000-0000-0000-000000000000",
		"", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

// TestCJ2_Get_TrainingAdminUnderscore_200 reproduces the CHO-2256 follow-up
// list-vs-detail projection mismatch at the handler edge. The R+ catalog list
// shows a training_admin every author's review-queue course (RLS grants the
// staff read), but GET /api/v1/courses/{id} 404'd those same rows because the
// caller carries the CANONICAL underscore role the gateway mint stamps
// ("instructor,training_admin") while VisibleToCaller matched only the legacy
// hyphen. List and detail must agree. cj2TestAdminGCID is NOT the author, so
// only the staff-role branch can grant visibility.
func TestCJ2_Get_TrainingAdminUnderscore_200(t *testing.T) {
	srv, _, _ := newCJ2Server()
	id := createCJ2Course(t, srv) // DRAFT authored by cj2TestAuthorGCID

	// Non-author reviewer with the EXACT live mint role shape (underscore).
	rec := doCJ2(t, srv, "GET", "/api/v1/courses/"+id, "",
		cj2TestAdminGCID, "instructor,training_admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("training_admin (underscore) get non-own DRAFT status=%d want 200 body=%s",
			rec.Code, rec.Body.String())
	}

	// Negative control: a bare instructor (not the author) still cannot see it —
	// the fix must not over-broaden past the RLS staff set.
	rec = doCJ2(t, srv, "GET", "/api/v1/courses/"+id, "",
		cj2TestAdminGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bare instructor (not author) get non-own DRAFT status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func createCJ2Course(t *testing.T, srv http.Handler) string {
	t.Helper()
	body := `{
		"title": "CJ2 Test Course",
		"description": "smoke",
		"learning_objectives": ["LO1"],
		"prerequisites": [],
		"test_set_ids": ["` + cj2TestTestSetID + `"]
	}`
	rec := doCJ2(t, srv, "POST", "/api/v1/courses", body, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup create: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("setup create: missing id")
	}
	return id
}
