// course_cj2_extra_unit_test.go — extra unit tests for the CJ#2 adapter,
// topping up course_cj2_test.go + course_cj2_cert_test.go.
//
// Covers the 0% CourseRepoCJ2Port forwarders (Save / ListByState), the
// guard-clause + error-wrap branches of SaveCJ2 / GetCJ2 / ListByState /
// ListByStateAndAuthor, and the non-nil-pointer branches of scanCJ2Course
// (the five optional CJ#2 columns) + scanCourseCert's nullable cert fields.
//
// Mirrors the sibling test files' stub discipline: stub Querier, RLS first
// (SET LOCAL), SQL-shape + bind-arg assertions, wrapped-cause error checks.
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

// cj2xAuthor is the author GCID used across these tests.
const cj2xAuthor = "00000000-0000-7000-8000-000000001999"

// cj2xMainCJ2Row fills ALL 22 scanCJ2Course dests, including non-nil pointers
// for the five optional CJ#2 columns (deleted_at, author_gcid, scheduled_open_at,
// review_notes, published_at) so scanCJ2Course's non-nil branches run.
func cj2xMainCJ2Row(dest []any, id string) error {
	if len(dest) != 22 {
		return errors.New("scanCJ2Course: expected 22 destinations")
	}
	now := time.Now().UTC()
	notes := "needs more examples"
	author := cj2xAuthor
	open := now.Add(24 * time.Hour)
	pub := now
	deleted := now.Add(-time.Hour)
	*(dest[0].(*string)) = id
	*(dest[1].(*string)) = tenantID
	*(dest[2].(*string)) = cj2xAuthor
	*(dest[3].(*string)) = "CJ2 full row"
	*(dest[4].(*string)) = "desc"
	*(dest[5].(*[]string)) = []string{"01970000-0000-7000-8000-000000000001"}
	*(dest[6].(*bool)) = true
	*(dest[7].(*int64)) = 12345
	*(dest[8].(*bool)) = false
	*(dest[9].(*int)) = 50
	*(dest[10].(*time.Time)) = now
	*(dest[11].(*time.Time)) = now
	*(dest[12].(**time.Time)) = &deleted
	*(dest[13].(*string)) = "AWAITING_REVIEW"
	*(dest[14].(**string)) = &author
	*(dest[15].(*[]string)) = []string{"01970000-0000-7000-8000-0000000000aa"}
	*(dest[16].(*[]string)) = []string{cj2xAuthor}
	*(dest[17].(**time.Time)) = &open
	*(dest[18].(**string)) = &notes
	*(dest[19].(*[]string)) = []string{"LO1"}
	*(dest[20].(*[]string)) = []string{"PR1"}
	*(dest[21].(**time.Time)) = &pub
	return nil
}

// -----------------------------------------------------------------------------
// CourseRepoCJ2Port forwarders (both were 0%)
// -----------------------------------------------------------------------------

func TestCourseRepoCJ2Port_Save_Forwards(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	port := &pg.CourseRepoCJ2Port{Repo: r}
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := port.Save(ctx, newCJ2DraftCourseForTest(t)); err != nil {
		t.Fatalf("port.Save: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected SET LOCAL + UPSERT; got %d SQLs", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[1], "INSERT INTO courses") {
		t.Fatalf("port.Save must forward to the CJ#2 UPSERT; got %q", q.sqls[1])
	}
}

func TestCourseRepoCJ2Port_ListByState_Forwards(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	port := &pg.CourseRepoCJ2Port{Repo: pg.NewCourseRepo(&stubTxRunner{q: q})}
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := port.ListByState(ctx, tenantID, domain.CourseStateAwaitingReview, "", 0, 10)
	if err != nil {
		t.Fatalf("port.ListByState: %v", err)
	}
	// An empty queue returns a nil/empty slice — ListByState only appends when
	// rows exist (same contract as the empty-query happy paths above).
	if len(out) != 0 {
		t.Fatalf("expected an empty result; got %d courses", len(out))
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected SET LOCAL + SELECT; got %d SQLs", len(q.sqls))
	}
	if !strings.Contains(q.sqls[len(q.sqls)-1], "state = $2") {
		t.Fatalf("forwarder must reach the state-scoped SELECT; got %q", q.sqls[len(q.sqls)-1])
	}
}

// -----------------------------------------------------------------------------
// SaveCJ2 — guard clauses + failure wraps (happy SQL covered by course_cj2_test.go)
// -----------------------------------------------------------------------------

func TestSaveCJ2_NilCourse_ReturnsErrInvalidCourse(t *testing.T) {
	t.Parallel()
	r := pg.NewCourseRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, nil); !errors.Is(err, pg.ErrInvalidCourse) {
		t.Fatalf("expected ErrInvalidCourse; got %v", err)
	}
}

