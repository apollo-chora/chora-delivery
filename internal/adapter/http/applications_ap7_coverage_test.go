// applications_ap7_coverage_test.go — coverage-driving tests for the
// learner-side application surface (applications.go) that were NOT exercised
// by the S6.1 spec suite in applications_test.go: method guards (405),
// malformed-body 400s, cross-tenant 404s, dependency-missing 503s, error
// paths (500/502) and the optional applicationDTO-ish fields.
//
// Helpers defined here carry the `ap7` prefix (they are NOT redefinitions of
// the shared suite helpers: doReq / newAppTestServer / seedAdminQueueApps /
// reqAdminGET / stubPaymentsRPC / failingAppRepo / reqJSON / reqGET all live
// in sibling files and are reused as-is).
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

	"google.golang.org/grpc"

	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/payments/v1"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/invoice"
	"github.com/apollo-chora/chora-delivery/internal/adapter/payments"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	ap7PrivateCourseID = "01970000-0000-7000-8000-BB000000000001"
	ap7OtherTenantID   = "01970000-0000-7000-8000-0000000000aa"
)

// ap7RawReq issues a request with an already-serialised body (used for the
// malformed-JSON 400 paths where doReq's json.Marshal would never fire).
func ap7RawReq(t *testing.T, srv http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Tenant-Id", tenantHdr)
	req.Header.Set("gcid", gcidHdr)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// ap7CourseCatalogue builds a Catalogue port seeded with the canonical paid
// course (tenantHdr, public) plus a tenant-only course owned by ANOTHER
// tenant (for the cross-tenant submit 404). Mirrors the seeding done inside
// newAppTestServer so partial Deps wiring can reuse one fixture.
func ap7CourseCatalogue() domain.CataloguePort {
	cat := domain.NewCatalogue()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:        tenantHdr,
		Title:           "Ap7 Paid Course",
		InstructorGCID:  "01970000-0000-7000-9000-000000000999",
		InstructorName:  "Mr. Chen",
		PriceSGDCents:   210000,
		Visibility:      domain.VisibilityPublic,
		SFEligible:      true,
		Tags:            []string{"agile"},
		SyllabusOutline: []string{"Sprint Mechanics"},
	})
	pc.ID = courseID
	cat.Save(pc)

	priv, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       ap7OtherTenantID,
		Title:          "Other Tenant Private Course",
		InstructorGCID: "01970000-0000-7000-9000-000000000999",
		InstructorName: "Mr. Chen",
		PriceSGDCents:  100,
		Visibility:     domain.VisibilityTenantOnly,
	})
	priv.ID = ap7PrivateCourseID
	cat.Save(priv)

	return domain.NewInMemCatalogueFrom(cat)
}

// ap7FailedCatalogue is a CataloguePort whose reads fail — drives the 500
// branches of handleApplicationForm / handleApplicationSubmit.
type ap7FailedCatalogue struct{ err error }

func (ap7FailedCatalogue) Save(context.Context, *domain.PublicCourse) error { return nil }
func (c ap7FailedCatalogue) Get(context.Context, string) (*domain.PublicCourse, bool, error) {
	return nil, false, c.err
}
func (ap7FailedCatalogue) Search(context.Context, domain.CatalogueQuery) ([]*domain.PublicCourse, int, error) {
	return nil, 0, nil
}
func (ap7FailedCatalogue) SearchCursor(context.Context, domain.CatalogueQuery) (domain.CursorPage, error) {
	return domain.CursorPage{}, nil
}
func (ap7FailedCatalogue) ListByInstructor(context.Context, string, string, int, int) ([]*domain.PublicCourse, int, error) {
	return nil, 0, nil
}

// ap7SaveErrAppRepo embeds the real in-memory repo but lets Save fail on
// demand — drives the 500 branches of handleAcceptOffer / handleWithdraw /
// handleInvoice (the shared failingAppRepo cannot: its Get fails first, so
// the sub-routes would 404/500 before reaching Save).
type ap7SaveErrAppRepo struct {
	*repoinmem.ApplicationRepo
	failSave bool
	saveErr  error
}

func (r *ap7SaveErrAppRepo) Save(ctx context.Context, app *application.Application) error {
	if r.failSave {
		return r.saveErr
	}
	return r.ApplicationRepo.Save(ctx, app)
}

