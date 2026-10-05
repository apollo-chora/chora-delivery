// completion_inbox_test.go — CHO-2157.
//
// The live case, reproduced: offering carries {AwardsCertificate: true,
// PassingScorePct: 70}; the learner's RELEASED grade is 38/40 = 95%; and the
// certifications table stayed empty because no engine ever evaluated the policy.
package subscribers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/subscribers"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	compTenant   = "11111111-1111-7111-8111-111111111111"
	compLearner  = "00000000-0000-7000-8000-000000002999"
	compCourse   = "019edf3e-b405-707a-b3ef-851fd4e38e78"
	compOffering = "019f5f04-92e6-79d3-a048-f98d8d47cd51"
	compAssess   = "019f5f10-1690-7c5c-9b03-58ed0804cf8e"
)

// --- test doubles: readers the engine consults ------------------------------

type stubAssessments struct{ a *domain.Assessment }

func (s stubAssessments) Get(_ context.Context, _, _ string) (*domain.Assessment, bool, error) {
	if s.a == nil {
		return nil, false, nil
	}
	return s.a, true, nil
}

// stubOfferings mimics pg.OfferingRepo, which takes NO tenantID argument and so
// reads the tenant from the CONTEXT alone (rls.ApplySession). It therefore
// refuses to answer an untenanted context — exactly as the real repo does.
//
// The live walk caught the engine handing it a bare push context: every offering
// lookup failed, every certificate was silently withheld, and the service looked
// perfectly healthy.
//
// ⚠ This stub USED TO model that RLS failure as (nil, false) — a MISS. That made
// the stub an instance of the very bug it claimed to guard: it could never catch
// a swallowed error, because it WAS the swallow. It now returns
// rls.ErrNoTenantContext, which is what the real repo returns (CHO-2184), so a
// regression to ack-and-drop fails the suite instead of passing it.
type stubOfferings struct {
	o   *domain.Offering
	err error // when set, every read fails — the infra-outage case
}

func (s stubOfferings) Get(ctx context.Context, _ string) (*domain.Offering, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	if tracing.TenantIDFromContext(ctx) == "" {
		// The real repo fails HERE, before any SQL: rls.ApplySession refuses an
		// untenanted context. It is an error, NOT an empty result set.
		return nil, false, rls.ErrNoTenantContext
	}
	if s.o == nil {
		return nil, false, nil // genuine miss
	}
	return s.o, true, nil
}

type stubCourses struct {
	requireAllContent bool
	found             bool
}

func (s stubCourses) CertDefinition(_ context.Context, _, _ string) (bool, bool, bool, error) {
	return true, s.requireAllContent, s.found, nil
}

type stubContent struct{ complete bool }

func (s stubContent) AllContentComplete(_ context.Context, _, _, _ string) (bool, error) {
	return s.complete, nil
}

// --- fixture ----------------------------------------------------------------

// newCompletionWorld builds the live shape and returns the engine plus the
// durable-ish cert store so a test can inspect what was issued.
func newCompletionWorld(t *testing.T, scoreEarned float64, policy *domain.CompletionPolicy, courses stubCourses, content stubContent) (*subscribers.CompletionSubscriber, *domain.CertificationRegistry, *events.InMemoryPublisher, *domain.Submission) {
	t.Helper()
	return newCompletionWorldWith(t, scoreEarned, policy, courses, content, nil)
}

// newCompletionWorldWith is newCompletionWorld with the offerings reader
// injectable, so a test can model the store being DOWN (CHO-2184) rather than
// merely empty. A nil offerings arg wires the healthy default.
func newCompletionWorldWith(t *testing.T, scoreEarned float64, policy *domain.CompletionPolicy, courses stubCourses, content stubContent, offerings *stubOfferings) (*subscribers.CompletionSubscriber, *domain.CertificationRegistry, *events.InMemoryPublisher, *domain.Submission) {
	t.Helper()
	certs := domain.NewCertificationRegistry()
	// nil components reader: these fixtures declare no components, so the
	// resolver is never consulted. The declared-component seam has its own
	// suite (completion_inbox_components_test.go).
	eng, pub, sub := newCompletionEngine(t, scoreEarned, policy, courses, content, offerings, nil, certs)
	return eng, certs, pub, sub
}

