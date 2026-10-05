// grading_review_handler_coverage_test.go — statement-coverage battery for
// grading_review_handler.go (ADR-172 instructor grading-review / HITL gate).
//
// Supplements grading_review_detail_test.go (authored projection) +
// grading_final_grade_test.go (grade-of-record) with the branches the live
// flows do not reach: loadTestSetQuestionRefs/Authored miss-paths, the
// guardInstructorSubmission 400/401/403/404 cases, PATCH .../grades
// validation + conflict paths, PATCH .../overall-comment, the approve handler
// error paths, bulk POST .../approve-all (release / skip / already-approved /
// missing-assessment), the writeGradeOverrideError domain-mapping cases and
// the nil-publisher guard branches of every event emitter.
package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// gr3 file-local fixtures (gr3 prefix per convention — no collisions).
// -----------------------------------------------------------------------------

// gr3GradedAnswers returns the shape under review everywhere below: an MCQ
// answer + one AI-graded OE answer inside a submission carrying the mandatory
// HITL gate. oeTSQID must match the seeded test-set question (see
// gr3SeedOETestSet) for the authored-snapshot lookups to resolve.
func gr3GradedAnswers(oeTSQID string) []domain.SubmissionAnswer {
	correct := true
	aiPts := 8.0
	return []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-mcq", QuestionID: "qid-mcq",
			QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "A", MCQCorrect: &correct,
			PointsEarned: 30, PointsPossible: 30, GradingDispatch: domain.GradingDispatchDeterministic,
		},
		{
			TestSetQuestionID: oeTSQID, QuestionID: "qid-oe",
			QuestionType: domain.QuestionTypeOE, OEResponseText: "learner essay",
			PointsEarned: 8, PointsPossible: 10, AIPointsEarned: &aiPts,
			OEComment: "AI rationale", AIComment: "AI rationale",
			ScoreProvenance: domain.ProvenanceAI, CommentProvenance: domain.ProvenanceAI,
			ModelAnswerProvenance: domain.ProvenanceAI,
			GradingDispatch:       domain.GradingDispatchLLMEvaluator,
		},
	}
}

// gr3GradedDrive lands a submission in GRADED_PENDING_RELEASE / PENDING_REVIEW
// — the state every HITL edit + approve requires.
func gr3GradedDrive(s *domain.Submission) {
	s.State = domain.SubmissionStateGradedPendingRelease
	s.ReviewStatus = domain.ReviewStatusPendingReview
}

// gr3SeedOETestSet seeds a test-set with one OE question carrying an authored
// OE snapshot (prompt + model_answer + rubric) so the question-refs / authored
// projections resolve a full map. Returns the generated question ID.
func gr3SeedOETestSet(t *testing.T, tsStore *httpapi.InMemTestSetStore, tsID string) string {
	t.Helper()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: instructor, Title: "gr3 OE test set",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	ts.ID = tsID
	q, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-oe", QuestionID: "qid-oe",
		QuestionType: "oe", DisplayOrder: 1, Points: 10,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	q.PayloadSnapshot = `{"stem":"Explain","prompt":"Explain the model","model_answer":"Authored model answer","rubric":[{"criterion_id":"clarity","description":"Clear","weight":1}]}`
	if err := tsStore.Save(context.Background(), ts); err != nil {
		t.Fatalf("Save test set: %v", err)
	}
	return q.ID
}

// gr3PayloadOf decodes a published event's payload into a plain map.
func gr3PayloadOf(t *testing.T, e events.PublishedEvent) map[string]any {
	t.Helper()
	b, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return m
}

// gr3EventByTopic returns the first published event payload on a topic.
func gr3EventByTopic(t *testing.T, pub *events.InMemoryPublisher, topic string) (map[string]any, bool) {
	t.Helper()
	for _, e := range pub.History() {
		if e.Topic == topic {
			return gr3PayloadOf(t, e), true
		}
	}
	return nil, false
}

func gr3GradingPath(aID, subID string) string {
	return "/api/v1/assessments/" + aID + "/submissions/" + subID + "/grading"
}

func gr3GradesPath(aID, subID string) string {
	return "/api/v1/assessments/" + aID + "/submissions/" + subID + "/grades"
}

func gr3CommentPath(aID, subID string) string {
	return "/api/v1/assessments/" + aID + "/submissions/" + subID + "/overall-comment"
}

func gr3ApprovePath(aID, subID string) string {
	return "/api/v1/assessments/" + aID + "/submissions/" + subID + "/approve"
}

func gr3ApproveAllPath(aID string) string {
	return "/api/v1/assessments/" + aID + "/approve-all"
}

