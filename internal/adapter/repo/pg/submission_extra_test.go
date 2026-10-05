// submission_extra_test.go — unit tests for the remaining SubmissionRepo
// methods (Get / GetInProgressByLearner / ListByAssessment / ListByLearner /
// CountAttemptsByLearner / FindLatestByLearner / AppendGradeOverrides / Save)
// that were at or near 0% statement coverage. Complements submission_test.go
// (which owns ListReleasedByLearner).
//
// Same stub pattern as application_test.go: a stubQuerier stands in for pgx so
// the SQL surface, bind-arg order and RLS contract are exercised without a live
// DB. sqls[0] is always the rls.ApplySession SET LOCAL chora.tenant_id Exec
// (the repo stamps the tenant on ctx before the tx begins).
//
// Row fillers write EVERY dest in scan order: scanSubmissionRow consumes 29
// cols, scanSubmissionAnswerRow 28. The answer rows' `mcq_choice_ids` dest is
// the package-unexported textArray, so subxScan sets it via reflection.
package pg_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// subx* identifiers — shared pg_test namespace with all other test files, so
// every helper/filler/const added here carries the subx prefix.

const (
	subxSubmissionID = "01970000-0000-7000-8000-0000000000aa"
	subxAssessmentID = "01970000-0000-7000-8000-0000000000ac"
	subxLearnerGCID  = "01970000-0000-7000-9000-000000000002"
	subxActorGCID    = "01970000-0000-7000-9000-000000000003"
	subxAnswerID     = "01970000-0000-7000-8000-0000000000bb"
	subxTSQID        = "01970000-0000-7000-8000-0000000000cc"
	subxQuestionID   = "01970000-0000-7000-8000-0000000000dd"
	subxTSQID2       = "01970000-0000-7000-8000-0000000000ee"
)

// subxFixedTime is the canonical timestamp used across row fillers.
var subxFixedTime = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

// -----------------------------------------------------------------------------
// Row fillers — write every dest in scanSubmissionRow / scanSubmissionAnswerRow
// order. Zero time marks an sql.NullTime as invalid (NULL); "" marks an
// sql.NullString as invalid. The textArray dest for mcq_choice_ids is set via
// reflection (the concrete type lives in the pg package and cannot be asserted
// from pg_test).
// -----------------------------------------------------------------------------

// subxScan writes val into a scan dest, dispatching on the dest's concrete
// type. The pg-side sql.Null* aliases are exactly database/sql types, so the
// assertions below match what scanSubmissionRow/scanSubmissionAnswerRow pass.
func subxScan(dest any, val any) {
	switch d := dest.(type) {
	case *string:
		*d = val.(string)
	case *int:
		*d = val.(int)
	case *float64:
		*d = val.(float64)
	case *time.Time:
		*d = val.(time.Time)
	case *[]byte:
		*d = val.([]byte)
	case *sql.NullString:
		d.String = val.(string)
		d.Valid = d.String != ""
	case *sql.NullBool:
		d.Bool = val.(bool)
		d.Valid = true
	case *sql.NullFloat64:
		d.Float64 = val.(float64)
		d.Valid = true
	case *sql.NullTime:
		d.Time = val.(time.Time)
		d.Valid = !d.Time.IsZero()
	default:
		// textArray ([]string) and friends — set through reflection.
		rv := reflect.ValueOf(dest)
		if rv.Kind() == reflect.Ptr && !rv.IsNil() && rv.Elem().Kind() == reflect.Slice {
			sv := reflect.ValueOf(val)
			if sv.Type().AssignableTo(rv.Elem().Type()) {
				rv.Elem().Set(sv)
			}
		}
	}
}

// subxScanRow applies vals to dest in lockstep; a county mismatch means the
// filler drifted from the scan order and must fail loudly.
func subxScanRow(dest, vals []any) error {
	if len(dest) != len(vals) {
		return fmt.Errorf("pg_test: filler/cursor dest mismatch: %d dests vs %d vals", len(dest), len(vals))
	}
	for i := range vals {
		subxScan(dest[i], vals[i])
	}
	return nil
}

// subxSubmissionRow fills scanSubmissionRow's 29 dests. Optional time fields
// are valid (non-zero) so the pointer-promotion bodies run; deleted_at is NULL
// (soft-deleted rows are filtered by SQL anyway).
func subxSubmissionRow(dest ...any) error {
	return subxScanRow(dest, []any{
		subxSubmissionID, // submission_id
		subxAssessmentID, // assessment_id
		tenantID,         // tenant_id
		subxLearnerGCID,  // learner_gcid
		1,                // attempt_number
		string(delivery.SubmissionStateInProgress), // state
		subxFixedTime,                         // opens_at
		subxFixedTime.Add(time.Hour),          // closes_at
		3600,                                  // time_limit_secs
		subxFixedTime,                         // started_at
		subxFixedTime.Add(time.Minute),        // last_saved_at
		subxFixedTime.Add(2 * time.Minute),    // submitted_at
		subxFixedTime.Add(3 * time.Minute),    // graded_at
		subxFixedTime.Add(4 * time.Minute),    // released_at
		8.0,                                   // mcq_score
		0.0,                                   // oe_score
		8.0,                                   // total_score
		10,                                    // max_score
		80,                                    // passing_percent
		true,                                  // passed
		time.Time{},                           // deleted_at — NULL
		subxFixedTime.Add(-time.Hour),         // created_at
		subxFixedTime,                         // updated_at
		string(delivery.ReviewStatusApproved), // review_status
		subxActorGCID,                         // approved_by_gcid
		subxFixedTime.Add(5 * time.Minute),    // approved_at
		"good work",                           // overall_comment
		"good work",                           // ai_overall_comment
		string(delivery.ProvenanceHuman),      // overall_comment_provenance
	})
}

