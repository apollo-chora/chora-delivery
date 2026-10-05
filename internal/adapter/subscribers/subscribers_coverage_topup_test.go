// subscribers_coverage_topup_test.go — branch top-ups for the subscriber
// adapters. The existing suites (grading_inbox_test.go, completion_inbox_test.go,
// batch_testset_inbox_test.go, exam_result_inbox_test.go, course_progress/
// module_progress/subscribers tests) cover the happy paths; this file drives the
// remaining GUARDS + ERROR branches: nil receivers, nil-inbox constructor
// fallbacks, port-failure NACKs, ack-and-drop no-ops, emit-failure log paths,
// and the repository error injection needed to reach them.
//
// Everything stays behind the ports — no database, no network.
package subscribers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Shared doubles
// -----------------------------------------------------------------------------

// faultySubmissionRepo wraps the in-memory submission repo with injectable
// Get/Save failures + a getOverride that returns a DIFFERENT aggregate than the
// queried id (which is how the subscribers' ErrSubmissionMismatch branch is
// reached — the load key and the aggregate id disagree).
type faultySubmissionRepo struct {
	*domain.InMemSubmissionRepo
	getErr        error
	saveErr       error
	getOverrideID string
}

func (f *faultySubmissionRepo) Get(ctx context.Context, tenantID, id string) (*domain.Submission, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	if f.getOverrideID != "" {
		return f.InMemSubmissionRepo.Get(ctx, tenantID, f.getOverrideID)
	}
	return f.InMemSubmissionRepo.Get(ctx, tenantID, id)
}

func (f *faultySubmissionRepo) Save(ctx context.Context, s *domain.Submission) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.InMemSubmissionRepo.Save(ctx, s)
}

// certFailingPublisher fails PublishCertificationIssued — drives the
// "certificate durable but its announced event failed" log branch.
type certFailingPublisher struct {
	*events.InMemoryPublisher
	certErr error
}

func (f *certFailingPublisher) PublishCertificationIssued(events.CertificationIssued) (events.PublishedEvent, error) {
	return events.PublishedEvent{}, f.certErr
}

// noCustomPublisher satisfies events.Publisher WITHOUT the optional
// PublishCustom escape hatch (embedding the INTERFACE, not the concrete
// publisher, so PublishCustom is not promoted) — drives the graded/failed
// emit-skip branches.
type noCustomPublisher struct {
	events.Publisher
}

// -----------------------------------------------------------------------------
// Completion engine — error-injecting doubles for the readers
// -----------------------------------------------------------------------------

type scriptedAssessment struct {
	a   *domain.Assessment
	err error
}

func (s scriptedAssessment) Get(_ context.Context, _, _ string) (*domain.Assessment, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	if s.a == nil {
		return nil, false, nil
	}
	return s.a, true, nil
}

type scriptedOfferingReader struct {
	o   *domain.Offering
	err error
}

func (s scriptedOfferingReader) Get(_ context.Context, _ string) (*domain.Offering, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	if s.o == nil {
		return nil, false, nil
	}
	return s.o, true, nil
}

type scriptedCourseCert struct {
	enabled, requireAll, found bool
	err                        error
}

func (s scriptedCourseCert) CertDefinition(_ context.Context, _, _ string) (bool, bool, bool, error) {
	return s.enabled, s.requireAll, s.found, s.err
}

type scriptedContent struct {
	complete bool
	err      error
}

func (s scriptedContent) AllContentComplete(_ context.Context, _, _, _ string) (bool, error) {
	return s.complete, s.err
}

// completionFixture assembles the live shape (submission + assessment +
// offering with an awarding policy) with the readers swappable per test.
type completionFixture struct {
	repo       *faultySubmissionRepo
	sub        *domain.Submission
	assessment *domain.Assessment
	offering   *domain.Offering
	policy     *domain.CompletionPolicy
}

const topupCourse = compCourse

func newCompletionFixture(t *testing.T, score float64) *completionFixture {
	t.Helper()
	fix := &completionFixture{repo: &faultySubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo()}}
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID: compAssess, TenantID: compTenant, LearnerGCID: compLearner,
		AttemptNumber: 1, OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour),
		MaxScore: 40, PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	sub.TotalScore = score
	if err := fix.repo.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed submission: %v", err)
	}
	fix.sub = sub
	fix.assessment = &domain.Assessment{ID: compAssess, TenantID: compTenant, OfferingID: compOffering, Title: "top-up"}
	fix.policy = &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70, CertTitle: "L3 Competency"}
	fix.offering = &domain.Offering{ID: compOffering, TenantID: compTenant, CourseIDs: []string{topupCourse}, CompletionPolicy: fix.policy}
	return fix
}