var _ application.ApplicationPort = (*ap7SaveErrAppRepo)(nil)

// ap7ErrPaymentsRPC is a PaymentServiceGRPCClient whose application-session
// mint always fails — drives the 502 branch of handleAcceptOffer.
type ap7ErrPaymentsRPC struct{ err error }

func (ap7ErrPaymentsRPC) CreateCourseCheckoutSession(context.Context, *paymentsv1.CreateCourseCheckoutSessionRequest, ...grpc.CallOption) (*paymentsv1.CreateCourseCheckoutSessionResponse, error) {
	return nil, nil
}
func (r ap7ErrPaymentsRPC) CreateApplicationCheckoutSession(context.Context, *paymentsv1.CreateApplicationCheckoutSessionRequest, ...grpc.CallOption) (*paymentsv1.CreateApplicationCheckoutSessionResponse, error) {
	return nil, r.err
}

// ap7FailStorage is an invoice.Storage whose Put fails — drives the 500
// branch of handleInvoice.
type ap7FailStorage struct{ err error }

func (s ap7FailStorage) Put(context.Context, invoice.PutInput) (string, error) {
	return "", s.err
}
func (ap7FailStorage) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "gs://ap7/object", nil
}

// ap7SeedApplication submits via HTTP and returns the created application id.
func ap7SeedApplication(t *testing.T, srv http.Handler) string {
	t.Helper()
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications",
		map[string]string{"course_id": courseID}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed submit: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("seed submit decode: %v", err)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("seed submit returned no id: %s", w.Body.String())
	}
	return id
}

// ap7OfferMadeApp seeds + walks an application to StatusOfferMade through the
// supplied ApplicationPort and persists it. Mirror of the accept-offer test
// transition sequence in applications_test.go.
func ap7OfferMadeApp(t *testing.T, apps application.ApplicationPort) *application.Application {
	t.Helper()
	ctx := context.Background()
	app, _, err := apps.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantHdr, CourseID: courseID, GCID: gcidHdr,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := app.Transition(application.StatusUnderReview); err != nil {
		t.Fatalf("under_review: %v", err)
	}
	if err := app.Transition(application.StatusOfferMade); err != nil {
		t.Fatalf("offer_made: %v", err)
	}
	if err := apps.Save(ctx, app); err != nil {
		t.Fatalf("save: %v", err)
	}
	return app
}

// ap7PaidApp seeds + walks an application to StatusPaid with an attached
// payment intent (precondition for the invoice routes).
func ap7PaidApp(t *testing.T, apps application.ApplicationPort) *application.Application {
	t.Helper()
	ctx := context.Background()
	app, _, err := apps.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantHdr, CourseID: courseID, GCID: gcidHdr,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	for _, st := range []application.Status{
		application.StatusUnderReview, application.StatusOfferMade,
		application.StatusAccepted, application.StatusPaid,
	} {
		if err := app.Transition(st); err != nil {
			t.Fatalf("transition %s: %v", st, err)
		}
	}
	app.AttachPaymentIntent("pi_ap7_paid")
	if err := apps.Save(ctx, app); err != nil {
		t.Fatalf("save: %v", err)
	}
	return app
}

// ---------------------------------------------------------------------------
// meApplicationsHandler dispatch guard — 405 for unsupported methods.
// ---------------------------------------------------------------------------

