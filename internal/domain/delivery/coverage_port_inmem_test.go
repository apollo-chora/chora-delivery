// coverage_port_inmem_test.go — statement-coverage top-up for the in-memory
// hexagonal adapters (InMemAssessmentRepo / InMemSubmissionRepo /
// InMemCatalogue / CertificationRegistry ctx-surface / InMemEnrollmentStore /
// InMemCourseCJ2Store). These methods back local dev + unit tests; the tests
// below exercise every persistence branch so the domain suite keeps a high
// statement bar.
package delivery

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// InMemAssessmentRepo — Get / Save / ListByInstructor / ListVisibleToLearner
// -----------------------------------------------------------------------------

func TestInMemAssessmentRepo_Save_RequiresNonNilWithID(t *testing.T) {
	repo := NewInMemAssessmentRepo()
	if err := repo.Save(context.Background(), nil); err != ErrAssessmentTenantRequired {
		t.Fatalf("nil save: want ErrAssessmentTenantRequired, got %v", err)
	}
	a, _ := NewAssessment(validAssessmentInput())
	a.ID = ""
	if err := repo.Save(context.Background(), a); err != ErrAssessmentTenantRequired {
		t.Fatalf("empty-ID save: want ErrAssessmentTenantRequired, got %v", err)
	}
}

func TestInMemAssessmentRepo_Get_ScopingAndSoftDelete(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemAssessmentRepo()
	in := validAssessmentInput()
	in.Title = "Get me"
	a, err := NewAssessment(in)
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := repo.Get(ctx, a.TenantID, a.ID)
	if err != nil || !ok || got == nil || got.ID != a.ID {
		t.Fatalf("Get hit: ok=%v err=%v", ok, err)
	}
	// Tenant mismatch denies.
	if _, ok, _ := repo.Get(ctx, "22222222-2222-7222-8222-222222222222", a.ID); ok {
		t.Fatal("cross-tenant Get must miss")
	}
	// Unknown id denies.
	if _, ok, _ := repo.Get(ctx, a.TenantID, "no-such-id"); ok {
		t.Fatal("unknown-id Get must miss")
	}
	// Soft-deleted denies.
	now := time.Now().UTC()
	a.DeletedAt = &now
	if _, ok, _ := repo.Get(ctx, a.TenantID, a.ID); ok {
		t.Fatal("soft-deleted Get must miss")
	}
}

func TestInMemAssessmentRepo_ListByInstructor(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemAssessmentRepo()
	tenant := validAssessmentInput().TenantID
	instructor := validAssessmentInput().InstructorGCID
	other := "00000000-0000-7000-8000-000000002999"

	mk := func(t *testing.T, tenantID, instructorID string, deleted bool) *Assessment {
		t.Helper()
		in := validAssessmentInput()
		in.TenantID = tenantID
		in.InstructorGCID = instructorID
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		if deleted {
			now := time.Now().UTC()
			a.DeletedAt = &now
		}
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return a
	}
	mk(t, tenant, instructor, false)
	mk(t, tenant, instructor, true)                                  // soft-deleted — hidden
	mk(t, tenant, other, false)                                      // other instructor — hidden
	mk(t, "22222222-2222-7222-8222-222222222222", instructor, false) // other tenant — hidden

	got, _, err := repo.ListByInstructor(ctx, tenant, instructor, 20, "")
	if err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("items: want 1, got %d", len(got))
	}
	// pageSize <= 0 defaults to 20; truncation caps at pageSize.
	got, _, err = repo.ListByInstructor(ctx, tenant, instructor, 0, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("default pageSize: want 1 item, got %d (err %v)", len(got), err)
	}
	mk(t, tenant, instructor, false)
	got, _, err = repo.ListByInstructor(ctx, tenant, instructor, 1, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("pageSize=1 truncation: want 1 item, got %d (err %v)", len(got), err)
	}
}

