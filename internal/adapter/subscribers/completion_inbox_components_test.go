// completion_inbox_components_test.go - CHO-2222 sub-phase 2.
//
// Sub-phase 1 landed the DECLARATION and the gate, and recorded a sequencing
// constraint at the call site: CompletedComponents was never resolved, so the
// gate was inert-by-luck (every offering's requirement was nil, because no write
// path could set one). The moment an admin could declare a component, an
// unresolved gate would withhold EVERY certificate on that offering.
//
// These tests cover the seam that closes that: the engine must actually ASK the
// resolver, hand it a tenant-bearing context, feed the answer to the gate, and
// fail LOUD rather than guess when it cannot get one.
package subscribers_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	compAssessRef = "019f5f10-1690-7c5c-9b03-58ed0804cf8e" // == compAssess, the released one
	compExamRef   = "019f5f20-2690-7c5c-9b03-58ed0804cf90"
)

var (
	declAssessment = domain.CompletionComponent{Kind: domain.ComponentKindAssessment, Ref: compAssessRef}
	declExam       = domain.CompletionComponent{Kind: domain.ComponentKindExam, Ref: compExamRef}
)

// stubComponents models pg.ComponentCompletionRepo faithfully, including the
// part that matters most: the real repo reads the tenant from the CONTEXT
// (rls.ApplySession), NOT from its tenantID argument, so it REFUSES an
// untenanted ctx before any SQL runs. A stub that ignored the ctx would supply
// what production does not and would pass over an engine that cannot make a
// single live call - the exact shape of the CHO-2153 / CHO-2157 traps.
type stubComponents struct {
	completed   []domain.CompletionComponent
	err         error
	calls       int
	gotDeclared []domain.CompletionComponent
	gotLearner  string
}

func (s *stubComponents) CompletedComponents(ctx context.Context, tenantID, learnerGCID string, declared []domain.CompletionComponent) ([]domain.CompletionComponent, error) {
	s.calls++
	s.gotDeclared = declared
	s.gotLearner = learnerGCID
	if s.err != nil {
		return nil, s.err
	}
	if tracing.TenantIDFromContext(ctx) == "" {
		return nil, rls.ErrNoTenantContext
	}
	return s.completed, nil
}

// offeringDeclaring builds the live offering shape carrying a declared
// requirement. It goes through the real mutator, so the refs must be UUIDs and
// the stored form is canonical - the same bytes the pg repo would rehydrate.
func offeringDeclaring(t *testing.T, policy *domain.CompletionPolicy, components ...domain.CompletionComponent) *domain.Offering {
	t.Helper()
	off := &domain.Offering{
		ID: compOffering, TenantID: compTenant,
		CourseIDs:        []string{compCourse},
		CompletionPolicy: policy,
	}
	if len(components) > 0 {
		if err := off.SetCompletionRequirement(domain.SetCompletionRequirementInput{Components: components}); err != nil {
			t.Fatalf("SetCompletionRequirement: %v", err)
		}
	}
	return off
}

