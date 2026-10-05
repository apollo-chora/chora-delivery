// assessment_extra_test.go — AssessmentRepo pg adapter unit tests for the
// branches assessment_test.go leaves at 0%: Get, ListVisibleToLearner,
// MonitorCounts, plus the remaining error paths through Save /
// ListByInstructor / ListByOffering / ReleaseAllSubmissions and the 28-column
// scanAssessmentRow rehydration. Same stub-Querier contract as
// assessment_test.go — no live DB, no integration tags.
package pg_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Shared row filler + stub extras (asx* helper prefix)
// -----------------------------------------------------------------------------

// asxScanRow writes EVERY scanAssessmentRow dest in column order (28 dests):
//
//	 0 id, 1 tenant_id, 2 instructor_gcid, 3 test_set_id        → *string
//	 4 test_set_revision_snapshot                               → *int
//	 5 class_id                                                 → *sql.NullString
//	 6 invited_gcids                                            → *uuidArray (sql.Scanner)
//	 7 title, 8 learner_facing_name, 9 state                    → *string / NullString
//	10 scheduled_open_at, 11 scheduled_close_at                 → *sql.NullTime
//	12 max_attempts, 13 shuffle_questions, 14 shuffle_mcq_opts  → *int / *bool
//	15 accommodations_jsonb,                                    → *[]byte
//	16 total_points, 17 question_count                          → *int
//	18 grading_config_snapshot, 19 release_announcement         → *[]byte / NullString
//	20 published_at, 21 closed_at, 22 results_released_at,
//	23 archived_at, 24 deleted_at                               → *sql.NullTime
//	25 created_at, 26 updated_at                                → *time.Time
//	27 offering_id                                              → *sql.NullString
//
// allTimesValid additionally marks dest[21..24] Valid so the pointer-
// timestamp rehydration branches (and their Valid=false skip) are both
// exercised; the Get-hit test asserts the nil side, the timestamp test the
// non-nil side.
func asxScanRow(allTimesValid bool) func(dest ...any) error {
	openT := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	closeT := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	publishedT := time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC)
	createdT := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	return func(dest ...any) error {
		if v, ok := dest[0].(*string); ok {
			*v = "assess-hit-1"
		}
		if v, ok := dest[1].(*string); ok {
			*v = testTenant
		}
		if v, ok := dest[2].(*string); ok {
			*v = testInstr
		}
		if v, ok := dest[3].(*string); ok {
			*v = testTestSet
		}
		if v, ok := dest[4].(*int); ok {
			*v = 7
		}
		if v, ok := dest[5].(*sql.NullString); ok {
			v.String, v.Valid = "class-1", true
		}
		// invited_gcids — *uuidArray is unexported; drive it through sql.Scanner.
		if sc, ok := dest[6].(interface{ Scan(src any) error }); ok {
			if err := sc.Scan([]byte("{" + testLearner + "}")); err != nil {
				return err
			}
		}
		if v, ok := dest[7].(*string); ok {
			*v = "Phyllis Practice"
		}
		if v, ok := dest[8].(*sql.NullString); ok {
			v.String, v.Valid = "Phyllis", true
		}
		if v, ok := dest[9].(*string); ok {
			*v = string(domain.AssessmentStateOpen)
		}
		if v, ok := dest[10].(*sql.NullTime); ok {
			v.Time, v.Valid = openT, true
		}
		if v, ok := dest[11].(*sql.NullTime); ok {
			v.Time, v.Valid = closeT, true
		}
		if v, ok := dest[12].(*int); ok {
			*v = 1
		}
		if v, ok := dest[13].(*bool); ok {
			*v = true
		}
		if v, ok := dest[14].(*bool); ok {
			*v = false
		}
		if v, ok := dest[15].(*[]byte); ok {
			*v = []byte(`{"extra_time_minutes":30,"allow_open_book":true}`)
		}
		if v, ok := dest[16].(*int); ok {
			*v = 100
		}
		if v, ok := dest[17].(*int); ok {
			*v = 10
		}
		if v, ok := dest[18].(*[]byte); ok {
			*v = []byte(`{"mcq_dispatch":"DETERMINISTIC","oe_dispatch":"LLM_EVALUATOR_AGENT","passing_threshold_percent":70}`)
		}
		if v, ok := dest[19].(*sql.NullString); ok {
			v.String, v.Valid = "announce-now", true
		}
		if v, ok := dest[20].(*sql.NullTime); ok {
			v.Time, v.Valid = publishedT, true
		}
		if allTimesValid {
			extra := []time.Time{
				closeT.Add(1 * time.Hour), publishedT.Add(2 * time.Hour),
				publishedT.Add(3 * time.Hour), publishedT.Add(4 * time.Hour),
			}
			for i, ts := range extra {
				if v, ok := dest[21+i].(*sql.NullTime); ok {
					v.Time, v.Valid = ts, true
				}
			}
		}
		if v, ok := dest[25].(*time.Time); ok {
			*v = createdT
		}
		if v, ok := dest[26].(*time.Time); ok {
			*v = createdT.Add(1 * time.Hour)
		}
		if v, ok := dest[27].(*sql.NullString); ok {
			v.String, v.Valid = "offering-42", true
		}
		return nil
	}
}