// subxAnswerRow fills scanSubmissionAnswerRow's 28 dests. mcq_choice_ids is
// written as *textArray via the reflection branch of subxScan.
func subxAnswerRow(dest ...any) error {
	return subxScanRow(dest, []any{
		subxAnswerID,                     // answer_id
		subxSubmissionID,                 // submission_id
		subxTSQID,                        // test_set_question_id
		subxQuestionID,                   // question_id
		string(delivery.QuestionTypeMCQ), // question_type
		"c1",                             // mcq_choice_id
		[]string{"c1", "c2"},             // mcq_choice_ids (textArray)
		"",                               // oe_response_text — NULL
		subxFixedTime,                    // answered_at
		true,                             // mcq_correct
		4.0,                              // points_earned
		4,                                // points_possible
		"",                               // oe_feedback — NULL
		[]byte(`{"criterion":0.9}`),      // oe_criterion_jsonb
		"",                               // grading_dispatch — NULL
		subxFixedTime,                    // oe_graded_at
		"",                               // oe_comment — NULL
		"",                               // oe_ai_comment — NULL
		9.5,                              // ai_points_earned
		string(delivery.ProvenanceAI),    // score_provenance
		string(delivery.ProvenanceAI),    // comment_provenance
		"",                               // amended_model_answer — NULL
		string(delivery.ProvenanceAI),    // model_answer_provenance
		true,                             // quality_flagged
		"grading-model-1",                // grading_model_id
		"grading-response-1",             // grading_response_id
		"",                               // overridden_by_gcid — NULL
		subxFixedTime,                    // overridden_at
	})
}

// subxNoRows is pgx's no-row sentinel that isNoRows recognises.
var subxNoRows = errors.New("no rows in result set")

// -----------------------------------------------------------------------------
// Nil TxRunner — every SubmissionRepo method must fail fast with
// ErrNotImplemented before touching sqls.
// -----------------------------------------------------------------------------

func TestSubmissionRepo_NilTxRunner_AllMethods_ReturnErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewSubmissionRepo(nil)
	ctx := context.Background()
	sub := &delivery.Submission{TenantID: tenantID, ID: subxSubmissionID}

	checks := []struct {
		name string
		fn   func() error
	}{
		{"Save", func() error { return r.Save(ctx, sub) }},
		{"Get", func() error { _, _, e := r.Get(ctx, tenantID, subxSubmissionID); return e }},
		{"GetInProgressByLearner", func() error {
			_, _, e := r.GetInProgressByLearner(ctx, tenantID, subxAssessmentID, subxLearnerGCID)
			return e
		}},
		{"ListByAssessment", func() error { _, _, e := r.ListByAssessment(ctx, tenantID, subxAssessmentID, 10, ""); return e }},
		{"ListByLearner", func() error { _, e := r.ListByLearner(ctx, tenantID, subxLearnerGCID); return e }},
		{"ListReleasedByLearner", func() error { _, e := r.ListReleasedByLearner(ctx, tenantID, subxLearnerGCID, 10, 0); return e }},
		{"CountAttemptsByLearner", func() error {
			_, e := r.CountAttemptsByLearner(ctx, tenantID, subxAssessmentID, subxLearnerGCID)
			return e
		}},
		{"FindLatestByLearner", func() error {
			_, _, e := r.FindLatestByLearner(ctx, tenantID, subxAssessmentID, subxLearnerGCID)
			return e
		}},
		{"AppendGradeOverrides", func() error {
			return r.AppendGradeOverrides(ctx, tenantID, subxSubmissionID, []delivery.GradeOverride{{Field: "SCORE"}})
		}},
	}
	for _, c := range checks {
		err := c.fn()
		if !errors.Is(err, pg.ErrNotImplemented) {
			t.Fatalf("%s on nil TxRunner: expected ErrNotImplemented; got %v", c.name, err)
		}
	}
}

// -----------------------------------------------------------------------------
// Get — SELECT by submission_id + tenant, then loadAnswers
// -----------------------------------------------------------------------------

