// Visibility tests — public catalogue visibility enum.
//
// Per the S4.3 brief (A-Content-Delivery): Visibility enum is the canonical
// access scope for a Course / PublicCourse:
//
//   - private      : only the creator + invited members
//   - tenant_only  : visible to all members of the same tenant
//   - public       : visible cross-tenant (Mr. Chen's CSM-Prep)
//
// The legacy boolean `Public` field stays as an additive denorm/derivation
// (Public == (Visibility == VisibilityPublic)) so existing call-sites do
// NOT break during the migration window.
package delivery_test

import (
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestVisibility_DefaultsToTenantOnlyOnEmpty(t *testing.T) {
	t.Parallel()
	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "Default visibility",
		InstructorGCID: gcidA,
		InstructorName: "x",
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: %v", err)
	}
	if pc.Visibility != domain.VisibilityTenantOnly {
		t.Fatalf("default visibility: expected tenant_only, got %q", pc.Visibility)
	}
}

func TestVisibility_PublicTrueImpliesPublicVisibility(t *testing.T) {
	t.Parallel()
	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Public:         true, // legacy bool path — must coerce to public visibility
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: %v", err)
	}
	if pc.Visibility != domain.VisibilityPublic {
		t.Fatalf("Public=true should coerce to VisibilityPublic, got %q", pc.Visibility)
	}
	if !pc.Public {
		t.Fatalf("Public=true should round-trip")
	}
}

func TestVisibility_ExplicitVisibilityOverridesPublicFlag(t *testing.T) {
	t.Parallel()
	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPrivate,
		Public:         true, // contradiction: explicit field wins
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: %v", err)
	}
	if pc.Visibility != domain.VisibilityPrivate {
		t.Fatalf("expected explicit Private to win, got %q", pc.Visibility)
	}
	if pc.Public {
		t.Fatalf("Public must reflect Visibility=Private (false)")
	}
}

func TestVisibility_RejectsUnknown(t *testing.T) {
	t.Parallel()
	_, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     "not-a-real-visibility",
	})
	if err == nil {
		t.Fatalf("expected error for unknown visibility")
	}
}

func TestVisibility_TransitionToPublicEmitsPublishedSignal(t *testing.T) {
	t.Parallel()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPrivate,
	})
	flipped := pc.SetVisibility(domain.VisibilityPublic)
	if !flipped {
		t.Fatalf("expected published-signal=true on private→public flip")
	}
	if pc.Visibility != domain.VisibilityPublic {
		t.Fatalf("Visibility not updated")
	}
	if !pc.Public {
		t.Fatalf("Public denorm not updated")
	}
}

func TestVisibility_TransitionWithinNonPublicNoSignal(t *testing.T) {
	t.Parallel()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPrivate,
	})
	flipped := pc.SetVisibility(domain.VisibilityTenantOnly)
	if flipped {
		t.Fatalf("expected published-signal=false (private→tenant_only)")
	}
}

func TestVisibility_TransitionFromPublicNoSignal(t *testing.T) {
	t.Parallel()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPublic,
	})
	flipped := pc.SetVisibility(domain.VisibilityTenantOnly)
	if flipped {
		t.Fatalf("expected published-signal=false (public→tenant_only)")
	}
	if pc.Public {
		t.Fatalf("Public denorm should clear after demoting from public")
	}
}

func TestVisibility_NoOpSameValue(t *testing.T) {
	t.Parallel()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPublic,
	})
	flipped := pc.SetVisibility(domain.VisibilityPublic)
	if flipped {
		t.Fatalf("same-value transition must NOT emit published-signal")
	}
}

// -----------------------------------------------------------------------------
// Cross-tenant catalogue visibility (the public/cross-tenant catalog story)
// -----------------------------------------------------------------------------