func completionEnv(id string) events.EventEnvelope {
	return events.EventEnvelope{EventID: id, TenantID: compTenant, GCID: compLearner}
}

func completionPayload(sub *domain.Submission) subscribers.SubmissionReleasedPayload {
	return subscribers.SubmissionReleasedPayload{
		SubmissionID: sub.ID, AssessmentID: compAssess,
		LearnerGCID: compLearner, TenantID: compTenant,
	}
}

func buildCompletionEngine(
	fix *completionFixture,
	assess scriptedAssessment,
	off scriptedOfferingReader,
	courses scriptedCourseCert,
	content scriptedContent,
	certs domain.CertificationStore,
	pub events.Publisher,
) *subscribers.CompletionSubscriber {
	return subscribers.NewCompletionSubscriber(
		fix.repo, assess, off, courses, content, nil,
		certs, pub, idempotent.NewMemoryStore(),
	)
}

// --- nil-wiring + constructor fallback ---------------------------------------

func TestCompletion_NilInboxFallsBackToMemory(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	certs := domain.NewCertificationRegistry()
	eng := subscribers.NewCompletionSubscriber(
		fix.repo, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, nil, certs,
		events.NewInMemoryPublisher("chora-489812", "chora-delivery"), nil, // nil inbox → MemoryStore
	)
	if err := eng.Handle(context.Background(), completionEnv("evt-ci"), completionPayload(fix.sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, topupCourse); !ok {
		t.Fatal("certificate must be issued through the fallback-inbox engine")
	}
}

func TestCompletion_SubscribedTopic(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), nil)
	if got := eng.SubscribedTopic(); got != "chora.delivery.submission.released.v1" {
		t.Fatalf("SubscribedTopic: %q", got)
	}
}

func TestCompletion_NilReceiverNotWired(t *testing.T) {
	var eng *subscribers.CompletionSubscriber
	if err := eng.Handle(context.Background(), completionEnv("evt-nr"), subscribers.SubmissionReleasedPayload{
		SubmissionID: "sub-1", TenantID: compTenant,
	}); err == nil {
		t.Fatal("a nil receiver must fail loud (not wired)")
	}
}

func TestCompletion_TenantFromEnvelope(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	certs := domain.NewCertificationRegistry()
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, certs, nil)
	p := completionPayload(fix.sub)
	p.TenantID = "" // envelope carries the tenant
	if err := eng.Handle(context.Background(), completionEnv("evt-ttp"), p); err != nil {
		t.Fatalf("Handle (envelope tenant): %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, topupCourse); !ok {
		t.Fatal("envelope-tenant fallback must still issue")
	}
}

func TestCompletion_TenantRequired(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), nil)
	p := completionPayload(fix.sub)
	p.TenantID = ""
	env := events.EventEnvelope{EventID: "evt-treq"}
	if err := eng.Handle(context.Background(), env, p); err == nil {
		t.Fatal("a tenant-less event must fail loud")
	}
}

func TestCompletion_SubmissionIDRequired(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), nil)
	p := subscribers.SubmissionReleasedPayload{TenantID: compTenant}
	if err := eng.Handle(context.Background(), completionEnv("evt-sid"), p); err == nil {
		t.Fatal("a submission_id-less event must fail loud")
	}
}

// --- reader failures (NACK) and misses (ACK) ---------------------------------

func TestCompletion_SubmissionGetError_Nacks(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	fix.repo.getErr = errors.New("pg: get submission: connection reset")
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), nil)
	if err := eng.Handle(context.Background(), completionEnv("evt-geterr"), completionPayload(fix.sub)); err == nil {
		t.Fatal("an infra failure on the submission read must NACK")
	}
}

func TestCompletion_SubmissionNotFound_AcksWithLog(t *testing.T) {
	fix := &completionFixture{repo: &faultySubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo()}}
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), nil)
	p := completionPayload(&domain.Submission{ID: "ghost-submission", AssessmentID: compAssess})
	if err := eng.Handle(context.Background(), completionEnv("evt-ghost"), p); err != nil {
		t.Fatalf("a genuine miss must ACK (nil), got %v", err)
	}
}

