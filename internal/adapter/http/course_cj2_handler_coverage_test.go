// course_cj2_handler_coverage_test.go — statement-coverage battery for
// internal/adapter/http/course_cj2_handler.go. Supplements
// course_cj2_handler_test.go with the remaining branches:
//
//   - root/sub method dispatch 405s + not-found branches (cj2CoursesRootHandler /
//     cj2CoursesSubHandler)
//   - create/update/publish/release/reject validation 400s + state 409s + the
//     500 error envelopes (reached through cj2FailStore, a test-local wrapper
//     over the in-mem adapter — the inmem adapter itself never errors)
//   - list pagination parse failures (422) + page/page_size happy branches +
//     empty results + ListByState/ListByStateAndAuthor errors
//   - cross-tenant 404 on GET
//   - hasAuthorRole / meshRolesForRLS edge branches (mixed-case, dirty tokens)
//   - missing-gcid 401 battery — a request with tenant but no gcid passes
//     tenantRequired, then callerTenantGCID 401s and each handler's
//     `if tenantID == "" { return }` early-return fires (the only reachable
//     path into those blocks; missing-tenant requests are rejected by the
//     middleware first)
//   - checkout: nil Payments 501, empty URL templates 501, tenant 400, 404s,
//     409s (not PUBLISHED / free), gRPC error 502 and happy 200 — the
//     payments.Client seam is satisfied in-memory via a test-local
//     PaymentServiceGRPCClient stub (no production changes)
//   - emitCourseReleased / publishCourseEvent: nil publisher no-op, publisher
//     without PublishCustom (drop branch), failing PublishCustom (log branch),
//     scheduled_open_at payload emission
package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/payments/v1"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/payments"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"google.golang.org/grpc"
)

const (
	// cj2OtherTenantID — a tenant that never owns a seeded course; drives the
	// cross-tenant 404 (the in-mem store is tenant-scoped by key).
	cj2OtherTenantID = "019e2f93-d586-71b5-8c3d-e2b0d0d59999"

	cj2SuccessURL = "https://chora.site/a/courses/{COURSE_ID}/enrolled?session_id={CHECKOUT_SESSION_ID}"
	cj2CancelURL  = "https://chora.site/a/courses/{COURSE_ID}?checkout_cancelled=1"
)

// -----------------------------------------------------------------------------
// Fixture helpers (cj2* prefix — file-local, no collisions with the shared
// helpers in course_cj2_handler_test.go: newCJ2Server / doCJ2 / createCJ2Course
// / seedCJ2Draft / listItems).
// -----------------------------------------------------------------------------

// cj2Server wires the CJ#2 routes over a fresh in-mem store; mut lets tests
// swap in a failing store, a different publisher or the payments client
// before NewServer is built. The returned store is the SAME instance the
// deps were seeded with (a mut that wraps it with cj2FailStore keeps sharing
// it), so tests can seed courses directly and still hit wrapper errors.
func cj2Server(mut func(*httpapi.CourseCJ2Deps, *domain.InMemCourseCJ2Store)) (http.Handler, *events.InMemoryPublisher, *domain.InMemCourseCJ2Store) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	store := domain.NewInMemCourseCJ2Store()
	deps := &httpapi.CourseCJ2Deps{
		Courses:         store,
		OutboxPublisher: pub,
	}
	if mut != nil {
		mut(deps, store)
	}
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		CourseCJ2:      deps,
	})
	return srv, pub, store
}

// cj2Req builds a raw CJ#2 request with caller-controlled tenant header —
// doCJ2 in course_cj2_handler_test.go hardcodes the tenant, which the
// cross-tenant + missing-tenant tests cannot use.
func cj2Req(method, path, body, tenant, gcid, roles string) *http.Request {
	// Always a real reader: httptest.NewRequest inspects body via a Len()
	// interface, and a typed-nil *strings.Reader would panic on it.
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Tenant-Id", tenant)
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func cj2Do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// cj2FailStore wraps InMemCourseCJ2Store and injects errors on demand so the
// handler's 500 error envelopes become reachable (the inmem adapter always
// returns nil). Reads/writes not gated by an error field delegate to inner.
type cj2FailStore struct {
	inner    *domain.InMemCourseCJ2Store
	saveErr  error
	getErr   error
	listErr  error
	listAErr error
}

func (s *cj2FailStore) Save(ctx context.Context, c *domain.Course) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.inner.Save(ctx, c)
}

