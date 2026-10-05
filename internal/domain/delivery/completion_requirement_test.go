// completion_requirement_test.go - CHO-2222.
//
// ADR-190 D1 specifies a per-offering CompletionRequirement that declares which
// components THIS offering needs, with certificate issuance as "a policy on
// top", component-optional by design. The type never existed. Its only
// occurrence in the entire repo was a NEGATIVE mention inside a comment
// (offering_certification_handler.go:17, "it touches NO ... CompletionRequirement"),
// so the masterplan's section 10.6 capstone criterion ("cert issuance fires only
// when its declared components are complete") was unimplementable as written.
//
// The live consequence: an offering could not declare "assessment AND exam".
// EvaluateCertificate gated on ONE score threshold plus a coarse all-content
// boolean, so a learner who passed the assessment of a two-component offering
// received the certificate without ever sitting its exam.
//
// Component-optional is the load-bearing subtlety, and it cuts BOTH ways:
// declaring nothing must stay vacuously satisfied (else every offering that
// declares no components denies every learner), while declaring something must
// actually gate (else the declaration is decoration). Both directions are
// asserted here.
package delivery

import (
	"errors"
	"testing"
)

// Component refs name rows in UUID columns (submissions.assessment_id,
// exam_results.exam_id), so a declared ref must BE a UUID - see the
// canonicalisation guard below.
const (
	refAssessmentA = "01970000-0000-7000-a000-0000000000a1"
	refExamE       = "01970000-0000-7000-b000-0000000000e1"
	refProjectP    = "01970000-0000-7000-c000-0000000000c1"
)

// -----------------------------------------------------------------------------
// The declaration (Offering.SetCompletionRequirement)
// -----------------------------------------------------------------------------

func TestOffering_SetCompletionRequirement_Accepts(t *testing.T) {
	o := &Offering{}
	err := o.SetCompletionRequirement(SetCompletionRequirementInput{
		Components: []CompletionComponent{
			{Kind: ComponentKindAssessment, Ref: "  " + refAssessmentA + "  "},
			{Kind: ComponentKindExam, Ref: refExamE},
		},
	})
	if err != nil {
		t.Fatalf("SetCompletionRequirement: %v", err)
	}
	if o.CompletionRequirement == nil {
		t.Fatal("the requirement must be set on the offering")
	}
	if got := len(o.CompletionRequirement.Components); got != 2 {
		t.Fatalf("want 2 components, got %d", got)
	}
	// Refs are trimmed, mirroring CertTitle in SetCompletionPolicy: an
	// untrimmed ref would never match a resolved component id and the gate
	// would silently never fire.
	if got := o.CompletionRequirement.Components[0].Ref; got != refAssessmentA {
		t.Errorf("component ref must be trimmed, got %q", got)
	}
	if o.CompletionRequirement.UpdatedAt.IsZero() {
		t.Error("UpdatedAt must be stamped")
	}
	if o.UpdatedAt.IsZero() {
		t.Error("setting a requirement must bump the offering's UpdatedAt")
	}
}

// A ref is stored in its CANONICAL uuid form, not as typed. uuid.Parse is
// deliberately lenient - it accepts "urn:uuid:<uuid>", "{<uuid>}" and the
// 32-char dashless form - and Postgres accepts only some of those. Validating
// with Parse but storing the RAW text would leave the 22P02 door open just
// narrower, and would make dedup dishonest: on raw text "X" and "urn:uuid:X"
// survive as two entries for one component. This is the identical lesson
// canonicalCourseIDs already learned one file over (offering.go).
func TestOffering_SetCompletionRequirement_CanonicalisesRefs(t *testing.T) {
	o := &Offering{}
	err := o.SetCompletionRequirement(SetCompletionRequirementInput{
		Components: []CompletionComponent{
			{Kind: ComponentKindAssessment, Ref: "urn:uuid:" + refAssessmentA},
		},
	})
	if err != nil {
		t.Fatalf("SetCompletionRequirement: %v", err)
	}
	if got := o.CompletionRequirement.Components[0].Ref; got != refAssessmentA {
		t.Errorf("ref must be stored canonical (Postgres rejects the urn: form); want %q, got %q",
			refAssessmentA, got)
	}
}