func TestCatalogue_PublicCoursesVisibleCrossTenant(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	// Tenant A's public CSM course (Mr. Chen).
	mrChen, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "CSM Prep",
		InstructorGCID: gcidB,
		InstructorName: "Mr. Chen",
		Visibility:     domain.VisibilityPublic,
	})
	cat.Save(mrChen)
	// Tenant B's private course — must not leak.
	bPriv, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       "01970000-0000-7000-8000-0000000000FF",
		Title:          "Tenant B private",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPrivate,
	})
	cat.Save(bPriv)

	// Cross-tenant query: a viewer from a different tenant should still
	// see Mr. Chen's public course AND not see Tenant B's private one.
	got, total := cat.Search(domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	})
	if total != 1 {
		t.Fatalf("expected exactly 1 public course cross-tenant, got total=%d", total)
	}
	if len(got) != 1 || got[0].Title != "CSM Prep" {
		t.Fatalf("expected CSM Prep, got %+v", got)
	}
}

func TestCatalogue_TenantOnlyHiddenAcrossTenants(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	tenantOnly, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "Tenant-only Course",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityTenantOnly,
	})
	cat.Save(tenantOnly)

	// Same tenant — visible.
	gotSame, totalSame := cat.Search(domain.CatalogueQuery{
		TenantID:   tenantA,
		Visibility: domain.VisibilityFilterAny,
	})
	if totalSame != 1 || len(gotSame) != 1 {
		t.Fatalf("same tenant should see its tenant_only course (got total=%d)", totalSame)
	}
	// Different tenant under "public-only" filter — must not see it.
	_, totalCross := cat.Search(domain.CatalogueQuery{
		TenantID:   "01970000-0000-7000-8000-0000000000FF",
		Visibility: domain.VisibilityFilterPublic,
	})
	if totalCross != 0 {
		t.Fatalf("tenant_only must not leak cross-tenant; got total=%d", totalCross)
	}
}

// VisibilityFilterTenantOrPublic — tenant_only scoped to active tenant + public.
func TestCatalogue_VisibilityFilterTenantOrPublic(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	pcA, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "A tenant-only",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityTenantOnly,
	})
	pcPriv, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "A private",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPrivate,
	})
	pcPublic, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       "01970000-0000-7000-8000-0000000000FF",
		Title:          "B public",
		InstructorGCID: gcidB,
		InstructorName: "x",
		Visibility:     domain.VisibilityPublic,
	})
	cat.Save(pcA)
	cat.Save(pcPriv)
	cat.Save(pcPublic)

	// Tenant-A viewer: tenant_only of A + public from anywhere; NOT private.
	got, total := cat.Search(domain.CatalogueQuery{
		TenantID:   tenantA,
		Visibility: domain.VisibilityFilterTenantOrPublic,
	})
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	titles := map[string]bool{}
	for _, pc := range got {
		titles[pc.Title] = true
	}
	if !titles["A tenant-only"] {
		t.Fatalf("expected 'A tenant-only' present")
	}
	if !titles["B public"] {
		t.Fatalf("expected 'B public' present")
	}
	if titles["A private"] {
		t.Fatalf("private must NOT surface in TenantOrPublic")
	}

	// Without TenantID set — tenant_only is unscoped (returns).
	_, total2 := cat.Search(domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterTenantOrPublic,
	})
	if total2 != 2 {
		t.Fatalf("expected 2 results (tenant_only + public when scope unset), got %d", total2)
	}
}

// SearchCursor — Relay-style cursor pagination
func TestCatalogue_SearchCursor_FirstPageHasNextPage(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	for i := 0; i < 5; i++ {
		pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
			TenantID:       tenantA,
			Title:          "course",
			InstructorGCID: gcidA,
			InstructorName: "x",
			Visibility:     domain.VisibilityPublic,
		})
		cat.Save(pc)
	}
	page := cat.SearchCursor(domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      2,
	})
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(page.Items))
	}
	if !page.HasNextPage {
		t.Fatalf("expected has_next_page=true")
	}
	if page.EndCursor == "" {
		t.Fatalf("expected non-empty end_cursor")
	}
	if page.Total != 5 {
		t.Fatalf("expected total=5, got %d", page.Total)
	}
}