// THE capstone case (masterplan 10.6): a two-component offering must not
// certify on the assessment alone.
func TestCompletion_DeclaredComponentMissing_WithholdsAndIssuesNothing(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	off := offeringDeclaring(t, awarding, declAssessment, declExam)
	components := &stubComponents{completed: []domain.CompletionComponent{declAssessment}} // exam NOT done
	certs := domain.NewCertificationRegistry()

	eng, _, sub := newCompletionEngine(t, 38, awarding, // 38/40 = 95%, well over the cut
		stubCourses{found: false}, stubContent{complete: true},
		&stubOfferings{o: off}, components, certs)

	if err := eng.Handle(context.Background(), releaseEnv("evt-comp-missing"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if components.calls != 1 {
		t.Fatalf("the engine must ASK the resolver exactly once; it asked %d times", components.calls)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Fatal("a learner who has not completed the DECLARED exam must NOT be certified on the " +
			"assessment alone")
	}
}

// The other direction: every declared component complete ⇒ the certificate
// issues. Without this, the gate could pass its sibling test by simply never
// issuing anything.
func TestCompletion_AllDeclaredComponentsComplete_Issues(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	off := offeringDeclaring(t, awarding, declAssessment, declExam)
	components := &stubComponents{completed: []domain.CompletionComponent{declAssessment, declExam}}
	certs := domain.NewCertificationRegistry()

	eng, _, sub := newCompletionEngine(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true},
		&stubOfferings{o: off}, components, certs)

	if err := eng.Handle(context.Background(), releaseEnv("evt-comp-all"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); !ok {
		t.Fatal("every declared component is complete and the score clears the cut: the certificate must issue")
	}
	// The resolver must be asked about the DECLARED set, for THIS learner.
	if len(components.gotDeclared) != 2 {
		t.Errorf("the resolver must receive the declared set (2 components), got %+v", components.gotDeclared)
	}
	if components.gotLearner != compLearner {
		t.Errorf("the resolver must be asked about the submitting learner; want %s, got %s", compLearner, components.gotLearner)
	}
}

// The engine hands the resolver a TENANT-BEARING context. The real repo reads
// the tenant from the ctx alone; a Pub/Sub push handler supplies a BARE one.
// This is the trap that has landed twice (CHO-2153, CHO-2157) - so it gets an
// assertion, not a comment. Handle is driven with context.Background() on
// purpose: that is exactly what production hands it.
func TestCompletion_ResolverReceivesATenantBearingContext(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	off := offeringDeclaring(t, awarding, declAssessment)
	components := &stubComponents{completed: []domain.CompletionComponent{declAssessment}}
	certs := domain.NewCertificationRegistry()

	eng, _, sub := newCompletionEngine(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true},
		&stubOfferings{o: off}, components, certs)

	// BARE ctx in, exactly as the push handler supplies it.
	if err := eng.Handle(context.Background(), releaseEnv("evt-comp-ctx"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v — the resolver was handed an untenanted context, so every component "+
			"lookup would fail RLS in production", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); !ok {
		t.Fatal("want the certificate issued, proving the resolver answered under a tenant-bearing ctx")
	}
}

// An infra failure in the resolver must NACK, never ack-and-drop. A dropped
// event here is a certificate the learner earned and no one ever hears about.
func TestCompletion_ResolverError_NACKsAndIssuesNothing(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	off := offeringDeclaring(t, awarding, declAssessment)
	boom := errors.New("pg: completed assessment components: connection reset")
	components := &stubComponents{err: boom}
	certs := domain.NewCertificationRegistry()

	eng, _, sub := newCompletionEngine(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true},
		&stubOfferings{o: off}, components, certs)

	err := eng.Handle(context.Background(), releaseEnv("evt-comp-boom"), releasePayload(sub))
	if err == nil {
		t.Fatal("a resolver failure must NACK (retry → DLQ), not ack-and-drop: an infra error " +
			"reported as 'component incomplete' silently withholds an EARNED certificate")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying failure must be wrapped, not replaced; got %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Error("nothing may be issued when the components could not be resolved")
	}
}

// A wiring bug must be LOUD. If an offering declares components and no reader is
// wired, the engine must refuse to decide - not fail open (certify without
// checking) and not fail closed-in-silence (withhold with a reason that reads
// like policy).
func TestCompletion_DeclaresComponentsButNoReaderWired_IsLoud(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	off := offeringDeclaring(t, awarding, declAssessment)
	certs := domain.NewCertificationRegistry()

	eng, _, sub := newCompletionEngine(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true},
		&stubOfferings{o: off}, nil, certs) // ← no reader wired

	err := eng.Handle(context.Background(), releaseEnv("evt-comp-unwired"), releasePayload(sub))
	if err == nil {
		t.Fatal("an offering that DECLARES components with no resolver wired must fail LOUD; " +
			"deciding anyway means either a certificate issued without checking the declaration, " +
			"or one withheld forever for a reason that looks like policy")
	}
	if !strings.Contains(err.Error(), "component") {
		t.Errorf("the error must name what is unwired; got %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Error("nothing may be issued while the gate cannot be evaluated")
	}
}

// Backward compatibility, and the reason the gate was safe to land inert: an
// offering that declares NOTHING is VACUOUSLY satisfied. It must not consult the
// resolver at all (no DB round-trip for the live shape of every offering today),
// and it must still certify.
func TestCompletion_NoComponentsDeclared_SkipsTheResolverAndStillIssues(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	components := &stubComponents{}
	certs := domain.NewCertificationRegistry()

	for _, tc := range []struct {
		name string
		off  *domain.Offering
	}{
		{"nil requirement (every offering that predates the type)", offeringDeclaring(t, awarding)},
		{"empty requirement (declared, but declares nothing)", func() *domain.Offering {
			o := offeringDeclaring(t, awarding)
			if err := o.SetCompletionRequirement(domain.SetCompletionRequirementInput{}); err != nil {
				t.Fatalf("SetCompletionRequirement: %v", err)
			}
			return o
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			components.calls = 0
			fresh := domain.NewCertificationRegistry()
			eng, _, sub := newCompletionEngine(t, 38, awarding,
				stubCourses{found: false}, stubContent{complete: true},
				&stubOfferings{o: tc.off}, components, fresh)

			if err := eng.Handle(context.Background(), releaseEnv("evt-vacuous-"+tc.name), releasePayload(sub)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if components.calls != 0 {
				t.Errorf("an offering that declares nothing must not consult the resolver; it was asked %d time(s)", components.calls)
			}
			if _, ok := fresh.GetByLearnerCourse(compLearner, compCourse); !ok {
				t.Error("a vacuous requirement must still certify")
			}
		})
	}
	_ = certs
}
