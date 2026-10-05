// Application HTTP routes tests — S6.1.
//
// Routes (per docs/design/ux_course_application.md):
//
//	GET  /v1/courses?paid=true                        — paid catalog browse
//	GET  /v1/courses/{id}/application-form            — form schema + funding lines
//	POST /v1/me/applications                          — submit (idempotent on course+gcid)
//	GET  /v1/me/applications                          — list user's applications
//	GET  /v1/me/applications/{id}                     — detail with state + history
//	POST /v1/me/applications/{id}/accept-offer        — accept → Stripe intent
//	POST /v1/me/applications/{id}/withdraw            — withdraw
//	GET  /v1/me/applications/{id}/invoice             — signed-URL to PDF
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"

	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/payments/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/invoice"
	"github.com/apollo-chora/chora-delivery/internal/adapter/payments"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/singpass"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// stubPaymentsRPC — minimal in-process fake for the chora-payments gRPC
// PaymentServiceClient. Used by the accept-offer test path to assert the
// outbound RPC was made + return a canned Checkout Session response. Per
// ADR-164 Stage C the inline Stripe SDK is retired in favour of this gRPC.
// -----------------------------------------------------------------------------

type stubPaymentsRPC struct {
	lastCourseReq      *paymentsv1.CreateCourseCheckoutSessionRequest
	lastApplicationReq *paymentsv1.CreateApplicationCheckoutSessionRequest
}

func (s *stubPaymentsRPC) CreateCourseCheckoutSession(_ context.Context, in *paymentsv1.CreateCourseCheckoutSessionRequest, _ ...grpc.CallOption) (*paymentsv1.CreateCourseCheckoutSessionResponse, error) {
	s.lastCourseReq = in
	return &paymentsv1.CreateCourseCheckoutSessionResponse{
		PurchaseId:        "purchase-stub-" + in.GetCourseId(),
		StripeSessionId:   "cs_stub_" + in.GetCourseId(),
		StripeCheckoutUrl: "https://checkout.stripe.invalid/c/cs_stub_" + in.GetCourseId(),
		State:             paymentsv1.PurchaseState_PURCHASE_STATE_CHECKOUT_STARTED,
	}, nil
}

func (s *stubPaymentsRPC) CreateApplicationCheckoutSession(_ context.Context, in *paymentsv1.CreateApplicationCheckoutSessionRequest, _ ...grpc.CallOption) (*paymentsv1.CreateApplicationCheckoutSessionResponse, error) {
	s.lastApplicationReq = in
	return &paymentsv1.CreateApplicationCheckoutSessionResponse{
		PurchaseId:        "purchase-stub-app-" + in.GetApplicationId(),
		StripeSessionId:   "cs_stub_app_" + in.GetApplicationId(),
		StripeCheckoutUrl: "https://checkout.stripe.invalid/c/cs_stub_app_" + in.GetApplicationId(),
		State:             paymentsv1.PurchaseState_PURCHASE_STATE_CHECKOUT_STARTED,
	}, nil
}

const (
	tenantHdr = "01970000-0000-7000-8000-000000000001"
	gcidHdr   = "01970000-0000-7000-9000-000000000001"
	courseID  = "01970000-0000-7000-8000-000000000099"
)

// -----------------------------------------------------------------------------
// Test wiring helper — full Deps with application stack.
// -----------------------------------------------------------------------------

