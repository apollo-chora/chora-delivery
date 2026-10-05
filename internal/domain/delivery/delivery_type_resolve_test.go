// delivery_type_resolve_test — ResolveDeliveryType (CHO-2224).
//
// The resolver is the SINGLE copy of "which mode delivered this graded
// submission", shared by BOTH publish sites (the HTTP emit funnel and the OE
// grading inbox). It exists as one function precisely so the two sites cannot
// drift: two hand-maintained copies of the same lookup is how a routing table
// ends up inverted.
//
// Its contract is FAIL-SOFT BY DESIGN, and that is a deliberate,
// argued-for choice rather than laziness: delivery_type is METADATA on a graded
// outcome, while the score is the payload. Every miss (no port, no offering,
// a dead read) must yield "" so the grade still publishes. Refusing to publish a
// learner's grade because an offering lookup blipped would trade a real outcome
// for a nicety. This mirrors the ratified assessment_title precedent: "a miss is
// non-fatal - the title is a nicety, the score is not".
package delivery_test

import (
	"context"
	"errors"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// stubOfferings is a hand-rolled OfferingPort double. Only Get is exercised;
// the other three methods satisfy the interface and must never be called.
type stubOfferings struct {
	got      string // the id Get was called with (proves/disproves a lookup happened)
	calls    int
	offering *domain.Offering
	ok       bool
	err      error
}

func (s *stubOfferings) Get(_ context.Context, id string) (*domain.Offering, bool, error) {
	s.calls++
	s.got = id
	return s.offering, s.ok, s.err
}
func (s *stubOfferings) Save(context.Context, *domain.Offering) error { return nil }
func (s *stubOfferings) ListByTenant(context.Context, string) ([]*domain.Offering, error) {
	return nil, nil
}
func (s *stubOfferings) Search(context.Context, domain.OfferingQuery) (*domain.OfferingSearchPage, error) {
	return nil, nil
}

func TestResolveDeliveryType_ResolvesGraduate(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryTypeGraduate},
		ok:       true,
	}
	a := &domain.Assessment{ID: "ass-1", OfferingID: "off-1"}

	got := domain.ResolveDeliveryType(context.Background(), offerings, a)

	if got != "graduate" {
		t.Errorf("ResolveDeliveryType = %q; want %q", got, "graduate")
	}
	if offerings.got != "off-1" {
		t.Errorf("looked up offering id %q; want %q", offerings.got, "off-1")
	}
}

// TestResolveDeliveryType_ResolvesShort — the OTHER half of the §10.6 ">=2
// modes" proof. A resolver hardcoded to "graduate" passes the test above and
// still fails the capstone, so the second value is asserted independently.
func TestResolveDeliveryType_ResolvesShort(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-2", DeliveryType: domain.DeliveryTypeShort},
		ok:       true,
	}
	a := &domain.Assessment{ID: "ass-2", OfferingID: "off-2"}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "short" {
		t.Errorf("ResolveDeliveryType = %q; want %q", got, "short")
	}
}

func TestResolveDeliveryType_ResolvesAsync(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-3", DeliveryType: domain.DeliveryTypeAsync},
		ok:       true,
	}
	a := &domain.Assessment{ID: "ass-3", OfferingID: "off-3"}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "async" {
		t.Errorf("ResolveDeliveryType = %q; want %q", got, "async")
	}
}

// TestResolveDeliveryType_FreestandingAssessmentNeverLooksUp — assessments.
// offering_id is NULLABLE (migration 0033): a freestanding assessment has no
// Offering and therefore genuinely has NO delivery_type. The resolver must
// return "" AND must not attempt a lookup on a blank id (a blank-id read is a
// pointless round-trip that can only return a spurious answer).
func TestResolveDeliveryType_FreestandingAssessmentNeverLooksUp(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryTypeGraduate},
		ok:       true,
	}
	a := &domain.Assessment{ID: "ass-1", OfferingID: ""}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "" {
		t.Errorf("ResolveDeliveryType = %q; want \"\" (freestanding assessment has no mode to fabricate)", got)
	}
	if offerings.calls != 0 {
		t.Errorf("Get called %d times for a blank offering_id; want 0", offerings.calls)
	}
}

// TestResolveDeliveryType_WhitespaceOfferingIDIsFreestanding — a whitespace id
// is a blank id, not a lookup key.
func TestResolveDeliveryType_WhitespaceOfferingIDIsFreestanding(t *testing.T) {
	offerings := &stubOfferings{ok: true}
	a := &domain.Assessment{ID: "ass-1", OfferingID: "   "}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "" {
		t.Errorf("ResolveDeliveryType = %q; want \"\"", got)
	}
	if offerings.calls != 0 {
		t.Errorf("Get called %d times for a whitespace offering_id; want 0", offerings.calls)
	}
}

