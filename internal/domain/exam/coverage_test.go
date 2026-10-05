// coverage_test.go — closes the remaining statement gaps in the exam package:
// CutScore.Validate's max/range branches, CutScore.Passes' validate-error
// propagation, ExamForm.SetCutScore's invalid-cut path, Expose's RETIRED +
// invalid-cut branches, and Grade's no-cut + result-error branches.
package exam

import (
	"errors"
	"testing"
)

// -----------------------------------------------------------------------------
// CutScore.Validate
// -----------------------------------------------------------------------------

func TestCutScore_Validate_Branches(t *testing.T) {
	t.Parallel()
	if err := (CutScore{MaxScore: 0, Mode: CutModeRaw}).Validate(); !errors.Is(err, ErrCutScoreMaxInvalid) {
		t.Errorf("max=0 want ErrCutScoreMaxInvalid; got %v", err)
	}
	if err := (CutScore{MaxScore: 50, Mode: CutModeRaw, RawMark: 51}).Validate(); !errors.Is(err, ErrCutScoreRawOutOfRange) {
		t.Errorf("raw>max want ErrCutScoreRawOutOfRange; got %v", err)
	}
	if err := (CutScore{MaxScore: 50, Mode: CutModePercent, Percent: 60}).Validate(); err != nil {
		t.Errorf("valid percent: got %v want nil", err)
	}
	if err := (CutScore{MaxScore: 50, Mode: CutModePercent, Percent: 100.1}).Validate(); !errors.Is(err, ErrCutScorePercentOutOfRange) {
		t.Errorf("percent>100 want ErrCutScorePercentOutOfRange; got %v", err)
	}
	if err := (CutScore{MaxScore: 50, Mode: CutModePercent, Percent: -0.1}).Validate(); !errors.Is(err, ErrCutScorePercentOutOfRange) {
		t.Errorf("percent<0 want ErrCutScorePercentOutOfRange; got %v", err)
	}
}

func TestCutScore_Passes_PropagatesValidateError(t *testing.T) {
	t.Parallel()
	bad := CutScore{MaxScore: 0, Mode: CutModeRaw}
	if _, err := bad.Passes(0); !errors.Is(err, ErrCutScoreMaxInvalid) {
		t.Errorf("Passes on invalid cut want ErrCutScoreMaxInvalid; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ExamForm.SetCutScore
// -----------------------------------------------------------------------------

func TestExamForm_SetCutScore_RejectsInvalidCut(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	if err := f.SetCutScore(CutScore{MaxScore: 0, Mode: CutModeRaw}); !errors.Is(err, ErrCutScoreMaxInvalid) {
		t.Errorf("invalid cut want ErrCutScoreMaxInvalid; got %v", err)
	}
	if f.Cut != nil {
		t.Errorf("a rejected cut must not be stored")
	}
}

// -----------------------------------------------------------------------------
// ExamForm.Expose
// -----------------------------------------------------------------------------

func TestExamForm_Expose_RejectsRetired(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 2)
	if err := f.Expose(); err != nil {
		t.Fatalf("expose: %v", err)
	}
	if err := f.Retire(); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := f.Expose(); !errors.Is(err, ErrExamFormRetired) {
		t.Errorf("expose RETIRED want ErrExamFormRetired; got %v", err)
	}
}

func TestExamForm_Expose_RejectsInvalidCut(t *testing.T) {
	t.Parallel()
	f := newDraftForm(t)
	if err := f.AddItem(pin(0)); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	// White-box: an ASSEMBLED form carrying a cut that fails re-validation.
	f.State = ExamFormStateAssembled
	f.Cut = &CutScore{MaxScore: 0, Mode: CutModeRaw}
	if err := f.Expose(); !errors.Is(err, ErrCutScoreMaxInvalid) {
		t.Errorf("expose with invalid cut want ErrCutScoreMaxInvalid; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ExamForm.Grade
// -----------------------------------------------------------------------------

func TestExamForm_Grade_RequiresCutScore(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 2)
	if err := f.Expose(); err != nil {
		t.Fatalf("expose: %v", err)
	}
	f.Cut = nil
	if _, _, err := f.Grade("cand", 80); !errors.Is(err, ErrExamFormNoCutScore) {
		t.Errorf("grade without cut want ErrExamFormNoCutScore; got %v", err)
	}
}

func TestExamForm_Grade_RawOutOfRange_Propagates(t *testing.T) {
	t.Parallel()
	f := assembledForm(t, 2) // cut max=100
	if err := f.Expose(); err != nil {
		t.Fatalf("expose: %v", err)
	}
	if _, _, err := f.Grade("cand", 101); !errors.Is(err, ErrExamResultRawInvalid) {
		t.Errorf("raw>max want ErrExamResultRawInvalid; got %v", err)
	}
}
