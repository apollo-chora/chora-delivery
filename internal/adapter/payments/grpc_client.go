// Package payments — outbound gRPC client adapter for the canonical
// chora-payments PaymentService (per ADR-164 PROPOSED 2026-05-24).
//
// chora-delivery is one of the originating services that ADR-164 cuts over
// from inline Stripe-SDK calls to the central chora-payments domain. This
// adapter is the synchronous WRITE path: chora-delivery handlers mint a
// Stripe Checkout Session by calling chora-payments via gRPC. The async
// outcome (checkout.session.completed → enrolment provisioning) arrives
// later via Pub/Sub — see payments_subscriber.go in the events package.
//
// Hexagonal: ADAPTER. Domain code never imports this package — handlers
// hold a typed *Client and pass it to the relevant call site (the CJ#2
// course checkout handler + the course application accept-offer flow).
//
// Cross-DB queries FORBIDDEN per ddd-enforcement #3 — chora-delivery
// cannot read chora_payments tables directly. All payment-correlation
// state in chora-payments is materialised back into chora_delivery via
// events. This sync gRPC is the sanctioned synchronous write channel.
//
// Per `feedback_no_inline_config`: the upstream URL is sourced from env
// (`CHORA_PAYMENTS_GRPC_ADDR`, default `payments:9090` in the mesh).
// Construction happens in cmd/server/main.go; this file is config-free.
//
// Per `feedback_no_stubs_real_wiring` — input validation rejects empty
// required fields up-front so that a misconfigured caller fails loud
// rather than silently hitting chora-payments with garbage.
package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc"

	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/payments/v1"
)

// ErrRPCClientNil — the underlying paymentsv1.PaymentServiceClient is nil.
// Surfaced as a sentinel so callers can `errors.Is` distinguish a wiring
// error from an upstream transport failure.
var ErrRPCClientNil = errors.New("payments grpc: rpc client nil")

// -----------------------------------------------------------------------------
// Port — minimal slice of paymentsv1.PaymentServiceClient used by this adapter.
// Tests inject a stub; production wires the real grpc.NewClient dial.
// -----------------------------------------------------------------------------

// PaymentServiceGRPCClient is the minimal slice of
// paymentsv1.PaymentServiceClient that chora-delivery calls.
type PaymentServiceGRPCClient interface {
	CreateCourseCheckoutSession(ctx context.Context, in *paymentsv1.CreateCourseCheckoutSessionRequest, opts ...grpc.CallOption) (*paymentsv1.CreateCourseCheckoutSessionResponse, error)
	CreateApplicationCheckoutSession(ctx context.Context, in *paymentsv1.CreateApplicationCheckoutSessionRequest, opts ...grpc.CallOption) (*paymentsv1.CreateApplicationCheckoutSessionResponse, error)
}

// -----------------------------------------------------------------------------
// Input + output value types — adapter-local, decoupled from the wire proto.
// Keeps domain + handler code from leaking paymentsv1 types upward.
// -----------------------------------------------------------------------------

// CreateCourseCheckoutInput is the value-bag for CreateCourseCheckoutSession.
//
// IdempotencyKey is optional — when empty the adapter mints a deterministic
// key from (course_id, learner_gcid) so a double-clicked Enrol button
// within Stripe's 24h idempotency window returns the same Session.
type CreateCourseCheckoutInput struct {
	IdempotencyKey string
	TenantID       string
	LearnerGCID    string
	CourseID       string
	AmountCents    int64
	Currency       string // ISO-4217, e.g. "SGD"
	SuccessURL     string
	CancelURL      string
}

// CreateCourseCheckoutOutput mirrors paymentsv1.CreateCourseCheckoutSessionResponse
// but flattens the enum onto a stable string so handlers never import paymentsv1.
type CreateCourseCheckoutOutput struct {
	PurchaseID        string
	StripeSessionID   string
	StripeCheckoutURL string
	State             string // canonical PurchaseState name, e.g. "CHECKOUT_STARTED"
}

