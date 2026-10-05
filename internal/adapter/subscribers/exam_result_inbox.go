// exam_result_inbox.go - the EXAM-mode auto-cert engine (R+ Four-Mode DoD
// keystone, docs/R-PLUS-FOUR-MODE-DELIVERY-REFACTOR-2026-06-24.md §10.4 step 4).
//
// The graduate/short modes already earn a certificate on a released grade
// (completion_inbox.go). The EXAM mode had the other half of the outcome spine
// wired only up to the PUBLISH: chora-delivery emits
// chora.delivery.exam_result.released.v1 at ExamForm.Grade, but NOTHING
// consumed it, so a candidate who PASSED a revision-pinned, exposure-locked
// ExamForm minted no credential and never reached the transcript. This is that
// missing consumer.
//
// # Why this mirrors completion_inbox rather than reusing it
//
// The graduate lane rides submission.released.v1 and evaluates the OFFERING's
// CompletionPolicy (a passing PERCENT + optional all-content). An exam has no
// offering and no submission: its authoritative, legal-grade gate is the
// ExamForm's own CUT-SCORE, already applied when the ExamResult was finalised
// (ADR-190 D2 "legal/compliance-grade cut-score -> pass/fail -> certificate
// chains"). So the exam lane does NOT re-evaluate a delivery CompletionPolicy -
// the event's OUTCOME (PASS/FAIL) IS the released verdict, and PASS is the whole
// policy. See the owner-ratification note in the completion report for the
// alternative (gating on the course CertDefinition) and why it was rejected (a
// gate keyed on a column exam-only courses never set would silently never fire).
//
// # What crosses the wire vs what is loaded locally
//
// The consumer reads only OUTCOME + exam_id + candidate_ref + tenant_id from the
// event. It loads the Exam LOCALLY (intra-domain, chora_delivery) for the anchor
// COURSE + title; it does not read the raw/max/cut scores from the wire (it does
// not need them) and does not re-load the write-once ExamResult row (the event's
// outcome IS that row's verdict). No proto/schema change is forced.
//
// candidate_ref IS the learner GCID (verified: the R+ Results UI records it as a
// GCID and renders it in the .gcid slot; CandidateStore is keyed by GCID). The
// certificate anchors on Exam.CourseID for that GCID.
//
// # Release, not grade
//
// For an exam, recording the result IS its release: there is no HITL provisional
// -> released split (unlike the OE submission lane). The topic name (.released.)
// reflects that, so consuming it never leaks an outcome ahead of release.
//
// # Idempotency
//
// The inbox dedupe key guards broker RETRIES; the DURABLE, cross-pod guarantee
// that a learner never holds two certificates for one course is certifications'
// UNIQUE(course_id, gcid) (ON CONFLICT DO NOTHING -> ErrCertAlreadyIssued). This
// converges with the graduate lane: whichever lane fires first wins; the other
// is a logged no-op. A learner holds ONE credential per course.
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
	deliverydomain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// examResultInboxTTL is the dedupe-key retention for broker retries. The durable
// guarantee is certifications' UNIQUE(course_id, gcid), which holds forever and
// across pods - this only suppresses a same-message redelivery storm.
const examResultInboxTTL = 24 * time.Hour

// ExamReader loads an Exam by id (for its anchor CourseID + Title). Satisfied by
// pg.ExamRepo (prod) and inmem.ExamRepo (dev/tests) via exam.ExamStore.
//
// The error is part of the contract (CHO-2184): ok=false is a GENUINE MISS; an
// infra/RLS failure returns a non-nil error so a dead read cannot masquerade as
// an absent exam and drop a credential on the floor.
type ExamReader interface {
	Get(ctx context.Context, id string) (*exam.Exam, bool, error)
}

// ExamResultReleasedPayload is the slice of chora.delivery.exam_result.released
// .v1 the engine needs. The scores are deliberately NOT here: the verdict is the
// OUTCOME, and the anchor course is loaded from the Exam, so no sensitive score
// drives issuance and no proto widening is forced.
type ExamResultReleasedPayload struct {
	// ResultID is the durable ExamResult row id (correlation only).
	ResultID string
	// ExamID resolves the anchor Course (Exam.CourseID) + the credential title.
	ExamID string
	// ExamFormID is carried for logging/audit correlation only.
	ExamFormID string
	// CandidateGCID is candidate_ref on the wire, which IS the learner GCID.
	CandidateGCID string
	// Outcome is the released verdict ("PASS"/"FAIL"). Only PASS certifies.
	Outcome string
	// TenantID scopes the RLS write.
	TenantID string
}

