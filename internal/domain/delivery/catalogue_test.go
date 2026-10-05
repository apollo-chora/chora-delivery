// Catalogue tests — Phyllis MVP (Comic Ch5 P10 — "Udemy moment").
//
// The Catalogue exposes Courses with marketplace metadata (instructor name,
// enrolled count, price, tags, syllabus outline) and supports public-flag
// filtering, pagination, and free-text search across title + tags.
//
// These tests are RED-first per .claude/rules/development-execution.md.
package delivery_test

import (
	"strings"
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// PublicCourse construction
// -----------------------------------------------------------------------------

func TestNewPublicCourse_AssignsDefaults(t *testing.T) {
	t.Parallel()

	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:        tenantA,
		Title:           "CSPO Fundamentals",
		InstructorGCID:  gcidA,
		InstructorName:  "Phyllis Tan",
		PriceSGDCents:   20000,
		Public:          true,
		SFEligible:      false,
		Tags:            []string{"agile", "scrum"},
		SyllabusOutline: []string{"Module 1", "Module 2", "Module 3"},
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: unexpected error: %v", err)
	}
	if pc.ID == "" {
		t.Fatalf("expected non-empty UUIDv7 ID")
	}
	if pc.TenantID != tenantA {
		t.Fatalf("tenant_id mismatch")
	}
	if pc.PriceSGDCents != 20000 {
		t.Fatalf("price mismatch: got %d", pc.PriceSGDCents)
	}
	if pc.EnrolledCount != 0 {
		t.Fatalf("expected fresh course to have 0 enrolled, got %d", pc.EnrolledCount)
	}
	if !pc.Public {
		t.Fatalf("expected Public=true")
	}
	if pc.SyllabusOutlineCount() != 3 {
		t.Fatalf("expected 3 syllabus items, got %d", pc.SyllabusOutlineCount())
	}
	if pc.IsFree() {
		t.Fatalf("expected paid course (price > 0)")
	}
	if pc.DeletedAt != nil {
		t.Fatalf("expected fresh course not soft-deleted")
	}
}

func TestNewPublicCourse_FreeWhenPriceZero(t *testing.T) {
	t.Parallel()

	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "CSM Prep",
		InstructorGCID: gcidA,
		InstructorName: "Mr. Chen",
		PriceSGDCents:  0,
		Public:         true,
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: unexpected error: %v", err)
	}
	if !pc.IsFree() {
		t.Fatalf("expected price=0 to be free")
	}
}

func TestNewPublicCourse_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	_, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "",
		InstructorGCID: gcidA,
		InstructorName: "x",
	})
	if err == nil {
		t.Fatalf("expected error for empty title")
	}
}

func TestNewPublicCourse_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	_, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
	})
	if err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}

func TestNewPublicCourse_RejectsEmptyInstructorGCID(t *testing.T) {
	t.Parallel()
	_, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: "",
		InstructorName: "x",
	})
	if err == nil {
		t.Fatalf("expected error for empty instructor GCID")
	}
}

func TestNewPublicCourse_RejectsNegativePrice(t *testing.T) {
	t.Parallel()
	_, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
		PriceSGDCents:  -100,
	})
	if err == nil {
		t.Fatalf("expected error for negative price")
	}
}

// -----------------------------------------------------------------------------
// Catalogue search + filter + paginate
// -----------------------------------------------------------------------------

func newCatalogueWithSeed(t *testing.T) *domain.Catalogue {
	t.Helper()
	cat := domain.NewCatalogue()
	pcs := []domain.NewPublicCourseInput{
		{
			TenantID:        tenantA,
			Title:           "CSPO Fundamentals",
			InstructorGCID:  gcidA,
			InstructorName:  "Phyllis Tan",
			PriceSGDCents:   20000,
			Public:          false,
			Tags:            []string{"agile", "scrum", "product-owner"},
			SyllabusOutline: []string{"Intro", "Backlog", "Refinement"},
		},
		{
			TenantID:        tenantA,
			Title:           "CSM Prep",
			InstructorGCID:  gcidB,
			InstructorName:  "Mr. Chen",
			PriceSGDCents:   0,
			Public:          true,
			Tags:            []string{"agile", "scrum-master"},
			SyllabusOutline: []string{"Sprint", "Stand-up", "Retro"},
		},
		{
			TenantID:        tenantA,
			Title:           "PMP Crash Course",
			InstructorGCID:  gcidC,
			InstructorName:  "Ms. Lee",
			PriceSGDCents:   15000,
			Public:          true,
			Tags:            []string{"pmp", "project-management"},
			SyllabusOutline: []string{"PMBOK"},
		},
	}
	for _, in := range pcs {
		pc, err := domain.NewPublicCourse(in)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		cat.Save(pc)
	}
	return cat
}

func TestCatalogue_FilterPublicTrue(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	out, total := cat.Search(domain.CatalogueQuery{
		Public: domain.PublicFilterTrue,
		Page:   1,
		Per:    10,
	})
	if total != 2 {
		t.Fatalf("expected 2 public courses, got total=%d", total)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items, got %d", len(out))
	}
	for _, pc := range out {
		if !pc.Public {
			t.Fatalf("non-public course leaked: %q", pc.Title)
		}
	}
}

