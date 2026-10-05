// topup_repos_test.go — branch top-up for the in-memory adapter suite:
// nil-input no-ops, sort comparators with >1 element, multi-item list
// orderings, WITHDRAWN default-scope exclusion in WblRepo, fail-loud error
// propagation in the roster/directory/course stitches, and the constructor
// panics.
package inmem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
	"github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

// ---------------------------------------------------------------------------
// WblRepo — Get, ListByTenant state scoping, nil-safe Save
// ---------------------------------------------------------------------------

func TestWblRepo_SaveGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := inmem.NewWblRepo()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	p, err := wbl.NewPlacement(wbl.NewPlacementInput{
		TenantID:        tenantA,
		GCID:            gcidA,
		CourseID:        "01970000-0000-7000-8000-000000000099",
		HostOrgName:     "Acme Pte Ltd",
		SupervisorName:  "Ms. Tan",
		SupervisorEmail: "tan@acme.example",
		StartDate:       time.Now().UTC().Add(24 * time.Hour),
		EndDate:         time.Now().UTC().Add(60 * 24 * time.Hour),
		HoursRequired:   160,
	})
	if err != nil {
		t.Fatalf("NewPlacement: %v", err)
	}
	if err := r.Save(ctx, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, p.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != p.ID {
		t.Fatalf("round-trip mismatch")
	}
	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

// ListByTenant: the default scope (state=="") excludes WITHDRAWN; an explicit
// WITHDRAWN filter opts back in; any other explicit state scopes exactly.
func TestWblRepo_ListByTenant_StateScoping(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := inmem.NewWblRepo()

	scheduled := mustPlacement(t, "p-1", tenantA)
	inProgress := mustPlacement(t, "p-2", tenantA)
	if err := inProgress.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	withdrawn := mustPlacement(t, "p-3", tenantA)
	if err := withdrawn.Withdraw("learner dropped"); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	otherTenant := mustPlacement(t, "p-4", tenantB)

	for _, p := range []*wbl.Placement{scheduled, inProgress, withdrawn, otherTenant} {
		if err := r.Save(ctx, p); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	// Default scope: everything except WITHDRAWN.
	all, err := r.ListByTenant(ctx, tenantA, "")
	if err != nil {
		t.Fatalf("ListByTenant(default): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("default scope must exclude WITHDRAWN; got %d items", len(all))
	}
	// Sorted by ID ascending.
	if all[0].ID != "p-1" || all[1].ID != "p-2" {
		t.Fatalf("expected [p-1 p-2] by ID, got [%s %s]", all[0].ID, all[1].ID)
	}

	// Explicit WITHDRAWN scope opts back in.
	withdrawnOnly, err := r.ListByTenant(ctx, tenantA, wbl.PlacementStateWithdrawn)
	if err != nil {
		t.Fatalf("ListByTenant(withdrawn): %v", err)
	}
	if len(withdrawnOnly) != 1 || withdrawnOnly[0].ID != "p-3" {
		t.Fatalf("explicit WITHDRAWN scope failed; got %d items", len(withdrawnOnly))
	}

	// Explicit IN_PROGRESS scope.
	progressOnly, err := r.ListByTenant(ctx, tenantA, wbl.PlacementStateInProgress)
	if err != nil {
		t.Fatalf("ListByTenant(in_progress): %v", err)
	}
	if len(progressOnly) != 1 || progressOnly[0].ID != "p-2" {
		t.Fatalf("explicit state scope failed; got %d items", len(progressOnly))
	}
}

func mustPlacement(t *testing.T, id, tenantID string) *wbl.Placement {
	t.Helper()
	p, err := wbl.NewPlacement(wbl.NewPlacementInput{
		TenantID:        tenantID,
		GCID:            gcidA,
		CourseID:        "01970000-0000-7000-8000-000000000099",
		HostOrgName:     "Acme Pte Ltd",
		SupervisorName:  "Ms. Tan",
		SupervisorEmail: "tan@acme.example",
		StartDate:       time.Now().UTC().Add(24 * time.Hour),
		EndDate:         time.Now().UTC().Add(60 * 24 * time.Hour),
		HoursRequired:   160,
	})
	if err != nil {
		t.Fatalf("NewPlacement: %v", err)
	}
	p.ID = id
	return p
}

// ---------------------------------------------------------------------------
// FranchiseSatelliteRepo — nil Save + the CreatedAt-tie ID tiebreak in
// ListByOwner
// ---------------------------------------------------------------------------

func TestFranchiseSatelliteRepo_SaveNilIsNoop(t *testing.T) {
	t.Parallel()
	r := inmem.NewFranchiseSatelliteRepo()
	if err := r.Save(context.Background(), nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	if items, _ := r.ListByOwner(context.Background(), fsOwner); len(items) != 0 {
		t.Fatalf("store must stay empty after Save(nil); got %d", len(items))
	}
}

func TestFranchiseSatelliteRepo_ListByOwner_CreatedAtTieIDOrder(t *testing.T) {
	t.Parallel()
	r := inmem.NewFranchiseSatelliteRepo()
	ctx := context.Background()

	tie := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	m1 := mustMapping(t, fsOwner, fsSatellite)
	m1.CreatedAt = tie
	m2 := mustMapping(t, fsOwner, fsOther)
	m2.CreatedAt = tie
	if m1.ID == m2.ID {
		t.Fatal("test bug: mappings must have distinct ids")
	}
	if err := r.Save(ctx, m1); err != nil {
		t.Fatalf("Save m1: %v", err)
	}
	if err := r.Save(ctx, m2); err != nil {
		t.Fatalf("Save m2: %v", err)
	}

	out, err := r.ListByOwner(ctx, fsOwner)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2; got %d", len(out))
	}
	// Ascending ID on the CreatedAt tie.
	if out[0].ID > out[1].ID {
		t.Fatalf("expected ID-ascending tie order, got [%s %s]", out[0].ID, out[1].ID)
	}
}

func TestFranchiseSatelliteRepo_ListByOwner_OldestFirst(t *testing.T) {
	t.Parallel()
	r := inmem.NewFranchiseSatelliteRepo()
	ctx := context.Background()

	older := mustMapping(t, fsOwner, fsSatellite)
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := mustMapping(t, fsOwner, fsOther)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Saved out of order — the repo sorts oldest-first.
	if err := r.Save(ctx, newer); err != nil {
		t.Fatalf("Save newer: %v", err)
	}
	if err := r.Save(ctx, older); err != nil {
		t.Fatalf("Save older: %v", err)
	}

	out, err := r.ListByOwner(ctx, fsOwner)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(out) != 2 || out[0].ID != older.ID || out[1].ID != newer.ID {
		t.Fatalf("expected [older newer] by CreatedAt, got [%s %s]",
			out[0].ID, out[1].ID)
	}
}

// ---------------------------------------------------------------------------
// Nil-input no-ops for the remaining W4 Brick-B + webhook stores
// ---------------------------------------------------------------------------

func TestRemainingRepos_NilInputIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := inmem.NewIncidentRepo().Append(ctx, nil); err != nil {
		t.Fatalf("IncidentRepo.Append(nil): %v", err)
	}
	if err := inmem.NewInvigilatorRepo().Save(ctx, nil); err != nil {
		t.Fatalf("InvigilatorRepo.Save(nil): %v", err)
	}
	if err := inmem.NewSittingRepo().Save(ctx, nil); err != nil {
		t.Fatalf("SittingRepo.Save(nil): %v", err)
	}
	if err := inmem.NewSkillsFuturesRepo().Save(ctx, nil); err != nil {
		t.Fatalf("SkillsFuturesRepo.Save(nil): %v", err)
	}
	if err := inmem.NewExamWebhookEventRepo().Insert(ctx, nil); err != nil {
		t.Fatalf("ExamWebhookEventRepo.Insert(nil): %v", err)
	}
	if err := inmem.NewBookingRepo().ReserveSeatAndSave(ctx, reserveClassID, 1, nil); err != nil {
		t.Fatalf("BookingRepo.ReserveSeatAndSave(nil): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Multi-item sort comparators (previously exercised with ≤1 element)
// ---------------------------------------------------------------------------

func TestInmemInvigilatorRepo_ListBySitting_Sorted(t *testing.T) {
	t.Parallel()
	r := inmem.NewInvigilatorRepo()
	ctx := context.Background()
	v1 := mkInvig(t, sbTenantA, sbSit1, "019e2f93-d586-71b5-8c3d-e2b0d0d5ee01", exam.InvigilatorRankInvigilator)
	v2 := mkInvig(t, sbTenantA, sbSit1, "019e2f93-d586-71b5-8c3d-e2b0d0d5ee02", exam.InvigilatorRankChief)
	// Saved out of order — the repo sorts by ID.
	if err := r.Save(ctx, v2); err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	if err := r.Save(ctx, v1); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	list, err := r.ListBySitting(ctx, sbTenantA, sbSit1)
	if err != nil {
		t.Fatalf("ListBySitting: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2; got %d", len(list))
	}
	if list[0].ID > list[1].ID {
		t.Fatalf("expected ID-ascending order, got [%s %s]", list[0].ID, list[1].ID)
	}
}

func TestInmemSittingRepo_ListByExam_Sorted(t *testing.T) {
	t.Parallel()
	r := inmem.NewSittingRepo()
	ctx := context.Background()
	s1 := mkSitting(t, sbTenantA, sbExam1)
	s2 := mkSitting(t, sbTenantA, sbExam1)
	if err := r.Save(ctx, s2); err != nil {
		t.Fatalf("Save s2: %v", err)
	}
	if err := r.Save(ctx, s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}
	list, err := r.ListByExam(ctx, sbTenantA, sbExam1)
	if err != nil {
		t.Fatalf("ListByExam: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2; got %d", len(list))
	}
	if list[0].ID > list[1].ID {
		t.Fatalf("expected ID-ascending order, got [%s %s]", list[0].ID, list[1].ID)
	}
}

// ---------------------------------------------------------------------------
// CourseRosterRepo — nil-port panic + EnrollmentPort fail-loud
// ---------------------------------------------------------------------------

func TestCourseRosterRepo_NewWithNilEnrollments_Panics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when the enrollments port is nil")
		}
	}()
	inmem.NewCourseRosterRepoWithDirectory(nil, nil)
}

type failingEnrollmentList struct{}

func (failingEnrollmentList) ListByCourse(context.Context, string, string) ([]*delivery.Enrollment, error) {
	return nil, errors.New("enrollment port boom")
}

func TestCourseRosterRepo_EnrollmentError_BubblesUp(t *testing.T) {
	t.Parallel()
	repo := inmem.NewCourseRosterRepo(failingEnrollmentList{})
	if _, err := repo.ListByCourse(context.Background(), rosterTenant, rosterCourse); err == nil {
		t.Fatal("expected the EnrollmentPort error to bubble up (fail loud), got nil")
	}
}

// ---------------------------------------------------------------------------
// SkillsFuturesRepo — nil Save
// ---------------------------------------------------------------------------

func TestSkillsFuturesRepo_SaveNilIsNoop(t *testing.T) {
	t.Parallel()
	r := inmem.NewSkillsFuturesRepo()
	if err := r.Save(context.Background(), nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	if out, _ := r.ListByTenant(context.Background(), tenantA, ""); len(out) != 0 {
		t.Fatalf("store must stay empty; got %d", len(out))
	}
}

// ---------------------------------------------------------------------------
// EnrichedWblStore — constructor panic, fail-loud LookupNames/Get, blank-name
// stripping, and course-id dedup
// ---------------------------------------------------------------------------

func TestEnrichedWblStore_NewWithNilInner_Panics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when the inner WblStore is nil")
		}
	}()
	inmem.NewEnrichedWblStore(nil, nil, nil)
}

