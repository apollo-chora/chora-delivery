// exam_result_inbox_test.go - the EXAM-mode auto-cert engine (R+ Four-Mode DoD
// §10.4 step 4: cut-score -> pass/fail -> ResultRelease -> cert -> transcript).
//
// The live shape reproduced: a candidate PASSES a revision-pinned ExamForm; the
// finalise path publishes chora.delivery.exam_result.released.v1; and until this
// engine existed NOTHING consumed it, so a passed high-stakes exam minted no
// credential and never reached the learner's transcript. This is that trigger,
// mirroring the graduate/short-mode completion engine (completion_inbox.go).
package subscribers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	deliverydomain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	erTenant    = "11111111-1111-7111-8111-111111111111"
	erLearner   = "00000000-0000-7000-8000-000000003777"
	erCourse    = "019edf3e-b405-707a-b3ef-851fd4e38e78"
	erExam      = "019f6a01-1111-7aaa-8bbb-000000000001"
	erExamForm  = "019f6a02-2222-7aaa-8bbb-000000000002"
	erResult    = "019f6a03-3333-7aaa-8bbb-000000000003"
	erExamTitle = "AWS Solutions Architect Proctored Exam"
)

// stubExams mimics pg.ExamRepo: Get takes NO tenantID argument and reads the
// tenant from the CONTEXT alone (rls.ApplySession), so it REFUSES an untenanted
// context exactly as the real repo does (CHO-2184). Modelling an RLS failure as
// a plain miss would make this stub the very swallow it guards against, so an
// untenanted read returns rls.ErrNoTenantContext, and an infra outage returns
// the injected err - never a silent (nil,false).
type stubExams struct {
	ex  *exam.Exam
	err error // when set, every read fails - the infra-outage case
}

func (s stubExams) Get(ctx context.Context, _ string) (*exam.Exam, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	if tracing.TenantIDFromContext(ctx) == "" {
		return nil, false, rls.ErrNoTenantContext
	}
	if s.ex == nil {
		return nil, false, nil
	}
	return s.ex, true, nil
}

func newExam(courseID string) *exam.Exam {
	return &exam.Exam{ID: erExam, TenantID: erTenant, CourseID: courseID, Title: erExamTitle}
}

func newExamResultWorld(t *testing.T, exams stubExams) (*subscribers.ExamResultCertSubscriber, *deliverydomain.CertificationRegistry, *events.InMemoryPublisher) {
	t.Helper()
	certs := deliverydomain.NewCertificationRegistry()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	sub := subscribers.NewExamResultCertSubscriber(exams, certs, pub, nil)
	return sub, certs, pub
}

func passEnv() events.EventEnvelope {
	return events.EventEnvelope{EventID: "evt-exam-pass-1", TenantID: erTenant, Traceparent: "00-trace-parent-01"}
}

func passPayload(outcome string) subscribers.ExamResultReleasedPayload {
	return subscribers.ExamResultReleasedPayload{
		ResultID:      erResult,
		ExamID:        erExam,
		ExamFormID:    erExamForm,
		CandidateGCID: erLearner,
		Outcome:       outcome,
		TenantID:      erTenant,
	}
}

// TestExamResultCert_Pass_IssuesCertificateAnchoredOnExamCourse - the golden
// path: a PASS anchors a durable Certification on the EXAM's course for the
// candidate GCID, and emits certification.issued.v1 for the transcript rollup.
func TestExamResultCert_Pass_IssuesCertificateAnchoredOnExamCourse(t *testing.T) {
	sub, certs, pub := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})

	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("Handle PASS: unexpected error %v", err)
	}

	cert, ok, err := certs.GetByLearnerCourseCtx(context.Background(), erTenant, erLearner, erCourse)
	if err != nil || !ok || cert == nil {
		t.Fatalf("expected a certificate for learner=%s course=%s; ok=%v err=%v", erLearner, erCourse, ok, err)
	}
	if len(cert.Accomplishments) == 0 || cert.Accomplishments[0] != "Passed: "+erExamTitle {
		t.Fatalf("expected accomplishment %q; got %v", "Passed: "+erExamTitle, cert.Accomplishments)
	}
	// The transcript rollup depends on certification.issued.v1 being emitted.
	if !hasCertIssued(pub) {
		t.Fatalf("expected certification.issued.v1 to be published for the transcript rollup")
	}
}