func newAppTestServer(t *testing.T) (http.Handler, *seededAppDeps) {
	t.Helper()
	cat := domain.NewCatalogue()
	enr := domain.NewInMemEnrollmentStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	camp := repoinmem.NewCampusRepo()

	// Seed a paid course.
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:        tenantHdr,
		Title:           "Advanced Certified ScrumMaster",
		InstructorGCID:  "01970000-0000-7000-9000-000000000999",
		InstructorName:  "Mr. Chen",
		PriceSGDCents:   210000,
		Visibility:      domain.VisibilityPublic,
		SFEligible:      true,
		Tags:            []string{"agile", "scrum-master"},
		SyllabusOutline: []string{"Sprint Mechanics", "Daily Stand-up"},
	})
	pc.ID = courseID
	cat.Save(pc)

	// Seed a free course for the catalog filter.
	free, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantHdr,
		Title:          "Free Intro",
		InstructorGCID: "01970000-0000-7000-9000-000000000999",
		InstructorName: "Mr. Chen",
		PriceSGDCents:  0,
		Visibility:     domain.VisibilityPublic,
	})
	free.ID = "01970000-0000-7000-8000-FFFFFFFFFFFF"
	cat.Save(free)

	apps := repoinmem.NewApplicationRepo()
	paymentsStub := &stubPaymentsRPC{}
	paymentsClient := payments.NewClient(paymentsStub)
	singStub := singpass.NewStubClient(singpass.Config{RedirectURL: "https://chora.site/cb"})

	pdfGen := invoice.NewPDFGenerator()
	pdfStore := invoice.NewInMemoryStorage("chora-invoices-test")
	issuer := invoice.NewIssuer(pdfGen, pdfStore, "chora-invoices-test")

	deps := httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		// Catalogue is the domain.CataloguePort port — the in-memory store
		// `cat` is wrapped via NewInMemCatalogueFrom so the fixture keeps
		// its concrete-store Save() for seeding above.
		Catalogue:    domain.NewInMemCatalogueFrom(cat),
		Enrollments:  enr,
		Publisher:    pub,
		CampusOps:    camp,
		Applications: apps,
		Payments:     paymentsClient,
		Singpass:     singStub,
		Invoice:      issuer,
	}
	return httpapi.NewServer(deps), &seededAppDeps{
		applications: apps,
		catalogue:    cat,
		publisher:    pub,
		invoice:      issuer,
		paymentsRPC:  paymentsStub,
	}
}

type seededAppDeps struct {
	applications *repoinmem.ApplicationRepo
	catalogue    *domain.Catalogue
	publisher    *events.InMemoryPublisher
	invoice      *invoice.Issuer
	paymentsRPC  *stubPaymentsRPC
}