func TestCompletion_AssessmentReadError_Nacks(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	eng := buildCompletionEngine(fix, scriptedAssessment{err: errors.New("pg: get assessment: refused")},
		scriptedOfferingReader{o: fix.offering}, scriptedCourseCert{}, scriptedContent{},
		domain.NewCertificationRegistry(), nil)
	if err := eng.Handle(context.Background(), completionEnv("evt-aerr"), completionPayload(fix.sub)); err == nil {
		t.Fatal("an assessment-read failure must NACK")
	}
}

func TestCompletion_AssessmentWithoutOffering_Acks(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	fix.assessment.OfferingID = ""
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), nil)
	if err := eng.Handle(context.Background(), completionEnv("evt-nooff"), completionPayload(fix.sub)); err != nil {
		t.Fatalf("an assessment with no offering is a legit no-op (ACK): %v", err)
	}
}

func TestCompletion_OfferingWithoutCourse_Acks(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	fix.offering.CourseIDs = nil
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), nil)
	if err := eng.Handle(context.Background(), completionEnv("evt-nocourse"), completionPayload(fix.sub)); err != nil {
		t.Fatalf("an offering with no course anchor is a legit no-op (ACK): %v", err)
	}
}

func TestCompletion_CourseCertDefinitionError_Nacks(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{err: errors.New("pg: cert definition: refused")}, scriptedContent{},
		domain.NewCertificationRegistry(), nil)
	if err := eng.Handle(context.Background(), completionEnv("evt-cderr"), completionPayload(fix.sub)); err == nil {
		t.Fatal("a course cert-definition failure must NACK")
	}
}

func TestCompletion_ContentCompletionError_Nacks(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{enabled: true, requireAll: true, found: true},
		scriptedContent{err: errors.New("pg: content completion: refused")},
		domain.NewCertificationRegistry(), nil)
	if err := eng.Handle(context.Background(), completionEnv("evt-ccerr"), completionPayload(fix.sub)); err == nil {
		t.Fatal("a content-completion failure must NACK")
	}
}

func TestCompletion_PayloadAssessmentIDEmpty_UsesSubmission(t *testing.T) {
	// firstNonEmpty(p.AssessmentID, sub.AssessmentID): an empty wire
	// assessment_id must fall back to the aggregate's own id.
	fix := newCompletionFixture(t, 38)
	certs := domain.NewCertificationRegistry()
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, certs, nil)
	p := completionPayload(fix.sub)
	p.AssessmentID = ""
	if err := eng.Handle(context.Background(), completionEnv("evt-fne"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, topupCourse); !ok {
		t.Fatal("fallback to the submission's assessment id must still issue")
	}
}

// --- emit paths --------------------------------------------------------------

func TestCompletion_IssuedEventPublishFailure_LoggedNotNacked(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	pub := &certFailingPublisher{InMemoryPublisher: events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
		certErr: errors.New("outbox row conflict")}
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, domain.NewCertificationRegistry(), pub)
	// The certificate IS issued + durable; only the announcement failed → the
	// handler must ACK (nil), never NACK.
	if err := eng.Handle(context.Background(), completionEnv("evt-puberr"), completionPayload(fix.sub)); err != nil {
		t.Fatalf("a publish failure must be logged, not NACKed: %v", err)
	}
}

func TestCompletion_NilPublisher_SkipsEmit(t *testing.T) {
	fix := newCompletionFixture(t, 38)
	certs := domain.NewCertificationRegistry()
	eng := buildCompletionEngine(fix, scriptedAssessment{a: fix.assessment}, scriptedOfferingReader{o: fix.offering},
		scriptedCourseCert{}, scriptedContent{}, certs, nil)
	if err := eng.Handle(context.Background(), completionEnv("evt-nilpub"), completionPayload(fix.sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, topupCourse); !ok {
		t.Fatal("certificate must issue even with no publisher wired")
	}
}

// -----------------------------------------------------------------------------
// Exam-result engine top-ups
// -----------------------------------------------------------------------------

func TestExamResultCert_MissingExamID_FailsLoud(t *testing.T) {
	sub, _, _ := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})
	p := passPayload("PASS")
	p.ExamID = ""
	if err := sub.Handle(context.Background(), passEnv(), p); err == nil {
		t.Fatal("an exam_id-less event must fail loud")
	}
}

