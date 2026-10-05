// grading_inbox.go — Fix-E / ADR-155 Lane B completion-event subscriber.
//
// Closes the agentic-dispatch loop: chora-delivery emits
// chora.delivery.grading.oe_batch_requested.v1 on /submit; the
// chora-ai-kernel-orchestrator (Python LangGraph) invokes the oe_grader
// Vertex engine and emits chora.delivery.grading.oe_batch_completed.v1; THIS
// subscriber consumes the completion event, applies the OE scores onto the
// submission aggregate, transitions PENDING_OE_GRADING → GRADED_PENDING_RELEASE
// when all OE answers are graded, and (optionally) emits
// chora.delivery.submission.graded.v1 via the outbox.
//
// Per ADR-155 §"Locked architectural rule" + memory feedback_agentic_pubsub_only:
// agentic dispatch is Pub/Sub-orchestrator-managed. The chora-delivery side
// NEVER calls a reasoning engine directly. This subscriber is the inbound
// adapter; it depends on the domain.Submission aggregate operation
// ApplyOEGradingBatch.
//
// Hexagonal:
//   - INBOUND ADAPTER from Pub/Sub
//   - depends on the SubmissionRepo port (existing)
//   - depends on a SubmissionGradedPublisher port (the publishCustomEvent
//     pattern in assessment_handler.go already exposes PublishCustom; we
//     accept events.Publisher and call PublishCustom via interface assertion)
//
// Idempotency: keyed on (event_id) via libs/chora-go-common/idempotent.Store.
// Pub/Sub at-least-once delivery means the orchestrator MAY re-emit the same
// completion event after a transient broker error — the inbox short-circuits
// the second arrival before mutating the aggregate.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// TopicGradingOEBatchCompleted — chora-ai-kernel-orchestrator emits this when
// the oe_grader Vertex engine returns batched results (or fails). Subscriber
// binding: per-service push subscription chora-delivery-grading-inbox.
const TopicGradingOEBatchCompleted = "chora.delivery.grading.oe_batch_completed.v1"

// TopicGradingSubmissionCompleted — ADR-172 per-submission grading completion.
// The oe_grading_crew (evaluator→moderator loop + assess_summary) emits this
// with per-OE-question scores+comments + the whole-assessment overall comment.
// Supersedes the batch completion event above.
const TopicGradingSubmissionCompleted = "chora.delivery.grading.submission_completed.v1"

// GradingInboxTTL is the dedupe-key retention window for the grading inbox.
// Pub/Sub redelivery for the orchestrator → chora-delivery hop is typically
// seconds; 24h amply covers the worst case including DLQ recovery.
const GradingInboxTTL = 24 * time.Hour

// OEBatchCompletedPayload mirrors the projected shape produced by
// protodecode.DecodePayloadMap for the completion event. Mirrors the load-
// bearing fields of GradingOeBatchCompleted in chora-contracts.
type OEBatchCompletedPayload struct {
	OEBatchID      string
	SubmissionID   string
	AssessmentID   string
	BatchOutcome   string // "ok" / "failed" / "skipped"
	FailureMessage string
	CompletedAt    time.Time
	TenantID       string // hydrated from Pub/Sub attributes
	LearnerGCID    string // hydrated from envelope.gcid attribute
	Traceparent    string
	Results        []OEBatchGradedAnswer
}

// OEBatchGradedAnswer mirrors QuestionGradeBatchResult — one per OE answer.
type OEBatchGradedAnswer struct {
	SubmissionID         string
	TestSetQuestionID    string
	QuestionID           string
	LearnerGCID          string
	PointsEarned         float64
	PointsPossible       int
	CriterionScoresJSON  string
	LLMEvaluatorFeedback string
}

// SubmissionCompletedPayload is the projected shape of
// chora.delivery.grading.submission_completed.v1 (ADR-172).
type SubmissionCompletedPayload struct {
	SubmissionID             string
	AssessmentID             string
	LearnerGCID              string
	Outcome                  string // SUCCESS | PARTIAL | FAILED
	FailureMessage           string
	OverallComment           string
	OverallCommentModelID    string
	OverallCommentResponseID string // IMDA D2 provenance for the overall comment
	CompletedAt              time.Time
	TenantID                 string
	Traceparent              string
	Graded                   []SubmissionGradedAnswer
}

// SubmissionGradedAnswer mirrors OEQuestionGradeResult — one per OE answer.
type SubmissionGradedAnswer struct {
	TestSetQuestionID   string
	QuestionID          string
	PointsEarned        float64
	PointsPossible      int
	CriterionScoresJSON string
	Comment             string
	GradingModelID      string
	GradingResponseID   string
	QualityFlagged      bool
}