// TestExamResultCert_Fail_IssuesNothing - a FAIL earns no credential and is a
// valid terminal outcome (ack), not an error. A phantom cert on a failed
// high-stakes exam is the worst possible defect.
func TestExamResultCert_Fail_IssuesNothing(t *testing.T) {
	sub, certs, pub := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})

	if err := sub.Handle(context.Background(), passEnv(), passPayload("FAIL")); err != nil {
		t.Fatalf("Handle FAIL: expected ack (nil), got %v", err)
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(context.Background(), erTenant, erLearner, erCourse); ok {
		t.Fatalf("a FAIL must not issue a certificate")
	}
	if hasCertIssued(pub) {
		t.Fatalf("a FAIL must not publish certification.issued.v1")
	}
}

// TestExamResultCert_Idempotent_NoDoubleCert - a redelivered PASS must not mint
// a second credential. The durable guarantee is UNIQUE(course_id, gcid); here
// the registry models it (ErrCertAlreadyIssued -> ack).
func TestExamResultCert_Idempotent_NoDoubleCert(t *testing.T) {
	sub, certs, _ := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})

	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("first PASS: %v", err)
	}
	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("redelivered PASS must ack, not error: %v", err)
	}
	list, err := certs.ListByTenantCtx(context.Background(), erTenant, erLearner, erCourse)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one certificate after a redelivery; got %d", len(list))
	}
}

// TestExamResultCert_ExamLookupError_NACKs - an infra/RLS failure loading the
// exam is NOT an absent exam: it must NACK (retry -> DLQ), never ack-and-drop a
// credential the candidate earned. (CHO-2184 - the swallow that ate a cert.)
func TestExamResultCert_ExamLookupError_NACKs(t *testing.T) {
	sub, certs, _ := newExamResultWorld(t, stubExams{err: errors.New("connection refused")})

	err := sub.Handle(context.Background(), passEnv(), passPayload("PASS"))
	if err == nil {
		t.Fatalf("an exam-lookup failure must NACK, not swallow into a silent ack")
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(context.Background(), erTenant, erLearner, erCourse); ok {
		t.Fatalf("no certificate must be issued when the exam load failed")
	}
}

// TestExamResultCert_MissingExam_AcksDrop - a released result whose exam is
// genuinely absent cannot be anchored on a course; ack-and-drop with a loud log
// (retrying will never conjure the exam).
func TestExamResultCert_MissingExam_AcksDrop(t *testing.T) {
	sub, certs, _ := newExamResultWorld(t, stubExams{ex: nil})

	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("a genuinely-absent exam must ack-drop, not error: %v", err)
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(context.Background(), erTenant, erLearner, erCourse); ok {
		t.Fatalf("no certificate without an exam to anchor")
	}
}

// TestExamResultCert_ExamWithoutCourse_AcksDrop - an exam carrying no course has
// nothing to anchor a certificate on; loud log + ack.
func TestExamResultCert_ExamWithoutCourse_AcksDrop(t *testing.T) {
	sub, certs, _ := newExamResultWorld(t, stubExams{ex: newExam("")})

	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("exam-without-course must ack-drop, not error: %v", err)
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(context.Background(), erTenant, erLearner, erCourse); ok {
		t.Fatalf("no certificate without a course to anchor")
	}
}

// TestExamResultCert_MissingTenant_FailsLoud - a released result with no tenant
// cannot be RLS-scoped; refuse loudly rather than write outside a tenant.
func TestExamResultCert_MissingTenant_FailsLoud(t *testing.T) {
	sub, _, _ := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})

	p := passPayload("PASS")
	p.TenantID = ""
	if err := sub.Handle(context.Background(), events.EventEnvelope{EventID: "e"}, p); err == nil {
		t.Fatalf("missing tenant must fail loud")
	}
}

// TestExamResultCert_SubscribedTopic - the engine rides the finalised-exam-result
// outcome feed.
func TestExamResultCert_SubscribedTopic(t *testing.T) {
	sub, _, _ := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})
	if got := sub.SubscribedTopic(); got != events.TopicExamResultReleased {
		t.Fatalf("SubscribedTopic = %q; want %q", got, events.TopicExamResultReleased)
	}
}