func TestExamResultCert_DifferentEventIDs_NoDuplicate(t *testing.T) {
	sub, certs, pub := newExamResultWorld(t, stubExams{ex: newExam(erCourse)})
	// Two DIFFERENT event ids (a genuine re-publish, not a broker retry of the
	// same message) — the inbox cannot dedupe these; certifications'
	// (course_id, gcid) uniqueness must.
	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("Handle 1: %v", err)
	}
	env2 := events.EventEnvelope{EventID: "evt-exam-pass-2", TenantID: erTenant, Traceparent: "00-trace-parent-01"}
	if err := sub.Handle(context.Background(), env2, passPayload("PASS")); err != nil {
		t.Fatalf("edelivery #2 must ACK (nil) — a duplicate is refused, not fatal: %v", err)
	}
	all, _ := certs.ListByTenantCtx(context.Background(), erTenant, "", "")
	if len(all) != 1 {
		t.Fatalf("a redelivered PASS must not mint a second cert: got %d", len(all))
	}
	if got := filterByTopic(pub.History(), "chora.delivery.certification.issued.v1"); len(got) != 1 {
		t.Fatalf("exactly one certification.issued.v1: got %d", len(got))
	}
}

func TestExamResultCert_PublishFailure_LoggedNotNacked(t *testing.T) {
	certs := domain.NewCertificationRegistry()
	pub := &certFailingPublisher{InMemoryPublisher: events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
		certErr: errors.New("outbox row conflict")}
	sub := subscribers.NewExamResultCertSubscriber(stubExams{ex: newExam(erCourse)}, certs, pub, nil)
	if err := sub.Handle(context.Background(), passEnv(), passPayload("PASS")); err != nil {
		t.Fatalf("a publish failure after a durable issue must ACK (nil), got %v", err)
	}
	all, _ := certs.ListByTenantCtx(context.Background(), erTenant, "", "")
	if len(all) != 1 {
		t.Fatal("the certificate must be issued even when its announcement fails")
	}
}

// -----------------------------------------------------------------------------
// Course-progress top-ups
// -----------------------------------------------------------------------------

func TestCourseProgressInbox_CompletionRequiresTenant(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)
	p := subscribers.LearningPathCompletedPayload{
		PathID: cpiPath, CourseID: cpiCourse, LearnerGCID: cpiGCID, TenantID: "",
	}
	env := events.EventEnvelope{EventID: "evt-ct", TenantID: ""}
	if err := sub.HandleCompletion(context.Background(), env, p); err == nil {
		t.Fatal("a tenant-less completion must fail loud")
	}
}

func TestCourseProgressInbox_ZeroOccurredAtDefaultsToNow(t *testing.T) {
	port := newRecordingPort()
	sub := newCPSub(port)
	p := advPayload()
	p.OccurredAt = time.Time{} // producer clock absent — must not rewind to epoch
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-zero"), p); err != nil {
		t.Fatalf("HandleAdvance: %v", err)
	}
	if _, ok := storedFraction(t, port); !ok {
		t.Fatal("a zero occurred_at must still project (watermark = now)")
	}
}

func TestCourseProgressInbox_ShortSQLStateCodeNacks(t *testing.T) {
	// A PgError whose SQLSTATE is shorter than 2 chars carries no usable class
	// → the classifier stays reversible: NACK (permanentPGDataFault short-code
	// guard, the safe-default bias).
	port := newRecordingPort()
	port.advanceErr = fmt.Errorf("pg: advance: %w", &pgconn.PgError{Code: "8", Message: "weird code"})
	sub := newCPSub(port)
	if err := sub.HandleAdvance(context.Background(), cpiEnv("evt-short"), advPayload()); err == nil {
		t.Fatal("an unrecognisable SQLSTATE must NACK, not ack")
	}
}

// -----------------------------------------------------------------------------
// Batch-test-set top-ups
// -----------------------------------------------------------------------------

// failingAssemblyStore fails the idempotency read — the transient-infra NACK.
type failingAssemblyStore struct{ err error }

func (f *failingAssemblyStore) GetBySourceJobID(_ context.Context, _, _ string) (*domain.TestSet, bool, error) {
	return nil, false, f.err
}
func (f *failingAssemblyStore) Save(_ context.Context, _ *domain.TestSet) error { return nil }

func TestBatchTestSet_NilInboxFallsBackToMemory(t *testing.T) {
	store := newFakeAssemblyStore()
	sub := subscribers.NewBatchTestSetSubscriber(store, &fakeSnapshotter{},
		events.NewInMemoryPublisher("chora-489812", "chora-delivery"), nil)
	if err := sub.Handle(context.Background(), btEnv("evt-ni"), btPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 Save; got %d", len(store.saved))
	}
}

