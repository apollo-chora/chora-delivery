// assessment_delivery_type_test.go — CHO-2224 (§10.6 capstone criterion 1).
//
// The HTTP emit funnel (emitSubmissionGradedEvent) is the busiest of the two
// producers of chora.delivery.submission.graded.v1: it serves BOTH the MCQ-only
// fast path AND emitGradeOfRecord, which covers the instructor's per-submission
// approve and approve-all. If it does not stamp the parent Offering's
// delivery_type, the graduate lane, the one §10.6 leans on hardest, stays
// mode-blind.
//
// ⚠ This file exists because mutation testing found the funnel completely
// unproven: replacing `adeps.Offerings` with nil killed NOTHING across the whole
// package. Every other leg of the chain was covered in isolation while the path
// a real instructor actually walks was not.
//
// These drive the REAL route (POST .../approve) through the real server, and
// assert on the REAL emitted event.
package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// dtOfferings is a minimal domain.OfferingPort double returning one offering.
//
// It records the TENANT IT SAW ON THE CONTEXT, because OfferingPort.Get takes no
// tenant parameter: the pg adapter reads it from the ctx via rls.ApplySession.
// A caller that forgets tracing.WithTenantID therefore compiles, passes a naive
// double, and then finds ZERO rows (or the wrong tenant's) under real RLS. That
// exact trap has bitten this repo repeatedly, so the double observes it.
type dtOfferings struct {
	offering  *domain.Offering
	ok        bool
	err       error
	sawTenant string
	gotCalled bool
}

func (d *dtOfferings) Get(ctx context.Context, _ string) (*domain.Offering, bool, error) {
	d.gotCalled = true
	d.sawTenant = tracing.TenantIDFromContext(ctx)
	return d.offering, d.ok, d.err
}
func (d *dtOfferings) Save(context.Context, *domain.Offering) error { return nil }
func (d *dtOfferings) ListByTenant(context.Context, string) ([]*domain.Offering, error) {
	return nil, nil
}
func (d *dtOfferings) Search(context.Context, domain.OfferingQuery) (*domain.OfferingSearchPage, error) {
	return nil, nil
}

// newAssessmentServerWithOfferings mirrors newAssessmentTestServerWithRoster but
// wires the Offering port, so the graded emit can resolve a mode.
func newAssessmentServerWithOfferings(t *testing.T, offerings domain.OfferingPort) (
	http.Handler, *domain.InMemAssessmentRepo, *domain.InMemSubmissionRepo, *events.InMemoryPublisher,
) {
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
			Offerings:       offerings,
		},
	})
	return srv, aRepo, sRepo, pub
}

