// examresult_test.go — TDD for the ExamResult aggregate (W4 Brick-1).
//
// The pure cut-score -> PASS/FAIL computation (NewExamResult) is the #1
// highest-risk path in the four-mode delivery refactor (plan §8 #1): a
// mis-scored cut mis-issues (or withholds) a certificate. This suite pins the
// boundary (raw==cut PASS, raw==cut-1 FAIL), the raw-range guards, and the
// required-reference guards.
package exam

import (
	"errors"
	"testing"
)

func TestOutcome_IsValid(t *testing.T) {
	t.Parallel()
	if !OutcomePass.IsValid() || !OutcomeFail.IsValid() {
		t.Fatal("canonical outcomes must be valid")
	}
	if Outcome("MEH").IsValid() {
		t.Fatal("bogus outcome must be invalid")
	}
}

func resultInput(raw int, cut CutScore) NewExamResultInput {
	return NewExamResultInput{
		TenantID:     formTestTenantID,
		ExamID:       formTestExamID,
		ExamFormID:   "019e2f93-d586-71b5-8c3d-e2b0d0d50600",
		CandidateRef: "019e2f93-d586-71b5-8c3d-e2b0d0d50500",
		Cut:          cut,
		RawScore:     raw,
	}
}

func TestNewExamResult_CutScorePassFail_Boundary(t *testing.T) {
	t.Parallel()
	cut := mustRawCut(t, 50, 30)

	pass, err := NewExamResult(resultInput(30, cut)) // exactly at the cut
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if pass.Outcome != OutcomePass {
		t.Errorf("raw==cut must PASS; got %s", pass.Outcome)
	}
	if pass.MaxScore != 50 || pass.RawScore != 30 {
		t.Errorf("scores wrong: %+v", pass)
	}
	if pass.ScoredAt.IsZero() || pass.ID == "" {
		t.Error("result must carry an ID + ScoredAt")
	}

	fail, err := NewExamResult(resultInput(29, cut)) // one below the cut
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if fail.Outcome != OutcomeFail {
		t.Errorf("raw==cut-1 must FAIL; got %s", fail.Outcome)
	}
}

func TestNewExamResult_GuardsInvalidRaw(t *testing.T) {
	t.Parallel()
	cut := mustRawCut(t, 50, 30)
	if _, err := NewExamResult(resultInput(51, cut)); !errors.Is(err, ErrExamResultRawInvalid) {
		t.Errorf("raw>max want ErrExamResultRawInvalid; got %v", err)
	}
	if _, err := NewExamResult(resultInput(-1, cut)); !errors.Is(err, ErrExamResultRawInvalid) {
		t.Errorf("raw<0 want ErrExamResultRawInvalid; got %v", err)
	}
}

func TestNewExamResult_GuardsRequiredRefs(t *testing.T) {
	t.Parallel()
	cut := mustRawCut(t, 50, 30)
	base := resultInput(40, cut)

	mut := base
	mut.TenantID = ""
	if _, err := NewExamResult(mut); !errors.Is(err, ErrExamResultTenantRequired) {
		t.Errorf("tenant want ErrExamResultTenantRequired; got %v", err)
	}
	mut = base
	mut.ExamID = ""
	if _, err := NewExamResult(mut); !errors.Is(err, ErrExamResultExamRequired) {
		t.Errorf("exam want ErrExamResultExamRequired; got %v", err)
	}
	mut = base
	mut.ExamFormID = ""
	if _, err := NewExamResult(mut); !errors.Is(err, ErrExamResultFormRequired) {
		t.Errorf("form want ErrExamResultFormRequired; got %v", err)
	}
	mut = base
	mut.CandidateRef = ""
	if _, err := NewExamResult(mut); !errors.Is(err, ErrExamResultCandidateRequired) {
		t.Errorf("candidate want ErrExamResultCandidateRequired; got %v", err)
	}
}

func TestNewExamResult_PercentCut(t *testing.T) {
	t.Parallel()
	cut, err := NewPercentCutScore(200, 50) // passmark = 100
	if err != nil {
		t.Fatalf("NewPercentCutScore: %v", err)
	}
	pass, _ := NewExamResult(resultInput(100, cut))
	if pass.Outcome != OutcomePass {
		t.Errorf("100/200 at 50%% → PASS; got %s", pass.Outcome)
	}
	fail, _ := NewExamResult(resultInput(99, cut))
	if fail.Outcome != OutcomeFail {
		t.Errorf("99/200 at 50%% → FAIL; got %s", fail.Outcome)
	}
}

func TestNewExamResult_GuardsInvalidCut(t *testing.T) {
	t.Parallel()
	// The zero-value cut (max=0) is invalid and must be rejected loudly.
	if _, err := NewExamResult(resultInput(10, CutScore{})); err == nil {
		t.Error("zero-value cut must be rejected")
	}
}