func TestCatalogue_FilterPublicFalse(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	out, total := cat.Search(domain.CatalogueQuery{
		Public: domain.PublicFilterFalse,
		Page:   1,
		Per:    10,
	})
	if total != 1 {
		t.Fatalf("expected 1 private course (CSPO), got total=%d", total)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 item, got %d", len(out))
	}
	if out[0].Public {
		t.Fatalf("public course leaked into private list")
	}
}

func TestCatalogue_FilterAny(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	out, total := cat.Search(domain.CatalogueQuery{
		Public: domain.PublicFilterAny,
		Page:   1,
		Per:    10,
	})
	if total != 3 {
		t.Fatalf("expected 3 total courses, got %d", total)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out))
	}
}

func TestCatalogue_SearchQ_MatchesTitleCaseInsensitive(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	out, total := cat.Search(domain.CatalogueQuery{
		Public: domain.PublicFilterAny,
		Q:      "cspo",
		Page:   1,
		Per:    10,
	})
	if total != 1 {
		t.Fatalf("expected 1 hit for q=cspo, got %d", total)
	}
	if !strings.Contains(strings.ToLower(out[0].Title), "cspo") {
		t.Fatalf("expected CSPO in title, got %q", out[0].Title)
	}
}

func TestCatalogue_SearchQ_MatchesTags(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	out, total := cat.Search(domain.CatalogueQuery{
		Public: domain.PublicFilterAny,
		Q:      "scrum",
		Page:   1,
		Per:    10,
	})
	// CSPO has "scrum" tag; CSM has "scrum-master" tag — both match
	// substring "scrum".
	if total != 2 {
		t.Fatalf("expected 2 hits for q=scrum, got %d", total)
	}
	_ = out
}

func TestCatalogue_SearchQ_NoMatch(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	_, total := cat.Search(domain.CatalogueQuery{
		Public: domain.PublicFilterAny,
		Q:      "underwater-basket-weaving",
		Page:   1,
		Per:    10,
	})
	if total != 0 {
		t.Fatalf("expected 0 hits, got %d", total)
	}
}

func TestCatalogue_Pagination(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	// seed 5 public courses
	for i := 0; i < 5; i++ {
		pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
			TenantID:       tenantA,
			Title:          "course",
			InstructorGCID: gcidA,
			InstructorName: "x",
			Public:         true,
		})
		cat.Save(pc)
	}
	page1, total := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterTrue, Page: 1, Per: 2})
	if total != 5 {
		t.Fatalf("total: expected 5, got %d", total)
	}
	if len(page1) != 2 {
		t.Fatalf("page1: expected 2 items, got %d", len(page1))
	}
	page2, _ := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterTrue, Page: 2, Per: 2})
	if len(page2) != 2 {
		t.Fatalf("page2: expected 2 items, got %d", len(page2))
	}
	page3, _ := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterTrue, Page: 3, Per: 2})
	if len(page3) != 1 {
		t.Fatalf("page3: expected 1 item (last), got %d", len(page3))
	}
	pageOver, _ := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterTrue, Page: 99, Per: 2})
	if len(pageOver) != 0 {
		t.Fatalf("page=99: expected 0 items, got %d", len(pageOver))
	}
}

func TestCatalogue_DefaultsPageAndPer(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	out, total := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterAny}) // page=0, per=0
	if total != 3 {
		t.Fatalf("expected total=3 with default paging, got %d", total)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out))
	}
}

func TestCatalogue_SoftDeletedCoursesHidden(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	// soft-delete the CSM course
	all, _ := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterAny})
	var csm *domain.PublicCourse
	for _, c := range all {
		if c.Title == "CSM Prep" {
			csm = c
			break
		}
	}
	if csm == nil {
		t.Fatalf("seed: CSM not found")
	}
	csm.SoftDelete()
	cat.Save(csm)

	_, total := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterAny})
	if total != 2 {
		t.Fatalf("expected 2 after soft-delete, got %d", total)
	}
}

func TestCatalogue_GetByID(t *testing.T) {
	t.Parallel()
	cat := newCatalogueWithSeed(t)
	all, _ := cat.Search(domain.CatalogueQuery{Public: domain.PublicFilterAny})
	first := all[0]
	got, ok := cat.Get(first.ID)
	if !ok {
		t.Fatalf("Get: expected hit")
	}
	if got.ID != first.ID {
		t.Fatalf("ID mismatch")
	}
	if _, ok := cat.Get("does-not-exist"); ok {
		t.Fatalf("Get: expected miss")
	}
}

func TestPublicCourse_IncrementEnrolled(t *testing.T) {
	t.Parallel()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "x",
		InstructorGCID: gcidA,
		InstructorName: "x",
	})
	if pc.EnrolledCount != 0 {
		t.Fatalf("expected 0, got %d", pc.EnrolledCount)
	}
	pc.IncrementEnrolled()
	pc.IncrementEnrolled()
	if pc.EnrolledCount != 2 {
		t.Fatalf("expected 2, got %d", pc.EnrolledCount)
	}
	if pc.UpdatedAt.Before(pc.CreatedAt) {
		t.Fatalf("UpdatedAt should be >= CreatedAt")
	}
}

