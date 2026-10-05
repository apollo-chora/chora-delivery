// grpc_client_test.go — unit tests for the chora-payments gRPC client adapter.
//
// Per ADR-164 Stage C — chora-delivery synchronous write path. Tests use a
// stub PaymentServiceClient that records the inbound request + returns a
// canned response, exercising the adapter's input-mapping + output-mapping
// without standing up a real gRPC server.
//
// Hexagonal: ADAPTER test. The domain layer never imports this package, so
// these tests stay in package payments alongside the implementation.
package payments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/payments/v1"
)

// -----------------------------------------------------------------------------
// stubPaymentServiceClient — records inbound + returns canned response.
// -----------------------------------------------------------------------------

type stubPaymentServiceClient struct {
	lastCourseReq      *paymentsv1.CreateCourseCheckoutSessionRequest
	lastApplicationReq *paymentsv1.CreateApplicationCheckoutSessionRequest
	courseResp         *paymentsv1.CreateCourseCheckoutSessionResponse
	courseErr          error
	applicationResp    *paymentsv1.CreateApplicationCheckoutSessionResponse
	applicationErr     error
}

func (s *stubPaymentServiceClient) CreateCourseCheckoutSession(ctx context.Context, in *paymentsv1.CreateCourseCheckoutSessionRequest, opts ...grpc.CallOption) (*paymentsv1.CreateCourseCheckoutSessionResponse, error) {
	s.lastCourseReq = in
	return s.courseResp, s.courseErr
}

func (s *stubPaymentServiceClient) CreateApplicationCheckoutSession(ctx context.Context, in *paymentsv1.CreateApplicationCheckoutSessionRequest, opts ...grpc.CallOption) (*paymentsv1.CreateApplicationCheckoutSessionResponse, error) {
	s.lastApplicationReq = in
	return s.applicationResp, s.applicationErr
}

// -----------------------------------------------------------------------------
// CreateCourseCheckoutSession — happy path
// -----------------------------------------------------------------------------

func TestClient_CreateCourseCheckoutSession_HappyPath(t *testing.T) {
	t.Parallel()
	stub := &stubPaymentServiceClient{
		courseResp: &paymentsv1.CreateCourseCheckoutSessionResponse{
			PurchaseId:        "purchase-123",
			StripeSessionId:   "cs_test_abc",
			StripeCheckoutUrl: "https://checkout.stripe.com/c/cs_test_abc",
			State:             paymentsv1.PurchaseState_PURCHASE_STATE_CHECKOUT_STARTED,
		},
	}
	c := NewClient(stub)

	out, err := c.CreateCourseCheckoutSession(context.Background(), CreateCourseCheckoutInput{
		TenantID:    "tenant-1",
		LearnerGCID: "gcid-1",
		CourseID:    "course-1",
		AmountCents: 19900,
		Currency:    "SGD",
		SuccessURL:  "https://chora.site/a/courses/course-1/enrolled",
		CancelURL:   "https://chora.site/a/courses/course-1",
	})
	if err != nil {
		t.Fatalf("CreateCourseCheckoutSession returned error: %v", err)
	}
	if out.PurchaseID != "purchase-123" {
		t.Errorf("PurchaseID = %q, want purchase-123", out.PurchaseID)
	}
	if out.StripeSessionID != "cs_test_abc" {
		t.Errorf("StripeSessionID = %q, want cs_test_abc", out.StripeSessionID)
	}
	if out.StripeCheckoutURL != "https://checkout.stripe.com/c/cs_test_abc" {
		t.Errorf("StripeCheckoutURL = %q, want stripe checkout url", out.StripeCheckoutURL)
	}
	if out.State != "CHECKOUT_STARTED" {
		t.Errorf("State = %q, want CHECKOUT_STARTED", out.State)
	}

	// Verify request mapping into the wire proto.
	if stub.lastCourseReq.GetTenantId() != "tenant-1" {
		t.Errorf("wire TenantId = %q, want tenant-1", stub.lastCourseReq.GetTenantId())
	}
	if stub.lastCourseReq.GetLearnerGcid() != "gcid-1" {
		t.Errorf("wire LearnerGcid = %q, want gcid-1", stub.lastCourseReq.GetLearnerGcid())
	}
	if stub.lastCourseReq.GetCourseId() != "course-1" {
		t.Errorf("wire CourseId = %q, want course-1", stub.lastCourseReq.GetCourseId())
	}
	if stub.lastCourseReq.GetAmountCents() != 19900 {
		t.Errorf("wire AmountCents = %d, want 19900", stub.lastCourseReq.GetAmountCents())
	}
	if stub.lastCourseReq.GetCurrency() != "SGD" {
		t.Errorf("wire Currency = %q, want SGD", stub.lastCourseReq.GetCurrency())
	}
	if stub.lastCourseReq.GetSuccessUrl() == "" {
		t.Error("wire SuccessUrl is empty")
	}
	if stub.lastCourseReq.GetCancelUrl() == "" {
		t.Error("wire CancelUrl is empty")
	}
	if stub.lastCourseReq.GetIdempotencyKey() == "" {
		t.Error("wire IdempotencyKey is empty — caller must produce one")
	}
}

