// completion_inbox.go — the auto-issue engine (CHO-2157).
//
// The CompletionPolicy editor shipped and persisted, and NOTHING consumed it.
// Learner L passed at 95% against a live {AwardsCertificate: true,
// PassingScorePct: 70} policy and received no certificate; the certifications
// table was empty tenant-wide. Every downstream link already worked — a manual
// issue mints a certificate AND rolls it up to the learner's transcript — so the
// ONLY missing piece was the trigger. This is that trigger.
//
// It rides chora.delivery.submission.released.v1, a topic chora-delivery already
// publishes per-submission and which, until now, NOTHING consumed. RELEASE (not
// approve) is the right moment: a certificate issued before results are released
// would leak the outcome to the learner ahead of their instructor.
//
// Everything it reads — submissions, assessments, offerings, courses, module
// progress, certifications — lives in chora_delivery. This is an intra-domain
// read, so no cross-DB query and no cross-domain event is involved
// (ddd-enforcement #1).
//
// Idempotency is the DATABASE's: certifications carries UNIQUE (course_id, gcid),
// so a redelivered release cannot mint a second credential — a guarantee an
// in-memory registry could never make (see pg.CertificationRepo).
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

// TopicSubmissionReleased is the released-grade topic the engine rides.
const TopicSubmissionReleased = "chora.delivery.submission.released.v1"

// completionInboxTTL is the dedupe-key retention for the inbox. It only guards
// against broker RETRIES of the same message; the durable guarantee that a
// learner never receives two certificates is certifications' UNIQUE
// (course_id, gcid), which holds forever and across pods.
const completionInboxTTL = 24 * time.Hour

// SubmissionReleasedPayload is the slice of submission.released.v1 the engine
// needs. The scores are NOT on the wire — the engine loads the submission from
// chora_delivery, which owns it, rather than widening a Schema-Registry proto
// (and risking the additive-field dead-letter trap that killed graded.v1 for
// three weeks).
type SubmissionReleasedPayload struct {
	SubmissionID string
	AssessmentID string
	LearnerGCID  string
	TenantID     string
}

// OfferingReader loads an Offering (for its CompletionPolicy).
//
// The error is part of the contract (CHO-2184). Without it, an RLS/infra
// failure arrived here as ok=false — indistinguishable from an absent offering
// — and the engine ACKed the event and dropped the certificate on the floor.
type OfferingReader interface {
	Get(ctx context.Context, id string) (*domain.Offering, bool, error)
}

// CourseCertReader reads a course's cert definition — cert_require_all_content
// in particular.
type CourseCertReader interface {
	CertDefinition(ctx context.Context, tenantID, courseID string) (enabled bool, requireAllContent bool, ok bool, err error)
}

// ContentCompletionReader reports whether a learner has completed every content
// component a course declares.
//
// A course that declares NO components has nothing to complete, so it reports
// true — the requirement is vacuous, not unmet. Reporting false there would deny
// a certificate to every learner on every course without modules.
type ContentCompletionReader interface {
	AllContentComplete(ctx context.Context, tenantID, courseID, learnerGCID string) (bool, error)
}

// AssessmentOfferingReader resolves an assessment's parent offering.
type AssessmentOfferingReader interface {
	Get(ctx context.Context, tenantID, id string) (*domain.Assessment, bool, error)
}

// ComponentCompletionReader resolves which of an offering's DECLARED components
// (ADR-190 D1, CHO-2222) a learner has actually completed. Satisfied by
// pg.ComponentCompletionRepo, which reads submissions + exam_results - both in
// chora_delivery, so the resolution is intra-domain (ddd-enforcement #1).
//
// It takes the declared set rather than returning everything a learner has ever
// done: the gate only asks about declared components, and passing the set in is
// what lets the adapter refuse an unresolvable kind LOUDLY.
//
// The error is part of the contract, for the same reason OfferingReader's is
// (CHO-2184): a (T, bool) port would report an infra/RLS failure as "nothing
// completed", which is indistinguishable from a learner who has done nothing -
// so the gate would fail CLOSED in silence and withhold an EARNED certificate
// while looking perfectly healthy.
type ComponentCompletionReader interface {
	CompletedComponents(ctx context.Context, tenantID, learnerGCID string, declared []domain.CompletionComponent) ([]domain.CompletionComponent, error)
}

