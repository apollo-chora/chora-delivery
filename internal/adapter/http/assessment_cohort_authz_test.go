// assessment_cohort_authz_test.go — CHO-2153.
//
// Two independent live defects, both found by the R+ GRADUATE DoD walk
// (CHO-2151). Every test in this file is a BEHAVIOURAL RED: it compiles and
// runs against the PRE-FIX handler and fails there, so the fix is proven by
// the test going green rather than by the test being written to fit it.
//
//	F5 — cohort authz. An offering-attached OPEN_LINK assessment was eligible
//	     to ANY tenant member. ADR-155 §D9 sanctioned this as a "v1 demo
//	     simplification"; on a platform with real offerings, rosters and
//	     cert-bearing assessments it is an authz hole. START returned 201 for
//	     a learner with no relationship to the offering — burning an attempt
//	     against max_attempts and emitting submission.started.v1.
//
//	F6 — the close window was never enforced. Nothing transitioned OPEN →
//	     CLOSED at scheduled_close_at, and START validated no window at either
//	     end. A past-close assessment stayed startable forever; only the FIRST
//	     AUTOSAVE 409'd (CanAutosave) — after the attempt row was committed and
//	     the event emitted. Live: two SE-Test001 assessments sat in state OPEN
//	     with scheduled_close_at = 2026-07-08 and still rendered AVAILABLE with
//	     an enabled Start on 2026-07-14.
//
// Each test asserts the negative that actually matters: refusal happens BEFORE
// any submission row is written and BEFORE any event is emitted. A 403 that
// still burns the attempt is not a fix.
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
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// offeringID is the cohort anchor: the assessment hangs off this offering and
// the roster truth is "the learner has a live course_enrollments row for ≥1
// course of this offering". This is the live SE-Test001 offering that leaked.
const offeringID = "019f3c77-c003-7611-a468-8a8f0e836401"

// seedOfferingOpenLinkAssessment builds the exact live shape that leaked:
// offering-attached, no class, no invite list ⇒ Cohort() == CohortOpenLink.
func seedOfferingOpenLinkAssessment(t *testing.T, aRepo *domain.InMemAssessmentRepo, openAt, closeAt time.Time) *domain.Assessment {
	t.Helper()
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		OfferingID:       offeringID,
		Title:            "CHO-2153 offering-attached open-link",
		ScheduledOpenAt:  openAt,
		ScheduledCloseAt: closeAt,
		MaxAttempts:      1,
		TotalPoints:      100,
		QuestionCount:    1,
		// ClassID + InvitedGCIDs deliberately empty → OPEN_LINK.
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	if err := a.Publish(time.Now().Add(-72 * time.Hour)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	a.AutoFlipToOpen(time.Now())
	if got := a.Cohort(); got != domain.CohortOpenLink {
		t.Fatalf("fixture is not the shape under test: want OPEN_LINK, got %s", got)
	}
	if err := aRepo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return a
}

// startSubmission POSTs the learner START.
func startSubmission(t *testing.T, srv http.Handler, assessmentID, gcid string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, "/api/v1/me/assessments/"+assessmentID+"/submissions", nil, gcid, "learner")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// F5 — cohort authz
// -----------------------------------------------------------------------------

// A learner with NO enrolment in any course of the assessment's offering must
// not be able to START it.
//
// PRE-FIX: IsLearnerEligible() returns true for CohortOpenLink unconditionally
// ⇒ 201 Created, attempt burned, submission.started.v1 emitted. This is the
// live hole: learner L, enrolled only in course 019edf3e, could start
// SE-Test001's assessments (offering 019f3c77, courses 019eb059 + 019edf1c —
// L in neither).
func TestCHO2153_Start_NonEnrolledLearner_OfferingOpenLink_Is403(t *testing.T) {
	srv, aRepo, sRepo, _, pub := newAssessmentTestServer(t)
	a := seedOfferingOpenLinkAssessment(t, aRepo,
		time.Now().Add(-1*time.Hour), time.Now().Add(2*time.Hour))

	// `learner` has no course_enrollments row for any course of `offeringID`.
	w := startSubmission(t, srv, a.ID, learner)

	if w.Code != http.StatusForbidden {
		t.Errorf("START by a non-enrolled learner: want 403, got %d — body: %s",
			w.Code, strings.TrimSpace(w.Body.String()))
	}
	if n := attemptCount(t, sRepo, a.ID, learner); n != 0 {
		t.Errorf("a refused START must not burn an attempt: got %d submission rows", n)
	}
	if emitted := startedTopics(pub.History()); len(emitted) != 0 {
		t.Errorf("a refused START must not emit a lifecycle event: got %v", emitted)
	}
}

// The learner must also not be able to SEE it — the list and the detail read
// are the same authz decision as START (a 403 on START behind a listed card is
// a broken UI, and the card itself leaks the assessment's existence).
func TestCHO2153_GetDetail_NonEnrolledLearner_OfferingOpenLink_Is403(t *testing.T) {
	srv, aRepo, _, _, _ := newAssessmentTestServer(t)
	a := seedOfferingOpenLinkAssessment(t, aRepo,
		time.Now().Add(-1*time.Hour), time.Now().Add(2*time.Hour))

	r := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments/"+a.ID, nil, learner, "learner")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("GET detail by a non-enrolled learner: want 403, got %d — body: %s",
			w.Code, strings.TrimSpace(w.Body.String()))
	}
}