func TestSubmissionRepo_Get_Hit_LoadsSubmissionAndAnswers(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: subxSubmissionRow}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{subxAnswerRow}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	sub, ok, err := r.Get(ctx, tenantID, subxSubmissionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || sub == nil {
		t.Fatalf("expected ok=true with a hit row; got ok=%v sub=%v", ok, sub)
	}
	if sub.ID != subxSubmissionID || sub.AssessmentID != subxAssessmentID {
		t.Fatalf("Get rehydrated wrong keys: id=%q assessment=%q", sub.ID, sub.AssessmentID)
	}
	if sub.State != delivery.SubmissionStateInProgress {
		t.Fatalf("expected IN_PROGRESS state; got %q", sub.State)
	}
	if sub.Passed == nil || !*sub.Passed {
		t.Fatalf("expected passed=*true; got %v", sub.Passed)
	}
	if sub.ApprovedAt == nil || sub.OverallComment != "good work" {
		t.Fatalf("expected approved_at + overall_comment rehydrated; got %+v", sub)
	}
	if len(sub.Answers) != 1 {
		t.Fatalf("expected 1 loaded answer; got %d", len(sub.Answers))
	}
	a := sub.Answers[0]
	if a.TestSetQuestionID != subxTSQID || a.QuestionType != delivery.QuestionTypeMCQ {
		t.Fatalf("answer keys wrong: %+v", a)
	}
	if len(a.MCQChoiceIDs) != 2 || a.MCQChoiceIDs[0] != "c1" {
		t.Fatalf("expected mcq_choice_ids {c1,c2} scanned; got %v", a.MCQChoiceIDs)
	}
	if a.MCQCorrect == nil || !*a.MCQCorrect {
		t.Fatalf("expected mcq_correct=*true; got %v", a.MCQCorrect)
	}
	if a.OECriterionJSON == "" || a.QualityFlagged != true {
		t.Fatalf("expected criterion JSON + quality flag scanned; got %+v", a)
	}
	if a.OverriddenAt == nil {
		t.Fatalf("expected overridden_at scanned; got %+v", a)
	}

	// RLS first, then the by-id SELECT, then the answers SELECT.
	if len(q.sqls) < 3 {
		t.Fatalf("expected >=3 SQLs (SET LOCAL + SELECT + answers); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	if !strings.Contains(sel, "FROM submissions") || !strings.Contains(sel, "WHERE submission_id = $1") {
		t.Fatalf("by-id SELECT malformed:\n%s", sel)
	}
	if args := q.args[1]; len(args) != 2 || args[0] != subxSubmissionID || args[1] != tenantID {
		t.Fatalf("by-id binds wrong: %v", args)
	}
	ansSel := q.sqls[2]
	if !strings.Contains(ansSel, "FROM submission_answers") || !strings.Contains(ansSel, "ORDER BY answered_at ASC") {
		t.Fatalf("answers SELECT malformed:\n%s", ansSel)
	}
	if args := q.args[2]; len(args) != 1 || args[0] != subxSubmissionID {
		t.Fatalf("answers bind must be submission_id; got %v", args)
	}
}

func TestSubmissionRepo_Get_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return subxNoRows }}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	sub, ok, err := r.Get(context.Background(), tenantID, "nope")
	if err != nil {
		t.Fatalf("miss must not error; got %v", err)
	}
	if ok || sub != nil {
		t.Fatalf("expected ok=false / nil sub on miss; got ok=%v sub=%v", ok, sub)
	}
}

func TestSubmissionRepo_Get_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("bad row bytes") }}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, ok, err := r.Get(context.Background(), tenantID, subxSubmissionID)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got ok=%v err=%v", ok, err)
	}
}

// -----------------------------------------------------------------------------
// GetInProgressByLearner — SELECT ... state IN ('STARTED','IN_PROGRESS')
// -----------------------------------------------------------------------------

func TestSubmissionRepo_GetInProgressByLearner_Hit(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: subxSubmissionRow}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{subxAnswerRow}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	sub, ok, err := r.GetInProgressByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err != nil || !ok || sub == nil {
		t.Fatalf("expected hit; ok=%v err=%v sub=%v", ok, err, sub)
	}
	if len(sub.Answers) != 1 {
		t.Fatalf("expected answers loaded; got %d", len(sub.Answers))
	}
	sel := q.sqls[1]
	for _, want := range []string{
		"FROM submissions",
		"state IN ('STARTED','IN_PROGRESS')",
		"ORDER BY attempt_number DESC",
		"LIMIT 1",
	} {
		if !strings.Contains(sel, want) {
			t.Fatalf("in-progress SELECT missing %q:\n%s", want, sel)
		}
	}
	if args := q.args[1]; len(args) != 3 || args[0] != tenantID || args[1] != subxAssessmentID || args[2] != subxLearnerGCID {
		t.Fatalf("in-progress binds wrong: %v", args)
	}
}

func TestSubmissionRepo_GetInProgressByLearner_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return subxNoRows }}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	sub, ok, err := r.GetInProgressByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err != nil || ok || sub != nil {
		t.Fatalf("expected miss; ok=%v err=%v sub=%v", ok, err, sub)
	}
}

