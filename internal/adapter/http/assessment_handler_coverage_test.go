// assessment_handler_coverage_test.go — statement-coverage battery for
// internal/adapter/http/assessment_handler.go. Supplements
// assessment_handler_test.go with the remaining branches: GET /assessments
// detail + monitor, release-results, learner detail (with questions +
// image resolution), autosave/submit error paths, OE dispatch with
// snapshots, start-submission window/FSE gates, and the media/criterion
// projection helpers reached through the RELEASED result reveal.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

// -----------------------------------------------------------------------------
// Fixture helpers (asm* prefix — file-local, no collisions with the shared
// newAssessmentTestServer* helpers in assessment_handler_test.go).
// -----------------------------------------------------------------------------

// asmServer wires an AssessmentDeps-backed server; mut lets tests add
// optional deps (MediaResolver, LearnerDirectory, Offerings…).
func asmServer(t *testing.T, mut func(*httpapi.AssessmentDeps)) (http.Handler, *domain.InMemAssessmentRepo, *domain.InMemSubmissionRepo, *events.InMemoryPublisher) {
	t.Helper()
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	roster := domain.NewInMemCohortRoster()
	aRepo.SetRosterLink(roster)
	tsStore := httpapi.NewInMemTestSetStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	ad := &httpapi.AssessmentDeps{
		Assessments:     aRepo,
		Submissions:     sRepo,
		Roster:          roster,
		TestSets:        tsStore,
		OutboxPublisher: pub,
	}
	if mut != nil {
		mut(ad)
	}
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		TestSets:       tsStore,
		AssessmentDeps: ad,
	})
	return srv, aRepo, sRepo, pub
}

// asmNewAssessment builds + saves an assessment. Extra fields can be set via
// opt before Save (e.g. a.State = domain.AssessmentStateOpen).
func asmNewAssessment(t *testing.T, aRepo *domain.InMemAssessmentRepo, opt func(*domain.Assessment)) *domain.Assessment {
	t.Helper()
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		Title:            "Coverage Assessment",
		ScheduledOpenAt:  time.Now().Add(-1 * time.Minute),
		ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts:      2,
		TotalPoints:      100,
		QuestionCount:    1,
		InvitedGCIDs:     []string{learner},
		GradingConfigSnapshot: domain.GradingConfigSnapshot{
			MCQDispatch:             "DETERMINISTIC",
			OEDispatch:              "LLM_EVALUATOR_AGENT",
			PassingThresholdPercent: 70,
		},
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	if opt != nil {
		opt(a)
	}
	if err := aRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}
	return a
}

// asmNewSubmission builds + saves a submission for the learner. answers are
// attached when non-nil; state can be pre-driven via the `drive` func.
func asmNewSubmission(t *testing.T, sRepo *domain.InMemSubmissionRepo, assessmentID string, answers []domain.SubmissionAnswer, drive func(*domain.Submission)) *domain.Submission {
	t.Helper()
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   assessmentID,
		TenantID:       tenantID,
		LearnerGCID:    learner,
		AttemptNumber:  1,
		OpensAt:        time.Now().Add(-1 * time.Minute),
		ClosesAt:       time.Now().Add(2 * time.Hour),
		MaxScore:       100,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if answers != nil {
		sub.Answers = answers
	}
	if drive != nil {
		drive(sub)
	}
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save submission: %v", err)
	}
	return sub
}

// asmTopics returns topic → occurrence count from the in-memory publisher.
func asmTopics(t *testing.T, pub *events.InMemoryPublisher) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, ev := range pub.History() {
		out[ev.Topic]++
	}
	return out
}

// asmMediaResolver is a fake httpapi.MediaURLResolver that signs every gs://
// ref with a deterministic https:// prefix (fail-visible otherwise).
type asmMediaResolver struct {
	mu map[string]string
}

func (f asmMediaResolver) ResolveDownloadURLs(_ context.Context, _ string, uris []string) map[string]string {
	if f.mu == nil {
		return nil
	}
	out := map[string]string{}
	for _, u := range uris {
		if signed, ok := f.mu[u]; ok {
			out[u] = signed
		}
	}
	return out
}

// noCustomPublisher is an events.Publisher that does NOT implement
// PublishCustom — drives publishCustomEvent's drop-branch (else path).
type noCustomPublisher struct {
	events.Publisher
}

// -----------------------------------------------------------------------------
// callerTenantGCID — missing gcid ⇒ 401 (only branch left uncovered)
// -----------------------------------------------------------------------------

