// survey_repo_test.go — direct unit tests for the in-memory SurveyRepo
// (Wave-5 R+ /r/surveys). Pins the defensive Survey snapshot, tenant + state
// scoping, the append-only SurveyResponse surface (SaveResponse / HasResponse /
// ListResponsesBySurvey), and the SortableId/SubmittedAt orderings.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/survey"
)

func mkSurvey(id, tenantID string, deleted bool) *survey.Survey {
	s := &survey.Survey{
		ID:            id,
		TenantID:      tenantID,
		CourseID:      "019e2f93-d586-71b5-8c3d-e2b0d0d5cc01",
		Title:         "survey-" + id,
		Questions:     []survey.Question{{QuestionID: "q1", Prompt: "P?", Type: survey.QuestionTypeLikert}},
		DistributedTo: []string{"019e2f93-d586-71b5-8c3d-e2b0d0d5ee01"},
		State:         survey.SurveyStateDraft,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if deleted {
		now := time.Now().UTC()
		s.DeletedAt = &now
	}
	return s
}

func mkResponse(id, surveyID, gcid string, at time.Time) *survey.SurveyResponse {
	return &survey.SurveyResponse{
		ID:          id,
		SurveyID:    surveyID,
		GCID:        gcid,
		Answers:     []survey.Answer{{QuestionID: "q1", Value: "4"}},
		SubmittedAt: at,
	}
}

func TestSurveyRepo_SaveGet(t *testing.T) {
	t.Parallel()
	r := inmem.NewSurveyRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	s := mkSurvey("sv-1", tenantA, false)
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, s.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != s.ID || len(got.Questions) != 1 {
		t.Fatalf("round-trip mismatch; questions=%d", len(got.Questions))
	}

	// Defensive snapshot: mutating the returned copy must not touch the store.
	got.Questions = append(got.Questions, survey.Question{QuestionID: "q2", Prompt: "?P?", Type: survey.QuestionTypeLikert})
	got.DistributedTo = append(got.DistributedTo, "extra-gcid")
	again, ok, _ := r.Get(ctx, s.ID)
	if !ok || len(again.Questions) != 1 || len(again.DistributedTo) != 1 {
		t.Fatalf("store must hold a defensive copy; questions=%d distributed=%d", len(again.Questions), len(again.DistributedTo))
	}

	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

func TestSurveyRepo_Get_SoftDeletedIsMiss(t *testing.T) {
	t.Parallel()
	r := inmem.NewSurveyRepo()
	ctx := context.Background()
	s := mkSurvey("sv-2", tenantA, true)
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, _ := r.Get(ctx, s.ID); ok {
		t.Fatal("soft-deleted survey must read as a miss")
	}
}

func TestSurveyRepo_ListByTenant_FiltersByTenantAndState(t *testing.T) {
	t.Parallel()
	r := inmem.NewSurveyRepo()
	ctx := context.Background()

	s1 := mkSurvey("sv-a1", tenantA, false)
	s1.State = survey.SurveyStateDistributed
	s2 := mkSurvey("sv-a2", tenantA, false)
	s2.State = survey.SurveyStateClosed
	b1 := mkSurvey("sv-b1", tenantB, false)
	gone := mkSurvey("sv-gone", tenantA, true)

	for _, s := range []*survey.Survey{s1, s2, b1, gone} {
		if err := r.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	all, err := r.ListByTenant(ctx, tenantA, "")
	if err != nil {
		t.Fatalf("ListByTenant(no filter): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 (tenant-scoped, soft-delete filtered); got %d", len(all))
	}

	distributed, err := r.ListByTenant(ctx, tenantA, survey.SurveyStateDistributed)
	if err != nil {
		t.Fatalf("ListByTenant(distributed): %v", err)
	}
	if len(distributed) != 1 || distributed[0].ID != s1.ID {
		t.Fatalf("state filter failed; got %d items", len(distributed))
	}

	tenantBList, err := r.ListByTenant(ctx, tenantB, "")
	if err != nil {
		t.Fatalf("ListByTenant(tenantB): %v", err)
	}
	if len(tenantBList) != 1 || tenantBList[0].ID != b1.ID {
		t.Fatalf("tenantB must see only its own survey; got %d", len(tenantBList))
	}
}

// ListByTenant sorts by created_at DESC with an ID tiebreak for identical
// timestamps.
func TestSurveyRepo_ListByTenant_NewestFirstWithTiebreak(t *testing.T) {
	t.Parallel()
	r := inmem.NewSurveyRepo()
	ctx := context.Background()

	older := mkSurvey("sv-t-1", tenantA, false)
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := mkSurvey("sv-t-2", tenantA, false)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := r.Save(ctx, older); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	if err := r.Save(ctx, newer); err != nil {
		t.Fatalf("Save newer: %v", err)
	}

	out, err := r.ListByTenant(ctx, tenantA, "")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 || out[0].ID != newer.ID || out[1].ID != older.ID {
		t.Fatalf("expected [newer older] created_at DESC, got [%s %s]", out[0].ID, out[1].ID)
	}
}

func TestSurveyRepo_Responses(t *testing.T) {
	t.Parallel()
	r := inmem.NewSurveyRepo()
	ctx := context.Background()

	if err := r.SaveResponse(ctx, nil); err != nil {
		t.Fatalf("SaveResponse(nil) must be a safe no-op; got %v", err)
	}
	if has, err := r.HasResponse(ctx, "sv-1", "gcid-1"); err != nil || has {
		t.Fatalf("empty repo must report no response; has=%v err=%v", has, err)
	}

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	err := r.SaveResponse(ctx, mkResponse("resp-1", "sv-1", "gcid-1", base))
	if err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	if err := r.SaveResponse(ctx, mkResponse("resp-2", "sv-1", "gcid-2", base.Add(time.Hour))); err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	if err := r.SaveResponse(ctx, mkResponse("resp-3", "sv-2", "gcid-1", base.Add(2*time.Hour))); err != nil {
		t.Fatalf("SaveResponse (other survey): %v", err)
	}

	// HasResponse: hit for the matching tuple, miss otherwise.
	if has, err := r.HasResponse(ctx, "sv-1", "gcid-1"); err != nil || !has {
		t.Fatalf("HasResponse(sv-1,gcid-1): want true; has=%v err=%v", has, err)
	}
	if has, err := r.HasResponse(ctx, "sv-1", "gcid-9"); err != nil || has {
		t.Fatalf("HasResponse unknown gcid: want false; has=%v err=%v", has, err)
	}

	// ListResponsesBySurvey: scoped to the survey, submitted_at ASC (oldest first).
	out, err := r.ListResponsesBySurvey(ctx, "sv-1")
	if err != nil {
		t.Fatalf("ListResponsesBySurvey: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 responses for sv-1; got %d", len(out))
	}
	if out[0].ID != "resp-1" || out[1].ID != "resp-2" {
		t.Fatalf("expected arrival order [resp-1 resp-2], got [%s %s]", out[0].ID, out[1].ID)
	}
	if len(out[0].Answers) != 1 {
		t.Fatalf("defensive copy of Answers expected; got %d answers", len(out[0].Answers))
	}

	// Empty survey yields an empty (non-nil) slice.
	empty, err := r.ListResponsesBySurvey(ctx, "sv-nope")
	if err != nil {
		t.Fatalf("ListResponsesBySurvey(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected 0 responses; got %d", len(empty))
	}
}

// Equal SubmittedAt timestamps fall back to an ID tiebreak (oldest-first
// arrival order preserved).
func TestSurveyRepo_ListResponsesBySurvey_SubmittedAtTie(t *testing.T) {
	t.Parallel()
	r := inmem.NewSurveyRepo()
	ctx := context.Background()

	tie := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if err := r.SaveResponse(ctx, mkResponse("resp-b", "sv-1", "gcid-b", tie)); err != nil {
		t.Fatalf("SaveResponse resp-b: %v", err)
	}
	if err := r.SaveResponse(ctx, mkResponse("resp-a", "sv-1", "gcid-a", tie)); err != nil {
		t.Fatalf("SaveResponse resp-a: %v", err)
	}

	out, err := r.ListResponsesBySurvey(ctx, "sv-1")
	if err != nil {
		t.Fatalf("ListResponsesBySurvey: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2; got %d", len(out))
	}
	if out[0].ID > out[1].ID {
		t.Fatalf("expected ID-ascending tie order, got [%s %s]", out[0].ID, out[1].ID)
	}
}