// AssessmentTitleReader reads an assessment (for its title) by id — the narrow
// slice of domain.AssessmentRepo the OE emit needs (WS-6, CHO-1958). The MCQ
// fast path holds the loaded Assessment; this OE-batch path has only the
// Submission, so it resolves the title by the submission's assessment id.
// domain.AssessmentRepo satisfies this structurally.
type AssessmentTitleReader interface {
	Get(ctx context.Context, tenantID, id string) (*domain.Assessment, bool, error)
}

// GradingInboxSubscriber consumes the OE batch completion event + applies it
// onto the submission aggregate.
type GradingInboxSubscriber struct {
	submissions domain.SubmissionRepo
	publisher   events.Publisher
	inbox       idempotent.Store
	ttl         time.Duration
	assessments AssessmentTitleReader // optional (WS-6): resolves assessment_title for the emitted graded event
	offerings   domain.OfferingPort   // optional (CHO-2224): resolves the parent Offering's delivery_type for the emitted graded event
}

// NewGradingInboxSubscriber wires the repo + publisher + inbox.
//
// inbox is OPTIONAL — nil triggers a MemoryStore fallback so dev / unit tests
// don't need a database. Production callers SHOULD pass a PostgresStore-
// backed inbox so dedup survives pod restart + works across replicas.
func NewGradingInboxSubscriber(
	submissions domain.SubmissionRepo,
	publisher events.Publisher,
	inbox idempotent.Store,
) *GradingInboxSubscriber {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &GradingInboxSubscriber{
		submissions: submissions,
		publisher:   publisher,
		inbox:       inbox,
		ttl:         GradingInboxTTL,
	}
}

// WithTTL overrides the dedupe-key retention window.
func (s *GradingInboxSubscriber) WithTTL(ttl time.Duration) *GradingInboxSubscriber {
	if s == nil || ttl <= 0 {
		return s
	}
	s.ttl = ttl
	return s
}

// WithAssessmentReader attaches the assessment-title reader (WS-6, CHO-1958)
// so the OE-batch emit can snapshot assessment_title onto submission.graded.v1.
// Builder-style, nil-safe: without it the event still emits, just title-less
// (the prior behaviour — consumption then acks it as unmappable evidence).
func (s *GradingInboxSubscriber) WithAssessmentReader(r AssessmentTitleReader) *GradingInboxSubscriber {
	if s == nil {
		return s
	}
	s.assessments = r
	return s
}

// WithOfferings attaches the Offering port (CHO-2224) so this OE-batch emit can
// snapshot the parent Offering's delivery_type onto submission.graded.v1 — the
// MODE attribution §10.6 criterion 1 needs, and chora-consumption's only
// mode-bearing signal.
//
// Builder-style, nil-safe: without it the event still emits, just mode-less.
// Resolution ALSO needs the assessment reader (the offering id lives on the
// Assessment), so an offerings port without WithAssessmentReader yields nothing.
func (s *GradingInboxSubscriber) WithOfferings(o domain.OfferingPort) *GradingInboxSubscriber {
	if s == nil {
		return s
	}
	s.offerings = o
	return s
}

// SubscribedTopic returns the inbound topic this subscriber binds to.
func (s *GradingInboxSubscriber) SubscribedTopic() string { return TopicGradingOEBatchCompleted }