func TestInMemAssessmentRepo_ListVisibleToLearner(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemAssessmentRepo()
	tenant := validAssessmentInput().TenantID
	learner := "00000000-0000-7000-8000-000000001999"
	const offering = "019f3c77-c003-7611-a468-8a8f0e836401"

	offeringAttached := func(t *testing.T, offerID string) *Assessment {
		t.Helper()
		in := validAssessmentInput()
		in.OfferingID = offerID
		a, err := NewAssessment(in)
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		a.State = AssessmentStateOpen
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return a
	}

	// Default roster is EMPTY — an offering-attached assessment must be
	// denied to an unenrolled learner (authz gate defaults closed).
	_ = offeringAttached(t, offering)
	got, _, err := repo.ListVisibleToLearner(ctx, tenant, learner, 20, "")
	if err != nil {
		t.Fatalf("ListVisibleToLearner: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty roster must deny offering-attached; got %d items", len(got))
	}

	// After wiring a roster with the learner enrolled, the same assessment shows.
	roster := NewInMemCohortRoster()
	roster.Enrol(offering, learner)
	repo.SetRosterLink(roster)
	got, _, err = repo.ListVisibleToLearner(ctx, tenant, learner, 20, "")
	if err != nil {
		t.Fatalf("ListVisibleToLearner (enrolled): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("enrolled learner: want 1 item, got %d", len(got))
	}
	if got[0].OfferingID != offering {
		t.Fatalf("unexpected assessment returned: %+v", got[0])
	}

	// Roster outage refuses the whole listing (fail loud, never fails open).
	roster.Err = errors.New("roster outage")
	if _, _, err := repo.ListVisibleToLearner(ctx, tenant, learner, 20, ""); err == nil {
		t.Fatal("roster outage must surface an error, not an empty list")
	}
}

func TestInMemAssessmentRepo_MonitorCountsAndReleaseAll(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemAssessmentRepo()
	aRepo := NewInMemAssessmentRepo()
	subs := NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(subs) // covers SetSubmissionLink

	tenant := validAssessmentInput().TenantID
	assessment := func() *Assessment {
		t.Helper()
		a, err := NewAssessment(validAssessmentInput())
		if err != nil {
			t.Fatalf("NewAssessment: %v", err)
		}
		if err := aRepo.Save(ctx, a); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return a
	}

	// Unknown assessment → zero counts, no error.
	if c, err := aRepo.MonitorCounts(ctx, tenant, "no-such"); err != nil || c.TotalInvited != 0 {
		t.Fatalf("MonitorCounts unknown: %+v err=%v", c, err)
	}

	a := assessment()
	sub := func(state SubmissionState, total float64) *Submission {
		t.Helper()
		s, err := NewSubmission(validSubmissionInput())
		if err != nil {
			t.Fatalf("NewSubmission: %v", err)
		}
		s.AssessmentID = a.ID
		s.State = state
		s.MaxScore = 100
		s.TotalScore = total
		if err := subs.Save(ctx, s); err != nil {
			t.Fatalf("sub Save: %v", err)
		}
		return s
	}
	inProgress := sub(SubmissionStateInProgress, 0)
	pending := sub(SubmissionStatePendingOEGrading, 0)
	graded := sub(SubmissionStateGradedPendingRelease, 80) // 80%
	_ = sub(SubmissionStateReleased, 90)                   // 90%
	_ = inProgress
	_ = pending

	c, err := aRepo.MonitorCounts(ctx, tenant, a.ID)
	if err != nil {
		t.Fatalf("MonitorCounts: %v", err)
	}
	if c.TotalStarted != 4 || c.TotalSubmitted != 3 || c.TotalGraded != 2 || c.TotalReleased != 1 {
		t.Fatalf("counts mismatch: %+v", c)
	}
	if c.InProgressCount != 1 {
		t.Fatalf("in_progress: want 1, got %d", c.InProgressCount)
	}
	if c.AverageScorePct != 85 || !c.HasGradedSamples {
		t.Fatalf("average: want 85 + samples, got %f/%v", c.AverageScorePct, c.HasGradedSamples)
	}

	// ReleaseAllSubmissions with no linked repo → nil, nil.
	if ids, err := repo.ReleaseAllSubmissions(ctx, tenant, a.ID); err != nil || len(ids) != 0 {
		t.Fatalf("no-link ReleaseAll: %v %v", ids, err)
	}

	// The GRADED_PENDING_RELEASE sub releases (no review gate); pending-review one stays.
	graded.ReviewStatus = ReviewStatusNotRequired
	pendingReview := sub(SubmissionStateGradedPendingRelease, 70)
	pendingReview.ReviewStatus = ReviewStatusPendingReview
	ids, err := aRepo.ReleaseAllSubmissions(ctx, tenant, a.ID)
	if err != nil {
		t.Fatalf("ReleaseAllSubmissions: %v", err)
	}
	if len(ids) != 1 || ids[0] != graded.ID {
		t.Fatalf("released ids: want [%s], got %v", graded.ID, ids)
	}
	if graded.State != SubmissionStateReleased {
		t.Fatalf("graded sub must be RELEASED, got %s", graded.State)
	}
	if pendingReview.State != SubmissionStateGradedPendingRelease {
		t.Fatalf("pending-review sub must stay GRADED_PENDING_RELEASE, got %s", pendingReview.State)
	}
	if pending.State != SubmissionStatePendingOEGrading {
		t.Fatalf("pending-OE sub must be untouched, got %s", pending.State)
	}
}

// -----------------------------------------------------------------------------
// InMemSubmissionRepo — Get / GetInProgressByLearner / ListByLearner /
// CountAttempts / FindLatest / AppendGradeOverrides
// -----------------------------------------------------------------------------

func TestInMemSubmissionRepo_Save_RequiresID(t *testing.T) {
	repo := NewInMemSubmissionRepo()
	if err := repo.Save(context.Background(), nil); err != ErrSubmissionAssessmentRequired {
		t.Fatalf("nil save: want ErrSubmissionAssessmentRequired, got %v", err)
	}
	s, _ := NewSubmission(validSubmissionInput())
	s.ID = ""
	if err := repo.Save(context.Background(), s); err != ErrSubmissionAssessmentRequired {
		t.Fatalf("empty-ID save: want ErrSubmissionAssessmentRequired, got %v", err)
	}
}

func TestInMemSubmissionRepo_Get(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	s, _ := NewSubmission(validSubmissionInput())
	if err := repo.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, ok, _ := repo.Get(ctx, s.TenantID, s.ID); !ok || got.ID != s.ID {
		t.Fatal("Get hit failed")
	}
	if _, ok, _ := repo.Get(ctx, "other-tenant", s.ID); ok {
		t.Fatal("cross-tenant Get must miss")
	}
	now := time.Now().UTC()
	s.DeletedAt = &now
	if _, ok, _ := repo.Get(ctx, s.TenantID, s.ID); ok {
		t.Fatal("soft-deleted Get must miss")
	}
}

func TestInMemSubmissionRepo_GetInProgressByLearner(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	learner := validSubmissionInput().LearnerGCID

	started, _ := NewSubmission(validSubmissionInput())
	started.State = SubmissionStateStarted
	if err := repo.Save(ctx, started); err != nil {
		t.Fatalf("Save started: %v", err)
	}
	got, ok, err := repo.GetInProgressByLearner(ctx, started.TenantID, started.AssessmentID, learner)
	if err != nil || !ok || got.ID != started.ID {
		t.Fatalf("started sub must be found: ok=%v err=%v", ok, err)
	}

	// A SUBMITTED sub for a DIFFERENT assessment is not "in progress" (the
	// assessment is scoped into the lookup).
	submitted, _ := NewSubmission(validSubmissionInput())
	submitted.AssessmentID = "01985e7f-1234-7abc-8def-000000000099"
	submitted.State = SubmissionStateSubmitted
	if err := repo.Save(ctx, submitted); err != nil {
		t.Fatalf("Save submitted: %v", err)
	}
	if _, ok, _ := repo.GetInProgressByLearner(ctx, submitted.TenantID, submitted.AssessmentID, learner); ok {
		t.Fatal("SUBMITTED sub must not match GetInProgressByLearner")
	}
	// No match at all.
	if _, ok, _ := repo.GetInProgressByLearner(ctx, submitted.TenantID, "other-assessment", learner); ok {
		t.Fatal("no in-progress sub must miss")
	}
}

func TestInMemSubmissionRepo_ListByLearnerAndCount(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	learner := validSubmissionInput().LearnerGCID
	other := "00000000-0000-7000-8000-000000002999"

	one, _ := NewSubmission(validSubmissionInput())
	one.AttemptNumber = 1
	two, _ := NewSubmission(validSubmissionInput())
	two.AttemptNumber = 2
	foreign, _ := NewSubmission(validSubmissionInput())
	foreign.LearnerGCID = other
	for _, s := range []*Submission{one, two, foreign} {
		if err := repo.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	all, err := repo.ListByLearner(ctx, one.TenantID, learner)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListByLearner: want 2, got %d", len(all))
	}

	n, err := repo.CountAttemptsByLearner(ctx, one.TenantID, one.AssessmentID, learner)
	if err != nil {
		t.Fatalf("CountAttemptsByLearner: %v", err)
	}
	if n != 2 {
		t.Fatalf("attempts: want 2, got %d", n)
	}

	latest, ok, err := repo.FindLatestByLearner(ctx, one.TenantID, one.AssessmentID, learner)
	if err != nil || !ok {
		t.Fatalf("FindLatestByLearner: ok=%v err=%v", ok, err)
	}
	if latest.AttemptNumber != 2 {
		t.Fatalf("latest attempt: want 2, got %d", latest.AttemptNumber)
	}
	// Fresh learner with no attempts → miss.
	if _, ok, _ := repo.FindLatestByLearner(ctx, one.TenantID, one.AssessmentID, "nobody"); ok {
		t.Fatal("no-attempt learner must miss")
	}
}

func TestInMemSubmissionRepo_AppendGradeOverrides(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemSubmissionRepo()
	if err := repo.AppendGradeOverrides(ctx, "t", "sub-1", nil); err != nil {
		t.Fatalf("empty overrides must be a no-op: %v", err)
	}
	if repo.overrides != nil {
		t.Fatal("no-op append must not allocate the audit map")
	}
	o1 := GradeOverride{Field: "SCORE", OldValue: "1", NewValue: "2", At: time.Now()}
	o2 := GradeOverride{Field: "COMMENT", At: time.Now()}
	if err := repo.AppendGradeOverrides(ctx, "t", "sub-1", []GradeOverride{o1, o2}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := repo.AppendGradeOverrides(ctx, "t", "sub-1", []GradeOverride{o1}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if got := len(repo.overrides["sub-1"]); got != 3 {
		t.Fatalf("audit rows: want 3, got %d", got)
	}
}

func TestTrimGCID(t *testing.T) {
	if TrimGCID("  Alice  ") != "alice" {
		t.Fatalf("TrimGCID: want 'alice', got %q", TrimGCID("  Alice  "))
	}
}

// -----------------------------------------------------------------------------
// InMemCatalogue — adapter surface (From / Underlying / Get / Search /
// SearchCursor)
// -----------------------------------------------------------------------------

func TestInMemCatalogue_AdapterSurfaces(t *testing.T) {
	cat := NewCatalogue()
	pc, err := NewPublicCourse(NewPublicCourseInput{
		TenantID: "01970000-0000-7000-8000-000000000001",
		Title:    "Adapter course", InstructorGCID: "01970000-0000-7000-9000-000000000001",
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: %v", err)
	}
	cat.Save(pc)

	in := NewInMemCatalogueFrom(cat)
	if in.Underlying() != cat {
		t.Fatal("Underlying must return the wrapped catalogue")
	}
	if _, ok, err := in.Get(context.Background(), pc.ID); err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := in.Get(context.Background(), "nope"); ok {
		t.Fatal("Get miss expected")
	}
	items, total, err := in.Search(context.Background(), CatalogueQuery{TenantID: pc.TenantID, Public: PublicFilterAny})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("Search: items=%d total=%d err=%v", len(items), total, err)
	}
	page, err := in.SearchCursor(context.Background(), CatalogueQuery{TenantID: pc.TenantID, First: 20})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("SearchCursor: %+v err=%v", page, err)
	}
}

func TestCatalogue_SearchCursor_Windows(t *testing.T) {
	cat := NewCatalogue()
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		pc, err := NewPublicCourse(NewPublicCourseInput{
			TenantID: "01970000-0000-7000-8000-000000000001",
			Title:    "C", InstructorGCID: "01970000-0000-7000-9000-000000000001",
			Public: true,
		})
		if err != nil {
			t.Fatalf("NewPublicCourse: %v", err)
		}
		cat.Save(pc)
		ids = append(ids, pc.ID)
	}
	q := CatalogueQuery{Public: PublicFilterTrue}

	// First defaults to 20 when <= 0.
	page := cat.SearchCursor(q)
	if len(page.Items) != 5 || page.HasNextPage || page.Total != 5 {
		t.Fatalf("default first: %+v", page)
	}
	// The EndCursor is the lexicographically-greatest id of the returned page.
	wantEnd := ""
	for _, id := range ids {
		if id > wantEnd {
			wantEnd = id
		}
	}
	if page.EndCursor != wantEnd {
		t.Fatalf("end cursor: want %q, got %q", wantEnd, page.EndCursor)
	}
	// first clamps at 200.
	big := CatalogueQuery{Public: PublicFilterTrue, First: 999}
	if p := cat.SearchCursor(big); len(p.Items) != 5 {
		t.Fatalf("clamped first: want 5 items, got %d", len(p.Items))
	}

	// Windowed with AfterCursor: sort ids lexicographically — UUIDv7 ids minted
	// in the SAME millisecond are random-ordered, so creation order is not a
	// reliable cursor anchor. After cursor=sorted[1] the page is sorted[2],sorted[3].
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	win := CatalogueQuery{Public: PublicFilterTrue, First: 2, AfterCursor: sorted[1]}
	page = cat.SearchCursor(win)
	if len(page.Items) != 2 || !page.HasNextPage || page.Total != 5 {
		t.Fatalf("cursor page: %+v", page)
	}
	if page.Items[0].ID != sorted[2] || page.Items[1].ID != sorted[3] {
		t.Fatalf("cursor page items: want [%s %s], got [%s %s]", sorted[2], sorted[3], page.Items[0].ID, page.Items[1].ID)
	}
	if page.EndCursor != sorted[3] {
		t.Fatalf("cursor end: want %q, got %q", sorted[3], page.EndCursor)
	}
	// Cursor past the end → empty page, total preserved.
	after := CatalogueQuery{Public: PublicFilterTrue, First: 2, AfterCursor: sorted[len(sorted)-1]}
	page = cat.SearchCursor(after)
	if len(page.Items) != 0 || page.HasNextPage || page.Total != 5 {
		t.Fatalf("past-end cursor: %+v", page)
	}
	// Empty catalogue → empty page.
	empty := NewCatalogue().SearchCursor(CatalogueQuery{})
	if len(empty.Items) != 0 || empty.Total != 0 {
		t.Fatalf("empty catalogue: %+v", empty)
	}
}

// -----------------------------------------------------------------------------
// CertificationRegistry — ctx port surface + ListByTenant
// -----------------------------------------------------------------------------

func TestCertificationRegistry_CtxSurfaces(t *testing.T) {
	ctx := context.Background()
	reg := NewCertificationRegistry()
	score := 88
	cert, err := reg.IssueCtx(ctx, "t1", "learner-1", "course-1", &score, []string{"accomplished"})
	if err != nil {
		t.Fatalf("IssueCtx: %v", err)
	}
	if cert.LearnerID != "learner-1" || cert.CourseID != "course-1" {
		t.Fatalf("cert fields: %+v", cert)
	}

	if got, ok, _ := reg.GetCtx(ctx, "t1", cert.ID); !ok || got.ID != cert.ID {
		t.Fatal("GetCtx hit failed")
	}
	if _, ok, _ := reg.GetCtx(ctx, "other-tenant", cert.ID); ok {
		t.Fatal("GetCtx must be tenant-scoped")
	}
	if got, ok, _ := reg.GetByLearnerCourseCtx(ctx, "t1", "learner-1", "course-1"); !ok || got.ID != cert.ID {
		t.Fatal("GetByLearnerCourseCtx hit failed")
	}
	if _, ok, _ := reg.GetByLearnerCourseCtx(ctx, "other-tenant", "learner-1", "course-1"); ok {
		t.Fatal("GetByLearnerCourseCtx must be tenant-scoped")
	}
	list, err := reg.ListByTenantCtx(ctx, "t1", "", "")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByTenantCtx: len=%d err=%v", len(list), err)
	}
}

