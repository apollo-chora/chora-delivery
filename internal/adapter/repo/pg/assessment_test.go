// Package pg — assessment + submission adapter unit tests.
//
// Exercises the SQL templates + RLS contract via a stub Querier:
//  1. rls.ApplySession is called BEFORE any data query.
//  2. SQL template selection is correct per repo method.
//  3. Bind args flow through.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	testTenant   = "01970000-0000-7000-8000-000000000001"
	testInstr    = "00000000-0000-7000-8000-000000001999"
	testLearner  = "00000000-0000-7000-8000-000000002999"
	testTestSet  = "01985e7f-1234-7abc-8def-000000000a01"
	testAssessID = "01985e7f-1234-7abc-8def-000000000b01"
	testSubID    = "01985e7f-1234-7abc-8def-000000000c01"
)

func newTestAssessment(t *testing.T) *domain.Assessment {
	t.Helper()
	a, err := domain.NewAssessment(domain.NewAssessmentInput{
		TenantID:         testTenant,
		InstructorGCID:   testInstr,
		TestSetID:        testTestSet,
		Title:            "Phyllis Practice",
		ScheduledOpenAt:  time.Now().Add(1 * time.Hour),
		ScheduledCloseAt: time.Now().Add(3 * time.Hour),
		MaxAttempts:      1,
		TotalPoints:      100,
		QuestionCount:    10,
		InvitedGCIDs:     []string{testLearner},
		GradingConfigSnapshot: domain.GradingConfigSnapshot{
			MCQDispatch:             "DETERMINISTIC",
			OEDispatch:              "LLM_EVALUATOR_AGENT",
			PassingThresholdPercent: 70,
		},
	})
	if err != nil {
		t.Fatalf("NewAssessment: %v", err)
	}
	a.ID = testAssessID
	return a
}

func TestAssessmentRepo_NilTxRunner_ReturnsNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewAssessmentRepo(nil)
	err := r.Save(context.Background(), newTestAssessment(t))
	if !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}

func TestAssessmentRepo_Save_CallsApplySessionBeforeUpsert(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewAssessmentRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), testTenant)
	a := newTestAssessment(t)
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("want >=2 SQLs (SET LOCAL + UPSERT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[len(q.sqls)-1], "INSERT INTO assessments") {
		t.Fatalf("last SQL must be INSERT INTO assessments; got %q", q.sqls[len(q.sqls)-1])
	}
}

func TestAssessmentRepo_ListByInstructor_BindsInstructorAndPageSize(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewAssessmentRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), testTenant)
	_, _, err := r.ListByInstructor(ctx, testTenant, testInstr, 25, "")
	if err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	// Find the SELECT call
	found := false
	for i, sql := range q.sqls {
		if strings.Contains(sql, "FROM assessments") &&
			strings.Contains(sql, "instructor_gcid") {
			found = true
			args := q.args[i]
			if len(args) != 3 || args[0] != testTenant || args[1] != testInstr || args[2] != 25 {
				t.Fatalf("ListByInstructor bind args mismatch: %v", args)
			}
		}
	}
	if !found {
		t.Fatalf("ListByInstructor SQL not executed")
	}
}

func TestAssessmentRepo_ListByOffering_BindsOfferingAndPageSize(t *testing.T) {
	t.Parallel()
	const testOffering = "01985e7f-1234-7abc-8def-0000000000f1"
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewAssessmentRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), testTenant)
	_, _, err := r.ListByOffering(ctx, testTenant, testOffering, 25, "")
	if err != nil {
		t.Fatalf("ListByOffering: %v", err)
	}
	// First SQL must be the RLS SET LOCAL.
	if len(q.sqls) == 0 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL; got %v", q.sqls)
	}
	// Find the offering-scoped SELECT call + assert tenant + offering + LIMIT binds.
	found := false
	for i, sql := range q.sqls {
		if strings.Contains(sql, "FROM assessments") && strings.Contains(sql, "offering_id") {
			found = true
			args := q.args[i]
			if len(args) != 3 || args[0] != testTenant || args[1] != testOffering || args[2] != 25 {
				t.Fatalf("ListByOffering bind args mismatch: %v", args)
			}
		}
	}
	if !found {
		t.Fatalf("ListByOffering offering-scoped SELECT not executed; sqls=%v", q.sqls)
	}
}

func TestSubmissionRepo_Save_UpsertsSubmissionAndAnswers(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewSubmissionRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), testTenant)

	s, _ := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:  testAssessID,
		TenantID:      testTenant,
		LearnerGCID:   testLearner,
		AttemptNumber: 1,
		OpensAt:       time.Now(),
		ClosesAt:      time.Now().Add(2 * time.Hour),
		MaxScore:      100,
	})
	s.ID = testSubID
	s.Answers = []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "01985e7f-1234-7abc-8def-000000000d01",
			QuestionID:        "01985e7f-1234-7abc-8def-000000000e01",
			QuestionType:      domain.QuestionTypeMCQ,
			MCQChoiceID:       "01985e7f-1234-7abc-8def-000000000f01",
		},
	}
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Should see SET LOCAL + INSERT submissions + INSERT submission_answers
	if !strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("first SQL must be SET LOCAL")
	}
	hasSubInsert := false
	hasAnsInsert := false
	for _, sql := range q.sqls {
		if strings.Contains(sql, "INSERT INTO submissions") {
			hasSubInsert = true
		}
		if strings.Contains(sql, "INSERT INTO submission_answers") {
			hasAnsInsert = true
		}
	}
	if !hasSubInsert || !hasAnsInsert {
		t.Fatalf("expected both submissions + submission_answers INSERTs; got sub=%v ans=%v", hasSubInsert, hasAnsInsert)
	}
}

func TestAssessmentRepo_ReleaseAllSubmissions_ExecutesReleaseSQL(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{
				rows: []func(...any) error{
					func(dest ...any) error {
						if p, ok := dest[0].(*string); ok {
							*p = "submission-1"
						}
						return nil
					},
					func(dest ...any) error {
						if p, ok := dest[0].(*string); ok {
							*p = "submission-2"
						}
						return nil
					},
				},
			}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewAssessmentRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), testTenant)
	ids, err := r.ReleaseAllSubmissions(ctx, testTenant, testAssessID)
	if err != nil {
		t.Fatalf("ReleaseAllSubmissions: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("released ids: want 2, got %d", len(ids))
	}
	// Verify the atomic UPDATE SQL was sent
	found := false
	for _, sql := range q.sqls {
		if strings.Contains(sql, "UPDATE submissions") &&
			strings.Contains(sql, "GRADED_PENDING_RELEASE") &&
			strings.Contains(sql, "RELEASED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("release atomic UPDATE SQL not executed")
	}
}