func TestGetAssessment_MissingGCIDHeader_401(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assessments/some-id", nil)
	req.Header.Set("X-Tenant-Id", tenantID)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing gcid: want 401, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// getAssessmentHandler (0% baseline) — GET /api/v1/assessments/{id}
// -----------------------------------------------------------------------------

func TestGetAssessment_InstructorRole_200(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID, nil, gcidB, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("instructor GET: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["assessment_id"] != a.ID || body["tenant_id"] != tenantID {
		t.Fatalf("DTO identity wrong: %v", body)
	}
}

func TestGetAssessment_SelfInstructor_NoRole_200(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil) // a.InstructorGCID == instructor
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID, nil, instructor, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("self-instructor GET: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGetAssessment_OtherLearner_Forbidden_403(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil) // instructor == instructor, caller is gcidB learner
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID, nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-instructor non-self GET: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGetAssessment_Missing_404(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/no-such-id", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing GET: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// monitorAssessmentHandler (0% baseline) — GET /api/v1/assessments/{id}/monitor
// -----------------------------------------------------------------------------

func TestMonitorAssessment_NoSubmissions_AverageNil(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/monitor", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("monitor: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["average_score_percent"] != nil {
		t.Fatalf("expected nil average with no graded samples, got %v", body["average_score_percent"])
	}
	if body["assessment_id"] != a.ID || body["state"] != "OPEN" {
		t.Fatalf("monitor identity wrong: %v", body)
	}
	if _, ok := body["per_question_pass_rate"]; !ok {
		t.Fatalf("expected per_question_pass_rate key")
	}
}

func TestMonitorAssessment_WithGradedSubmission_AveragePresent(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	asmNewSubmission(t, sRepo, a.ID, []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "q-mcq-1",
			QuestionID:        "qid-mcq-1",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "opt-a",
		},
	}, func(s *domain.Submission) {
		// MonitorCounts classifies a RELEASED row as a graded sample.
		s.State = domain.SubmissionStateReleased
		s.TotalScore = 80
	})
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/monitor", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("monitor: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["average_score_percent"] == nil {
		t.Fatalf("expected average with a released graded submission, got %v", body)
	}
	if v, _ := body["total_graded"].(float64); v != 1 {
		t.Fatalf("expected total_graded=1, got %v", body["total_graded"])
	}
}

func TestMonitorAssessment_NotInstructor_403(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/monitor", nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("monitor non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMonitorAssessment_Missing_404(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/nope/monitor", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("monitor missing: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// releaseAssessmentResultsHandler — POST /api/v1/assessments/{id}/release-results
// -----------------------------------------------------------------------------

func TestReleaseResults_FromDraft_409(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil) // stays DRAFT
	w := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/release-results", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("draft release: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestReleaseResults_NotInstructor_403(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/release-results", nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("release non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestReleaseResults_Missing_404(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/assessments/nope/release-results", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("release missing: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestReleaseResults_Happy_EmitsReleaseEvents(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	asmNewSubmission(t, sRepo, a.ID, nil, func(s *domain.Submission) {
		// ReleaseAllSubmissions only flips GRADED_PENDING_RELEASE rows.
		s.MarkSubmitted(time.Now())
		s.MarkGradedPendingRelease(time.Now())
	})
	// Malformed release body must not fail the release (decode is best-effort).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assessments/"+a.ID+"/release-results", bytes.NewReader([]byte("{not-json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantID)
	req.Header.Set("gcid", instructor)
	req.Header.Set("x-mesh-user-roles", "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("release happy: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["state"] != "RELEASED" {
		t.Fatalf("expected RELEASED DTO, got %q", body["state"])
	}
	if _, ok := body["results_released_at"]; !ok {
		t.Fatalf("expected results_released_at on released DTO: %v", body)
	}
	if _, ok := body["released_at"]; !ok {
		t.Fatalf("expected released_at alias on released DTO")
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.assessment.released.v1"] != 1 {
		t.Fatalf("expected assessment.released v1 exactly once, got %v", topics)
	}
	if topics["chora.delivery.submission.released.v1"] != 1 {
		t.Fatalf("expected submission.released v1 exactly once, got %v", topics)
	}
}

// -----------------------------------------------------------------------------
// Learner detail — getMyAssessmentHandler + learnerAssessmentDetailDTO (0%)
// -----------------------------------------------------------------------------

func TestGetMyAssessment_DetailWithQuestionsAndImages(t *testing.T) {
	tsStore := httpapi.NewInMemTestSetStore()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID: tenantID, AuthorGCID: instructor, Title: "cover ts",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	mcqQ, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-mcq-1", QuestionID: "qid-mcq-1",
		QuestionType: "mcq", DisplayOrder: 1, Points: 10,
	})
	if err != nil {
		t.Fatalf("AddQuestion mcq: %v", err)
	}
	mcqQ.PayloadSnapshot = `{"stem":"Which planet?","image_url":"gs://bucket/stem-mcq.png","options":[{"option_id":"opt-a","label":"Earth","is_correct":true},{"option_id":"opt-b","label":"Mars","is_correct":false}]}`
	oeQ, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-oe-1", QuestionID: "qid-oe-1",
		QuestionType: "oe", DisplayOrder: 2, Points: 20,
	})
	if err != nil {
		t.Fatalf("AddQuestion oe: %v", err)
	}
	oeQ.PayloadSnapshot = `{"stem":"Explain","image_url":"gs://bucket/stem-oe.png","prompt":"Why?","max_words":300,"model_answer":"secret"}`

	srv, aRepo, _, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = tsStore
		ad.MediaResolver = asmMediaResolver{mu: map[string]string{
			"gs://bucket/stem-mcq.png": "https://signed/stem-mcq.png",
			"gs://bucket/stem-oe.png":  "https://signed/stem-oe.png",
		}}
	})
	_ = tsStore.Save(context.Background(), ts)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.TestSetID = ts.ID
		a.State = domain.AssessmentStateOpen
		a.ShuffleMCQOptions = true
	})
	_ = mcqQ
	_ = oeQ

	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+a.ID, nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("learner detail: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Questions []map[string]interface{} `json:"questions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Questions) != 2 {
		t.Fatalf("expected 2 learner-safe questions, got %d body=%s", len(body.Questions), rr.Body.String())
	}
	byType := map[string]map[string]interface{}{}
	for _, q := range body.Questions {
		byType[q["question_type"].(string)] = q
	}
	mcqDTO := byType["mcq"]
	prompt := mcqDTO["prompt"].(map[string]interface{})
	if prompt["image_url"] != "https://signed/stem-mcq.png" {
		t.Fatalf("expected signed mcq image_url, got %v", prompt["image_url"])
	}
	if _, leaked := prompt["is_correct"]; leaked {
		t.Fatalf("learner-safe MCQ leaked is_correct")
	}
	opts := prompt["options"].([]interface{})
	if len(opts) != 2 {
		t.Fatalf("expected 2 options, got %d", len(opts))
	}
	oeDTO := byType["oe"]
	oePrompt := oeDTO["prompt"].(map[string]interface{})
	if oePrompt["image_url"] != "https://signed/stem-oe.png" {
		t.Fatalf("expected signed oe image_url, got %v", oePrompt["image_url"])
	}
	if _, leaked := oePrompt["model_answer"]; leaked {
		t.Fatalf("learner-safe OE leaked model_answer")
	}
}