// -----------------------------------------------------------------------------
// ListByAssessment / ListByLearner — row loops over scanSubmissionRow (answers
// are NOT lazy-loaded on the list paths — comment in ListByAssessment).
// -----------------------------------------------------------------------------

func TestSubmissionRepo_ListByAssessment_EmptyRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	items, _, err := r.ListByAssessment(context.Background(), tenantID, subxAssessmentID, 0, "")
	if err != nil {
		t.Fatalf("ListByAssessment: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items; got %d", len(items))
	}
	sel := q.sqls[1]
	if !strings.Contains(sel, "ORDER BY created_at DESC") || !strings.Contains(sel, "LIMIT $3") {
		t.Fatalf("list-by-assessment SELECT malformed:\n%s", sel)
	}
	// pageSize <= 0 defaults to 20 — the guarded value is what gets bound.
	if args := q.args[1]; len(args) != 3 || args[0] != tenantID || args[1] != subxAssessmentID || args[2] != 20 {
		t.Fatalf("expected (tenant, assessment, 20) binds; got %v", args)
	}
}

func TestSubmissionRepo_ListByAssessment_RowsHit(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{subxSubmissionRow, subxSubmissionRow}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	items, _, err := r.ListByAssessment(context.Background(), tenantID, subxAssessmentID, 5, "")
	if err != nil {
		t.Fatalf("ListByAssessment: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items; got %d", len(items))
	}
	// List view leaves Answers nil (lazy-load is Get's job).
	if len(items[0].Answers) != 0 {
		t.Fatalf("list path must not load answers; got %d", len(items[0].Answers))
	}
	if args := q.args[1]; len(args) != 3 || args[2] != 5 {
		t.Fatalf("expected pageSize=5 bound; got %v", args)
	}
}

func TestSubmissionRepo_ListByAssessment_PageSizeGuards(t *testing.T) {
	t.Parallel()
	r := pg.NewSubmissionRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()

	if _, _, err := r.ListByAssessment(ctx, tenantID, subxAssessmentID, 0, ""); err != nil {
		t.Fatalf("ListByAssessment: %v", err)
	}
	q := &stubQuerier{}
	if _, _, err := pg.NewSubmissionRepo(&stubTxRunner{q: q}).ListByAssessment(ctx, tenantID, subxAssessmentID, 999, ""); err != nil {
		t.Fatalf("ListByAssessment: %v", err)
	}
	if args := q.args[1]; len(args) != 3 || args[2] != 200 {
		t.Fatalf("expected pageSize clamped to 200; got %v", args)
	}
}

func TestSubmissionRepo_ListByAssessment_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, _, err := r.ListByAssessment(context.Background(), tenantID, subxAssessmentID, 10, "")
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected query error to propagate; got %v", err)
	}
}

func TestSubmissionRepo_ListByAssessment_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, _, err := r.ListByAssessment(context.Background(), tenantID, subxAssessmentID, 10, "")
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

func TestSubmissionRepo_ListByLearner_EmptyRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	items, err := r.ListByLearner(context.Background(), tenantID, subxLearnerGCID)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items; got %d", len(items))
	}
	sel := q.sqls[1]
	if !strings.Contains(sel, "FROM submissions") || !strings.Contains(sel, "ORDER BY created_at DESC") {
		t.Fatalf("list-by-learner SELECT malformed:\n%s", sel)
	}
	if strings.Contains(sel, "LIMIT") {
		t.Fatalf("list-by-learner must not carry a LIMIT:\n%s", sel)
	}
	if args := q.args[1]; len(args) != 2 || args[0] != tenantID || args[1] != subxLearnerGCID {
		t.Fatalf("expected (tenant, learner) binds; got %v", args)
	}
}

func TestSubmissionRepo_ListByLearner_RowsHit(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{subxSubmissionRow}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	items, err := r.ListByLearner(context.Background(), tenantID, subxLearnerGCID)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(items) != 1 || items[0].ID != subxSubmissionID {
		t.Fatalf("expected 1 rehydrated item; got %d (%v)", len(items), items)
	}
}

func TestSubmissionRepo_ListByLearner_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, err := r.ListByLearner(context.Background(), tenantID, subxLearnerGCID)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListReleasedByLearner — completes the remaining branches (row loop, query +
// scan errors) on top of submission_test.go's SQL-shape coverage.
// -----------------------------------------------------------------------------

func TestSubmissionRepo_ListReleasedByLearner_RowsHit(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{subxSubmissionRow}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	items, err := r.ListReleasedByLearner(context.Background(), tenantID, subxLearnerGCID, 21, 20)
	if err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	if len(items) != 1 || items[0].State != delivery.SubmissionStateInProgress {
		t.Fatalf("expected 1 rehydrated item; got %d", len(items))
	}
	if args := q.args[len(q.args)-1]; len(args) != 4 || args[2] != 21 || args[3] != 20 {
		t.Fatalf("limit/offset binds wrong: %v", args)
	}
}

func TestSubmissionRepo_ListReleasedByLearner_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, err := r.ListReleasedByLearner(context.Background(), tenantID, subxLearnerGCID, 21, 0)
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected query error to propagate; got %v", err)
	}
}

