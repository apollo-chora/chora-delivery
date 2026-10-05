// grading_pubsub_handler_test.go — Fix-E push handler RED → GREEN coverage.
package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/eventpush"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	gpTenant   = "11111111-1111-7111-8111-111111111111"
	gpLearner  = "00000000-0000-7000-8000-000000002999"
	gpAssessID = "01985e7f-1234-7abc-8def-000000000a01"
)

func seedGradingSubmission(t *testing.T) (*domain.InMemSubmissionRepo, *domain.Submission) {
	t.Helper()
	repo := domain.NewInMemSubmissionRepo()
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   gpAssessID,
		TenantID:       gpTenant,
		LearnerGCID:    gpLearner,
		AttemptNumber:  1,
		OpensAt:        time.Now().Add(-1 * time.Minute),
		ClosesAt:       time.Now().Add(time.Hour),
		MaxScore:       100,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	mcqCorrect := true
	sub.Answers = []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "mcq-1",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "A",
			MCQCorrect:        &mcqCorrect,
			PointsEarned:      50,
			PointsPossible:    50,
		},
		{
			TestSetQuestionID: "oe-1",
			QuestionType:      domain.QuestionTypeOE,
			OEResponseText:    "essay",
			PointsPossible:    50,
		},
	}
	sub.State = domain.SubmissionStateSubmitted
	sub.MoveToOEPending(time.Now())
	if err := repo.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	return repo, sub
}

func TestGradingInboxPushHandler_AllGraded_200(t *testing.T) {
	repo, sub := seedGradingSubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	gradingSub := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	body := buildGradingPushBody(t,
		gradingEnv("evt-handler-1", gpTenant, gpLearner),
		map[string]any{
			"oe_batch_id":   "batch-1",
			"submission_id": sub.ID,
			"assessment_id": sub.AssessmentID,
			"batch_outcome": "ok",
			"completed_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"graded": []map[string]any{
				{
					"submission_id":          sub.ID,
					"test_set_question_id":   "oe-1",
					"question_id":            "oe-q-1",
					"learner_gcid":           gpLearner,
					"points_earned":          40.0,
					"points_possible":        50,
					"criterion_scores_json":  `{"clarity":4}`,
					"llm_evaluator_feedback": "good",
				},
			},
		})
	rec := dispatchGrading(t, h, "/api/internal/pubsub/grading-inbox", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	// Aggregate advanced + graded event emitted.
	got, ok, _ := repo.Get(context.Background(), gpTenant, sub.ID)
	if !ok {
		t.Fatalf("submission missing post-handle")
	}
	if got.State != domain.SubmissionStateGradedPendingRelease {
		t.Errorf("state: want GRADED_PENDING_RELEASE, got %s", got.State)
	}
	if got.TotalScore != 90 {
		t.Errorf("total: want 90, got %f", got.TotalScore)
	}
	emitted := 0
	for _, e := range pub.History() {
		if e.Topic == "chora.delivery.submission.graded.v1" {
			emitted++
		}
	}
	if emitted != 1 {
		t.Errorf("submission.graded.v1: want 1, got %d", emitted)
	}
}

