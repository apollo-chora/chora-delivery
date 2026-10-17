// s3_media_ref_contract_test.go — CONTRACT test for the atom-media re-home
// (W8 / OT#4 follow-up).
//
// chora-creation's atom-media bucket moved to an S3-compatible store
// (Garage/MinIO), so durable atom-media references are now minted as
// `s3://<bucket>/<key>` — see chora-creation
// internal/adapter/mediastore.AtomMediaURI + CopyToDurable. The legacy
// `gs://<bucket>/<key>` scheme still appears in snapshots frozen by earlier
// deployments, and chora-creation's ClassifyReference + TenantOwnsRef accept
// BOTH schemes on the durable bucket.
//
// The learner assessment read paths used to collect image refs by filtering
// gs:// only, so an s3:// durable ref was never submitted to
// MintAtomMediaDownloadURL and never became a browser-fetchable URL — the
// learner saw a broken image. These tests start from the durable s3:// ref
// shape creation now emits and prove it reaches the HTTP response as a
// signed GET URL on BOTH learner read paths:
//
//	GET /api/v1/me/assessments/{id}                            (take-time questions)
//	GET /api/v1/me/assessments/{id}/submissions/{subId}/result (reveal)
//
// SCOPE GAP, stated plainly: the signed URL is minted by a FAKE
// httpapi.MediaURLResolver, not by chora-creation's gRPC. A true end-to-end
// test (delivery → creation gRPC → Garage presign) is not runnable in this
// repo's harness — it needs the chora-creation service + an S3-compatible
// endpoint. What IS proven here, end to end at the HTTP seam: an s3:// ref
// frozen in test_set_questions.payload_snapshot is collected, handed to the
// resolver in ONE batched call, and projected into the learner-facing JSON
// as the returned signed URL (never as a raw s3:// URI).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// The durable atom-media bucket name chora-creation now uses — the ref shape
// CopyToDurable returns post-re-home.
const s3AtomMediaBucket = "chora-atom-media-dev"

// s3AtomMediaRef builds the durable s3:// atom-media URI for a tenant object,
// i.e. the exact shape chora-creation's CopyToDurable now emits.
func s3AtomMediaRef(tenantID, objectKey string) string {
	return "s3://" + s3AtomMediaBucket + "/tenants/" + tenantID + "/" + objectKey
}

// s3StubResolver is a contract-level MediaURLResolver: it signs any durable
// ref it is handed with a deterministic https:// prefix, exactly like
// chora-creation's read projections do for s3:// refs. It also records the
// batched ref list so a test can assert the s3:// ref was actually SUBMITTED
// to the mint RPC (the whole point of the fix) — a raw s3:// in the response
// with an empty submittedRefs means the read path never collected it.
type s3StubResolver struct {
	mu            map[string]string
	calls         int
	submittedRefs [][]string
}

func (s *s3StubResolver) ResolveDownloadURLs(_ context.Context, _ string, uris []string) map[string]string {
	s.calls++
	cp := make([]string, len(uris))
	copy(cp, uris)
	s.submittedRefs = append(s.submittedRefs, cp)
	out := map[string]string{}
	for _, u := range uris {
		if signed, ok := s.mu[u]; ok && signed != "" {
			out[u] = signed
		}
	}
	return out
}

