// completion_requirement.go - the per-offering declaration of WHICH components
// this offering needs before it certifies anyone (CHO-2222, ADR-190 D1).
//
// ADR-190 D1: "a per-course CompletionRequirement declares which components THIS
// offering needs, assessment/exam/project outcome events roll up into a unified
// per-learner transcript owned by Consumption, and certificate issuance is a
// policy on top, component-optional by design".
//
// The declaration half of that sentence had never been written. CompletionPolicy
// shipped (a cut-score plus a coarse all-content boolean), so an offering could
// say "70% to pass" but could NOT say "the assessment AND the exam". A learner
// who passed the assessment of a two-component offering was certified without
// ever sitting its exam, and nothing recorded that anything was missing.
//
// # Why this rides the Offering JSONB snapshot
//
// CompletionPolicy (offering.go) is persisted inside the Offering aggregate's
// JSONB snapshot with no table of its own. A requirement is the same kind of
// thing: offering-scoped delivery configuration reached only via its root, never
// queried independently. It follows that precedent exactly, so it needs no
// migration and no FK.
//
// # Why per-OFFERING and not per-Course
//
// ADR-190 D1 puts delivery_type on the offering "never on Course, so one
// curriculum is reusable across all three modes". The same curriculum delivered
// as a graduate cohort and as a short course can demand different components, so
// the declaration has to live where the mode lives: on the offering.
//
// # Refs are FK-less
//
// A component's Ref is a cross-aggregate id (assessment / exam / project)
// carried without a foreign key and validated over events, per ADR-190 §3.5 #3.
// FK-less constrains the reference's MEANING, not its SHAPE: a Ref names a row
// in a UUID column, so it is canonicalised as a UUID (see canonicalRef).
package delivery

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CompletionComponentKind is the kind of outcome a declared component requires.
// The set is closed: an unrecognised kind is refused at declare time rather than
// skipped at evaluate time, because a component the evaluator cannot resolve
// would produce a gate that silently never fires.
type CompletionComponentKind string

// The three component kinds ADR-190 D1 names: "assessment/exam/project outcome
// events roll up into a unified per-learner transcript".
const (
	ComponentKindAssessment CompletionComponentKind = "assessment"
	ComponentKindExam       CompletionComponentKind = "exam"
	ComponentKindProject    CompletionComponentKind = "project"
)

// CompletionComponent is ONE declared component: a kind plus the FK-less id of
// the thing that must be completed. Identity is the (Kind, Ref) PAIR, not Ref
// alone, so an assessment and an exam may share an id space.
//
// Comparable by design: it is used as a map key when the gate checks the
// declared set against the completed set.
type CompletionComponent struct {
	Kind CompletionComponentKind
	Ref  string
}

// canonical returns the comparison form. Refs are trimmed on BOTH sides of the
// gate: a declared "a-1 " that never matched a resolved "a-1" would withhold
// every certificate on that offering forever, and would look like a policy
// decision rather than a whitespace bug.
func (c CompletionComponent) canonical() CompletionComponent {
	return CompletionComponent{Kind: c.Kind, Ref: strings.TrimSpace(c.Ref)}
}

// valid reports whether the kind is one ADR-190 D1 names.
func (k CompletionComponentKind) valid() bool {
	switch k {
	case ComponentKindAssessment, ComponentKindExam, ComponentKindProject:
		return true
	default:
		return false
	}
}