// TestExamResultCert_EmptyCandidate_AcksDrop - a PASS with no candidate_ref has
// no learner to anchor; loud log + ack, no cert.
func TestExamResultCert_EmptyCandidate_AcksDrop(t *testing.T) {
	sub, certs, _ := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})
	p := passPayload("PASS")
	p.CandidateGCID = ""
	if err := sub.Handle(context.Background(), passEnv(), p); err != nil {
		t.Fatalf("empty candidate must ack-drop, not error: %v", err)
	}
	if list, _ := certs.ListByTenantCtx(context.Background(), erTenant, "", erCourse); len(list) != 0 {
		t.Fatalf("no certificate without a candidate to anchor; got %d", len(list))
	}
}

// TestExamResultCert_EmptyExamTitle_NeutralAccomplishment - an exam with a course
// but no title still certifies, with a neutral (never-blank) accomplishment.
func TestExamResultCert_EmptyExamTitle_NeutralAccomplishment(t *testing.T) {
	ex := newExam(erCourse)
	ex.Title = ""
	sub, certs, _ := newExamResultWorld(t, stubExams{ex: ex})
	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("Handle PASS: %v", err)
	}
	cert, ok, err := certs.GetByLearnerCourseCtx(context.Background(), erTenant, erLearner, erCourse)
	if err != nil || !ok {
		t.Fatalf("expected a certificate; ok=%v err=%v", ok, err)
	}
	if len(cert.Accomplishments) == 0 || cert.Accomplishments[0] != "Passed a proctored exam" {
		t.Fatalf("expected neutral accomplishment; got %v", cert.Accomplishments)
	}
}

// TestExamResultCert_NilPublisher_StillIssues - the certificate is durable even
// when no publisher is wired (the announcement is best-effort, the credential is
// not). No panic.
func TestExamResultCert_NilPublisher_StillIssues(t *testing.T) {
	certs := deliverydomain.NewCertificationRegistry()
	sub := subscribers.NewExamResultCertSubscriber(stubExams{ex: newExam(erCourse)}, certs, nil, nil)
	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("Handle PASS with nil publisher: %v", err)
	}
	if _, ok, _ := certs.GetByLearnerCourseCtx(context.Background(), erTenant, erLearner, erCourse); !ok {
		t.Fatalf("the certificate must be issued even without a publisher")
	}
}

// TestExamResultCert_NotWired_FailsLoud - a subscriber missing its exam reader
// refuses loudly rather than silently dropping the credential.
func TestExamResultCert_NotWired_FailsLoud(t *testing.T) {
	sub := subscribers.NewExamResultCertSubscriber(nil, deliverydomain.NewCertificationRegistry(), nil, nil)
	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err == nil {
		t.Fatalf("an unwired engine must fail loud")
	}
}

// -----------------------------------------------------------------------------
// Permanent vs transient classification of the cert-issue failure
// -----------------------------------------------------------------------------

// stubCerts is a CertificationStore whose IssueCtx fails with an injected error
// - the seam the live 22P02 arrived through. It counts calls so a test can prove
// an ack really did stop the retry loop rather than merely surviving one pass.
type stubCerts struct {
	issueErr error
	calls    int
}

func (s *stubCerts) IssueCtx(_ context.Context, _, _, _ string, _ *int, _ []string) (*deliverydomain.Certification, error) {
	s.calls++
	return nil, s.issueErr
}

func (s *stubCerts) GetCtx(_ context.Context, _, _ string) (*deliverydomain.Certification, bool, error) {
	return nil, false, nil
}

func (s *stubCerts) GetByLearnerCourseCtx(_ context.Context, _, _, _ string) (*deliverydomain.Certification, bool, error) {
	return nil, false, nil
}

func (s *stubCerts) ListByTenantCtx(_ context.Context, _, _, _ string) ([]*deliverydomain.Certification, error) {
	return nil, nil
}

// newExamResultWorldWithCerts wires the engine over an injectable cert store.
func newExamResultWorldWithCerts(t *testing.T, certs deliverydomain.CertificationStore) *subscribers.ExamResultCertSubscriber {
	t.Helper()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	return subscribers.NewExamResultCertSubscriber(stubExams{ex: newExam(erCourse)}, certs, pub, nil)
}