// ExamResultCertSubscriber issues a durable Certification for a PASSED exam
// result, anchored on the exam's course, then relies on the existing
// certification.issued.v1 -> chora-consumption transcript projector for rollup.
type ExamResultCertSubscriber struct {
	exams     ExamReader
	certs     deliverydomain.CertificationStore
	publisher events.Publisher
	inbox     idempotent.Store
}

// NewExamResultCertSubscriber constructs the engine. A nil inbox falls back to
// an in-process memory store (broker-retry dedupe only; the durable guarantee is
// the DB UNIQUE constraint).
func NewExamResultCertSubscriber(
	exams ExamReader,
	certs deliverydomain.CertificationStore,
	publisher events.Publisher,
	inbox idempotent.Store,
) *ExamResultCertSubscriber {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &ExamResultCertSubscriber{exams: exams, certs: certs, publisher: publisher, inbox: inbox}
}

// SubscribedTopic reports the topic this subscriber rides.
func (s *ExamResultCertSubscriber) SubscribedTopic() string { return events.TopicExamResultReleased }

// Handle evaluates one released exam result and issues a certificate on PASS.
//
// Every outcome is LOGGED with its reason: a candidate who does not receive a
// certificate leaves a trail saying exactly why. Silence is precisely what let
// the graduate-lane cert outage sit unnoticed.
func (s *ExamResultCertSubscriber) Handle(ctx context.Context, env events.EventEnvelope, p ExamResultReleasedPayload) error {
	if s == nil || s.exams == nil || s.certs == nil {
		return errors.New("subscribers: exam-result cert engine not wired")
	}
	tenantID := strings.TrimSpace(p.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(env.TenantID)
	}
	if tenantID == "" {
		return errors.New("subscribers: exam-result inbox tenant_id required")
	}
	if strings.TrimSpace(p.ExamID) == "" {
		return errors.New("subscribers: exam-result inbox exam_id required")
	}

	// Put the tenant on the CONTEXT before any read. rls.ApplySession reads it
	// from there, not from the SQL args - and ExamRepo.Get takes no tenantID
	// argument at all, so the context is its ONLY source. A Pub/Sub push handler
	// hands us a BARE ctx, so without this line every exam lookup fails RLS
	// (CHO-2184: a swallowed RLS error once ate a certificate).
	ctx = tracing.WithTenantID(ctx, tenantID)

	return s.inbox.Process(ctx, "exam-result:"+env.EventID, examResultInboxTTL, func() error {
		// Only PASS earns a credential. A FAIL is a valid terminal outcome, not
		// an error: ack it (retrying will never turn a fail into a pass), and a
		// phantom certificate on a failed high-stakes exam is the worst defect.
		if !strings.EqualFold(strings.TrimSpace(p.Outcome), string(exam.OutcomePass)) {
			log.Printf("exam-cert: no certificate for candidate=%s exam=%s result=%s - outcome=%q (only PASS certifies)",
				p.CandidateGCID, p.ExamID, p.ResultID, p.Outcome)
			return nil
		}

		gcid := strings.TrimSpace(p.CandidateGCID)
		if gcid == "" {
			log.Printf("exam-cert: exam=%s result=%s released PASS but candidate_ref is empty - cannot anchor a learner", p.ExamID, p.ResultID)
			return nil
		}

		// Anchor course: load the Exam locally (intra-domain; no cross-DB read,
		// nothing sensitive on the wire). An infra/RLS failure must NACK
		// (retry -> DLQ); a genuine miss is safe to ack (CHO-2184).
		ex, ok, err := s.exams.Get(ctx, p.ExamID)
		if err != nil {
			return fmt.Errorf("subscribers: get exam %s: %w", p.ExamID, err)
		}
		if !ok || ex == nil {
			log.Printf("exam-cert: exam %s not found for result %s - no course to anchor a certificate", p.ExamID, p.ResultID)
			return nil
		}
		courseID := strings.TrimSpace(ex.CourseID)
		if courseID == "" {
			log.Printf("exam-cert: exam %s carries no course - cannot anchor a certificate", ex.ID)
			return nil
		}

		// An exam credential is PASS/FAIL, not a percentage: score stays nil (the
		// durable ExamResult row holds the raw/max for the record). The human-
		// facing name is the exam title.
		cert, err := s.certs.IssueCtx(ctx, tenantID, gcid, courseID, nil, examCertAccomplishments(ex))
		if err != nil {
			if errors.Is(err, deliverydomain.ErrCertAlreadyIssued) {
				// A redelivered release, or the graduate lane / a manual issue
				// beat us to this (course, learner). The DB refused the duplicate,
				// exactly as intended.
				log.Printf("exam-cert: learner=%s already holds a certificate for course=%s - no duplicate issued", gcid, courseID)
				return nil
			}
			// A fault in the ROW cannot be fixed by sending the row again. Ack it
			// so the DLQ keeps meaning "replayable", but shout: a credential the
			// candidate EARNED is not being minted, and only this log says so.
			if pgErr, permanent := permanentPGDataFault(err); permanent {
				log.Printf("exam-cert: PERMANENT DATA FAULT - certificate NOT issued, NOT retried (acked) - "+
					"learner=%s course_id=%q exam=%s result=%s form=%s: postgres refused the row with SQLSTATE %s: %s. "+
					"The anchor course_id is the usual culprit: certifications.course_id is UUID NOT NULL, so a "+
					"non-UUID course on the exam (a placeholder such as \"course-cspo\") can never insert, and every "+
					"redelivery would carry the same value into the same refusal. ACTION: fix exam %s's course_id, "+
					"then re-issue this candidate's credential - it will NOT arrive on its own.",
					gcid, courseID, ex.ID, p.ResultID, p.ExamFormID, pgErr.Code, pgErr.Message, ex.ID)
				return nil
			}
			return fmt.Errorf("subscribers: issue certification learner=%s course=%s: %w", gcid, courseID, err)
		}

		log.Printf("exam-cert: ISSUED certificate %s to learner=%s course=%s from exam=%s (result=%s, form=%s)",
			cert.ID, cert.LearnerID, cert.CourseID, ex.ID, p.ResultID, p.ExamFormID)
		s.emitCertificationIssued(cert, env.Traceparent)
		return nil
	})
}

