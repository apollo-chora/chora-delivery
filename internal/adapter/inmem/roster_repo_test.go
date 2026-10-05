// roster_repo_test.go — CourseRosterRepo name-projection stitch (Q3).
//
// Proves the directory LookupNames enrichment: a learner whose GCID has a
// directory row renders that display name; a learner without one falls back to
// the raw GCID (the existing feedback_no_stubs_real_wiring behaviour). The
// nil-directory constructor preserves the pre-Q3 GCID-only rendering.
package inmem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

const (
	rosterTenant = "tenant-roster-1"
	rosterCourse = "course-roster-1"
	gcidNamed    = "gcid-named-1"
	gcidUnnamed  = "gcid-unnamed-2"
)

func seedRosterEnrollments(t *testing.T) *delivery.InMemEnrollmentStore {
	t.Helper()
	reg := delivery.NewEnrollmentRegistry()
	if _, err := reg.Register(rosterTenant, rosterCourse, gcidNamed); err != nil {
		t.Fatalf("seed named: %v", err)
	}
	if _, err := reg.Register(rosterTenant, rosterCourse, gcidUnnamed); err != nil {
		t.Fatalf("seed unnamed: %v", err)
	}
	return delivery.NewInMemEnrollmentStoreFrom(reg)
}

func TestCourseRosterRepo_WithDirectory_PopulatesNamesWithGCIDFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	enroll := seedRosterEnrollments(t)
	dir := directory.NewInMemUserDirectory()
	if err := dir.Upsert(ctx, directory.UserDirectoryEntry{
		GCID: gcidNamed, DisplayName: "Alice Tan", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed directory: %v", err)
	}

	repo := inmem.NewCourseRosterRepoWithDirectory(enroll, dir)
	roster, err := repo.ListByCourse(ctx, rosterTenant, rosterCourse)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}

	names := map[string]string{}
	for _, l := range roster.Learners {
		names[l.GCID] = l.DisplayName
	}
	if names[gcidNamed] != "Alice Tan" {
		t.Errorf("named learner display_name = %q, want Alice Tan", names[gcidNamed])
	}
	if names[gcidUnnamed] != gcidUnnamed {
		t.Errorf("unnamed learner should fall back to GCID; got %q, want %q", names[gcidUnnamed], gcidUnnamed)
	}
}

// failingDirectory returns an error from LookupNames so the roster stitch's
// fail-loud path (bubble the real cause, don't silently drop to GCID-only) is
// exercised.
type failingDirectory struct{}

func (failingDirectory) Upsert(context.Context, directory.UserDirectoryEntry) error { return nil }
func (failingDirectory) LookupNames(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("directory boom")
}

func TestCourseRosterRepo_DirectoryError_BubblesUp(t *testing.T) {
	t.Parallel()
	enroll := seedRosterEnrollments(t)
	repo := inmem.NewCourseRosterRepoWithDirectory(enroll, failingDirectory{})
	if _, err := repo.ListByCourse(context.Background(), rosterTenant, rosterCourse); err == nil {
		t.Fatal("expected LookupNames error to bubble up (fail loud), got nil")
	}
}

func TestCourseRosterRepo_NilDirectory_GCIDFallbackForAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	enroll := seedRosterEnrollments(t)

	repo := inmem.NewCourseRosterRepo(enroll) // no directory → pre-Q3 behaviour
	roster, err := repo.ListByCourse(ctx, rosterTenant, rosterCourse)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	for _, l := range roster.Learners {
		if l.DisplayName != l.GCID {
			t.Errorf("nil-directory: learner %s display_name = %q, want GCID fallback", l.GCID, l.DisplayName)
		}
	}
}
