// Enrollment-completion lifecycle tests (chora.delivery.enrollment.completed.v1
// driver). Covers the Status field, the Complete() state transition (happy /
// idempotent / refuse-cancelled), the SoftDelete -> cancelled reconciliation,
// and the InMemEnrollmentStore MarkCompleted persist surface.
//
// Pure domain — time-injected, stdlib only, matches submission_test.go +
// enrollment_test.go style.
package delivery_test

import (
	"context"
	"testing"
	"time"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// NewEnrollment — Status defaults to active
// -----------------------------------------------------------------------------

func TestNewEnrollment_DefaultsToActiveStatus(t *testing.T) {
	t.Parallel()
	e, err := domain.NewEnrollment(tenantA, "course-1", gcidA)
	if err != nil {
		t.Fatalf("NewEnrollment: %v", err)
	}
	if e.Status != domain.EnrollmentStatusActive {
		t.Fatalf("fresh enrollment status: want %q, got %q", domain.EnrollmentStatusActive, e.Status)
	}
	if e.CompletedAt != nil {
		t.Fatalf("fresh enrollment must have nil CompletedAt")
	}
	if e.Passed != nil {
		t.Fatalf("fresh enrollment must have nil Passed")
	}
}

// -----------------------------------------------------------------------------
// Complete — happy path
// -----------------------------------------------------------------------------

func TestEnrollment_Complete_TransitionsActiveToCompleted(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		passed bool
	}{
		{"passed", true},
		{"completed-without-passing", false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, _ := domain.NewEnrollment(tenantA, "course-1", gcidA)

			if err := e.Complete(tc.passed, now); err != nil {
				t.Fatalf("Complete: unexpected error %v", err)
			}
			if e.Status != domain.EnrollmentStatusCompleted {
				t.Fatalf("status: want completed, got %q", e.Status)
			}
			if e.CompletedAt == nil || !e.CompletedAt.Equal(now) {
				t.Fatalf("CompletedAt: want %v, got %v", now, e.CompletedAt)
			}
			if e.Passed == nil || *e.Passed != tc.passed {
				t.Fatalf("Passed: want %v, got %v", tc.passed, e.Passed)
			}
			// Completing must NOT soft-delete the row.
			if e.DeletedAt != nil {
				t.Fatalf("Complete must not set DeletedAt")
			}
		})
	}
}