// -----------------------------------------------------------------------------
// Failing-repo wrappers — drive the 500 branches the inmem repo can't reach.
// -----------------------------------------------------------------------------

// gr3FailSaveRepo fails every Save (handlers' "save failed" 500 branch).
type gr3FailSaveRepo struct {
	*domain.InMemSubmissionRepo
}

func (r *gr3FailSaveRepo) Save(_ context.Context, _ *domain.Submission) error {
	return errors.New("gr3: forced save failure")
}

// gr3FailAppendRepo fails AppendGradeOverrides ("audit failed" 500 branch).
type gr3FailAppendRepo struct {
	*domain.InMemSubmissionRepo
}

func (r *gr3FailAppendRepo) AppendGradeOverrides(_ context.Context, _, _ string, _ []domain.GradeOverride) error {
	return errors.New("gr3: forced append failure")
}

// gr3FailListRepo fails ListByAssessment (approve-all "list failed" 500).
type gr3FailListRepo struct {
	*domain.InMemSubmissionRepo
}

func (r *gr3FailListRepo) ListByAssessment(_ context.Context, _, _ string, _ int, _ string) ([]*domain.Submission, string, error) {
	return nil, "", errors.New("gr3: forced list failure")
}

// -----------------------------------------------------------------------------
// GET .../grading — getSubmissionGradingDetailHandler (0% baseline)
// -----------------------------------------------------------------------------

func TestGr3GetGradingDetail_Happy_ProjectsAuthoredSnapshot(t *testing.T) {
	tsStore := httpapi.NewInMemTestSetStore()
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = tsStore
	})
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	oeQID := gr3SeedOETestSet(t, tsStore, a.TestSetID)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers(oeQID), gr3GradedDrive)

	w := reqWithHeaders(http.MethodGet, gr3GradingPath(a.ID, sub.ID), nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET grading: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["submission_id"] != sub.ID || body["review_status"] != "PENDING_REVIEW" {
		t.Fatalf("detail identity wrong: %v", body)
	}
	qs, _ := body["questions"].([]any)
	if len(qs) != 2 {
		t.Fatalf("questions len: want 2, got %d", len(qs))
	}
	oe := qs[1].(map[string]any)
	if oe["model_answer"] != "Authored model answer" {
		t.Fatalf("OE model_answer should fall back to authored snapshot, got %v", oe["model_answer"])
	}
	if oe["prompt"] != "Explain the model" {
		t.Fatalf("prompt should project from authored snapshot, got %v", oe["prompt"])
	}
	if _, has := oe["rubric"]; !has {
		t.Fatalf("rubric not projected from authored snapshot")
	}
	if oe["ai_points_earned"].(float64) != 8 {
		t.Fatalf("ai_points_earned = %v, want 8", oe["ai_points_earned"])
	}
}