// asxRowsIterErr is a Rows stub whose iteration ends with a non-nil Err — the
// one branch stubRows (Err() always nil) cannot reach. Forces the
// `return rows.Err()` propagation path in every list-style repo method.
type asxRowsIterErr struct{}

func (asxRowsIterErr) Next() bool             { return false }
func (asxRowsIterErr) Scan(dest ...any) error { return nil }
func (asxRowsIterErr) Close() error           { return nil }
func (asxRowsIterErr) Err() error             { return errors.New("rows iteration failed") }

// asxAssertApplySessionError runs a repo method against a Querier whose Exec
// fails (execErr) so the rls.ApplySession SET LOCAL fails first and the error
// must propagate unchanged.
func asxAssertApplySessionError(t *testing.T, run func(r *pg.AssessmentRepo) error) {
	t.Helper()
	q := &stubQuerier{execErr: errors.New("set local failed")}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	if err := run(r); err == nil || !strings.Contains(err.Error(), "set local failed") {
		t.Fatalf("expected RLS SET LOCAL failure to propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Nil TxRunner — every method returns ErrNotImplemented (Save covered in
// assessment_test.go)
// -----------------------------------------------------------------------------

func TestAssessmentRepo_NilTxRunner_RemainingMethods(t *testing.T) {
	t.Parallel()
	r := pg.NewAssessmentRepo(nil)
	ctx := context.Background()

	cases := []struct {
		name string
		run  func() error
	}{
		{"Get", func() error {
			_, _, err := r.Get(ctx, tenantID, testAssessID)
			return err
		}},
		{"ListByInstructor", func() error {
			_, _, err := r.ListByInstructor(ctx, tenantID, gcid, 20, "")
			return err
		}},
		{"ListByOffering", func() error {
			_, _, err := r.ListByOffering(ctx, tenantID, gcid, 20, "")
			return err
		}},
		{"ListVisibleToLearner", func() error {
			_, _, err := r.ListVisibleToLearner(ctx, tenantID, gcid, 20, "")
			return err
		}},
		{"MonitorCounts", func() error {
			_, err := r.MonitorCounts(ctx, tenantID, testAssessID)
			return err
		}},
		{"ReleaseAllSubmissions", func() error {
			_, err := r.ReleaseAllSubmissions(ctx, tenantID, testAssessID)
			return err
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, pg.ErrNotImplemented) {
				t.Fatalf("expected ErrNotImplemented; got %v", err)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Get
// -----------------------------------------------------------------------------

func TestAssessmentRepo_Get_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	a, ok, err := r.Get(context.Background(), testTenant, "missing-id")
	if err != nil || ok || a != nil {
		t.Fatalf("Get miss: ok=%v err=%v a=%v", ok, err, a)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL; got %v", q.sqls)
	}
	if !strings.Contains(q.sqls[1], "WHERE assessment_id = $1") {
		t.Fatalf("Get must select by assessment_id; got %q", q.sqls[1])
	}
}

func TestAssessmentRepo_Get_Hit_PopulatesAssessment(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: asxScanRow(false)}
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	a, ok, err := r.Get(context.Background(), testTenant, "assess-hit-1")
	if err != nil || !ok || a == nil {
		t.Fatalf("Get hit: ok=%v err=%v a=%v", ok, err, a)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL; got %v", q.sqls)
	}
	args := q.args[1]
	if len(args) != 2 || args[0] != "assess-hit-1" || args[1] != testTenant {
		t.Fatalf("Get bind args mismatch: %v", args)
	}
	// Full rehydration asserts — mirror the 28-column scan.
	if a.ID != "assess-hit-1" || a.TenantID != testTenant || a.InstructorGCID != testInstr || a.TestSetID != testTestSet {
		t.Fatalf("identity fields wrong: %+v", a)
	}
	if a.TestSetRevisionSnapshot != 7 {
		t.Fatalf("revSnapshot=7, got %d", a.TestSetRevisionSnapshot)
	}
	if a.ClassID != "class-1" || a.OfferingID != "offering-42" {
		t.Fatalf("nullable refs wrong: class=%q offering=%q", a.ClassID, a.OfferingID)
	}
	if len(a.InvitedGCIDs) != 1 || a.InvitedGCIDs[0] != testLearner {
		t.Fatalf("InvitedGCIDs=%v, want [%s]", a.InvitedGCIDs, testLearner)
	}
	if a.Title != "Phyllis Practice" || a.LearnerFacingName != "Phyllis" {
		t.Fatalf("title/learner name wrong: %q / %q", a.Title, a.LearnerFacingName)
	}
	if a.State != domain.AssessmentStateOpen {
		t.Fatalf("state=%s, want OPEN", a.State)
	}
	if !a.ScheduledOpenAt.Equal(time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)) ||
		!a.ScheduledCloseAt.Equal(time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("window wrong: open=%v close=%v", a.ScheduledOpenAt, a.ScheduledCloseAt)
	}
	if a.MaxAttempts != 1 || !a.ShuffleQuestions || a.ShuffleMCQOptions {
		t.Fatalf("attempts/shuffle wrong: max=%d q=%v mcq=%v", a.MaxAttempts, a.ShuffleQuestions, a.ShuffleMCQOptions)
	}
	if a.TotalPoints != 100 || a.QuestionCount != 10 {
		t.Fatalf("points/count wrong: %d / %d", a.TotalPoints, a.QuestionCount)
	}
	if a.Accommodations == nil || a.Accommodations.ExtraTimeMinutes != 30 || !a.Accommodations.AllowOpenBook {
		t.Fatalf("accommodations not rehydrated: %+v", a.Accommodations)
	}
	if a.GradingConfigSnapshot.MCQDispatch != "DETERMINISTIC" || a.GradingConfigSnapshot.PassingThresholdPercent != 70 {
		t.Fatalf("grading config not rehydrated: %+v", a.GradingConfigSnapshot)
	}
	if a.ReleaseAnnouncement != "announce-now" {
		t.Fatalf("release announcement=%q", a.ReleaseAnnouncement)
	}
	if a.PublishedAt == nil || !a.PublishedAt.Equal(time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC)) {
		t.Fatalf("published_at not rehydrated: %v", a.PublishedAt)
	}
	for name, ptr := range map[string]*time.Time{
		"closed": a.ClosedAt, "released": a.ResultsReleasedAt,
		"archived": a.ArchivedAt, "deleted": a.DeletedAt,
	} {
		if ptr != nil {
			t.Fatalf("expected nil %s_at; got %v", name, ptr)
		}
	}
	if !a.CreatedAt.Equal(time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("created_at wrong: %v", a.CreatedAt)
	}
}

func TestAssessmentRepo_Get_Hit_RebuildsPointerTimestamps(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: asxScanRow(true)}
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	a, ok, err := r.Get(context.Background(), testTenant, "assess-hit-1")
	if err != nil || !ok || a == nil {
		t.Fatalf("Get hit: ok=%v err=%v a=%v", ok, err, a)
	}
	for name, ptr := range map[string]*time.Time{
		"closed": a.ClosedAt, "released": a.ResultsReleasedAt,
		"archived": a.ArchivedAt, "deleted": a.DeletedAt,
	} {
		if ptr == nil {
			t.Fatalf("expected non-nil %s_at; got nil", name)
		}
		if ptr.UTC().Equal(*ptr) == false {
			t.Fatalf("%s_at must be UTC-normalised; got %v", name, ptr)
		}
	}
}

func TestAssessmentRepo_Get_OtherScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("conn boom")
			}}
		},
	}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.Get(context.Background(), testTenant, testAssessID)
	if err == nil || !strings.Contains(err.Error(), "conn boom") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_Get_EmptyTenantID_Error(t *testing.T) {
	t.Parallel()
	r := pg.NewAssessmentRepo(&stubTxRunner{q: &stubQuerier{}})
	_, _, err := r.Get(context.Background(), "   ", testAssessID)
	if err == nil || !strings.Contains(err.Error(), "tenantID required") {
		t.Fatalf("expected tenantID required; got %v", err)
	}
}