// Handle processes one chora.delivery.grading.oe_batch_completed.v1 event.
//
// Required envelope fields (validated): EventID + TenantID + SubmissionID.
// The Pub/Sub push handler hydrates these from message attributes before
// calling Handle — see http/grading_pubsub_handler.go.
//
// Behaviour:
//   - dedupe on event_id via the inbox Store
//   - load the submission aggregate
//   - call Submission.ApplyOEGradingBatch (the load-bearing aggregate op)
//   - save the submission
//   - when allGraded == true, emit chora.delivery.submission.graded.v1 via
//     the outbox (the assessment handler's existing emitSubmissionGradedEvent
//     does the same when the MCQ-only fast path completes inline)
//
// Returns:
//   - nil on success (ack)
//   - error on persistence / publish failure (Nack → Pub/Sub retry)
func (s *GradingInboxSubscriber) Handle(ctx context.Context, env events.EventEnvelope, p OEBatchCompletedPayload) error {
	if s == nil || s.submissions == nil {
		return errors.New("subscribers: nil grading inbox")
	}
	if strings.TrimSpace(env.EventID) == "" {
		return errors.New("subscribers: grading inbox event_id required")
	}
	tenantID := strings.TrimSpace(p.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(env.TenantID)
	}
	if tenantID == "" {
		return errors.New("subscribers: grading inbox tenant_id required")
	}
	if strings.TrimSpace(p.SubmissionID) == "" {
		return errors.New("subscribers: grading inbox submission_id required")
	}

	return s.inbox.Process(ctx, "grading:"+env.EventID, s.ttl, func() error {
		sub, ok, err := s.submissions.Get(ctx, tenantID, p.SubmissionID)
		if err != nil {
			return fmt.Errorf("subscribers: get submission %s: %w", p.SubmissionID, err)
		}
		if !ok || sub == nil {
			// Unknown submission — could be a tenant-mismatch or a routing
			// bug. Ack so Pub/Sub doesn't keep redelivering; a future audit
			// reconciles via outbox + DLQ.
			return nil
		}

		now := time.Now().UTC()
		outcome := strings.ToLower(strings.TrimSpace(p.BatchOutcome))
		batch := domain.OEGradingBatch{
			SubmissionID:   p.SubmissionID,
			AssessmentID:   p.AssessmentID,
			BatchOutcome:   outcome,
			FailureMessage: p.FailureMessage,
			GradedAt:       firstNonZero(p.CompletedAt, now),
			Results:        toDomainResults(p.Results),
		}
		allGraded, applyErr := sub.ApplyOEGradingBatch(now, batch)
		if applyErr != nil {
			if errors.Is(applyErr, domain.ErrSubmissionMismatch) {
				// Routing bug — ack-and-drop. The orchestrator's retry path
				// would loop forever on a Nack.
				return nil
			}
			return applyErr
		}
		if err := s.submissions.Save(ctx, sub); err != nil {
			return fmt.Errorf("subscribers: save submission %s: %w", p.SubmissionID, err)
		}
		if allGraded {
			s.emitSubmissionGraded(ctx, sub, env.Traceparent)
		}
		// E2E-BE-8 — terminal grading-job failure marker.
		// When the orchestrator returns batch_outcome != "ok" the
		// submission FSM stays PENDING_OE_GRADING (per ApplyOEGradingBatch
		// invariant) but downstream subscribers (O+ governance, R+ admin
		// monitoring, Observability) MUST see a typed failure event with
		// full provenance for IMDA D1 accountability.
		if outcome != "ok" && outcome != "" {
			s.emitGradingFailed(sub, p, env.Traceparent, now)
		}
		return nil
	})
}

// HandleSubmissionCompleted processes one
// chora.delivery.grading.submission_completed.v1 event (ADR-172). Mirrors
// Handle but applies the richer per-submission grading (per-question comment +
// LLM provenance + quality flag + overall comment) via Submission.ApplyGrading,
// landing the submission GRADED_PENDING_RELEASE + review_status=PENDING_REVIEW.
func (s *GradingInboxSubscriber) HandleSubmissionCompleted(ctx context.Context, env events.EventEnvelope, p SubmissionCompletedPayload) error {
	if s == nil || s.submissions == nil {
		return errors.New("subscribers: nil grading inbox")
	}
	if strings.TrimSpace(env.EventID) == "" {
		return errors.New("subscribers: grading inbox event_id required")
	}
	tenantID := strings.TrimSpace(p.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(env.TenantID)
	}
	if tenantID == "" {
		return errors.New("subscribers: grading inbox tenant_id required")
	}
	if strings.TrimSpace(p.SubmissionID) == "" {
		return errors.New("subscribers: grading inbox submission_id required")
	}

	return s.inbox.Process(ctx, "grading:"+env.EventID, s.ttl, func() error {
		sub, ok, err := s.submissions.Get(ctx, tenantID, p.SubmissionID)
		if err != nil {
			return fmt.Errorf("subscribers: get submission %s: %w", p.SubmissionID, err)
		}
		if !ok || sub == nil {
			return nil // unknown submission — ack-and-drop
		}

		now := time.Now().UTC()
		grading := domain.SubmissionGrading{
			SubmissionID:             p.SubmissionID,
			AssessmentID:             p.AssessmentID,
			Outcome:                  strings.ToUpper(strings.TrimSpace(p.Outcome)),
			FailureMessage:           p.FailureMessage,
			OverallComment:           p.OverallComment,
			OverallCommentModelID:    p.OverallCommentModelID,
			OverallCommentResponseID: p.OverallCommentResponseID,
			GradedAt:                 firstNonZero(p.CompletedAt, now),
			Graded:                   toDomainGrades(p.Graded),
		}
		allGraded, applyErr := sub.ApplyGrading(now, grading)
		if applyErr != nil {
			if errors.Is(applyErr, domain.ErrSubmissionMismatch) {
				return nil // routing bug — ack-and-drop
			}
			return applyErr
		}
		if err := s.submissions.Save(ctx, sub); err != nil {
			return fmt.Errorf("subscribers: save submission %s: %w", p.SubmissionID, err)
		}
		if allGraded {
			s.emitSubmissionGraded(ctx, sub, env.Traceparent)
		}
		if outcome := strings.ToUpper(strings.TrimSpace(p.Outcome)); outcome == "FAILED" {
			s.emitGradingFailed(sub, OEBatchCompletedPayload{
				SubmissionID: p.SubmissionID, AssessmentID: p.AssessmentID,
				FailureMessage: p.FailureMessage,
			}, env.Traceparent, now)
		}
		return nil
	})
}

