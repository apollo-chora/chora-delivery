// cutscore_test.go — TDD for the CutScore value object (W4 Brick-1).
//
// CutScore is the compliance-critical pass-mark on an ExamForm. This suite
// pins the boundary values of the `0 <= cut <= max` invariant and the
// raw-score -> PASS/FAIL comparison (>= cut), because a cut-score off-by-one
// mis-scores a legal/certification exam.
//
// White-box (package exam) so we can assert on the exported value object
// without a cross-package hop.
package exam

import (
	"errors"
	"testing"
)

func TestCutMode_IsValid(t *testing.T) {
	t.Parallel()
	if !CutModeRaw.IsValid() || !CutModePercent.IsValid() {
		t.Fatal("canonical cut modes must be valid")
	}
	if CutMode("BOGUS").IsValid() {
		t.Fatal("bogus mode must be invalid")
	}
}

func TestNewRawCutScore_OK(t *testing.T) {
	t.Parallel()
	c, err := NewRawCutScore(50, 30)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c.MaxScore != 50 || c.RawMark != 30 || c.Mode != CutModeRaw {
		t.Fatalf("got %+v", c)
	}
	if c.PassMark() != 30 {
		t.Fatalf("passmark=%d want 30", c.PassMark())
	}
}

func TestNewRawCutScore_Boundaries(t *testing.T) {
	t.Parallel()
	// 0 <= cut <= max — the compliance invariant.
	if _, err := NewRawCutScore(50, 0); err != nil {
		t.Errorf("mark=0 must be valid: %v", err)
	}
	if _, err := NewRawCutScore(50, 50); err != nil {
		t.Errorf("mark==max must be valid: %v", err)
	}
	if _, err := NewRawCutScore(50, 51); !errors.Is(err, ErrCutScoreRawOutOfRange) {
		t.Errorf("mark>max want ErrCutScoreRawOutOfRange; got %v", err)
	}
	if _, err := NewRawCutScore(50, -1); !errors.Is(err, ErrCutScoreRawOutOfRange) {
		t.Errorf("mark<0 want ErrCutScoreRawOutOfRange; got %v", err)
	}
	if _, err := NewRawCutScore(0, 0); !errors.Is(err, ErrCutScoreMaxInvalid) {
		t.Errorf("max=0 want ErrCutScoreMaxInvalid; got %v", err)
	}
	if _, err := NewRawCutScore(-5, 0); !errors.Is(err, ErrCutScoreMaxInvalid) {
		t.Errorf("max<0 want ErrCutScoreMaxInvalid; got %v", err)
	}
}

func TestNewPercentCutScore_OK_AndCeil(t *testing.T) {
	t.Parallel()
	c, err := NewPercentCutScore(50, 60)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c.PassMark() != 30 {
		t.Fatalf("60%% of 50 → passmark=%d want 30", c.PassMark())
	}
	// 61% of 50 = 30.5 → ceil to 31 (never round a candidate UP into a pass).
	c2, _ := NewPercentCutScore(50, 61)
	if c2.PassMark() != 31 {
		t.Fatalf("61%% of 50 → passmark=%d want 31 (ceil)", c2.PassMark())
	}
}

func TestNewPercentCutScore_Boundaries(t *testing.T) {
	t.Parallel()
	if _, err := NewPercentCutScore(50, 0); err != nil {
		t.Errorf("0%% must be valid: %v", err)
	}
	if _, err := NewPercentCutScore(50, 100); err != nil {
		t.Errorf("100%% must be valid: %v", err)
	}
	if _, err := NewPercentCutScore(50, 100.1); !errors.Is(err, ErrCutScorePercentOutOfRange) {
		t.Errorf(">100 want ErrCutScorePercentOutOfRange; got %v", err)
	}
	if _, err := NewPercentCutScore(50, -0.1); !errors.Is(err, ErrCutScorePercentOutOfRange) {
		t.Errorf("<0 want ErrCutScorePercentOutOfRange; got %v", err)
	}
	if _, err := NewPercentCutScore(0, 50); !errors.Is(err, ErrCutScoreMaxInvalid) {
		t.Errorf("max=0 want ErrCutScoreMaxInvalid; got %v", err)
	}
}

func TestCutScore_Passes_RawBoundaries(t *testing.T) {
	t.Parallel()
	c, _ := NewRawCutScore(50, 30)
	cases := []struct {
		raw  int
		want bool
	}{
		{30, true},  // exactly at the cut → PASS (>=)
		{31, true},  // above → PASS
		{29, false}, // one below → FAIL
		{50, true},  // max → PASS
		{0, false},  // zero → FAIL
	}
	for _, tc := range cases {
		got, err := c.Passes(tc.raw)
		if err != nil {
			t.Fatalf("raw=%d unexpected err %v", tc.raw, err)
		}
		if got != tc.want {
			t.Errorf("raw=%d passes=%v want %v", tc.raw, got, tc.want)
		}
	}
}

func TestCutScore_Passes_GuardsRawRange(t *testing.T) {
	t.Parallel()
	c, _ := NewRawCutScore(50, 30)
	if _, err := c.Passes(51); !errors.Is(err, ErrCutScoreRawInvalid) {
		t.Errorf("raw>max want ErrCutScoreRawInvalid; got %v", err)
	}
	if _, err := c.Passes(-1); !errors.Is(err, ErrCutScoreRawInvalid) {
		t.Errorf("raw<0 want ErrCutScoreRawInvalid; got %v", err)
	}
}

func TestCutScore_Validate_RejectsBadMode(t *testing.T) {
	t.Parallel()
	if err := (CutScore{MaxScore: 10, Mode: "NOPE"}).Validate(); !errors.Is(err, ErrCutScoreModeInvalid) {
		t.Errorf("bad mode want ErrCutScoreModeInvalid; got %v", err)
	}
}