// emitCertificationIssued publishes certification.issued.v1 - the event
// chora-consumption already projects onto the learner's transcript
// (kind=certification). That link was never broken; only the exam trigger was.
func (s *ExamResultCertSubscriber) emitCertificationIssued(cert *deliverydomain.Certification, traceparent string) {
	if s.publisher == nil || cert == nil {
		return
	}
	if _, err := s.publisher.PublishCertificationIssued(events.CertificationIssued{
		TenantID:        cert.TenantID,
		GCID:            cert.LearnerID,
		CertificationID: cert.ID,
		CourseID:        cert.CourseID,
		LearnerGCID:     cert.LearnerID,
		Hash:            cert.Hash,
		Traceparent:     traceparent,
	}); err != nil {
		// The certificate IS issued and durable; only its announcement failed.
		// Loud, and NOT a NACK - a retry would re-run the whole handler and the
		// DB would (correctly) refuse the duplicate, so the event would never be
		// re-emitted anyway. Surface it rather than pretend.
		log.Printf("exam-cert: certificate %s issued but certification.issued.v1 FAILED to publish: %v - "+
			"the learner holds the credential but their transcript will not show it", cert.ID, err)
	}
}

// examCertAccomplishments is the label list stamped on an exam-issued
// certificate. The exam title is the human-facing name of the award; an empty
// title falls back to a neutral phrase so a certificate never renders blank.
func examCertAccomplishments(ex *exam.Exam) []string {
	if ex == nil {
		return []string{"Passed a proctored exam"}
	}
	title := strings.TrimSpace(ex.Title)
	if title == "" {
		return []string{"Passed a proctored exam"}
	}
	return []string{"Passed: " + title}
}