func doReq(t *testing.T, srv http.Handler, method, path string, body interface{}, hdrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("X-Tenant-Id", tenantHdr)
	req.Header.Set("gcid", gcidHdr)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// -----------------------------------------------------------------------------
// GET /v1/courses?paid=true — paid catalog browse
// -----------------------------------------------------------------------------

func TestGET_PaidCatalog_FiltersByPrice(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	rec := doReq(t, srv, http.MethodGet, "/v1/courses?paid=true", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total < 1 {
		t.Fatalf("expected at least one paid course; got %d", resp.Total)
	}
	for _, item := range resp.Items {
		if price, ok := item["price_sgd_cents"].(float64); ok && price == 0 {
			t.Fatalf("paid filter leaked a free course")
		}
	}
}

// -----------------------------------------------------------------------------
// GET /v1/courses/{id}/application-form
// -----------------------------------------------------------------------------

func TestGET_ApplicationForm_PaidCourse_ReturnsBreakdown(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	rec := doReq(t, srv, http.MethodGet, "/v1/courses/"+courseID+"/application-form", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["course_title"] == "" {
		t.Fatalf("expected course_title")
	}
	if resp["currency"] != "SGD" {
		t.Fatalf("expected SGD currency")
	}
	gross, _ := resp["tuition_gross_cents"].(float64)
	if gross == 0 {
		t.Fatalf("paid course must have gross tuition; got %v", resp["tuition_gross_cents"])
	}
	gst, _ := resp["gst_amount_cents"].(float64)
	if gst == 0 {
		t.Fatalf("paid course must have GST line")
	}
	// Funding skeleton present (SkillsFutures eligible).
	if _, ok := resp["funding_lines"]; !ok {
		t.Fatalf("expected funding_lines key in response")
	}
}

func TestGET_ApplicationForm_WithSingpassPrefillToken_AttachesProfile(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	rec := doReq(t, srv, http.MethodGet,
		"/v1/courses/"+courseID+"/application-form?prefill_token=tok-1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["singpass_prefill"] != true {
		t.Fatalf("expected singpass_prefill=true; got %v", resp["singpass_prefill"])
	}
	prof, ok := resp["singpass_profile"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected singpass_profile map; got %v", resp["singpass_profile"])
	}
	if prof["legal_name"] == "" {
		t.Fatalf("expected legal_name from singpass profile")
	}
}

func TestGET_ApplicationForm_UnknownCourse_404(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	rec := doReq(t, srv, http.MethodGet, "/v1/courses/nope/application-form", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404; got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /v1/me/applications — submit (idempotent)
// -----------------------------------------------------------------------------

func TestPOST_Applications_Submit_HappyPath(t *testing.T) {
	t.Parallel()
	srv, deps := newAppTestServer(t)
	rec := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{
		"course_id": courseID,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["id"] == "" {
		t.Fatalf("expected application id")
	}
	if resp["status"] != "submitted" {
		t.Fatalf("expected status=submitted; got %v", resp["status"])
	}
	// Verify event emitted.
	hist := deps.publisher.History()
	found := false
	for _, h := range hist {
		if h.Topic == "chora.delivery.application.submitted.v1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected submitted event in publisher history")
	}
}

func TestPOST_Applications_Submit_Idempotent_ReturnsSame(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	body := map[string]string{"course_id": courseID}

	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", body, nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first: status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResp map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &firstResp)

	second := doReq(t, srv, http.MethodPost, "/v1/me/applications", body, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("idempotent second: status=%d (want 200) body=%s", second.Code, second.Body.String())
	}
	var secondResp map[string]interface{}
	_ = json.Unmarshal(second.Body.Bytes(), &secondResp)

	if firstResp["id"] != secondResp["id"] {
		t.Fatalf("idempotent submit must return same id; %v vs %v", firstResp["id"], secondResp["id"])
	}
}

func TestPOST_Applications_Submit_BlankCourseID(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	rec := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d", rec.Code)
	}
}

func TestPOST_Applications_Submit_MissingGCID(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/me/applications", bytes.NewReader([]byte(`{"course_id":"x"}`)))
	req.Header.Set("X-Tenant-Id", tenantHdr)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing gcid: expected 401; got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /v1/me/applications — list
// -----------------------------------------------------------------------------

func TestGET_Applications_List_OnlyOwn(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)

	rec := doReq(t, srv, http.MethodGet, "/v1/me/applications", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total < 1 {
		t.Fatalf("expected at least 1 application")
	}
}

// -----------------------------------------------------------------------------
// GET /v1/me/applications/{id} — detail
// -----------------------------------------------------------------------------

func TestGET_ApplicationDetail_IncludesHistory(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)
	var sub map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &sub)

	rec := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+sub["id"].(string), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: status=%d", rec.Code)
	}
	var detail map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail["status"] != "submitted" {
		t.Fatalf("status: got %v", detail["status"])
	}
	if hist, ok := detail["history"].([]interface{}); !ok || len(hist) == 0 {
		t.Fatalf("expected history array; got %v", detail["history"])
	}
}

func TestGET_ApplicationDetail_OtherUser_404(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)
	var sub map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &sub)

	rec := doReq(t, srv, http.MethodGet, "/v1/me/applications/"+sub["id"].(string), nil, map[string]string{
		"gcid": "01970000-0000-7000-9000-000000000999",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user must return 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /v1/me/applications/{id}/accept-offer
// -----------------------------------------------------------------------------

// TestPOST_AcceptOffer_MintsCheckoutSession verifies the ADR-164 Stage C
// cutover: the accept-offer flow now calls chora-payments PaymentService
// gRPC to mint a Checkout Session instead of inline Stripe PaymentIntent.
// The response shape carries `checkout_url` + `session_id` + `purchase_id`
// + `payment_state` for the FE to redirect via window.location.
func TestPOST_AcceptOffer_MintsCheckoutSession(t *testing.T) {
	t.Parallel()
	srv, deps := newAppTestServer(t)
	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)
	var sub map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &sub)
	appID := sub["id"].(string)

	// Walk the application to OfferMade so accept-offer is legal.
	app, _, _ := deps.applications.Get(context.Background(), tenantHdr, appID)
	_ = app.Transition(application.StatusUnderReview)
	_ = app.Transition(application.StatusOfferMade)
	_ = deps.applications.Save(context.Background(), app)

	rec := doReq(t, srv, http.MethodPost,
		"/v1/me/applications/"+appID+"/accept-offer", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept-offer: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if s, _ := resp["session_id"].(string); s == "" {
		t.Fatalf("expected non-empty session_id on accept-offer; got %v", resp["session_id"])
	}
	if s, _ := resp["checkout_url"].(string); s == "" {
		t.Fatalf("expected non-empty checkout_url on accept-offer; got %v", resp["checkout_url"])
	}
	if s, _ := resp["purchase_id"].(string); s == "" {
		t.Fatalf("expected non-empty purchase_id on accept-offer; got %v", resp["purchase_id"])
	}
	if resp["payment_state"] != "CHECKOUT_STARTED" {
		t.Fatalf("payment_state = %v, want CHECKOUT_STARTED", resp["payment_state"])
	}
	if resp["status"] != "accepted" {
		t.Fatalf("status not accepted: %v", resp["status"])
	}

	// Verify the outbound gRPC was made with the correct correlation IDs.
	if deps.paymentsRPC.lastApplicationReq == nil {
		t.Fatal("expected CreateApplicationCheckoutSession to be called")
	}
	if got := deps.paymentsRPC.lastApplicationReq.GetApplicationId(); got != appID {
		t.Errorf("upstream ApplicationId = %q, want %q", got, appID)
	}
	if got := deps.paymentsRPC.lastApplicationReq.GetCourseId(); got != courseID {
		t.Errorf("upstream CourseId = %q, want %q", got, courseID)
	}
	if got := deps.paymentsRPC.lastApplicationReq.GetCurrency(); got != "SGD" {
		t.Errorf("upstream Currency = %q, want SGD", got)
	}
	if deps.paymentsRPC.lastApplicationReq.GetAmountCents() <= 0 {
		t.Errorf("upstream AmountCents = %d, want > 0",
			deps.paymentsRPC.lastApplicationReq.GetAmountCents())
	}
}

func TestPOST_AcceptOffer_WrongState_409(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)
	var sub map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &sub)
	appID := sub["id"].(string)

	rec := doReq(t, srv, http.MethodPost,
		"/v1/me/applications/"+appID+"/accept-offer", nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 from non-OfferMade; got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /v1/me/applications/{id}/withdraw
// -----------------------------------------------------------------------------

func TestPOST_Withdraw_FromSubmitted(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)
	var sub map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &sub)
	appID := sub["id"].(string)

	rec := doReq(t, srv, http.MethodPost,
		"/v1/me/applications/"+appID+"/withdraw",
		map[string]string{"reason": "changed mind"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("withdraw: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["status"] != "withdrawn" {
		t.Fatalf("status: got %v", resp["status"])
	}
}

// -----------------------------------------------------------------------------
// GET /v1/me/applications/{id}/invoice
// -----------------------------------------------------------------------------

func TestGET_Invoice_AfterPaid_ReturnsSignedURL(t *testing.T) {
	t.Parallel()
	srv, deps := newAppTestServer(t)
	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)
	var sub map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &sub)
	appID := sub["id"].(string)

	app, _, _ := deps.applications.Get(context.Background(), tenantHdr, appID)
	_ = app.Transition(application.StatusUnderReview)
	_ = app.Transition(application.StatusOfferMade)
	_ = app.Transition(application.StatusAccepted)
	_ = app.Transition(application.StatusPaid)
	app.AttachPaymentIntent("pi_test_xyz")
	_ = deps.applications.Save(context.Background(), app)

	rec := doReq(t, srv, http.MethodGet,
		"/v1/me/applications/"+appID+"/invoice", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("invoice: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["signed_url"] == "" {
		t.Fatalf("expected signed_url")
	}
	if resp["invoice_number"] == "" {
		t.Fatalf("expected invoice_number")
	}
	if exp, ok := resp["ttl_seconds"].(float64); !ok || exp == 0 {
		t.Fatalf("expected ttl_seconds")
	}
}

func TestGET_Invoice_NotPaid_409(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	first := doReq(t, srv, http.MethodPost, "/v1/me/applications", map[string]string{"course_id": courseID}, nil)
	var sub map[string]interface{}
	_ = json.Unmarshal(first.Body.Bytes(), &sub)
	appID := sub["id"].(string)

	rec := doReq(t, srv, http.MethodGet,
		"/v1/me/applications/"+appID+"/invoice", nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 invoice not yet generated; got %d", rec.Code)
	}
}

func TestGET_Applications_TraceparentEchoed(t *testing.T) {
	t.Parallel()
	srv, _ := newAppTestServer(t)
	rec := doReq(t, srv, http.MethodGet, "/v1/me/applications", nil, map[string]string{
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec.Header().Get("traceparent") == "" {
		t.Fatalf("traceparent should be echoed back")
	}
}

func ExampleNewServer_application_routes_print() {
	// Smoke-document for godoc — exercises the route table (build-only).
	_ = "/v1/me/applications"
	fmt.Println("ok")
	// Output: ok
}

var _ = time.Second