func TestCertificationRegistry_ListByTenant(t *testing.T) {
	reg := NewCertificationRegistry()
	if _, err := reg.Issue("t1", "learner-a", "course-a", nil); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := reg.Issue("t1", "learner-a", "course-b", nil); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// (gcid, course) uniqueness is keyed globally — a duplicate is refused.
	if _, err := reg.Issue("t2", "learner-a", "course-a", nil); err != ErrCertAlreadyIssued {
		t.Fatalf("duplicate issue: want ErrCertAlreadyIssued, got %v", err)
	}
	if _, err := reg.Issue("t2", "learner-b", "course-c", nil); err != nil {
		t.Fatalf("Issue t2: %v", err)
	}

	all, total := reg.ListByTenant("t1", "", "")
	if total != 2 || len(all) != 2 {
		t.Fatalf("all: total=%d len=%d", total, len(all))
	}
	if _, n := reg.ListByTenant("t1", "learner-a", "course-a"); n != 1 {
		t.Fatalf("learner+course filter: want 1, got %d", n)
	}
	if _, n := reg.ListByTenant("t1", "other-learner", ""); n != 0 {
		t.Fatalf("learner filter: want 0, got %d", n)
	}
	// Sorted by ID (UUIDv7 ⇒ creation order).
	if all[0].ID > all[1].ID {
		t.Fatalf("ListByTenant must sort by id asc: %s > %s", all[0].ID, all[1].ID)
	}
}

