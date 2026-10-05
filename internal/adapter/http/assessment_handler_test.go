// assessment_handler_test.go — domain + handler-level verification of the
// Phyllis E2E walk per ADR-155 §D7-§D9 (cohort scoping + atomic visibility
// flip) + §"Locked architectural rule" (agentic dispatch via Pub/Sub).
//
// The full handler-wired E2E with events.Publisher is validated via the
// smoke walk against the deployed image (see docs/m13/handoff). This file
// covers the domain transitions + cohort eligibility filters that are the
// architectural centrepieces.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

const (
	tenantID   = "11111111-1111-7111-8111-111111111111"
	instructor = "00000000-0000-7000-8000-000000001999"
	learner    = "00000000-0000-7000-8000-000000002999"
	testSetID  = "01985e7f-1234-7abc-8def-000000000a01"
)

// TestAssessmentHandlers_DomainRoundTrip wires the in-mem repos directly +
// asserts the round-trip through NewAssessment + NewSubmission +
// MergeAnswers + MarkSubmitted + ReleaseResults, then verifies the
// DTO + state transitions.
func TestAssessmentHandlers_DomainRoundTrip(t *testing.T) {
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	ctx := context.Background()

	// 1. Instructor creates assessment with Phyllis in invited_gcids.
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		Title:            "Phyllis Practice",
		ScheduledOpenAt:  time.Now().Add(-1 * time.Minute),
		ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts:      1,
		TotalPoints:      100,
		InvitedGCIDs:     []string{learner},
		GradingConfigSnapshot: domain.GradingConfigSnapshot{
			MCQDispatch:             "DETERMINISTIC",
			PassingThresholdPercent: 70,
		},
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	a.State = domain.AssessmentStateOpen // shortcut DRAFT → OPEN for demo
	if err := aRepo.Save(ctx, a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}

	// 2. Phyllis sees the assessment in her list (cohort eligibility).
	visible, _, err := aRepo.ListVisibleToLearner(ctx, tenantID, learner, 20, "")
	if err != nil {
		t.Fatalf("ListVisibleToLearner: %v", err)
	}
	if len(visible) != 1 {
		t.Fatalf("learner-visible count: want 1, got %d", len(visible))
	}

	// 3. Non-invited learner sees NOTHING (ADR-155 §D9 cohort scoping).
	other := "00000000-0000-7000-8000-000000099999"
	visibleOther, _, _ := aRepo.ListVisibleToLearner(ctx, tenantID, other, 20, "")
	if len(visibleOther) != 0 {
		t.Fatalf("other learner should see 0 assessments, got %d", len(visibleOther))
	}

	// 4. Phyllis starts a submission.
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   a.ID,
		TenantID:       tenantID,
		LearnerGCID:    learner,
		AttemptNumber:  1,
		OpensAt:        a.ScheduledOpenAt,
		ClosesAt:       a.ScheduledCloseAt,
		MaxScore:       100,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if err := sRepo.Save(ctx, sub); err != nil {
		t.Fatalf("Save sub: %v", err)
	}

	// 5. Phyllis autosaves an MCQ answer.
	if err := sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "01985e7f-1234-7abc-8def-000000000d01",
			QuestionID:        "01985e7f-1234-7abc-8def-000000000e01",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "01985e7f-1234-7abc-8def-000000000f01",
		},
	}); err != nil {
		t.Fatalf("MergeAnswers: %v", err)
	}
	if sub.State != domain.SubmissionStateInProgress {
		t.Fatalf("after autosave: want IN_PROGRESS, got %s", sub.State)
	}

	// 6. Phyllis submits — MCQ-only fast path (demo simplification §D8)
	if err := sub.MarkSubmitted(time.Now()); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	// Simulate inline MCQ grading.
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 100
	correct := true
	sub.Answers[0].MCQCorrect = &correct
	sub.Answers[0].GradingDispatch = domain.GradingDispatchDeterministic
	sub.MarkGradedPendingRelease(time.Now())
	if sub.State != domain.SubmissionStateGradedPendingRelease {
		t.Fatalf("after grading: want GRADED_PENDING_RELEASE, got %s", sub.State)
	}

	// 7. Result GET pre-release → PENDING_RELEASE envelope per contract.
	if !sub.IsPendingRelease() {
		t.Fatalf("pre-release: want IsPendingRelease()=true")
	}

	// 8. Instructor releases — atomic visibility flip per ADR-155 §D9.
	if err := a.ReleaseResults(time.Now(), "well done"); err != nil {
		t.Fatalf("ReleaseResults: %v", err)
	}
	if a.State != domain.AssessmentStateReleased {
		t.Fatalf("after release: want RELEASED, got %s", a.State)
	}
	if a.ResultsReleasedAt == nil {
		t.Fatalf("results_released_at not set")
	}
	if err := sRepo.Save(ctx, sub); err != nil {
		t.Fatalf("Save sub: %v", err)
	}
	if err := aRepo.Save(ctx, a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}
	released, err := aRepo.ReleaseAllSubmissions(ctx, tenantID, a.ID)
	if err != nil {
		t.Fatalf("ReleaseAllSubmissions: %v", err)
	}
	if len(released) != 1 {
		t.Fatalf("released count: want 1, got %d", len(released))
	}
	// 9. Result GET post-release → RELEASED with score (full SubmissionResult).
	if sub.State != domain.SubmissionStateReleased {
		t.Fatalf("post-release submission: want RELEASED, got %s", sub.State)
	}
	if sub.ReleasedAt == nil {
		t.Fatalf("released_at not set")
	}
	if sub.TotalScore != 100 {
		t.Fatalf("total score: want 100, got %f", sub.TotalScore)
	}
}

// TestAgenticDispatch_OEPath_TransitionsToPendingOEGrading verifies the
// load-bearing architectural rule per ADR-155 §"Locked architectural rule":
// when a submission contains OE answers, the submission state moves to
// PENDING_OE_GRADING + the orchestrator (NOT chora-delivery) owns the
// downstream invocation via the Pub/Sub event flow.
func TestAgenticDispatch_OEPath_TransitionsToPendingOEGrading(t *testing.T) {
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   testSetID,
		TenantID:       tenantID,
		LearnerGCID:    learner,
		AttemptNumber:  1,
		ClosesAt:       time.Now().Add(2 * time.Hour),
		MaxScore:       100,
		PassingPercent: 70,
	})
	sub.Answers = []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "q-oe-1",
			QuestionID:        "qid-oe-1",
			QuestionType:      domain.QuestionTypeOE,
			OEResponseText:    "My answer is...",
		},
	}
	_ = sub.MarkSubmitted(time.Now())
	if !sub.HasOEAnswers() {
		t.Fatalf("expected HasOEAnswers=true")
	}
	sub.MoveToOEPending(time.Now())
	if sub.State != domain.SubmissionStatePendingOEGrading {
		t.Fatalf("after MoveToOEPending: want PENDING_OE_GRADING, got %s", sub.State)
	}
}

