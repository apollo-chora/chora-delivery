// testset_sourcejob_test.go — Lane 1c W4 (CHO-1703 / ADR-180 D10) domain
// tests for TestSet.SourceJobID.
//
// TDD RED phase: written FIRST against a TestSet aggregate that does not yet
// carry SourceJobID. Drives the batch→test-set assembly provenance shape per
// chora-contracts/openapi/delivery-test-sets.yaml v1.1.0:
//
//   - NewTestSetInput.SourceJobID optional — empty/whitespace ⇒ nil pointer
//     (hand-authored test set), non-empty ⇒ trimmed + recorded.
//   - TestSetListFilter gains SourceJobID (exact-match; "" ⇒ no filter).
package delivery_test

import (
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const sourceJobID = "01985e7f-0000-7000-8000-00000000aaaa"

func TestNewTestSet_SourceJobID_RecordedWhenSupplied(t *testing.T) {
	t.Parallel()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:    "11111111-1111-7111-8111-111111111111",
		AuthorGCID:  "00000000-0000-7000-8000-000000001999",
		Title:       "Batch-assembled test set",
		SourceJobID: "  " + sourceJobID + "  ", // whitespace must trim
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if ts.SourceJobID == nil {
		t.Fatal("SourceJobID: expected non-nil pointer when supplied")
	}
	if got := *ts.SourceJobID; got != sourceJobID {
		t.Fatalf("SourceJobID: got %q want %q (trimmed)", got, sourceJobID)
	}
	if ts.State != domain.TestSetStateDraft {
		t.Fatalf("state: got %s want DRAFT", ts.State)
	}
}

func TestNewTestSet_SourceJobID_NilWhenAbsent(t *testing.T) {
	t.Parallel()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:   "11111111-1111-7111-8111-111111111111",
		AuthorGCID: "00000000-0000-7000-8000-000000001999",
		Title:      "Hand-authored test set",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if ts.SourceJobID != nil {
		t.Fatalf("SourceJobID: expected nil for hand-authored; got %q", *ts.SourceJobID)
	}
}

func TestNewTestSet_SourceJobID_WhitespaceOnlyIsNil(t *testing.T) {
	t.Parallel()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:    "11111111-1111-7111-8111-111111111111",
		AuthorGCID:  "00000000-0000-7000-8000-000000001999",
		Title:       "T",
		SourceJobID: "   ",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if ts.SourceJobID != nil {
		t.Fatalf("SourceJobID: whitespace-only must normalise to nil; got %q", *ts.SourceJobID)
	}
}

func TestTestSetListFilter_CarriesSourceJobID(t *testing.T) {
	t.Parallel()
	// Compile-level contract: the cross-adapter filter shape carries the
	// Lane-1c exact-match dimension ("" ⇒ no filter applied).
	f := domain.TestSetListFilter{SourceJobID: sourceJobID}
	if f.SourceJobID != sourceJobID {
		t.Fatalf("filter SourceJobID round-trip: got %q", f.SourceJobID)
	}
}