// newCompletionEngine is newCompletionWorldWith with the CERT STORE injectable
// too, so a test can model the store REFUSING a write (a Postgres data fault)
// rather than only the happy in-memory registry. Everything else is identical.
func newCompletionEngine(t *testing.T, scoreEarned float64, policy *domain.CompletionPolicy, courses stubCourses, content stubContent, offerings *stubOfferings, components subscribers.ComponentCompletionReader, certs domain.CertificationStore) (*subscribers.CompletionSubscriber, *events.InMemoryPublisher, *domain.Submission) {
	t.Helper()

	sRepo := domain.NewInMemSubmissionRepo()
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   compAssess,
		TenantID:       compTenant,
		LearnerGCID:    compLearner,
		AttemptNumber:  1,
		OpensAt:        time.Now().Add(-2 * time.Hour),
		ClosesAt:       time.Now().Add(2 * time.Hour),
		MaxScore:       40,
		PassingPercent: 70,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	sub.TotalScore = scoreEarned // the instructor's grade of record (CHO-2154)
	if err := sRepo.Save(context.Background(), sub); err != nil {
		t.Fatalf("save submission: %v", err)
	}

	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID: compTenant, InstructorGCID: compLearner, TestSetID: compAssess,
		OfferingID: compOffering, Title: "CHO-2157",
		ScheduledOpenAt: time.Now().Add(-2 * time.Hour), ScheduledCloseAt: time.Now().Add(2 * time.Hour),
		MaxAttempts: 1, TotalPoints: 40, QuestionCount: 1,
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}

	off := &domain.Offering{
		ID: compOffering, TenantID: compTenant,
		CourseIDs:        []string{compCourse},
		CompletionPolicy: policy,
	}

	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")

	offReader := stubOfferings{o: off}
	if offerings != nil {
		offReader = *offerings
	}

	eng := subscribers.NewCompletionSubscriber(
		sRepo, stubAssessments{a: a}, offReader, courses, content, components,
		certs, pub, idempotent.NewMemoryStore(),
	)
	return eng, pub, sub
}

func releaseEnv(id string) events.EventEnvelope {
	return events.EventEnvelope{EventID: id, TenantID: compTenant, GCID: compLearner}
}

func releasePayload(sub *domain.Submission) subscribers.SubmissionReleasedPayload {
	return subscribers.SubmissionReleasedPayload{
		SubmissionID: sub.ID, AssessmentID: compAssess,
		LearnerGCID: compLearner, TenantID: compTenant,
	}
}

// --- the tests --------------------------------------------------------------

// TestCompletion_PermanentPGDataFault_Acks - the graduate lane shares the exam
// lane's cert-issue seam, so it shares the failure mode: a row Postgres refuses
// on a data fault is refused identically on every redelivery, and NACKing it
// only burns retries and fills the DLQ with a message no replay can drain.
//
// The graduate lane anchors on the OFFERING's course rather than an exam's, so
// the exact live 22P02 arrived through the exam door - but the anchor is still
// an unvalidated string flowing into certifications.course_id UUID NOT NULL, so
// the door is the same shape. Classified identically, and loudly.
func TestCompletion_PermanentPGDataFault_Acks(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70, CertTitle: "L3 Competency"}
	certs := &stubCerts{issueErr: fmt.Errorf("pg: insert certification: %w", &pgconn.PgError{
		Severity: "ERROR",
		Code:     "22P02",
		Message:  `invalid input syntax for type uuid: "course-cspo"`,
	})}
	eng, _, sub := newCompletionEngine(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true}, nil, nil, certs)

	if err := eng.Handle(context.Background(), releaseEnv("evt-perm-1"), releasePayload(sub)); err != nil {
		t.Fatalf("a permanent 22P02 data fault must ACK (nil), not NACK into the DLQ; got %v", err)
	}
	if certs.calls != 1 {
		t.Fatalf("expected exactly 1 issue attempt; got %d", certs.calls)
	}
}

