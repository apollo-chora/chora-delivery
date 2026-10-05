package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// CHO-2351 - an `assessment` curriculum item's ref is a TEST_SET_ID by design
// (see course_content.go: "atom_id / test_set_id / classroom_id / url"), but the
// A+ curriculum CTA routes it as /a/me/assessments/{ref}, which expects an
// ASSESSMENT_ID. The learner therefore lands on "We couldn't find this
// assessment." These tests pin the ADR-185 learner-media seam as the place that
// resolves test_set_id to the assessment THIS learner may actually open.

// testSetRef is the test-set a curriculum `assessment` item points at.
const testSetRef = "019f8a0b-cfdd-7579-a563-2c4cf6c64fbf"

// learnerAssessmentID is the assessment built FROM testSetRef that the learner
// is in the cohort for. This is what the resolved ref must become.
const learnerAssessmentID = "019f8f7f-938d-7fed-bb42-766f26fdb9b8"

// fakeLearnerAssessments is a narrow stub of LearnerAssessmentLister. It records
// the scoping arguments so a test can prove the lookup is learner-scoped rather
// than a blind tenant-wide scan.
type fakeLearnerAssessments struct {
	items     []*domain.Assessment
	err       error
	gotTnt    string
	gotGCID   string
	callCount int
}

func (f *fakeLearnerAssessments) ListVisibleToLearner(
	_ context.Context, tenantID, learnerGCID string, _ int, _ string,
) ([]*domain.Assessment, string, error) {
	f.callCount++
	f.gotTnt, f.gotGCID = tenantID, learnerGCID
	if f.err != nil {
		return nil, "", f.err
	}
	return f.items, "", nil
}

// visibleAssessment builds an OPEN assessment carrying the given test-set id.
func visibleAssessment(id, testSetID string) *domain.Assessment {
	return &domain.Assessment{
		ID:        id,
		TenantID:  tnt,
		TestSetID: testSetID,
		State:     domain.AssessmentStateOpen,
		CreatedAt: time.Now(),
	}
}

func TestMeCourseMedia_ResolvesAssessmentRefToLearnerAssessmentID(t *testing.T) {
	lister := &fakeLearnerAssessments{
		items: []*domain.Assessment{visibleAssessment(learnerAssessmentID, testSetRef)},
	}
	h := newMeMediaHandlerWithAssessments(true, fakeResolver{}, lister,
		seedItem("assessment", testSetRef, "Final assessment"))

	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Resolved) != 1 {
		t.Fatalf("want the assessment item resolved, got %d: %v", len(got.Resolved), got.Resolved)
	}
	for itemID, ref := range got.Resolved {
		if itemID == "" {
			t.Fatalf("resolved key must be a content item_id")
		}
		if ref != learnerAssessmentID {
			t.Fatalf("assessment ref must resolve to the ASSESSMENT id %q, got %q "+
				"(a test_set_id here is the CHO-2351 defect)", learnerAssessmentID, ref)
		}
	}
	// The lookup must be scoped to the calling learner, not tenant-wide.
	if lister.gotGCID != enrolledGCID || lister.gotTnt != tnt {
		t.Fatalf("lookup must be learner-scoped, got tenant=%q gcid=%q", lister.gotTnt, lister.gotGCID)
	}
}

func TestMeCourseMedia_AssessmentRefLeftRawWhenNoVisibleMatch(t *testing.T) {
	// The learner is in no cohort for any assessment built from this test-set.
	// Leaving the ref RAW is fail-visible: the CTA stays broken rather than
	// silently pointing at some other learner's assessment.
	lister := &fakeLearnerAssessments{items: nil}
	h := newMeMediaHandlerWithAssessments(true, fakeResolver{}, lister,
		seedItem("assessment", testSetRef, "Final assessment"))

	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Resolved) != 0 {
		t.Fatalf("no visible assessment: want nothing resolved, got %v", got.Resolved)
	}
}

func TestMeCourseMedia_AssessmentRefNotResolvedToAnotherTestSet(t *testing.T) {
	// A visible assessment exists, but it was built from a DIFFERENT test-set.
	// Matching must be on test_set_id, never "first visible assessment wins".
	lister := &fakeLearnerAssessments{
		items: []*domain.Assessment{visibleAssessment(learnerAssessmentID, "019e0000-0000-7000-8000-00000000dead")},
	}
	h := newMeMediaHandlerWithAssessments(true, fakeResolver{}, lister,
		seedItem("assessment", testSetRef, "Final assessment"))

	rec := do(h, http.MethodGet, mediaURLsPath, "")
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Resolved) != 0 {
		t.Fatalf("test-set mismatch must not resolve, got %v", got.Resolved)
	}
}

func TestMeCourseMedia_AssessmentListerFailureLeavesRefRaw(t *testing.T) {
	// The curriculum is authoritative: an assessment-lookup outage must not
	// blank or 500 the media response. Refs stay raw, mirroring the existing
	// media best-effort contract.
	lister := &fakeLearnerAssessments{err: errors.New("boom")}
	h := newMeMediaHandlerWithAssessments(true, fakeResolver{}, lister,
		seedItem("assessment", testSetRef, "Final assessment"))

	rec := do(h, http.MethodGet, mediaURLsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lister outage must stay 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Resolved) != 0 {
		t.Fatalf("lister outage: want nothing resolved, got %v", got.Resolved)
	}
}

func TestMeCourseMedia_NoAssessmentItemsSkipsLookupEntirely(t *testing.T) {
	// An atom-only curriculum must not pay for an assessment query.
	lister := &fakeLearnerAssessments{}
	h := newMeMediaHandlerWithAssessments(true, fakeResolver{}, lister,
		seedItem("atom", atomRef, "Intro"))

	if rec := do(h, http.MethodGet, mediaURLsPath, ""); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if lister.callCount != 0 {
		t.Fatalf("no assessment items: lookup must be skipped, got %d calls", lister.callCount)
	}
}

func TestMeCourseMedia_ResolvesMediaAndAssessmentTogether(t *testing.T) {
	// The mixed curriculum this shipped for: a gs:// document AND an assessment
	// must both resolve in one response.
	lister := &fakeLearnerAssessments{
		items: []*domain.Assessment{visibleAssessment(learnerAssessmentID, testSetRef)},
	}
	seed := func(svc *cc.Service) {
		seedItem("video", gsVideoRef, "Lecture")(svc)
		seedItem("assessment", testSetRef, "Final assessment")(svc)
	}
	h := newMeMediaHandlerWithAssessments(true, fakeResolver{}, lister, seed)

	rec := do(h, http.MethodGet, mediaURLsPath, "")
	var got struct {
		Resolved map[string]string `json:"resolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Resolved) != 2 {
		t.Fatalf("want BOTH the signed video and the assessment resolved, got %d: %v",
			len(got.Resolved), got.Resolved)
	}
	var sawSigned, sawAssessment bool
	for _, v := range got.Resolved {
		if v == "https://signed-get.example/"+gsVideoRef {
			sawSigned = true
		}
		if v == learnerAssessmentID {
			sawAssessment = true
		}
	}
	if !sawSigned || !sawAssessment {
		t.Fatalf("mixed curriculum: signed=%v assessment=%v (%v)", sawSigned, sawAssessment, got.Resolved)
	}
}