func TestOffering_SetCompletionRequirement_CanonicalFormMakesDedupHonest(t *testing.T) {
	// The same component typed two ways is ONE component, and must be refused
	// as the duplicate it is.
	o := &Offering{}
	err := o.SetCompletionRequirement(SetCompletionRequirementInput{
		Components: []CompletionComponent{
			{Kind: ComponentKindAssessment, Ref: refAssessmentA},
			{Kind: ComponentKindAssessment, Ref: "urn:uuid:" + refAssessmentA},
		},
	})
	if !errors.Is(err, ErrCompletionComponentDuplicate) {
		t.Fatalf("want ErrCompletionComponentDuplicate for the same uuid typed two ways, got %v", err)
	}
}

func TestOffering_SetCompletionRequirement_DeclaringNothingIsAllowed(t *testing.T) {
	// An offering that requires nothing is legitimate (the live shape of every
	// offering today). It must be storable, and it must NOT be an error.
	o := &Offering{}
	if err := o.SetCompletionRequirement(SetCompletionRequirementInput{}); err != nil {
		t.Fatalf("declaring no components must be allowed: %v", err)
	}
	if o.CompletionRequirement == nil {
		t.Fatal("an empty requirement is still a declaration and must be set")
	}
	if len(o.CompletionRequirement.Components) != 0 {
		t.Errorf("want no components, got %d", len(o.CompletionRequirement.Components))
	}
}

func TestOffering_SetCompletionRequirement_Guards(t *testing.T) {
	cases := []struct {
		name    string
		in      SetCompletionRequirementInput
		wantErr error
	}{
		{
			// Fail loud. A component kind the evaluator does not understand
			// cannot be resolved, so tolerating it would produce a gate that
			// silently never fires: exactly the failure mode the exam lane's
			// owner-ratified note (exam_result_inbox.go:15-21) rejected when it
			// refused a gate keyed on a column exam-only courses never set.
			name: "unknown component kind is refused",
			in: SetCompletionRequirementInput{Components: []CompletionComponent{
				{Kind: CompletionComponentKind("homework"), Ref: "h-1"},
			}},
			wantErr: ErrCompletionComponentKind,
		},
		{
			name: "empty component kind is refused",
			in: SetCompletionRequirementInput{Components: []CompletionComponent{
				{Kind: "", Ref: "h-1"},
			}},
			wantErr: ErrCompletionComponentKind,
		},
		{
			name: "blank component ref is refused",
			in: SetCompletionRequirementInput{Components: []CompletionComponent{
				{Kind: ComponentKindAssessment, Ref: "   "},
			}},
			wantErr: ErrCompletionComponentRef,
		},
		{
			// A ref names a row in a UUID column (submissions.assessment_id /
			// exam_results.exam_id). The requirement lives in JSONB, so there is
			// no typed DB guard on it, and the resolver casts the declared refs
			// to uuid[]. A ref that is not a UUID would therefore 22P02 at
			// RESOLVE time - inside a Pub/Sub handler, far from the request that
			// stored it - NACKing every released grade on the offering and
			// reading as an infra outage rather than the typo it is. Refuse it
			// HERE, where the admin is still holding the keyboard.
			name: "a ref that is not a UUID is refused",
			in: SetCompletionRequirementInput{Components: []CompletionComponent{
				{Kind: ComponentKindAssessment, Ref: "assessment-a"},
			}},
			wantErr: ErrCompletionComponentRefInvalid,
		},
		{
			// chora_delivery has NO per-learner project-completion source: the
			// only live project table is project_groups, which is keyed by
			// COURSE and carries a group-level state, not a per-offering project
			// a learner completes. Accepting a project component would arm a gate
			// nothing can ever satisfy, withholding every certificate on the
			// offering forever. Refuse to arm what nothing can resolve.
			name: "the project kind has no completion source and is refused",
			in: SetCompletionRequirementInput{Components: []CompletionComponent{
				{Kind: ComponentKindProject, Ref: refProjectP},
			}},
			wantErr: ErrCompletionComponentUnresolvable,
		},
		{
			// A duplicate is never a caller's intent, and a silently deduped
			// list would make the declaration disagree with what the admin typed.
			name: "duplicate (kind, ref) is refused",
			in: SetCompletionRequirementInput{Components: []CompletionComponent{
				{Kind: ComponentKindAssessment, Ref: refAssessmentA},
				{Kind: ComponentKindAssessment, Ref: " " + refAssessmentA + " "},
			}},
			wantErr: ErrCompletionComponentDuplicate,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := &Offering{}
			err := o.SetCompletionRequirement(c.in)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("want %v, got %v", c.wantErr, err)
			}
			// A refused declaration must not half-apply.
			if o.CompletionRequirement != nil {
				t.Error("a refused requirement must leave the offering untouched")
			}
		})
	}
}