// Resolvable reports whether chora_delivery can actually resolve, per learner,
// whether a component of this kind is complete. It is NOT the same question as
// valid: a kind can be a legitimate part of the ADR-190 vocabulary and still
// have no completion source behind it.
//
//	assessment ⇒ submissions   (state = RELEASED and passed)
//	exam       ⇒ exam_results  (outcome = PASS)
//	project    ⇒ NOTHING       ← see below
//
// # Why project is not resolvable
//
// chora_delivery has no per-learner project-completion source. The only live
// project table is project_groups (mig 0022), and it does not model this: it is
// keyed by COURSE, its members live inside the JSONB blob, and its state
// (FORMING/ACTIVE/SUBMITTED/GRADED) belongs to the GROUP, not to a project a
// learner completes. An offering-level declaration cannot name a group, because
// each learner is in a DIFFERENT one - so a declared {project, group-id} would
// be satisfiable only by that single group's members and permanently unmet for
// everyone else on the offering. (The capstone_projects / capstone_submissions
// tables are legacy/wbl: migrations/legacy/README.md says they are deliberately
// never applied, and no live Go code reads them. A name in a migration is not a
// table.)
//
// So the declaration guard REFUSES to arm a project component
// (ErrCompletionComponentUnresolvable) and the pg resolver refuses to serve one.
// The alternative - accepting it and reporting "not complete" - is precisely the
// failure the exam lane's owner-ratified note rejected: a gate that can never
// fire (exam_result_inbox.go:15-21). Here it would be worse than inert, because
// EVERY certificate on that offering would be withheld, with a reason that reads
// like a policy decision rather than a missing feature.
//
// The kind itself stays valid - ADR-190 D1 names it, and removing it would be a
// silent architectural deviation. Only ARMING it is refused. Flip this to true
// the day a project-completion source exists, and not before.
func (k CompletionComponentKind) Resolvable() bool {
	switch k {
	case ComponentKindAssessment, ComponentKindExam:
		return true
	default:
		return false
	}
}

// CompletionRequirement is the offering-level declaration of the components this
// offering needs. nil or empty Components means "this offering declares no
// components", which is VACUOUSLY SATISFIED, not unmet: it is the live shape of
// every offering that predates this type, and withholding there would deny a
// certificate to every learner on every one of them.
type CompletionRequirement struct {
	Components []CompletionComponent
	UpdatedAt  time.Time
}

// Requirement guards.
var (
	// ErrCompletionComponentKind - the kind must be assessment, exam or project.
	ErrCompletionComponentKind = errors.New("delivery: completion component kind must be assessment, exam or project")
	// ErrCompletionComponentRef - a component must name what it refers to.
	ErrCompletionComponentRef = errors.New("delivery: completion component ref must not be blank")
	// ErrCompletionComponentRefInvalid - the ref is not a UUID. Distinct from
	// ErrCompletionComponentRef so the 400 tells an admin whether they OMITTED
	// the ref or MISTYPED it (the ErrOfferingCourseRequired /
	// ErrOfferingCourseInvalid split, offering.go, for the same reason).
	ErrCompletionComponentRefInvalid = errors.New("delivery: completion component ref must be a UUID")
	// ErrCompletionComponentUnresolvable - the kind is a legitimate ADR-190 kind
	// but nothing in chora_delivery can resolve a learner's completion of it, so
	// declaring one would withhold every certificate on the offering forever.
	// See CompletionComponentKind.Resolvable.
	ErrCompletionComponentUnresolvable = errors.New("delivery: completion component kind has no completion source in chora_delivery")
	// ErrCompletionComponentDuplicate - the same (kind, ref) was declared twice.
	ErrCompletionComponentDuplicate = errors.New("delivery: completion component (kind, ref) declared twice")
)

// canonicalRef trims and normalises a ref to its canonical UUID form.
//
// Why a UUID: a ref names a row in a UUID column (submissions.assessment_id,
// exam_results.exam_id) and the requirement lives in the Offering's JSONB blob,
// so - exactly like CourseIDs one file over - there is NO typed DB guard on it.
// The resolver casts the declared refs to uuid[], so a non-UUID ref would 22P02
// inside a Pub/Sub handler, NACKing every released grade on the offering, far
// from the request that stored it, and reading as an infra outage rather than
// the typo it is. This mutator is the one chokepoint every declaration crosses.
//
// Why the CANONICAL form and not the raw text: uuid.Parse is lenient (it takes
// "urn:uuid:<uuid>", "{<uuid>}" and the 32-char dashless form) and Postgres is
// not, so validating with Parse but storing raw would leave the same 22P02 door
// open through a narrower gap. Normalising also makes dedup honest: on raw text,
// "X" and "urn:uuid:X" survive as two entries for one component.
//
// This costs nothing to add now: no offering has ever stored a requirement (the
// type shipped with no write path at all), so there is no legacy ref to break.
func canonicalRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", ErrCompletionComponentRef
	}
	u, err := uuid.Parse(trimmed)
	if err != nil {
		return "", ErrCompletionComponentRefInvalid
	}
	return u.String(), nil
}

