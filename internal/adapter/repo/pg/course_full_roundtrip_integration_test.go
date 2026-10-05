//go:build integration

// course_full_roundtrip_integration_test.go — real-Postgres round-trip for the
// core CourseRepo CJ#2 adapter. It writes a course with EVERY persisted field
// set to a DISTINCT value, reads it back, and compares every field.
//
// Why this gate exists: stub-querier unit tests cannot catch an omitted INSERT
// column — the value simply never reaches Postgres and the round-trip silently
// returns the zero value. Only a real database round-trip proves the column
// list in SQLUpsertCourseCJ2 matches the SELECT list in SQLSelectCJ2CourseByID.
//
// Run:
//
//	CHORA_TEST_DSN='postgres://chora_delivery_app_rw:chora@localhost:5432/chora_delivery?sslmode=disable' \
//	  go test -tags integration -run TestIntegration_CourseRepoCJ2_FullFieldRoundTrip ./internal/adapter/repo/pg/...
//
// Skips cleanly when CHORA_TEST_DSN is unset (see liveDB in integration_test.go).
package pg_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestIntegration_CourseRepoCJ2_FullFieldRoundTrip(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewCourseRepo(&liveTxRunner{pool: pool})

	tenantID := uuid.Must(uuid.NewV7()).String()
	courseID := uuid.Must(uuid.NewV7()).String()
	instructorGCID := uuid.Must(uuid.NewV7()).String()
	authorGCID := uuid.Must(uuid.NewV7()).String()
	atomA, atomB := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	testSetA, testSetB := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	coInstructor := uuid.Must(uuid.NewV7()).String()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM courses WHERE course_id = $1`, courseID)
	})

	createdAt := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	updatedAt := time.Date(2026, 4, 5, 6, 7, 8, 654321000, time.UTC)
	scheduledOpenAt := time.Date(2026, 5, 6, 7, 8, 9, 111111000, time.UTC)
	publishedAt := time.Date(2026, 6, 7, 8, 9, 10, 222222000, time.UTC)

	want := &domain.Course{
		ID:                 courseID,
		TenantID:           tenantID,
		InstructorGCID:     instructorGCID,
		Title:              "Distinct Title 42",
		Description:        "Distinct description with every field set",
		AtomIDs:            []string{atomA, atomB},
		PriceSGDCents:      12345,
		SFEligible:         true,
		MaxCapacity:        77,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
		State:              domain.CourseStatePublished,
		AuthorGCID:         authorGCID,
		TestSetIDs:         []string{testSetA, testSetB},
		InstructorGCIDs:    []string{coInstructor},
		LearningObjectives: []string{"objective one", "objective two"},
		PrerequisiteNotes:  []string{"prereq one", "prereq two"},
		ScheduledOpenAt:    &scheduledOpenAt,
		ReviewNotes:        "distinct review notes",
		PublishedAt:        &publishedAt,
	}

	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, want); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}

	got, ok, err := r.GetCJ2(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("GetCJ2: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("GetCJ2 returned no row for a just-written course")
	}

	// Every persisted field must round-trip verbatim.
	if got.ID != want.ID {
		t.Errorf("ID: got %q want %q", got.ID, want.ID)
	}
	if got.TenantID != want.TenantID {
		t.Errorf("TenantID: got %q want %q", got.TenantID, want.TenantID)
	}
	if got.InstructorGCID != want.InstructorGCID {
		t.Errorf("InstructorGCID: got %q want %q", got.InstructorGCID, want.InstructorGCID)
	}
	if got.AuthorGCID != want.AuthorGCID {
		t.Errorf("AuthorGCID: got %q want %q", got.AuthorGCID, want.AuthorGCID)
	}
	if got.Title != want.Title {
		t.Errorf("Title: got %q want %q", got.Title, want.Title)
	}
	if got.Description != want.Description {
		t.Errorf("Description: got %q want %q", got.Description, want.Description)
	}
	if !reflect.DeepEqual(got.AtomIDs, want.AtomIDs) {
		t.Errorf("AtomIDs: got %v want %v", got.AtomIDs, want.AtomIDs)
	}
	if !reflect.DeepEqual(got.TestSetIDs, want.TestSetIDs) {
		t.Errorf("TestSetIDs: got %v want %v", got.TestSetIDs, want.TestSetIDs)
	}
	if !reflect.DeepEqual(got.InstructorGCIDs, want.InstructorGCIDs) {
		t.Errorf("InstructorGCIDs: got %v want %v", got.InstructorGCIDs, want.InstructorGCIDs)
	}
	if !reflect.DeepEqual(got.LearningObjectives, want.LearningObjectives) {
		t.Errorf("LearningObjectives: got %v want %v", got.LearningObjectives, want.LearningObjectives)
	}
	if !reflect.DeepEqual(got.PrerequisiteNotes, want.PrerequisiteNotes) {
		t.Errorf("PrerequisiteNotes: got %v want %v", got.PrerequisiteNotes, want.PrerequisiteNotes)
	}
	if got.PriceSGDCents != want.PriceSGDCents {
		t.Errorf("PriceSGDCents: got %d want %d", got.PriceSGDCents, want.PriceSGDCents)
	}
	if got.SFEligible != want.SFEligible {
		t.Errorf("SFEligible: got %v want %v", got.SFEligible, want.SFEligible)
	}
	if got.MaxCapacity != want.MaxCapacity {
		t.Errorf("MaxCapacity: got %d want %d", got.MaxCapacity, want.MaxCapacity)
	}
	if got.State != want.State {
		t.Errorf("State: got %q want %q", got.State, want.State)
	}
	if got.ReviewNotes != want.ReviewNotes {
		t.Errorf("ReviewNotes: got %q want %q", got.ReviewNotes, want.ReviewNotes)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, want.CreatedAt)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v want %v", got.UpdatedAt, want.UpdatedAt)
	}
	if got.ScheduledOpenAt == nil || !got.ScheduledOpenAt.Equal(*want.ScheduledOpenAt) {
		t.Errorf("ScheduledOpenAt: got %v want %v", got.ScheduledOpenAt, want.ScheduledOpenAt)
	}
	if got.PublishedAt == nil || !got.PublishedAt.Equal(*want.PublishedAt) {
		t.Errorf("PublishedAt: got %v want %v", got.PublishedAt, want.PublishedAt)
	}
	if got.DeletedAt != nil {
		t.Errorf("DeletedAt: got %v want nil", got.DeletedAt)
	}
}