func TestGetMyAssessment_NoTestSetPort_EmptyQuestions(t *testing.T) {
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	roster := domain.NewInMemCohortRoster()
	aRepo.SetRosterLink(roster)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	srv := httpapi.NewServer(httpapi.Deps{
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments: aRepo, Submissions: sRepo, Roster: roster,
		},
	})
	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+a.ID, nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail no testset: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Questions []interface{} `json:"questions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Questions == nil || len(body.Questions) != 0 {
		t.Fatalf("expected empty questions slice, got %v", body.Questions)
	}
}

func TestGetMyAssessment_NotEligible_403(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	// gcidB is NOT on the invited cohort → Forbidden.
	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+a.ID, nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-eligible learner detail: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGetMyAssessment_Missing_404(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/nope", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing my assessment: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// autosaveMySubmissionHandler — error paths + type inference
// -----------------------------------------------------------------------------

func TestAutosave_NotYourSubmission_403(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, nil)
	body := map[string]interface{}{
		"answers": []map[string]string{{"test_set_question_id": "q1", "question_id": "qid1", "mcq_choice_id": "opt-a"}},
	}
	w := reqWithHeaders(http.MethodPatch, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID, mustJSON(t, body), gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("autosave foreign sub: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAutosave_BadJSON_400(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, nil)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID, bytes.NewReader([]byte("{oops")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantID)
	req.Header.Set("gcid", learner)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("autosave bad json: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAutosave_EmptyAnswers_400(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, nil)
	w := reqWithHeaders(http.MethodPatch, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID, mustJSON(t, map[string]interface{}{"answers": []interface{}{}}), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("autosave empty answers: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAutosave_WindowClosed_409(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: time.Now().Add(-2 * time.Hour),
		ClosesAt: time.Now().Add(-1 * time.Hour), MaxScore: 100, PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save: %v", err)
	}
	w := reqWithHeaders(http.MethodPatch, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID, mustJSON(t, map[string]interface{}{
		"answers": []map[string]string{{"question_id": "q1"}},
	}), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("autosave closed window: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("DELIVERY_SUBMISSION_WINDOW_CLOSED")) {
		t.Fatalf("expected window-closed code, body=%s", rr.Body.String())
	}
}

func TestAutosave_Happy_MixedTypes(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, nil)
	w := reqWithHeaders(http.MethodPatch, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID, mustJSON(t, map[string]interface{}{
		"answers": []map[string]interface{}{
			{"test_set_question_id": "q-mcq", "question_id": "qid-mcq", "question_type": "mcq", "mcq_choice_id": "opt-a"},
			{"test_set_question_id": "q-oe", "question_id": "qid-oe", "question_type": "oe", "oe_response_text": "text"},
			{"test_set_question_id": "q-auto", "question_id": "qid-auto", "oe_response_text": "auto-detect"},
			{"test_set_question_id": "q-multi", "question_id": "qid-multi", "mcq_choice_ids": []string{"o1", "o2"}},
		},
	}), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("autosave happy: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Answers []map[string]interface{} `json:"answers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Answers) != 4 {
		t.Fatalf("expected 4 echoed answers, got %d", len(body.Answers))
	}
}