// TestGradingInboxPushHandler_SubmissionCompleted_RoutesViaEventTopic_200 pins
// the routing fallback for the ADR-172 per-submission completion. The Python
// oe_grading_crew publisher CANNOT emit a Pub/Sub attribute literally named
// "topic" (it collides with google-cloud-pubsub PublisherClient.publish's
// positional arg and raises TypeError, stranding the row), so it carries the
// routing topic under the non-reserved "event_topic" key. Without honoring it,
// topic resolves to "" → defaults to the legacy oe_batch path → the mandatory
// per-submission HITL review gate never fires and the learner is stuck in
// PENDING_OE_GRADING. Regression for the live OE grading e2e.
func TestGradingInboxPushHandler_SubmissionCompleted_RoutesViaEventTopic_200(t *testing.T) {
	repo, sub := seedGradingSubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	gradingSub := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	attrs := map[string]string{
		"event_topic":     subscribers.TopicGradingSubmissionCompleted,
		"event_id":        "evt-subcompleted-1",
		"idempotency_key": "evt-subcompleted-1",
		"tenant_id":       gpTenant,
		"gcid":            gpLearner,
		"occurred_at":     "2026-06-02T12:00:00Z",
		"source_project":  "chora-489812",
		"source_service":  "chora-ai-kernel-orchestrator",
		"schema_version":  "1",
	}
	body := buildGradingPushBody(t, attrs, map[string]any{
		"submission_id":            sub.ID,
		"assessment_id":            sub.AssessmentID,
		"learner_gcid":             gpLearner,
		"outcome":                  "SUCCESS",
		"overall_comment":          "Solid answer covering inputs and outputs.",
		"overall_comment_model_id": "gemini-3.1-pro-preview",
		"completed_at":             time.Now().UTC().Format(time.RFC3339Nano),
		"graded": []map[string]any{
			{
				"test_set_question_id":  "oe-1",
				"question_id":           "oe-q-1",
				"points_earned":         45.0,
				"points_possible":       50,
				"criterion_scores_json": `[{"criterion_id":"c1","title":"Inputs","score":4,"max_score":5}]`,
				"comment":               "Names CO2, water, light; glucose + O2.",
				"grading_model_id":      "gemini-3.1-pro-preview",
				"quality_flagged":       false,
			},
		},
	})
	rec := dispatchGrading(t, h, "/api/internal/pubsub/grading-inbox", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	got, ok, _ := repo.Get(context.Background(), gpTenant, sub.ID)
	if !ok {
		t.Fatalf("submission missing post-handle")
	}
	if got.State != domain.SubmissionStateGradedPendingRelease {
		t.Errorf("state: want GRADED_PENDING_RELEASE, got %s", got.State)
	}
	// Distinguishes the submission_completed route from the oe_batch default:
	// only HandleSubmissionCompleted sets the HITL gate + overall comment.
	if got.ReviewStatus != domain.ReviewStatusPendingReview {
		t.Errorf("review_status: want PENDING_REVIEW, got %q", got.ReviewStatus)
	}
	if got.OverallComment != "Solid answer covering inputs and outputs." {
		t.Errorf("overall_comment not persisted (mis-routed to oe_batch?): %q", got.OverallComment)
	}
}

func TestGradingInboxPushHandler_MisroutedTopic_200(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	gradingSub := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	// Different topic in the attribute set.
	body := buildGradingPushBody(t,
		map[string]string{
			"topic":     "chora.unrelated.thing.happened.v1",
			"event_id":  "evt-other",
			"tenant_id": gpTenant,
			"gcid":      gpLearner,
		},
		map[string]any{"submission_id": "anything"})
	rec := dispatchGrading(t, h, "/", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("misrouted: want 200 (ack-and-drop), got %d", rec.Code)
	}
}

func TestGradingInboxPushHandler_EmptyData_5xx(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	gradingSub := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	body := buildRawPushBody(t, "", gradingEnv("evt-empty", gpTenant, gpLearner), "projects/p/subscriptions/sub")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code < 500 {
		t.Fatalf("empty data: want 5xx, got %d", rec.Code)
	}
}

func TestGradingInboxPushHandler_NilSubscriber_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("want panic on nil subscriber")
		}
	}()
	httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{})
}

func TestGradingInboxPushHandler_MissingTenantInBoth_5xx(t *testing.T) {
	// Subscriber rejects when tenant_id is empty in both payload + envelope.
	repo := domain.NewInMemSubmissionRepo()
	gradingSub := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	// envelope without tenant_id
	env := map[string]string{
		"topic":    subscribers.TopicGradingOEBatchCompleted,
		"event_id": "evt-no-tenant",
	}
	body := buildGradingPushBody(t, env, map[string]any{
		"submission_id": "sub-1",
		"batch_outcome": "ok",
	})
	rec := dispatchGrading(t, h, "/", body)
	if rec.Code < 500 {
		t.Fatalf("missing tenant: want 5xx, got %d", rec.Code)
	}
}

func TestGradingInboxPushHandler_ReplayDedupesAtInbox(t *testing.T) {
	repo, sub := seedGradingSubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	gradingSub := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	body := buildGradingPushBody(t,
		gradingEnv("evt-replay", gpTenant, gpLearner),
		map[string]any{
			"submission_id": sub.ID,
			"batch_outcome": "ok",
			"graded": []map[string]any{
				{"test_set_question_id": "oe-1", "points_earned": 40.0, "points_possible": 50},
			},
		})
	first := dispatchGrading(t, h, "/", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first: %d", first.Code)
	}
	second := dispatchGrading(t, h, "/", body)
	if second.Code != http.StatusOK {
		t.Fatalf("replay: %d", second.Code)
	}
	emitted := 0
	for _, e := range pub.History() {
		if e.Topic == "chora.delivery.submission.graded.v1" {
			emitted++
		}
	}
	if emitted != 1 {
		t.Errorf("dedup: want 1 emit, got %d", emitted)
	}
}