// -----------------------------------------------------------------------------
// InMemEnrollmentStore — constructor variants + port surface + by-course list
// -----------------------------------------------------------------------------

func TestInMemEnrollmentStore_Surfaces(t *testing.T) {
	ctx := context.Background()
	tenant := "11111111-1111-7111-8111-111111111111"
	course := "01985e7f-1234-7abc-8def-000000000a01"
	learner := "00000000-0000-7000-8000-000000001999"

	store := NewInMemEnrollmentStoreFrom(nil) // nil registry → fresh
	if store.Registry() == nil {
		t.Fatal("nil-registry constructor must allocate a registry")
	}
	if _, err := store.Register(ctx, tenant, course, learner); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Wrapping an existing pre-seeded registry keeps its rows.
	inner := NewEnrollmentRegistry()
	if _, err := inner.Register(tenant, course, learner); err != nil {
		t.Fatalf("inner Register: %v", err)
	}
	wrapped := NewInMemEnrollmentStoreFrom(inner)
	if wrapped.Registry() != inner {
		t.Fatal("Registry must return the wrapped registry")
	}
	// Register is idempotent on the natural key — the wrapped store's register
	// returns the SAME row the inner registry already minted.
	we, err := wrapped.Register(ctx, tenant, course, learner)
	if err != nil {
		t.Fatalf("wrapped Register: %v", err)
	}
	innerR, _ := inner.Get(we.ID)
	if innerR == nil {
		t.Fatal("idempotent Register must return the existing row")
	}

	list, err := wrapped.ListByGCID(ctx, tenant, learner)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByGCID: len=%d err=%v", len(list), err)
	}
	n, err := wrapped.CountByCourse(ctx, tenant, course)
	if err != nil || n != 1 {
		t.Fatalf("CountByCourse: n=%d err=%v", n, err)
	}

	// Cancelling with a tenant mismatch is a no-op; matching tenant cancels.
	if err := wrapped.Cancel(ctx, "other-tenant", we.ID); err != nil {
		t.Fatalf("Cancel mismatch: %v", err)
	}
	if got, _ := wrapped.Registry().Get(we.ID); got == nil || got.DeletedAt != nil {
		t.Fatal("cross-tenant Cancel must not cancel")
	}
	if err := wrapped.Cancel(ctx, tenant, we.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got, ok := wrapped.Registry().Get(we.ID); ok {
		t.Fatalf("cancelled enrollment must be hidden: %+v", got)
	}
}