// CompletionSubscriber issues certificates for released grades that satisfy
// their offering's CompletionPolicy.
type CompletionSubscriber struct {
	submissions domain.SubmissionRepo
	assessments AssessmentOfferingReader
	offerings   OfferingReader
	courses     CourseCertReader
	content     ContentCompletionReader
	components  ComponentCompletionReader
	certs       domain.CertificationStore
	publisher   events.Publisher
	inbox       idempotent.Store
}

// NewCompletionSubscriber constructs the engine.
//
// components may be nil ONLY where no offering declares any (it is consulted
// lazily, and an offering that declares nothing never reaches it). If one does
// declare and this is nil, Handle fails LOUD rather than guess - see the call
// site.
func NewCompletionSubscriber(
	submissions domain.SubmissionRepo,
	assessments AssessmentOfferingReader,
	offerings OfferingReader,
	courses CourseCertReader,
	content ContentCompletionReader,
	components ComponentCompletionReader,
	certs domain.CertificationStore,
	publisher events.Publisher,
	inbox idempotent.Store,
) *CompletionSubscriber {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &CompletionSubscriber{
		submissions: submissions,
		assessments: assessments,
		offerings:   offerings,
		courses:     courses,
		content:     content,
		components:  components,
		certs:       certs,
		publisher:   publisher,
		inbox:       inbox,
	}
}

// SubscribedTopic reports the topic this subscriber rides.
func (s *CompletionSubscriber) SubscribedTopic() string { return TopicSubmissionReleased }