func TestGr3GetGradingDetail_NoTestSetsPort_200(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = nil // loadTestSetQuestionAuthored early-return
	})
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodGet, gr3GradingPath(a.ID, sub.ID), nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("no-test-sets GET grading: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3GetGradingDetail_AssessmentNotSaved_200(t *testing.T) {
	srv, _, sRepo, _ := asmServer(t, nil)
	// The submission exists, but its parent assessment was never saved —
	// loadTestSetQuestionAuthored must fail-soft to an empty map.
	sub := asmNewSubmission(t, sRepo, "no-such-assessment", gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodGet, gr3GradingPath("no-such-assessment", sub.ID), nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("missing-assessment GET grading: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// guardInstructorSubmission — the 400/401/403/404 branches (50% baseline).
func TestGr3GetGradingDetail_MissingTenant_400(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	req := httptest.NewRequest(http.MethodGet, gr3GradingPath(a.ID, sub.ID), nil)
	req.Header.Set("gcid", instructor)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3GetGradingDetail_MissingGCID_401(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	req := httptest.NewRequest(http.MethodGet, gr3GradingPath(a.ID, sub.ID), nil)
	req.Header.Set("X-Tenant-Id", tenantID)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing gcid: want 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3GetGradingDetail_NotInstructor_403(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodGet, gr3GradingPath(a.ID, sub.ID), nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3GetGradingDetail_SubmissionNotFound_404(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	w := reqWithHeaders(http.MethodGet, gr3GradingPath(a.ID, "no-such-sub"), nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing submission: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3GetGradingDetail_WrongAssessment_404(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	other := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	// Ask through `other`'s assessment id — the submission belongs to `a`.
	w := reqWithHeaders(http.MethodGet, gr3GradingPath(other.ID, sub.ID), nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("mismatched assessment: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// buildGradingReviewDetail — leftover branches (86.4% baseline)
// -----------------------------------------------------------------------------

func TestGr3BuildGradingReviewDetail_KitchenSink(t *testing.T) {
	tsStore := httpapi.NewInMemTestSetStore()
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = tsStore
	})
	a := asmNewAssessment(t, aRepo, nil)
	oeQID := gr3SeedOETestSet(t, tsStore, a.TestSetID)

	correct := true
	aiPts := 9.0
	approvedAt := time.Now().UTC()
	answers := []domain.SubmissionAnswer{
		// MCQ with multi-correct ids + correct flag (no single choice, no OE text).
		{
			TestSetQuestionID: "tsq-mcq", QuestionID: "qid-mcq",
			QuestionType: domain.QuestionTypeMCQ, MCQCorrect: &correct, MCQChoiceIDs: []string{"o1", "o2"},
			PointsEarned: 5, PointsPossible: 5,
		},
		// OE with EVERYTHING: amended model answer wins, authored prompt/rubric
		// project, criterion scores decode, human provenances render.
		{
			TestSetQuestionID: oeQID, QuestionID: "qid-oe-1",
			QuestionType: domain.QuestionTypeOE, OEResponseText: "essay",
			PointsEarned: 9, PointsPossible: 10, AIPointsEarned: &aiPts,
			OEComment: "human comment", AIComment: "AI comment",
			CommentProvenance: domain.ProvenanceHuman, ScoreProvenance: domain.ProvenanceHuman,
			ModelAnswerProvenance: domain.ProvenanceHuman, QualityFlagged: true,
			AmendedModelAnswer: "Amended wins",
			OECriterionJSON:    `[{"criterion_id":"clarity","score":3}]`,
		},
		// OE with a MALFORMED criterion snapshot → no criterion_scores, and no
		// authored entry → no model_answer fallback.
		{
			TestSetQuestionID: "tsq-oe-2", QuestionID: "qid-oe-2",
			QuestionType: domain.QuestionTypeOE, OEResponseText: "other essay",
			PointsPossible: 10, OECriterionJSON: "not-json",
		},
		// Blank MCQ → learner_answer stays an empty object.
		{TestSetQuestionID: "tsq-blank", QuestionID: "qid-blank", QuestionType: domain.QuestionTypeMCQ},
	}
	passed := true
	sub := asmNewSubmission(t, sRepo, a.ID, answers, func(s *domain.Submission) {
		gr3GradedDrive(s)
		s.Passed = &passed
		s.ApprovedByGCID = instructor
		s.ApprovedAt = &approvedAt
	})

	w := reqWithHeaders(http.MethodGet, gr3GradingPath(a.ID, sub.ID), nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("kitchen-sink GET grading: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["passed"] != true || body["approved_by_gcid"] != instructor {
		t.Fatalf("passed/approved_by_gcid wrong: %v", body)
	}
	if at, _ := body["approved_at"].(string); at == "" {
		t.Fatalf("approved_at not projected")
	}
	qs, _ := body["questions"].([]any)
	if len(qs) != 4 {
		t.Fatalf("questions len: want 4, got %d", len(qs))
	}
	q0 := qs[0].(map[string]any)
	if q0["correct"] != true {
		t.Fatalf("MCQ correct flag missing: %v", q0)
	}
	la0, _ := q0["learner_answer"].(map[string]any)
	ids, _ := la0["mcq_choice_ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("mcq_choice_ids: want [o1 o2], got %v", la0)
	}
	if _, has := la0["mcq_choice_id"]; has {
		t.Fatalf("blank mcq_choice_id must be omitted, got %v", la0)
	}
	q1 := qs[1].(map[string]any)
	if q1["model_answer"] != "Amended wins" {
		t.Fatalf("amended model answer should win, got %v", q1["model_answer"])
	}
	if q1["ai_points_earned"].(float64) != 9 {
		t.Fatalf("ai_points_earned = %v, want 9", q1["ai_points_earned"])
	}
	if _, has := q1["criterion_scores"]; !has {
		t.Fatalf("criterion_scores should decode from valid JSON")
	}
	if q1["prompt"] != "Explain the model" || q1["comment_provenance"] != "HUMAN" || q1["score_provenance"] != "HUMAN" || q1["quality_flagged"] != true {
		t.Fatalf("OE projection wrong: %v", q1)
	}
	q2 := qs[2].(map[string]any)
	if _, has := q2["criterion_scores"]; has {
		t.Fatalf("malformed criterion JSON must not project criterion_scores: %v", q2)
	}
	if _, has := q2["model_answer"]; has {
		t.Fatalf("no authored + no amended ⇒ model_answer must be absent: %v", q2)
	}
	if _, has := q2["prompt"]; has {
		t.Fatalf("no authored entry ⇒ prompt must be absent: %v", q2)
	}
	q3 := qs[3].(map[string]any)
	la3, _ := q3["learner_answer"].(map[string]any)
	if len(la3) != 0 {
		t.Fatalf("blank MCQ learner_answer must be empty, got %v", la3)
	}
}

// -----------------------------------------------------------------------------
// PATCH .../grades — editSubmissionGradesHandler (54.3% baseline)
// -----------------------------------------------------------------------------

func TestGr3EditGrades_BadJSON_400(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	req := httptest.NewRequest(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), strings.NewReader("{oops"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantID)
	req.Header.Set("gcid", instructor)
	req.Header.Set("x-mesh-user-roles", "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3EditGrades_EmptyEdits_400(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{"question_edits": []any{}}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty edits: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3EditGrades_NotGraded_409(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	// Default state (Started) — no grading has landed → ErrSubmissionNotGraded.
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), nil)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{{"test_set_question_id": "tsq-oe", "points_earned": 7.0, "reason": "gr3"}},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("not graded: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "DELIVERY_SUBMISSION_NOT_GRADED") {
		t.Fatalf("expected NOT_GRADED code, body=%s", rr.Body.String())
	}
}

func TestGr3EditGrades_AlreadyApproved_409(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	// Approve first; a post-approval edit is a conflict.
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath(a.ID, sub.ID), []byte(`{}`), instructor, "instructor")
	rrApprove := httptest.NewRecorder()
	srv.ServeHTTP(rrApprove, w)
	if rrApprove.Code != http.StatusOK {
		t.Fatalf("pre-approve: want 200, got %d body=%s", rrApprove.Code, rrApprove.Body.String())
	}
	w2 := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{{"test_set_question_id": "tsq-oe", "points_earned": 9.0}},
	}), instructor, "instructor")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, w2)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("already approved: want 409, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "DELIVERY_SUBMISSION_ALREADY_APPROVED") {
		t.Fatalf("expected ALREADY_APPROVED code, body=%s", rr2.Body.String())
	}
}

func TestGr3EditGrades_OEAnswerNotFound_404(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{{"test_set_question_id": "missing-oe", "points_earned": 7.0}},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown tsq: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "oe answer not found") {
		t.Fatalf("expected oe-answer-not-found message, body=%s", rr.Body.String())
	}
}

func TestGr3EditGrades_NotInstructor_403(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{{"test_set_question_id": "tsq-oe", "points_earned": 7.0}},
	}), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("grades non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3EditGrades_ScoreOutOfRange_400(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)

	for _, bad := range []float64{-1, 20} { // negative + beyond points_possible
		w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
			"question_edits": []map[string]any{{"test_set_question_id": "tsq-oe", "points_earned": bad}},
		}), instructor, "instructor")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, w)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("score %v: want 400, got %d body=%s", bad, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "score out of range") {
			t.Fatalf("expected score-out-of-range message, body=%s", rr.Body.String())
		}
	}
}