func TestBatchTestSet_NilReceiverErrors(t *testing.T) {
	var sub *subscribers.BatchTestSetSubscriber
	if err := sub.Handle(context.Background(), btEnv("evt-nr"), btPayload()); err == nil {
		t.Fatal("a nil receiver must fail loud")
	}
}

func TestBatchTestSet_AuthorRequired(t *testing.T) {
	store := newFakeAssemblyStore()
	sub, _ := newBatchSubscriber(store, &fakeSnapshotter{})
	p := btPayload()
	p.AuthorGCID = ""
	env := btEnv("evt-1")
	env.GCID = ""
	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("missing author_gcid must error even after envelope fallback")
	}
}

func TestBatchTestSet_IdempotencyReadError_Nacks(t *testing.T) {
	sub := subscribers.NewBatchTestSetSubscriber(&failingAssemblyStore{err: errors.New("pg: dead pool")}, nil, nil, nil)
	if err := sub.Handle(context.Background(), btEnv("evt-1"), btPayload()); err == nil {
		t.Fatal("an idempotency-read failure must NACK")
	}
}

func TestBatchTestSet_RedeliveryBeyondInbox_AckNoOp(t *testing.T) {
	// Two subscribers share the store but NOT an inbox — the second arrival
	// passes the inbox and hits the durable source_job_id existence check.
	store := newFakeAssemblyStore()
	sub1, _ := newBatchSubscriber(store, &fakeSnapshotter{})
	if err := sub1.Handle(context.Background(), btEnv("evt-1"), btPayload()); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	sub2, _ := newBatchSubscriber(store, &fakeSnapshotter{}) // fresh inbox
	if err := sub2.Handle(context.Background(), btEnv("evt-2"), btPayload()); err != nil {
		t.Fatalf("duplicate Handle (fresh inbox) must ACK nil: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("no second Save expected; got %d", len(store.saved))
	}
}

func TestBatchTestSet_UnassemblablePayload_Nacks(t *testing.T) {
	store := newFakeAssemblyStore()
	sub, _ := newBatchSubscriber(store, &fakeSnapshotter{})
	p := btPayload()
	p.Title = ""
	if err := sub.Handle(context.Background(), btEnv("evt-1"), p); err == nil {
		t.Fatal("a title-less batch cannot assemble a test set → NACK")
	}
}

func TestBatchTestSet_NilPublisher_SkipsCreatedEvent(t *testing.T) {
	store := newFakeAssemblyStore()
	sub := subscribers.NewBatchTestSetSubscriber(store, &fakeSnapshotter{}, nil, idempotent.NewMemoryStore())
	if err := sub.Handle(context.Background(), btEnv("evt-np"), btPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("assembly must still persist without a publisher; got %d saves", len(store.saved))
	}
}

// -----------------------------------------------------------------------------
// Grading inbox top-ups
// -----------------------------------------------------------------------------

func TestGradingInbox_WithTTL(t *testing.T) {
	s := subscribers.NewGradingInboxSubscriber(domain.NewInMemSubmissionRepo(), nil, nil)
	if got := s.WithTTL(time.Hour); got != s {
		t.Fatal("WithTTL must return the receiver")
	}
	if got := s.WithTTL(0); got != s {
		t.Fatal("WithTTL(0) must no-op and return the receiver")
	}
	var nilSub *subscribers.GradingInboxSubscriber
	if got := nilSub.WithTTL(time.Hour); got != nil {
		t.Fatal("nil receiver must pass through")
	}
	if got := nilSub.WithAssessmentReader(nil); got != nil {
		t.Fatal("nil receiver WithAssessmentReader must pass through")
	}
	if got := nilSub.WithOfferings(nil); got != nil {
		t.Fatal("nil receiver WithOfferings must pass through")
	}
}

func TestGradingInbox_WithAssessmentReaderAndOfferings(t *testing.T) {
	s := subscribers.NewGradingInboxSubscriber(domain.NewInMemSubmissionRepo(), nil, nil)
	if got := s.WithAssessmentReader(&fakeAssessReader{}); got != s {
		t.Fatal("WithAssessmentReader must return the receiver")
	}
	if got := s.WithOfferings(&fakeOfferingsPort{}); got != s {
		t.Fatal("WithOfferings must return the receiver")
	}
}

func TestGradingInbox_NilReceiverErrors(t *testing.T) {
	var nilSub *subscribers.GradingInboxSubscriber
	if err := nilSub.Handle(context.Background(), events.EventEnvelope{EventID: "e"}, subscribers.OEBatchCompletedPayload{}); err == nil {
		t.Fatal("nil receiver Handle must fail loud")
	}
	if err := nilSub.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{EventID: "e"}, subscribers.SubmissionCompletedPayload{}); err == nil {
		t.Fatal("nil receiver HandleSubmissionCompleted must fail loud")
	}
}

