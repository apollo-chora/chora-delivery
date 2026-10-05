// grading_final_grade_test.go — CHO-2154.
//
// `chora.delivery.submission.graded.v1` currently means "the AI produced a
// number". It must mean "this submission has a GRADE OF RECORD".
//
// The OE path always lands the submission in GRADED_PENDING_RELEASE with
// review_status=PENDING_REVIEW (ADR-172 §D6's mandatory HITL gate) — so EVERY
// graded.v1 emitted from this subscriber carries a PROVISIONAL, un-reviewed AI
// grade. Five subscribers act on it: the learner's transcript, their XP, their
// notification ("your grade is ready"), the derived Growth-Edge projector, and
// module progress. All five therefore acted on a number no human had approved,
// and none of them ever heard about the instructor's correction.
//
// The instructor's grade could not simply be re-emitted afterwards: the outbox
// derives a STABLE idempotency key (`<submission_id>:graded`) and the column is
// UNIQUE, so a second graded.v1 for the same submission is silently swallowed —
// and salting it to force publication would re-fire all five, double-awarding
// XP and re-notifying the learner.
//
// So the event moves instead of multiplying: suppressed here while the grade is
// provisional, emitted ONCE at approve, carrying the human's final scores.
package subscribers_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// An AI-graded OE submission is PENDING_REVIEW — a provisional grade. It must
// NOT be announced to the platform as the learner's graded outcome.
//
// PRE-FIX: graded.v1 IS emitted here, so the learner's transcript, XP and
// "your grade is ready" notification all fire off the AI's number, before any
// human has seen it.
func TestCHO2154_GradingInbox_PendingReview_DoesNotEmitProvisionalGrade(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	// HandleSubmissionCompleted is the ADR-172 per-submission path — the one
	// that arms the mandatory HITL gate (review_status = PENDING_REVIEW). The
	// sibling Handle (ADR-155 OE *batch*) sets no gate, so its grade IS final
	// on computation and its emit is correct; this test is about the gated one.
	env := events.EventEnvelope{EventID: "evt-cho2154", TenantID: gradingTenant, GCID: gradingLearner}
	p := subscribers.SubmissionCompletedPayload{
		SubmissionID: sub.ID,
		AssessmentID: sub.AssessmentID,
		Outcome:      "ok",
		CompletedAt:  time.Now().UTC(),
		TenantID:     gradingTenant,
		LearnerGCID:  gradingLearner,
		Graded: []subscribers.SubmissionGradedAnswer{{
			TestSetQuestionID: "oe-1",
			QuestionID:        "oe-q-1",
			PointsEarned:      40,
			PointsPossible:    50,
			Comment:           "good",
		}},
	}
	if err := s.HandleSubmissionCompleted(context.Background(), env, p); err != nil {
		t.Fatalf("HandleSubmissionCompleted: %v", err)
	}

	// The submission must STILL advance + persist — only the announcement waits.
	got, ok, _ := repo.Get(context.Background(), gradingTenant, sub.ID)
	if !ok {
		t.Fatalf("submission missing post-handle")
	}
	if got.State != domain.SubmissionStateGradedPendingRelease {
		t.Errorf("state: want GRADED_PENDING_RELEASE, got %s", got.State)
	}
	if got.ReviewStatus != domain.ReviewStatusPendingReview {
		t.Fatalf("fixture is not the shape under test: want PENDING_REVIEW, got %q", got.ReviewStatus)
	}
	if got.TotalScore != 90 {
		t.Errorf("total: want 90 (the AI's provisional number, persisted), got %v", got.TotalScore)
	}

	// ...but NOTHING is announced. This grade is not yet a grade of record.
	if emitted := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1"); len(emitted) != 0 {
		t.Errorf("a PENDING_REVIEW grade must not be announced as graded: got %d graded.v1 events — "+
			"five subscribers (transcript, XP, notification, Growth-Edge, module progress) act on this",
			len(emitted))
	}
}