// CreateApplicationCheckoutInput is the value-bag for CreateApplicationCheckoutSession.
type CreateApplicationCheckoutInput struct {
	IdempotencyKey string
	TenantID       string
	LearnerGCID    string
	ApplicationID  string
	CourseID       string
	AmountCents    int64
	Currency       string
	SuccessURL     string
	CancelURL      string
}

// CreateApplicationCheckoutOutput mirrors paymentsv1.CreateApplicationCheckoutSessionResponse.
type CreateApplicationCheckoutOutput struct {
	PurchaseID        string
	StripeSessionID   string
	StripeCheckoutURL string
	State             string
}

// -----------------------------------------------------------------------------
// Client — the adapter.
// -----------------------------------------------------------------------------

// Client is the chora-delivery → chora-payments gRPC adapter.
type Client struct {
	rpc PaymentServiceGRPCClient
}

// NewClient constructs a Client from a PaymentServiceGRPCClient.
// The caller owns the underlying gRPC conn lifecycle.
func NewClient(rpc PaymentServiceGRPCClient) *Client {
	return &Client{rpc: rpc}
}

// -----------------------------------------------------------------------------
// CreateCourseCheckoutSession — CJ#2 paid course enrolment flow.
// -----------------------------------------------------------------------------

// CreateCourseCheckoutSession mints a Stripe Checkout Session for the CJ#2
// paid-course flow. Returns the hosted Checkout URL the FE redirects to
// via window.location, plus the canonical Chora purchase_id chora-payments
// records in chora_payments.course_purchases.
//
// Idempotency: the adapter forwards a deterministic key derived from
// (course_id, learner_gcid) when the caller leaves it blank, so a
// double-clicked Enrol button surfaces the same Session.
func (c *Client) CreateCourseCheckoutSession(ctx context.Context, in CreateCourseCheckoutInput) (CreateCourseCheckoutOutput, error) {
	if c == nil || c.rpc == nil {
		return CreateCourseCheckoutOutput{}, ErrRPCClientNil
	}
	if err := validateCourseInput(in); err != nil {
		return CreateCourseCheckoutOutput{}, err
	}
	idem := strings.TrimSpace(in.IdempotencyKey)
	if idem == "" {
		idem = fmt.Sprintf("course-checkout-%s-%s", in.CourseID, in.LearnerGCID)
	}
	resp, err := c.rpc.CreateCourseCheckoutSession(ctx, &paymentsv1.CreateCourseCheckoutSessionRequest{
		IdempotencyKey: idem,
		TenantId:       in.TenantID,
		LearnerGcid:    in.LearnerGCID,
		CourseId:       in.CourseID,
		AmountCents:    in.AmountCents,
		Currency:       strings.ToUpper(strings.TrimSpace(in.Currency)),
		SuccessUrl:     in.SuccessURL,
		CancelUrl:      in.CancelURL,
	})
	if err != nil {
		return CreateCourseCheckoutOutput{}, fmt.Errorf("payments grpc: create course checkout: %w", err)
	}
	if resp == nil {
		return CreateCourseCheckoutOutput{}, errors.New("payments grpc: nil course checkout response")
	}
	return CreateCourseCheckoutOutput{
		PurchaseID:        resp.GetPurchaseId(),
		StripeSessionID:   resp.GetStripeSessionId(),
		StripeCheckoutURL: resp.GetStripeCheckoutUrl(),
		State:             purchaseStateString(resp.GetState()),
	}, nil
}

// -----------------------------------------------------------------------------
// CreateApplicationCheckoutSession — course-application accept-offer flow.
// -----------------------------------------------------------------------------