func TestGr3EditGrades_Happy_ScoreCommentModelAnswer(t *testing.T) {
	tsStore := httpapi.NewInMemTestSetStore()
	srv, aRepo, sRepo, pub := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = tsStore
	})
	a := asmNewAssessment(t, aRepo, nil)
	oeQID := gr3SeedOETestSet(t, tsStore, a.TestSetID)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers(oeQID), gr3GradedDrive)

	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{
			{"test_set_question_id": oeQID, "points_earned": 7.0, "comment": "better", "reason": "gr3 override"},
			{"test_set_question_id": oeQID, "model_answer": "new canonical answer"},
		},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("grades happy: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.grading.score_overridden.v1"] != 3 { // SCORE + COMMENT + MODEL_ANSWER audits
		t.Fatalf("score_overridden count: want 3, got %v", topics)
	}
	if topics["chora.delivery.grading.model_answer_amended.v1"] != 1 {
		t.Fatalf("model_answer_amended count: want 1, got %v", topics)
	}
	// The amended model answer must now win the projection.
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	qs, _ := body["questions"].([]any)
	oe := qs[1].(map[string]any)
	if oe["model_answer"] != "new canonical answer" {
		t.Fatalf("amended model answer should win post-edit, got %v", oe["model_answer"])
	}
	if oe["score_provenance"] != "HUMAN" || oe["comment_provenance"] != "HUMAN" {
		t.Fatalf("provenance should flip AI→HUMAN, got %v", oe)
	}
}

