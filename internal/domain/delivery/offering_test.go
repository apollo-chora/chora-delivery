// Offering tests — the delivery-instance aggregate that carries delivery_type
// + the DRAFT→LAUNCHED→RUNNING→CONCLUDED→ARCHIVED lifecycle FSM (R+ four-mode
// refactor W1, per ADR-190). Promotes the prior Cohort skeleton.
//
// Invariants under test:
//   - offering_id is UUIDv7; constructed in DRAFT
//   - delivery_type ∈ {graduate, short, async}, set once at creation
//   - course_id required (opaque UUID; the reusable curriculum delivered)
//   - capacity ≥ 0 (0 = unbounded, e.g. async)
//   - FSM advances only along DRAFT→LAUNCHED→RUNNING→CONCLUDED
//   - Archive is allowed from any state, idempotent, and soft-deletes
package delivery_test

import (
	"errors"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// Course ids are stored CANONICAL (uuid.String() is lowercase), so these
// fixtures are lowercase: NewOffering normalises whatever spelling a caller
// pastes. offering_courseid_guard_test.go proves the normalisation itself.
const offeringCourseA = "01970000-0000-7000-8000-0000000000c0"

const offeringCourseB = "01970000-0000-7000-8000-0000000000c1"

func validOfferingInput() domain.NewOfferingInput {
	return domain.NewOfferingInput{
		TenantID:     tenantA,
		CourseIDs:    []string{offeringCourseA},
		DeliveryType: domain.DeliveryTypeGraduate,
		Label:        "2026 Spring Cohort",
		Capacity:     30,
	}
}

func TestNewOffering_DefaultsToDraft(t *testing.T) {
	t.Parallel()
	o, err := domain.NewOffering(validOfferingInput())
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	if o.ID == "" {
		t.Fatalf("expected a generated UUIDv7 ID")
	}
	if o.State != domain.OfferingStateDraft {
		t.Fatalf("fresh offering must be DRAFT, got %q", o.State)
	}
	if o.DeliveryType != domain.DeliveryTypeGraduate {
		t.Fatalf("delivery_type mismatch: %q", o.DeliveryType)
	}
	if o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() {
		t.Fatalf("timestamps must be set")
	}
	if o.DeletedAt != nil {
		t.Fatalf("fresh offering must not be soft-deleted")
	}
	if o.LaunchedAt != nil || o.ConcludedAt != nil || o.ArchivedAt != nil {
		t.Fatalf("lifecycle timestamps must be nil on a DRAFT offering")
	}
}

func TestNewOffering_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*domain.NewOfferingInput)
		wantErr error
	}{
		{"missing tenant", func(in *domain.NewOfferingInput) { in.TenantID = "  " }, domain.ErrOfferingTenantRequired},
		{"nil courses", func(in *domain.NewOfferingInput) { in.CourseIDs = nil }, domain.ErrOfferingCourseRequired},
		{"all-blank courses", func(in *domain.NewOfferingInput) { in.CourseIDs = []string{"", "  "} }, domain.ErrOfferingCourseRequired},
		{"missing label", func(in *domain.NewOfferingInput) { in.Label = "   " }, domain.ErrOfferingLabelRequired},
		{"invalid delivery_type", func(in *domain.NewOfferingInput) { in.DeliveryType = "weekend" }, domain.ErrOfferingDeliveryTypeInvalid},
		{"empty delivery_type", func(in *domain.NewOfferingInput) { in.DeliveryType = "" }, domain.ErrOfferingDeliveryTypeInvalid},
		{"negative capacity", func(in *domain.NewOfferingInput) { in.Capacity = -1 }, domain.ErrOfferingCapacityNegative},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := validOfferingInput()
			tc.mutate(&in)
			_, err := domain.NewOffering(in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestNewOffering_MultipleCourses_DedupAndPrimary(t *testing.T) {
	t.Parallel()
	in := validOfferingInput()
	// Duplicates + blanks + surrounding space; order must be preserved, first wins.
	in.CourseIDs = []string{offeringCourseA, "  ", offeringCourseB, offeringCourseA, " " + offeringCourseB + " "}
	o, err := domain.NewOffering(in)
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	if len(o.CourseIDs) != 2 {
		t.Fatalf("expected 2 deduped courses, got %v", o.CourseIDs)
	}
	if o.CourseIDs[0] != offeringCourseA || o.CourseIDs[1] != offeringCourseB {
		t.Fatalf("order not preserved / not trimmed: %v", o.CourseIDs)
	}
	if o.PrimaryCourseID() != offeringCourseA {
		t.Fatalf("primary must be the first course, got %q", o.PrimaryCourseID())
	}
}

func TestOffering_PrimaryCourseID_EmptyAggregate(t *testing.T) {
	t.Parallel()
	var o domain.Offering
	if o.PrimaryCourseID() != "" {
		t.Fatalf("empty aggregate must yield empty primary, got %q", o.PrimaryCourseID())
	}
}

func TestNewOffering_AllDeliveryTypes(t *testing.T) {
	t.Parallel()
	for _, dt := range []domain.DeliveryType{
		domain.DeliveryTypeGraduate, domain.DeliveryTypeShort, domain.DeliveryTypeAsync,
	} {
		in := validOfferingInput()
		in.DeliveryType = dt
		in.Capacity = 0 // unbounded — legitimate (esp. async)
		o, err := domain.NewOffering(in)
		if err != nil {
			t.Fatalf("delivery_type %q with capacity 0 should be valid: %v", dt, err)
		}
		if o.DeliveryType != dt {
			t.Fatalf("delivery_type mismatch: want %q got %q", dt, o.DeliveryType)
		}
	}
}

func TestDeliveryType_IsValid(t *testing.T) {
	t.Parallel()
	valid := []domain.DeliveryType{domain.DeliveryTypeGraduate, domain.DeliveryTypeShort, domain.DeliveryTypeAsync}
	for _, dt := range valid {
		if !dt.IsValid() {
			t.Fatalf("%q should be valid", dt)
		}
	}
	for _, dt := range []domain.DeliveryType{"", "GRADUATE", "weekend", "exam"} {
		if dt.IsValid() {
			t.Fatalf("%q should be invalid", dt)
		}
	}
}

func TestOfferingState_IsValid(t *testing.T) {
	t.Parallel()
	valid := []domain.OfferingState{
		domain.OfferingStateDraft, domain.OfferingStateLaunched,
		domain.OfferingStateRunning, domain.OfferingStateConcluded, domain.OfferingStateArchived,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Fatalf("%q should be valid", s)
		}
	}
	for _, s := range []domain.OfferingState{"", "draft", "OPEN", "CANCELLED"} {
		if s.IsValid() {
			t.Fatalf("%q should be invalid", s)
		}
	}
}

func TestOffering_HappyPathFSM(t *testing.T) {
	t.Parallel()
	o, err := domain.NewOffering(validOfferingInput())
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	if err := o.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if o.State != domain.OfferingStateLaunched || o.LaunchedAt == nil {
		t.Fatalf("after Launch want LAUNCHED + LaunchedAt set, got %q / %v", o.State, o.LaunchedAt)
	}
	if err := o.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if o.State != domain.OfferingStateRunning {
		t.Fatalf("after Start want RUNNING, got %q", o.State)
	}
	if err := o.Conclude(); err != nil {
		t.Fatalf("Conclude: %v", err)
	}
	if o.State != domain.OfferingStateConcluded || o.ConcludedAt == nil {
		t.Fatalf("after Conclude want CONCLUDED + ConcludedAt set, got %q / %v", o.State, o.ConcludedAt)
	}
}

func TestOffering_IllegalTransitions(t *testing.T) {
	t.Parallel()
	// Start before Launch.
	o := mustOffering(t)
	if err := o.Start(); !errors.Is(err, domain.ErrOfferingNotLaunched) {
		t.Fatalf("Start on DRAFT: want ErrOfferingNotLaunched, got %v", err)
	}
	if o.State != domain.OfferingStateDraft {
		t.Fatalf("failed transition must not mutate state, got %q", o.State)
	}
	// Conclude before Running.
	o2 := mustOffering(t)
	_ = o2.Launch()
	if err := o2.Conclude(); !errors.Is(err, domain.ErrOfferingNotRunning) {
		t.Fatalf("Conclude on LAUNCHED: want ErrOfferingNotRunning, got %v", err)
	}
	// Re-launch a non-DRAFT offering.
	if err := o2.Launch(); !errors.Is(err, domain.ErrOfferingNotDraft) {
		t.Fatalf("Launch on LAUNCHED: want ErrOfferingNotDraft, got %v", err)
	}
}

func TestOffering_ArchiveFromAnyState(t *testing.T) {
	t.Parallel()
	// From DRAFT.
	o := mustOffering(t)
	o.Archive()
	if o.State != domain.OfferingStateArchived || o.ArchivedAt == nil || o.DeletedAt == nil {
		t.Fatalf("Archive from DRAFT: want ARCHIVED + ArchivedAt + DeletedAt set, got %q", o.State)
	}
	firstDeletedAt := *o.DeletedAt
	o.Archive() // idempotent
	if !o.DeletedAt.Equal(firstDeletedAt) {
		t.Fatalf("second Archive must be a no-op (DeletedAt unchanged)")
	}
	// From RUNNING.
	o2 := mustOffering(t)
	_ = o2.Launch()
	_ = o2.Start()
	o2.Archive()
	if o2.State != domain.OfferingStateArchived || o2.DeletedAt == nil {
		t.Fatalf("Archive from RUNNING: want ARCHIVED + soft-delete")
	}
}

func mustOffering(t *testing.T) *domain.Offering {
	t.Helper()
	o, err := domain.NewOffering(validOfferingInput())
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	return o
}

// -----------------------------------------------------------------------------
// Sections — intra-cohort sub-groups of a GRADUATE offering (W7 / D2).
//
// A Section has its own lead instructor / room / delivery dates but SHARES the
// cohort's curriculum + gradebook. NO independent lifecycle, NO own RLS, NO
// cross-aggregate references; it is a child entity reached only via the Offering
// root and persisted inside the Offering's JSONB snapshot (no own table).
// -----------------------------------------------------------------------------

func TestOffering_AddSection_AppendsTrimmedWithUUIDv7(t *testing.T) {
	t.Parallel()
	o := mustOffering(t) // graduate (validOfferingInput)
	sec, err := o.AddSection(domain.AddSectionInput{
		Name:               "  Section A  ",
		LeadInstructorGCID: "  00000000-0000-7000-8000-0000000000f1  ",
		Room:               "  Room 204  ",
		StartDate:          " 2026-09-01 ",
		EndDate:            " 2026-12-15 ",
	})
	if err != nil {
		t.Fatalf("AddSection: %v", err)
	}
	if sec.SectionID == "" {
		t.Fatalf("expected a generated UUIDv7 section id")
	}
	if len(o.Sections) != 1 || o.Sections[0].SectionID != sec.SectionID {
		t.Fatalf("section must be appended to the offering, got %#v", o.Sections)
	}
	if sec.Name != "Section A" || sec.Room != "Room 204" {
		t.Fatalf("string fields not trimmed: %#v", sec)
	}
	if sec.LeadInstructorGCID != "00000000-0000-7000-8000-0000000000f1" {
		t.Fatalf("lead not trimmed: %q", sec.LeadInstructorGCID)
	}
	if sec.StartDate != "2026-09-01" || sec.EndDate != "2026-12-15" {
		t.Fatalf("dates not trimmed: %q..%q", sec.StartDate, sec.EndDate)
	}
	if sec.CreatedAt.IsZero() || sec.UpdatedAt.IsZero() {
		t.Fatalf("section timestamps must be set")
	}
	// AddSection stamps section.CreatedAt + offering.UpdatedAt from one `now`.
	if !o.UpdatedAt.Equal(sec.CreatedAt) {
		t.Fatalf("offering UpdatedAt must be bumped to the section's creation time")
	}
}

func TestOffering_AddSection_PreservesAppendOrder(t *testing.T) {
	t.Parallel()
	o := mustOffering(t)
	a, _ := o.AddSection(domain.AddSectionInput{Name: "Morning"})
	b, _ := o.AddSection(domain.AddSectionInput{Name: "Evening"})
	if len(o.Sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(o.Sections))
	}
	if o.Sections[0].SectionID != a.SectionID || o.Sections[1].SectionID != b.SectionID {
		t.Fatalf("append order not preserved")
	}
	if a.SectionID == b.SectionID {
		t.Fatalf("each section must get a distinct id")
	}
}

