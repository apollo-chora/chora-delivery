// completion_policy_eval.go — the rule that turns a released grade into a
// certificate (CHO-2157).
//
// The CompletionPolicy editor has shipped and persisted for weeks, and NOTHING
// consumed it. A learner who passed at 95% against an active
// {AwardsCertificate: true, PassingScorePct: 70} policy received no certificate,
// because no engine ever evaluated "passed ∧ policy satisfied ⇒ issue". Storing
// a policy that never fires is worse than shipping no editor at all: it looks
// like the feature works.
//
// The rule lives here — pure, total, and unit-testable — so the subscriber that
// drives it only has to fetch facts, never to decide.
package delivery

import "strings"

// CertificateOutcome is the decision for one released grade.
type CertificateOutcome struct {
	// Issue — mint a certificate for this learner.
	Issue bool
	// Reason is a short machine-ish token explaining the decision. It is LOGGED
	// on every evaluation, so a learner who does not get a certificate leaves a
	// trail saying exactly why — the previous behaviour (silence) is what let
	// this go unnoticed.
	Reason string
}

// Completion-outcome reasons.
const (
	ReasonNoPolicy            = "NO_COMPLETION_POLICY"
	ReasonPolicyAwardsNothing = "POLICY_AWARDS_NO_CERTIFICATE"
	ReasonBelowCutScore       = "BELOW_PASSING_SCORE"
	ReasonContentIncomplete   = "CONTENT_INCOMPLETE"
	// ReasonComponentsIncomplete - the offering DECLARED components (ADR-190 D1)
	// and at least one of them is not complete.
	ReasonComponentsIncomplete = "DECLARED_COMPONENTS_INCOMPLETE"
	ReasonIssue                = "ISSUE"
)

// CompletionFacts are the inputs an auto-issue decision needs. The subscriber
// resolves them; this package decides.
type CompletionFacts struct {
	// ScorePercent is the learner's FINAL released score as a percentage of the
	// assessment's total. It must be the instructor's grade of record — see
	// CHO-2154, where the platform announced the AI's provisional number instead.
	ScorePercent float64

	// RequireAllContent mirrors the COURSE's cert_require_all_content flag.
	RequireAllContent bool

	// AllContentComplete reports whether the learner has finished every content
	// component the course declares. A course that declares NO components has
	// nothing to complete, so this is true — the requirement is vacuous, not
	// unmet. (Withholding a certificate because a course defines no modules
	// would deny every learner on every course that has none.)
	AllContentComplete bool

	// CompletedComponents are the components this learner has ACTUALLY
	// completed, resolved by the subscriber from chora_delivery's own tables
	// (intra-domain: cross-DB queries are forbidden). It is the learner-side
	// fact that the offering's CompletionRequirement is checked against.
	//
	// Order is irrelevant and extras are harmless: the gate asks "is every
	// DECLARED component in here?", not "do these two sets match?".
	CompletedComponents []CompletionComponent
}

// EvaluateCertificate decides whether a released grade earns a certificate.
//
// It is deliberately total: every path returns a Reason, so a withheld
// certificate is always explicable. Nothing here silently declines.
//
// req is the offering's declared-component requirement (ADR-190 D1, CHO-2222);
// nil means the offering declares none, which is VACUOUSLY satisfied. It is a
// parameter rather than a CompletionFact because, like policy, it is offering
// CONFIG read straight off the aggregate, not a learner fact the subscriber has
// to resolve.
//
// Gate ORDER is load-bearing. The component check runs LAST, after the
// cut-score, so it is strictly ADDITIVE: a learner who completed every declared
// component but scored below the cut-score is still refused, and the reason
// still says BELOW_PASSING_SCORE. A component gate that could overturn the score
// gate would be a bypass wearing the costume of a requirement.
//
// This applies to the offering-bound lanes only. A STANDALONE exam (no offering,
// no submission) is deliberately NOT routed through here: its legal-grade
// cut-score was already applied when the ExamResult was finalised, so PASS is
// the whole policy (ADR-190 D2, owner-ratified; see exam_result_inbox.go:15-21).
func EvaluateCertificate(policy *CompletionPolicy, req *CompletionRequirement, facts CompletionFacts) CertificateOutcome {
	if policy == nil {
		return CertificateOutcome{Issue: false, Reason: ReasonNoPolicy}
	}
	if !policy.AwardsCertificate {
		return CertificateOutcome{Issue: false, Reason: ReasonPolicyAwardsNothing}
	}
	// Below the cut-score ⇒ no phantom credential.
	if facts.ScorePercent < float64(policy.PassingScorePct) {
		return CertificateOutcome{Issue: false, Reason: ReasonBelowCutScore}
	}
	// The course may additionally require that all declared content is complete.
	if facts.RequireAllContent && !facts.AllContentComplete {
		return CertificateOutcome{Issue: false, Reason: ReasonContentIncomplete}
	}
	// The offering may additionally DECLARE named components (ADR-190 D1).
	if len(req.missingComponents(facts.CompletedComponents)) > 0 {
		return CertificateOutcome{Issue: false, Reason: ReasonComponentsIncomplete}
	}
	return CertificateOutcome{Issue: true, Reason: ReasonIssue}
}

// CertificateAccomplishments is the label list stamped onto an auto-issued
// certificate. The offering's CertTitle is the human-facing name of the award;
// an empty one falls back to a neutral phrase rather than an empty string, so a
// certificate never renders blank.
func CertificateAccomplishments(policy *CompletionPolicy) []string {
	if policy == nil {
		return nil
	}
	title := strings.TrimSpace(policy.CertTitle)
	if title == "" {
		return []string{"Completed the offering"}
	}
	return []string{title}
}