// -----------------------------------------------------------------------------
// Handler-level fixtures + helpers (M13 BE batch — E2E-BE-2/4/30/31 +
// LEG4-A + LEG5-A/B/C).
// -----------------------------------------------------------------------------

// newAssessmentTestServer wires the AssessmentDeps onto NewServer with in-mem
// repos + the in-memory Publisher so PublishCustom-based emit paths can be
// asserted.
func newAssessmentTestServer(t *testing.T) (http.Handler, *domain.InMemAssessmentRepo, *domain.InMemSubmissionRepo, *httpapi.InMemTestSetStore, *events.InMemoryPublisher) {
	t.Helper()
	srv, aRepo, sRepo, tsStore, pub, _ := newAssessmentTestServerWithRoster(t)
	return srv, aRepo, sRepo, tsStore, pub
}

// newAssessmentTestServerWithRoster is newAssessmentTestServer plus a handle on
// the cohort roster (CHO-2153), so a test can enrol a learner onto an offering
// or book them onto a class. The roster starts EMPTY — nobody is enrolled and
// nobody is booked — which is the correct default for an authz gate: a test
// that forgets to enrol its learner gets a denial, not a free pass.
func newAssessmentTestServerWithRoster(t *testing.T) (http.Handler, *domain.InMemAssessmentRepo, *domain.InMemSubmissionRepo, *httpapi.InMemTestSetStore, *events.InMemoryPublisher, *domain.InMemCohortRoster) {
	t.Helper()
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	roster := domain.NewInMemCohortRoster()
	aRepo.SetRosterLink(roster)
	tsStore := httpapi.NewInMemTestSetStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		TestSets:       tsStore,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments:     aRepo,
			Submissions:     sRepo,
			OutboxPublisher: pub,
			TestSets:        tsStore,
			Roster:          roster,
		},
	})
	return srv, aRepo, sRepo, tsStore, pub, roster
}

// reqHeaders attaches the standard auth headers (instructor role unless caller
// overrides via the role string).
func reqWithHeaders(method, path string, body []byte, gcid, role string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("X-Tenant-Id", tenantID)
	r.Header.Set("gcid", gcid)
	if role != "" {
		r.Header.Set("x-mesh-user-roles", role)
	}
	return r
}

// seedOpenAssessmentWithCohort creates an OPEN assessment with the learner on
// the invited cohort. Returns the assessment so the caller can drive
// downstream submission flows.
func seedOpenAssessmentWithCohort(t *testing.T, aRepo *domain.InMemAssessmentRepo) *domain.Assessment {
	t.Helper()
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		Title:            "Phyllis Practice",
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
	if err := aRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save assessment: %v", err)
	}
	return a
}

// -----------------------------------------------------------------------------
// E2E-BE-2 — GET /api/v1/assessments instructor list (cursor paginated)
// -----------------------------------------------------------------------------