func TestSaveCJ2_EmptyAuthorGCID_ErrorsBeforeSQL(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	c := &domain.Course{ID: "01970000-0000-7000-8000-0000000000aa", TenantID: tenantID, Title: "no author"}
	err := r.SaveCJ2(ctx, c)
	if err == nil || !strings.Contains(err.Error(), "AuthorGCID required") {
		t.Fatalf("expected the AuthorGCID guard error; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("no SQL may run before the guard; got %d", len(q.sqls))
	}
}

func TestSaveCJ2_MirrorsInstructorFromAuthorAndDefaultsStateToDraft(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	// Raw aggregate: no InstructorGCID (legacy column) and no State — the two
	// branches existing tests never hit because NewCJ2Course sets both.
	c := &domain.Course{
		ID:         "01970000-0000-7000-8000-0000000000aa",
		TenantID:   tenantID,
		AuthorGCID: cj2xAuthor,
		Title:      "mirror test",
	}
	if err := r.SaveCJ2(ctx, c); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}
	if c.InstructorGCID != cj2xAuthor {
		t.Fatalf("InstructorGCID must mirror AuthorGCID; got %q", c.InstructorGCID)
	}
	args := q.args[len(q.args)-1]
	if len(args) < 14 {
		t.Fatalf("expected ≥14 bind args for the UPSERT; got %d", len(args))
	}
	if args[2] != cj2xAuthor {
		t.Fatalf("$3 instructor_gcid must be the mirrored author; got %v", args[2])
	}
	if args[13] != "DRAFT" {
		t.Fatalf("empty state must default to DRAFT at $14; got %v", args[13])
	}
	if pub, _ := args[6].(bool); pub {
		t.Fatalf("DRAFT must bind public=false; got true")
	}
}

func TestSaveCJ2_UpsertExecError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("deadline exceeded")
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO courses") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	err := r.SaveCJ2(ctx, certTestCourse(t))
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the UPSERT failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: upsert cj2 course") {
		t.Fatalf("expected the wrap to name the upsert; got %v", err)
	}
}

func TestSaveCJ2_CertUpdateExecError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("cert column not migrated yet")
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "cert_enabled") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q}).EnableCertColumns()
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	err := r.SaveCJ2(ctx, certTestCourse(t))
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the cert UPDATE failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: update cj2 course cert") {
		t.Fatalf("expected the wrap to name the cert update; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// GetCJ2 — miss + cert SELECT failure (happy round-trip covered by the cert tests)
// -----------------------------------------------------------------------------

func TestGetCJ2_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCourseRepo(nil)
	if _, _, err := r.GetCJ2(context.Background(), tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

func TestGetCJ2_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	c, ok, err := r.GetCJ2(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("GetCJ2 miss must not error; got %v", err)
	}
	if ok || c != nil {
		t.Fatalf("expected (nil,false) on miss; got (%+v,%v)", c, ok)
	}
}

func TestGetCJ2_CertSelectError_Wrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("cert row unreadable")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			if strings.Contains(sql, "cert_enabled") {
				return stubRow{scanFn: func(dest ...any) error { return boom }}
			}
			return stubRow{scanFn: func(dest ...any) error { return cj2xMainCJ2Row(dest, courseID) }}
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q}).EnableCertColumns()
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, _, err := r.GetCJ2(ctx, tenantID, courseID)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the cert SELECT failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: select cj2 course cert") {
		t.Fatalf("expected the wrap to name the cert select; got %v", err)
	}
}

