// completion_policy_eval_test.go — CHO-2157.
//
// The live shape that produced no certificate: offering 019f5f04 carried
// {AwardsCertificate: true, PassingScorePct: 70}, learner L passed at 95%
// (38/40, APPROVED + RELEASED), and the certifications table stayed empty —
// tenant-wide. Nothing evaluated the policy.
package delivery

import "testing"

func TestEvaluateCertificate(t *testing.T) {
	awarding := &CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}

	cases := []struct {
		name       string
		policy     *CompletionPolicy
		facts      CompletionFacts
		wantIssue  bool
		wantReason string
	}{
		{
			// THE live case. 38/40 = 95% against a 70% cut-score.
			name:       "passes the cut-score against an awarding policy → issue",
			policy:     awarding,
			facts:      CompletionFacts{ScorePercent: 95},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
		{
			name:       "exactly on the cut-score → issue (the threshold is inclusive)",
			policy:     awarding,
			facts:      CompletionFacts{ScorePercent: 70},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
		{
			// No phantom credentials.
			name:       "below the cut-score → withheld",
			policy:     awarding,
			facts:      CompletionFacts{ScorePercent: 69.9},
			wantIssue:  false,
			wantReason: ReasonBelowCutScore,
		},
		{
			name:       "policy awards no certificate → withheld",
			policy:     &CompletionPolicy{AwardsCertificate: false, PassingScorePct: 70},
			facts:      CompletionFacts{ScorePercent: 100},
			wantIssue:  false,
			wantReason: ReasonPolicyAwardsNothing,
		},
		{
			name:       "no policy at all → withheld",
			policy:     nil,
			facts:      CompletionFacts{ScorePercent: 100},
			wantIssue:  false,
			wantReason: ReasonNoPolicy,
		},
		{
			name:       "course requires all content and the learner has not finished → withheld",
			policy:     awarding,
			facts:      CompletionFacts{ScorePercent: 95, RequireAllContent: true, AllContentComplete: false},
			wantIssue:  false,
			wantReason: ReasonContentIncomplete,
		},
		{
			name:       "course requires all content and the learner HAS finished → issue",
			policy:     awarding,
			facts:      CompletionFacts{ScorePercent: 95, RequireAllContent: true, AllContentComplete: true},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
		{
			// A course that declares NO content components has nothing to
			// complete — the requirement is vacuous, not unmet. Withholding here
			// would deny a certificate to every learner on every course without
			// modules, which is the live shape of the walk's own course.
			name:       "requires all content but the course declares none → vacuously satisfied",
			policy:     awarding,
			facts:      CompletionFacts{ScorePercent: 95, RequireAllContent: true, AllContentComplete: true},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// nil requirement: every pre-CHO-2222 assertion below must still
			// hold unchanged. An offering that declares no components is
			// vacuously satisfied, so the component gate is invisible here.
			got := EvaluateCertificate(c.policy, nil, c.facts)
			if got.Issue != c.wantIssue {
				t.Errorf("Issue: want %v, got %v (reason %s)", c.wantIssue, got.Issue, got.Reason)
			}
			if got.Reason != c.wantReason {
				t.Errorf("Reason: want %s, got %s", c.wantReason, got.Reason)
			}
		})
	}
}

// Every decision must be explicable — a withheld certificate that logs no reason
// is exactly how this defect survived: silence looked like success.
func TestEvaluateCertificate_AlwaysGivesAReason(t *testing.T) {
	inputs := []struct {
		policy *CompletionPolicy
		facts  CompletionFacts
	}{
		{nil, CompletionFacts{}},
		{&CompletionPolicy{}, CompletionFacts{}},
		{&CompletionPolicy{AwardsCertificate: true, PassingScorePct: 50}, CompletionFacts{ScorePercent: 10}},
		{&CompletionPolicy{AwardsCertificate: true}, CompletionFacts{ScorePercent: 0}},
	}
	for _, in := range inputs {
		if got := EvaluateCertificate(in.policy, nil, in.facts); got.Reason == "" {
			t.Errorf("every outcome must carry a reason; got none for %+v", in)
		}
	}
}

func TestCertificateAccomplishments(t *testing.T) {
	if got := CertificateAccomplishments(&CompletionPolicy{CertTitle: "L3 Competency"}); len(got) != 1 || got[0] != "L3 Competency" {
		t.Errorf("the offering's CertTitle must label the award, got %v", got)
	}
	// Never render a blank certificate.
	got := CertificateAccomplishments(&CompletionPolicy{})
	if len(got) != 1 || got[0] == "" {
		t.Errorf("an untitled policy must still yield a non-empty label, got %v", got)
	}
	if CertificateAccomplishments(nil) != nil {
		t.Error("a nil policy yields no labels")
	}
}