func toDomainGrades(in []SubmissionGradedAnswer) []domain.OEQuestionGrade {
	out := make([]domain.OEQuestionGrade, 0, len(in))
	for _, r := range in {
		out = append(out, domain.OEQuestionGrade{
			TestSetQuestionID:   r.TestSetQuestionID,
			QuestionID:          r.QuestionID,
			PointsEarned:        r.PointsEarned,
			PointsPossible:      r.PointsPossible,
			CriterionScoresJSON: r.CriterionScoresJSON,
			Comment:             r.Comment,
			GradingModelID:      r.GradingModelID,
			GradingResponseID:   r.GradingResponseID,
			QualityFlagged:      r.QualityFlagged,
		})
	}
	return out
}

// emitSubmissionGraded publishes chora.delivery.submission.graded.v1 via the
// outbox publisher (when one is wired). Mirrors emitSubmissionGradedEvent in
// internal/adapter/http/assessment_handler.go for the MCQ-only fast path.
//
// The outbox publisher implements a PublishCustom escape hatch via interface
// assertion — the InMemoryPublisher used in tests captures the call; the
// outbox.TransactionalPublisher writes a row to outbox_events that the
// dispatcher drains to Cloud Pub/Sub.
func (s *GradingInboxSubscriber) emitSubmissionGraded(ctx context.Context, sub *domain.Submission, traceparent string) {
	if s == nil || s.publisher == nil || sub == nil {
		return
	}
	// CHO-2154 — chora.delivery.submission.graded.v1 announces a GRADE OF
	// RECORD, not "the AI produced a number".
	//
	// The ADR-172 per-submission path lands the submission in
	// GRADED_PENDING_RELEASE with review_status=PENDING_REVIEW: the mandatory
	// HITL gate. That grade is PROVISIONAL — an instructor may still revise it,
	// and until they approve, nobody has awarded it. Announcing it drove FIVE
	// consumers off a number no human had seen: the learner's transcript, their
	// XP, their "your grade is ready" notification, the derived Growth-Edge
	// projector, and module progress. None of them ever heard about the
	// instructor's correction, so the transcript kept the AI's mark forever.
	//
	// It cannot simply be re-announced after approval: the outbox derives a
	// STABLE idempotency key (`<submission_id>:graded`) and that column is
	// UNIQUE, so a second graded.v1 for the same submission is silently
	// swallowed — and salting it through would re-fire all five, double-awarding
	// XP and re-notifying the learner.
	//
	// So the announcement WAITS. approveSubmissionGradingHandler emits it once
	// the human has blessed the grade, carrying their final scores.
	if sub.ReviewStatus == domain.ReviewStatusPendingReview {
		log.Printf("delivery: submission %s graded but PENDING_REVIEW — withholding submission.graded.v1 until an instructor approves (CHO-2154)", sub.ID)
		return
	}
	type publishCustomer interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (events.PublishedEvent, error)
	}
	pc, ok := s.publisher.(publishCustomer)
	if !ok {
		return
	}
	// ADR-205 WS-6 (CHO-1958): this OE-batch path holds only the Submission, so
	// it resolves the parent assessment's title by id (via the optional reader)
	// and snapshots it onto the event — closing the emit-site gap so essay/OE-
	// graded submissions also feed chora-consumption's derived Growth-Edge
	// projector. Fail-soft: no reader / lookup failure omits the title (the
	// event still publishes; consumption acks the title-less variant), never a
	// reason to NACK the grade state-transition.
	// CHO-2224: the SAME assessment lookup now feeds two enrichments — the title
	// (WS-6) and the parent Offering's delivery_type. Resolving the assessment
	// once and deriving both avoids a second round-trip for the same row.
	a := s.assessmentFor(ctx, sub.TenantID, sub.AssessmentID)
	title := ""
	if a != nil {
		title = a.Title
	}
	// delivery_type — §10.6 criterion 1 MODE attribution. The tenant must be on
	// the ctx: OfferingPort.Get reads the RLS tenant from the context, not a
	// parameter. Fail-soft by the resolver's contract: no port / freestanding
	// assessment / dead read all yield "", and the grade still publishes.
	deliveryType := domain.ResolveDeliveryType(
		tracing.WithTenantID(ctx, sub.TenantID), s.offerings, a)
	payload := events.SubmissionGradedPayload(sub, title, deliveryType, traceparent)
	_, _ = pc.PublishCustom("chora.delivery.submission.graded.v1", sub.TenantID, sub.LearnerGCID, payload)
}