// Complete normalises the supplied timestamp to UTC.
func TestEnrollment_Complete_NormalisesToUTC(t *testing.T) {
	t.Parallel()
	sgt := time.FixedZone("SGT", 8*3600)
	now := time.Date(2026, 6, 28, 18, 0, 0, 0, sgt)
	e, _ := domain.NewEnrollment(tenantA, "course-1", gcidA)

	if err := e.Complete(true, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if e.CompletedAt.Location() != time.UTC {
		t.Fatalf("CompletedAt must be UTC, got %v", e.CompletedAt.Location())
	}
	if !e.CompletedAt.Equal(now) {
		t.Fatalf("CompletedAt instant must equal the supplied time")
	}
}

// -----------------------------------------------------------------------------
// Complete — idempotent (re-complete = no-op)
// -----------------------------------------------------------------------------

func TestEnrollment_Complete_IsIdempotent(t *testing.T) {
	t.Parallel()
	first := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	second := first.Add(48 * time.Hour)

	e, _ := domain.NewEnrollment(tenantA, "course-1", gcidA)
	if err := e.Complete(true, first); err != nil {
		t.Fatalf("Complete #1: %v", err)
	}

	// Re-complete with DIFFERENT args must be a no-op (no error, no mutation).
	if err := e.Complete(false, second); err != nil {
		t.Fatalf("Complete #2 (idempotent): unexpected error %v", err)
	}
	if !e.CompletedAt.Equal(first) {
		t.Fatalf("idempotent re-complete must not move CompletedAt; want %v got %v", first, e.CompletedAt)
	}
	if e.Passed == nil || *e.Passed != true {
		t.Fatalf("idempotent re-complete must not flip Passed; got %v", e.Passed)
	}
	if e.Status != domain.EnrollmentStatusCompleted {
		t.Fatalf("status must remain completed")
	}
}

// -----------------------------------------------------------------------------
// Complete — refuses a cancelled / soft-deleted enrollment
// -----------------------------------------------------------------------------

func TestEnrollment_Complete_RefusesCancelled(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	e, _ := domain.NewEnrollment(tenantA, "course-1", gcidA)
	e.SoftDelete() // cancel

	err := e.Complete(true, now)
	if err == nil {
		t.Fatalf("expected Complete to refuse a cancelled enrollment")
	}
	// Refusal must not mutate completion state.
	if e.Status != domain.EnrollmentStatusCancelled {
		t.Fatalf("status must remain cancelled, got %q", e.Status)
	}
	if e.CompletedAt != nil || e.Passed != nil {
		t.Fatalf("refused Complete must leave CompletedAt/Passed nil")
	}
}

// -----------------------------------------------------------------------------
// SoftDelete reconciles Status -> cancelled (alongside DeletedAt)
// -----------------------------------------------------------------------------

func TestEnrollment_SoftDelete_SetsCancelledStatus(t *testing.T) {
	t.Parallel()
	e, _ := domain.NewEnrollment(tenantA, "course-1", gcidA)
	e.SoftDelete()
	if e.DeletedAt == nil {
		t.Fatalf("SoftDelete must set DeletedAt")
	}
	if e.Status != domain.EnrollmentStatusCancelled {
		t.Fatalf("SoftDelete must reconcile status to cancelled, got %q", e.Status)
	}
}

// -----------------------------------------------------------------------------
// InMemEnrollmentStore.MarkCompleted — persists the completion fact
// -----------------------------------------------------------------------------

func TestInMemEnrollmentStore_MarkCompleted_Persists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domain.NewInMemEnrollmentStore()

	e, err := store.Register(ctx, tenantA, "course-1", gcidA)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	if err := e.Complete(true, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.MarkCompleted(ctx, e); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	// Re-load: completion must be visible on a fresh Get.
	got, ok, err := store.Get(ctx, e.ID)
	if err != nil || !ok {
		t.Fatalf("Get after MarkCompleted: ok=%v err=%v", ok, err)
	}
	if got.Status != domain.EnrollmentStatusCompleted {
		t.Fatalf("persisted status: want completed, got %q", got.Status)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(now) {
		t.Fatalf("persisted CompletedAt: want %v, got %v", now, got.CompletedAt)
	}
	if got.Passed == nil || *got.Passed != true {
		t.Fatalf("persisted Passed: want true, got %v", got.Passed)
	}
}

// MarkCompleted on an unregistered enrollment is a loud miss.
func TestInMemEnrollmentStore_MarkCompleted_UnknownIsNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domain.NewInMemEnrollmentStore()

	orphan, _ := domain.NewEnrollment(tenantA, "course-1", gcidA) // never Register()ed
	if err := store.MarkCompleted(ctx, orphan); err == nil {
		t.Fatalf("expected MarkCompleted to miss on an unregistered enrollment")
	}
}

// MarkCompleted refuses a soft-deleted (cancelled) stored row.
func TestInMemEnrollmentStore_MarkCompleted_RefusesSoftDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := domain.NewInMemEnrollmentStore()
	e, _ := store.Register(ctx, tenantA, "course-1", gcidA)
	e.SoftDelete() // cancels the stored pointer

	if err := store.MarkCompleted(ctx, e); err == nil {
		t.Fatalf("expected MarkCompleted to refuse a soft-deleted row")
	}
}

// MarkCompleted on a nil aggregate is a loud miss (registry guard).
func TestEnrollmentRegistry_MarkCompleted_NilIsNotFound(t *testing.T) {
	t.Parallel()
	reg := domain.NewEnrollmentRegistry()
	if err := reg.MarkCompleted(nil); err == nil {
		t.Fatalf("expected MarkCompleted(nil) to error")
	}
}

// Compile-time assertion: InMemEnrollmentStore satisfies the focused
// completion port (mirrors the EnrollmentListByCoursePort assertion pattern).
var _ domain.EnrollmentCompletionPort = (*domain.InMemEnrollmentStore)(nil)