func TestSubmissionRepo_ListReleasedByLearner_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, err := r.ListReleasedByLearner(context.Background(), tenantID, subxLearnerGCID, 21, 0)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// CountAttemptsByLearner — COUNT QueryRow scan
// -----------------------------------------------------------------------------

func TestSubmissionRepo_CountAttemptsByLearner_ReturnsCount(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*int); ok {
					*ptr = 3
				}
				return nil
			}}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	n, err := r.CountAttemptsByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err != nil {
		t.Fatalf("CountAttemptsByLearner: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected count 3; got %d", n)
	}
	if !strings.Contains(q.sqls[1], "SELECT count(*) FROM submissions") {
		t.Fatalf("COUNT SELECT malformed:\n%s", q.sqls[1])
	}
	if args := q.args[1]; len(args) != 3 || args[0] != tenantID || args[1] != subxAssessmentID || args[2] != subxLearnerGCID {
		t.Fatalf("COUNT binds wrong: %v", args)
	}
}

func TestSubmissionRepo_CountAttemptsByLearner_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("boom") }}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, err := r.CountAttemptsByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// FindLatestByLearner — ORDER BY attempt_number DESC, created_at DESC LIMIT 1
// -----------------------------------------------------------------------------

func TestSubmissionRepo_FindLatestByLearner_Hit(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: subxSubmissionRow}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	sub, ok, err := r.FindLatestByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err != nil || !ok || sub == nil {
		t.Fatalf("expected hit; ok=%v err=%v", ok, err)
	}
	if sub.ID != subxSubmissionID {
		t.Fatalf("expected latest submission id %q; got %q", subxSubmissionID, sub.ID)
	}
	sel := q.sqls[1]
	for _, want := range []string{"ORDER BY attempt_number DESC, created_at DESC", "LIMIT 1"} {
		if !strings.Contains(sel, want) {
			t.Fatalf("latest SELECT missing %q:\n%s", want, sel)
		}
	}
	if args := q.args[1]; len(args) != 3 || args[0] != tenantID || args[1] != subxAssessmentID || args[2] != subxLearnerGCID {
		t.Fatalf("latest binds wrong: %v", args)
	}
}

func TestSubmissionRepo_FindLatestByLearner_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return subxNoRows }}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	sub, ok, err := r.FindLatestByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err != nil || ok || sub != nil {
		t.Fatalf("expected miss; ok=%v err=%v sub=%v", ok, err, sub)
	}
}

// -----------------------------------------------------------------------------
// Save — UPSERT submission + answers, nil/error sentinels, nullableUUIDArray
// -----------------------------------------------------------------------------

func TestSubmissionRepo_Save_Errors(t *testing.T) {
	t.Parallel()
	r := pg.NewSubmissionRepo(&stubTxRunner{q: &stubQuerier{}})

	if err := r.Save(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "pg: submission is nil") {
		t.Fatalf("nil submission: expected 'pg: submission is nil'; got %v", err)
	}
	if err := r.Save(context.Background(), &delivery.Submission{}); err == nil || !strings.Contains(err.Error(), "pg: submission.TenantID required") {
		t.Fatalf("empty tenant: expected 'pg: submission.TenantID required'; got %v", err)
	}
}

// subxFullSubmission builds a Save input with every nullable field set so the
// non-null coercions (nullTimePtr / nullFloatPtr / nullableUUIDArray non-empty)
// all run; subxSparseAnswer exercises the nil/empty branches.
func subxFullSubmission() *delivery.Submission {
	passed := true
	aiPts := 9.5
	subxFixedTime2 := subxFixedTime
	return &delivery.Submission{
		ID: subxSubmissionID, AssessmentID: subxAssessmentID, TenantID: tenantID,
		LearnerGCID: subxLearnerGCID, AttemptNumber: 1,
		State:   delivery.SubmissionStateInProgress,
		OpensAt: subxFixedTime, ClosesAt: subxFixedTime2.Add(time.Hour),
		TimeLimitSecs: 3600, StartedAt: subxFixedTime,
		LastSavedAt: &subxFixedTime2, SubmittedAt: &subxFixedTime2,
		GradedAt: &subxFixedTime2, ReleasedAt: &subxFixedTime2,
		MCQScore: 8, OEScore: 0, TotalScore: 8, MaxScore: 10, PassingPercent: 80,
		Passed: &passed, CreatedAt: subxFixedTime, UpdatedAt: subxFixedTime,
		ReviewStatus: delivery.ReviewStatusApproved, ApprovedByGCID: subxActorGCID,
		ApprovedAt: &subxFixedTime2, OverallComment: "good work", AIOverallComment: "good work",
		OverallCommentProvenance: delivery.ProvenanceHuman,
		Answers: []delivery.SubmissionAnswer{
			{
				TestSetQuestionID: subxTSQID, QuestionID: subxQuestionID,
				QuestionType: delivery.QuestionTypeMCQ, MCQChoiceID: "c1",
				MCQChoiceIDs:   []string{"c1", "c2"},
				OEResponseText: "", AnsweredAt: &subxFixedTime2,
				MCQCorrect: &passed, PointsEarned: 4, PointsPossible: 4,
				OEFeedback: "", OECriterionJSON: `{"criterion":0.9}`,
				GradingDispatch: delivery.GradingDispatchDeterministic, OEGradedAt: &subxFixedTime2,
				OEComment: "", AIComment: "", AIPointsEarned: &aiPts,
				ScoreProvenance: delivery.ProvenanceAI, CommentProvenance: delivery.ProvenanceAI,
				AmendedModelAnswer: "", ModelAnswerProvenance: delivery.ProvenanceAI,
				QualityFlagged: true, GradingModelID: "grading-model-1", GradingResponseID: "grading-response-1",
				OverriddenByGCID: "", OverriddenAt: &subxFixedTime2,
			},
			{
				// Sparse answer — nil MCQCorrect / AnsweredAt / AIPointsEarned,
				// empty MCQChoiceIDs + OECriterionJSON → nil bind coercions.
				TestSetQuestionID: subxTSQID2, QuestionType: delivery.QuestionTypeOE,
				OEResponseText: "essay",
			},
		},
	}
}

