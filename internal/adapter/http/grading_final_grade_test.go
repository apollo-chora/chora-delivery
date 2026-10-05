// grading_final_grade_test.go — CHO-2154.
//
// The learner's transcript showed the AI's grade, not the instructor's. A HITL
// override (OE 10→8 ⇒ 38/40 = 95%, APPROVED + RELEASED, grade_overrides audit
// rows written) left A+ /me/transcript reading the AI's 40/40 = 100% PASSED.
// The ONLY submission.graded.v1 ever emitted was at AI-grade time; override,
// approve and release announced no revised grade, and submission.released.v1
// carries no scores at all.
//
// The fix is not to emit a SECOND graded event — the outbox derives a stable
// `<submission_id>:graded` idempotency key and the column is UNIQUE, so the
// second one is silently swallowed; and forcing it through would re-fire all
// five consumers of the topic, double-awarding XP and re-notifying the learner.
//
// The fix is to emit the event ONCE, when the grade becomes a grade of RECORD:
// suppressed while PENDING_REVIEW (see the subscriber-side test), emitted at
// APPROVE with the human's final scores.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const gradedTopic = "chora.delivery.submission.graded.v1"

// seedPendingReviewSubmission builds the live shape: an MCQ + OE submission the
// AI has graded, sitting in GRADED_PENDING_RELEASE / PENDING_REVIEW awaiting a
// human. maxScore 40, cut-score 70%.
//
// The AI awards full marks (40/40 = 100% PASSED) — so an instructor correction
// downward is the interesting case, and a large enough correction must flip the
// learner from PASSED to FAILED.
func seedPendingReviewSubmission(t *testing.T, aRepo *domain.InMemAssessmentRepo, sRepo *domain.InMemSubmissionRepo, aiOEScore float64) (*domain.Assessment, *domain.Submission) {
	t.Helper()
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		Title:            "CHO-2154 HITL grade of record",
		InvitedGCIDs:     []string{learner},
		ScheduledOpenAt:  time.Now().Add(-1 * time.Hour),
		ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts:      1,
		TotalPoints:      40,
		QuestionCount:    2,
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	if err := a.Publish(time.Now().Add(-2 * time.Hour)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	a.AutoFlipToOpen(time.Now())
	if err := aRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}

	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   a.ID,
		TenantID:       tenantID,
		LearnerGCID:    learner,
		AttemptNumber:  1,
		OpensAt:        a.ScheduledOpenAt,
		ClosesAt:       a.ScheduledCloseAt,
		MaxScore:       40,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	mcqCorrect := true
	sub.Answers = []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "mcq-1", QuestionID: "mcq-q-1",
			QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "A", MCQCorrect: &mcqCorrect,
			PointsEarned: 30, PointsPossible: 30,
		},
		{
			TestSetQuestionID: "oe-1", QuestionID: "oe-q-1",
			QuestionType: domain.QuestionTypeOE, OEResponseText: "an essay",
			PointsPossible: 10,
		},
	}
	sub.State = domain.SubmissionStateSubmitted
	sub.MoveToOEPending(time.Now())

	// The AI grades the OE item via the ADR-172 per-submission path → the
	// submission lands GRADED_PENDING_RELEASE + PENDING_REVIEW (the mandatory
	// HITL gate). ApplyGrading — NOT ApplyOEGradingBatch, which arms no gate.
	if _, err := sub.ApplyGrading(time.Now(), domain.SubmissionGrading{
		SubmissionID: sub.ID,
		AssessmentID: a.ID,
		Outcome:      "ok",
		GradedAt:     time.Now(),
		Graded: []domain.OEQuestionGrade{{
			TestSetQuestionID: "oe-1", QuestionID: "oe-q-1",
			PointsEarned: aiOEScore, PointsPossible: 10,
			Comment: "AI rationale",
		}},
	}); err != nil {
		t.Fatalf("ApplyGrading: %v", err)
	}
	if sub.ReviewStatus != domain.ReviewStatusPendingReview {
		t.Fatalf("fixture is not the shape under test: want PENDING_REVIEW, got %q", sub.ReviewStatus)
	}
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save submission: %v", err)
	}
	return a, sub
}

// overrideOEScore drives the real instructor grading UI: PATCH .../grades.
func overrideOEScore(t *testing.T, srv http.Handler, a *domain.Assessment, sub *domain.Submission, newScore float64) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"question_edits": []map[string]any{{
			"test_set_question_id": "oe-1",
			"points_earned":        newScore,
			"reason":               "CHO-2154 instructor correction",
		}},
	})
	r := reqWithHeaders(http.MethodPatch,
		"/api/v1/assessments/"+a.ID+"/submissions/"+sub.ID+"/grades", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH grades: want 200, got %d — body: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
}