func TestAssessmentRepo_Get_ApplySessionError_Propagates(t *testing.T) {
	t.Parallel()
	asxAssertApplySessionError(t, func(r *pg.AssessmentRepo) error {
		_, _, err := r.Get(context.Background(), testTenant, testAssessID)
		return err
	})
}

// -----------------------------------------------------------------------------
// ListByInstructor
// -----------------------------------------------------------------------------

func TestAssessmentRepo_ListByInstructor_ReturnsRows_DefaultsPageSize(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{asxScanRow(false)}}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	items, _, err := r.ListByInstructor(context.Background(), testTenant, testInstr, 0, "")
	if err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if len(items) != 1 || items[0].ID != "assess-hit-1" {
		t.Fatalf("want 1 row; got %d", len(items))
	}
	args := q.args[1]
	if len(args) != 3 || args[0] != testTenant || args[1] != testInstr || args[2] != 20 {
		t.Fatalf("pageSize 0 must default to 20; bind args %v", args)
	}
}

func TestAssessmentRepo_ListByInstructor_PageSizeCapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListByInstructor(context.Background(), testTenant, testInstr, 500, ""); err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if args := q.args[1]; len(args) != 3 || args[2] != 200 {
		t.Fatalf("pageSize 500 must cap to 200; bind args %v", args)
	}
}