func TestGradingInboxPushHandler_UnauthenticatedWhenVerifierEnabled_401(t *testing.T) {
	// When the Verifier is enabled (audience configured) without a token,
	// the handler should return 401.
	repo := domain.NewInMemSubmissionRepo()
	gradingSub := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier: eventpush.NewVerifier(eventpush.VerifierConfig{
			Audience: "https://example.com/grading-inbox",
		}),
	})
	body := buildGradingPushBody(t, gradingEnv("evt-auth", gpTenant, gpLearner), map[string]any{
		"submission_id": "any",
	})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing token: want 401, got %d", rec.Code)
	}
}

// shouldRetryHandle exercises an injected error from the subscriber via a
// fake repo failure → handler returns 5xx so Pub/Sub retries.
func TestGradingInboxPushHandler_PersistenceFailure_5xx(t *testing.T) {
	repo := &failingSaveSubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo(), failSave: errors.New("rls failure")}
	// Seed via internal repo (bypassing the failure) so Get returns something.
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: gpAssessID, TenantID: gpTenant, LearnerGCID: gpLearner,
		AttemptNumber: 1, OpensAt: time.Now(), ClosesAt: time.Now().Add(time.Hour),
		MaxScore: 100, PassingPercent: 70,
	})
	sub.Answers = []domain.SubmissionAnswer{{
		TestSetQuestionID: "oe-1", QuestionType: domain.QuestionTypeOE,
		OEResponseText: "essay", PointsPossible: 50,
	}}
	sub.State = domain.SubmissionStateSubmitted
	sub.MoveToOEPending(time.Now())
	_ = repo.InMemSubmissionRepo.Save(context.Background(), sub)

	gradingSub := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	h := httpadapter.NewGradingInboxPushHandler(httpadapter.GradingInboxPushDeps{
		Subscriber: gradingSub,
		Verifier:   eventpush.NewVerifier(eventpush.VerifierConfig{}),
	})
	body := buildGradingPushBody(t,
		gradingEnv("evt-persist-fail", gpTenant, gpLearner),
		map[string]any{
			"submission_id": sub.ID,
			"batch_outcome": "ok",
			"graded": []map[string]any{
				{"test_set_question_id": "oe-1", "points_earned": 40.0, "points_possible": 50},
			},
		})
	rec := dispatchGrading(t, h, "/", body)
	if rec.Code < 500 {
		t.Errorf("persistence failure: want 5xx, got %d", rec.Code)
	}
}

// failingSaveSubmissionRepo wraps InMemSubmissionRepo with a configurable
// Save failure for the persistence-fail test.
type failingSaveSubmissionRepo struct {
	*domain.InMemSubmissionRepo
	failSave error
}

func (r *failingSaveSubmissionRepo) Save(ctx context.Context, s *domain.Submission) error {
	if r.failSave != nil {
		return r.failSave
	}
	return r.InMemSubmissionRepo.Save(ctx, s)
}

// -------------------------------------------------------------------------
// helpers
// -------------------------------------------------------------------------

func gradingEnv(eventID, tenantID, gcid string) map[string]string {
	return map[string]string{
		"topic":           subscribers.TopicGradingOEBatchCompleted,
		"event_id":        eventID,
		"idempotency_key": eventID,
		"tenant_id":       tenantID,
		"gcid":            gcid,
		"occurred_at":     "2026-05-16T12:00:00Z",
		"published_at":    "2026-05-16T12:00:00.1Z",
		"traceparent":     "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"source_project":  "chora-489812",
		"source_service":  "chora-ai-kernel-orchestrator",
		"schema_version":  "1",
	}
}

func buildGradingPushBody(t *testing.T, attrs map[string]string, payload map[string]any) string {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return buildRawPushBody(t,
		base64.StdEncoding.EncodeToString(b),
		attrs,
		"projects/chora-489812/subscriptions/chora-delivery-grading-inbox")
}

func buildRawPushBody(t *testing.T, data string, attrs map[string]string, sub string) string {
	t.Helper()
	type msg struct {
		Data        string            `json:"data"`
		MessageID   string            `json:"messageId"`
		PublishTime string            `json:"publishTime"`
		Attributes  map[string]string `json:"attributes,omitempty"`
	}
	type env struct {
		Message      msg    `json:"message"`
		Subscription string `json:"subscription"`
	}
	e := env{
		Message: msg{
			Data:        data,
			MessageID:   fmt.Sprintf("mid-%d", time.Now().UnixNano()),
			PublishTime: "2026-05-16T12:00:00Z",
			Attributes:  attrs,
		},
		Subscription: sub,
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	return string(b)
}

func dispatchGrading(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
