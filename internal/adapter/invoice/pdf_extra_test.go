// pdf_extra_test.go — internal (package invoice) top-up tests for the
// Issuer/Money internals not reachable from the external test package:
// the negative-fraction money branch, objectNameFor tenant normalization,
// the ErrAlreadyIssued idempotent Put path, and the SignedURL failure paths.
package invoice

import (
	"context"
	"errors"
	"testing"
	"time"
)

// conflictStorage returns ErrAlreadyIssued from Put but is NOT
// *InMemoryStorage — forces Issue down the append-only-conflict branch
// (the InMemoryStorage short-circuit is exercised in pdf_test.go).
type conflictStorage struct{ signed string }

func (c conflictStorage) Put(context.Context, PutInput) (string, error) { return "", ErrAlreadyIssued }
func (c conflictStorage) SignedURL(context.Context, string, time.Duration) (string, error) {
	if c.signed == "" {
		return "", errors.New("signed url unavailable")
	}
	return c.signed, nil
}

// failSignedStorage Put succeeds but SignedURL always fails.
type failSignedStorage struct{ uri string }

func (f failSignedStorage) Put(context.Context, PutInput) (string, error) { return f.uri, nil }
func (f failSignedStorage) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("gcs signed url unavailable")
}

func sampleInput() Input {
	return Input{
		InvoiceNumber:        "CHO-INV-X-001",
		IssueDate:            time.Now().UTC(),
		TenantName:           "MTM",
		LearnerName:          "Phyllis",
		LearnerGCID:          "gcid",
		CourseTitle:          "ACSM",
		TuitionGrossSGDCents: 100000,
		GSTSGDCents:          9000,
		NetSGDCents:          109000,
		Currency:             "SGD",
		StripeChargeID:       "ch_x",
	}
}

func TestMoney_NegativeFractionNormalized(t *testing.T) {
	t.Parallel()
	if got := money("SGD", -1005); got != "SGD -10.05" {
		t.Fatalf("money(SGD, -1005) = %q, want %q", got, "SGD -10.05")
	}
	if got := money("SGD", 100500); got != "SGD 1005.00" {
		t.Fatalf("money(SGD, 100500) = %q, want %q", got, "SGD 1005.00")
	}
}

func TestIssuer_ObjectNameFor_TenantNormalization(t *testing.T) {
	t.Parallel()
	iss := NewIssuer(NewPDFGenerator(), NewInMemoryStorage("bkt"), "bkt")

	if got := iss.objectNameFor(Input{InvoiceNumber: "N1", TenantName: "ACME Institute"}); got != "invoices/ACME-Institute/N1.pdf" {
		t.Fatalf("spaces: %q", got)
	}
	if got := iss.objectNameFor(Input{InvoiceNumber: "N2"}); got != "invoices/unknown-tenant/N2.pdf" {
		t.Fatalf("blank tenant: %q", got)
	}
}

func TestIssuer_Issue_ErrAlreadyIssued_Idempotent(t *testing.T) {
	t.Parallel()
	iss := NewIssuer(NewPDFGenerator(), conflictStorage{signed: "https://signed.example/N.pdf"}, "bkt")

	out, err := iss.Issue(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Issue (ErrAlreadyIssued path): %v", err)
	}
	if out.URI != "gs://bkt/invoices/MTM/CHO-INV-X-001.pdf" {
		t.Fatalf("URI = %q", out.URI)
	}
	if out.SignedURL != "https://signed.example/N.pdf" {
		t.Fatalf("SignedURL = %q", out.SignedURL)
	}
	if out.SizeBytes == 0 {
		t.Fatal("SizeBytes = 0, want the generated body length")
	}
}

func TestIssuer_Issue_ErrAlreadyIssued_SignedURLFailure(t *testing.T) {
	t.Parallel()
	iss := NewIssuer(NewPDFGenerator(), conflictStorage{}, "bkt")
	if _, err := iss.Issue(context.Background(), sampleInput()); err == nil {
		t.Fatal("SignedURL failure in ErrAlreadyIssued path: want error")
	}
}

func TestIssuer_Issue_SignedURLFailure(t *testing.T) {
	t.Parallel()
	iss := NewIssuer(NewPDFGenerator(), failSignedStorage{uri: "gs://bkt/invoices/MTM/CHO-INV-X-001.pdf"}, "bkt")
	if _, err := iss.Issue(context.Background(), sampleInput()); err == nil {
		t.Fatal("SignedURL failure at tail of happy path: want error")
	}
}
