package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// CHO-2352 - GET /api/v1/surveys was tenant-scoped but NOT role-scoped, so a
// LEARNER-only caller received every survey in the tenant: DRAFT (unpublished)
// titles, course ids and question prompts, plus the distributed_to recipient
// GCID list of other learners. Verified live on 2026-07-23 against a
// learner-only token, with responses-read and create both correctly 403 as
// positive controls, so the 200 was a true gap and not a broken probe.
//
// The fix scopes the list by role rather than blanket-denying it, because the
// A+ learner surface legitimately needs "the surveys sent to me":
//   - instructor / admin / training-admin  -> the whole tenant list (unchanged)
//   - anyone else (learner)                -> only surveys DISTRIBUTED to them

// listSurveyIDs GETs /api/v1/surveys as the given caller and returns the ids.
func listSurveyIDs(t *testing.T, srv http.Handler, gcid, roles, query string) []string {
	t.Helper()
	rec := doSV(t, srv, "GET", "/api/v1/surveys"+query, "", gcid, roles)
	if rec.Code != http.StatusOK {
		t.Fatalf("list as %q: status=%d want 200 body=%s", roles, rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, _ := resp["items"].([]interface{})
	out := make([]string, 0, len(items))
	for _, raw := range items {
		if m, ok := raw.(map[string]interface{}); ok {
			id, _ := m["id"].(string)
			out = append(out, id)
		}
	}
	return out
}

func TestSV_List_LearnerSeesOnlySurveysDistributedToThem(t *testing.T) {
	srv, repo := newSurveyServer()
	mine, _, _ := createDraftSurvey(t, srv)
	theirs, _, _ := createDraftSurvey(t, srv)

	s, ok, _ := repo.Get(context.Background(), mine)
	if !ok {
		t.Fatalf("setup: survey %s missing", mine)
	}
	if err := s.Distribute([]string{svTestLearner1ID}); err != nil {
		t.Fatalf("setup distribute: %v", err)
	}
	repo.Save(context.Background(), s)

	got := listSurveyIDs(t, srv, svTestLearner1ID, "learner", "")
	if len(got) != 1 || got[0] != mine {
		t.Fatalf("learner must see ONLY the survey distributed to them: got %v, want [%s] "+
			"(seeing %s would leak another cohort's survey)", got, mine, theirs)
	}
}

func TestSV_List_LearnerNeverSeesDraftSurveys(t *testing.T) {
	// DRAFT surveys are unpublished admin content. A learner must never see
	// their titles, course ids or question prompts.
	srv, _ := newSurveyServer()
	_, _, _ = createDraftSurvey(t, srv)
	_, _, _ = createDraftSurvey(t, srv)

	got := listSurveyIDs(t, srv, svTestLearner1ID, "learner", "")
	if len(got) != 0 {
		t.Fatalf("learner must see no DRAFT surveys, got %v", got)
	}
}

func TestSV_List_LearnerDoesNotSeeAnotherLearnersSurvey(t *testing.T) {
	srv, repo := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)

	s, ok, _ := repo.Get(context.Background(), id)
	if !ok {
		t.Fatalf("setup: survey %s missing", id)
	}
	if err := s.Distribute([]string{svTestLearner2ID}); err != nil {
		t.Fatalf("setup distribute: %v", err)
	}
	repo.Save(context.Background(), s)

	got := listSurveyIDs(t, srv, svTestLearner1ID, "learner", "")
	if len(got) != 0 {
		t.Fatalf("learner1 must not see a survey distributed to learner2, got %v", got)
	}
}

func TestSV_List_AdminRolesStillSeeWholeTenant(t *testing.T) {
	// Regression: the staff list must NOT narrow. Each staff role sees both
	// DRAFT surveys.
	for _, role := range []string{"instructor", "admin", "training-admin", "training_admin"} {
		t.Run(role, func(t *testing.T) {
			srv, _ := newSurveyServer()
			_, _, _ = createDraftSurvey(t, srv)
			_, _, _ = createDraftSurvey(t, srv)

			got := listSurveyIDs(t, srv, svTestAuthorHTTPGCID, role, "")
			if len(got) != 2 {
				t.Fatalf("%s must see the whole tenant list, got %d", role, len(got))
			}
		})
	}
}

func TestSV_List_LearnerScopingSurvivesStateFilter(t *testing.T) {
	// A learner passing ?state=DRAFT must not be able to widen their scope
	// back to unpublished content.
	srv, repo := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	_, _, _ = createDraftSurvey(t, srv)

	s, _, _ := repo.Get(context.Background(), id)
	if err := s.Distribute([]string{svTestLearner1ID}); err != nil {
		t.Fatalf("setup distribute: %v", err)
	}
	repo.Save(context.Background(), s)

	if got := listSurveyIDs(t, srv, svTestLearner1ID, "learner", "?state=DRAFT"); len(got) != 0 {
		t.Fatalf("learner asking for DRAFT must still get nothing, got %v", got)
	}
	got := listSurveyIDs(t, srv, svTestLearner1ID, "learner", "?state=DISTRIBUTED")
	if len(got) != 1 || got[0] != id {
		t.Fatalf("learner DISTRIBUTED filter should return their own survey, got %v", got)
	}
}

func TestSV_List_NoRolesHeaderIsTreatedAsLearner(t *testing.T) {
	// Fail closed: a caller with no roles header must get the narrow scope,
	// never the full tenant list.
	srv, _ := newSurveyServer()
	_, _, _ = createDraftSurvey(t, srv)

	got := listSurveyIDs(t, srv, svTestLearner1ID, "", "")
	if len(got) != 0 {
		t.Fatalf("missing roles header must fail closed to learner scope, got %v", got)
	}
}