func TestResolveDeliveryType_NilAssessment(t *testing.T) {
	offerings := &stubOfferings{ok: true}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, nil); got != "" {
		t.Errorf("ResolveDeliveryType(nil assessment) = %q; want \"\"", got)
	}
	if offerings.calls != 0 {
		t.Errorf("Get called %d times for a nil assessment; want 0", offerings.calls)
	}
}

// TestResolveDeliveryType_NilPort — an unwired port must not panic. The grade
// still publishes, mode-less.
func TestResolveDeliveryType_NilPort(t *testing.T) {
	a := &domain.Assessment{ID: "ass-1", OfferingID: "off-1"}

	if got := domain.ResolveDeliveryType(context.Background(), nil, a); got != "" {
		t.Errorf("ResolveDeliveryType(nil port) = %q; want \"\"", got)
	}
}

// TestResolveDeliveryType_LookupErrorIsNonFatal — a dead/RLS-failed read must
// NOT invent a mode. Returning "" leaves the transcript honestly unattributed;
// the alternative (guessing) would write a WRONG mode into a learner's record.
//
// ⚠ The stub returns a POPULATED offering ALONGSIDE the error, deliberately.
// An earlier version returned nil there, which let the resolver's `o == nil`
// arm pass the test while the `err != nil` guard did nothing — mutation testing
// caught that the guard was decorative. A port that hands back both a value and
// an error is exactly what this guard exists for: the value is NOT trustworthy.
func TestResolveDeliveryType_LookupErrorIsNonFatal(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryTypeGraduate},
		ok:       true,
		err:      errors.New("boom"),
	}
	a := &domain.Assessment{ID: "ass-1", OfferingID: "off-1"}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "" {
		t.Errorf("ResolveDeliveryType(err) = %q; want \"\": a value returned alongside an error must never be trusted", got)
	}
}

// TestResolveDeliveryType_GenuineMiss — ok=false is a genuine absent row
// (distinct from an error per OfferingPort's contract).
//
// ⚠ Same construction as above: a populated offering with ok=false, so the
// `!ok` guard is the ONLY thing that can produce the pass.
func TestResolveDeliveryType_GenuineMiss(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryTypeShort},
		ok:       false,
	}
	a := &domain.Assessment{ID: "ass-1", OfferingID: "off-1"}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "" {
		t.Errorf("ResolveDeliveryType(miss) = %q; want \"\": ok=false is an absent row, whatever the port also returned", got)
	}
}

// TestResolveDeliveryType_NilOfferingWithOKTrue — a port that answers ok=true
// with a nil offering is misbehaving; the resolver must not dereference it.
func TestResolveDeliveryType_NilOfferingWithOKTrue(t *testing.T) {
	offerings := &stubOfferings{offering: nil, ok: true}
	a := &domain.Assessment{ID: "ass-1", OfferingID: "off-1"}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "" {
		t.Errorf("ResolveDeliveryType(nil offering, ok=true) = %q; want \"\"", got)
	}
}

// TestResolveDeliveryType_RejectsUnknownDeliveryType — the resolver emits only
// values the contract recognises (DeliveryType.IsValid). offerings.delivery_type
// is bare TEXT with no DB CHECK, so a garbage value IS reachable; propagating it
// would let a typo travel the wire and land in a learner's transcript as a
// "mode". "" is the honest answer for an unrecognisable value.
func TestResolveDeliveryType_RejectsUnknownDeliveryType(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryType("wat")},
		ok:       true,
	}
	a := &domain.Assessment{ID: "ass-1", OfferingID: "off-1"}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "" {
		t.Errorf("ResolveDeliveryType(unknown) = %q; want \"\"", got)
	}
}

// TestResolveDeliveryType_BlankDeliveryType — an offering with an empty
// delivery_type resolves to "", not to a blank stamp.
func TestResolveDeliveryType_BlankDeliveryType(t *testing.T) {
	offerings := &stubOfferings{
		offering: &domain.Offering{ID: "off-1", DeliveryType: domain.DeliveryType("")},
		ok:       true,
	}
	a := &domain.Assessment{ID: "ass-1", OfferingID: "off-1"}

	if got := domain.ResolveDeliveryType(context.Background(), offerings, a); got != "" {
		t.Errorf("ResolveDeliveryType(blank) = %q; want \"\"", got)
	}
}