func TestGradingInbox_MissingSubmissionID_Errors(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.Handle(context.Background(), events.EventEnvelope{EventID: "e", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{TenantID: gradingTenant}); err == nil {
		t.Fatal("missing submission_id must fail loud (Handle)")
	}
	if err := s.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{EventID: "e", TenantID: gradingTenant},
		subscribers.SubmissionCompletedPayload{TenantID: gradingTenant}); err == nil {
		t.Fatal("missing submission_id must fail loud (HandleSubmissionCompleted)")
	}
}

func TestGradingInbox_HandleSubmissionCompleted_MissingTenant(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{EventID: "e"},
		subscribers.SubmissionCompletedPayload{SubmissionID: "s"}); err == nil {
		t.Fatal("missing tenant must fail loud")
	}
}

func TestGradingInbox_HandleSubmissionCompleted_MissingEventID(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{TenantID: gradingTenant},
		subscribers.SubmissionCompletedPayload{SubmissionID: "s", TenantID: gradingTenant}); err == nil {
		t.Fatal("missing event_id must fail loud")
	}
}

func TestGradingInbox_SubmissionCompleted_Mismatch_AckDrops(t *testing.T) {
	_, sub := newPendingOESubmission(t)
	repo := &faultySubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo()}
	if err := repo.InMemSubmissionRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The payload's submission_id differs from the aggregate the repo returns —
	// ApplyGrading declares the routing mismatch and the subscriber ack-drops.
	repo.getOverrideID = sub.ID
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{EventID: "evt-mm", TenantID: gradingTenant},
		subscribers.SubmissionCompletedPayload{SubmissionID: "stale-key", Outcome: "ok", TenantID: gradingTenant}); err != nil {
		t.Fatalf("a routing mismatch must ACK-and-drop: %v", err)
	}
}

func TestGradingInbox_SubmissionGetError_Nacks(t *testing.T) {
	repo := &faultySubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo(), getErr: errors.New("pg: dead pool")}
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	env := events.EventEnvelope{EventID: "evt-1", TenantID: gradingTenant}
	if err := s.Handle(context.Background(), env, subscribers.OEBatchCompletedPayload{
		SubmissionID: "s-1", BatchOutcome: "ok", TenantID: gradingTenant,
	}); err == nil {
		t.Fatal("a Get failure must NACK (Handle)")
	}
	if err := s.HandleSubmissionCompleted(context.Background(), env, subscribers.SubmissionCompletedPayload{
		SubmissionID: "s-1", Outcome: "ok", TenantID: gradingTenant,
	}); err == nil {
		t.Fatal("a Get failure must NACK (HandleSubmissionCompleted)")
	}
}

func TestGradingInbox_BatchMismatch_AckDrops(t *testing.T) {
	_, sub := newPendingOESubmission(t)
	repo := &faultySubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo()}
	if err := repo.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo.getOverrideID = sub.ID // load by the override id, query by a stale key
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	// The payload's submission_id differs from the aggregate the repo returns —
	// ApplyOEGradingBatch refuses the mismatch and the subscriber ack-drops
	// (routing bug; NACKing would loop forever).
	if err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-mm", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{SubmissionID: "stale-key", BatchOutcome: "ok", TenantID: gradingTenant}); err != nil {
		t.Fatalf("a routing mismatch must ACK-and-drop: %v", err)
	}
}

func TestGradingInbox_SaveError_Nacks(t *testing.T) {
	_, sub := newPendingOESubmission(t)
	repo := &faultySubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo()}
	if err := repo.InMemSubmissionRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo.saveErr = errors.New("pg: upsert dead")
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-se", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{SubmissionID: sub.ID, BatchOutcome: "ok", TenantID: gradingTenant,
			Results: []subscribers.OEBatchGradedAnswer{{TestSetQuestionID: "oe-1", PointsEarned: 40, PointsPossible: 50}}}); err == nil {
		t.Fatal("a Save failure must NACK")
	}
}