// seedOfferingScopedSubmission builds an assessment SCOPED TO AN OFFERING with a
// submission awaiting instructor approval — the live shape of the graduate lane.
func seedOfferingScopedSubmission(t *testing.T, aRepo *domain.InMemAssessmentRepo, sRepo *domain.InMemSubmissionRepo, offeringID string) (*domain.Assessment, *domain.Submission) {
	t.Helper()
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         tenantID,
		InstructorGCID:   instructor,
		TestSetID:        testSetID,
		Title:            "CHO-2224 mode attribution",
		InvitedGCIDs:     []string{learner},
		OfferingID:       offeringID,
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

	// Mirrors seedPendingReviewSubmission's shape: an MCQ + OE submission the AI
	// has graded, parked in GRADED_PENDING_RELEASE / PENDING_REVIEW awaiting the
	// human. That gate is what makes approve (not AI-grade time) the single
	// grade-of-record announcement, per CHO-2154.
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
	if _, err := sub.ApplyGrading(time.Now(), domain.SubmissionGrading{
		SubmissionID: sub.ID,
		AssessmentID: a.ID,
		Outcome:      "ok",
		GradedAt:     time.Now(),
		Graded: []domain.OEQuestionGrade{{
			TestSetQuestionID: "oe-1", QuestionID: "oe-q-1",
			PointsEarned: 8, PointsPossible: 10,
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

// TestCHO2224_Approve_StampsOfferingDeliveryType — the instructor approves; the
// single grade-of-record announcement must carry the offering's mode.
func TestCHO2224_Approve_StampsOfferingDeliveryType(t *testing.T) {
	off, err := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01970000-0000-7000-a000-0000000000c1"},
		Label:        "MSc Cohort 2026",
		DeliveryType: domain.DeliveryTypeGraduate,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	offerings := &dtOfferings{offering: off, ok: true}
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t, offerings)
	a, sub := seedOfferingScopedSubmission(t, aRepo, sRepo, off.ID)

	approveSubmission(t, srv, a, sub)

	emitted := gradedEvents(pub)
	if len(emitted) != 1 {
		t.Fatalf("approve must announce exactly ONE grade of record: got %d", len(emitted))
	}
	p := payloadOf(t, emitted[0])
	if got := p["delivery_type"]; got != "graduate" {
		t.Errorf("delivery_type = %v; want %q — the graduate lane is mode-blind, so §10.6 cannot attribute a mode", got, "graduate")
	}
	// The RLS tenant MUST ride the ctx: OfferingPort.Get takes no tenant param,
	// so an unstamped ctx finds zero rows under real RLS while passing any naive
	// double.
	if !offerings.gotCalled {
		t.Fatal("the offering was never read")
	}
	if offerings.sawTenant != tenantID {
		t.Errorf("Offerings.Get saw tenant %q on the ctx; want %q — without it the RLS-scoped read returns nothing in production",
			offerings.sawTenant, tenantID)
	}
}

// TestCHO2224_Approve_StampsShortDeliveryType — the OTHER half of the ">=2
// modes" proof, over the same real route.
func TestCHO2224_Approve_StampsShortDeliveryType(t *testing.T) {
	off, err := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01970000-0000-7000-a000-0000000000c2"},
		Label:        "Weekend Intro Workshop",
		DeliveryType: domain.DeliveryTypeShort,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t, &dtOfferings{offering: off, ok: true})
	a, sub := seedOfferingScopedSubmission(t, aRepo, sRepo, off.ID)

	approveSubmission(t, srv, a, sub)

	emitted := gradedEvents(pub)
	if len(emitted) != 1 {
		t.Fatalf("want 1 graded event, got %d", len(emitted))
	}
	p := payloadOf(t, emitted[0])
	if got := p["delivery_type"]; got != "short" {
		t.Errorf("delivery_type = %v; want %q", got, "short")
	}
}

// TestCHO2224_Approve_FreestandingAssessment_OmitsMode — offering_id is NULL
// (legal per migration 0033). No mode is invented, and the grade still lands.
func TestCHO2224_Approve_FreestandingAssessment_OmitsMode(t *testing.T) {
	off, _ := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01970000-0000-7000-a000-0000000000c1"},
		Label:        "Unrelated Offering",
		DeliveryType: domain.DeliveryTypeGraduate,
	})
	// The port would happily answer, but a freestanding assessment must never ask.
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t, &dtOfferings{offering: off, ok: true})
	a, sub := seedOfferingScopedSubmission(t, aRepo, sRepo, "") // no offering

	approveSubmission(t, srv, a, sub)

	emitted := gradedEvents(pub)
	if len(emitted) != 1 {
		t.Fatalf("the grade must still be announced: got %d graded events", len(emitted))
	}
	p := payloadOf(t, emitted[0])
	if _, present := p["delivery_type"]; present {
		t.Errorf("delivery_type must be ABSENT for a freestanding assessment, got %v", p["delivery_type"])
	}
	// ⚠ Absence is also the pre-revision publish safety property: a populated
	// field 14 is rejected 400 AT PUBLISH until the schema revision is committed.
}

// TestCHO2224_Approve_UnwiredOfferings_StillAnnouncesGrade — the port is nil
// (the pre-deploy state, and any misconfiguration). The mode is metadata; the
// grade is the payload. It must still be announced.
func TestCHO2224_Approve_UnwiredOfferings_StillAnnouncesGrade(t *testing.T) {
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t, nil)
	a, sub := seedOfferingScopedSubmission(t, aRepo, sRepo, "off-1")

	approveSubmission(t, srv, a, sub)

	emitted := gradedEvents(pub)
	if len(emitted) != 1 {
		t.Fatalf("an unwired Offering port must NOT cost the learner their grade: got %d graded events", len(emitted))
	}
	p := payloadOf(t, emitted[0])
	if _, present := p["delivery_type"]; present {
		t.Errorf("delivery_type must be absent without an Offering port, got %v", p["delivery_type"])
	}
}

// TestCHO2224_Approve_OfferingLookupError_StillAnnouncesGrade — a dead/RLS-failed
// offering read must never block the grade of record.
func TestCHO2224_Approve_OfferingLookupError_StillAnnouncesGrade(t *testing.T) {
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t,
		&dtOfferings{err: context.DeadlineExceeded})
	a, sub := seedOfferingScopedSubmission(t, aRepo, sRepo, "off-1")

	approveSubmission(t, srv, a, sub)

	emitted := gradedEvents(pub)
	if len(emitted) != 1 {
		t.Fatalf("a failed offering read must NOT block the grade: got %d graded events", len(emitted))
	}
	p := payloadOf(t, emitted[0])
	if _, present := p["delivery_type"]; present {
		t.Errorf("delivery_type must be absent when the lookup errored, got %v", p["delivery_type"])
	}
}
