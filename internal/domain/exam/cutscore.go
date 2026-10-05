// cutscore.go — the CutScore value object for the Exam bounded context.
//
// A CutScore is the compliance-critical pass-mark on an ExamForm (ADR-190 D2:
// "legal/compliance-grade cut-score → pass/fail → certificate chains"). It is
// an immutable value object — replace, never mutate. Two authoring modes:
//
//	RAW     — an absolute raw-score threshold (e.g. 30 out of 50)
//	PERCENT — a percentage threshold (e.g. 60%), converted to a raw pass-mark
//	          via CEIL so a candidate is never rounded UP into a pass
//
// Invariant (both modes): 0 <= cut <= max, max > 0. Passing rule: a raw score
// PASSES iff raw >= PassMark(). This file is pure domain — NO http, NO
// persistence imports.
package exam

import (
	"errors"
	"math"
)

// CutMode identifies how a CutScore's threshold is expressed.
type CutMode string

const (
	// CutModeRaw — the threshold is an absolute raw score (RawMark).
	CutModeRaw CutMode = "RAW"
	// CutModePercent — the threshold is a percentage (Percent) of MaxScore.
	CutModePercent CutMode = "PERCENT"
)

// IsValid reports whether m is one of the canonical cut modes.
func (m CutMode) IsValid() bool {
	switch m {
	case CutModeRaw, CutModePercent:
		return true
	}
	return false
}

var (
	// ErrCutScoreMaxInvalid — max_score must be > 0.
	ErrCutScoreMaxInvalid = errors.New("exam: cut_score max_score must be > 0")
	// ErrCutScoreRawOutOfRange — raw_mark must satisfy 0 <= raw_mark <= max_score.
	ErrCutScoreRawOutOfRange = errors.New("exam: cut_score raw_mark out of range [0,max]")
	// ErrCutScorePercentOutOfRange — percent must satisfy 0 <= percent <= 100.
	ErrCutScorePercentOutOfRange = errors.New("exam: cut_score percent out of range [0,100]")
	// ErrCutScoreModeInvalid — mode must be RAW or PERCENT.
	ErrCutScoreModeInvalid = errors.New("exam: cut_score mode invalid")
	// ErrCutScoreRawInvalid — a scored raw value is outside [0,max_score].
	ErrCutScoreRawInvalid = errors.New("exam: raw_score out of range [0,max]")
)

// CutScore is an immutable pass-mark value object. Fields are exported so the
// aggregate JSONB snapshot round-trips losslessly; construct via the New*
// constructors so the invariant is enforced.
type CutScore struct {
	MaxScore int     `json:"max_score"`
	Mode     CutMode `json:"mode"`
	RawMark  int     `json:"raw_mark"` // meaningful when Mode == RAW
	Percent  float64 `json:"percent"`  // meaningful when Mode == PERCENT
}

// NewRawCutScore builds a RAW-mode cut. Guards max > 0 and 0 <= rawMark <= max.
func NewRawCutScore(maxScore, rawMark int) (CutScore, error) {
	if maxScore <= 0 {
		return CutScore{}, ErrCutScoreMaxInvalid
	}
	if rawMark < 0 || rawMark > maxScore {
		return CutScore{}, ErrCutScoreRawOutOfRange
	}
	return CutScore{MaxScore: maxScore, Mode: CutModeRaw, RawMark: rawMark}, nil
}

// NewPercentCutScore builds a PERCENT-mode cut. Guards max > 0 and
// 0 <= percent <= 100.
func NewPercentCutScore(maxScore int, percent float64) (CutScore, error) {
	if maxScore <= 0 {
		return CutScore{}, ErrCutScoreMaxInvalid
	}
	if percent < 0 || percent > 100 {
		return CutScore{}, ErrCutScorePercentOutOfRange
	}
	return CutScore{MaxScore: maxScore, Mode: CutModePercent, Percent: percent}, nil
}

// Validate re-checks the invariant. Used before scoring so a snapshot that was
// tampered with (or a zero-value CutScore) is caught fail-loud.
func (c CutScore) Validate() error {
	if c.MaxScore <= 0 {
		return ErrCutScoreMaxInvalid
	}
	switch c.Mode {
	case CutModeRaw:
		if c.RawMark < 0 || c.RawMark > c.MaxScore {
			return ErrCutScoreRawOutOfRange
		}
	case CutModePercent:
		if c.Percent < 0 || c.Percent > 100 {
			return ErrCutScorePercentOutOfRange
		}
	default:
		return ErrCutScoreModeInvalid
	}
	return nil
}

// PassMark returns the effective raw-score threshold a candidate must reach
// (>=) to PASS. PERCENT mode uses CEIL so, e.g., 61% of 50 (30.5) resolves to
// 31 — never round a borderline candidate up into a pass.
func (c CutScore) PassMark() int {
	if c.Mode == CutModePercent {
		return int(math.Ceil(c.Percent / 100 * float64(c.MaxScore)))
	}
	return c.RawMark
}

// Passes reports whether rawScore meets or exceeds the cut. It guards
// 0 <= rawScore <= MaxScore (a raw score outside the attainable range is a
// grading bug and must not silently score) and validates the cut first.
func (c CutScore) Passes(rawScore int) (bool, error) {
	if err := c.Validate(); err != nil {
		return false, err
	}
	if rawScore < 0 || rawScore > c.MaxScore {
		return false, ErrCutScoreRawInvalid
	}
	return rawScore >= c.PassMark(), nil
}