func TestCatalogue_TenantIsolation(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	a, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "a",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Public:         true,
	})
	b, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       "01970000-0000-7000-8000-0000000000FF",
		Title:          "b",
		InstructorGCID: gcidA,
		InstructorName: "x",
		Public:         true,
	})
	cat.Save(a)
	cat.Save(b)
	out, total := cat.Search(domain.CatalogueQuery{
		TenantID: tenantA,
		Public:   domain.PublicFilterAny,
	})
	if total != 1 {
		t.Fatalf("expected tenant-scoped total=1, got %d", total)
	}
	if len(out) != 1 || out[0].TenantID != tenantA {
		t.Fatalf("tenant filter leaked")
	}
}

// -----------------------------------------------------------------------------
// ListByInstructor — by-instructor projection (closes debt #4 / A6)
// -----------------------------------------------------------------------------

func TestCatalogue_ListByInstructor_FiltersToInstructorAndTenant(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	mine1, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: tenantA, Title: "Mine 1", InstructorGCID: gcidA, Public: false,
	})
	mine2, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: tenantA, Title: "Mine 2", InstructorGCID: gcidA, Public: true,
	})
	other, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: tenantA, Title: "Not mine", InstructorGCID: "01970000-0000-7000-8000-000000000999", Public: true,
	})
	crossTenant, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: "01970000-0000-7000-8000-0000000000FF", Title: "Cross tenant", InstructorGCID: gcidA, Public: true,
	})
	cat.Save(mine1)
	cat.Save(mine2)
	cat.Save(other)
	cat.Save(crossTenant)

	out, total := cat.ListByInstructor(tenantA, gcidA, 1, 20)
	if total != 2 {
		t.Fatalf("total = %d, want 2 (only my courses in tenantA)", total)
	}
	if len(out) != 2 {
		t.Fatalf("items = %d, want 2", len(out))
	}
	for _, pc := range out {
		if pc.InstructorGCID != gcidA {
			t.Fatalf("leaked other instructor: %s", pc.InstructorGCID)
		}
		if pc.TenantID != tenantA {
			t.Fatalf("leaked other tenant: %s", pc.TenantID)
		}
	}
}

func TestCatalogue_ListByInstructor_HidesSoftDeleted(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	live, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: tenantA, Title: "Live", InstructorGCID: gcidA,
	})
	deleted, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: tenantA, Title: "Deleted", InstructorGCID: gcidA,
	})
	deleted.SoftDelete()
	cat.Save(live)
	cat.Save(deleted)

	out, total := cat.ListByInstructor(tenantA, gcidA, 1, 20)
	if total != 1 {
		t.Fatalf("total = %d, want 1 (soft-deleted must be hidden)", total)
	}
	if len(out) != 1 || out[0].Title != "Live" {
		t.Fatalf("soft-delete leaked into list: %+v", out)
	}
}

func TestCatalogue_ListByInstructor_EmptyTenantOrInstructor_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: tenantA, Title: "x", InstructorGCID: gcidA,
	})
	cat.Save(pc)

	if items, total := cat.ListByInstructor("", gcidA, 1, 20); len(items) != 0 || total != 0 {
		t.Fatalf("empty tenant must yield empty; got %d/%d", len(items), total)
	}
	if items, total := cat.ListByInstructor(tenantA, "", 1, 20); len(items) != 0 || total != 0 {
		t.Fatalf("empty instructor must yield empty; got %d/%d", len(items), total)
	}
}

func TestCatalogue_ListByInstructor_Pagination(t *testing.T) {
	t.Parallel()
	cat := domain.NewCatalogue()
	for i := 0; i < 5; i++ {
		pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
			TenantID: tenantA, Title: "C", InstructorGCID: gcidA,
		})
		cat.Save(pc)
	}
	out, total := cat.ListByInstructor(tenantA, gcidA, 2 /*page*/, 2 /*per*/)
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(out) != 2 {
		t.Fatalf("page-2 size = %d, want 2", len(out))
	}
	// page beyond → empty (not nil-pointer; the in-memory adapter returns []).
	out, _ = cat.ListByInstructor(tenantA, gcidA, 99, 10)
	if len(out) != 0 {
		t.Fatalf("page=99 size = %d, want 0", len(out))
	}
}

func TestInMemCatalogue_ListByInstructor_ProxiesToUnderlying(t *testing.T) {
	t.Parallel()
	in := domain.NewInMemCatalogue()
	pc, _ := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID: tenantA, Title: "x", InstructorGCID: gcidA,
	})
	_ = in.Save(nil, pc)
	items, total, err := in.ListByInstructor(nil, tenantA, gcidA, 1, 20)
	if err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d, items=%d; want 1/1", total, len(items))
	}
}