func TestGr3EditGrades_NoTestSetsPort_ModelAnswerEdit(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = nil // loadTestSetQuestionRefs early-return → empty tsqRef
	})
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{
			{"test_set_question_id": "tsq-oe", "points_earned": 6.0},
			{"test_set_question_id": "tsq-oe", "model_answer": "answer with no refs"},
		},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("no-test-sets grades: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	topics := asmTopics(t, pub)
	// model_answer_amended still fires — the refs map is best-effort enrichment.
	if topics["chora.delivery.grading.model_answer_amended.v1"] != 1 {
		t.Fatalf("model_answer_amended count: want 1, got %v", topics)
	}
}

func TestGr3EditGrades_SaveFail_500(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.Submissions = &gr3FailSaveRepo{InMemSubmissionRepo: ad.Submissions.(*domain.InMemSubmissionRepo)}
	})
	a := asmNewAssessment(t, aRepo, nil)
	// Seed through the RAW repo (sRepo) — the wrapper fails only on handler saves.
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{{"test_set_question_id": "tsq-oe", "points_earned": 6.0}},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("save fail: want 500, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "save failed") {
		t.Fatalf("expected save-failed message, body=%s", rr.Body.String())
	}
}

func TestGr3EditGrades_AppendFail_500(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.Submissions = &gr3FailAppendRepo{InMemSubmissionRepo: ad.Submissions.(*domain.InMemSubmissionRepo)}
	})
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{{"test_set_question_id": "tsq-oe", "points_earned": 6.0}},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("append fail: want 500, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "audit failed") {
		t.Fatalf("expected audit-failed message, body=%s", rr.Body.String())
	}
}