// TestCompletion_TransientPGFault_NACKs - the counterweight: a transient fault
// still NACKs, so the ack above cannot quietly widen into "ack any cert-store
// error" and start destroying credentials.
func TestCompletion_TransientPGFault_NACKs(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70, CertTitle: "L3 Competency"}
	certs := &stubCerts{issueErr: fmt.Errorf("pg: insert certification: %w", &pgconn.PgError{
		Severity: "ERROR", Code: "40001", Message: "could not serialize access due to concurrent update",
	})}
	eng, _, sub := newCompletionEngine(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true}, nil, nil, certs)

	if err := eng.Handle(context.Background(), releaseEnv("evt-transient-1"), releasePayload(sub)); err == nil {
		t.Fatalf("a serialization failure is transient and MUST NACK; acking it destroys an earned credential")
	}
}

// THE live case. 38/40 = 95% ≥ 70% against an awarding policy ⇒ a certificate,
// and a certification.issued.v1 so the learner's transcript shows it.
func TestCHO2157_ReleasedPassingGrade_IssuesCertificate(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70, CertTitle: "L3 Competency"}
	eng, certs, pub, sub := newCompletionWorld(t, 38, awarding, stubCourses{found: false}, stubContent{complete: true})

	if err := eng.Handle(context.Background(), releaseEnv("evt-1"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	cert, ok := certs.GetByLearnerCourse(compLearner, compCourse)
	if !ok {
		t.Fatal("a learner who passed the cut-score against an awarding policy must receive a certificate")
	}
	if len(cert.Accomplishments) == 0 || cert.Accomplishments[0] != "L3 Competency" {
		t.Errorf("the certificate must carry the offering's CertTitle, got %v", cert.Accomplishments)
	}
	if got := filterByTopic(pub.History(), "chora.delivery.certification.issued.v1"); len(got) != 1 {
		t.Errorf("certification.issued.v1 must fire so the transcript shows it: got %d", len(got))
	}
}

// No phantom credentials: below the cut-score, nothing is issued.
func TestCHO2157_BelowCutScore_IssuesNothing(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	eng, certs, pub, sub := newCompletionWorld(t, 20, awarding, stubCourses{found: false}, stubContent{complete: true}) // 20/40 = 50%

	if err := eng.Handle(context.Background(), releaseEnv("evt-2"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Error("a learner below the cut-score must NOT receive a certificate")
	}
	if got := filterByTopic(pub.History(), "chora.delivery.certification.issued.v1"); len(got) != 0 {
		t.Errorf("no certificate ⇒ no certification.issued.v1: got %d", len(got))
	}
}

// The course requires all content and the learner has not finished ⇒ withheld.
func TestCHO2157_RequireAllContent_Incomplete_WithholdsCertificate(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	eng, certs, _, sub := newCompletionWorld(t, 38, awarding,
		stubCourses{requireAllContent: true, found: true}, stubContent{complete: false})

	if err := eng.Handle(context.Background(), releaseEnv("evt-3"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Error("cert_require_all_content=true with incomplete content must WITHHOLD the certificate")
	}
}

// ...and once the content IS complete, the certificate issues.
func TestCHO2157_RequireAllContent_Complete_Issues(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	eng, certs, _, sub := newCompletionWorld(t, 38, awarding,
		stubCourses{requireAllContent: true, found: true}, stubContent{complete: true})

	if err := eng.Handle(context.Background(), releaseEnv("evt-4"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); !ok {
		t.Error("content complete + above the cut-score ⇒ certificate")
	}
}

// A policy that awards nothing issues nothing, however well the learner did.
func TestCHO2157_PolicyAwardsNothing_IssuesNothing(t *testing.T) {
	eng, certs, _, sub := newCompletionWorld(t, 40,
		&domain.CompletionPolicy{AwardsCertificate: false, PassingScorePct: 70},
		stubCourses{found: false}, stubContent{complete: true})

	if err := eng.Handle(context.Background(), releaseEnv("evt-5"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Error("AwardsCertificate=false must issue nothing")
	}
}

// A REDELIVERED release must not mint a second credential. Pub/Sub is
// at-least-once, so this is not hypothetical.
func TestCHO2157_RedeliveredRelease_IssuesNoDuplicate(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}
	eng, certs, pub, sub := newCompletionWorld(t, 38, awarding, stubCourses{found: false}, stubContent{complete: true})

	// Two DIFFERENT event ids — i.e. a genuine re-publish, not just a broker
	// retry of the same message. The inbox key would not save us here; the
	// (course_id, gcid) uniqueness does.
	if err := eng.Handle(context.Background(), releaseEnv("evt-a"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle 1: %v", err)
	}
	if err := eng.Handle(context.Background(), releaseEnv("evt-b"), releasePayload(sub)); err != nil {
		t.Fatalf("Handle 2 must not error — a duplicate is refused, not fatal: %v", err)
	}

	all, _ := certs.ListByTenant(compTenant, compLearner, compCourse)
	if len(all) != 1 {
		t.Errorf("a redelivered release must not mint a second credential: got %d certificates", len(all))
	}
	if got := filterByTopic(pub.History(), "chora.delivery.certification.issued.v1"); len(got) != 1 {
		t.Errorf("exactly one certification.issued.v1: got %d", len(got))
	}
}

// -----------------------------------------------------------------------------
// CHO-2184 — an offering lookup that FAILED must NACK, never ack-and-drop.
// -----------------------------------------------------------------------------

// The outage: OfferingRepo.Get swallowed its RLS error into (nil, false), so the
// engine read an infrastructure failure as "offering not found", logged that, and
// returned nil — which ACKS the Pub/Sub message and destroys the event. The
// certificate was never issued, never retried, and never reached the DLQ. The
// service looked perfectly healthy the whole time.
//
// The returned error is what makes the message redeliverable. This pins it.
func TestCHO2184_OfferingReadFailure_NACKs_AndDoesNotDropTheCertificate(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70, CertTitle: "L3 Competency"}
	boom := errors.New("pg: get offering: connection reset by peer")

	eng, certs, pub, sub := newCompletionWorldWith(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true},
		&stubOfferings{err: boom}) // the backing store is DOWN

	err := eng.Handle(context.Background(), releaseEnv("evt-cho2184"), releasePayload(sub))

	if err == nil {
		t.Fatal("an offering read that FAILED must return an error so Pub/Sub NACKs and redelivers. " +
			"Returning nil ACKs the message and destroys the certificate — the CHO-2184 outage.")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying failure must reach the operator, got: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Error("no certificate may be issued off a failed read")
	}
	if got := filterByTopic(pub.History(), "chora.delivery.certification.issued.v1"); len(got) != 0 {
		t.Errorf("a failed read must not emit an issuance: got %d", len(got))
	}
}

// The other half of the contract: a GENUINE miss stays a quiet ACK. If "no such
// offering" started NACKing, every orphaned submission would redeliver forever.
// not-found and blew-up must remain distinguishable in BOTH directions.
func TestCHO2184_GenuineOfferingMiss_StillAcks(t *testing.T) {
	awarding := &domain.CompletionPolicy{AwardsCertificate: true, PassingScorePct: 70}

	eng, certs, _, sub := newCompletionWorldWith(t, 38, awarding,
		stubCourses{found: false}, stubContent{complete: true},
		&stubOfferings{o: nil}) // no such offering — a real miss, not a failure

	if err := eng.Handle(context.Background(), releaseEnv("evt-miss"), releasePayload(sub)); err != nil {
		t.Fatalf("a genuine miss must ACK (nil), not NACK — got: %v", err)
	}
	if _, ok := certs.GetByLearnerCourse(compLearner, compCourse); ok {
		t.Error("a missing offering awards nothing")
	}
}

// The engine must put the tenant on the context before the offering read. If it
// regresses to passing the bare push ctx, the repo now returns
// rls.ErrNoTenantContext — and that must surface as a NACK, not a silent miss.
func TestCHO2184_UntenantedContext_SurfacesAsError_NotAMiss(t *testing.T) {
	s := stubOfferings{o: &domain.Offering{ID: compOffering, TenantID: compTenant}}

	_, ok, err := s.Get(context.Background(), compOffering) // BARE ctx — no tenant
	if err == nil {
		t.Fatal("an untenanted read must be an ERROR (rls.ErrNoTenantContext), not a clean miss — " +
			"modelling it as ok=false is what made the original stub blind to the bug")
	}
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Errorf("want rls.ErrNoTenantContext, got %v", err)
	}
	if ok {
		t.Error("a failed read reports ok=false")
	}
}