// s3TestSetWithMCQ builds + saves a published-shape test set whose single MCQ
// question carries a payload_snapshot with durable s3:// image refs (stem
// image_url + answer_image_url) — the W8 image-gen shape, re-home edition.
func s3TestSetWithMCQ(t *testing.T, store *httpapi.InMemTestSetStore) *domain.TestSet {
	t.Helper()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:   tenantID,
		AuthorGCID: instructor,
		Title:      "S3 atom-media test set",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	q, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-s3-1",
		QuestionID:     "q-s3-1",
		QuestionType:   string(domain.QuestionTypeMCQ),
		DisplayOrder:   1,
		Points:         100,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	q.PayloadSnapshot = `{
		"stem":"Which planet is known as the Red Planet?",
		"image_url":"` + s3AtomMediaRef(tenantID, "mcq/s3-stem.png") + `",
		"answer_image_url":"` + s3AtomMediaRef(tenantID, "mcq/s3-answer.png") + `",
		"options":[
			{"option_id":"opt-1","label":"A","text":"Earth","is_correct":false,"explainer":"No."},
			{"option_id":"opt-2","label":"B","text":"Mars","is_correct":true,"explainer":"Yes."}
		],
		"scoring":{"mode":"single_correct"}
	}`
	if err := store.Save(context.Background(), ts); err != nil {
		t.Fatalf("Save test set: %v", err)
	}
	return ts
}

// s3ReleasedSubmission builds a RELEASED MCQ submission against tsq so the
// result reveal projects the snapshot's images.
func s3ReleasedSubmission(t *testing.T, sRepo *domain.InMemSubmissionRepo, a *domain.Assessment, tsq *domain.TestSetQuestion) *domain.Submission {
	t.Helper()
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if err := sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: tsq.ID, QuestionID: tsq.QuestionID, QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-2"},
	}); err != nil {
		t.Fatalf("MergeAnswers: %v", err)
	}
	if err := sub.MarkSubmitted(time.Now()); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 100
	yes := true
	sub.Answers[0].MCQCorrect = &yes
	sub.Answers[0].GradingDispatch = domain.GradingDispatchDeterministic
	sub.MarkGradedPendingRelease(time.Now())
	sub.MarkReleased(time.Now())
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save submission: %v", err)
	}
	return sub
}

// CONTRACT — learner take path. GET /api/v1/me/assessments/{id} must return the
// frozen s3:// stem ref as a signed GET URL, and that ref must have been
// submitted to the mint RPC in exactly one batched call.
func TestContract_LearnerAssessmentDetail_ResolvesS3AtomMediaRef(t *testing.T) {
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	roster := domain.NewInMemCohortRoster()
	aRepo.SetRosterLink(roster)
	tsStore := httpapi.NewInMemTestSetStore()

	stemRef := s3AtomMediaRef(tenantID, "mcq/s3-stem.png")
	resolver := &s3StubResolver{mu: map[string]string{
		stemRef: "https://s3-signed.example/tenants/" + tenantID + "/mcq/s3-stem.png?sig=abc",
	}}

	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        "ts-s3-contract",
		Title:            "S3 contract assessment",
		ScheduledOpenAt:  time.Now().Add(-1 * time.Minute),
		ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts:      1,
		TotalPoints:      100,
		QuestionCount:    1,
		InvitedGCIDs:     []string{learner},
		GradingConfigSnapshot: domain.GradingConfigSnapshot{
			MCQDispatch:             "DETERMINISTIC",
			PassingThresholdPercent: 70,
		},
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	a.State = domain.AssessmentStateOpen
	ts := s3TestSetWithMCQ(t, tsStore)
	a.TestSetID = ts.ID
	if err := aRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}

	srv := httpapi.NewServer(httpapi.Deps{
		TestSets: tsStore,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments:   aRepo,
			Submissions:   sRepo,
			Roster:        roster,
			TestSets:      tsStore,
			MediaResolver: resolver,
		},
	})

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
	if len(body.Questions) != 1 {
		t.Fatalf("expected 1 question, got %d body=%s", len(body.Questions), rr.Body.String())
	}
	prompt, ok := body.Questions[0]["prompt"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected prompt object, got %v", body.Questions[0])
	}
	got, _ := prompt["image_url"].(string)
	if got != resolver.mu[stemRef] {
		t.Fatalf("prompt.image_url = %q; want the signed GET URL %q (a raw s3:// here means the fix did not land)", got, resolver.mu[stemRef])
	}
	if resolver.calls != 1 {
		t.Fatalf("mint RPC calls = %d; want exactly 1 batched call", resolver.calls)
	}
	if len(resolver.submittedRefs) != 1 || len(resolver.submittedRefs[0]) != 1 || resolver.submittedRefs[0][0] != stemRef {
		t.Fatalf("submitted refs = %v; want [[%s]] — the s3:// ref must be submitted to the mint RPC", resolver.submittedRefs, stemRef)
	}
}