// TestListAssessments_Instructor_ReturnsAuthoredItems mirrors handleListTestSets:
// authoring role required, `items[]` + `next_page_token` envelope, scoped to
// caller's authored assessments via ListByInstructor.
func TestListAssessments_Instructor_ReturnsAuthoredItems(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments?page_size=20", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items         []map[string]any `json:"items"`
		NextPageToken any              `json:"next_page_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items count: want 1, got %d body=%s", len(resp.Items), w.Body.String())
	}
	if resp.Items[0]["assessment_id"] != a.ID {
		t.Fatalf("assessment_id: want %s, got %v", a.ID, resp.Items[0]["assessment_id"])
	}
	if _, ok := resp.Items[0]["state"]; !ok {
		t.Fatalf("expected `state` on instructor list item")
	}
}

// TestListAssessments_Instructor_RoleRequired returns 403 when caller lacks
// the instructor/training-admin role.
func TestListAssessments_Instructor_RoleRequired(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	_ = seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments", nil, instructor, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestListAssessments_Instructor_PageSizeClamp coerces invalid page_size to
// the contract default (20). Mirrors parseTestSetPageSize behaviour.
func TestListAssessments_Instructor_PageSizeClamp(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	_ = seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments?page_size=9999", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestListAssessmentSubmissions_Instructor_IncludesReviewStatus pins the R+
// grading-queue contract: the instructor submissions list MUST carry
// review_status per row. The FE queue renders the REVIEW STATUS pill from it
// AND gates "Release results" on the count of APPROVED rows — without it every
// row reads "Pending review" and the per-step release stays disabled forever
// (the learner's graded OE result can never be released via the 2-step UX).
// Regression for the live OE grading e2e (R+ queue showed 0/1 approved after a
// successful approve). Instructor-gated handler → no learner HITL-status leak.
func TestListAssessmentSubmissions_Instructor_IncludesReviewStatus(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   a.ID,
		TenantID:       tenantID,
		LearnerGCID:    learner,
		AttemptNumber:  1,
		OpensAt:        time.Now().Add(-time.Minute),
		ClosesAt:       time.Now().Add(time.Hour),
		MaxScore:       100,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	sub.State = domain.SubmissionStateGradedPendingRelease
	sub.ReviewStatus = domain.ReviewStatusPendingReview
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save submission: %v", err)
	}

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/submissions", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items count: want 1, got %d body=%s", len(resp.Items), w.Body.String())
	}
	if resp.Items[0]["review_status"] != "PENDING_REVIEW" {
		t.Fatalf("review_status: want PENDING_REVIEW, got %v (R+ grading queue can't gate release without it)", resp.Items[0]["review_status"])
	}
}

// TestListAssessmentSubmissions_Instructor_ReviewStatusNotRequiredFaithful pins
// the CHO-2343 bug #2 fix: the submissions list MUST faithfully mirror the
// domain review_status INCLUDING the empty "" NotRequired value (MCQ-only
// auto-graded submissions, ADR-172 §D6). Previously the handler OMITTED the ""
// value, so the FE could not distinguish NotRequired from PENDING_REVIEW and
// rendered MCQ-only rows as "Pending review". The key MUST be PRESENT and empty
// (not absent) so the queue can classify the row without a per-row /grading fetch.
func TestListAssessmentSubmissions_Instructor_ReviewStatusNotRequiredFaithful(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   a.ID,
		TenantID:       tenantID,
		LearnerGCID:    learner,
		AttemptNumber:  1,
		OpensAt:        time.Now().Add(-time.Minute),
		ClosesAt:       time.Now().Add(time.Hour),
		MaxScore:       100,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	// MCQ-only auto-graded + released: NO human gate → ReviewStatusNotRequired ("").
	sub.State = domain.SubmissionStateReleased
	sub.ReviewStatus = domain.ReviewStatusNotRequired
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save submission: %v", err)
	}

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/submissions", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items count: want 1, got %d body=%s", len(resp.Items), w.Body.String())
	}
	val, present := resp.Items[0]["review_status"]
	if !present {
		t.Fatalf("review_status MUST be present on the list DTO (faithful NotRequired mirror), got absent; item=%v", resp.Items[0])
	}
	if val != "" {
		t.Fatalf("review_status: want empty \"\" (NotRequired), got %v (MCQ-only must not render as PENDING_REVIEW)", val)
	}
}

// newAssessmentTestServerWithDirectory is newAssessmentTestServer wired with a
// seeded user_directory (CHO-2343) so the instructor submissions list resolves
// a learner GCID → display name via the chora_delivery.user_directory
// projection (LookupNames) — the SAME read-model CHO-2335 uses for WBL. Returns
// the server + assessment/submission repos.
func newAssessmentTestServerWithDirectory(t *testing.T, dir *directory.InMemUserDirectory) (http.Handler, *domain.InMemAssessmentRepo, *domain.InMemSubmissionRepo) {
	t.Helper()
	aRepo := domain.NewInMemAssessmentRepo()
	sRepo := domain.NewInMemSubmissionRepo()
	aRepo.SetSubmissionLink(sRepo)
	roster := domain.NewInMemCohortRoster()
	aRepo.SetRosterLink(roster)
	tsStore := httpapi.NewInMemTestSetStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		TestSets:       tsStore,
		AssessmentDeps: &httpapi.AssessmentDeps{
			Assessments:      aRepo,
			Submissions:      sRepo,
			OutboxPublisher:  pub,
			TestSets:         tsStore,
			Roster:           roster,
			LearnerDirectory: dir,
		},
	})
	return srv, aRepo, sRepo
}

// TestListAssessmentSubmissions_Instructor_ResolvesLearnerName pins the CHO-2343
// grading-queue fix: the instructor submissions list MUST carry
// learner_display_name resolved from the chora_delivery.user_directory
// projection (the same GCID→name read-model CHO-2335 uses for WBL), and MUST
// fall back to the raw learner GCID (never blank) when a learner has no
// directory row. Regression for the live R+ bug where the LEARNER column
// rendered a raw GCID (019eded8-…) instead of a human name. The FE reads
// learner_display_name ?? learner_gcid, so this BE field is the missing piece.
func TestListAssessmentSubmissions_Instructor_ResolvesLearnerName(t *testing.T) {
	dir := directory.NewInMemUserDirectory()
	if err := dir.Upsert(context.Background(), directory.UserDirectoryEntry{
		GCID:        learner,
		DisplayName: "Dale (user #1)",
		UpdatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("seed directory: %v", err)
	}
	srv, aRepo, sRepo := newAssessmentTestServerWithDirectory(t, dir)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	// A submission whose learner IS in the directory (name resolves) + one whose
	// learner is NOT (GCID fallback). Distinct GCIDs so we can index by learner.
	unknownLearner := "00000000-0000-7000-8000-0000000042ab"
	for _, g := range []string{learner, unknownLearner} {
		sub, err := domain.NewSubmission(domain.NewSubmissionInput{
			AssessmentID:   a.ID,
			TenantID:       tenantID,
			LearnerGCID:    g,
			AttemptNumber:  1,
			OpensAt:        time.Now().Add(-time.Minute),
			ClosesAt:       time.Now().Add(time.Hour),
			MaxScore:       100,
			PassingPercent: 70,
		})
		if err != nil {
			t.Fatalf("NewSubmission(%s): %v", g, err)
		}
		if err := sRepo.Save(context.Background(), sub); err != nil {
			t.Fatalf("Save submission(%s): %v", g, err)
		}
	}

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/submissions", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v body=%s", err, w.Body.String())
	}
	byLearner := map[string]map[string]any{}
	for _, it := range resp.Items {
		g, _ := it["learner_gcid"].(string)
		byLearner[g] = it
	}
	resolved, ok := byLearner[learner]
	if !ok {
		t.Fatalf("resolved learner row missing; items=%v", resp.Items)
	}
	if resolved["learner_display_name"] != "Dale (user #1)" {
		t.Fatalf("learner_display_name (resolved): want %q, got %v (raw GCID still leaking to the R+ grading queue LEARNER column)", "Dale (user #1)", resolved["learner_display_name"])
	}
	unknown, ok := byLearner[unknownLearner]
	if !ok {
		t.Fatalf("unknown learner row missing; items=%v", resp.Items)
	}
	if unknown["learner_display_name"] != unknownLearner {
		t.Fatalf("learner_display_name (unresolved): want GCID fallback %q (never blank), got %v", unknownLearner, unknown["learner_display_name"])
	}
}

// -----------------------------------------------------------------------------
// E2E-BE-31 — GET /api/v1/me/assessments/{id}/submissions/{subId} learner
// rehydrate handler. Currently 404; closing missing handler.
// -----------------------------------------------------------------------------

// TestGetMySubmission_Learner_ReturnsRehydratedSubmission verifies the no-
// suffix submission GET returns the learner's submission with answers[] +
// last_saved_at + time_remaining_seconds for idempotent-replay rehydrate.
func TestGetMySubmission_Learner_ReturnsRehydratedSubmission(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   a.ID,
		TenantID:       tenantID,
		LearnerGCID:    learner,
		AttemptNumber:  1,
		OpensAt:        a.ScheduledOpenAt,
		ClosesAt:       a.ScheduledCloseAt,
		MaxScore:       100,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-1",
			QuestionID:        "q-1",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "opt-1",
		},
	})
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save sub: %v", err)
	}

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, w.Body.String())
	}
	if body["submission_id"] != sub.ID {
		t.Fatalf("submission_id: want %s, got %v", sub.ID, body["submission_id"])
	}
	if _, ok := body["last_saved_at"]; !ok {
		t.Fatalf("expected `last_saved_at` on rehydrate body")
	}
	answers, _ := body["answers"].([]any)
	if len(answers) != 1 {
		t.Fatalf("answers count: want 1, got %d body=%s", len(answers), w.Body.String())
	}
}

// TestGetMySubmission_Learner_NotYours returns 403 for a sub the caller does
// not own.
func TestGetMySubmission_Learner_NotYours(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sRepo.Save(context.Background(), sub)

	other := "00000000-0000-7000-8000-000000099999"
	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID
	r := reqWithHeaders(http.MethodGet, path, nil, other, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// E2E-BE-30 — autosave route drift: PATCH no-suffix path is canonical per
// OpenAPI. The /autosave suffix is REMOVED — FE conforms to BE per user
// 2026-05-15 directive, and FE has migrated to no-suffix path.
// -----------------------------------------------------------------------------

// TestAutosaveMySubmission_NoSuffix_IsCanonical PATCHes the no-suffix path
// directly + expects 200.
func TestAutosaveMySubmission_NoSuffix_IsCanonical(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sRepo.Save(context.Background(), sub)

	body := []byte(`{"answers":[{"test_set_question_id":"tsq-1","question_id":"q-1","question_type":"mcq","mcq_choice_id":"opt-1"}]}`)
	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID
	r := reqWithHeaders(http.MethodPatch, path, body, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("autosave no-suffix: want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// E2E-BE-4 + LEG5-A/B/C — getMySubmissionResult response shape extensions:
//   - LEG5-A: oe_batch_pending field surfaced when OE awaits batch grading
//   - LEG5-B: mcq_post_grade.options[] (is_correct + explainer) for reveal
//   - LEG5-C: IN_PROGRESS submissions return 409 (FE redirects to
//             /submissions/{id} rehydrate)
//   - E2E-BE-4: per-question correctness + breakdown surfaced
// -----------------------------------------------------------------------------

// TestGetMySubmissionResult_InProgress_Returns409 — LEG5-C. IN_PROGRESS subs
// must NOT be conflated with PENDING_RELEASE. FE redirects to rehydrate.
func TestGetMySubmissionResult_InProgress_Returns409(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: "tsq-1", QuestionID: "q-1", QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-1"},
	})
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("LEG5-C: IN_PROGRESS result: want 409, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "DELIVERY_SUBMISSION_IN_PROGRESS" {
		t.Fatalf("expected error.code=DELIVERY_SUBMISSION_IN_PROGRESS, got body=%s", w.Body.String())
	}
}

// TestGetMySubmissionResult_PendingRelease_NoOE returns the PENDING_RELEASE
// envelope when MCQ-only submission was inline-graded + awaiting release.
// oe_batch_pending must be false (no OE present).
func TestGetMySubmissionResult_PendingRelease_NoOE(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: "tsq-1", QuestionID: "q-1", QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-1"},
	})
	_ = sub.MarkSubmitted(time.Now())
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 100
	yes := true
	sub.Answers[0].MCQCorrect = &yes
	sub.Answers[0].GradingDispatch = domain.GradingDispatchDeterministic
	sub.MarkGradedPendingRelease(time.Now())
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("pending release: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["state"] != "PENDING_RELEASE" {
		t.Fatalf("state: want PENDING_RELEASE, got %v", body["state"])
	}
	if v, ok := body["oe_batch_pending"]; !ok {
		t.Fatalf("LEG5-A: expected oe_batch_pending field present (body=%s)", w.Body.String())
	} else if v != false {
		t.Fatalf("LEG5-A: oe_batch_pending: want false (no OE), got %v", v)
	}
}

// TestGetMySubmissionResult_PendingOE_FlagsOEBatchPending — LEG5-A. When the
// submission has an OE answer awaiting batch grading, oe_batch_pending=true.
func TestGetMySubmissionResult_PendingOE_FlagsOEBatchPending(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: "tsq-1", QuestionID: "q-1", QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-1"},
		{TestSetQuestionID: "tsq-2", QuestionID: "q-2", QuestionType: domain.QuestionTypeOE, OEResponseText: "My answer..."},
	})
	_ = sub.MarkSubmitted(time.Now())
	// MCQ graded inline.
	sub.Answers[0].PointsPossible = 50
	sub.Answers[0].PointsEarned = 50
	yes := true
	sub.Answers[0].MCQCorrect = &yes
	sub.Answers[0].GradingDispatch = domain.GradingDispatchDeterministic
	// OE: still pending (no PointsEarned, GradingDispatch=LLM_EVALUATOR
	// inferred by handler from blank OEFeedback).
	sub.Answers[1].PointsPossible = 50
	sub.Answers[1].GradingDispatch = domain.GradingDispatchLLMEvaluator
	sub.MoveToOEPending(time.Now())
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("pending OE: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if v, _ := body["oe_batch_pending"].(bool); !v {
		t.Fatalf("LEG5-A: oe_batch_pending: want true (OE pending), got body=%s", w.Body.String())
	}
}

// TestGetMySubmissionResult_Released_BreakdownAndMCQPostGrade — E2E-BE-4 +
// LEG5-B. Released submissions carry per-question correctness + breakdown
// (mcq_correct_count, mcq_total, oe_pending_count, oe_total) + the
// mcq_post_grade.options[] reveal payload sourced from the test_set_question
// PayloadSnapshot.
func TestGetMySubmissionResult_Released_BreakdownAndMCQPostGrade(t *testing.T) {
	srv, aRepo, sRepo, tsStore, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	// Seed the test_set + question + payload_snapshot so LEG5-B can resolve
	// mcq_post_grade.options[] via the TestSets port at result time.
	ts := seedPublishedTestSetWithMCQ(t, tsStore, a.TestSetID)

	// Inject an MCQ-only released submission keyed to the seeded test-set
	// question_id so the result handler can resolve the snapshot.
	tsq := ts.Questions()[0]
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: tsq.ID, QuestionID: tsq.QuestionID, QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-1"},
	})
	_ = sub.MarkSubmitted(time.Now())
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 100
	yes := true
	sub.Answers[0].MCQCorrect = &yes
	sub.Answers[0].GradingDispatch = domain.GradingDispatchDeterministic
	sub.MarkGradedPendingRelease(time.Now())
	sub.MarkReleased(time.Now())
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("released result: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["state"] != "RELEASED" {
		t.Fatalf("state: want RELEASED, got %v", body["state"])
	}
	result, _ := body["result"].(map[string]any)
	if result == nil {
		t.Fatalf("expected result envelope; body=%s", w.Body.String())
	}
	bk, _ := result["breakdown"].(map[string]any)
	if bk == nil {
		t.Fatalf("E2E-BE-4: expected `breakdown` summary; body=%s", w.Body.String())
	}
	if fmt.Sprintf("%v", bk["mcq_correct_count"]) != "1" || fmt.Sprintf("%v", bk["mcq_total"]) != "1" {
		t.Fatalf("E2E-BE-4: breakdown mcq counts mismatch — body=%s", w.Body.String())
	}
	// LEG5-B mcq_post_grade.options[]
	grades, _ := result["per_question_grades"].([]any)
	if len(grades) != 1 {
		t.Fatalf("per_question_grades count: want 1, got %d", len(grades))
	}
	g0, _ := grades[0].(map[string]any)
	postGrade, _ := g0["mcq_post_grade"].(map[string]any)
	if postGrade == nil {
		t.Fatalf("LEG5-B: expected mcq_post_grade on grade row — body=%s", w.Body.String())
	}
	options, _ := postGrade["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("LEG5-B: mcq_post_grade.options count: want 2, got %d", len(options))
	}
}

// E2E-BE-RESULT-LEARNER-ANSWER (P1 smoke #3 2026-05-17) — released
// per_question_grades[i] MUST carry `learner_answer` echo so the FE result
// template can render "YOUR ANSWER: …" (not "No answer"). Reference shape:
// chora-contracts/openapi/delivery-assessments.yaml §LearnerAnswerEcho:
//
//	learner_answer: {
//	  mcq_choice_id:    string|null   (single-correct MCQ)
//	  mcq_choice_ids:   []string|null (multi-correct MCQ)
//	  oe_response_text: string|null   (OE)
//	}
func TestGetMySubmissionResult_Released_MCQ_LearnerAnswer_Echoed(t *testing.T) {
	srv, aRepo, sRepo, tsStore, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	ts := seedPublishedTestSetWithMCQ(t, tsStore, a.TestSetID)
	tsq := ts.Questions()[0]
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: tsq.ID, QuestionID: tsq.QuestionID, QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-saturn"},
	})
	_ = sub.MarkSubmitted(time.Now())
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 100
	yes := true
	sub.Answers[0].MCQCorrect = &yes
	sub.Answers[0].GradingDispatch = domain.GradingDispatchDeterministic
	sub.MarkGradedPendingRelease(time.Now())
	sub.MarkReleased(time.Now())
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("released result: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	result, _ := body["result"].(map[string]any)
	grades, _ := result["per_question_grades"].([]any)
	if len(grades) != 1 {
		t.Fatalf("per_question_grades count: want 1, got %d body=%s", len(grades), w.Body.String())
	}
	g0, _ := grades[0].(map[string]any)
	la, _ := g0["learner_answer"].(map[string]any)
	if la == nil {
		t.Fatalf("E2E-BE-RESULT-LEARNER-ANSWER: expected learner_answer on grade row — body=%s", w.Body.String())
	}
	if got := la["mcq_choice_id"]; got != "opt-saturn" {
		t.Errorf("learner_answer.mcq_choice_id: want opt-saturn, got %v (FE would render 'No answer')", got)
	}
}

// OE submission released: learner_answer.oe_response_text echoed.
func TestGetMySubmissionResult_Released_OE_LearnerAnswer_Echoed(t *testing.T) {
	srv, aRepo, sRepo, tsStore, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	ts := seedPublishedTestSetWithMCQ(t, tsStore, a.TestSetID)
	tsq := ts.Questions()[0]
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	oeText := "Chlorophyll converts photons to electrons that drive ATP synthesis."
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: tsq.ID, QuestionID: tsq.QuestionID, QuestionType: domain.QuestionTypeOE, OEResponseText: oeText},
	})
	_ = sub.MarkSubmitted(time.Now())
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 80
	sub.Answers[0].GradingDispatch = domain.GradingDispatchLLMEvaluator
	sub.Answers[0].OEFeedback = "Good answer."
	now := time.Now()
	sub.Answers[0].OEGradedAt = &now
	sub.MarkGradedPendingRelease(time.Now())
	sub.MarkReleased(time.Now())
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("released OE result: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	result, _ := body["result"].(map[string]any)
	grades, _ := result["per_question_grades"].([]any)
	if len(grades) != 1 {
		t.Fatalf("per_question_grades count: want 1, got %d", len(grades))
	}
	g0, _ := grades[0].(map[string]any)
	la, _ := g0["learner_answer"].(map[string]any)
	if la == nil {
		t.Fatalf("learner_answer missing on OE grade row body=%s", w.Body.String())
	}
	if got := la["oe_response_text"]; got != oeText {
		t.Errorf("learner_answer.oe_response_text: want %q, got %v", oeText, got)
	}
}

// Multi-correct MCQ: learner_answer.mcq_choice_ids carries the array.
func TestGetMySubmissionResult_Released_MCQMulti_LearnerAnswer_Echoed(t *testing.T) {
	srv, aRepo, sRepo, tsStore, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	ts := seedPublishedTestSetWithMCQ(t, tsStore, a.TestSetID)
	tsq := ts.Questions()[0]
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: tsq.ID, QuestionID: tsq.QuestionID, QuestionType: domain.QuestionTypeMCQ, MCQChoiceIDs: []string{"opt-a", "opt-b"}},
	})
	_ = sub.MarkSubmitted(time.Now())
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 100
	yes := true
	sub.Answers[0].MCQCorrect = &yes
	sub.Answers[0].GradingDispatch = domain.GradingDispatchDeterministic
	sub.MarkGradedPendingRelease(time.Now())
	sub.MarkReleased(time.Now())
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	result, _ := body["result"].(map[string]any)
	grades, _ := result["per_question_grades"].([]any)
	g0, _ := grades[0].(map[string]any)
	la, _ := g0["learner_answer"].(map[string]any)
	if la == nil {
		t.Fatalf("learner_answer missing body=%s", w.Body.String())
	}
	ids, _ := la["mcq_choice_ids"].([]any)
	if len(ids) != 2 || ids[0] != "opt-a" || ids[1] != "opt-b" {
		t.Errorf("learner_answer.mcq_choice_ids: want [opt-a, opt-b], got %v", ids)
	}
}

// -----------------------------------------------------------------------------
// ATOM-2 — AUTHOR-SAFE snapshot store + projection split (B3 lane).
//
// After B6 ships AUTHOR-SAFE SnapshotQuestionByID from chora-creation, the
// payload_snapshot column stores the FULL author shape (is_correct +
// explainer + model_answer + rubric). The result handler MUST project that
// to the wire-level reveal payload on RELEASED submissions:
//
//   - MCQ:  mcq_post_grade.options[].{is_correct, explainer}
//           (already wired via LEG5-B; tested at
//            TestGetMySubmissionResult_Released_BreakdownAndMCQPostGrade)
//   - OE:   oe_post_grade.{model_answer, rubric}
//           (NEW — this test gates the wiring)
//
// Per chora-contracts/openapi/delivery-assessments.yaml §LearnerQuestionGrade.
// -----------------------------------------------------------------------------

// TestGetMySubmissionResult_Released_OE_ExposesModelAnswerAndRubric — ATOM-2.
// Released OE submissions MUST surface oe_post_grade.{model_answer, rubric}
// sourced from the test_set_question PayloadSnapshot.
func TestGetMySubmissionResult_Released_OE_ExposesModelAnswerAndRubric(t *testing.T) {
	srv, aRepo, sRepo, tsStore, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	// Seed test_set with AUTHOR-SAFE OE snapshot.
	ts := seedPublishedTestSetWithOE(t, tsStore, a.TestSetID)
	tsq := ts.Questions()[0]

	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	oeText := "Plants split water; the oxygen is a byproduct."
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: tsq.ID, QuestionID: tsq.QuestionID, QuestionType: domain.QuestionTypeOE, OEResponseText: oeText},
	})
	_ = sub.MarkSubmitted(time.Now())
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 80
	sub.Answers[0].GradingDispatch = domain.GradingDispatchLLMEvaluator
	sub.Answers[0].OEFeedback = "Mostly correct; missing the NADPH chain."
	now := time.Now()
	sub.Answers[0].OEGradedAt = &now
	sub.MarkGradedPendingRelease(time.Now())
	sub.MarkReleased(time.Now())
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("released OE result: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	result, _ := body["result"].(map[string]any)
	grades, _ := result["per_question_grades"].([]any)
	if len(grades) != 1 {
		t.Fatalf("per_question_grades count: want 1, got %d body=%s", len(grades), w.Body.String())
	}
	g0, _ := grades[0].(map[string]any)
	postGrade, _ := g0["oe_post_grade"].(map[string]any)
	if postGrade == nil {
		t.Fatalf("ATOM-2: expected oe_post_grade on OE grade row; body=%s", w.Body.String())
	}
	if got, _ := postGrade["model_answer"].(string); got == "" {
		t.Errorf("ATOM-2: oe_post_grade.model_answer MUST be present (post-RELEASE reveal); got %v", postGrade["model_answer"])
	}
	rub, _ := postGrade["rubric"].([]any)
	if len(rub) != 2 {
		t.Fatalf("ATOM-2: oe_post_grade.rubric count: want 2, got %d (full=%v)", len(rub), postGrade)
	}
	c0, _ := rub[0].(map[string]any)
	if got := c0["criterion_id"]; got != "clarity" {
		t.Errorf("ATOM-2: rubric[0].criterion_id: want clarity, got %v", got)
	}
	// LLM evaluator feedback also lands on the grade row.
	if got := g0["llm_evaluator_feedback"]; got == nil {
		t.Errorf("ATOM-2: expected llm_evaluator_feedback on OE grade row; got nil")
	}
}

// TestGetMySubmissionResult_PreRelease_OE_StripsModelAnswerAndRubric — guard
// path: pre-RELEASE result endpoints MUST NOT expose model_answer / rubric.
// The /me/assessments/{id}/submissions/{id} working-canvas endpoint is the
// pre-RELEASE read path; tested separately via the learner-detail tests.
// Here we lock in the post-RELEASE-only condition for the result endpoint:
// pre-RELEASE result calls return the PENDING_RELEASE envelope WITHOUT
// surfacing the snapshot AT ALL.
func TestGetMySubmissionResult_PreRelease_OE_DoesNotLeakAuthorSafe(t *testing.T) {
	srv, aRepo, sRepo, tsStore, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	ts := seedPublishedTestSetWithOE(t, tsStore, a.TestSetID)
	tsq := ts.Questions()[0]

	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: tsq.ID, QuestionID: tsq.QuestionID, QuestionType: domain.QuestionTypeOE, OEResponseText: "answer"},
	})
	_ = sub.MarkSubmitted(time.Now())
	// MarkSubmitted on OE-only submission transitions to PENDING_OE_GRADING
	// by virtue of HasOEAnswers — but only after MoveToOEPending is called.
	// For pre-RELEASE we want GRADED_PENDING_RELEASE state — i.e., the
	// submission is graded but the instructor hasn't released yet.
	sub.Answers[0].PointsPossible = 100
	sub.Answers[0].PointsEarned = 80
	sub.Answers[0].GradingDispatch = domain.GradingDispatchLLMEvaluator
	sub.Answers[0].OEFeedback = "ok"
	now := time.Now()
	sub.Answers[0].OEGradedAt = &now
	sub.MarkGradedPendingRelease(time.Now())
	// Note: NOT releasing — staying in GRADED_PENDING_RELEASE.
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/result"
	r := reqWithHeaders(http.MethodGet, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-release OE result: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	// Body MUST be the PENDING_RELEASE envelope — no model_answer / rubric.
	rendered := w.Body.String()
	for _, forbid := range []string{"model_answer", "rubric", "Plants split water"} {
		if containsSubstring(rendered, forbid) {
			t.Errorf("ATOM-2 pre-RELEASE LEAK: result endpoint exposed %q in PENDING_RELEASE envelope; body=%s", forbid, rendered)
		}
	}
}

// containsSubstring is a no-import test helper for substring checks.
func containsSubstring(haystack, needle string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// LEG4-A — chora.delivery.grading.mcq_completed.v1 publisher
// -----------------------------------------------------------------------------

// TestSubmit_PublishesMCQCompletedMarker — after successful inline MCQ
// grading on /submit, the topic chora.delivery.grading.mcq_completed.v1 is
// emitted with mcq_correct_count + mcq_incorrect_count + mcq_points_earned +
// mcq_points_possible + no_oe_pending. Per ADR-155 §line 70 IMDA D1 marker.
func TestSubmit_PublishesMCQCompletedMarker(t *testing.T) {
	srv, aRepo, sRepo, _, pub := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	sub, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: a.ID, TenantID: tenantID, LearnerGCID: learner,
		AttemptNumber: 1, OpensAt: a.ScheduledOpenAt, ClosesAt: a.ScheduledCloseAt,
		MaxScore: 100, PassingPercent: 70,
	})
	_ = sub.MergeAnswers(time.Now(), []domain.SubmissionAnswer{
		{TestSetQuestionID: "tsq-1", QuestionID: "q-1", QuestionType: domain.QuestionTypeMCQ, MCQChoiceID: "opt-1"},
	})
	_ = sRepo.Save(context.Background(), sub)

	path := "/api/v1/me/assessments/" + a.ID + "/submissions/" + sub.ID + "/submit"
	r := reqWithHeaders(http.MethodPost, path, nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit: want 202, got %d body=%s", w.Code, w.Body.String())
	}

	found := false
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.grading.mcq_completed.v1" {
			if _, ok := ev.Payload["submission_id"].(string); !ok {
				t.Fatalf("mcq_completed: missing submission_id on payload")
			}
			if v, ok := ev.Payload["no_oe_pending"].(bool); !ok || !v {
				t.Fatalf("mcq_completed: no_oe_pending: want true (no OE), got %v", ev.Payload["no_oe_pending"])
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("LEG4-A: expected chora.delivery.grading.mcq_completed.v1 event after /submit; topics published: %v", topicsOf(pub.History()))
	}
}

func topicsOf(evs []events.PublishedEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Topic)
	}
	return out
}

// seedPublishedTestSetWithMCQ seeds the in-mem TestSet store with a single
// MCQ question carrying a PayloadSnapshot (so LEG5-B mcq_post_grade.options[]
// can be resolved at result time). Returns the populated TestSet.
func seedPublishedTestSetWithMCQ(t *testing.T, store *httpapi.InMemTestSetStore, testSetIDOverride string) *domain.TestSet {
	t.Helper()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:    tenantID,
		AuthorGCID:  instructor,
		Title:       "Phyllis TestSet",
		Description: "Demo MCQ for LEG5-B reveal",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	// Force the ID to match the assessment's test_set_id so the result
	// handler's snapshot lookup resolves the same aggregate.
	if testSetIDOverride != "" {
		ts.ID = testSetIDOverride
	}
	q, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-1",
		QuestionID:     "q-1",
		QuestionType:   string(domain.QuestionTypeMCQ),
		DisplayOrder:   1,
		Points:         100,
	})
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	q.PayloadSnapshot = `{"options":[{"option_id":"opt-1","label":"A","text":"Right","is_correct":true,"explainer":"Indeed."},{"option_id":"opt-2","label":"B","text":"Wrong","is_correct":false}],"scoring":{"mode":"single_correct"}}`
	if err := store.Save(context.Background(), ts); err != nil {
		t.Fatalf("Save test_set: %v", err)
	}
	return ts
}

// seedPublishedTestSetWithOE seeds the in-mem TestSet store with a single
// OE question carrying a FULL AUTHOR-SAFE PayloadSnapshot (stem +
// model_answer + rubric) — the shape B6 commits to producing from
// chora-creation's SnapshotQuestionByID gRPC. The result handler MUST
// project this to oe_post_grade on RELEASED submissions.
func seedPublishedTestSetWithOE(t *testing.T, store *httpapi.InMemTestSetStore, testSetIDOverride string) *domain.TestSet {
	t.Helper()
	ts, err := domain.NewTestSet(domain.NewTestSetInput{
		TenantID:    tenantID,
		AuthorGCID:  instructor,
		Title:       "Phyllis OE TestSet",
		Description: "Demo OE for ATOM-2 oe_post_grade reveal",
	})
	if err != nil {
		t.Fatalf("NewTestSet: %v", err)
	}
	if testSetIDOverride != "" {
		ts.ID = testSetIDOverride
	}
	q, err := ts.AddQuestion(domain.AddQuestionInput{
		QuestionAtomID: "atom-oe-1",
		QuestionID:     "q-oe-1",
		QuestionType:   string(domain.QuestionTypeOE),
		DisplayOrder:   1,
		Points:         100,
	})
	if err != nil {
		t.Fatalf("AddQuestion OE: %v", err)
	}
	// AUTHOR-SAFE OE snapshot — exactly what B6 will return from
	// SnapshotQuestionByID. Pre-RELEASE projection MUST strip model_answer
	// + rubric; post-RELEASE projection MUST preserve them.
	q.PayloadSnapshot = `{
		"stem":"Why do plants release oxygen during photosynthesis?",
		"prompt":"Why do plants release oxygen during photosynthesis?",
		"max_words":150,
		"min_words":30,
		"placeholder_text":"Explain in your own words...",
		"model_answer":"Plants split water (H2O) during the light-dependent reactions; oxygen is released as a byproduct while hydrogen reduces NADP+ to NADPH.",
		"rubric":[
			{"criterion_id":"clarity","title":"Clarity","description":"Argument is clearly stated","weight":0.4},
			{"criterion_id":"accuracy","title":"Accuracy","description":"Mentions H2O splitting + NADPH chain","weight":0.6}
		]
	}`
	if err := store.Save(context.Background(), ts); err != nil {
		t.Fatalf("Save test_set: %v", err)
	}
	return ts
}

// -----------------------------------------------------------------------------
// BE-ASSESSMENT-LIFECYCLE-CTAS-MISSING — /force-close + /archive (P0)
//
// Per docs/m13/be-cj1-specifics-2026-05-16.md §BE-ASSESSMENT-LIFECYCLE-CTAS-
// MISSING. FE R+ monitor calls POST /api/v1/assessments/{id}/force-close +
// /archive; both 404 pre-fix because routes are not registered in the admin
// sub-handler even though domain methods exist on *Assessment.
//
// FSM choice: Option A (permissive) — release-results already accepts from
// OPEN per `(a *Assessment) ReleaseResults(...)`; force-close becomes a
// curtailment CTA (close the window early without releasing). Archive accepts
// from any state (idempotent on ARCHIVED).
// -----------------------------------------------------------------------------

func TestForceCloseAssessment_FromOpen_ReturnsClosed(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/force-close", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v body=%s", err, w.Body.String())
	}
	if resp["state"] != "CLOSED" {
		t.Errorf("state: want CLOSED, got %v", resp["state"])
	}
	if resp["assessment_id"] != a.ID {
		t.Errorf("assessment_id: want %s, got %v", a.ID, resp["assessment_id"])
	}
}

func TestForceCloseAssessment_FromNonOpen_Returns409(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	// Move to RELEASED so force-close is invalid.
	a.State = domain.AssessmentStateReleased
	_ = aRepo.Save(context.Background(), a)

	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/force-close", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("status: want 409 from non-OPEN, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestForceCloseAssessment_NotInstructor_Returns403(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	// Caller is a learner — no instructor role, not the assessment instructor.
	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/force-close", nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestForceCloseAssessment_NotFound_Returns404(t *testing.T) {
	srv, _, _, _, _ := newAssessmentTestServer(t)

	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/nonexistent/force-close", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestForceCloseAssessment_MethodNotAllowed(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/force-close", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: want 405, got %d", w.Code)
	}
}

func TestArchiveAssessment_FromOpen_ReturnsArchived(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/archive", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v body=%s", err, w.Body.String())
	}
	if resp["state"] != "ARCHIVED" {
		t.Errorf("state: want ARCHIVED, got %v", resp["state"])
	}
}

// Re-archiving a soft-deleted assessment hits the deleted_at filter on
// InMemAssessmentRepo.Get (per .claude/rules/ddd-enforcement.md Aggregate
// Invariant #6 — default queries filter `WHERE deleted_at IS NULL`). The
// canonical REST semantic for "resource already in terminal soft-delete
// state" is 404, not 200 idempotent — once archived, it's not queryable.
// FE button gates on assessment state to avoid double-clicks.
func TestArchiveAssessment_Idempotent_FromArchived_Returns404Hidden(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)
	now := time.Now().UTC()
	a.State = domain.AssessmentStateArchived
	a.ArchivedAt = &now
	a.DeletedAt = &now
	_ = aRepo.Save(context.Background(), a)

	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/archive", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: want 404 (soft-deleted assessment is hidden per ddd-enforcement #6), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestArchiveAssessment_NotInstructor_Returns403(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/"+a.ID+"/archive", nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestArchiveAssessment_NotFound_Returns404(t *testing.T) {
	srv, _, _, _, _ := newAssessmentTestServer(t)

	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments/nonexistent/archive", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestArchiveAssessment_MethodNotAllowed(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodGet, "/api/v1/assessments/"+a.ID+"/archive", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: want 405, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// E2E-BE-RESULT-LINK-SUBMISSION-ID — list endpoint MUST carry
// learner_latest_submission_id (+ _state) so the FE "View result" link can
// route to /a/me/assessments/{id}/result/{subId} without an extra round-trip.
// Filed at c1bf953a 2026-05-17 ~07:57 after FE user-flagged the silent
// redirect to cover page.
// -----------------------------------------------------------------------------

// TestListMyAssessments_PopulatesLatestSubmissionFields seeds an OPEN
// assessment + one SUBMITTED submission for the learner, then asserts the
// list row carries both `learner_latest_submission_id` and
// `learner_latest_submission_state`.
func TestListMyAssessments_PopulatesLatestSubmissionFields(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		TenantID:     tenantID,
		AssessmentID: a.ID,
		LearnerGCID:  learner,
		MaxScore:     100,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if err := sub.MarkSubmitted(time.Now().UTC()); err != nil {
		t.Fatalf("MarkSubmitted: %v", err)
	}
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("sRepo.Save: %v", err)
	}

	r := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments", nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if len(resp.Items) == 0 {
		t.Fatalf("expected ≥1 item; body=%s", w.Body.String())
	}
	row := resp.Items[0]
	gotID, _ := row["learner_latest_submission_id"].(string)
	if gotID != sub.ID {
		t.Errorf("learner_latest_submission_id = %q; want %q (row=%+v)", gotID, sub.ID, row)
	}
	gotState, _ := row["learner_latest_submission_state"].(string)
	if gotState != string(sub.State) {
		t.Errorf("learner_latest_submission_state = %q; want %q", gotState, sub.State)
	}
}

// TestListMyAssessments_NoSubmission_OmitsLatestFields seeds an OPEN
// assessment without any learner submissions; both new fields must be
// absent (or null) so the FE's TypeScript narrow typing stays honest.
func TestListMyAssessments_NoSubmission_OmitsLatestFields(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	_ = seedOpenAssessmentWithCohort(t, aRepo)

	r := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments", nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) == 0 {
		t.Fatalf("expected ≥1 item")
	}
	row := resp.Items[0]
	if v, present := row["learner_latest_submission_id"]; present && v != nil {
		t.Errorf("learner_latest_submission_id should be absent/null with 0 attempts; got %v", v)
	}
	if v, present := row["learner_latest_submission_state"]; present && v != nil {
		t.Errorf("learner_latest_submission_state should be absent/null with 0 attempts; got %v", v)
	}
}

// TestListMyAssessments_PicksHighestAttemptNumber seeds two submissions
// for the same learner+assessment and asserts the LATEST (highest
// attempt_number) lands in the list row.
func TestListMyAssessments_PicksHighestAttemptNumber(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOpenAssessmentWithCohort(t, aRepo)

	old, _ := domain.NewSubmission(domain.NewSubmissionInput{
		TenantID:     tenantID,
		AssessmentID: a.ID,
		LearnerGCID:  learner,
		MaxScore:     100,
	})
	old.AttemptNumber = 1
	_ = old.MarkSubmitted(time.Now().UTC())
	if err := sRepo.Save(context.Background(), old); err != nil {
		t.Fatalf("Save old: %v", err)
	}

	latest, _ := domain.NewSubmission(domain.NewSubmissionInput{
		TenantID:     tenantID,
		AssessmentID: a.ID,
		LearnerGCID:  learner,
		MaxScore:     100,
	})
	latest.AttemptNumber = 2
	if err := sRepo.Save(context.Background(), latest); err != nil {
		t.Fatalf("Save latest: %v", err)
	}

	r := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments", nil, learner, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	row := resp.Items[0]
	gotID, _ := row["learner_latest_submission_id"].(string)
	if gotID != latest.ID {
		t.Errorf("learner_latest_submission_id = %q; want latest %q (not old %q)", gotID, latest.ID, old.ID)
	}
}

// -----------------------------------------------------------------------------
// shuffle_mcq_options DEFAULT-OFF (owner decision 2026-06-21, reverses 06-20)
//
// The create path defaults shuffle_mcq_options to FALSE when the field is
// OMITTED from the request body (new assessments keep MCQ options in their
// authored order unless the instructor opts into the "Scramble answer options
// (anti-cheat)" toggle on the R+ create form), while still honouring an
// explicit `true`/`false`. Implemented by making the request field a *bool so
// omitted (nil) is distinguishable from explicit false.
// -----------------------------------------------------------------------------

func TestCreateAssessment_ShuffleMCQOptions_DefaultsOffWhenOmitted(t *testing.T) {
	srv, _, _, tsStore, _ := newAssessmentTestServer(t)
	seedPublishedTestSetWithMCQ(t, tsStore, testSetID)

	// Body OMITS shuffle_mcq_options entirely.
	body := []byte(`{"test_set_id":"` + testSetID + `","title_override":"Default-off probe","invited_gcids":["` + learner + `"]}`)
	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if got, ok := resp["shuffle_mcq_options"].(bool); !ok || got {
		t.Errorf("shuffle_mcq_options omitted: want default false, got %v", resp["shuffle_mcq_options"])
	}
}

func TestCreateAssessment_ShuffleMCQOptions_HonoursExplicitTrue(t *testing.T) {
	srv, _, _, tsStore, _ := newAssessmentTestServer(t)
	seedPublishedTestSetWithMCQ(t, tsStore, testSetID)

	// Instructor ticked the anti-cheat toggle ⇒ explicit true.
	body := []byte(`{"test_set_id":"` + testSetID + `","title_override":"Anti-cheat-on probe","shuffle_mcq_options":true,"invited_gcids":["` + learner + `"]}`)
	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if got, _ := resp["shuffle_mcq_options"].(bool); !got {
		t.Errorf("shuffle_mcq_options=true explicit: want true, got %v", resp["shuffle_mcq_options"])
	}
}

func TestCreateAssessment_ShuffleMCQOptions_HonoursExplicitFalse(t *testing.T) {
	srv, _, _, tsStore, _ := newAssessmentTestServer(t)
	seedPublishedTestSetWithMCQ(t, tsStore, testSetID)

	body := []byte(`{"test_set_id":"` + testSetID + `","title_override":"Explicit-off probe","shuffle_mcq_options":false,"invited_gcids":["` + learner + `"]}`)
	r := reqWithHeaders(http.MethodPost, "/api/v1/assessments", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if got, _ := resp["shuffle_mcq_options"].(bool); got {
		t.Errorf("shuffle_mcq_options=false explicit: want false, got %v", resp["shuffle_mcq_options"])
	}
}