func TestOffering_SetCompletionRequirement_SameRefDifferentKindIsNotADuplicate(t *testing.T) {
	// Identity is (kind, ref), not ref alone: an assessment and an exam may
	// legitimately share an id space.
	const shared = "01970000-0000-7000-d000-0000000000d1"
	o := &Offering{}
	err := o.SetCompletionRequirement(SetCompletionRequirementInput{
		Components: []CompletionComponent{
			{Kind: ComponentKindAssessment, Ref: shared},
			{Kind: ComponentKindExam, Ref: shared},
		},
	})
	if err != nil {
		t.Fatalf("same ref under a different kind is not a duplicate: %v", err)
	}
}

// Resolvable is the capability fact the whole design turns on: it is what the
// declaration guard consults, and what the pg resolver refuses on. The two MUST
// agree - if the editor could arm a kind the resolver cannot serve, the gate
// would jam shut; if the resolver rejected a kind the editor allows, a live
// offering would NACK every grade.
func TestCompletionComponentKind_Resolvable(t *testing.T) {
	cases := map[CompletionComponentKind]bool{
		ComponentKindAssessment: true, // submissions (state=RELEASED, passed)
		ComponentKindExam:       true, // exam_results (outcome=PASS)
		// ADR-190 D1 names project as a component kind, and it stays a valid
		// KIND. What is missing is a per-learner completion SOURCE. Flip this to
		// true the day one exists - and not before.
		ComponentKindProject:          false,
		CompletionComponentKind("hw"): false,
	}
	for kind, want := range cases {
		if got := kind.Resolvable(); got != want {
			t.Errorf("%q.Resolvable(): want %v, got %v", kind, want, got)
		}
	}
}

// -----------------------------------------------------------------------------
// The gate (EvaluateCertificate)
// -----------------------------------------------------------------------------