// GetCJ2 with cert columns and NULL cert_type / passing_score — the nullable
// branches of scanCourseCert (the cert test only exercises non-nil values).
func TestGetCJ2_WithCertColumns_NullableCertFields(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			if strings.Contains(sql, "cert_enabled") {
				return stubRow{scanFn: func(dest ...any) error {
					*(dest[0].(*bool)) = true
					// cert_type + passing_score stay nil (zero values).
					*(dest[3].(*bool)) = true
					return nil
				}}
			}
			return stubRow{scanFn: func(dest ...any) error { return cj2xMainCJ2Row(dest, courseID) }}
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q}).EnableCertColumns()
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	c, ok, err := r.GetCJ2(ctx, tenantID, courseID)
	if err != nil || !ok || c == nil {
		t.Fatalf("GetCJ2: ok=%v err=%v", ok, err)
	}
	if !c.Certification.Enabled || !c.Certification.RequireAllContent {
		t.Fatalf("bool cert fields must flow through; got %+v", c.Certification)
	}
	if c.Certification.CertType != "" || c.Certification.PassingScorePct != 0 {
		t.Fatalf("nil cert_type / passing must stay zero-valued; got %+v", c.Certification)
	}
}

// -----------------------------------------------------------------------------
// ListByState — query/scan failures + full-row pointer scan (happy SQL shape
// covered by course_cj2_test.go)
// -----------------------------------------------------------------------------

func TestListByState_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCourseRepo(nil)
	if _, err := r.ListByState(context.Background(), tenantID, domain.CourseStatePublished, "", 0, 20); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

func TestListByState_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("queue query conn closed")
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByState(ctx, tenantID, domain.CourseStatePublished, "", 0, 20); err == nil || !strings.Contains(err.Error(), "queue query conn closed") {
		t.Fatalf("expected the SELECT error to propagate; got %v", err)
	}
}

func TestListByState_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad course row bytes") },
			}}, nil
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByState(ctx, tenantID, domain.CourseStatePublished, "", 0, 20); err == nil || !strings.Contains(err.Error(), "bad course row bytes") {
		t.Fatalf("expected the row scan error to propagate; got %v", err)
	}
}

// Full 22-column row with non-nil optional pointers — exercises the non-nil
// branches of scanCJ2Course (deleted_at, author_gcid, scheduled_open_at,
// review_notes, published_at) that the sibling tests leave nil.
func TestListByState_FullRow_PopsulatesOptionalColumns(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return cj2xMainCJ2Row(dest, courseID) },
			}}, nil
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByState(ctx, tenantID, domain.CourseStatePublished, "", 0, 20)
	if err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 course; got %d", len(out))
	}
	c := out[0]
	if c.ID != courseID || c.TenantID != tenantID {
		t.Fatalf("identity columns wrong: %+v", c)
	}
	if c.AuthorGCID != cj2xAuthor {
		t.Fatalf("author_gcid must flow through; got %q", c.AuthorGCID)
	}
	if c.ReviewNotes != "needs more examples" {
		t.Fatalf("review_notes must flow through; got %q", c.ReviewNotes)
	}
	if c.DeletedAt == nil || c.ScheduledOpenAt == nil || c.PublishedAt == nil {
		t.Fatalf("optional pointers must be set: %+v", c)
	}
	if c.State != domain.CourseStateAwaitingReview {
		t.Fatalf("state must flow through; got %s", c.State)
	}
	if len(c.TestSetIDs) != 1 || len(c.InstructorGCIDs) != 1 || len(c.LearningObjectives) != 1 || len(c.PrerequisiteNotes) != 1 {
		t.Fatalf("slash columns wrong: %+v", c)
	}
}

// -----------------------------------------------------------------------------
// ListByStateAndAuthor — nil-tx guard + query/scan failures (happy path + ILIKE
// covered by course_cj2_test.go)
// -----------------------------------------------------------------------------

func TestListByStateAndAuthor_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCourseRepo(nil)
	if _, err := r.ListByStateAndAuthor(context.Background(), tenantID, domain.CourseStateDraft, cj2xAuthor, "", 0, 20); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

func TestListByStateAndAuthor_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("author queue query conn closed")
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByStateAndAuthor(ctx, tenantID, domain.CourseStateDraft, cj2xAuthor, "", 0, 20); err == nil || !strings.Contains(err.Error(), "author queue query conn closed") {
		t.Fatalf("expected the SELECT error to propagate; got %v", err)
	}
}

func TestListByStateAndAuthor_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad author course row bytes") },
			}}, nil
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByStateAndAuthor(ctx, tenantID, domain.CourseStateDraft, cj2xAuthor, "", 0, 20); err == nil || !strings.Contains(err.Error(), "bad author course row bytes") {
		t.Fatalf("expected the row scan error to propagate; got %v", err)
	}
}