func (s *cj2FailStore) Get(ctx context.Context, tenantID, courseID string) (*domain.Course, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	return s.inner.Get(ctx, tenantID, courseID)
}

func (s *cj2FailStore) ListByState(ctx context.Context, tenantID string, state domain.CourseState, query string, offset, limit int) ([]*domain.Course, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.inner.ListByState(ctx, tenantID, state, query, offset, limit)
}

func (s *cj2FailStore) ListByStateAndAuthor(ctx context.Context, tenantID string, state domain.CourseState, authorGCID, query string, offset, limit int) ([]*domain.Course, error) {
	if s.listAErr != nil {
		return nil, s.listAErr
	}
	return s.inner.ListByStateAndAuthor(ctx, tenantID, state, authorGCID, query, offset, limit)
}

// cj2PayStub fakes payments.PaymentServiceGRPCClient so the checkout handler's
// gRPC round-trip runs fully in-memory — the public NewClient seam, no test
// hooks in production code.
type cj2PayStub struct {
	resp *paymentsv1.CreateCourseCheckoutSessionResponse
	err  error
}

func (s *cj2PayStub) CreateCourseCheckoutSession(_ context.Context, _ *paymentsv1.CreateCourseCheckoutSessionRequest, _ ...grpc.CallOption) (*paymentsv1.CreateCourseCheckoutSessionResponse, error) {
	return s.resp, s.err
}

func (s *cj2PayStub) CreateApplicationCheckoutSession(_ context.Context, _ *paymentsv1.CreateApplicationCheckoutSessionRequest, _ ...grpc.CallOption) (*paymentsv1.CreateApplicationCheckoutSessionResponse, error) {
	return nil, nil
}

// cj2WireCheckout points the deps at a stub payments client + canonical URL
// templates, enabling the checkout surface past its 501 wiring guards.
func cj2WireCheckout(d *httpapi.CourseCJ2Deps, stub *cj2PayStub) {
	d.Payments = payments.NewClient(stub)
	d.CheckoutSuccessURLTemplate = cj2SuccessURL
	d.CheckoutCancelURLTemplate = cj2CancelURL
}

// cj2NoCustomPublisher is an events.Publisher WITHOUT PublishCustom — drives
// publishCourseEvent's type-assertion drop-branch. Local copy of the
// noCustomPublisher idea (assessment_handler_coverage_test.go) so this suite
// doesn't depend on that file's identifiers.
type cj2NoCustomPublisher struct {
	events.Publisher
}

// cj2FailPublisher implements PublishCustom but always errors — drives the
// log.Printf failure branch inside publishCourseEvent.
type cj2FailPublisher struct {
	events.Publisher
}

func (cj2FailPublisher) PublishCustom(string, string, string, map[string]any) (events.PublishedEvent, error) {
	return events.PublishedEvent{}, errors.New("publish failed")
}

// cj2SeedCourse builds + saves a course in the given state, bypassing the
// create handler's role gate. DRAFT is the constructor default.
func cj2SeedCourse(t *testing.T, store *domain.InMemCourseCJ2Store, authorGCID, title string, state domain.CourseState) *domain.Course {
	t.Helper()
	c, err := domain.NewCJ2Course(domain.NewCJ2CourseInput{
		TenantID:           cj2TestTenantID,
		AuthorGCID:         authorGCID,
		Title:              title,
		LearningObjectives: []string{"LO1"},
		TestSetIDs:         []string{cj2TestTestSetID},
	})
	if err != nil {
		t.Fatalf("cj2SeedCourse NewCJ2Course: %v", err)
	}
	if state != "" && state != domain.CourseStateDraft {
		c.State = state
	}
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatalf("cj2SeedCourse Save: %v", err)
	}
	return c
}