func TestGr3EditGrades_NilPublisher_200(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.OutboxPublisher = nil // emitter nil-guards
	})
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{
			{"test_set_question_id": "tsq-oe", "points_earned": 6.0, "comment": "c"},
			{"test_set_question_id": "tsq-oe", "model_answer": "m"},
		},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("nil publisher grades: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// PATCH .../overall-comment — editSubmissionOverallCommentHandler (0%)
// -----------------------------------------------------------------------------

func TestGr3EditOverallComment_Happy(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3CommentPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"overall_comment": "Human overall comment",
		"reason":          "gr3",
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("overall-comment happy: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.grading.score_overridden.v1"] != 1 {
		t.Fatalf("OVERALL_COMMENT override should emit score_overridden, got %v", topics)
	}
	if pl, ok := gr3EventByTopic(t, pub, "chora.delivery.grading.score_overridden.v1"); !ok || pl["field"] != "OVERALL_COMMENT" {
		t.Fatalf("expected OVERALL_COMMENT override audit event, got %v", pl)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["overall_comment"] != "Human overall comment" || body["overall_comment_provenance"] != "HUMAN" {
		t.Fatalf("overall comment projection wrong: %v", body)
	}
}

func TestGr3EditOverallComment_BadJSONOrEmpty_400(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)

	for _, body := range []string{"{oops", `{"overall_comment":"   "}`} {
		req := httptest.NewRequest(http.MethodPatch, gr3CommentPath(a.ID, sub.ID), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant-Id", tenantID)
		req.Header.Set("gcid", instructor)
		req.Header.Set("x-mesh-user-roles", "instructor")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %q: want 400, got %d body=%s", body, rr.Code, rr.Body.String())
		}
	}
}

func TestGr3EditOverallComment_NotGraded_409(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), nil) // not graded
	w := reqWithHeaders(http.MethodPatch, gr3CommentPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"overall_comment": "too early",
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("overall-comment not graded: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3EditOverallComment_NotInstructor_403(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3CommentPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"overall_comment": "x",
	}), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("overall-comment non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3EditOverallComment_SaveFail_500(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.Submissions = &gr3FailSaveRepo{InMemSubmissionRepo: ad.Submissions.(*domain.InMemSubmissionRepo)}
	})
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3CommentPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"overall_comment": "lost in save",
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("overall-comment save fail: want 500, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "save failed") {
		t.Fatalf("expected save-failed message, body=%s", rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST .../approve — approveSubmissionGradingHandler (61.5% baseline)
// -----------------------------------------------------------------------------

func TestGr3Approve_Happy_AssessmentMissing_Silent(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	// Emit a sub whose PARENT ASSESSMENT was not saved: approve still works and
	// emitGradeOfRecord's assessment-lookup miss is non-fatal (title nicety).
	sub := asmNewSubmission(t, sRepo, "no-such-assessment", gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	_ = a
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath("no-such-assessment", sub.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve (assessment missing): want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.grading.submission_approved.v1"] != 1 {
		t.Fatalf("submission_approved: want 1, got %v", topics)
	}
	if topics["chora.delivery.submission.graded.v1"] != 1 {
		t.Fatalf("grade of record: want 1 graded event, got %v", topics)
	}
	if pl, ok := gr3EventByTopic(t, pub, "chora.delivery.grading.submission_approved.v1"); ok && pl["had_edits"] != false {
		t.Fatalf("fresh AI submission must approve with had_edits=false, got %v", pl)
	}
	// The approved letter state is now the grade of record.
	got, ok, err := sRepo.Get(context.Background(), tenantID, sub.ID)
	if err != nil || !ok || got.ReviewStatus != domain.ReviewStatusApproved {
		t.Fatalf("submission must be APPROVED after approve: ok=%v err=%v status=%q", ok, err, got.ReviewStatus)
	}
}

func TestGr3Approve_HumanEditedAnswers_HadEditsTrue(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	// Every answer provenance flipped HUMAN → anyAnswerHumanEdited short-circuits.
	answers := gr3GradedAnswers("tsq-oe")
	answers[1].ScoreProvenance = domain.ProvenanceHuman
	answers[1].CommentProvenance = domain.ProvenanceHuman
	answers[1].ModelAnswerProvenance = domain.ProvenanceHuman
	sub := asmNewSubmission(t, sRepo, a.ID, answers, gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath(a.ID, sub.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve (human edited): want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if pl, ok := gr3EventByTopic(t, pub, "chora.delivery.grading.submission_approved.v1"); !ok || pl["had_edits"] != true {
		t.Fatalf("human-edited answer must approve with had_edits=true, got %v", pl)
	}
}

func TestGr3Approve_OverallCommentHuman_HadEditsTrue(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), func(s *domain.Submission) {
		gr3GradedDrive(s)
		s.OverallCommentProvenance = domain.ProvenanceHuman
	})
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath(a.ID, sub.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve (overall comment human): want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if pl, ok := gr3EventByTopic(t, pub, "chora.delivery.grading.submission_approved.v1"); !ok || pl["had_edits"] != true {
		t.Fatalf("human overall comment must approve with had_edits=true, got %v", pl)
	}
}

func TestGr3Approve_NotGraded_409(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), nil) // never graded
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath(a.ID, sub.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("approve not graded: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "DELIVERY_SUBMISSION_NOT_GRADED") {
		t.Fatalf("expected NOT_GRADED code, body=%s", rr.Body.String())
	}
}

func TestGr3Approve_NilPublisher_200(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.OutboxPublisher = nil // emitSubmissionApproved + emitGradeOfRecord + emitSubmissionGradedEvent nil-guards
	})
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath(a.ID, sub.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve nil publisher: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3Approve_SaveFail_500(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.Submissions = &gr3FailSaveRepo{InMemSubmissionRepo: ad.Submissions.(*domain.InMemSubmissionRepo)}
	})
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath(a.ID, sub.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("approve save fail: want 500, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "save failed") {
		t.Fatalf("expected save-failed message, body=%s", rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST .../approve-all — approveAllSubmissionsHandler (64.6% baseline)
// -----------------------------------------------------------------------------

func TestGr3ApproveAll_Happy_NoRelease(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)

	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), mustJSON(t, map[string]any{}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all happy: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["approved_submission_count"].(float64) != 1 || body["released"] != false || len(body["skipped_submission_ids"].([]any)) != 0 {
		t.Fatalf("approve-all envelope wrong: %v", body)
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.grading.assessment_approved.v1"] != 1 ||
		topics["chora.delivery.grading.submission_approved.v1"] != 1 ||
		topics["chora.delivery.submission.graded.v1"] != 1 {
		t.Fatalf("approve-all happy events wrong: %v", topics)
	}
	if topics["chora.delivery.submission.released.v1"] != 0 {
		t.Fatalf("no release requested — released event must not fire, got %v", topics)
	}
	_ = sub
}

func TestGr3ApproveAll_Happy_WithRelease(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)

	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), mustJSON(t, map[string]any{"release": true, "release_announcement": "gr3 bulk"}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all release: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["released"] != true || body["approved_submission_count"].(float64) != 1 {
		t.Fatalf("approve-and-release envelope wrong: %v", body)
	}
	topics := asmTopics(t, pub)
	for _, want := range []string{
		"chora.delivery.grading.assessment_approved.v1",
		"chora.delivery.grading.submission_approved.v1",
		"chora.delivery.submission.graded.v1",
		"chora.delivery.assessment.released.v1",
		"chora.delivery.submission.released.v1",
	} {
		if topics[want] != 1 {
			t.Fatalf("approve-and-release event %s: want 1, got %v", want, topics)
		}
	}
}

func TestGr3ApproveAll_ReleaseRefused_DraftAssessment(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	// DRAFT assessment — ReleaseResults refuses (ErrAssessmentNotGraded) and the
	// handler logs + proceeds; submissions still flip RELEASED via
	// ReleaseAllSubmissions.
	a := asmNewAssessment(t, aRepo, nil)
	asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)

	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), mustJSON(t, map[string]any{"release": true}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all refused release: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["released"] != true || body["approved_submission_count"].(float64) != 1 {
		t.Fatalf("draft-assessment approve-all envelope wrong: %v", body)
	}
}

func TestGr3ApproveAll_Release_MissingAssessment(t *testing.T) {
	srv, _, sRepo, _ := asmServer(t, nil)
	sub := asmNewSubmission(t, sRepo, "no-such-assessment", gr3GradedAnswers("tsq-oe"), gr3GradedDrive)

	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath("no-such-assessment"), mustJSON(t, map[string]any{"release": true}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all missing assessment: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// ReleaseAllSubmissions succeeds with the (now-approved) sub even though the
	// assessment row never existed; emitReleaseEvents skips on a==nil.
	if body["released"] != true || body["approved_submission_count"].(float64) != 1 {
		t.Fatalf("missing-assessment approve-all envelope wrong: %v", body)
	}
	if got, _, _ := sRepo.Get(context.Background(), tenantID, sub.ID); got.ReviewStatus != domain.ReviewStatusApproved {
		t.Fatalf("sub should be APPROVED, got %q", got.ReviewStatus)
	}
}

func TestGr3ApproveAll_NoTenant_400(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	req := httptest.NewRequest(http.MethodPost, gr3ApproveAllPath(a.ID), nil)
	req.Header.Set("gcid", instructor)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("approve-all missing tenant: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3ApproveAll_NotInstructor_403(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), []byte(`{}`), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("approve-all non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3ApproveAll_MixedStates(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	// s1: graded + pending review → approved by the bulk action.
	s1 := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	// s2: already approved → counted, not re-approved.
	s2 := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	if err := s2.ApproveAsIs(time.Now(), instructor); err != nil {
		t.Fatalf("pre-approve s2: %v", err)
	}
	if err := sRepo.Save(context.Background(), s2); err != nil {
		t.Fatalf("save s2: %v", err)
	}
	// s3: PENDING_OE_GRADING → skipped + surfaced in skipped_submission_ids.
	s3 := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), func(s *domain.Submission) {
		s.State = domain.SubmissionStatePendingOEGrading
	})
	// s4: any other state → silently skipped.
	s4 := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), nil)

	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), mustJSON(t, map[string]any{}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all mixed: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["approved_submission_count"].(float64) != 2 {
		t.Fatalf("approved count: want 2, got %v", body["approved_submission_count"])
	}
	skipped := body["skipped_submission_ids"].([]any)
	if len(skipped) != 1 || skipped[0] != s3.ID {
		t.Fatalf("skipped ids: want [%s], got %v", s3.ID, skipped)
	}
	// s1 got approved by the bulk action; s4 was left untouched.
	if got, _, _ := sRepo.Get(context.Background(), tenantID, s1.ID); got.ReviewStatus != domain.ReviewStatusApproved {
		t.Fatalf("s1 should be APPROVED, got %q", got.ReviewStatus)
	}
	if got, _, _ := sRepo.Get(context.Background(), tenantID, s4.ID); got.ReviewStatus != "" {
		t.Fatalf("s4 must not be touched, got %q", got.ReviewStatus)
	}
}

func TestGr3ApproveAll_ListFail_500(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.Submissions = &gr3FailListRepo{InMemSubmissionRepo: ad.Submissions.(*domain.InMemSubmissionRepo)}
	})
	a := asmNewAssessment(t, aRepo, nil)
	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("approve-all list fail: want 500, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "list failed") {
		t.Fatalf("expected list-failed message, body=%s", rr.Body.String())
	}
}