// SetCompletionRequirementInput is the value-bag for
// Offering.SetCompletionRequirement.
type SetCompletionRequirementInput struct {
	Components []CompletionComponent
}

// SetCompletionRequirement sets or replaces the offering's declared components.
//
// Guards, all fail-loud and all refusing the WHOLE input rather than dropping
// the offending entry: an unknown kind (ErrCompletionComponentKind), a kind
// nothing can resolve (ErrCompletionComponentUnresolvable), a blank ref
// (ErrCompletionComponentRef), a non-UUID ref (ErrCompletionComponentRefInvalid),
// a repeated (kind, ref) pair (ErrCompletionComponentDuplicate). Silently
// deduping or skipping would make the stored declaration disagree with what the
// admin typed, and a component dropped on the way in is a gate that cannot fire
// on the way out.
//
// The index-bearing errors point at the entry the admin actually typed, so the
// 400 is actionable rather than "something in your list is wrong".
//
// Declaring NO components is allowed and meaningful: it records "this offering
// requires nothing beyond its policy".
//
// Allowed in any lifecycle state, mirroring SetCompletionPolicy: this is a
// delivery policy, not course content.
func (o *Offering) SetCompletionRequirement(in SetCompletionRequirementInput) error {
	canonical := make([]CompletionComponent, 0, len(in.Components))
	seen := make(map[CompletionComponent]struct{}, len(in.Components))

	for i, raw := range in.Components {
		if !raw.Kind.valid() {
			return fmt.Errorf("%w (components[%d].kind=%q)", ErrCompletionComponentKind, i, raw.Kind)
		}
		// Refuse to ARM what the resolver cannot serve. A declared-but-
		// unresolvable component withholds every certificate on this offering,
		// forever, for a reason that reads like policy.
		if !raw.Kind.Resolvable() {
			return fmt.Errorf("%w (components[%d].kind=%q)", ErrCompletionComponentUnresolvable, i, raw.Kind)
		}
		ref, err := canonicalRef(raw.Ref)
		if err != nil {
			return fmt.Errorf("%w (components[%d].ref=%q)", err, i, raw.Ref)
		}
		c := CompletionComponent{Kind: raw.Kind, Ref: ref}
		if _, dup := seen[c]; dup {
			return fmt.Errorf("%w (components[%d]=%s/%s)", ErrCompletionComponentDuplicate, i, c.Kind, c.Ref)
		}
		seen[c] = struct{}{}
		canonical = append(canonical, c)
	}

	now := time.Now().UTC()
	o.CompletionRequirement = &CompletionRequirement{
		Components: canonical,
		UpdatedAt:  now,
	}
	o.UpdatedAt = now
	return nil
}

// missingComponents returns the declared components the learner has NOT
// completed, in declaration order. Empty means every declared component is done
// (vacuously true when nothing is declared).
//
// The gate is "every DECLARED component is complete", not "the completed set
// equals the declared set": completing something extra that was never asked for
// is harmless and must not block.
func (r *CompletionRequirement) missingComponents(completed []CompletionComponent) []CompletionComponent {
	if r == nil || len(r.Components) == 0 {
		return nil
	}
	done := make(map[CompletionComponent]struct{}, len(completed))
	for _, c := range completed {
		done[c.canonical()] = struct{}{}
	}
	var missing []CompletionComponent
	for _, want := range r.Components {
		if _, ok := done[want.canonical()]; !ok {
			missing = append(missing, want)
		}
	}
	return missing
}