// cj2SeedPublished builds + saves a PUBLISHED course at the given price.
func cj2SeedPublished(t *testing.T, store *domain.InMemCourseCJ2Store, price int64) *domain.Course {
	t.Helper()
	c, err := domain.NewCJ2Course(domain.NewCJ2CourseInput{
		TenantID:           cj2TestTenantID,
		AuthorGCID:         cj2TestAuthorGCID,
		Title:              "Published CJ2",
		LearningObjectives: []string{"LO1"},
		TestSetIDs:         []string{cj2TestTestSetID},
	})
	if err != nil {
		t.Fatalf("cj2SeedPublished NewCJ2Course: %v", err)
	}
	c.State = domain.CourseStateAwaitingReview
	if err := c.Release(domain.ReleaseInput{
		PriceSGDCents:   price,
		InstructorGCIDs: []string{cj2TestAuthorGCID},
	}); err != nil {
		t.Fatalf("cj2SeedPublished Release: %v", err)
	}
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatalf("cj2SeedPublished Save: %v", err)
	}
	return c
}

// -----------------------------------------------------------------------------
// cj2CoursesRootHandler — non-GET/POST methods → 405.
// -----------------------------------------------------------------------------

func TestCj2_RootMethodNotAllowed_405(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := doCJ2(t, srv, m, "/api/v1/courses", "", cj2TestAuthorGCID, "instructor")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/v1/courses: status=%d want 405", m, rec.Code)
		}
	}
}

// -----------------------------------------------------------------------------
// cj2CoursesSubHandler — 404 + method-guard branches.
// -----------------------------------------------------------------------------

func TestCj2_SubDispatch_NotFoundBranches(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	cases := []struct {
		method, path string
	}{
		{"GET", "/api/v1/courses/"},                                   // empty id part
		{"POST", "/api/v1/courses/" + cj2TestTestSetID + "/bogus"},    // unknown action
		{"GET", "/api/v1/courses/" + cj2TestTestSetID + "/content/x"}, // >2 parts, Content nil
	}
	for _, tc := range cases {
		rec := doCJ2(t, srv, tc.method, tc.path, "", cj2TestAuthorGCID, "instructor")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status=%d want 404 body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestCj2_SubDispatch_ActionMethodNotAllowed_405(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	for _, action := range []string{"publish", "release", "reject", "checkout"} {
		rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses/"+cj2TestTestSetID+"/"+action, "", cj2TestAuthorGCID, "instructor")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET .../%s: status=%d want 405", action, rec.Code)
		}
	}
}