// -----------------------------------------------------------------------------
// submitMySubmissionHandler — OE dispatch, snapshot grading, replay
// -----------------------------------------------------------------------------

// asmSeedOEQuestion adds an OE question with a rubric/model-answer snapshot.
func asmSeedOEQuestion(t *testing.T, tsStore *httpapi.InMemTestSetStore, tsID string) string {
	t.Helper()
	ts, ok, err := tsStore.Get(context.Background(), tenantID, tsID)
	if err != nil || !ok || ts == nil {
		t.Fatalf("Get testset %s: ok=%v err=%v", tsID, ok, err)
	}
	q, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-oe-1", QuestionID: "qid-oe-1",
		QuestionType: "oe", DisplayOrder: 1, Points: 20,
	})
	if err != nil {
		t.Fatalf("AddQuestion oe: %v", err)
	}
	q.PayloadSnapshot = `{"stem":"Discuss","prompt":"Explain in detail","subject":"Physics","topic":"Forces","model_answer":"Reference answer","rubric":[{"criterion_id":"c1","title":"Clarity","description":"is it clear","weight":1}]}`
	if err := tsStore.Save(context.Background(), ts); err != nil {
		t.Fatalf("Save testset: %v", err)
	}
	return q.ID
}

func TestSubmit_OEPath_EmitsRequestedEnvelope(t *testing.T) {
	tsStore := httpapi.NewInMemTestSetStore()
	srv, aRepo, sRepo, pub := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = tsStore
	})
	ts, err := domain.NewTestSet(domain.NewTestSetInput{TenantID: tenantID, AuthorGCID: instructor, Title: "ts"})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if err := tsStore.Save(context.Background(), ts); err != nil {
		t.Fatalf("save ts: %v", err)
	}
	oeQID := asmSeedOEQuestion(t, tsStore, ts.ID)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.TestSetID = ts.ID
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, []domain.SubmissionAnswer{
		{
			TestSetQuestionID: oeQID,
			QuestionID:        "qid-oe-1",
			QuestionType:      domain.QuestionTypeOE,
			OEResponseText:    "My essay about forces.",
		},
	}, nil)

	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/submit", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("OE submit: want 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		GradingETASeconds int    `json:"grading_eta_seconds"`
		State             string `json:"state"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.GradingETASeconds != 60 {
		t.Fatalf("OE submit ETA: want 60, got %d", body.GradingETASeconds)
	}
	topics := asmTopics(t, pub)
	for _, want := range []string{
		"chora.delivery.grading.submission_requested.v1",
		"chora.delivery.grading.mcq_completed.v1",
		"chora.delivery.submission.submitted.v1",
	} {
		if topics[want] == 0 {
			t.Fatalf("expected topic %s on OE submit, got %v", want, topics)
		}
	}
	// Submission must have moved to PENDING_OE_GRADING.
	got, ok, err := sRepo.Get(context.Background(), tenantID, sub.ID)
	if err != nil || !ok {
		t.Fatalf("refetch sub: ok=%v err=%v", ok, err)
	}
	if got.State != domain.SubmissionStatePendingOEGrading {
		t.Fatalf("expected PENDING_OE_GRADING, got %s", got.State)
	}
}

func TestSubmit_MCQOnly_AutoRelease_Happy(t *testing.T) {
	tsStore := httpapi.NewInMemTestSetStore()
	srv, aRepo, sRepo, pub := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = tsStore
	})
	ts, err := domain.NewTestSet(domain.NewTestSetInput{TenantID: tenantID, AuthorGCID: instructor, Title: "ts"})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	mcqQ, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-mcq-1", QuestionID: "qid-mcq-1",
		QuestionType: "mcq", DisplayOrder: 1, Points: 10,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	mcqQ.PayloadSnapshot = `{"options":[{"option_id":"opt-a","label":"Earth","is_correct":true}],"scoring":{"mode":"single_correct"}}`
	if err := tsStore.Save(context.Background(), ts); err != nil {
		t.Fatalf("save ts: %v", err)
	}
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.TestSetID = ts.ID
		a.State = domain.AssessmentStateOpen
		a.GradingConfigSnapshot.AutoRelease = true
	})
	sub := asmNewSubmission(t, sRepo, a.ID, []domain.SubmissionAnswer{
		{
			TestSetQuestionID: mcqQ.ID,
			QuestionID:        "qid-mcq-1",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "opt-a",
		},
	}, nil)

	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/submit", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("MCQ submit: want 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	got, _, _ := sRepo.Get(context.Background(), tenantID, sub.ID)
	if got.State != domain.SubmissionStateReleased {
		t.Fatalf("auto-release expected RELEASED, got %s", got.State)
	}
	if got.Answers[0].PointsEarned != 10 {
		t.Fatalf("expected 10 points from snapshot grading, got %f", got.Answers[0].PointsEarned)
	}
	if got.Answers[0].MCQCorrect == nil || !*got.Answers[0].MCQCorrect {
		t.Fatalf("expected MCQCorrect=true")
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.submission.released.v1"] != 1 {
		t.Fatalf("expected submission.released v1, got %v", topics)
	}
}

func TestSubmit_IdempotentReplay_200(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "q-mcq",
			QuestionID:        "qid-mcq",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "opt-a",
		},
	}, nil)

	first := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/submit", nil, learner, "")
	rr1 := httptest.NewRecorder()
	srv.ServeHTTP(rr1, first)
	if rr1.Code != http.StatusAccepted {
		t.Fatalf("first submit: want 202, got %d body=%s", rr1.Code, rr1.Body.String())
	}
	second := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/submit", nil, learner, "")
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, second)
	if rr2.Code != http.StatusOK {
		t.Fatalf("replay submit: want 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.grading.mcq_completed.v1"] != 1 {
		t.Fatalf("replay must not re-emit mcq_completed, got %v", topics)
	}
}

func TestSubmit_NotYourSubmission_403(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/submit", nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign submit: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSubmit_Missing_404(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions/nope/submit", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing submit: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSubmit_MCQSnapshotMissing_EmitsFailLoud(t *testing.T) {
	srv, aRepo, sRepo, pub := asmServer(t, nil) // TestSets wired but empty
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	// No question row for this tsqid → snapshot missing → equal-weight fallback + fail-loud event.
	sub := asmNewSubmission(t, sRepo, a.ID, []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "nowhere",
			QuestionID:        "qid-mcq",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "opt-a",
		},
	}, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/submit", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("snapshot-missing submit: want 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	topics := asmTopics(t, pub)
	if topics["chora.delivery.grading.mcq_snapshot_missing.v1"] != 1 {
		t.Fatalf("expected mcq_snapshot_missing.fail-loud event, got %v", topics)
	}
	got, _, _ := sRepo.Get(context.Background(), tenantID, sub.ID)
	if got.Answers[0].PointsEarned != 0 {
		t.Fatalf("missing snapshot must grade zero, got %f", got.Answers[0].PointsEarned)
	}
	if got.Answers[0].PointsPossible != 100 {
		t.Fatalf("equal-weight fallback: want 100, got %d", got.Answers[0].PointsPossible)
	}
}

// -----------------------------------------------------------------------------
// getMySubmissionHandler — mismatched assessment 404
// -----------------------------------------------------------------------------

func TestGetMySubmission_WrongAssessment_404(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	other := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, nil)
	// Ask via `other`'s assessment id — mismatches sub.AssessmentID → 404.
	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+other.ID+"/submissions/"+sub.ID, nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("mismatched assessment GET: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// startMySubmissionHandler — window / FSM / attempts gates
// -----------------------------------------------------------------------------

func TestStartSubmission_NotYetOpen_409(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.ScheduledOpenAt = time.Now().Add(2 * time.Hour)
		a.ScheduledCloseAt = time.Now().Add(4 * time.Hour)
		a.State = domain.AssessmentStateScheduled
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("not-yet-open: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("DELIVERY_ASSESSMENT_NOT_YET_OPEN")) {
		t.Fatalf("expected NOT_YET_OPEN code, body=%s", rr.Body.String())
	}
}

func TestStartSubmission_WindowClosed_409(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.ScheduledOpenAt = time.Now().Add(-4 * time.Hour)
		a.ScheduledCloseAt = time.Now().Add(-1 * time.Hour)
		a.State = domain.AssessmentStateOpen
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("closed window: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("DELIVERY_ASSESSMENT_WINDOW_CLOSED")) {
		t.Fatalf("expected WINDOW_CLOSED code, body=%s", rr.Body.String())
	}
}

func TestStartSubmission_NotOpenState_409(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateGrading // window open but FSM not OPEN
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("non-open state: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("DELIVERY_ASSESSMENT_NOT_OPEN")) {
		t.Fatalf("expected NOT_OPEN code, body=%s", rr.Body.String())
	}
}

func TestStartSubmission_AttemptsExhausted_409(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
		a.MaxAttempts = 1
	})
	// Existing submitted attempt counts toward the max.
	asmNewSubmission(t, sRepo, a.ID, nil, func(s *domain.Submission) {
		s.AttemptNumber = 1
		s.MarkSubmitted(time.Now())
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusConflict {
		t.Fatalf("attempts exhausted: want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("DELIVERY_SUBMISSION_MAX_ATTEMPTS_EXHAUSTED")) {
		t.Fatalf("expected MAX_ATTEMPTS code, body=%s", rr.Body.String())
	}
}

func TestStartSubmission_InProgress_Replay_200(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	// An IN_PROGRESS submission exists → start returns it idempotently.
	asmNewSubmission(t, sRepo, a.ID, nil, func(s *domain.Submission) {
		s.State = domain.SubmissionStateInProgress
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("in-progress replay: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		IdempotentReplay bool `json:"idempotent_replay"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.IdempotentReplay {
		t.Fatalf("expected idempotent_replay=true, body=%s", rr.Body.String())
	}
}