func TestCatalogue_SearchCursor_AfterCursorAdvances(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	for i := 0; i < 5; i++ {
		pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
			TenantID:       tenantA,
			Title:          "course",
			InstructorGCID: gcidA,
			InstructorName: "x",
			Visibility:     domain.VisibilityPublic,
		})
		cat.Save(pc)
	}
	page1 := cat.SearchCursor(domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      2,
	})
	page2 := cat.SearchCursor(domain.CatalogueQuery{
		Visibility:  domain.VisibilityFilterPublic,
		First:       2,
		AfterCursor: page1.EndCursor,
	})
	if len(page2.Items) != 2 {
		t.Fatalf("page2 expected 2 items, got %d", len(page2.Items))
	}
	if !page2.HasNextPage {
		t.Fatalf("page2 should have next page")
	}
	page3 := cat.SearchCursor(domain.CatalogueQuery{
		Visibility:  domain.VisibilityFilterPublic,
		First:       2,
		AfterCursor: page2.EndCursor,
	})
	if len(page3.Items) != 1 {
		t.Fatalf("page3 expected 1 final item, got %d", len(page3.Items))
	}
	if page3.HasNextPage {
		t.Fatalf("page3 should be the last page")
	}
}

func TestCatalogue_SearchCursor_EmptyOnPastCursor(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPublic,
	})
	cat.Save(pc)
	page := cat.SearchCursor(domain.CatalogueQuery{
		Visibility:  domain.VisibilityFilterPublic,
		First:       10,
		AfterCursor: "ZZZZZZZZ-Z-ZZZZ-ZZZZ-ZZZZZZZZZZZZ",
	})
	if len(page.Items) != 0 {
		t.Fatalf("expected empty page past cursor")
	}
	if page.HasNextPage {
		t.Fatalf("expected no next page")
	}
	if page.EndCursor != "" {
		t.Fatalf("expected empty cursor")
	}
}

func TestCatalogue_SearchCursor_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	for i := 0; i < 25; i++ {
		pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
			TenantID:       tenantA,
			Title:          "x",
			InstructorGCID: gcidA,
			InstructorName: "x",
			Visibility:     domain.VisibilityPublic,
		})
		cat.Save(pc)
	}
	// First=0 -> default 20.
	page := cat.SearchCursor(domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	})
	if len(page.Items) != 20 {
		t.Fatalf("default First should yield 20, got %d", len(page.Items))
	}
	// First=999 -> clamped (we only have 25 so we get all 25).
	page2 := cat.SearchCursor(domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      999,
	})
	if len(page2.Items) != 25 {
		t.Fatalf("First clamped should still return 25, got %d", len(page2.Items))
	}
}

// PublicCourse.SoftDelete idempotent + SetVisibility unknown
func TestPublicCourse_SoftDelete_Idempotent(t *testing.T) {
	t.Parallel()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
	})
	pc.SoftDelete()
	first := *pc.DeletedAt
	pc.SoftDelete()
	if !pc.DeletedAt.Equal(first) {
		t.Fatalf("PublicCourse.SoftDelete must be idempotent")
	}
}

func TestPublicCourse_SetVisibility_RejectsUnknown(t *testing.T) {
	t.Parallel()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Visibility:     domain.VisibilityPrivate,
	})
	flipped := pc.SetVisibility("nope")
	if flipped {
		t.Fatalf("unknown visibility must NOT flip")
	}
	if pc.Visibility != domain.VisibilityPrivate {
		t.Fatalf("unknown visibility must NOT mutate state")
	}
}

func TestVisibility_IsValidExhaustive(t *testing.T) {
	t.Parallel()
	cases := map[domain.Visibility]bool{
		domain.VisibilityPrivate:    true,
		domain.VisibilityTenantOnly: true,
		domain.VisibilityPublic:     true,
		"":                          false,
		"public_v2":                 false,
		"PRIVATE":                   false,
	}
	for v, want := range cases {
		v, want := v, want
		t.Run(string(v), func(t *testing.T) {
			t.Parallel()
			if got := v.IsValid(); got != want {
				t.Fatalf("IsValid(%q) = %v, want %v", v, got, want)
			}
		})
	}
}