func TestCj2_SubDispatch_ItemMethodNotAllowed_405(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodDelete, "/api/v1/courses/"+cj2TestTestSetID, "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /{id}: status=%d want 405", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CourseCreate — validation 400s + save error envelope.
// -----------------------------------------------------------------------------

func TestCj2_Create_DecodeError_400(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses", "{not-json", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCj2_Create_TitleEmpty_400(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses",
		`{"test_set_ids":["`+cj2TestTestSetID+`"]}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Create_NoTestSets_400(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses",
		`{"title":"T","learning_objectives":["LO1"]}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Create_SaveError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, saveErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses",
		`{"title":"T","test_set_ids":["`+cj2TestTestSetID+`"]}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CourseList — state/pagination validation, 500 envelopes, RBAC edges.
// -----------------------------------------------------------------------------

func TestCj2_List_MissingState_400(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses", "", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCj2_List_InvalidState_400(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=NOPE", "", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCj2_List_PaginationErrors_422(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	for _, q := range []string{
		"page=abc", "page=0", "page=-3",
		"page_size=abc", "page_size=0", "page_size=101",
	} {
		path := "/api/v1/courses?state=PUBLISHED&" + q
		rec := doCJ2(t, srv, http.MethodGet, path, "", cj2TestLearnerGCID, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status=%d want 422 body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestCj2_List_Empty_200(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=PUBLISHED", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if items := listItems(t, rec); len(items) != 0 {
		t.Fatalf("expected an empty items list, got %d", len(items))
	}
}

func TestCj2_List_Paginated_200(t *testing.T) {
	srv, _, store := cj2Server(nil)
	_ = cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft one", domain.CourseStateDraft)
	_ = cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft two", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=DRAFT&page=2&page_size=1", "", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if items := listItems(t, rec); len(items) != 1 {
		t.Fatalf("page 2 with page_size 1 of 2 rows: want 1 item, got %d", len(items))
	}
}

func TestCj2_List_RepoError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, listErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=PUBLISHED", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_List_AuthorRepoError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, listAErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=DRAFT", "", cj2TestAuthorGCID, "author")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// TestCj2_List_LearnerRole_403 — a non-empty roles header that is neither
// training-admin nor author: drives hasAuthorRole's consume-and-deny branch
// (roles "" short-circuits earlier, roles "author" hits the true branch).
func TestCj2_List_LearnerRole_403(t *testing.T) {
	srv, _, store := cj2Server(nil)
	_ = cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=DRAFT", "", cj2TestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

// hasAuthorRole parses case-insensitively; meshRolesForRLS trims + drops
// non-[a-z0-9-_] tokens + empty tokens before joining.
func TestCj2_AuthorRole_CaseInsensitive_200(t *testing.T) {
	srv, _, store := cj2Server(nil)
	_ = cj2SeedCourse(t, store, cj2TestAuthorGCID, "Own draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=DRAFT", "", cj2TestAuthorGCID, "Author")
	if rec.Code != http.StatusOK {
		t.Fatalf("mixed-case author list: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if items := listItems(t, rec); len(items) != 1 {
		t.Fatalf("author should see exactly their own draft, got %d items", len(items))
	}
}

func TestCj2_MeshRoles_DirtyTokensSanitised_200(t *testing.T) {
	srv, _, store := cj2Server(nil)
	_ = cj2SeedCourse(t, store, cj2TestAuthorGCID, "Own draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses?state=DRAFT", "", cj2TestAuthorGCID, "author, B@d!role, ")
	if rec.Code != http.StatusOK {
		t.Fatalf("dirty roles list: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if items := listItems(t, rec); len(items) != 1 {
		t.Fatalf("author should see exactly their own draft, got %d items", len(items))
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CourseGet — repo error + cross-tenant scoping.
// -----------------------------------------------------------------------------

func TestCj2_Get_RepoError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, getErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses/"+cj2TestTestSetID, "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Get_CrossTenant_404(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Tenant-bound", domain.CourseStateDraft)
	req := cj2Req(http.MethodGet, "/api/v1/courses/"+c.ID, "", cj2OtherTenantID, cj2TestAuthorGCID, "training-admin")
	rec := cj2Do(srv, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get: status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CourseUpdate — 404 / RBAC / validation / error envelopes.
// -----------------------------------------------------------------------------

func TestCj2_Update_NotFound_404(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/00000000-0000-0000-0000-000000000000",
		`{"description":"x"}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestCj2_Update_NotAuthor_403(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/"+c.ID,
		`{"description":"x"}`, cj2TestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Update_DecodeError_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/"+c.ID, "{oops", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCj2_Update_EmptyTitle_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/"+c.ID,
		`{"title":""}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Update_SaveError_500(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, saveErr: errors.New("db down")}
	})
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/"+c.ID,
		`{"description":"x"}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Update_GetError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, getErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/"+cj2TestTestSetID,
		`{"description":"x"}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CoursePublish — 409 / 400 / 404 / error envelopes.
// -----------------------------------------------------------------------------

func TestCj2_Publish_NotDraft_409(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	id := createCJ2Course(t, srv)
	if rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("first publish: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("second publish: status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

// A course created without learning_objectives passes the constructor but
// fails Publish's objective gate → 400, not 409.
func TestCj2_Publish_MissingObjectives_400(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses",
		`{"title":"No LOs","test_set_ids":["`+cj2TestTestSetID+`"]}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	rec = doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("publish without LOs: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Publish_NotFound_404(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/00000000-0000-0000-0000-000000000000/publish", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestCj2_Publish_SaveError_500(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, saveErr: errors.New("db down")}
	})
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/publish", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Publish_GetError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, getErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/publish", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CourseRelease — 404 / 400s / 409 / error envelopes / emission.
// -----------------------------------------------------------------------------

func TestCj2_Release_NotFound_404(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/00000000-0000-0000-0000-000000000000/release",
		`{"price_sgd_cents":100,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestCj2_Release_DecodeError_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Awaiting", domain.CourseStateAwaitingReview)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/release", "{oops", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCj2_Release_NegativePrice_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Awaiting", domain.CourseStateAwaitingReview)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/release",
		`{"price_sgd_cents":-5,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Release_NoInstructors_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Awaiting", domain.CourseStateAwaitingReview)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/release",
		`{"price_sgd_cents":100}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Release_SaveError_500(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, saveErr: errors.New("db down")}
	})
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Awaiting", domain.CourseStateAwaitingReview)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/release",
		`{"price_sgd_cents":100,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Release_GetError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, getErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/release",
		`{"price_sgd_cents":100,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// cj2ReleaseDriven runs create → publish → release on srv and returns the
// release response (the variant tests below only differ in publisher wiring).
func cj2ReleaseDriven(t *testing.T, srv http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	id := createCJ2Course(t, srv)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: status=%d body=%s", rec.Code, rec.Body.String())
	}
	return doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+id+"/release",
		`{"price_sgd_cents":100,"instructor_gcids":["`+cj2TestAuthorGCID+`"]}`, cj2TestAdminGCID, "training-admin")
}

// emitCourseReleased's nil-publisher no-op: release must still 200.
func TestCj2_Release_NilPublisher_NoOp_200(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		d.OutboxPublisher = nil
	})
	rec := cj2ReleaseDriven(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("release with nil publisher: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// publishCourseEvent's type-assertion drop-branch: a publisher without
// PublishCustom must not crash the release.
func TestCj2_Release_NoCustomPublisher_NoOp_200(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		d.OutboxPublisher = cj2NoCustomPublisher{}
	})
	rec := cj2ReleaseDriven(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("release with no-custom publisher: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// publishCourseEvent's log.Printf failure branch: emission is fire-and-forget,
// a failing PublishCustom must still yield a 200 release.
func TestCj2_Release_PublishCustomError_Still200(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		d.OutboxPublisher = cj2FailPublisher{}
	})
	rec := cj2ReleaseDriven(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("release with failing publisher: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// scheduled_open_at flows into the DTO + the released-event payload.
func TestCj2_Release_ScheduledOpenAt_200(t *testing.T) {
	srv, pub, _ := cj2Server(nil)
	id := createCJ2Course(t, srv)
	if rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+id+"/publish", "", cj2TestAuthorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("publish: status=%d body=%s", rec.Code, rec.Body.String())
	}
	openAt := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339Nano)
	body := `{"price_sgd_cents":99900,"sf_eligible":true,"instructor_gcids":["` + cj2TestAuthorGCID + `"],"scheduled_open_at":"` + openAt + `"}`
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+id+"/release", body, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("release: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["scheduled_open_at"] == nil {
		t.Errorf("DTO scheduled_open_at missing: %v", got)
	}
	found := false
	for _, ev := range pub.History() {
		if ev.Topic != "chora.delivery.course.released.v1" {
			continue
		}
		if _, ok := ev.Payload["scheduled_open_at"]; ok {
			found = true
		}
	}
	if !found {
		t.Errorf("released event payload missing scheduled_open_at; history=%d", len(pub.History()))
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CourseReject — 409 / 400 / 404 / error envelopes.
// -----------------------------------------------------------------------------

func TestCj2_Reject_Draft_409(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/reject",
		`{"review_notes":"x"}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Reject_EmptyNotes_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Awaiting", domain.CourseStateAwaitingReview)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/reject",
		`{"review_notes":""}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Reject_NotFound_404(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/00000000-0000-0000-0000-000000000000/reject",
		`{"review_notes":"x"}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestCj2_Reject_DecodeError_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Awaiting", domain.CourseStateAwaitingReview)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/reject", "{oops", cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCj2_Reject_SaveError_500(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, saveErr: errors.New("db down")}
	})
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Awaiting", domain.CourseStateAwaitingReview)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/reject",
		`{"review_notes":"x"}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Reject_GetError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, getErr: errors.New("db down")}
	})
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/reject",
		`{"review_notes":"x"}`, cj2TestAdminGCID, "training-admin")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleCJ2CourseCheckout — wiring guards + FSM gates + payments round-trip.
// -----------------------------------------------------------------------------

func TestCj2_Checkout_PaymentsNil_501(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status=%d want 501 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Checkout_EmptyTemplates_501(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		d.Payments = payments.NewClient(&cj2PayStub{})
	})
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status=%d want 501 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Checkout_MissingTenant_400(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{})
	})
	req := cj2Req(http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/checkout", "", "", cj2TestLearnerGCID, "")
	rec := cj2Do(srv, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCj2_Checkout_UnknownCourse_404(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{})
	})
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/00000000-0000-0000-0000-000000000000/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestCj2_Checkout_InvisibleDraft_404(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{})
	})
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("invisible draft checkout: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Checkout_NotPublished_409(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{})
	})
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/checkout", "", cj2TestAuthorGCID, "")
	if rec.Code != http.StatusConflict {
		t.Errorf("unpublished checkout: status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Checkout_FreeCourse_409(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{})
	})
	c := cj2SeedPublished(t, store, 0)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusConflict {
		t.Errorf("free-course checkout: status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Checkout_RPCError_502(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{err: errors.New("rpc down")})
	})
	c := cj2SeedPublished(t, store, 99900)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status=%d want 502 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCj2_Checkout_Happy_200(t *testing.T) {
	srv, _, store := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{
			resp: &paymentsv1.CreateCourseCheckoutSessionResponse{
				PurchaseId:        "purchase-1",
				StripeSessionId:   "cs_test_1",
				StripeCheckoutUrl: "https://checkout.stripe.com/c/cs_test_1",
				State:             paymentsv1.PurchaseState_PURCHASE_STATE_CHECKOUT_STARTED,
			},
		})
	})
	c := cj2SeedPublished(t, store, 99900)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+c.ID+"/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["checkout_url"] != "https://checkout.stripe.com/c/cs_test_1" {
		t.Errorf("checkout_url=%v want the stub checkout URL", got["checkout_url"])
	}
	if got["session_id"] != "cs_test_1" {
		t.Errorf("session_id=%v want cs_test_1", got["session_id"])
	}
}

func TestCj2_Checkout_GetError_500(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, s *domain.InMemCourseCJ2Store) {
		d.Courses = &cj2FailStore{inner: s, getErr: errors.New("db down")}
		cj2WireCheckout(d, &cj2PayStub{})
	})
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/checkout", "", cj2TestLearnerGCID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Missing-gcid 401 — every CJ#2 handler's callerTenantGCID early-return.
//
// tenantRequired already rejects missing-tenant requests, so the ONLY reachable
// path into each handler's `if tenantID == "" { return }` block is a request
// with a tenant but NO gcid: the middleware passes it, callerTenantGCID 401s
// and returns ("", "") and the handler bails out.
// -----------------------------------------------------------------------------

func TestCj2_Handlers_MissingGCID_401(t *testing.T) {
	// Checkout checks its wiring guards BEFORE callerTenantGCID, so the
	// checkout row needs Payments wired to reach the 401 early-return (the
	// other six handlers never look at Payments).
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		cj2WireCheckout(d, &cj2PayStub{})
	})
	reqs := []*http.Request{
		cj2Req(http.MethodGet, "/api/v1/courses?state=PUBLISHED", "", cj2TestTenantID, "", "instructor"),
		cj2Req(http.MethodGet, "/api/v1/courses/"+cj2TestTestSetID, "", cj2TestTenantID, "", "instructor"),
		cj2Req(http.MethodPatch, "/api/v1/courses/"+cj2TestTestSetID, `{"description":"x"}`, cj2TestTenantID, "", "instructor"),
		cj2Req(http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/publish", "", cj2TestTenantID, "", "instructor"),
		cj2Req(http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/release", `{"price_sgd_cents":1}`, cj2TestTenantID, "", "training-admin"),
		cj2Req(http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/reject", `{"review_notes":"x"}`, cj2TestTenantID, "", "training-admin"),
		cj2Req(http.MethodPost, "/api/v1/courses/"+cj2TestTestSetID+"/checkout", "", cj2TestTenantID, "", ""),
	}
	for i, req := range reqs {
		rec := cj2Do(srv, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("req[%d] %s %s: status=%d want 401", i, req.Method, req.URL.Path, rec.Code)
		}
	}
}

// -----------------------------------------------------------------------------
// RBAC + cert-DTO leftover branches.
// -----------------------------------------------------------------------------

// hasInstructorOrAdmin's consume-and-deny branch: a non-empty roles header
// that is not instructor/admin/training-admin.
func TestCj2_Create_NonInstructorRole_403(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses",
		`{"title":"T","test_set_ids":["`+cj2TestTestSetID+`"]}`, cj2TestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

// toDomain's require_all_content override branch — an EXPLICIT false replaces
// the default true on the wire cert block.
func TestCj2_Create_CertRequireAllContentFalse_201(t *testing.T) {
	srv, _, _ := cj2Server(nil)
	body := `{"title":"T","test_set_ids":["` + cj2TestTestSetID + `"],"certification":{"enabled":true,"cert_type":"competency","passing_score_pct":70,"require_all_content":false}}`
	rec := doCJ2(t, srv, http.MethodPost, "/api/v1/courses", body, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	cert, _ := got["certification"].(map[string]any)
	if cert["require_all_content"] != false {
		t.Errorf("cert.require_all_content=%v want false", cert["require_all_content"])
	}
	if cert["passing_score_pct"].(float64) != 70 {
		t.Errorf("cert.passing_score_pct=%v want 70", cert["passing_score_pct"])
	}
}

// -----------------------------------------------------------------------------
// HandleCJ2CourseUpdate — optional-field mapping + cert validation branches.
// -----------------------------------------------------------------------------

func TestCj2_Update_AllOptionalFields_200(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	body := `{"title":"Renamed","learning_objectives":["LO-A"],"prerequisites":["P1"],"test_set_ids":["` + cj2TestTestSetID + `"],"certification":{"enabled":true,"cert_type":"competency","passing_score_pct":60}}`
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/"+c.ID, body, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["title"] != "Renamed" {
		t.Errorf("title=%v want Renamed", got["title"])
	}
	if los, _ := got["learning_objectives"].([]interface{}); len(los) != 1 || los[0] != "LO-A" {
		t.Errorf("learning_objectives=%v", got["learning_objectives"])
	}
	cert, _ := got["certification"].(map[string]interface{})
	if cert["cert_type"] != "COMPETENCY" {
		t.Errorf("cert.cert_type=%v want COMPETENCY", cert["cert_type"])
	}
}

// UpdateDraftContent re-validates the cert block — an invalid cert_type on
// PATCH surfaces as a 400, not a save.
func TestCj2_Update_InvalidCert_400(t *testing.T) {
	srv, _, store := cj2Server(nil)
	c := cj2SeedCourse(t, store, cj2TestAuthorGCID, "Draft", domain.CourseStateDraft)
	rec := doCJ2(t, srv, http.MethodPatch, "/api/v1/courses/"+c.ID,
		`{"certification":{"enabled":true,"cert_type":"DIPLOMA"}}`, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// cj2CoursesSubHandler — content-subtree delegation (Content wired).
// -----------------------------------------------------------------------------

// cj2CCRepo is a course_content.Repository that reports an empty curriculum.
type cj2CCRepo struct{}

func (cj2CCRepo) Get(_ context.Context, _, _ string) (*cc.CourseContent, error) {
	return nil, cc.ErrNotFound
}

func (cj2CCRepo) Save(_ context.Context, _ *cc.CourseContent) error { return nil }

// cj2CCPub is a no-op course_content.Publisher.
type cj2CCPub struct{}

func (cj2CCPub) PublishContentComposed(_ context.Context, _ string, _ *cc.CourseContent) error {
	return nil
}

// GET /{id}/content with a wired Content deps delegates to the content
// handler, which answers the "no curriculum yet" 200 {items:[]} — proving the
// sub-handler did NOT fall through to its own 404.
func TestCj2_SubDispatch_ContentSubtreeDelegated(t *testing.T) {
	srv, _, _ := cj2Server(func(d *httpapi.CourseCJ2Deps, _ *domain.InMemCourseCJ2Store) {
		d.Content = &httpapi.CourseContentDeps{
			Svc: cc.NewService(cj2CCRepo{}, cj2CCPub{}),
		}
	})
	rec := doCJ2(t, srv, http.MethodGet, "/api/v1/courses/"+cj2TestTestSetID+"/content", "", cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("content subtree: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode content body: %v", err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("expected empty curriculum, got %v", got.Items)
	}
}
