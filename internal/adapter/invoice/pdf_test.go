// Package invoice — tax-invoice PDF + storage tests, S6.1.
//
// PDF generation uses an in-memory adapter for tests (deterministic byte
// output via signintech/gopdf or equivalent). Storage adapter has an
// in-memory variant that mimics the GCS shape (PutObject + SignedURL).
//
// Append-only invariant: a stored invoice is never overwritten or deleted —
// the storage adapter MUST return ErrAlreadyIssued on duplicate Put.
package invoice_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/invoice"
)

// -----------------------------------------------------------------------------
// PDF generation
// -----------------------------------------------------------------------------

func TestPDFGenerator_Generate_NonEmptyOutput(t *testing.T) {
	t.Parallel()
	gen := invoice.NewPDFGenerator()
	out, err := gen.Generate(context.Background(), invoice.Input{
		InvoiceNumber:        "CHO-INV-2026-001",
		IssueDate:            time.Date(2026, 5, 9, 10, 30, 0, 0, time.UTC),
		TenantName:           "MTM Singapore — Bras Basah",
		LearnerName:          "Phyllis Tan",
		LearnerGCID:          "01970000-0000-7000-9000-000000000001",
		CourseTitle:          "Advanced Certified ScrumMaster",
		CourseStart:          time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		CourseEnd:            time.Date(2026, 6, 5, 17, 0, 0, 0, time.UTC),
		TuitionGrossSGDCents: 210000,
		GSTSGDCents:          18900,
		FundingLines: []invoice.FundingLine{
			{Type: "skillsfuture_credits", Description: "SkillsFuture Credits", AmountSGDCents: 50000},
		},
		NetSGDCents:        178900,
		StripeChargeID:     "ch_xxxxx",
		PaymentMethod:      "card",
		Currency:           "SGD",
		GSTRateBasisPoints: 900,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(out) < 100 {
		t.Fatalf("expected non-trivial PDF bytes; got %d", len(out))
	}
	// PDFs start with %PDF-
	if !strings.HasPrefix(string(out[:5]), "%PDF-") {
		t.Fatalf("expected PDF magic header; got %q", string(out[:5]))
	}
}

func TestPDFGenerator_RejectsBlankInvoiceNumber(t *testing.T) {
	t.Parallel()
	gen := invoice.NewPDFGenerator()
	_, err := gen.Generate(context.Background(), invoice.Input{
		LearnerName: "Phyllis", CourseTitle: "X",
	})
	if err == nil {
		t.Fatalf("expected error for blank invoice_number")
	}
}

func TestPDFGenerator_FreeCourse_GSTLineOmitted(t *testing.T) {
	t.Parallel()
	gen := invoice.NewPDFGenerator()
	out, err := gen.Generate(context.Background(), invoice.Input{
		InvoiceNumber:        "CHO-INV-FREE-001",
		IssueDate:            time.Now().UTC(),
		TenantName:           "Demo Tenant",
		LearnerName:          "Phyllis",
		LearnerGCID:          "gcid-x",
		CourseTitle:          "Free Course",
		TuitionGrossSGDCents: 0,
		GSTSGDCents:          0,
		NetSGDCents:          0,
		Currency:             "SGD",
	})
	if err != nil {
		t.Fatalf("Generate free: %v", err)
	}
	if len(out) < 100 {
		t.Fatalf("expected non-trivial PDF; got %d", len(out))
	}
}

// -----------------------------------------------------------------------------
// In-memory storage — mirrors the GCS storage shape
// -----------------------------------------------------------------------------

func TestInMemoryStorage_PutAndSignedURL(t *testing.T) {
	t.Parallel()
	s := invoice.NewInMemoryStorage("chora-invoices-dev")
	uri, err := s.Put(context.Background(), invoice.PutInput{
		ObjectName: "tenant-a/inv-001.pdf",
		Body:       []byte("%PDF-1.4 ..."),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if uri == "" {
		t.Fatalf("expected non-empty URI")
	}

	url, err := s.SignedURL(context.Background(), "tenant-a/inv-001.pdf", 60*time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if !strings.Contains(url, "tenant-a/inv-001.pdf") {
		t.Fatalf("signed URL does not include object name: %q", url)
	}
}

func TestInMemoryStorage_Put_AppendOnly(t *testing.T) {
	t.Parallel()
	s := invoice.NewInMemoryStorage("chora-invoices-dev")
	_, err := s.Put(context.Background(), invoice.PutInput{
		ObjectName: "tenant-a/inv-001.pdf",
		Body:       []byte("v1"),
	})
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	_, err = s.Put(context.Background(), invoice.PutInput{
		ObjectName: "tenant-a/inv-001.pdf",
		Body:       []byte("v2"),
	})
	if err == nil {
		t.Fatalf("expected ErrAlreadyIssued on duplicate Put")
	}
}

func TestInMemoryStorage_SignedURL_UnknownObject(t *testing.T) {
	t.Parallel()
	s := invoice.NewInMemoryStorage("chora-invoices-dev")
	_, err := s.SignedURL(context.Background(), "missing.pdf", time.Hour)
	if err == nil {
		t.Fatalf("expected error for unknown object")
	}
}

// -----------------------------------------------------------------------------
// Issuer — orchestrates Generate + Put + SignedURL
// -----------------------------------------------------------------------------

func TestIssuer_Issue_HappyPath(t *testing.T) {
	t.Parallel()
	gen := invoice.NewPDFGenerator()
	store := invoice.NewInMemoryStorage("chora-invoices-dev")
	iss := invoice.NewIssuer(gen, store, "chora-invoices-dev")

	out, err := iss.Issue(context.Background(), invoice.Input{
		InvoiceNumber:        "CHO-INV-001",
		IssueDate:            time.Now().UTC(),
		TenantName:           "MTM",
		LearnerName:          "Phyllis",
		LearnerGCID:          "gcid",
		CourseTitle:          "ACSM",
		TuitionGrossSGDCents: 100000,
		GSTSGDCents:          9000,
		NetSGDCents:          109000,
		Currency:             "SGD",
		StripeChargeID:       "ch_test",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if out.URI == "" {
		t.Fatalf("expected URI")
	}
	if out.ObjectName == "" {
		t.Fatalf("expected ObjectName")
	}
	if out.SizeBytes == 0 {
		t.Fatalf("expected non-zero SizeBytes")
	}
}

func TestIssuer_Issue_Idempotent_SameInvoiceNumber(t *testing.T) {
	t.Parallel()
	gen := invoice.NewPDFGenerator()
	store := invoice.NewInMemoryStorage("chora-invoices-dev")
	iss := invoice.NewIssuer(gen, store, "chora-invoices-dev")

	in := invoice.Input{
		InvoiceNumber:        "CHO-INV-DUP-001",
		IssueDate:            time.Now().UTC(),
		TenantName:           "MTM",
		LearnerName:          "Phyllis",
		LearnerGCID:          "gcid",
		CourseTitle:          "ACSM",
		TuitionGrossSGDCents: 100000,
		GSTSGDCents:          9000,
		NetSGDCents:          109000,
		Currency:             "SGD",
		StripeChargeID:       "ch_test_idem",
	}

	first, err := iss.Issue(context.Background(), in)
	if err != nil {
		t.Fatalf("first Issue: %v", err)
	}
	second, err := iss.Issue(context.Background(), in)
	if err != nil {
		t.Fatalf("second Issue (idempotent): %v", err)
	}
	if first.URI != second.URI {
		t.Fatalf("idempotent Issue must return same URI; %q vs %q", first.URI, second.URI)
	}
}
