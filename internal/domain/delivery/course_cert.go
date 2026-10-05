// course_cert.go — the certificate a Course awards on completion (CHO-1795, L3).
//
// CertDefinition is a value object on the Course aggregate, set at authoring
// time and CONSUMED by the issuance chain: the learner earns the certificate
// when they meet the course-defined criteria (passing score and, when
// RequireAllContent, completing every content item). Absent / disabled ⇒ the
// course awards no certificate.
//
// Distinct from Certification (delivery.go) — that is an ISSUED credential.
package delivery

import (
	"errors"
	"fmt"
)

// CertType is the kind of certificate a completed course awards.
type CertType string

const (
	CertTypeCompletion      CertType = "COMPLETION"
	CertTypeCompetency      CertType = "COMPETENCY"
	CertTypeAccredited      CertType = "ACCREDITED"
	CertTypeMicroCredential CertType = "MICRO_CREDENTIAL"
)

// Valid reports whether t is a recognised cert type.
func (t CertType) Valid() bool {
	switch t {
	case CertTypeCompletion, CertTypeCompetency, CertTypeAccredited, CertTypeMicroCredential:
		return true
	}
	return false
}

var (
	// ErrCertTypeInvalid — cert_type outside the enum when certification enabled.
	ErrCertTypeInvalid = errors.New("delivery: cert_type must be COMPLETION, COMPETENCY, ACCREDITED, or MICRO_CREDENTIAL")
	// ErrCertPassingScoreRange — passing_score_pct outside 0..100.
	ErrCertPassingScoreRange = errors.New("delivery: passing_score_pct must be between 0 and 100")
)

// CertDefinition is the cert a course awards. The zero value (Enabled=false)
// means the course awards no certificate.
type CertDefinition struct {
	Enabled           bool
	CertType          CertType
	PassingScorePct   int
	RequireAllContent bool
}

// Validate enforces the cert-definition invariants. A disabled definition is
// always valid (its other fields are ignored). When enabled, cert_type — if
// set — must be in the enum, and passing_score_pct must be 0..100.
func (d CertDefinition) Validate() error {
	if !d.Enabled {
		return nil
	}
	if d.CertType != "" && !d.CertType.Valid() {
		return fmt.Errorf("%w (got %q)", ErrCertTypeInvalid, string(d.CertType))
	}
	if d.PassingScorePct < 0 || d.PassingScorePct > 100 {
		return fmt.Errorf("%w (got %d)", ErrCertPassingScoreRange, d.PassingScorePct)
	}
	return nil
}

// Meets reports whether a learner's assessment score (0..100) satisfies this
// definition's passing threshold. A disabled definition is never "met" (no
// cert to award). Used by the issuance chain to consume the criteria.
func (d CertDefinition) Meets(scorePct int) bool {
	if !d.Enabled {
		return false
	}
	return scorePct >= d.PassingScorePct
}