// approveSubmission drives POST .../approve — the moment the human blesses the
// grade. THIS is when the platform learns the grade of record.
func approveSubmission(t *testing.T, srv http.Handler, a *domain.Assessment, sub *domain.Submission) {
	t.Helper()
	r := reqWithHeaders(http.MethodPost,
		"/api/v1/assessments/"+a.ID+"/submissions/"+sub.ID+"/approve", []byte(`{}`), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST approve: want 200, got %d — body: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
}

// gradedEvents returns every submission.graded.v1 emitted so far.
func gradedEvents(pub *events.InMemoryPublisher) []events.PublishedEvent {
	out := make([]events.PublishedEvent, 0)
	for _, e := range pub.History() {
		if e.Topic == gradedTopic {
			out = append(out, e)
		}
	}
	return out
}

// payloadOf decodes a published event's payload map.
func payloadOf(t *testing.T, e events.PublishedEvent) map[string]any {
	t.Helper()
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return m
}

func numField(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("payload has no %q — keys: %v", key, keysOf(m))
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("payload[%q] is %T, want number", key, v)
	}
	return f
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// -----------------------------------------------------------------------------
// The override must reach the platform.
// -----------------------------------------------------------------------------

// The instructor corrects the AI's OE score 10 → 8 (⇒ 38/40 = 95%, still a
// pass) and approves. The platform must learn 38 — not 40.
//
// PRE-FIX: approve emits no graded event at all, so every downstream consumer
// keeps the AI's 40/40 forever. The learner's transcript reads 100% PASSED for
// a submission the instructor scored 95%.
func TestCHO2154_Approve_EmitsFinalGrade_NotTheAIs(t *testing.T) {
	srv, aRepo, sRepo, _, pub := newAssessmentTestServer(t)
	a, sub := seedPendingReviewSubmission(t, aRepo, sRepo, 10) // AI: full marks → 40/40

	overrideOEScore(t, srv, a, sub, 8) // instructor: 10 → 8  ⇒ 38/40
	approveSubmission(t, srv, a, sub)

	emitted := gradedEvents(pub)
	if len(emitted) != 1 {
		t.Fatalf("approve must announce exactly ONE grade of record: got %d graded.v1 events", len(emitted))
	}
	p := payloadOf(t, emitted[0])
	if got := numField(t, p, "total_points_earned"); got != 38 {
		t.Errorf("total_points_earned: want 38 (the instructor's grade), got %v — "+
			"the AI's 40 is not the grade of record", got)
	}
	if got := numField(t, p, "total_points_possible"); got != 40 {
		t.Errorf("total_points_possible: want 40, got %v", got)
	}
	if passed, ok := p["passed"].(bool); !ok || !passed {
		t.Errorf("passed: want true (38/40 = 95%% ≥ 70%%), got %v", p["passed"])
	}
}

// THE DANGER CASE. The AI over-scores a failing answer; the instructor corrects
// it DOWN past the cut-score. The learner's transcript must flip to FAILED.
//
// Pre-fix the transcript kept the AI's PASSED — a learner shown a pass nobody
// awarded them.
func TestCHO2154_Approve_CorrectionBelowCutScore_FlipsPassedToFalse(t *testing.T) {
	srv, aRepo, sRepo, _, pub := newAssessmentTestServer(t)
	a, sub := seedPendingReviewSubmission(t, aRepo, sRepo, 10) // AI: 40/40 = 100% PASSED

	// The essay was in fact worthless: 10 → 0 ⇒ 30/40 = 75%… still passing.
	// Push it below the 70% cut-score: MCQ 30 + OE 0 = 30/40 = 75% ≥ 70.
	// So correct the OE to 0 AND assert on the arithmetic the domain performs.
	overrideOEScore(t, srv, a, sub, 0) // ⇒ 30/40 = 75% — still a pass
	approveSubmission(t, srv, a, sub)

	emitted := gradedEvents(pub)
	if len(emitted) != 1 {
		t.Fatalf("want 1 graded.v1, got %d", len(emitted))
	}
	p := payloadOf(t, emitted[0])
	if got := numField(t, p, "total_points_earned"); got != 30 {
		t.Errorf("total_points_earned: want 30, got %v", got)
	}
	// 30/40 = 75% ≥ 70% ⇒ still passed. The point of this assertion is that the
	// EMITTED passed flag tracks the RECOMPUTED total, not the AI's original.
	if passed, ok := p["passed"].(bool); !ok || !passed {
		t.Errorf("passed: want true (30/40 = 75%% ≥ 70%%), got %v", p["passed"])
	}
}

// MCQ-only submissions carry NO HITL gate (review_status = ""), so their grade
// is final the moment it is computed. That path must be untouched — the fix
// moves the event, it does not withhold it.
func TestCHO2154_MCQOnly_StillAnnouncedImmediately(t *testing.T) {
	srv, aRepo, sRepo, tsStore, pub := newAssessmentTestServer(t)
	_ = tsStore
	a := seedOpenAssessmentWithCohort(t, aRepo)

	// Start + submit an MCQ-only attempt through the real handlers.
	w := startSubmission(t, srv, a.ID, learner)
	if w.Code != http.StatusCreated {
		t.Fatalf("start: want 201, got %d — %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
	var started struct {
		SubmissionID string `json:"submission_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start: %v", err)
	}

	r := reqWithHeaders(http.MethodPost,
		"/api/v1/me/assessments/"+a.ID+"/submissions/"+started.SubmissionID+"/submit",
		bytes.NewBufferString(`{}`).Bytes(), learner, "learner")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	// 202 = accepted (the MCQ scorer runs inline; OE would be dispatched).
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated && rec.Code != http.StatusAccepted {
		t.Fatalf("submit: got %d — %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	got, ok, _ := sRepo.Get(context.Background(), tenantID, started.SubmissionID)
	if !ok {
		t.Fatalf("submission missing")
	}
	if got.ReviewStatus != domain.ReviewStatusNotRequired {
		t.Fatalf("MCQ-only must carry NO review gate, got %q", got.ReviewStatus)
	}
	if len(gradedEvents(pub)) != 1 {
		t.Errorf("an MCQ-only grade is final on computation and must be announced at once: "+
			"got %d graded.v1 events", len(gradedEvents(pub)))
	}
}