func TestOffering_AddSection_RequiresName(t *testing.T) {
	t.Parallel()
	o := mustOffering(t)
	if _, err := o.AddSection(domain.AddSectionInput{Name: "   "}); !errors.Is(err, domain.ErrSectionNameRequired) {
		t.Fatalf("blank name: want ErrSectionNameRequired, got %v", err)
	}
	if len(o.Sections) != 0 {
		t.Fatalf("a rejected AddSection must not append, got %d", len(o.Sections))
	}
}

func TestOffering_AddSection_RejectsNonGraduate(t *testing.T) {
	t.Parallel()
	for _, dt := range []domain.DeliveryType{domain.DeliveryTypeShort, domain.DeliveryTypeAsync} {
		in := validOfferingInput()
		in.DeliveryType = dt
		in.Capacity = 0
		o, err := domain.NewOffering(in)
		if err != nil {
			t.Fatalf("NewOffering(%s): %v", dt, err)
		}
		if _, err := o.AddSection(domain.AddSectionInput{Name: "Group 1"}); !errors.Is(err, domain.ErrSectionNotGraduate) {
			t.Fatalf("%s offering: want ErrSectionNotGraduate, got %v", dt, err)
		}
		if len(o.Sections) != 0 {
			t.Fatalf("%s: a rejected AddSection must not append", dt)
		}
	}
}

func TestOffering_RenameSection(t *testing.T) {
	t.Parallel()
	o := mustOffering(t)
	sec, _ := o.AddSection(domain.AddSectionInput{Name: "Old Name"})

	if err := o.RenameSection(sec.SectionID, "  New Name  "); err != nil {
		t.Fatalf("RenameSection: %v", err)
	}
	if o.Sections[0].Name != "New Name" {
		t.Fatalf("rename did not apply (trimmed): %q", o.Sections[0].Name)
	}
	// Blank new name is rejected.
	if err := o.RenameSection(sec.SectionID, "  "); !errors.Is(err, domain.ErrSectionNameRequired) {
		t.Fatalf("blank rename: want ErrSectionNameRequired, got %v", err)
	}
	// Unknown id is rejected.
	if err := o.RenameSection("no-such-id", "X"); !errors.Is(err, domain.ErrSectionNotFound) {
		t.Fatalf("unknown id: want ErrSectionNotFound, got %v", err)
	}
}
