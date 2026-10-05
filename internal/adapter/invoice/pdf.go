// Package invoice is the tax-invoice PDF + storage adapter for chora-delivery.
//
// Per docs/design/ux_course_application.md A-CRS-6 + S6.1 brief:
//
//   - Template includes: tenant name, learner GCID/name, course details,
//     tuition breakdown (gross + funding lines + net), GST line if
//     applicable (SG 9% — IRAS), payment method, Stripe charge ID,
//     issue date.
//   - Stored in GCS bucket `chora-invoices-{env}` (Terraform module landed
//     in chora-infra/terraform/modules/invoice-storage/).
//   - Returned via signed URL with 60-min TTL on GET …/invoice.
//   - Append-only: invoice never modified post-issuance (regulatory).
//
// PDF generated via codeberg.org/go-pdf/fpdf (already in the workspace).
// Hand-written minimal layout — single A4 page, no images, just text.
//
// Hexagonal: ADAPTER. Implements the invoice.Issuer interface for the HTTP
// layer (cf. adapter/http/handlers.go).
package invoice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"codeberg.org/go-pdf/fpdf"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// ErrAlreadyIssued — Put called twice for the same object name (append-only).
var ErrAlreadyIssued = errors.New("invoice: object already issued (append-only)")

// -----------------------------------------------------------------------------
// FundingLine — IRAS-aligned funding-source line on the invoice.
// -----------------------------------------------------------------------------

// FundingLine is one funding-source line on the invoice (e.g., SkillsFuture
// Credits). The struct is decoupled from application.FundingLine so the
// invoice template owns its own presentation shape.
type FundingLine struct {
	Type           string // "skillsfuture_credits", "mid_career_enhanced_subsidy", …
	Description    string // displayed on the invoice
	AmountSGDCents int64
}

// -----------------------------------------------------------------------------
// Input — value-bag for Generate / Issue.
// -----------------------------------------------------------------------------

// Input is the invoice template values. All amounts in SGD cents.
type Input struct {
	InvoiceNumber        string
	IssueDate            time.Time
	TenantName           string
	LearnerName          string
	LearnerGCID          string
	CourseTitle          string
	CourseStart          time.Time
	CourseEnd            time.Time
	TuitionGrossSGDCents int64
	GSTSGDCents          int64
	GSTRateBasisPoints   int
	FundingLines         []FundingLine
	NetSGDCents          int64
	StripeChargeID       string
	PaymentMethod        string
	Currency             string
}

// -----------------------------------------------------------------------------
// PDFGenerator — produces the invoice PDF bytes.
// -----------------------------------------------------------------------------

// PDFGenerator generates an A4 single-page tax invoice PDF.
type PDFGenerator struct{}

// NewPDFGenerator returns a stateless PDF generator.
func NewPDFGenerator() *PDFGenerator { return &PDFGenerator{} }