func TestEnrollmentRegistry_ListByCourseAndReregister(t *testing.T) {
	tenant := "11111111-1111-7111-8111-111111111111"
	course := "01985e7f-1234-7abc-8def-000000000a01"
	courseB := "01985e7f-1234-7abc-8def-000000000b02"
	learner := "00000000-0000-7000-8000-000000001999"

	reg := NewEnrollmentRegistry()
	store := NewInMemEnrollmentStoreFrom(reg)
	e, err := store.Register(context.Background(), tenant, course, learner)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := store.Register(context.Background(), tenant, courseB, learner); err != nil {
		t.Fatalf("Register B: %v", err)
	}

	byCourse := reg.ListByCourse(tenant, course)
	if len(byCourse) != 1 || byCourse[0].ID != e.ID {
		t.Fatalf("registry ListByCourse: %+v", byCourse)
	}
	viaStore, err := store.ListByCourse(context.Background(), tenant, course)
	if err != nil || len(viaStore) != 1 {
		t.Fatalf("store ListByCourse: len=%d err=%v", len(viaStore), err)
	}

	// Cancel then re-register creates a FRESH row (the old key is resurrected).
	if err := store.Cancel(context.Background(), tenant, e.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(reg.ListByCourse(tenant, course)) != 0 {
		t.Fatal("cancelled row must drop out of ListByCourse")
	}
	fresh, err := store.Register(context.Background(), tenant, course, learner)
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if fresh.ID == e.ID {
		t.Fatal("re-register after cancel must mint a new row")
	}
	if len(reg.ListByCourse(tenant, course)) != 1 {
		t.Fatal("re-registered row must reappear in ListByCourse")
	}
}

// -----------------------------------------------------------------------------
// InMemCourseCJ2Store — ListByState + state-bucket move (exercises removeID)
// -----------------------------------------------------------------------------

func TestInMemCourseCJ2Store_ListByState(t *testing.T) {
	ctx := context.Background()
	store := NewInMemCourseCJ2Store()
	tenant := "11111111-1111-7111-8111-111111111111"
	mk := func(id, title string, state CourseState, deleted bool) *Course {
		c := &Course{
			ID: id, TenantID: tenant, Title: title, State: state,
			CreatedAt: time.Now().UTC(),
		}
		if deleted {
			now := time.Now().UTC()
			c.DeletedAt = &now
		}
		if err := store.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return c
	}
	mk("c-1", "Roadmap basics", CourseStateDraft, false)
	mk("c-2", "Advanced Driving", CourseStateDraft, false)
	mk("c-3", "Hidden Draft", CourseStateDraft, true) // soft-deleted — hidden
	mk("c-4", "Published One", CourseStatePublished, false)
	// Other tenant — must never leak into tenantA's draft list (Note: Save
	// deep copies, so mutating the caller's pointer can't cross tenants).
	foreign := &Course{
		ID: "c-5", TenantID: "22222222-2222-7222-8222-222222222222",
		Title: "Roadmap X", State: CourseStateDraft, CreatedAt: time.Now().UTC(),
	}
	if err := store.Save(ctx, foreign); err != nil {
		t.Fatalf("Save foreign: %v", err)
	}

	got, err := store.ListByState(ctx, tenant, CourseStateDraft, "", 0, 20)
	if err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("draft items: want 2, got %d", len(got))
	}
	// query filters by case-insensitive title substring.
	q, err := store.ListByState(ctx, tenant, CourseStateDraft, "road", 0, 20)
	if err != nil || len(q) != 1 || q[0].Title != "Roadmap basics" {
		t.Fatalf("query filter: %d items err=%v", len(q), err)
	}
	// offset past the end → nil slice.
	if past, _ := store.ListByState(ctx, tenant, CourseStateDraft, "", 99, 20); past != nil {
		t.Fatalf("offset past end: want nil, got %+v", past)
	}
	// windowing: offset 1 limit 1 → 1 item.
	win, _ := store.ListByState(ctx, tenant, CourseStateDraft, "", 1, 1)
	if len(win) != 1 {
		t.Fatalf("window: want 1 item, got %d", len(win))
	}
	// limit <= 0 defaults to 20; negative offset clamps.
	if all, _ := store.ListByState(ctx, tenant, CourseStateDraft, "", -1, 0); len(all) != 2 {
		t.Fatalf("defaults: want 2 items, got %d", len(all))
	}
}