func TestSubmissionRepo_Save_UpsertsSubmissionThenAnswers(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})
	sub := subxFullSubmission()

	if err := r.Save(context.Background(), sub); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) != 4 {
		t.Fatalf("expected 4 SQLs (SET LOCAL + submission upsert + 2 answers); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	subSQL := q.sqls[1]
	if !strings.Contains(subSQL, "INSERT INTO submissions") || !strings.Contains(subSQL, "ON CONFLICT (assessment_id, learner_gcid, attempt_number)") {
		t.Fatalf("submission upsert malformed:\n%s", subSQL)
	}
	subArgs := q.args[1]
	if len(subArgs) != 29 {
		t.Fatalf("expected 29 submission binds; got %d (%v)", len(subArgs), subArgs)
	}
	if subArgs[0] != subxSubmissionID || subArgs[2] != tenantID || subArgs[5] != string(delivery.SubmissionStateInProgress) {
		t.Fatalf("submission binds wrong at head: %v", subArgs[:6])
	}
	// passed / nullString coercions.
	if subArgs[19] != true {
		t.Fatalf("expected passed bound as bool; got %#v", subArgs[19])
	}
	if subArgs[23] != string(delivery.ReviewStatusApproved) {
		t.Fatalf("expected approved review_status bound; got %#v", subArgs[23])
	}
	ansSQL := q.sqls[2]
	if !strings.Contains(ansSQL, "INSERT INTO submission_answers") || !strings.Contains(ansSQL, "ON CONFLICT (submission_id, test_set_question_id)") {
		t.Fatalf("answer upsert malformed:\n%s", ansSQL)
	}
	// Fully-populated answer: every coercion non-nil.
	full := q.args[2]
	if len(full) != 28 {
		t.Fatalf("expected 28 answer binds; got %d", len(full))
	}
	if full[1] != subxSubmissionID || full[6] != "{c1,c2}" {
		t.Fatalf("answer binds wrong (id / mcq_choice_ids): %v", full[:7])
	}
	if full[8] != subxFixedTime || full[9] != true {
		t.Fatalf("expected answered_at + mcq_correct=true bound; got %#v / %#v", full[8], full[9])
	}
	if full[13] == nil {
		t.Fatalf("expected non-empty criterion JSON bound")
	}
	if full[18] != 9.5 || full[23] != true {
		t.Fatalf("expected ai_points_earned + quality_flagged; got %#v / %#v", full[18], full[23])
	}
	if full[27] != subxFixedTime {
		t.Fatalf("expected overridden_at bound; got %#v", full[27])
	}
	// Sparse answer: nil coercions (nullableUUIDArray empty + nil pointers).
	sparse := q.args[3]
	if len(sparse) != 28 {
		t.Fatalf("expected 28 sparse-answer binds; got %d", len(sparse))
	}
	if sparse[2] != subxTSQID2 || sparse[6] != nil {
		t.Fatalf("expected empty mcq_choice_ids → NULL; got %#v (tsq=%v)", sparse[6], sparse[2])
	}
	if sparse[8] != (time.Time{}) || sparse[9] != nil {
		t.Fatalf("expected zero answered_at + nil mcq_correct; got %#v / %#v", sparse[8], sparse[9])
	}
	if sparse[13] != nil || sparse[18] != nil || sparse[27] != nil {
		t.Fatalf("expected nil criterion / ai_points / overridden_at; got %#v / %#v / %#v", sparse[13], sparse[18], sparse[27])
	}
}

func TestSubmissionRepo_Save_UpsertSubmissionError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO submissions") {
				return errors.New("conn closed")
			}
			return nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	err := r.Save(context.Background(), subxFullSubmission())
	if err == nil || !strings.Contains(err.Error(), "pg: upsert submission") || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected wrapped upsert submission error; got %v", err)
	}
}