func TestGr3ApproveAll_SaveFail_500(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.Submissions = &gr3FailSaveRepo{InMemSubmissionRepo: ad.Submissions.(*domain.InMemSubmissionRepo)}
	})
	a := asmNewAssessment(t, aRepo, nil)
	asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), []byte(`{}`), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("approve-all save fail: want 500, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "save failed") {
		t.Fatalf("expected save-failed message, body=%s", rr.Body.String())
	}
}

func TestGr3ApproveAll_NilPublisher_200(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.OutboxPublisher = nil // emitAssessmentApproved + emitSubmissionApproved + emitGradeOfRecord nil-guards
	})
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), mustJSON(t, map[string]any{"release": true}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all nil publisher: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Gap-closers: refs assessment-miss, AmendModelAnswer error, approve guard, and
// the approve-all assessment Get/Save error log branches.
// -----------------------------------------------------------------------------

// gr3FailAssessmentGetRepo fails AssessmentRepo.Get (approve-all release's
// aerr log branch + emitGradeOfRecord's lookup miss).
type gr3FailAssessmentGetRepo struct {
	*domain.InMemAssessmentRepo
}

func (r *gr3FailAssessmentGetRepo) Get(_ context.Context, _, _ string) (*domain.Assessment, bool, error) {
	return nil, false, errors.New("gr3: forced assessment get failure")
}

// gr3FailAssessmentSaveRepo fails AssessmentRepo.Save (approve-all release's
// "released in memory but NOT saved" log branch).
type gr3FailAssessmentSaveRepo struct {
	*domain.InMemAssessmentRepo
}

func (r *gr3FailAssessmentSaveRepo) Save(_ context.Context, _ *domain.Assessment) error {
	return errors.New("gr3: forced assessment save failure")
}

func TestGr3EditGrades_AssessmentNotSaved_200(t *testing.T) {
	srv, _, sRepo, _ := asmServer(t, nil)
	// The submission exists but its parent assessment was never saved —
	// loadTestSetQuestionRefs must fail-soft to an empty map (still 200).
	sub := asmNewSubmission(t, sRepo, "no-such-assessment", gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath("no-such-assessment", sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{{"test_set_question_id": "tsq-oe", "points_earned": 4.0}},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("grades with missing assessment: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3EditGrades_ModelAnswerUnknownTSQ_404(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPatch, gr3GradesPath(a.ID, sub.ID), mustJSON(t, map[string]any{
		"question_edits": []map[string]any{
			{"test_set_question_id": "missing-oe", "model_answer": "x"},
		},
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("amend unknown tsq: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "oe answer not found") {
		t.Fatalf("expected oe-answer-not-found message, body=%s", rr.Body.String())
	}
}

func TestGr3Approve_NotInstructor_403(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	sub := asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApprovePath(a.ID, sub.ID), []byte(`{}`), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("approve non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGr3ApproveAll_Release_AssessmentGetError_Still200(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		// AssessmentRepo.Get always fails → the release block's aerr log branch,
		// and emitGradeOfRecord's title lookup goes quiet.
		ad.Assessments = &gr3FailAssessmentGetRepo{InMemAssessmentRepo: ad.Assessments.(*domain.InMemAssessmentRepo)}
	})
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), mustJSON(t, map[string]any{"release": true}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all assessment-get error: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["released"] != true || body["approved_submission_count"].(float64) != 1 {
		t.Fatalf("assessment-get-error approve-all envelope wrong: %v", body)
	}
}

func TestGr3ApproveAll_Release_AssessmentSaveFail_Still200(t *testing.T) {
	var wrapped *gr3FailAssessmentSaveRepo
	srv, _, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		// AssessmentRepo.Save always fails → the "released in memory but NOT
		// saved" log branch.
		wrapped = &gr3FailAssessmentSaveRepo{InMemAssessmentRepo: ad.Assessments.(*domain.InMemAssessmentRepo)}
		ad.Assessments = wrapped
	})
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		Title:            "gr3 save-fail assessment",
		ScheduledOpenAt:  time.Now().Add(-1 * time.Minute),
		ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts:      2,
		TotalPoints:      100,
		QuestionCount:    1,
		InvitedGCIDs:     []string{learner},
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	a.State = domain.AssessmentStateOpen
	// Seed via the EMBEDDED (non-failing) repo — the wrapper only fails
	// handler-time saves.
	if err := wrapped.InMemAssessmentRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("save assessment: %v", err)
	}
	asmNewSubmission(t, sRepo, a.ID, gr3GradedAnswers("tsq-oe"), gr3GradedDrive)
	w := reqWithHeaders(http.MethodPost, gr3ApproveAllPath(a.ID), mustJSON(t, map[string]any{"release": true}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-all assessment-save error: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["released"] != true || body["approved_submission_count"].(float64) != 1 {
		t.Fatalf("assessment-save-error approve-all envelope wrong: %v", body)
	}
}
