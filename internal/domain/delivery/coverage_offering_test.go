// coverage_offering_test.go — statement-coverage top-up for Offering section
// editing + completion policy + the offering search sort comparator.
package delivery

import (
	"errors"
	"testing"
	"time"
)

// covOffering builds a graduate-delivery offering for section/policy tests.
func covOffering(t *testing.T) *Offering {
	t.Helper()
	o, err := NewOffering(NewOfferingInput{
		TenantID:     "11111111-1111-7111-8111-111111111111",
		CourseIDs:    []string{"01970000-0000-7000-8000-0000000000c0"},
		DeliveryType: DeliveryTypeGraduate,
		Label:        "2026 Spring Cohort",
		Capacity:     30,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	return o
}

func TestOffering_UpdateSection_AppliesProvidedFields(t *testing.T) {
	o := covOffering(t)
	sec, err := o.AddSection(AddSectionInput{
		Name:               "  Morning  ",
		LeadInstructorGCID: "  inst-1  ",
		Room:               "  A-101  ",
		StartDate:          " 2026-01-01 ",
		EndDate:            " 2026-02-01 ",
	})
	if err != nil {
		t.Fatalf("AddSection: %v", err)
	}
	oldUpdated := o.UpdatedAt

	name := "Afternoon"
	room := "B-202"
	updated, err := o.UpdateSection(sec.SectionID, UpdateSectionInput{
		Name: &name,
		Room: &room,
	})
	if err != nil {
		t.Fatalf("UpdateSection: %v", err)
	}
	if updated.Name != "Afternoon" || updated.Room != "B-202" {
		t.Fatalf("updated fields: %+v", updated)
	}
	// Untouched fields are preserved.
	if updated.StartDate != "2026-01-01" || updated.EndDate != "2026-02-01" || updated.LeadInstructorGCID != "inst-1" {
		t.Fatalf("untouched fields must be preserved: %+v", updated)
	}
	// Naive timestamp equality can land in the same microsecond, so assert
	// monotonicity rather than strict advancement.
	if updated.UpdatedAt.Before(sec.CreatedAt) || o.UpdatedAt.Before(oldUpdated) {
		t.Fatalf("updated_at must not regress: section=%v offering=%v", updated.UpdatedAt, o.UpdatedAt)
	}

	// Blank provided name is rejected.
	blank := "   "
	if _, err := o.UpdateSection(sec.SectionID, UpdateSectionInput{Name: &blank}); !errors.Is(err, ErrSectionNameRequired) {
		t.Fatalf("blank name: want ErrSectionNameRequired, got %v", err)
	}
	// Unknown section rejects.
	n := "X"
	if _, err := o.UpdateSection("no-such-section", UpdateSectionInput{Name: &n}); !errors.Is(err, ErrSectionNotFound) {
		t.Fatalf("unknown section: want ErrSectionNotFound, got %v", err)
	}
	// All-pointer fields applied in one pass.
	lead := "  lead-2 "
	start := "2026-03-01"
	if s, err := o.UpdateSection(sec.SectionID, UpdateSectionInput{
		LeadInstructorGCID: &lead, StartDate: &start,
	}); err != nil || s.LeadInstructorGCID != "lead-2" || s.StartDate != "2026-03-01" {
		t.Fatalf("remaining fields: %+v err=%v", s, err)
	}
}

func TestOffering_SetCompletionPolicy(t *testing.T) {
	o := covOffering(t)
	if err := o.SetCompletionPolicy(SetCompletionPolicyInput{
		AwardsCertificate: true,
		PassingScorePct:   75,
		CertTitle:         "  Advanced Scrum Practitioner  ",
	}); err != nil {
		t.Fatalf("SetCompletionPolicy: %v", err)
	}
	if o.CompletionPolicy == nil {
		t.Fatal("completion policy must be set")
	}
	if !o.CompletionPolicy.AwardsCertificate || o.CompletionPolicy.PassingScorePct != 75 {
		t.Fatalf("policy fields: %+v", o.CompletionPolicy)
	}
	if o.CompletionPolicy.CertTitle != "Advanced Scrum Practitioner" {
		t.Fatalf("cert title must be trimmed: %q", o.CompletionPolicy.CertTitle)
	}
	if o.CompletionPolicy.UpdatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) {
		t.Fatalf("timestamps: policy=%v offering=%v", o.CompletionPolicy.UpdatedAt, o.UpdatedAt)
	}

	if err := o.SetCompletionPolicy(SetCompletionPolicyInput{PassingScorePct: -1}); !errors.Is(err, ErrCompletionPolicyScoreRange) {
		t.Fatalf("negative pct: want ErrCompletionPolicyScoreRange, got %v", err)
	}
	if err := o.SetCompletionPolicy(SetCompletionPolicyInput{PassingScorePct: 101}); !errors.Is(err, ErrCompletionPolicyScoreRange) {
		t.Fatalf("over-100 pct: want ErrCompletionPolicyScoreRange, got %v", err)
	}
}

func TestCompareOfferings(t *testing.T) {
	idA, idB := "id-a", "id-b"
	label := func(l string, created, updated time.Time) *Offering {
		return &Offering{ID: idA, Label: l, CreatedAt: created, UpdatedAt: updated}
	}
	base := time.Unix(1000, 0)

	// Label comparison.
	if c := compareOfferings(label("alpha", base, base), label("beta", base, base), OfferingSortLabel); c >= 0 {
		t.Fatalf("label asc: want <0, got %d", c)
	}
	// UpdatedAt comparison.
	if c := compareOfferings(label("x", base, base.Add(2*time.Hour)), label("x", base, base.Add(1*time.Hour)), OfferingSortUpdatedAt); c <= 0 {
		t.Fatalf("updated desc-by-field: want >0, got %d", c)
	}
	// CreatedAt default + equal-value ID tiebreak (id-b sorts after id-a).
	a := &Offering{ID: idB, Label: "same", CreatedAt: base}
	b := &Offering{ID: idA, Label: "same", CreatedAt: base}
	if c := compareOfferings(a, b, OfferingSortUpdatedAt); c <= 0 {
		t.Fatalf("id tiebreak: want >0 (id-a < id-b), got %d", c)
	}
	// Unknown field defaults to CreatedAt comparison.
	a2 := &Offering{ID: idB, Label: "same", CreatedAt: base}
	b2 := &Offering{ID: idA, Label: "same", CreatedAt: base.Add(1 * time.Hour)}
	if c := compareOfferings(a2, b2, "unknown-field"); c >= 0 {
		t.Fatalf("default createdAt: want <0, got %d", c)
	}
	// sortOfferings descending direction.
	items := []*Offering{
		{ID: "1", Label: "z", CreatedAt: time.Unix(1, 0)},
		{ID: "2", Label: "a", CreatedAt: time.Unix(2, 0)},
	}
	sortOfferings(items, OfferingSortLabel, OfferingSortDirDesc)
	if items[0].Label != "z" || items[1].Label != "a" {
		t.Fatalf("desc label sort: %+v", items)
	}
}