// Handle evaluates one released grade against its offering's CompletionPolicy
// and issues a certificate when the policy is satisfied.
//
// Every outcome is LOGGED with its reason. A learner who does not receive a
// certificate leaves a trail saying exactly why — the previous behaviour
// (silence) is precisely what let this defect sit unnoticed: nothing fired, and
// nothing said so.
func (s *CompletionSubscriber) Handle(ctx context.Context, env events.EventEnvelope, p SubmissionReleasedPayload) error {
	if s == nil || s.submissions == nil || s.certs == nil {
		return errors.New("subscribers: completion engine not wired")
	}
	tenantID := strings.TrimSpace(p.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(env.TenantID)
	}
	if tenantID == "" {
		return errors.New("subscribers: completion inbox tenant_id required")
	}
	if strings.TrimSpace(p.SubmissionID) == "" {
		return errors.New("subscribers: completion inbox submission_id required")
	}

	// Put the tenant on the CONTEXT before any read. rls.ApplySession reads it
	// from there, not from the SQL args — and OfferingRepo.Get takes no tenantID
	// argument at all, so the context is its ONLY source. A Pub/Sub push handler
	// hands us a BARE ctx, so without this line every offering lookup fails RLS.
	//
	// This line is the FIX for the certificate outage; it is no longer the only
	// thing standing between us and it. OfferingRepo.Get used to swallow the RLS
	// error into (nil, false), so the failure arrived as "offering not found" and
	// the engine ACKed the event and withheld the certificate while looking
	// perfectly healthy. The repo now returns that error and the read below NACKs
	// on it (CHO-2184), so a regression here fails loud instead of going quiet.
	//
	// Caught by the live walk, on the second occurrence of this trap in one day.
	// See reusable_gotcha_integration_test_builds_its_own_rls_context.
	ctx = tracing.WithTenantID(ctx, tenantID)

	return s.inbox.Process(ctx, "completion:"+env.EventID, completionInboxTTL, func() error {
		sub, ok, err := s.submissions.Get(ctx, tenantID, p.SubmissionID)
		if err != nil {
			return fmt.Errorf("subscribers: get submission %s: %w", p.SubmissionID, err)
		}
		if !ok || sub == nil {
			// CHO-2349: this was the ONE early return in the engine that said
			// nothing. Every sibling below logs, so a withheld certificate is
			// normally explicable; a silent drop here looked identical to the
			// engine never running at all, and that ambiguity cost a whole
			// diagnosis pass on the live walk. Ack (a resend cannot conjure the
			// row) but never quietly.
			log.Printf("completion: submission %s not found in tenant %s - dropping the released event; "+
				"no certificate can be evaluated for it", p.SubmissionID, tenantID)
			return nil
		}

		// The offering carries the policy; the assessment carries the offering.
		assessmentID := firstNonEmpty(p.AssessmentID, sub.AssessmentID)
		a, ok, err := s.assessments.Get(ctx, tenantID, assessmentID)
		if err != nil {
			return fmt.Errorf("subscribers: get assessment %s: %w", assessmentID, err)
		}
		if !ok || a == nil || strings.TrimSpace(a.OfferingID) == "" {
			log.Printf("completion: submission %s released but its assessment has no offering — no policy to evaluate", sub.ID)
			return nil
		}
		// An infra/RLS failure must NACK (retry → DLQ), never ack-and-drop: a
		// dropped event here is a certificate the learner never receives and no
		// one ever hears about. Only a genuine miss is safe to ack (CHO-2184).
		off, ok, err := s.offerings.Get(ctx, a.OfferingID)
		if err != nil {
			return fmt.Errorf("subscribers: get offering %s: %w", a.OfferingID, err)
		}
		if !ok || off == nil {
			log.Printf("completion: offering %s not found for assessment %s", a.OfferingID, a.ID)
			return nil
		}

		// A certificate is anchored on a COURSE (certifications is
		// UNIQUE(course_id, gcid)). An offering may span several — offering
		// 019f3c77 spans two live — so the PRIMARY course anchors the award; the
		// human-facing name comes from the offering's own CertTitle regardless.
		courseID := ""
		if len(off.CourseIDs) > 0 {
			courseID = strings.TrimSpace(off.CourseIDs[0])
		}
		if courseID == "" {
			log.Printf("completion: offering %s carries no course — cannot anchor a certificate", off.ID)
			return nil
		}

		// The course may additionally require that all declared content is done.
		requireAllContent, allComplete := false, true
		if s.courses != nil {
			if _, ract, found, cerr := s.courses.CertDefinition(ctx, tenantID, courseID); cerr != nil {
				return fmt.Errorf("subscribers: course cert definition %s: %w", courseID, cerr)
			} else if found {
				requireAllContent = ract
			}
		}
		if requireAllContent && s.content != nil {
			done, cerr := s.content.AllContentComplete(ctx, tenantID, courseID, sub.LearnerGCID)
			if cerr != nil {
				return fmt.Errorf("subscribers: content completion %s/%s: %w", courseID, sub.LearnerGCID, cerr)
			}
			allComplete = done
		}

		// The offering's DECLARED components (ADR-190 D1, CHO-2222). The
		// sequencing constraint recorded here in sub-phase 1 is now DISCHARGED:
		// the resolver below and the editor that lets an admin declare
		// components landed together, so a declared component is resolved rather
		// than permanently unsatisfied.
		//
		// Resolved ONLY when something is declared: an offering that declares
		// nothing is vacuously satisfied (the live shape of every offering
		// today), and must not pay for a query to learn that.
		var completedComponents []domain.CompletionComponent
		if off.CompletionRequirement != nil && len(off.CompletionRequirement.Components) > 0 {
			if s.components == nil {
				// FAIL LOUD. The two silent alternatives are both defects: fail
				// OPEN certifies without honouring a declaration the admin made,
				// and fail CLOSED withholds every certificate on this offering
				// with a reason that reads like policy rather than a wiring bug.
				return fmt.Errorf("subscribers: offering %s declares %d completion component(s) but no "+
					"component-completion reader is wired - refusing to decide (a certificate issued "+
					"without checking the declaration, or withheld without being able to check it, are "+
					"both worse than a NACK)", off.ID, len(off.CompletionRequirement.Components))
			}
			// ctx carries the tenant (set above): the pg resolver reads it from
			// there via rls.ApplySession, NOT from the tenantID argument.
			completedComponents, err = s.components.CompletedComponents(ctx, tenantID, sub.LearnerGCID, off.CompletionRequirement.Components)
			if err != nil {
				// NACK (retry → DLQ). An infra failure must never arrive at the
				// gate as "not complete" (CHO-2184).
				return fmt.Errorf("subscribers: resolve declared components for learner=%s offering=%s: %w",
					sub.LearnerGCID, off.ID, err)
			}
		}

		outcome := domain.EvaluateCertificate(off.CompletionPolicy, off.CompletionRequirement, domain.CompletionFacts{
			ScorePercent:        sub.ScorePercent(),
			RequireAllContent:   requireAllContent,
			AllContentComplete:  allComplete,
			CompletedComponents: completedComponents,
		})
		if !outcome.Issue {
			log.Printf("completion: no certificate for learner=%s course=%s submission=%s — %s (score=%.1f%%)",
				sub.LearnerGCID, courseID, sub.ID, outcome.Reason, sub.ScorePercent())
			return nil
		}

		score := int(sub.ScorePercent() + 0.5) // certifications.score is SMALLINT 0..100
		cert, err := s.certs.IssueCtx(ctx, tenantID, sub.LearnerGCID, courseID, &score,
			domain.CertificateAccomplishments(off.CompletionPolicy))
		if err != nil {
			if errors.Is(err, domain.ErrCertAlreadyIssued) {
				// A redelivered release, or a manual issue that beat us to it.
				// The DB refused the duplicate — exactly as intended.
				log.Printf("completion: learner=%s already holds a certificate for course=%s — no duplicate issued",
					sub.LearnerGCID, courseID)
				return nil
			}
			// Sending the same row again cannot fix the row (see pg_error_class.go).
			// Ack so the DLQ stays replayable, but shout: a credential the learner
			// EARNED is not being minted, and only this log records that.
			if pgErr, permanent := permanentPGDataFault(err); permanent {
				log.Printf("completion: PERMANENT DATA FAULT - certificate NOT issued, NOT retried (acked) - "+
					"learner=%s course_id=%q submission=%s: postgres refused the row with SQLSTATE %s: %s. "+
					"certifications.course_id is UUID NOT NULL, so a non-UUID course anchor can never insert and "+
					"every redelivery would carry the same value into the same refusal. ACTION: fix the offering's "+
					"course_id, then re-issue this learner's credential - it will NOT arrive on its own.",
					sub.LearnerGCID, courseID, sub.ID, pgErr.Code, pgErr.Message)
				return nil
			}
			return fmt.Errorf("subscribers: issue certification learner=%s course=%s: %w", sub.LearnerGCID, courseID, err)
		}

		log.Printf("completion: ISSUED certificate %s to learner=%s course=%s (score=%d%%, policy cut-score=%d%%)",
			cert.ID, cert.LearnerID, cert.CourseID, score, off.CompletionPolicy.PassingScorePct)
		s.emitCertificationIssued(cert, env.Traceparent)
		return nil
	})
}

// emitCertificationIssued publishes certification.issued.v1 — the event
// chora-consumption already projects onto the learner's transcript
// (kind=certification). That link was never broken; only the trigger was missing.
func (s *CompletionSubscriber) emitCertificationIssued(cert *domain.Certification, traceparent string) {
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
		// Loud, and NOT a NACK — a retry would re-run the whole handler and the
		// DB would (correctly) refuse the duplicate, so the event would never be
		// re-emitted anyway. Surface it rather than pretend.
		log.Printf("completion: certificate %s issued but certification.issued.v1 FAILED to publish: %v — "+
			"the learner holds the credential but their transcript will not show it", cert.ID, err)
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