// assessmentFor resolves the parent assessment (WS-6 title + CHO-2224
// delivery_type), nil-safe + fail-soft: no reader / blank id / lookup error /
// not-found all yield nil so the graded event still publishes (both enrichments
// are secondary, never a reason to NACK a grade state-transition).
func (s *GradingInboxSubscriber) assessmentFor(ctx context.Context, tenantID, assessmentID string) *domain.Assessment {
	if s.assessments == nil || strings.TrimSpace(assessmentID) == "" {
		return nil
	}
	a, ok, err := s.assessments.Get(ctx, tenantID, assessmentID)
	if err != nil || !ok {
		return nil
	}
	return a
}

// emitGradingFailed publishes chora.delivery.grading.failed.v1 via the outbox
// publisher when the OE-batch completion event arrives with batch_outcome
// != "ok" (E2E-BE-8). The submission FSM stays in PENDING_OE_GRADING but the
// terminal grading-job failure marker is now observable for O+ governance
// (IMDA D1 accountability), R+ monitoring (re-dispatch CTA), and
// Observability alerting (failure-rate spikes).
//
// Status of the job is GRADING_JOB_STATUS_FAILED (= 8 per the schema enum).
// failure_category mirrors the canonical taxonomy from
// chora-contracts/proto/events/delivery/grading.proto:
//
//	1 VERTEX_AI_5XX        / 2 MODEL_ARMOR_BLOCKED / 3 TIMEOUT /
//	4 QUOTA_EXCEEDED       / 5 INSUFFICIENT_TENANT_MANA /
//	6 INTERNAL_ERROR       (default when the orchestrator does not categorise).
//
// Today the orchestrator hop conveys batch_outcome as a free string + a free
// FailureMessage; this emitter degrades gracefully to INTERNAL_ERROR (= 6)
// when the failure category is not communicated separately. When the
// orchestrator starts attaching a typed failure_category attribute, the
// HTTP-push decoder can forward it through OEBatchCompletedPayload + this
// emitter will map it through unchanged.
func (s *GradingInboxSubscriber) emitGradingFailed(
	sub *domain.Submission,
	p OEBatchCompletedPayload,
	traceparent string,
	now time.Time,
) {
	if s == nil || s.publisher == nil || sub == nil {
		return
	}
	type publishCustomer interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (events.PublishedEvent, error)
	}
	pc, ok := s.publisher.(publishCustomer)
	if !ok {
		return
	}
	// status = GRADING_JOB_STATUS_FAILED.
	const statusFailed = int32(8)
	// failure_category default INTERNAL_ERROR (= 6) when the orchestrator
	// did not supply a typed category.
	const failureCategoryInternalError = int32(6)
	payload := map[string]any{
		"submission_id":        sub.ID,
		"assessment_id":        sub.AssessmentID,
		"learner_gcid":         sub.LearnerGCID,
		"failure_category":     failureCategoryInternalError,
		"failure_message":      p.FailureMessage,
		"status":               statusFailed,
		"oe_batch_id":          p.OEBatchID,
		"failed_at":            now.Format(time.RFC3339Nano),
		"traceparent":          traceparent,
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "runtime",
	}
	_, _ = pc.PublishCustom("chora.delivery.grading.failed.v1", sub.TenantID, sub.LearnerGCID, payload)
}

func toDomainResults(in []OEBatchGradedAnswer) []domain.OEGradingResult {
	out := make([]domain.OEGradingResult, 0, len(in))
	for _, r := range in {
		out = append(out, domain.OEGradingResult{
			TestSetQuestionID:    r.TestSetQuestionID,
			QuestionID:           r.QuestionID,
			PointsEarned:         r.PointsEarned,
			PointsPossible:       r.PointsPossible,
			CriterionScoresJSON:  r.CriterionScoresJSON,
			LLMEvaluatorFeedback: r.LLMEvaluatorFeedback,
		})
	}
	return out
}

func firstNonZero(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}