func TestEnrichedWblStore_LearnerNames_DirectoryErrorFailsLoud(t *testing.T) {
	t.Parallel()
	s := inmem.NewEnrichedWblStore(inmem.NewWblRepo(), failingDirectory{}, nil)
	names, err := s.LearnerNames(context.Background(), []string{gcidA})
	if err == nil {
		t.Fatal("expected LookupNames error to surface, got nil")
	}
	if names != nil {
		t.Fatalf("error path must return nil map; got %v", names)
	}
}

func TestEnrichedWblStore_LearnerNames_BlankNameStripped(t *testing.T) {
	t.Parallel()
	dir := directory.NewInMemUserDirectory()
	if err := dir.Upsert(context.Background(), directory.UserDirectoryEntry{
		GCID: gcidA, DisplayName: "   ", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed blank name: %v", err)
	}
	s := inmem.NewEnrichedWblStore(inmem.NewWblRepo(), dir, nil)
	names, err := s.LearnerNames(context.Background(), []string{gcidA})
	if err != nil {
		t.Fatalf("LearnerNames: %v", err)
	}
	if _, ok := names[gcidA]; ok {
		t.Fatalf("whitespace-only display name must be filtered out, got %q", names[gcidA])
	}
}

// failingCourseRepo returns an error from Get so CourseTitles' fail-loud path
// is exercised.
type failingCourseRepo struct{}

func (failingCourseRepo) Save(context.Context, *delivery.Course) error { return nil }
func (failingCourseRepo) Get(context.Context, string, string) (*delivery.Course, bool, error) {
	return nil, false, errors.New("course repo boom")
}
func (failingCourseRepo) ListByTenant(context.Context, string, int, int) ([]*delivery.Course, int, error) {
	return nil, 0, nil
}

func TestEnrichedWblStore_CourseTitles_RepoErrorFailsLoud(t *testing.T) {
	t.Parallel()
	s := inmem.NewEnrichedWblStore(inmem.NewWblRepo(), nil, failingCourseRepo{})
	titles, err := s.CourseTitles(context.Background(), tenantA, []string{"course-1"})
	if err == nil {
		t.Fatal("expected the CourseRepo.Get error to surface, got nil")
	}
	if titles != nil {
		t.Fatalf("error path must return nil map; got %v", titles)
	}
}

func TestEnrichedWblStore_CourseTitles_DedupesAndSkipsEmpties(t *testing.T) {
	t.Parallel()
	s := newSeededEnrichedStore(t)
	titles, err := s.CourseTitles(context.Background(), enrTenantID, []string{enrCourseID, enrCourseID, ""})
	if err != nil {
		t.Fatalf("CourseTitles: %v", err)
	}
	if titles[enrCourseID] != enrCourseTitle {
		t.Errorf("title=%q want %q", titles[enrCourseID], enrCourseTitle)
	}
}
