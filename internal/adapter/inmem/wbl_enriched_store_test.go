// wbl_enriched_store_test.go - unit tests for the EnrichedWblStore decorator
// (CHO-2335). The decorator composes a base wbl.WblStore with the
// user_directory name projection + the course-title repo so the list handler
// can resolve learner_name + course_title. Written test-first (RED) then drove
// wbl_enriched_store.go.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
	"github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

const (
	enrTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	enrLearnerGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50103"
	enrCourseID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50200"
	enrLearnerName = "Alice Tan"
	enrCourseTitle = "Intro to Robotics"
)

func newSeededEnrichedStore(t *testing.T) *inmem.EnrichedWblStore {
	t.Helper()
	dir := directory.NewInMemUserDirectory()
	if err := dir.Upsert(context.Background(), directory.UserDirectoryEntry{
		GCID:        enrLearnerGCID,
		DisplayName: enrLearnerName,
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed directory: %v", err)
	}
	courses := inmem.NewCourseRepo()
	c, err := delivery.NewCourse(enrTenantID, enrCourseTitle, nil, 30)
	if err != nil {
		t.Fatalf("seed course: %v", err)
	}
	c.ID = enrCourseID
	if err := courses.Save(context.Background(), c); err != nil {
		t.Fatalf("save course: %v", err)
	}
	return inmem.NewEnrichedWblStore(inmem.NewWblRepo(), dir, courses)
}

func TestEnrichedWblStore_ImplementsPorts(t *testing.T) {
	// Compile-time-ish guard: the decorator must satisfy BOTH the store port
	// (so it can back deps.Wbl) and the enricher port (so the handler can
	// type-assert the capability).
	var _ wbl.WblStore = (*inmem.EnrichedWblStore)(nil)
	var _ wbl.PlacementEnricher = (*inmem.EnrichedWblStore)(nil)
}

func TestEnrichedWblStore_LearnerNames_Resolves(t *testing.T) {
	s := newSeededEnrichedStore(t)
	names, err := s.LearnerNames(context.Background(), []string{enrLearnerGCID})
	if err != nil {
		t.Fatalf("LearnerNames: %v", err)
	}
	if names[enrLearnerGCID] != enrLearnerName {
		t.Errorf("name=%q want %q", names[enrLearnerGCID], enrLearnerName)
	}
}

func TestEnrichedWblStore_LearnerNames_MissAbsent(t *testing.T) {
	s := newSeededEnrichedStore(t)
	names, err := s.LearnerNames(context.Background(), []string{"unknown-gcid"})
	if err != nil {
		t.Fatalf("LearnerNames: %v", err)
	}
	if _, ok := names["unknown-gcid"]; ok {
		t.Errorf("unknown gcid should be absent from the map, got %q", names["unknown-gcid"])
	}
}

func TestEnrichedWblStore_CourseTitles_Resolves(t *testing.T) {
	s := newSeededEnrichedStore(t)
	titles, err := s.CourseTitles(context.Background(), enrTenantID, []string{enrCourseID})
	if err != nil {
		t.Fatalf("CourseTitles: %v", err)
	}
	if titles[enrCourseID] != enrCourseTitle {
		t.Errorf("title=%q want %q", titles[enrCourseID], enrCourseTitle)
	}
}

func TestEnrichedWblStore_CourseTitles_MissAbsent(t *testing.T) {
	s := newSeededEnrichedStore(t)
	titles, err := s.CourseTitles(context.Background(), enrTenantID, []string{"unknown-course"})
	if err != nil {
		t.Fatalf("CourseTitles: %v", err)
	}
	if _, ok := titles["unknown-course"]; ok {
		t.Errorf("unknown course should be absent from the map, got %q", titles["unknown-course"])
	}
}

func TestEnrichedWblStore_CourseTitles_CrossTenantAbsent(t *testing.T) {
	// The course belongs to enrTenantID; a different tenant must not resolve it
	// (CourseRepo.Get is tenant-scoped - defence in depth against a cross-tenant
	// title leak on the shared course map).
	s := newSeededEnrichedStore(t)
	titles, err := s.CourseTitles(context.Background(), "other-tenant", []string{enrCourseID})
	if err != nil {
		t.Fatalf("CourseTitles: %v", err)
	}
	if _, ok := titles[enrCourseID]; ok {
		t.Errorf("cross-tenant title must not resolve, got %q", titles[enrCourseID])
	}
}

func TestEnrichedWblStore_NilResolvers_EmptyMaps(t *testing.T) {
	// A decorator wired with nil resolvers degrades to empty maps (no panic) so
	// the list still renders with the raw-id fallback.
	s := inmem.NewEnrichedWblStore(inmem.NewWblRepo(), nil, nil)
	names, err := s.LearnerNames(context.Background(), []string{enrLearnerGCID})
	if err != nil {
		t.Fatalf("LearnerNames nil dir: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("names=%v want empty", names)
	}
	titles, err := s.CourseTitles(context.Background(), enrTenantID, []string{enrCourseID})
	if err != nil {
		t.Fatalf("CourseTitles nil courses: %v", err)
	}
	if len(titles) != 0 {
		t.Errorf("titles=%v want empty", titles)
	}
}

func TestEnrichedWblStore_ForwardsStore(t *testing.T) {
	// The embedded WblStore methods must forward to the inner store: a Save
	// followed by a ListByTenant on the decorator round-trips the placement.
	s := newSeededEnrichedStore(t)
	p, err := wbl.NewPlacement(wbl.NewPlacementInput{
		TenantID:        enrTenantID,
		GCID:            enrLearnerGCID,
		CourseID:        enrCourseID,
		HostOrgName:     "Acme Robotics Pte Ltd",
		SupervisorName:  "Ms. Tan",
		SupervisorEmail: "tan@acme.example",
		StartDate:       time.Now().UTC().Add(24 * time.Hour),
		EndDate:         time.Now().UTC().Add(60 * 24 * time.Hour),
		HoursRequired:   160,
	})
	if err != nil {
		t.Fatalf("NewPlacement: %v", err)
	}
	if err := s.Save(context.Background(), p); err != nil {
		t.Fatalf("Save (forwarded): %v", err)
	}
	items, err := s.ListByTenant(context.Background(), enrTenantID, "")
	if err != nil {
		t.Fatalf("ListByTenant (forwarded): %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d want 1", len(items))
	}
	if items[0].GCID != enrLearnerGCID {
		t.Errorf("gcid=%q want %q", items[0].GCID, enrLearnerGCID)
	}
}