// CONTRACT — learner reveal path. GET
// /api/v1/me/assessments/{id}/submissions/{subId}/result must project BOTH
// frozen s3:// image refs (model-answer illustration + stem illustration) as
// signed GET URLs.
func TestContract_LearnerSubmissionResult_ResolvesS3AtomMediaRefs(t *testing.T) {
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	roster := domain.NewInMemCohortRoster()
	aRepo.SetRosterLink(roster)
	tsStore := httpapi.NewInMemTestSetStore()

	stemRef := s3AtomMediaRef(tenantID, "mcq/s3-stem.png")
	answerRef := s3AtomMediaRef(tenantID, "mcq/s3-answer.png")
	resolver := &s3StubResolver{mu: map[string]string{
		stemRef:   "https://s3-signed.example/stem?sig=1",
		answerRef: "https://s3-signed.example/answer?sig=2",
	}}

	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        "ts-s3-reveal",
		Title:            "S3 reveal contract assessment",
		ScheduledOpenAt:  time.Now().Add(-1 * time.Minute),
		ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts:      1,
		TotalPoints:      100,
		QuestionCount:    1,
		InvitedGCIDs:     []string{learner},
		GradingConfigSnapshot: domain.GradingConfigSnapshot{
			MCQDispatch:             "DETERMINISTIC",
			PassingThresholdPercent: 70,
		},
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	a.State = domain.AssessmentStateOpen
	ts := s3TestSetWithMCQ(t, tsStore)
	a.TestSetID = ts.ID
	if err := aRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}
	sub := s3ReleasedSubmission(t, sRepo, a, ts.Questions()[0])

	srv := httpapi.NewServer(httpapi.Deps{
		TestSets: tsStore,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments:   aRepo,
			Submissions:   sRepo,
			Roster:        roster,
			TestSets:      tsStore,
			MediaResolver: resolver,
		},
	})

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	w := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, w)
	if rr.Code != http.StatusOK {
		t.Fatalf("reveal: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, _ := body["result"].(map[string]interface{})
	if result == nil {
		t.Fatalf("expected result envelope; body=%s", rr.Body.String())
	}
	grades, _ := result["per_question_grades"].([]interface{})
	if len(grades) != 1 {
		t.Fatalf("per_question_grades: want 1, got %d body=%s", len(grades), rr.Body.String())
	}
	g0, _ := grades[0].(map[string]interface{})
	if got := g0["question_image_url"]; got != resolver.mu[stemRef] {
		t.Fatalf("question_image_url = %v; want %v", got, resolver.mu[stemRef])
	}
	postGrade, _ := g0["mcq_post_grade"].(map[string]interface{})
	if postGrade == nil {
		t.Fatalf("expected mcq_post_grade on the reveal row; body=%s", rr.Body.String())
	}
	if got := postGrade["answer_image_url"]; got != resolver.mu[answerRef] {
		t.Fatalf("answer_image_url = %v; want %v", got, resolver.mu[answerRef])
	}
	// Both durable refs must have been submitted to the mint RPC.
	if resolver.calls != 1 {
		t.Fatalf("mint RPC calls = %d; want exactly 1 batched call (stem + answer)", resolver.calls)
	}
	submitted := map[string]bool{}
	for _, r := range resolver.submittedRefs[0] {
		submitted[r] = true
	}
	if !submitted[stemRef] || !submitted[answerRef] {
		t.Fatalf("submitted refs = %v; want both %s and %s", resolver.submittedRefs[0], stemRef, answerRef)
	}
}