func TestSubmissionRepo_Save_UpsertAnswerError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO submission_answers") {
				return errors.New("conn closed")
			}
			return nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	err := r.Save(context.Background(), subxFullSubmission())
	if err == nil || !strings.Contains(err.Error(), "pg: upsert submission_answer") || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected wrapped upsert answer error; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// AppendGradeOverrides — append-only HITL audit rows (ADR-172 §D7)
// -----------------------------------------------------------------------------

func TestSubmissionRepo_AppendGradeOverrides_EmptyOverrides_Noop(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	if err := r.AppendGradeOverrides(context.Background(), tenantID, subxSubmissionID, nil); err != nil {
		t.Fatalf("empty overrides must no-op; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("empty overrides must not execute SQL; got %d SQLs", len(q.sqls))
	}
}

func TestSubmissionRepo_AppendGradeOverrides_InsertsAuditRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})
	at := subxFixedTime
	overrides := []delivery.GradeOverride{
		{Field: "SCORE", TestSetQuestionID: subxTSQID, QuestionID: subxQuestionID,
			OldValue: "2.0", NewValue: "3.5", ActorGCID: subxActorGCID, Reason: "review", At: at},
		{Field: "OVERALL_COMMENT", OldValue: "draft", NewValue: "final", ActorGCID: subxActorGCID, At: at},
	}

	if err := r.AppendGradeOverrides(context.Background(), tenantID, subxSubmissionID, overrides); err != nil {
		t.Fatalf("AppendGradeOverrides: %v", err)
	}
	if len(q.sqls) != 3 {
		t.Fatalf("expected 3 SQLs (SET LOCAL + 2 inserts); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	for i, want := range []string{"INSERT INTO grade_overrides", "answer_id", "created_at"} {
		if !strings.Contains(q.sqls[1], want) {
			t.Fatalf("grade_override INSERT[%d] missing %q:\n%s", i, want, q.sqls[1])
		}
	}
	for i, args := range q.args[1:] {
		if len(args) != 12 {
			t.Fatalf("override %d: expected 12 binds; got %d (%v)", i, len(args), args)
		}
		if v, ok := args[0].(string); !ok || v == "" {
			t.Fatalf("override %d: expected generated override_id; got %#v", i, args[0])
		}
		if args[1] != tenantID || args[2] != subxSubmissionID {
			t.Fatalf("override %d: tenant/submission binds wrong: %v", i, args[:3])
		}
		if args[3] != nil {
			t.Fatalf("override %d: answer_id must stay NULL; got %#v", i, args[3])
		}
	}
	first := q.args[1]
	if first[6] != "SCORE" || first[4] != subxTSQID || first[9] != subxActorGCID || first[11] != at {
		t.Fatalf("override binds wrong (field/tsq/actor/at): %v", first)
	}
	second := q.args[2]
	if second[4] != nil { // OVERALL_COMMENT carries no test_set_question_id → NULL
		t.Fatalf("expected tsq NULL for OVERALL_COMMENT override; got %#v", second[4])
	}
}

func TestSubmissionRepo_AppendGradeOverrides_InsertError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO grade_overrides") {
				return errors.New("conn closed")
			}
			return nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	err := r.AppendGradeOverrides(context.Background(), tenantID, subxSubmissionID, []delivery.GradeOverride{{Field: "SCORE"}})
	if err == nil || !strings.Contains(err.Error(), "pg: insert grade_override") || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected wrapped insert error; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Remaining branch coverage — RLS ApplySession failure, loadAnswers error
// propagation, non-miss scan errors, and the deletedAt pointer-promotion body.
// -----------------------------------------------------------------------------

// subxSubmissionRowWithDeletedAt fills like subxSubmissionRow but marks
// deleted_at valid so scanSubmissionRow's DeletedAt promotion body runs (the
// default filler leaves it NULL — soft-deleted rows are filtered by SQL).
func subxSubmissionRowWithDeletedAt(dest ...any) error {
	if err := subxSubmissionRow(dest...); err != nil {
		return err
	}
	if d, ok := dest[20].(*sql.NullTime); ok {
		*d = sql.NullTime{Time: subxFixedTime.Add(6 * time.Minute), Valid: true}
	}
	return nil
}