func TestGradingInbox_SubmissionCompleted_SaveErrorNacks(t *testing.T) {
	_, sub := newPendingOESubmission(t)
	repo := &faultySubmissionRepo{InMemSubmissionRepo: domain.NewInMemSubmissionRepo()}
	if err := repo.InMemSubmissionRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo.saveErr = errors.New("pg: upsert dead")
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{EventID: "evt-sce", TenantID: gradingTenant},
		subscribers.SubmissionCompletedPayload{SubmissionID: sub.ID, Outcome: "ok", TenantID: gradingTenant,
			Graded: []subscribers.SubmissionGradedAnswer{{TestSetQuestionID: "oe-1", PointsEarned: 40, PointsPossible: 50}}}); err == nil {
		t.Fatal("a Save failure must NACK")
	}
}

func TestGradingInbox_SubmissionCompleted_NotFoundAcks(t *testing.T) {
	repo := domain.NewInMemSubmissionRepo()
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{EventID: "evt-nf", TenantID: gradingTenant},
		subscribers.SubmissionCompletedPayload{SubmissionID: "ghost", Outcome: "ok", TenantID: gradingTenant}); err != nil {
		t.Fatalf("an unknown submission must ack-and-drop: %v", err)
	}
}

func TestGradingInbox_SubmissionCompleted_FailedOutcome_EmitsFailed(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())

	if err := s.HandleSubmissionCompleted(context.Background(), events.EventEnvelope{EventID: "evt-fail", TenantID: gradingTenant},
		subscribers.SubmissionCompletedPayload{SubmissionID: sub.ID, AssessmentID: sub.AssessmentID,
			Outcome: "FAILED", FailureMessage: "gemini blocked", TenantID: gradingTenant,
			Graded: []subscribers.SubmissionGradedAnswer{}}); err != nil {
		t.Fatalf("HandleSubmissionCompleted: %v", err)
	}
	// FAILED is a terminal outcome — the failure marker must fire, the grade
	// must NOT be announced.
	if got := filterByTopic(pub.History(), "chora.delivery.submission.graded.v1"); len(got) != 0 {
		t.Fatalf("a FAILED outcome must not announce a grade: got %d", len(got))
	}
	if got := filterByTopic(pub.History(), "chora.delivery.grading.failed.v1"); len(got) != 1 {
		t.Fatalf("a FAILED outcome must emit grading.failed.v1: got %d", len(got))
	}
	got, _, _ := repo.Get(context.Background(), gradingTenant, sub.ID)
	if got.State != domain.SubmissionStatePendingOEGrading {
		t.Fatalf("a FAILED outcome must not advance the FSM: got %s", got.State)
	}
}