// -----------------------------------------------------------------------------
// CreateCourseCheckoutSession — input validation
// -----------------------------------------------------------------------------

func TestClient_CreateCourseCheckoutSession_InputValidation(t *testing.T) {
	t.Parallel()
	base := CreateCourseCheckoutInput{
		TenantID:    "tenant-1",
		LearnerGCID: "gcid-1",
		CourseID:    "course-1",
		AmountCents: 100,
		Currency:    "SGD",
		SuccessURL:  "https://chora.site/ok",
		CancelURL:   "https://chora.site/cancel",
	}
	cases := []struct {
		name    string
		mutate  func(*CreateCourseCheckoutInput)
		wantSub string
	}{
		{"missing tenant", func(in *CreateCourseCheckoutInput) { in.TenantID = "" }, "tenant_id"},
		{"missing gcid", func(in *CreateCourseCheckoutInput) { in.LearnerGCID = "" }, "learner_gcid"},
		{"missing course", func(in *CreateCourseCheckoutInput) { in.CourseID = "" }, "course_id"},
		{"missing currency", func(in *CreateCourseCheckoutInput) { in.Currency = "" }, "currency"},
		{"zero amount", func(in *CreateCourseCheckoutInput) { in.AmountCents = 0 }, "amount_cents"},
		{"negative amount", func(in *CreateCourseCheckoutInput) { in.AmountCents = -1 }, "amount_cents"},
		{"missing success URL", func(in *CreateCourseCheckoutInput) { in.SuccessURL = "" }, "success_url"},
		{"missing cancel URL", func(in *CreateCourseCheckoutInput) { in.CancelURL = "" }, "cancel_url"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := base
			tc.mutate(&in)
			c := NewClient(&stubPaymentServiceClient{})
			_, err := c.CreateCourseCheckoutSession(context.Background(), in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// CreateCourseCheckoutSession — gRPC error propagation
// -----------------------------------------------------------------------------

func TestClient_CreateCourseCheckoutSession_RPCError(t *testing.T) {
	t.Parallel()
	stub := &stubPaymentServiceClient{
		courseErr: status.Error(codes.Internal, "boom"),
	}
	c := NewClient(stub)
	_, err := c.CreateCourseCheckoutSession(context.Background(), CreateCourseCheckoutInput{
		TenantID: "t", LearnerGCID: "g", CourseID: "c", AmountCents: 1, Currency: "SGD",
		SuccessURL: "u", CancelURL: "u",
	})
	if err == nil {
		t.Fatal("expected gRPC error to propagate")
	}
}

// -----------------------------------------------------------------------------
// CreateApplicationCheckoutSession — happy path
// -----------------------------------------------------------------------------

func TestClient_CreateApplicationCheckoutSession_HappyPath(t *testing.T) {
	t.Parallel()
	stub := &stubPaymentServiceClient{
		applicationResp: &paymentsv1.CreateApplicationCheckoutSessionResponse{
			PurchaseId:        "app-purchase-1",
			StripeSessionId:   "cs_app_1",
			StripeCheckoutUrl: "https://checkout.stripe.com/c/cs_app_1",
			State:             paymentsv1.PurchaseState_PURCHASE_STATE_CHECKOUT_STARTED,
		},
	}
	c := NewClient(stub)

	out, err := c.CreateApplicationCheckoutSession(context.Background(), CreateApplicationCheckoutInput{
		TenantID:      "tenant-1",
		LearnerGCID:   "gcid-1",
		ApplicationID: "app-1",
		CourseID:      "course-1",
		AmountCents:   100000,
		Currency:      "SGD",
		SuccessURL:    "https://chora.site/ok",
		CancelURL:     "https://chora.site/cancel",
	})
	if err != nil {
		t.Fatalf("CreateApplicationCheckoutSession returned error: %v", err)
	}
	if out.PurchaseID != "app-purchase-1" {
		t.Errorf("PurchaseID = %q, want app-purchase-1", out.PurchaseID)
	}
	if out.StripeSessionID != "cs_app_1" {
		t.Errorf("StripeSessionID = %q, want cs_app_1", out.StripeSessionID)
	}
	if stub.lastApplicationReq.GetApplicationId() != "app-1" {
		t.Errorf("wire ApplicationId = %q, want app-1", stub.lastApplicationReq.GetApplicationId())
	}
	if stub.lastApplicationReq.GetIdempotencyKey() == "" {
		t.Error("wire IdempotencyKey is empty — caller must produce one")
	}
}

// -----------------------------------------------------------------------------
// CreateApplicationCheckoutSession — input validation
// -----------------------------------------------------------------------------

func TestClient_CreateApplicationCheckoutSession_InputValidation(t *testing.T) {
	t.Parallel()
	base := CreateApplicationCheckoutInput{
		TenantID:      "tenant-1",
		LearnerGCID:   "gcid-1",
		ApplicationID: "app-1",
		CourseID:      "course-1",
		AmountCents:   100,
		Currency:      "SGD",
		SuccessURL:    "https://chora.site/ok",
		CancelURL:     "https://chora.site/cancel",
	}
	cases := []struct {
		name    string
		mutate  func(*CreateApplicationCheckoutInput)
		wantSub string
	}{
		{"missing tenant", func(in *CreateApplicationCheckoutInput) { in.TenantID = "" }, "tenant_id"},
		{"missing gcid", func(in *CreateApplicationCheckoutInput) { in.LearnerGCID = "" }, "learner_gcid"},
		{"missing application", func(in *CreateApplicationCheckoutInput) { in.ApplicationID = "" }, "application_id"},
		{"missing course", func(in *CreateApplicationCheckoutInput) { in.CourseID = "" }, "course_id"},
		{"missing currency", func(in *CreateApplicationCheckoutInput) { in.Currency = "" }, "currency"},
		{"zero amount", func(in *CreateApplicationCheckoutInput) { in.AmountCents = 0 }, "amount_cents"},
		{"missing success URL", func(in *CreateApplicationCheckoutInput) { in.SuccessURL = "" }, "success_url"},
		{"missing cancel URL", func(in *CreateApplicationCheckoutInput) { in.CancelURL = "" }, "cancel_url"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := base
			tc.mutate(&in)
			c := NewClient(&stubPaymentServiceClient{})
			_, err := c.CreateApplicationCheckoutSession(context.Background(), in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Nil-receiver guard — adapter should fail loud, not panic.
// -----------------------------------------------------------------------------

func TestClient_NilRPCClient(t *testing.T) {
	t.Parallel()
	c := NewClient(nil)
	_, err := c.CreateCourseCheckoutSession(context.Background(), CreateCourseCheckoutInput{
		TenantID: "t", LearnerGCID: "g", CourseID: "c", AmountCents: 1, Currency: "SGD",
		SuccessURL: "u", CancelURL: "u",
	})
	if err == nil {
		t.Fatal("nil rpc client should produce an error")
	}
	if !errors.Is(err, ErrRPCClientNil) {
		t.Errorf("error %v should wrap ErrRPCClientNil", err)
	}
	_, err = c.CreateApplicationCheckoutSession(context.Background(), CreateApplicationCheckoutInput{
		TenantID: "t", LearnerGCID: "g", ApplicationID: "a", CourseID: "c", AmountCents: 1, Currency: "SGD",
		SuccessURL: "u", CancelURL: "u",
	})
	if err == nil {
		t.Fatal("nil rpc client should produce an error on application checkout too")
	}
	if !errors.Is(err, ErrRPCClientNil) {
		t.Errorf("error %v should wrap ErrRPCClientNil", err)
	}
}

// -----------------------------------------------------------------------------
// Caller-supplied idempotency keys preserved.
// -----------------------------------------------------------------------------

func TestClient_PreservesCallerIdempotencyKey(t *testing.T) {
	t.Parallel()
	stub := &stubPaymentServiceClient{
		courseResp:      &paymentsv1.CreateCourseCheckoutSessionResponse{PurchaseId: "p1", StripeSessionId: "s1", StripeCheckoutUrl: "u"},
		applicationResp: &paymentsv1.CreateApplicationCheckoutSessionResponse{PurchaseId: "p2", StripeSessionId: "s2", StripeCheckoutUrl: "u"},
	}
	c := NewClient(stub)
	if _, err := c.CreateCourseCheckoutSession(context.Background(), CreateCourseCheckoutInput{
		IdempotencyKey: "caller-supplied-course-key",
		TenantID:       "t", LearnerGCID: "g", CourseID: "c", AmountCents: 1, Currency: "SGD",
		SuccessURL: "u", CancelURL: "u",
	}); err != nil {
		t.Fatalf("course checkout returned error: %v", err)
	}
	if stub.lastCourseReq.GetIdempotencyKey() != "caller-supplied-course-key" {
		t.Errorf("course IdempotencyKey = %q, want caller-supplied-course-key", stub.lastCourseReq.GetIdempotencyKey())
	}

	if _, err := c.CreateApplicationCheckoutSession(context.Background(), CreateApplicationCheckoutInput{
		IdempotencyKey: "caller-supplied-app-key",
		TenantID:       "t", LearnerGCID: "g", ApplicationID: "a", CourseID: "c", AmountCents: 1, Currency: "SGD",
		SuccessURL: "u", CancelURL: "u",
	}); err != nil {
		t.Fatalf("application checkout returned error: %v", err)
	}
	if stub.lastApplicationReq.GetIdempotencyKey() != "caller-supplied-app-key" {
		t.Errorf("application IdempotencyKey = %q, want caller-supplied-app-key", stub.lastApplicationReq.GetIdempotencyKey())
	}
}