func TestSubmissionRepo_RLSError_AllMethods_Propagate(t *testing.T) {
	t.Parallel()
	// A tenant containing a space fails rls.ValidateTenantID → ApplySession
	// errors before any query, exercising the `if err := rls.ApplySession(...)`
	// branch in every method.
	r := pg.NewSubmissionRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()
	sub := &delivery.Submission{TenantID: "bad tenant!", ID: subxSubmissionID}

	checks := []struct {
		name string
		fn   func() error
	}{
		{"Save", func() error { return r.Save(ctx, sub) }},
		{"Get", func() error { _, _, e := r.Get(ctx, "bad tenant!", subxSubmissionID); return e }},
		{"GetInProgressByLearner", func() error {
			_, _, e := r.GetInProgressByLearner(ctx, "bad tenant!", subxAssessmentID, subxLearnerGCID)
			return e
		}},
		{"ListByAssessment", func() error { _, _, e := r.ListByAssessment(ctx, "bad tenant!", subxAssessmentID, 10, ""); return e }},
		{"ListByLearner", func() error { _, e := r.ListByLearner(ctx, "bad tenant!", subxLearnerGCID); return e }},
		{"ListReleasedByLearner", func() error { _, e := r.ListReleasedByLearner(ctx, "bad tenant!", subxLearnerGCID, 10, 0); return e }},
		{"CountAttemptsByLearner", func() error {
			_, e := r.CountAttemptsByLearner(ctx, "bad tenant!", subxAssessmentID, subxLearnerGCID)
			return e
		}},
		{"FindLatestByLearner", func() error {
			_, _, e := r.FindLatestByLearner(ctx, "bad tenant!", subxAssessmentID, subxLearnerGCID)
			return e
		}},
		{"AppendGradeOverrides", func() error {
			return r.AppendGradeOverrides(ctx, "bad tenant!", subxSubmissionID, []delivery.GradeOverride{{Field: "SCORE"}})
		}},
	}
	for _, c := range checks {
		err := c.fn()
		if err == nil || !strings.Contains(err.Error(), "rls:") {
			t.Fatalf("%s with invalid tenant: expected rls error; got %v", c.name, err)
		}
	}
}

func TestSubmissionRepo_Get_AnswerQueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: subxSubmissionRow}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, ok, err := r.Get(context.Background(), tenantID, subxSubmissionID)
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected answers query error to propagate; got ok=%v err=%v", ok, err)
	}
}

func TestSubmissionRepo_Get_AnswerScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: subxSubmissionRow}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad answer row") },
			}}, nil
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, ok, err := r.Get(context.Background(), tenantID, subxSubmissionID)
	if err == nil || !strings.Contains(err.Error(), "bad answer row") {
		t.Fatalf("expected answer scan error to propagate; got ok=%v err=%v", ok, err)
	}
}

func TestSubmissionRepo_GetInProgressByLearner_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("bad row bytes") }}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, ok, err := r.GetInProgressByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got ok=%v err=%v", ok, err)
	}
}

func TestSubmissionRepo_GetInProgressByLearner_AnswerQueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: subxSubmissionRow}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, ok, err := r.GetInProgressByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected answers query error to propagate; got ok=%v err=%v", ok, err)
	}
}

func TestSubmissionRepo_ListByLearner_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, err := r.ListByLearner(context.Background(), tenantID, subxLearnerGCID)
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected query error to propagate; got %v", err)
	}
}

func TestSubmissionRepo_FindLatestByLearner_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("bad row bytes") }}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	_, ok, err := r.FindLatestByLearner(context.Background(), tenantID, subxAssessmentID, subxLearnerGCID)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got ok=%v err=%v", ok, err)
	}
}

func TestSubmissionRepo_Get_ScanWiring_PopulatesOptionalPointers(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: subxSubmissionRowWithDeletedAt}
		},
	}
	r := pg.NewSubmissionRepo(&stubTxRunner{q: q})

	sub, ok, err := r.Get(context.Background(), tenantID, subxSubmissionID)
	if err != nil || !ok || sub == nil {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	for name, p := range map[string]*time.Time{
		"last_saved_at": sub.LastSavedAt,
		"submitted_at":  sub.SubmittedAt,
		"graded_at":     sub.GradedAt,
		"released_at":   sub.ReleasedAt,
		"deleted_at":    sub.DeletedAt,
		"approved_at":   sub.ApprovedAt,
	} {
		if p == nil {
			t.Fatalf("expected %s rehydrated (valid scan value); got nil", name)
		}
	}
	if sub.Passed == nil || !*sub.Passed {
		t.Fatalf("expected passed pointer-promoted; got %v", sub.Passed)
	}
}

// -----------------------------------------------------------------------------
// SQL consts — exported templates must keep their load-bearing shapes.
// -----------------------------------------------------------------------------

func TestSQLSubmissionTemplates_KeepLoadBearingShape(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"INSERT INTO submissions", "ON CONFLICT (assessment_id, learner_gcid, attempt_number)"} {
		if !strings.Contains(pg.SQLUpsertSubmission, want) {
			t.Fatalf("SQLUpsertSubmission missing %q", want)
		}
	}
	for _, want := range []string{"INSERT INTO submission_answers", "ON CONFLICT (submission_id, test_set_question_id)"} {
		if !strings.Contains(pg.SQLUpsertSubmissionAnswer, want) {
			t.Fatalf("SQLUpsertSubmissionAnswer missing %q", want)
		}
	}
	if !strings.Contains(pg.SQLInsertGradeOverride, "INSERT INTO grade_overrides") {
		t.Fatalf("SQLInsertGradeOverride malformed")
	}
}