func TestStartSubmission_NotEligible_403(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+a.ID+"/submissions", nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("not-eligible start: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("DELIVERY_LEARNER_NOT_IN_COHORT")) {
		t.Fatalf("expected cohort code, body=%s", rr.Body.String())
	}
}

func TestStartSubmission_Missing_404(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/nope/submissions", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing start: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// myResultHandler — RELEASED reveal with media + criterion projection +
// defensive state + non-GET guard
// -----------------------------------------------------------------------------

// asmSeedReleasedRichSubmission builds a RELEASED submission with MCQ + OE
// answers carrying graded artifacts (criterion JSON, comment, overall
// comment) and returns ids for assertion.
func TestMyResult_Released_RevealsImagesCriterionAndComment(t *testing.T) {
	tsStore := httpapi.NewInMemTestSetStore()
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.TestSets = tsStore
		ad.MediaResolver = asmMediaResolver{mu: map[string]string{
			"gs://b/mcq-a.png": "https://signed/mcq-a.png",
			"gs://b/mcq-q.png": "https://signed/mcq-q.png",
			"gs://b/oe-a.png":  "https://signed/oe-a.png",
			"gs://b/oe-q.png":  "https://signed/oe-q.png",
		}}
	})
	ts, err := domain.NewTestSet(domain.NewTestSetInput{TenantID: tenantID, AuthorGCID: instructor, Title: "ts"})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	mcqQ, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-mcq", QuestionID: "qid-mcq",
		QuestionType: "mcq", DisplayOrder: 1, Points: 10,
	})
	if err != nil {
		t.Fatalf("AddQuestion mcq: %v", err)
	}
	mcqQ.PayloadSnapshot = `{
		"stem":"Which?","image_url":"gs://b/mcq-q.png",
		"answer_image_url":"gs://b/mcq-a.png",
		"options":[{"option_id":"opt-a","label":"Earth","is_correct":true,"explainer":"It spins"},
		           {"option_id":"opt-b","label":"Mars","is_correct":false}],
		"scoring":{"mode":"single_correct"}
	}`
	oeQ, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-oe", QuestionID: "qid-oe",
		QuestionType: "oe", DisplayOrder: 2, Points: 20,
	})
	if err != nil {
		t.Fatalf("AddQuestion oe: %v", err)
	}
	oeQ.PayloadSnapshot = `{
		"stem":"Discuss","image_url":"gs://b/oe-q.png","answer_image_url":"gs://b/oe-a.png",
		"prompt":"Explain","model_answer":"Model","rubric":[{"criterion_id":"c1","title":"Clarity","weight":1}]
	}`
	if err := tsStore.Save(context.Background(), ts); err != nil {
		t.Fatalf("save ts: %v", err)
	}
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.TestSetID = ts.ID
		a.State = domain.AssessmentStateOpen
		a.ShuffleMCQOptions = true
		a.ReleaseAnnouncement = "well done all"
	})
	correct := true
	gradedAt := time.Now().Add(-1 * time.Hour)
	sub := asmNewSubmission(t, sRepo, a.ID, []domain.SubmissionAnswer{
		{
			TestSetQuestionID: mcqQ.ID, QuestionID: "qid-mcq",
			QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-a",
			PointsPossible: 10, PointsEarned: 10, MCQCorrect: &correct,
			GradingDispatch: domain.GradingDispatchDeterministic,
		},
		{
			TestSetQuestionID: oeQ.ID, QuestionID: "qid-oe",
			QuestionType: domain.QuestionTypeOE, OEResponseText: "essay",
			PointsPossible: 20, PointsEarned: 15, OEFeedback: "solid",
			OEGradedAt: &gradedAt, OEComment: "Good structure",
			OECriterionJSON:   `[{"criterion_id":"c1","title":"Clarity","score":4,"max_score":5,"feedback":"clear"}]`,
			CommentProvenance: "AI", ScoreProvenance: "HITL", QualityFlagged: false,
		},
	}, func(s *domain.Submission) {
		// MarkReleased only fires from GRADED_PENDING_RELEASE; set the graded +
		// released state directly for the reveal fixture.
		s.State = domain.SubmissionStateReleased
		rel := time.Now()
		s.ReleasedAt = &rel
		s.TotalScore = 25
		passed := true
		s.Passed = &passed
		s.OverallComment = "Overall: strong."
		s.OverallCommentProvenance = "AI"
	})

	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/result", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("released result: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		State  string `json:"state"`
		Result struct {
			TotalPointsEarned float64                  `json:"total_points_earned"`
			Passed            bool                     `json:"passed"`
			Breakdown         map[string]interface{}   `json:"breakdown"`
			PerQuestion       []map[string]interface{} `json:"per_question_grades"`
			OverallComment    string                   `json:"overall_comment"`
			ReleasedAt        interface{}              `json:"released_at"`
			InstructorComment string                   `json:"instructor_comment"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.State != "RELEASED" {
		t.Fatalf("expected RELEASED state, got %q body=%s", body.State, rr.Body.String())
	}
	if body.Result.OverallComment != "Overall: strong." {
		t.Fatalf("expected overall_comment, body=%s", rr.Body.String())
	}
	if body.Result.ReleasedAt == nil {
		t.Fatalf("expected released_at on result")
	}
	if body.Result.InstructorComment != "well done all" {
		t.Fatalf("expected instructor_comment, body=%s", rr.Body.String())
	}
	var mcqEntry, oeEntry map[string]interface{}
	for _, row := range body.Result.PerQuestion {
		switch row["question_type"] {
		case "mcq":
			mcqEntry = row
		case "oe":
			oeEntry = row
		}
	}
	if mcqEntry == nil || oeEntry == nil {
		t.Fatalf("missing per-question rows: %v", body.Result.PerQuestion)
	}
	if mcqEntry["question_image_url"] != "https://signed/mcq-q.png" {
		t.Fatalf("mcq question_image_url: %v", mcqEntry["question_image_url"])
	}
	post := mcqEntry["mcq_post_grade"].(map[string]interface{})
	if post["answer_image_url"] != "https://signed/mcq-a.png" {
		t.Fatalf("mcq answer_image_url: %v", post["answer_image_url"])
	}
	opts := post["options"].([]interface{})
	if len(opts) != 2 {
		t.Fatalf("expected 2 reveal options, got %d", len(opts))
	}
	if oeEntry["question_image_url"] != "https://signed/oe-q.png" {
		t.Fatalf("oe question_image_url: %v", oeEntry["question_image_url"])
	}
	oePost := oeEntry["oe_post_grade"].(map[string]interface{})
	if oePost["answer_image_url"] != "https://signed/oe-a.png" {
		t.Fatalf("oe answer_image_url: %v", oePost["answer_image_url"])
	}
	crit := oePost["criterion_scores"].([]interface{})
	if len(crit) != 1 {
		t.Fatalf("expected criterion_scores, got %v", oePost["criterion_scores"])
	}
	if oePost["comment"] != "Good structure" {
		t.Fatalf("expected oe comment, got %v", oePost["comment"])
	}
	if oePost["model_answer"] != "Model" {
		t.Fatalf("expected model_answer reveal, got %v", oePost["model_answer"])
	}
}

func TestMyResult_DefensiveState_200(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, func(s *domain.Submission) {
		s.State = domain.SubmissionStateArchived
	})
	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/result", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("defensive result: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message == "" {
		t.Fatalf("expected defensive message, body=%s", rr.Body.String())
	}
}

func TestMyResult_NotYourSubmission_403(t *testing.T) {
	srv, aRepo, sRepo, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	sub := asmNewSubmission(t, sRepo, a.ID, nil, func(s *domain.Submission) {
		s.MarkReleased(time.Now())
	})
	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+a.ID+"/submissions/"+sub.ID+"/result", nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign result: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// listAssessmentSubmissionsHandler — 403 / 404 + learner-name resolution
// (dedup + blank-name filter + raw-GCID fallback)
// -----------------------------------------------------------------------------

func TestListSubmissions_NotInstructor_403(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	a := asmNewAssessment(t, aRepo, nil)
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/submissions", nil, gcidB, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("submissions list non-instructor: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestListSubmissions_MissingAssessment_404(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/nope/submissions", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("submissions list missing: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestListSubmissions_ResolvesNames_DedupsAndFallsBack(t *testing.T) {
	dir := directory.NewInMemUserDirectory()
	if err := dir.Upsert(context.Background(), directory.UserDirectoryEntry{
		GCID: learner, DisplayName: "Phyllis L.",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := dir.Upsert(context.Background(), directory.UserDirectoryEntry{
		GCID: gcidB, DisplayName: "   ", // blank name → filtered, raw GCID fallback
	}); err != nil {
		t.Fatalf("Upsert blank: %v", err)
	}
	// gcidC has no directory row at all → raw fallback.
	srv, aRepo, sRepo, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.LearnerDirectory = dir
	})
	a := asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.State = domain.AssessmentStateOpen
	})
	// Two submissions by the SAME learner — dedup must collapse to one lookup.
	asmNewSubmission(t, sRepo, a.ID, nil, nil)
	asmNewSubmission(t, sRepo, a.ID, nil, func(s *domain.Submission) {
		s.AttemptNumber = 2
	})
	// One by a blank-named learner and one by an unknown learner.
	subBlank, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: gcidB,
		AttemptNumber: 1, MaxScore: 100, PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if err := sRepo.Save(context.Background(), subBlank); err != nil {
		t.Fatalf("Save: %v", err)
	}
	subUnknown, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: gcidC,
		AttemptNumber: 1, MaxScore: 100, PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if err := sRepo.Save(context.Background(), subUnknown); err != nil {
		t.Fatalf("Save: %v", err)
	}

	w := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/submissions", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("submissions list: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 4 {
		t.Fatalf("expected 4 submission rows, got %d body=%s", len(body.Items), rr.Body.String())
	}
	byLearner := map[string]string{}
	for _, it := range body.Items {
		lg := it["learner_gcid"].(string)
		if name, ok := it["learner_display_name"].(string); ok {
			byLearner[lg] = name
		}
	}
	if byLearner[learner] != "Phyllis L." {
		t.Fatalf("named learner missing resolved name: %v", byLearner)
	}
	if byLearner[gcidB] != gcidB {
		t.Fatalf("blank-named learner must fall back to raw GCID: %v", byLearner)
	}
	if byLearner[gcidC] != gcidC {
		t.Fatalf("unknown learner must fall back to raw GCID: %v", byLearner)
	}
}

// -----------------------------------------------------------------------------
// Live-listing windows + root method guards + create assessment error paths
// -----------------------------------------------------------------------------

func TestListMyAssessments_AutoFlipClosed_RendersClosed(t *testing.T) {
	srv, aRepo, _, _ := asmServer(t, nil)
	asmNewAssessment(t, aRepo, func(a *domain.Assessment) {
		a.ScheduledCloseAt = time.Now().Add(-1 * time.Hour)
		a.State = domain.AssessmentStateOpen
	})
	w := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments", nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("my list: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(body.Items))
	}
	if state := body.Items[0]["state"]; state != "CLOSED" {
		t.Fatalf("expected CLOSED after lazy flip, got %v", state)
	}
}

func TestMyAssessments_NonGet_405(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments", mustJSON(t, map[string]interface{}{}), learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST my assessments: want 405, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateAssessment_TestSetMissing_400(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/assessments", mustJSON(t, map[string]interface{}{
		"test_set_id": "no-such-test-set",
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("create missing testset: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateAssessment_BadJSON_400(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assessments", bytes.NewReader([]byte("{nope")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantID)
	req.Header.Set("gcid", instructor)
	req.Header.Set("x-mesh-user-roles", "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("create bad json: want 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateAssessment_NoRole_403(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodPost, "/api/v1/assessments", mustJSON(t, map[string]interface{}{
		"test_set_id": "x",
	}), instructor, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("create no role: want 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateAssessment_NonPublishCustomPublisher_DropsEvent(t *testing.T) {
	srv, _, _, _ := asmServer(t, func(ad *httpapi.AssessmentDeps) {
		ad.OutboxPublisher = noCustomPublisher{}
		ad.TestSets = nil // exercise the legacy unscoped create path (251)
	})
	w := reqWithHeaders(http.MethodPost, "/api/v1/assessments", mustJSON(t, map[string]interface{}{
		"test_set_id":    "some-id",
		"title_override": "Dropped events",
	}), instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create with drop-publisher: want 201, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAssessmentsRoot_NonGetPost_405(t *testing.T) {
	srv, _, _, _ := asmServer(t, nil)
	w := reqWithHeaders(http.MethodPut, "/api/v1/assessments", nil, instructor, "instructor")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT root: want 405, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// mustJSON marshals v to JSON bytes (test helper).
func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