// CreateApplicationCheckoutSession mints a Stripe Checkout Session for the
// Course Application accept-offer flow. Returns the hosted Checkout URL the
// FE redirects to. Distinct from the CJ#2 path because the originating
// aggregate is the Application (not the Course) and the saga step is
// `Accepted → Paid` driven by chora.payments.application_payment.payment_captured.v1.
func (c *Client) CreateApplicationCheckoutSession(ctx context.Context, in CreateApplicationCheckoutInput) (CreateApplicationCheckoutOutput, error) {
	if c == nil || c.rpc == nil {
		return CreateApplicationCheckoutOutput{}, ErrRPCClientNil
	}
	if err := validateApplicationInput(in); err != nil {
		return CreateApplicationCheckoutOutput{}, err
	}
	idem := strings.TrimSpace(in.IdempotencyKey)
	if idem == "" {
		idem = fmt.Sprintf("application-checkout-%s-%s", in.ApplicationID, in.LearnerGCID)
	}
	resp, err := c.rpc.CreateApplicationCheckoutSession(ctx, &paymentsv1.CreateApplicationCheckoutSessionRequest{
		IdempotencyKey: idem,
		TenantId:       in.TenantID,
		LearnerGcid:    in.LearnerGCID,
		ApplicationId:  in.ApplicationID,
		CourseId:       in.CourseID,
		AmountCents:    in.AmountCents,
		Currency:       strings.ToUpper(strings.TrimSpace(in.Currency)),
		SuccessUrl:     in.SuccessURL,
		CancelUrl:      in.CancelURL,
	})
	if err != nil {
		return CreateApplicationCheckoutOutput{}, fmt.Errorf("payments grpc: create application checkout: %w", err)
	}
	if resp == nil {
		return CreateApplicationCheckoutOutput{}, errors.New("payments grpc: nil application checkout response")
	}
	return CreateApplicationCheckoutOutput{
		PurchaseID:        resp.GetPurchaseId(),
		StripeSessionID:   resp.GetStripeSessionId(),
		StripeCheckoutURL: resp.GetStripeCheckoutUrl(),
		State:             purchaseStateString(resp.GetState()),
	}, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func validateCourseInput(in CreateCourseCheckoutInput) error {
	if strings.TrimSpace(in.TenantID) == "" {
		return errors.New("payments grpc: tenant_id required")
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return errors.New("payments grpc: learner_gcid required")
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return errors.New("payments grpc: course_id required")
	}
	if in.AmountCents <= 0 {
		return fmt.Errorf("payments grpc: amount_cents must be > 0; got %d", in.AmountCents)
	}
	if strings.TrimSpace(in.Currency) == "" {
		return errors.New("payments grpc: currency required")
	}
	if strings.TrimSpace(in.SuccessURL) == "" {
		return errors.New("payments grpc: success_url required")
	}
	if strings.TrimSpace(in.CancelURL) == "" {
		return errors.New("payments grpc: cancel_url required")
	}
	return nil
}

func validateApplicationInput(in CreateApplicationCheckoutInput) error {
	if strings.TrimSpace(in.TenantID) == "" {
		return errors.New("payments grpc: tenant_id required")
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return errors.New("payments grpc: learner_gcid required")
	}
	if strings.TrimSpace(in.ApplicationID) == "" {
		return errors.New("payments grpc: application_id required")
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return errors.New("payments grpc: course_id required")
	}
	if in.AmountCents <= 0 {
		return fmt.Errorf("payments grpc: amount_cents must be > 0; got %d", in.AmountCents)
	}
	if strings.TrimSpace(in.Currency) == "" {
		return errors.New("payments grpc: currency required")
	}
	if strings.TrimSpace(in.SuccessURL) == "" {
		return errors.New("payments grpc: success_url required")
	}
	if strings.TrimSpace(in.CancelURL) == "" {
		return errors.New("payments grpc: cancel_url required")
	}
	return nil
}

// purchaseStateString maps the wire enum onto a stable canonical name —
// kept here so handlers + tests can switch on a string without importing
// paymentsv1.
func purchaseStateString(s paymentsv1.PurchaseState) string {
	switch s {
	case paymentsv1.PurchaseState_PURCHASE_STATE_CHECKOUT_STARTED:
		return "CHECKOUT_STARTED"
	case paymentsv1.PurchaseState_PURCHASE_STATE_PAYMENT_CAPTURED:
		return "PAYMENT_CAPTURED"
	case paymentsv1.PurchaseState_PURCHASE_STATE_PAYMENT_FAILED:
		return "PAYMENT_FAILED"
	case paymentsv1.PurchaseState_PURCHASE_STATE_REFUNDED:
		return "REFUNDED"
	case paymentsv1.PurchaseState_PURCHASE_STATE_EXPIRED:
		return "EXPIRED"
	default:
		return "UNSPECIFIED"
	}
}