func TestAssessmentRepo_ListByInstructor_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, errors.New("query boom")
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListByInstructor(context.Background(), testTenant, testInstr, 25, "")
	if err == nil || !strings.Contains(err.Error(), "query boom") {
		t.Fatalf("expected query error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListByInstructor_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListByInstructor(context.Background(), testTenant, testInstr, 25, "")
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListByInstructor_RowsErr_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &asxRowsIterErr{}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListByInstructor(context.Background(), testTenant, testInstr, 25, "")
	if err == nil || !strings.Contains(err.Error(), "rows iteration failed") {
		t.Fatalf("expected rows.Err() to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListByInstructor_ApplySessionError_Propagates(t *testing.T) {
	t.Parallel()
	asxAssertApplySessionError(t, func(r *pg.AssessmentRepo) error {
		_, _, err := r.ListByInstructor(context.Background(), testTenant, testInstr, 25, "")
		return err
	})
}

// -----------------------------------------------------------------------------
// ListByOffering
// -----------------------------------------------------------------------------

func TestAssessmentRepo_ListByOffering_ReturnsRows_BindsOffering(t *testing.T) {
	t.Parallel()
	const testOffering = "01985e7f-1234-7abc-8def-0000000000f1"
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{asxScanRow(false)}}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	items, _, err := r.ListByOffering(context.Background(), testTenant, testOffering, 25, "")
	if err != nil {
		t.Fatalf("ListByOffering: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 row; got %d", len(items))
	}
	args := q.args[1]
	if len(args) != 3 || args[0] != testTenant || args[1] != testOffering || args[2] != 25 {
		t.Fatalf("ListByOffering bind args mismatch: %v", args)
	}
}

func TestAssessmentRepo_ListByOffering_PageSizeDefaultsAndCaps(t *testing.T) {
	t.Parallel()
	const testOffering = "01985e7f-1234-7abc-8def-0000000000f1"
	cases := []struct {
		name     string
		pageSize int
		want     int
	}{
		{"defaults to 20", 0, 20},
		{"caps at 200", 500, 200},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			q := &stubQuerier{}
			r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
			if _, _, err := r.ListByOffering(context.Background(), testTenant, testOffering, tc.pageSize, ""); err != nil {
				t.Fatalf("ListByOffering: %v", err)
			}
			if args := q.args[1]; len(args) != 3 || args[2] != tc.want {
				t.Fatalf("pageSize=%d must bind %d; bind args %v", tc.pageSize, tc.want, args)
			}
		})
	}
}