// ...and it must not appear in the learner's list. A cohort fix that guards
// START but still lists the card leaks the assessment's existence and hands the
// learner a button that 403s.
func TestCHO2153_List_NonEnrolledLearner_OmitsOfferingOpenLink(t *testing.T) {
	srv, aRepo, _, _, _, roster := newAssessmentTestServerWithRoster(t)
	hidden := seedOfferingOpenLinkAssessment(t, aRepo,
		time.Now().Add(-1*time.Hour), time.Now().Add(2*time.Hour))

	// A second assessment the learner IS entitled to, via an explicit invite —
	// proves the filter scopes rather than simply emptying the list.
	invited := seedOpenAssessmentWithCohort(t, aRepo)

	_ = roster // learner enrolled in nothing

	ids := listMyAssessmentIDs(t, srv, learner)
	if containsID(ids, hidden.ID) {
		t.Errorf("a non-enrolled learner must not see the offering's assessment: got %v", ids)
	}
	if !containsID(ids, invited.ID) {
		t.Errorf("the explicitly-invited assessment must still be listed: got %v", ids)
	}
}

// The ENROLLED learner is unaffected — the fix must scope, not lock everyone
// out. Live, learner L was enrolled and legitimately sat the assessment;
// tightening open-link had to leave that path intact.
func TestCHO2153_Start_EnrolledLearner_OfferingOpenLink_Is201(t *testing.T) {
	srv, aRepo, sRepo, _, _, roster := newAssessmentTestServerWithRoster(t)
	a := seedOfferingOpenLinkAssessment(t, aRepo,
		time.Now().Add(-1*time.Hour), time.Now().Add(2*time.Hour))

	roster.Enrol(offeringID, learner)

	w := startSubmission(t, srv, a.ID, learner)
	if w.Code != http.StatusCreated {
		t.Fatalf("START by an ENROLLED learner: want 201, got %d — body: %s",
			w.Code, strings.TrimSpace(w.Body.String()))
	}
	if n := attemptCount(t, sRepo, a.ID, learner); n != 1 {
		t.Errorf("an accepted START must write exactly one submission row: got %d", n)
	}
	if ids := listMyAssessmentIDs(t, srv, learner); !containsID(ids, a.ID) {
		t.Errorf("the enrolled learner must SEE the assessment they can start: got %v", ids)
	}
}

// A roster OUTAGE must refuse, not admit. This is the failure mode that made
// CHO-2153 so quiet: a gate that cannot reach its source of truth and answers
// "sure, go ahead". It must 5xx loudly instead — a denial and an outage are
// different things, and only one of them is safe to guess at.
func TestCHO2153_Start_RosterOutage_RefusesAndDoesNotAdmit(t *testing.T) {
	srv, aRepo, sRepo, _, pub, roster := newAssessmentTestServerWithRoster(t)
	a := seedOfferingOpenLinkAssessment(t, aRepo,
		time.Now().Add(-1*time.Hour), time.Now().Add(2*time.Hour))

	roster.Enrol(offeringID, learner) // the learner IS entitled...
	roster.Err = errors.New("cloud sql: connection refused")

	w := startSubmission(t, srv, a.ID, learner)
	if w.Code == http.StatusCreated {
		t.Errorf("a roster outage must never admit: got 201")
	}
	if w.Code < 500 {
		t.Errorf("a roster outage must fail LOUD (5xx), got %d — a silent 403 would hide the outage", w.Code)
	}
	if n := attemptCount(t, sRepo, a.ID, learner); n != 0 {
		t.Errorf("a roster outage must not burn an attempt: got %d rows", n)
	}
	if emitted := startedTopics(pub.History()); len(emitted) != 0 {
		t.Errorf("a roster outage must not emit a lifecycle event: got %v", emitted)
	}
}