func TestInMemCourseCJ2Store_Save_MovesStateBucket(t *testing.T) {
	ctx := context.Background()
	store := NewInMemCourseCJ2Store()
	tenant := "11111111-1111-7111-8111-111111111111"
	c := &Course{ID: "c-1", TenantID: tenant, Title: "T", State: CourseStateDraft, CreatedAt: time.Now().UTC()}
	if err := store.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c2 := &Course{ID: "c-2", TenantID: tenant, Title: "T2", State: CourseStateDraft, CreatedAt: time.Now().UTC()}
	if err := store.Save(ctx, c2); err != nil {
		t.Fatalf("Save c2: %v", err)
	}
	// Move c-1 DRAFT → PUBLISHED; the old DRAFT bucket must drop to 1 entry.
	c1 := *c
	c1.State = CourseStatePublished
	if err := store.Save(ctx, &c1); err != nil {
		t.Fatalf("Save state change: %v", err)
	}
	drafts, _ := store.ListByState(ctx, tenant, CourseStateDraft, "", 0, 20)
	if len(drafts) != 1 || drafts[0].ID != "c-2" {
		t.Fatalf("draft bucket after move: want [c-2], got %+v", drafts)
	}
	pub, _ := store.ListByState(ctx, tenant, CourseStatePublished, "", 0, 20)
	if len(pub) != 1 || pub[0].ID != "c-1" {
		t.Fatalf("published bucket: want [c-1], got %+v", pub)
	}
	// Save of an unchanged state does not duplicate the bucket entry.
	if err := store.Save(ctx, &c1); err != nil {
		t.Fatalf("Save idempotent: %v", err)
	}
	if pub, _ := store.ListByState(ctx, tenant, CourseStatePublished, "", 0, 20); len(pub) != 1 {
		t.Fatalf("re-save must not duplicate bucket entries, got %d", len(pub))
	}
	// removeID: drop two entries by matching one target.
	ids := removeID([]string{"a", "b", "c", "b"}, "b")
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "c" {
		t.Fatalf("removeID: got %v", ids)
	}
}