func TestAp7Applications_RootMethodNotAllowed_405(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	w := doReq(t, srv, http.MethodPut, "/v1/me/applications", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleApplicationSubmit residual branches.
// ---------------------------------------------------------------------------

func TestAp7Applications_Submit_MalformedJSON_400(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	w := ap7RawReq(t, srv, http.MethodPost, "/v1/me/applications", `{"course_id":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7Applications_Submit_UnknownCourse_404(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications",
		map[string]string{"course_id": "no-such-course"}, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7Applications_Submit_CrossTenantCourse_404(t *testing.T) {
	t.Parallel()
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7CourseCatalogue(),
		Applications: repoinmem.NewApplicationRepo(),
	})
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications",
		map[string]string{"course_id": ap7PrivateCourseID}, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (tenant_only course owned by another tenant) body=%s",
			w.Code, w.Body.String())
	}
}

func TestAp7Applications_Submit_CatalogueError_500(t *testing.T) {
	t.Parallel()
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7FailedCatalogue{err: errors.New("catalogue: conn closed")},
		Applications: repoinmem.NewApplicationRepo(),
	})
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications",
		map[string]string{"course_id": courseID}, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7Applications_Submit_RepoError_400(t *testing.T) {
	t.Parallel()
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7CourseCatalogue(),
		Applications: failingAppRepo{err: errors.New("pg: submit failed: conn busy")},
	})
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications",
		map[string]string{"course_id": courseID}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleApplicationList residual branches.
// ---------------------------------------------------------------------------

func TestAp7Applications_List_RepoError_500(t *testing.T) {
	t.Parallel()
	srv := httpapi.NewServer(httpapi.Deps{
		Applications: failingAppRepo{err: errors.New("pg: list by gcid: conn closed")},
	})
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleApplicationForm residual branch — catalogue read failure → 500.
// ---------------------------------------------------------------------------

func TestAp7ApplicationForm_CatalogueError_500(t *testing.T) {
	t.Parallel()
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:   ap7FailedCatalogue{err: errors.New("catalogue: connection reset")},
		Enrollments: domain.NewInMemEnrollmentStore(), // /v1/courses/... routes require Enrollments to be mounted
	})
	w := doReq(t, srv, http.MethodGet, "/v1/courses/"+courseID+"/application-form", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// meApplicationsSubHandler dispatch guards + error path.
// ---------------------------------------------------------------------------

func TestAp7ApplicationSub_EmptyID_404(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (trailing-slash with empty id) body=%s", w.Code, w.Body.String())
	}
}

func TestAp7ApplicationSub_UnknownAction_404(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	id := ap7SeedApplication(t, srv)
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+id+"/foobar", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7ApplicationSub_DetailWrongMethod_405(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	id := ap7SeedApplication(t, srv)
	w := doReq(t, srv, http.MethodPut, "/v1/me/applications/"+id, nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7ApplicationSub_AcceptOfferWrongMethod_405(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	id := ap7SeedApplication(t, srv)
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+id+"/accept-offer", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7ApplicationSub_WithdrawWrongMethod_405(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	id := ap7SeedApplication(t, srv)
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+id+"/withdraw", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7ApplicationSub_InvoiceWrongMethod_405(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	id := ap7SeedApplication(t, srv)
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications/"+id+"/invoice", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7ApplicationSub_RepoError_500(t *testing.T) {
	t.Parallel()
	srv := httpapi.NewServer(httpapi.Deps{
		Applications: failingAppRepo{err: errors.New("pg: get application: conn busy")},
	})
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/01970000-0000-7000-8000-DEADBEEFDEAD", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleAcceptOffer residual branches.
// ---------------------------------------------------------------------------

func TestAp7AcceptOffer_ZeroNetAmount_400(t *testing.T) {
	t.Parallel()
	srv, seeded := newAppTestServer(t)
	// The shared fixture seeds a FREE course (price 0) → net payable 0 → 400.
	ctx := context.Background()
	app, _, err := seeded.applications.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantHdr,
		CourseID: "01970000-0000-7000-8000-FFFFFFFFFFFF",
		GCID:     gcidHdr,
	})
	if err != nil {
		t.Fatalf("submit free course: %v", err)
	}
	_ = app.Transition(application.StatusUnderReview)
	_ = app.Transition(application.StatusOfferMade)
	_ = seeded.applications.Save(ctx, app)

	w := doReq(t, srv, http.MethodPost, "/v1/me/applications/"+app.ID+"/accept-offer", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (net payable must be positive) body=%s", w.Code, w.Body.String())
	}
}

func TestAp7AcceptOffer_PaymentsNotWired_503(t *testing.T) {
	t.Parallel()
	apps := repoinmem.NewApplicationRepo()
	app := ap7OfferMadeApp(t, apps)
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7CourseCatalogue(),
		Applications: apps,
	})
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications/"+app.ID+"/accept-offer", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (payments client not configured) body=%s", w.Code, w.Body.String())
	}
}

func TestAp7AcceptOffer_PaymentsError_502(t *testing.T) {
	t.Parallel()
	apps := repoinmem.NewApplicationRepo()
	app := ap7OfferMadeApp(t, apps)
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7CourseCatalogue(),
		Applications: apps,
		Payments:     payments.NewClient(ap7ErrPaymentsRPC{err: errors.New("payments: rpc deadline exceeded")}),
	})
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications/"+app.ID+"/accept-offer", nil, nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7AcceptOffer_SaveError_500(t *testing.T) {
	t.Parallel()
	repo := &ap7SaveErrAppRepo{
		ApplicationRepo: repoinmem.NewApplicationRepo(),
		saveErr:         errors.New("pg: save application: conn closed"),
	}
	app := ap7OfferMadeApp(t, repo)
	repo.failSave = true
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7CourseCatalogue(),
		Applications: repo,
		Payments:     payments.NewClient(&stubPaymentsRPC{}),
	})
	w := doReq(t, srv, http.MethodPost, "/v1/me/applications/"+app.ID+"/accept-offer", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleWithdraw residual branches.
// ---------------------------------------------------------------------------

func TestAp7Withdraw_FromEnrolled_409(t *testing.T) {
	t.Parallel()
	srv, seeded := newAppTestServer(t)
	ctx := context.Background()
	app, _, err := seeded.applications.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantHdr, CourseID: courseID, GCID: gcidHdr,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	for _, st := range []application.Status{
		application.StatusUnderReview, application.StatusOfferMade,
		application.StatusAccepted, application.StatusPaid, application.StatusEnrolled,
	} {
		if err := app.Transition(st); err != nil {
			t.Fatalf("transition %s: %v", st, err)
		}
	}
	_ = seeded.applications.Save(ctx, app)

	w := doReq(t, srv, http.MethodPost, "/v1/me/applications/"+app.ID+"/withdraw",
		map[string]string{"reason": "too late"}, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (enrolled is terminal) body=%s", w.Code, w.Body.String())
	}
}

func TestAp7Withdraw_SaveError_500(t *testing.T) {
	t.Parallel()
	repo := &ap7SaveErrAppRepo{
		ApplicationRepo: repoinmem.NewApplicationRepo(),
		saveErr:         errors.New("pg: save withdraw: conn busy"),
	}
	ctx := context.Background()
	app, _, err := repo.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantHdr, CourseID: courseID, GCID: gcidHdr,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	_ = repo.Save(ctx, app) // real persist
	repo.failSave = true
	srv := httpapi.NewServer(httpapi.Deps{Applications: repo})

	w := doReq(t, srv, http.MethodPost, "/v1/me/applications/"+app.ID+"/withdraw",
		map[string]string{"reason": "changed mind"}, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleInvoice residual branches.
// ---------------------------------------------------------------------------

func TestAp7Invoice_InvoiceNotWired_503(t *testing.T) {
	t.Parallel()
	apps := repoinmem.NewApplicationRepo()
	app := ap7PaidApp(t, apps)
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7CourseCatalogue(),
		Applications: apps,
	})
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+app.ID+"/invoice", nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (invoice adapter not configured) body=%s", w.Code, w.Body.String())
	}
}

func TestAp7Invoice_CourseGone_404(t *testing.T) {
	t.Parallel()
	apps := repoinmem.NewApplicationRepo()
	app := ap7PaidApp(t, apps)
	issuer := invoice.NewIssuer(invoice.NewPDFGenerator(),
		invoice.NewInMemoryStorage("ap7-invoice-404"), "ap7-invoice-404")
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    domain.NewInMemCatalogue(), // empty — course dropped
		Applications: apps,
		Invoice:      issuer,
	})
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+app.ID+"/invoice", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (course not found) body=%s", w.Code, w.Body.String())
	}
}

func TestAp7Invoice_IssueError_500(t *testing.T) {
	t.Parallel()
	apps := repoinmem.NewApplicationRepo()
	app := ap7PaidApp(t, apps)
	issuer := invoice.NewIssuer(invoice.NewPDFGenerator(),
		ap7FailStorage{err: errors.New("gcs: upload failed: 503")}, "ap7-invoice-500")
	srv := httpapi.NewServer(httpapi.Deps{
		Catalogue:    ap7CourseCatalogue(),
		Applications: apps,
		Invoice:      issuer,
	})
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+app.ID+"/invoice", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 body=%s", w.Code, w.Body.String())
	}
}

func TestAp7Invoice_FundingAndExistingInvoice_200(t *testing.T) {
	t.Parallel()
	srv, seeded := newAppTestServer(t)
	ctx := context.Background()
	app := ap7PaidApp(t, seeded.applications)
	// One eligible + one ineligible funding line: exercises the funding-loop
	// skip/append arms and the fundedSum arithmetic.
	app.FundingLines = []application.FundingLine{
		{Type: application.FundingTypeSkillsFutureCredits, MaxAmountSGDCents: 50000, Eligible: true},
		{Type: application.FundingTypeWSGETSS, MaxAmountSGDCents: 25000, Eligible: false},
	}
	if err := seeded.applications.Save(ctx, app); err != nil {
		t.Fatalf("save funding: %v", err)
	}

	first := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+app.ID+"/invoice", nil, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first invoice: status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResp map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &firstResp)
	if firstResp["invoice_number"] == "" {
		t.Fatalf("first invoice: missing invoice_number; body=%s", first.Body.String())
	}
	if firstResp["signed_url"] == "" {
		t.Fatalf("first invoice: missing signed_url; body=%s", first.Body.String())
	}

	// Second issue on the same app: InvoiceID is already attached, so the
	// handler takes the `invNo := app.InvoiceID` non-empty arm and the issuer
	// short-circuits on its idempotent storage path.
	second := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+app.ID+"/invoice", nil, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second invoice: status=%d body=%s", second.Code, second.Body.String())
	}
	var secondResp map[string]interface{}
	_ = json.Unmarshal(second.Body.Bytes(), &secondResp)
	if secondResp["invoice_number"] != firstResp["invoice_number"] {
		t.Fatalf("re-issue changed invoice number: %v vs %v", firstResp["invoice_number"], secondResp["invoice_number"])
	}
}

func TestAp7Invoice_FundingOverGross_ClampsNetZero_200(t *testing.T) {
	t.Parallel()
	srv, seeded := newAppTestServer(t)
	ctx := context.Background()
	app := ap7PaidApp(t, seeded.applications)
	// Eligible funding exceeds gross+GST → the handler clamps net to 0.
	app.FundingLines = []application.FundingLine{
		{Type: application.FundingTypeMidCareer, MaxAmountSGDCents: 9000000, Eligible: true},
	}
	if err := seeded.applications.Save(ctx, app); err != nil {
		t.Fatalf("save funding: %v", err)
	}
	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+app.ID+"/invoice", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (net clamped to 0 must still issue) body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// applicationDTO / applicationDetailDTO — every optional field arm.
// ---------------------------------------------------------------------------

func TestAp7ApplicationDetail_AllOptionalDTOFields(t *testing.T) {
	t.Parallel()
	srv, seeded := newAppTestServer(t)
	ctx := context.Background()
	app, _, err := seeded.applications.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantHdr, CourseID: courseID, ClassID: "cls-ap7-4242", GCID: gcidHdr,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	app.SetOfferExpiresAt(time.Now().Add(72 * time.Hour))
	app.AttachPaymentIntent("pi_ap7_dto")
	app.AttachInvoice("inv_ap7_dto")
	app.AttachSingpassSession("sp_ap7_dto")
	app.FundingLines = []application.FundingLine{
		{Type: application.FundingTypeSkillsFutureCredits, MaxAmountSGDCents: 50000, Eligible: false},
	}
	if err := app.TransitionWithReason(application.StatusWithdrawn, "changed plans"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if err := seeded.applications.Save(ctx, app); err != nil {
		t.Fatalf("save: %v", err)
	}

	w := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+app.ID, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("detail: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{
		"class_id", "offer_expires_at", "stripe_payment_intent_id",
		"invoice_id", "singpass_session_id", "withdrawn_reason",
	} {
		if resp[key] == nil || resp[key] == "" {
			t.Errorf("detail missing %s; body=%s", key, w.Body.String())
		}
	}
	funding, ok := resp["funding_lines"].([]interface{})
	if !ok || len(funding) == 0 {
		t.Fatalf("expected funding_lines in detail; body=%s", w.Body.String())
	}
	hist, ok := resp["history"].([]interface{})
	if !ok || len(hist) == 0 {
		t.Fatalf("expected history in detail; body=%s", w.Body.String())
	}
	// The last transition is the withdraw (with a reason) — the detail DTO
	// only emits `reason` when present.
	last, ok := hist[len(hist)-1].(map[string]interface{})
	if !ok || last["reason"] != "changed plans" {
		t.Fatalf("expected withdraw reason on last history row; hist=%v", hist)
	}
}