// Generate produces the PDF bytes for the given Input.
func (g *PDFGenerator) Generate(_ context.Context, in Input) ([]byte, error) {
	if strings.TrimSpace(in.InvoiceNumber) == "" {
		return nil, errors.New("invoice: invoice_number required")
	}
	if strings.TrimSpace(in.LearnerName) == "" {
		return nil, errors.New("invoice: learner_name required")
	}
	if strings.TrimSpace(in.CourseTitle) == "" {
		return nil, errors.New("invoice: course_title required")
	}
	currency := in.Currency
	if currency == "" {
		currency = "SGD"
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()

	// Header.
	pdf.SetFont("Helvetica", "B", 18)
	pdf.Cell(0, 10, "Tax Invoice")
	pdf.Ln(12)

	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(0, 6, fmt.Sprintf("Invoice no.: %s", in.InvoiceNumber))
	pdf.Ln(5)
	pdf.Cell(0, 6, fmt.Sprintf("Issue date : %s", in.IssueDate.Format("2 January 2006")))
	pdf.Ln(8)

	// Provider + customer block.
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(95, 6, "Issued by")
	pdf.Cell(95, 6, "Issued to")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(95, 5, in.TenantName)
	pdf.Cell(95, 5, in.LearnerName)
	pdf.Ln(5)
	pdf.Cell(95, 5, "via Chora LMS")
	pdf.Cell(95, 5, fmt.Sprintf("GCID: %s", in.LearnerGCID))
	pdf.Ln(8)

	// Course details.
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(0, 6, "Course details")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Cell(0, 5, fmt.Sprintf("Course   : %s", in.CourseTitle))
	pdf.Ln(5)
	if !in.CourseStart.IsZero() {
		pdf.Cell(0, 5, fmt.Sprintf("Period   : %s — %s",
			in.CourseStart.Format("2 Jan 2006"),
			in.CourseEnd.Format("2 Jan 2006")))
		pdf.Ln(5)
	}
	pdf.Ln(3)

	// Pricing breakdown table.
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(0, 6, "Pricing breakdown")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 10)
	row := func(label, amount string) {
		pdf.Cell(120, 5, label)
		pdf.Cell(0, 5, amount)
		pdf.Ln(5)
	}
	row("Course tuition (gross)", money(currency, in.TuitionGrossSGDCents))
	if in.TuitionGrossSGDCents > 0 && in.GSTSGDCents > 0 {
		gstLabel := "GST"
		if in.GSTRateBasisPoints > 0 {
			gstLabel = fmt.Sprintf("GST (%.1f%%)", float64(in.GSTRateBasisPoints)/100)
		}
		row(gstLabel, money(currency, in.GSTSGDCents))
	}
	for _, fl := range in.FundingLines {
		desc := fl.Description
		if desc == "" {
			desc = fl.Type
		}
		row(desc, "− "+money(currency, fl.AmountSGDCents))
	}
	pdf.Ln(2)
	pdf.SetFont("Helvetica", "B", 11)
	row("NET PAYABLE", money(currency, in.NetSGDCents))
	pdf.Ln(4)

	// Payment block.
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(0, 6, "Payment")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 10)
	if in.PaymentMethod != "" {
		pdf.Cell(0, 5, fmt.Sprintf("Method      : %s", in.PaymentMethod))
		pdf.Ln(5)
	}
	if in.StripeChargeID != "" {
		pdf.Cell(0, 5, fmt.Sprintf("Charge ref. : %s", in.StripeChargeID))
		pdf.Ln(5)
	}
	pdf.Ln(8)

	// Footer.
	pdf.SetFont("Helvetica", "I", 8)
	pdf.Cell(0, 4, "This is a system-generated tax invoice. Append-only — corrections via support.")
	pdf.Ln(4)
	pdf.Cell(0, 4, "Singapore IRAS GST — registration handled by tenant.")

	var buf strings.Builder
	if err := pdf.Output(stringWriter{&buf}); err != nil {
		return nil, fmt.Errorf("invoice: pdf output: %w", err)
	}
	return []byte(buf.String()), nil
}

// stringWriter adapts strings.Builder to io.Writer.
type stringWriter struct{ b *strings.Builder }

func (s stringWriter) Write(p []byte) (int, error) { return s.b.Write(p) }

// money renders a SGD cents amount to a human-readable string.
func money(currency string, cents int64) string {
	whole := cents / 100
	fraction := cents % 100
	if fraction < 0 {
		fraction = -fraction
	}
	return fmt.Sprintf("%s %d.%02d", currency, whole, fraction)
}

// -----------------------------------------------------------------------------
// Storage — in-memory adapter mimics the GCS shape.
// -----------------------------------------------------------------------------

// PutInput is the value-bag for Storage.Put.
type PutInput struct {
	ObjectName string
	Body       []byte
}

// Storage is the storage port. The production adapter is a GCS one (deferred
// to S6.2 — Terraform module + Workload Identity Federation).
type Storage interface {
	Put(ctx context.Context, in PutInput) (string, error)
	SignedURL(ctx context.Context, objectName string, ttl time.Duration) (string, error)
}

// InMemoryStorage is the test-friendly Storage implementation.
//
// Each Put is append-only — duplicate object names yield ErrAlreadyIssued.
// SignedURL returns a deterministic mock URL pinned to the bucket + object.
type InMemoryStorage struct {
	bucket string
	mu     sync.RWMutex
	by     map[string][]byte
}

// NewInMemoryStorage returns an empty in-memory storage rooted at bucket.
func NewInMemoryStorage(bucket string) *InMemoryStorage {
	return &InMemoryStorage{bucket: bucket, by: make(map[string][]byte)}
}