// TestExamResultCert_PermanentPGDataFault_Acks - the LIVE finding. A fixture
// exam carried CourseID "course-cspo", so the cert INSERT died with SQLSTATE
// 22P02 (invalid input syntax for type uuid). That error was returned -> NACK ->
// 5 retries -> DLQ. A data fault in the ROW being written can never succeed on
// redelivery: the identical bytes arrive and Postgres refuses them identically.
// Retrying only burns the DLQ, so the engine must ACK (nil) and log LOUD.
//
// The error is WRAPPED, exactly as the real repo wraps it - a type assertion
// would miss it, so the classifier must use errors.As.
func TestExamResultCert_PermanentPGDataFault_Acks(t *testing.T) {
	certs := &stubCerts{issueErr: fmt.Errorf("pg: insert certification: %w", &pgconn.PgError{
		Severity: "ERROR",
		Code:     "22P02",
		Message:  `invalid input syntax for type uuid: "course-cspo"`,
	})}
	sub := newExamResultWorldWithCerts(t, certs)

	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("a permanent 22P02 data fault must ACK (nil) - retrying it can only fill the DLQ; got %v", err)
	}
	if certs.calls != 1 {
		t.Fatalf("expected exactly 1 issue attempt; got %d", certs.calls)
	}
}

// TestExamResultCert_TransientPGFaults_NACK - a genuine infra fault MIGHT
// succeed on redelivery, so it must keep NACKing. Acking any of these would
// silently destroy a credential the candidate earned (the CHO-2184 swallow).
func TestExamResultCert_TransientPGFaults_NACK(t *testing.T) {
	cases := []struct {
		name string
		code string
		msg  string
	}{
		{"connection exception (class 08)", "08006", "connection failure"},
		{"serialization failure (class 40)", "40001", "could not serialize access due to concurrent update"},
		{"deadlock detected (class 40)", "40P01", "deadlock detected"},
		{"insufficient resources (class 53)", "53300", "too many connections for role"},
		{"operator intervention (class 57)", "57014", "canceling statement due to statement timeout"},
		{"admin shutdown (class 57)", "57P01", "terminating connection due to administrator command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			certs := &stubCerts{issueErr: fmt.Errorf("pg: insert certification: %w", &pgconn.PgError{
				Severity: "ERROR", Code: tc.code, Message: tc.msg,
			})}
			sub := newExamResultWorldWithCerts(t, certs)

			err := sub.Handle(context.Background(), passEnv(), passPayload("PASS"))
			if err == nil {
				t.Fatalf("SQLSTATE %s (%s) is transient and MUST NACK; acking it destroys an earned credential", tc.code, tc.name)
			}
		})
	}
}

// TestExamResultCert_UnknownPGClass_NACKs - an unrecognised SQLSTATE defaults to
// NACK. Acking is the irreversible option (the message is gone), so the default
// must be the reversible one: retry, then DLQ, where a human can see it.
// 42501 = insufficient_privilege, the shape a missing GRANT takes.
func TestExamResultCert_UnknownPGClass_NACKs(t *testing.T) {
	certs := &stubCerts{issueErr: &pgconn.PgError{
		Severity: "ERROR", Code: "42501", Message: "permission denied for table certifications",
	}}
	sub := newExamResultWorldWithCerts(t, certs)

	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err == nil {
		t.Fatalf("an unclassified SQLSTATE must NACK to stay safe, not silently ack")
	}
}

// TestExamResultCert_NonPGErrors_NACK - errors that never reached Postgres (a
// dial timeout) and Chora's own RLS-context sentinel are not data faults: they
// carry no SQLSTATE and must NACK. This is the guard that keeps the 22P02 ack
// from widening into "ack anything the cert store complains about".
func TestExamResultCert_NonPGErrors_NACK(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"connection reset", errors.New("read tcp 10.0.0.1:5432: connection reset by peer")},
		{"context deadline", context.DeadlineExceeded},
		{"rls context missing", rls.ErrNoTenantContext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			certs := &stubCerts{issueErr: tc.err}
			sub := newExamResultWorldWithCerts(t, certs)

			if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err == nil {
				t.Fatalf("%s carries no SQLSTATE and must NACK; got a silent ack", tc.name)
			}
		})
	}
}

// hasCertIssued reports whether the publisher emitted certification.issued.v1.
func hasCertIssued(pub *events.InMemoryPublisher) bool {
	for _, ev := range pub.History() {
		if ev.Topic == events.TopicCertificationIssued {
			return true
		}
	}
	return false
}