func TestAssessmentRepo_ListByOffering_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, errors.New("query boom")
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListByOffering(context.Background(), testTenant, "offering-x", 25, "")
	if err == nil || !strings.Contains(err.Error(), "query boom") {
		t.Fatalf("expected query error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListByOffering_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListByOffering(context.Background(), testTenant, "offering-x", 25, "")
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListByOffering_ApplySessionError_Propagates(t *testing.T) {
	t.Parallel()
	asxAssertApplySessionError(t, func(r *pg.AssessmentRepo) error {
		_, _, err := r.ListByOffering(context.Background(), testTenant, "offering-x", 25, "")
		return err
	})
}

// -----------------------------------------------------------------------------
// ListVisibleToLearner
// -----------------------------------------------------------------------------

func TestAssessmentRepo_ListVisibleToLearner_BindsLearnerAndPageSize(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{asxScanRow(false), asxScanRow(false)}}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	items, _, err := r.ListVisibleToLearner(context.Background(), testTenant, testLearner, 25, "")
	if err != nil {
		t.Fatalf("ListVisibleToLearner: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 rows; got %d", len(items))
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL; got %v", q.sqls)
	}
	sql := q.sqls[1]
	for _, want := range []string{
		"FROM assessments a",
		"a.state IN ('SCHEDULED'",
		"bookings b",
		"course_enrollments ce",
		"invited_gcids",
		"LIMIT $3",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("learner-visible SQL missing %q; got %q", want, sql)
		}
	}
	args := q.args[1]
	if len(args) != 3 || args[0] != testTenant || args[1] != testLearner || args[2] != 25 {
		t.Fatalf("ListVisibleToLearner bind args mismatch: %v", args)
	}
}

func TestAssessmentRepo_ListVisibleToLearner_DefaultsPageSize(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListVisibleToLearner(context.Background(), testTenant, testLearner, 0, ""); err != nil {
		t.Fatalf("ListVisibleToLearner: %v", err)
	}
	if args := q.args[1]; len(args) != 3 || args[2] != 20 {
		t.Fatalf("pageSize 0 must default to 20; bind args %v", args)
	}
}

func TestAssessmentRepo_ListVisibleToLearner_PageSizeCapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListVisibleToLearner(context.Background(), testTenant, testLearner, 500, ""); err != nil {
		t.Fatalf("ListVisibleToLearner: %v", err)
	}
	if args := q.args[1]; len(args) != 3 || args[2] != 200 {
		t.Fatalf("pageSize 500 must cap to 200; bind args %v", args)
	}
}