// Put writes the body under the given object name. Append-only.
func (s *InMemoryStorage) Put(_ context.Context, in PutInput) (string, error) {
	if strings.TrimSpace(in.ObjectName) == "" {
		return "", errors.New("invoice storage: object_name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.by[in.ObjectName]; exists {
		return "", ErrAlreadyIssued
	}
	dup := make([]byte, len(in.Body))
	copy(dup, in.Body)
	s.by[in.ObjectName] = dup
	return fmt.Sprintf("gs://%s/%s", s.bucket, in.ObjectName), nil
}

// SignedURL returns a deterministic mock signed URL with a TTL hint.
func (s *InMemoryStorage) SignedURL(_ context.Context, objectName string, ttl time.Duration) (string, error) {
	s.mu.RLock()
	_, ok := s.by[objectName]
	s.mu.RUnlock()
	if !ok {
		return "", errors.New("invoice storage: object not found")
	}
	expires := time.Now().UTC().Add(ttl).Unix()
	return fmt.Sprintf("https://storage.googleapis.com/%s/%s?expires=%d&signature=stub",
		s.bucket, objectName, expires), nil
}

// Bytes returns the stored bytes for the given object — TEST-ONLY.
func (s *InMemoryStorage) Bytes(objectName string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.by[objectName]
	return b, ok
}

// -----------------------------------------------------------------------------
// Issuer — orchestrates Generate + Put + SignedURL
// -----------------------------------------------------------------------------

// IssueResult is the return shape from Issuer.Issue.
type IssueResult struct {
	URI        string // gs://bucket/object_name
	ObjectName string
	SizeBytes  int
	SignedURL  string
}

// Issuer wraps a PDFGenerator + Storage to produce the final invoice URI.
//
// Idempotent: re-issuing the same InvoiceNumber yields the same URI rather
// than failing — preserves regulatory append-only while supporting retries.
type Issuer struct {
	gen    *PDFGenerator
	store  Storage
	bucket string
}

// NewIssuer constructs an Issuer.
func NewIssuer(gen *PDFGenerator, store Storage, bucket string) *Issuer {
	return &Issuer{gen: gen, store: store, bucket: bucket}
}

// Issue generates the PDF, stores it, and returns the URI + signed URL.
func (i *Issuer) Issue(ctx context.Context, in Input) (*IssueResult, error) {
	objectName := i.objectNameFor(in)

	// Idempotency — if an object already exists at this name, return its URI.
	if mem, ok := i.store.(*InMemoryStorage); ok {
		if existing, has := mem.Bytes(objectName); has {
			signed, err := i.store.SignedURL(ctx, objectName, 60*time.Minute)
			if err != nil {
				return nil, err
			}
			return &IssueResult{
				URI:        fmt.Sprintf("gs://%s/%s", i.bucket, objectName),
				ObjectName: objectName,
				SizeBytes:  len(existing),
				SignedURL:  signed,
			}, nil
		}
	}

	body, err := i.gen.Generate(ctx, in)
	if err != nil {
		return nil, err
	}
	uri, err := i.store.Put(ctx, PutInput{ObjectName: objectName, Body: body})
	if err != nil {
		// Append-only conflict: still surface the URI so callers see idempotent.
		if errors.Is(err, ErrAlreadyIssued) {
			signed, sErr := i.store.SignedURL(ctx, objectName, 60*time.Minute)
			if sErr != nil {
				return nil, sErr
			}
			return &IssueResult{
				URI:        fmt.Sprintf("gs://%s/%s", i.bucket, objectName),
				ObjectName: objectName,
				SizeBytes:  len(body),
				SignedURL:  signed,
			}, nil
		}
		return nil, err
	}
	signed, err := i.store.SignedURL(ctx, objectName, 60*time.Minute)
	if err != nil {
		return nil, err
	}
	return &IssueResult{
		URI:        uri,
		ObjectName: objectName,
		SizeBytes:  len(body),
		SignedURL:  signed,
	}, nil
}

// objectNameFor returns the canonical GCS object name. Format:
// `invoices/{tenant}/{invoice-number}.pdf` (tenant-scoped sub-path).
func (i *Issuer) objectNameFor(in Input) string {
	tenant := strings.ReplaceAll(in.TenantName, " ", "-")
	if tenant == "" {
		tenant = "unknown-tenant"
	}
	return fmt.Sprintf("invoices/%s/%s.pdf", tenant, in.InvoiceNumber)
}