// -----------------------------------------------------------------------------
// F6 — the close window
// -----------------------------------------------------------------------------

// scheduled_close_at in the past ⇒ START refused BEFORE any row or event.
//
// PRE-FIX: AutoFlipToOpen() opens it, nothing ever closes it, the state check
// accepts OPEN, and NewSubmission validates no window ⇒ 201. The learner only
// discovers the assessment is closed on the first autosave — by which point
// the attempt row is committed and submission.started.v1 is on the wire.
func TestCHO2153_Start_AfterCloseWindow_IsRefused_NoAttemptBurned(t *testing.T) {
	srv, aRepo, sRepo, _, pub := newAssessmentTestServer(t)
	a := seedOfferingOpenLinkAssessment(t, aRepo,
		time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)) // closed a day ago

	w := startSubmission(t, srv, a.ID, learner)

	if w.Code == http.StatusCreated {
		t.Errorf("START after scheduled_close_at must be refused, got 201 — body: %s",
			strings.TrimSpace(w.Body.String()))
	}
	if n := attemptCount(t, sRepo, a.ID, learner); n != 0 {
		t.Errorf("a past-close START must not burn an attempt: got %d submission rows", n)
	}
	if emitted := startedTopics(pub.History()); len(emitted) != 0 {
		t.Errorf("a past-close START must not emit a lifecycle event: got %v", emitted)
	}
}

// The window must be enforced at BOTH ends. PRE-FIX the state check accepted
// SCHEDULED as well as OPEN, so an assessment whose window had not yet opened
// was startable too — the same class of hole, at the other end.
func TestCHO2153_Start_BeforeOpenWindow_IsRefused(t *testing.T) {
	srv, aRepo, sRepo, _, _ := newAssessmentTestServer(t)
	a := seedOfferingOpenLinkAssessment(t, aRepo,
		time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour)) // opens tomorrow

	w := startSubmission(t, srv, a.ID, learner)

	if w.Code == http.StatusCreated {
		t.Errorf("START before scheduled_open_at must be refused, got 201 — body: %s",
			strings.TrimSpace(w.Body.String()))
	}
	if n := attemptCount(t, sRepo, a.ID, learner); n != 0 {
		t.Errorf("a not-yet-open START must not burn an attempt: got %d submission rows", n)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func attemptCount(t *testing.T, sRepo *domain.InMemSubmissionRepo, assessmentID, gcid string) int {
	t.Helper()
	n, err := sRepo.CountAttemptsByLearner(context.Background(), tenantID, assessmentID, gcid)
	if err != nil {
		t.Fatalf("CountAttemptsByLearner: %v", err)
	}
	return n
}

// startedTopics returns the submission-lifecycle topics emitted so far. A
// refused START must emit NONE of them — the event is as damaging as the row
// (downstream projectors treat submission.started.v1 as "the learner sat it").
func startedTopics(evs []events.PublishedEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		if strings.Contains(e.Topic, "submission.started") {
			out = append(out, e.Topic)
		}
	}
	return out
}

// listMyAssessmentIDs drives GET /api/v1/me/assessments and returns the ids.
func listMyAssessmentIDs(t *testing.T, srv http.Handler, gcid string) []string {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/me/assessments", nil, gcid, "learner")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /me/assessments: want 200, got %d — body: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
	var body struct {
		Items []struct {
			AssessmentID string `json:"assessment_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /me/assessments: %v — body: %s", err, w.Body.String())
	}
	ids := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		ids = append(ids, it.AssessmentID)
	}
	return ids
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