func TestEvaluateCertificate_DeclaredComponents(t *testing.T) {
	awarding := &CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	assessmentA := CompletionComponent{Kind: ComponentKindAssessment, Ref: "a-1"}
	examE := CompletionComponent{Kind: ComponentKindExam, Ref: "e-1"}

	cases := []struct {
		name       string
		req        *CompletionRequirement
		facts      CompletionFacts
		wantIssue  bool
		wantReason string
	}{
		{
			name: "every declared component complete: issue",
			req:  &CompletionRequirement{Components: []CompletionComponent{assessmentA, examE}},
			facts: CompletionFacts{
				ScorePercent:        95,
				CompletedComponents: []CompletionComponent{assessmentA, examE},
			},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
		{
			// THE capstone case: a two-component offering must not certify on
			// the assessment alone.
			name: "a declared component is missing: withheld",
			req:  &CompletionRequirement{Components: []CompletionComponent{assessmentA, examE}},
			facts: CompletionFacts{
				ScorePercent:        95,
				CompletedComponents: []CompletionComponent{assessmentA},
			},
			wantIssue:  false,
			wantReason: ReasonComponentsIncomplete,
		},
		{
			name:       "declares components, learner completed none: withheld",
			req:        &CompletionRequirement{Components: []CompletionComponent{assessmentA}},
			facts:      CompletionFacts{ScorePercent: 100},
			wantIssue:  false,
			wantReason: ReasonComponentsIncomplete,
		},
		{
			// Component-optional, direction 1: declaring nothing is vacuous.
			name:       "an empty requirement is vacuously satisfied: issue",
			req:        &CompletionRequirement{},
			facts:      CompletionFacts{ScorePercent: 95},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
		{
			// The live shape of every offering today. A nil requirement must
			// behave exactly as before this story existed.
			name:       "no requirement declared at all: issue (backward compatible)",
			req:        nil,
			facts:      CompletionFacts{ScorePercent: 95},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
		{
			// Section 10.6 bullet 2: no exam declared, no exam event, no
			// phantom row, and the certificate still issues on the assessment.
			name: "no exam declared and none sat: issue on the assessment alone",
			req:  &CompletionRequirement{Components: []CompletionComponent{assessmentA}},
			facts: CompletionFacts{
				ScorePercent:        95,
				CompletedComponents: []CompletionComponent{assessmentA},
			},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
		{
			// The component gate is ADDITIVE to the score gate, never a bypass:
			// the score must still lose first, or a complete-but-failing learner
			// would be certified.
			name: "components complete but below the cut-score: the score gate still wins",
			req:  &CompletionRequirement{Components: []CompletionComponent{assessmentA}},
			facts: CompletionFacts{
				ScorePercent:        69.9,
				CompletedComponents: []CompletionComponent{assessmentA},
			},
			wantIssue:  false,
			wantReason: ReasonBelowCutScore,
		},
		{
			// Completing something that was never asked for cannot substitute
			// for the thing that was.
			name: "an undeclared completion does not satisfy a declared component",
			req:  &CompletionRequirement{Components: []CompletionComponent{examE}},
			facts: CompletionFacts{
				ScorePercent:        95,
				CompletedComponents: []CompletionComponent{assessmentA},
			},
			wantIssue:  false,
			wantReason: ReasonComponentsIncomplete,
		},
		{
			// Extra completions are harmless: the gate is "all declared are
			// complete", not "completed set equals declared set".
			name: "extra undeclared completions do not block: issue",
			req:  &CompletionRequirement{Components: []CompletionComponent{assessmentA}},
			facts: CompletionFacts{
				ScorePercent:        95,
				CompletedComponents: []CompletionComponent{assessmentA, examE},
			},
			wantIssue:  true,
			wantReason: ReasonIssue,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EvaluateCertificate(awarding, c.req, c.facts)
			if got.Issue != c.wantIssue {
				t.Errorf("Issue: want %v, got %v (reason %s)", c.wantIssue, got.Issue, got.Reason)
			}
			if got.Reason != c.wantReason {
				t.Errorf("Reason: want %s, got %s", c.wantReason, got.Reason)
			}
		})
	}
}

// The withheld-reason trail is the whole point: CHO-2157 existed because a
// silently withheld certificate looked like success. A component gate that
// withholds without naming the reason would repeat that defect exactly.
func TestEvaluateCertificate_ComponentGateAlwaysGivesAReason(t *testing.T) {
	awarding := &CompletionPolicy{AwardsCertificate: true, PassingScorePct: 50}
	reqs := []*CompletionRequirement{
		nil,
		{},
		{Components: []CompletionComponent{{Kind: ComponentKindProject, Ref: "p-1"}}},
	}
	for _, req := range reqs {
		for _, facts := range []CompletionFacts{
			{},
			{ScorePercent: 100},
			{ScorePercent: 100, CompletedComponents: []CompletionComponent{{Kind: ComponentKindProject, Ref: "p-1"}}},
		} {
			if got := EvaluateCertificate(awarding, req, facts); got.Reason == "" {
				t.Errorf("every outcome must carry a reason; got none for req=%+v facts=%+v", req, facts)
			}
		}
	}
}