func TestAssessmentRepo_ListVisibleToLearner_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, errors.New("query boom")
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListVisibleToLearner(context.Background(), testTenant, testLearner, 25, "")
	if err == nil || !strings.Contains(err.Error(), "query boom") {
		t.Fatalf("expected query error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListVisibleToLearner_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListVisibleToLearner(context.Background(), testTenant, testLearner, 25, "")
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListVisibleToLearner_RowsErr_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &asxRowsIterErr{}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, _, err := r.ListVisibleToLearner(context.Background(), testTenant, testLearner, 25, "")
	if err == nil || !strings.Contains(err.Error(), "rows iteration failed") {
		t.Fatalf("expected rows.Err() to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ListVisibleToLearner_ApplySessionError_Propagates(t *testing.T) {
	t.Parallel()
	asxAssertApplySessionError(t, func(r *pg.AssessmentRepo) error {
		_, _, err := r.ListVisibleToLearner(context.Background(), testTenant, testLearner, 25, "")
		return err
	})
}

// -----------------------------------------------------------------------------
// MonitorCounts
// -----------------------------------------------------------------------------

// asxMonitorRow stubs the SQLMonitor aggregation QueryRow (7 dests).
func asxMonitorRow(inProgress, started, submitted, graded, released int, avg float64, hasGraded bool) func(dest ...any) error {
	return func(dest ...any) error {
		if v, ok := dest[0].(*int); ok {
			*v = inProgress
		}
		if v, ok := dest[1].(*int); ok {
			*v = started
		}
		if v, ok := dest[2].(*int); ok {
			*v = submitted
		}
		if v, ok := dest[3].(*int); ok {
			*v = graded
		}
		if v, ok := dest[4].(*int); ok {
			*v = released
		}
		if v, ok := dest[5].(*float64); ok {
			*v = avg
		}
		if v, ok := dest[6].(*bool); ok {
			*v = hasGraded
		}
		return nil
	}
}

func TestAssessmentRepo_MonitorCounts_ScansBucketsAndInvited(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		if strings.Contains(sql, "FROM submissions") {
			return stubRow{scanFn: asxMonitorRow(3, 7, 5, 2, 1, 88.5, true)}
		}
		// SQLInvited — cardinality of invited_gcids.
		return stubRow{scanFn: func(dest ...any) error {
			if v, ok := dest[0].(*int); ok {
				*v = 12
			}
			return nil
		}}
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	out, err := r.MonitorCounts(context.Background(), testTenant, testAssessID)
	if err != nil {
		t.Fatalf("MonitorCounts: %v", err)
	}
	if out.InProgressCount != 3 || out.TotalStarted != 7 || out.TotalSubmitted != 5 ||
		out.TotalGraded != 2 || out.TotalReleased != 1 {
		t.Fatalf("bucket counts wrong: %+v", out)
	}
	if !out.HasGradedSamples || out.AverageScorePct != 88.5 {
		t.Fatalf("graded avg wrong: hasGraded=%v avg=%v", out.HasGradedSamples, out.AverageScorePct)
	}
	if out.TotalInvited != 12 {
		t.Fatalf("TotalInvited=%d, want 12", out.TotalInvited)
	}
	if len(q.sqls) < 3 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL; got %v", q.sqls)
	}
	if !strings.Contains(q.sqls[1], "FILTER (WHERE state") || !strings.Contains(q.sqls[1], "avg(") {
		t.Fatalf("SQLMonitor aggregation missing; got %q", q.sqls[1])
	}
	if !strings.Contains(q.sqls[2], "cardinality(invited_gcids)") {
		t.Fatalf("SQLInvited missing; got %q", q.sqls[2])
	}
	// SQLInvited binds (assessmentID, tenantID) — note the swapped order vs $1/$2.
	invArgs := q.args[2]
	if len(invArgs) != 2 || invArgs[0] != testAssessID || invArgs[1] != testTenant {
		t.Fatalf("SQLInvited bind args mismatch: %v", invArgs)
	}
}

func TestAssessmentRepo_MonitorCounts_NoGradedSamples_KeepsAverageZero(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		if strings.Contains(sql, "FROM submissions") {
			return stubRow{scanFn: asxMonitorRow(0, 0, 0, 0, 0, 0, false)}
		}
		return stubRow{scanFn: func(dest ...any) error { return nil }}
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	out, err := r.MonitorCounts(context.Background(), testTenant, testAssessID)
	if err != nil {
		t.Fatalf("MonitorCounts: %v", err)
	}
	if out.HasGradedSamples || out.AverageScorePct != 0 {
		t.Fatalf("no-graded path must keep avg 0; got %+v", out)
	}
}

func TestAssessmentRepo_MonitorCounts_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error {
			return errors.New("count boom")
		}}
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, err := r.MonitorCounts(context.Background(), testTenant, testAssessID)
	if err == nil || !strings.Contains(err.Error(), "count boom") {
		t.Fatalf("expected aggregation scan error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_MonitorCounts_ApplySessionError_Propagates(t *testing.T) {
	t.Parallel()
	asxAssertApplySessionError(t, func(r *pg.AssessmentRepo) error {
		_, err := r.MonitorCounts(context.Background(), testTenant, testAssessID)
		return err
	})
}

// -----------------------------------------------------------------------------
// ReleaseAllSubmissions — remaining branches (happy multi-row path is covered
// in assessment_test.go)
// -----------------------------------------------------------------------------

func TestAssessmentRepo_ReleaseAllSubmissions_NoRows_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	ids, err := r.ReleaseAllSubmissions(context.Background(), testTenant, testAssessID)
	if err != nil {
		t.Fatalf("ReleaseAllSubmissions: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("want 0 ids; got %v", ids)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL; got %v", q.sqls)
	}
}

func TestAssessmentRepo_ReleaseAllSubmissions_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, errors.New("query boom")
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, err := r.ReleaseAllSubmissions(context.Background(), testTenant, testAssessID)
	if err == nil || !strings.Contains(err.Error(), "query boom") {
		t.Fatalf("expected query error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ReleaseAllSubmissions_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("scan boom") },
		}}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, err := r.ReleaseAllSubmissions(context.Background(), testTenant, testAssessID)
	if err == nil || !strings.Contains(err.Error(), "scan boom") {
		t.Fatalf("expected scan error to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ReleaseAllSubmissions_RowsErr_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &asxRowsIterErr{}, nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	_, err := r.ReleaseAllSubmissions(context.Background(), testTenant, testAssessID)
	if err == nil || !strings.Contains(err.Error(), "rows iteration failed") {
		t.Fatalf("expected rows.Err() to propagate; got %v", err)
	}
}

func TestAssessmentRepo_ReleaseAllSubmissions_ApplySessionError_Propagates(t *testing.T) {
	t.Parallel()
	asxAssertApplySessionError(t, func(r *pg.AssessmentRepo) error {
		_, err := r.ReleaseAllSubmissions(context.Background(), testTenant, testAssessID)
		return err
	})
}

// -----------------------------------------------------------------------------
// Save — remaining branches (happy upsert covered in assessment_test.go)
// -----------------------------------------------------------------------------

func TestAssessmentRepo_Save_NilAssessment_Error(t *testing.T) {
	t.Parallel()
	r := pg.NewAssessmentRepo(&stubTxRunner{q: &stubQuerier{}})
	err := r.Save(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "assessment is nil") {
		t.Fatalf("expected nil-assessment error; got %v", err)
	}
}

func TestAssessmentRepo_Save_EmptyTenant_Error(t *testing.T) {
	t.Parallel()
	a := newTestAssessment(t)
	a.TenantID = "   "
	r := pg.NewAssessmentRepo(&stubTxRunner{q: &stubQuerier{}})
	err := r.Save(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "TenantID required") {
		t.Fatalf("expected TenantID-required error; got %v", err)
	}
}

func TestAssessmentRepo_Save_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO assessments") {
			return errors.New("upsert boom")
		}
		return nil
	}}
	r := pg.NewAssessmentRepo(&stubTxRunner{q: q})
	err := r.Save(context.Background(), newTestAssessment(t))
	if err == nil || !strings.Contains(err.Error(), "pg: upsert assessment") ||
		!strings.Contains(err.Error(), "upsert boom") {
		t.Fatalf("expected wrapped upsert error; got %v", err)
	}
}

func TestAssessmentRepo_Save_ApplySessionError_Propagates(t *testing.T) {
	t.Parallel()
	asxAssertApplySessionError(t, func(r *pg.AssessmentRepo) error {
		return r.Save(context.Background(), newTestAssessment(t))
	})
}