func TestGradingInbox_NilPublisher_SkipsGradedEmit(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-np", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{SubmissionID: sub.ID, BatchOutcome: "ok", TenantID: gradingTenant,
			Results: []subscribers.OEBatchGradedAnswer{{TestSetQuestionID: "oe-1", PointsEarned: 40, PointsPossible: 50}}}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestGradingInbox_NonCustomPublisher_SkipsGradedEmit(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := &noCustomPublisher{events.NewInMemoryPublisher("chora-489812", "chora-delivery")}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())
	if err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-nc", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{SubmissionID: sub.ID, BatchOutcome: "ok", TenantID: gradingTenant,
			Results: []subscribers.OEBatchGradedAnswer{{TestSetQuestionID: "oe-1", PointsEarned: 40, PointsPossible: 50}}}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestGradingInbox_FailedBatch_NilPublisher_EmitSkipped(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	s := subscribers.NewGradingInboxSubscriber(repo, nil, idempotent.NewMemoryStore())
	if err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-fnp", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{SubmissionID: sub.ID, BatchOutcome: "failed", TenantID: gradingTenant}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestGradingInbox_FailedBatch_NonCustomPublisher_EmitSkipped(t *testing.T) {
	repo, sub := newPendingOESubmission(t)
	pub := &noCustomPublisher{events.NewInMemoryPublisher("chora-489812", "chora-delivery")}
	s := subscribers.NewGradingInboxSubscriber(repo, pub, idempotent.NewMemoryStore())
	if err := s.Handle(context.Background(), events.EventEnvelope{EventID: "evt-fnc", TenantID: gradingTenant},
		subscribers.OEBatchCompletedPayload{SubmissionID: sub.ID, BatchOutcome: "failed", TenantID: gradingTenant}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Module-progress top-ups
// -----------------------------------------------------------------------------

func TestModuleProgressInbox_NilInboxFallsBack(t *testing.T) {
	// nil projector + nil inbox: constructor must not panic; Handle must fail
	// loud about the unwired projector.
	sub := subscribers.NewModuleProgressInboxSubscriber(nil, nil)
	if sub == nil {
		t.Fatal("constructor must return a subscriber")
	}
	if err := sub.Handle(context.Background(), "evt-1", "tenant", "gcid", "atom", "ref"); err == nil {
		t.Fatal("an unwired projector must fail loud")
	}
}

func TestModuleProgressInbox_NilReceiverErrors(t *testing.T) {
	var s *subscribers.ModuleProgressInboxSubscriber
	if err := s.Handle(context.Background(), "evt-1", "t", "g", "atom", "ref"); err == nil {
		t.Fatal("a nil receiver must fail loud")
	}
}

// -----------------------------------------------------------------------------
// application.Handler (payment-captured / KYC) top-ups
// -----------------------------------------------------------------------------

func TestHandler_WithInboxTTL(t *testing.T) {
	h := subscribers.NewHandler(repoinmem.NewApplicationRepo(), nil, nil)
	if got := h.WithInboxTTL(time.Hour); got != h {
		t.Fatal("WithInboxTTL must return the receiver")
	}
	if got := h.WithInboxTTL(0); got != h {
		t.Fatal("WithInboxTTL(0) must no-op and return the receiver")
	}
	var nilH *subscribers.Handler
	if got := nilH.WithInboxTTL(time.Hour); got != nil {
		t.Fatal("nil receiver must pass through")
	}
}

func TestHandler_NilReceivers_FailLoud(t *testing.T) {
	var nilH *subscribers.Handler
	if err := nilH.HandlePaymentCaptured(context.Background(), subscribers.PaymentCaptured{}); err == nil {
		t.Fatal("nil receiver HandlePaymentCaptured must fail loud")
	}
	if err := nilH.HandleKYCVerified(context.Background(), subscribers.KYCVerified{TenantID: "t", GCID: "g"}); err == nil {
		t.Fatal("nil receiver HandleKYCVerified must fail loud")
	}
}

func TestHandlePaymentCaptured_UnexpectedState_Errors(t *testing.T) {
	repo := repoinmem.NewApplicationRepo()
	h := subscribers.NewHandler(repo, nil, nil)

	app, _, _ := repo.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	_ = app.Transition(application.StatusUnderReview) // not yet Accepted
	_ = repo.Save(context.Background(), app)

	err := h.HandlePaymentCaptured(context.Background(), subscribers.PaymentCaptured{
		TenantID: tenantA, ApplicationID: app.ID, PaymentIntent: "pi_1",
	})
	if err == nil {
		t.Fatal("a payment landing before Accepted must surface an error (NACK → DLQ), not a silent ack")
	}
	got, _, _ := repo.Get(context.Background(), tenantA, app.ID)
	if got.Status != application.StatusUnderReview {
		t.Fatalf("no transition may happen off the unexpected state; got %s", got.Status)
	}
}

func TestHandleKYCVerified_SkipsNonUnderReview(t *testing.T) {
	repo := repoinmem.NewApplicationRepo()
	h := subscribers.NewHandler(repo, nil, nil)

	// Learner has one UnderReview app (must advance) + one already-Enrolled
	// app (must be skipped, not rewound).
	under, _, _ := repo.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	_ = under.Transition(application.StatusUnderReview)
	_ = repo.Save(context.Background(), under)

	enrolled, _, _ := repo.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantA, CourseID: "01970000-0000-7000-8000-000000000098", GCID: gcidA,
	})
	for _, s := range []application.Status{
		application.StatusUnderReview, application.StatusOfferMade,
		application.StatusAccepted, application.StatusPaid, application.StatusEnrolled,
	} {
		_ = enrolled.Transition(s)
	}
	_ = repo.Save(context.Background(), enrolled)

	if err := h.HandleKYCVerified(context.Background(), subscribers.KYCVerified{TenantID: tenantA, GCID: gcidA}); err != nil {
		t.Fatalf("HandleKYCVerified: %v", err)
	}
	gotUnder, _, _ := repo.Get(context.Background(), tenantA, under.ID)
	if gotUnder.Status != application.StatusOfferMade {
		t.Errorf("UnderReview app must advance to OfferMade; got %s", gotUnder.Status)
	}
	gotEnrolled, _, _ := repo.Get(context.Background(), tenantA, enrolled.ID)
	if gotEnrolled.Status != application.StatusEnrolled {
		t.Errorf("an already-Enrolled app must be skipped untouched; got %s", gotEnrolled.Status)
	}
}
