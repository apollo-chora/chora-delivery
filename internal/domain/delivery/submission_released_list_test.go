// submission_released_list_test.go — InMemSubmissionRepo.ListReleasedByLearner
// (CHO-2040 ceremony learning-edges read; port parity with the pg adapter).
//
// Semantics under test mirror the pg SQL exactly:
//   - only state = RELEASED rows (the learner-visibility gate — see
//     internal/adapter/http/assessment_handler.go myResultHandler);
//   - soft-delete respected (DeletedAt == nil);
//   - scoped to (tenant, learner);
//   - newest grading-completion first (GradedAt desc, nil GradedAt last);
//   - offset + limit windowing for the adapter's +1-probe pagination.
package delivery

import (
	"context"
	"testing"
	"time"
)

func releasedRowForList(id string, tenantID, learnerGCID string, state SubmissionState, gradedAt *time.Time, deletedAt *time.Time) *Submission {
	return &Submission{
		ID:           id,
		AssessmentID: "assess-1",
		TenantID:     tenantID,
		LearnerGCID:  learnerGCID,
		State:        state,
		GradedAt:     gradedAt,
		DeletedAt:    deletedAt,
	}
}

func TestInMemListReleasedByLearner_FiltersAndOrders(t *testing.T) {
	t.Parallel()
	tenant := "11111111-1111-7111-8111-111111111111"
	otherTenant := "22222222-2222-7222-8222-222222222222"
	learner := "00000000-0000-7000-8000-000000001999"
	otherLearner := "00000000-0000-7000-8000-000000002888"

	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-72 * time.Hour)
	recent := now.Add(-24 * time.Hour)

	r := NewInMemSubmissionRepo()
	ctx := context.Background()
	seed := []*Submission{
		releasedRowForList("rel-old", tenant, learner, SubmissionStateReleased, &old, nil),
		releasedRowForList("rel-recent", tenant, learner, SubmissionStateReleased, &recent, nil),
		// Not yet released — must NOT leak (pre-release overall comments are
		// invisible to the learner in the product).
		releasedRowForList("pending", tenant, learner, SubmissionStateGradedPendingRelease, &recent, nil),
		// Soft-deleted released row — must NOT appear.
		releasedRowForList("deleted", tenant, learner, SubmissionStateReleased, &recent, &now),
		// Other tenant / other learner — out of scope.
		releasedRowForList("other-tenant", otherTenant, learner, SubmissionStateReleased, &recent, nil),
		releasedRowForList("other-learner", tenant, otherLearner, SubmissionStateReleased, &recent, nil),
		// Released with nil GradedAt (schema-anomalous) — sorts last, still
		// visible (it IS released; hiding it would understate history).
		releasedRowForList("rel-nilgraded", tenant, learner, SubmissionStateReleased, nil, nil),
	}
	for _, s := range seed {
		if err := r.Save(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
	}

	got, err := r.ListReleasedByLearner(ctx, tenant, learner, 10, 0)
	if err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	if len(got) != 3 {
		ids := make([]string, 0, len(got))
		for _, s := range got {
			ids = append(ids, s.ID)
		}
		t.Fatalf("expected 3 visible rows; got %d (%v)", len(got), ids)
	}
	if got[0].ID != "rel-recent" || got[1].ID != "rel-old" || got[2].ID != "rel-nilgraded" {
		t.Fatalf("wrong order: %s, %s, %s", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestInMemListReleasedByLearner_OffsetLimitWindow(t *testing.T) {
	t.Parallel()
	tenant := "11111111-1111-7111-8111-111111111111"
	learner := "00000000-0000-7000-8000-000000001999"
	base := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	r := NewInMemSubmissionRepo()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		ts := base.Add(-time.Duration(i) * time.Hour)
		s := releasedRowForList("row-"+string(rune('0'+i)), tenant, learner, SubmissionStateReleased, &ts, nil)
		if err := r.Save(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	page, err := r.ListReleasedByLearner(ctx, tenant, learner, 2, 1)
	if err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("expected window of 2; got %d", len(page))
	}
	// Newest-first ordering: offset 1 skips row-0 (newest).
	if page[0].ID != "row-1" || page[1].ID != "row-2" {
		t.Fatalf("wrong window: %s, %s", page[0].ID, page[1].ID)
	}

	// Offset past the end → empty, no error.
	empty, err := r.ListReleasedByLearner(ctx, tenant, learner, 2, 99)
	if err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty page past the end; got %d", len(empty))
	}
}
