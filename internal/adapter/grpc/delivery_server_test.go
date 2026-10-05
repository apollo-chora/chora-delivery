// delivery_server_test.go — unit tests for the ListLearnerGradedSubmissions
// gRPC read (CHO-2040 ceremony learning-edges seam).
//
// The persistence side is FAKED behind the narrow GradedSubmissionPort so
// these tests exercise the adapter's own responsibilities only:
//
//   - validation: nil request / missing tenant_id / missing learner_gcid →
//     InvalidArgument (mirrors the file's existing RPC validation pattern);
//   - limit clamping: default 20, max 50, +1 probe row for next-page detect;
//   - opaque cursor round-trip + malformed cursor → InvalidArgument;
//   - tenant-context decoration: ctx must carry tenant_id so the pg repo's
//     rls.ApplySession sets the RLS GUC (mirrors EnrollLearner);
//   - domain→proto mapping: outcome derived from Submission.Passed exactly
//     as the learner result payload coerces it (myResultHandler:
//     `passed: sub.Passed != nil && *sub.Passed`); completed_at = GradedAt.
//
// The visibility rule itself (state = 'RELEASED' + deleted_at IS NULL) is
// delegated to the repo query — covered by the pg-layer test in
// internal/adapter/repo/pg/submission_test.go.
package grpc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/delivery/v1"

	"github.com/apollo-chora/chora-common/tracing"
	deliverygrpc "github.com/apollo-chora/chora-delivery/internal/adapter/grpc"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	testTenantID    = "11111111-1111-7111-8111-111111111111"
	testLearnerGCID = "00000000-0000-7000-8000-000000001999"
)

// fakeGradedSubmissionPort captures the adapter→port call and returns canned
// rows, honouring the (limit, offset) window the way the real repo would so
// the +1-probe pagination protocol is exercised end to end.
type fakeGradedSubmissionPort struct {
	gotTenantID    string
	gotLearnerGCID string
	gotLimit       int
	gotOffset      int
	gotCtxTenantID string
	calls          int

	rows []*domain.Submission
	err  error
}

func (f *fakeGradedSubmissionPort) ListReleasedByLearner(ctx context.Context, tenantID, learnerGCID string, limit, offset int) ([]*domain.Submission, error) {
	f.calls++
	f.gotTenantID = tenantID
	f.gotLearnerGCID = learnerGCID
	f.gotLimit = limit
	f.gotOffset = offset
	f.gotCtxTenantID = tracing.TenantIDFromContext(ctx)
	if f.err != nil {
		return nil, f.err
	}
	rows := f.rows
	if offset >= len(rows) {
		return nil, nil
	}
	rows = rows[offset:]
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func newGradedServer(f *fakeGradedSubmissionPort) *deliverygrpc.DeliveryServer {
	return deliverygrpc.NewDeliveryServer(nil, nil, nil, nil, f)
}

func releasedSubmission(id, assessmentID string, passed *bool, comment string, gradedAt *time.Time) *domain.Submission {
	return &domain.Submission{
		ID:             id,
		AssessmentID:   assessmentID,
		TenantID:       testTenantID,
		LearnerGCID:    testLearnerGCID,
		State:          domain.SubmissionStateReleased,
		Passed:         passed,
		OverallComment: comment,
		GradedAt:       gradedAt,
	}
}

func boolPtr(b bool) *bool { return &b }

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %v error; got nil", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("expected code %v; got %v (%v)", want, got, err)
	}
}

func TestListLearnerGradedSubmissions_NilRequest_InvalidArgument(t *testing.T) {
	t.Parallel()
	srv := newGradedServer(&fakeGradedSubmissionPort{})
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), nil)
	wantCode(t, err, codes.InvalidArgument)
}

func TestListLearnerGradedSubmissions_MissingTenant_InvalidArgument(t *testing.T) {
	t.Parallel()
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    "   ",
		LearnerGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.InvalidArgument)
	if f.calls != 0 {
		t.Fatalf("repo must not be called on validation failure; got %d calls", f.calls)
	}
}

func TestListLearnerGradedSubmissions_MissingLearner_InvalidArgument(t *testing.T) {
	t.Parallel()
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId: testTenantID,
	})
	wantCode(t, err, codes.InvalidArgument)
	if f.calls != 0 {
		t.Fatalf("repo must not be called on validation failure; got %d calls", f.calls)
	}
}

func TestListLearnerGradedSubmissions_PortNotWired_Unimplemented(t *testing.T) {
	t.Parallel()
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, nil, nil)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.Unimplemented)
}

func TestListLearnerGradedSubmissions_DefaultLimit_ProbesTwentyOne(t *testing.T) {
	t.Parallel()
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Default page size 20 + 1 probe row for next-page detection.
	if f.gotLimit != 21 {
		t.Fatalf("expected repo limit 21 (default 20 + probe); got %d", f.gotLimit)
	}
	if f.gotOffset != 0 {
		t.Fatalf("expected offset 0 for empty cursor; got %d", f.gotOffset)
	}
}

func TestListLearnerGradedSubmissions_ClampsLimitToFifty(t *testing.T) {
	t.Parallel()
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
		Limit:       500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.gotLimit != 51 {
		t.Fatalf("expected repo limit 51 (max 50 + probe); got %d", f.gotLimit)
	}
}

func TestListLearnerGradedSubmissions_TenantContextDecorated(t *testing.T) {
	t.Parallel()
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    "  " + testTenantID + "  ",
		LearnerGcid: testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.gotTenantID != testTenantID {
		t.Fatalf("expected trimmed tenant %q forwarded; got %q", testTenantID, f.gotTenantID)
	}
	// The pg repo's rls.ApplySession reads the tenant from ctx via
	// tracing.TenantIDFromContext — the adapter MUST decorate it (mirrors
	// EnrollLearner) or RLS scoping fails.
	if f.gotCtxTenantID != testTenantID {
		t.Fatalf("expected ctx decorated with tenant %q; got %q", testTenantID, f.gotCtxTenantID)
	}
}

func TestListLearnerGradedSubmissions_MapsRowsToSummaries(t *testing.T) {
	t.Parallel()
	gradedAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	f := &fakeGradedSubmissionPort{rows: []*domain.Submission{
		releasedSubmission("sub-1", "assess-1", boolPtr(true), "Solid grasp of stoichiometry.", &gradedAt),
		// NULL passed + no comment + no graded_at: outcome coerces to FAILED
		// exactly as the learner result payload does; comment stays empty;
		// completed_at stays unset.
		releasedSubmission("sub-2", "assess-2", nil, "", nil),
	}}
	srv := newGradedServer(f)
	resp, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetItems()) != 2 {
		t.Fatalf("expected 2 items; got %d", len(resp.GetItems()))
	}
	first := resp.GetItems()[0]
	if first.GetSubmissionId() != "sub-1" || first.GetAssessmentId() != "assess-1" {
		t.Fatalf("first item ids wrong: %+v", first)
	}
	if first.GetOverallComment() != "Solid grasp of stoichiometry." {
		t.Fatalf("overall_comment not mapped: %q", first.GetOverallComment())
	}
	if first.GetOutcome() != "PASSED" {
		t.Fatalf("expected outcome PASSED; got %q", first.GetOutcome())
	}
	if first.GetCompletedAt() == nil || !first.GetCompletedAt().AsTime().Equal(gradedAt) {
		t.Fatalf("completed_at not mapped from GradedAt: %v", first.GetCompletedAt())
	}
	second := resp.GetItems()[1]
	if second.GetOutcome() != "FAILED" {
		t.Fatalf("nil Passed must coerce to FAILED (mirrors myResultHandler); got %q", second.GetOutcome())
	}
	if second.GetOverallComment() != "" {
		t.Fatalf("expected empty overall_comment; got %q", second.GetOverallComment())
	}
	if second.GetCompletedAt() != nil {
		t.Fatalf("expected unset completed_at for nil GradedAt; got %v", second.GetCompletedAt())
	}
	if resp.GetNextCursor() != "" {
		t.Fatalf("expected empty next_cursor for a short page; got %q", resp.GetNextCursor())
	}
}

func TestListLearnerGradedSubmissions_PaginationCursorRoundTrip(t *testing.T) {
	t.Parallel()
	gradedAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	rows := make([]*domain.Submission, 0, 21)
	for i := 0; i < 21; i++ {
		ts := gradedAt.Add(-time.Duration(i) * time.Hour)
		rows = append(rows, releasedSubmission(
			"sub-"+string(rune('a'+i)), "assess-1", boolPtr(true), "c", &ts))
	}
	f := &fakeGradedSubmissionPort{rows: rows}
	srv := newGradedServer(f)

	// Page 1 — default limit 20; 21 rows exist so next_cursor must be set.
	page1, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.GetItems()) != 20 {
		t.Fatalf("page1: expected 20 items; got %d", len(page1.GetItems()))
	}
	if page1.GetNextCursor() == "" {
		t.Fatalf("page1: expected non-empty next_cursor")
	}

	// Page 2 — pass the cursor back; repo must be asked at offset 20 and the
	// final page must carry no next_cursor.
	page2, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
		Cursor:      page1.GetNextCursor(),
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if f.gotOffset != 20 {
		t.Fatalf("page2: expected repo offset 20; got %d", f.gotOffset)
	}
	if len(page2.GetItems()) != 1 {
		t.Fatalf("page2: expected 1 item; got %d", len(page2.GetItems()))
	}
	if page2.GetNextCursor() != "" {
		t.Fatalf("page2: expected empty next_cursor on final page; got %q", page2.GetNextCursor())
	}
}

func TestListLearnerGradedSubmissions_MalformedCursor_InvalidArgument(t *testing.T) {
	t.Parallel()
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
		Cursor:      "!!!not-a-cursor!!!",
	})
	wantCode(t, err, codes.InvalidArgument)
	if f.calls != 0 {
		t.Fatalf("repo must not be called on malformed cursor; got %d calls", f.calls)
	}
}

func TestListLearnerGradedSubmissions_RepoError_Internal(t *testing.T) {
	t.Parallel()
	f := &fakeGradedSubmissionPort{err: errors.New("pg: connection refused")}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.Internal)
}

// -----------------------------------------------------------------------------
// CHO-2201 (W0-F5) — CourseRepo persistence port must carry error.
//
// Before F5 the gRPC-adapter-local CourseRepo.Save(c)/Get(id) discarded every infrastructure failure: a broken durable
// write reported a success-shaped CreateCourse response on the
// wire, and a failed course read was indistinguishable from an absent row
// (fail-open NotFound). The narrow ports also blocked the existing
// pg.CourseRepo (Save(ctx) error / Get(ctx) (…, error)) from ever being wired —
// "a port with no ctx + no error cannot be made durable". These tests inject a
// failing adapter and assert the RPC surfaces codes.Internal (our-fault → page)
// rather than a fabricated success.
//
// RED: the widened (ctx, error) test doubles below do NOT satisfy the pre-F5
// narrow CourseRepo interface, so grpc_test fails to compile until
// the ports are widened.
// -----------------------------------------------------------------------------

// failingCourseRepo satisfies the widened grpc.CourseRepo and always returns an
// infra-shaped error — the shape rls.ApplySession / pgx surfaces on a dead DB.
type failingCourseRepo struct{ err error }

func (f *failingCourseRepo) Save(_ context.Context, _ *domain.Course) error { return f.err }

// Get is 3-arg tenant-scoped (ADR-236 D1, mirrors domain.CourseRepo.Get) —
// the tenant param is unused here since this fake always fails regardless.
func (f *failingCourseRepo) Get(_ context.Context, _, _ string) (*domain.Course, bool, error) {
	return nil, false, f.err
}

func TestCreateCourse_SaveInfraError_ReturnsInternalNotSuccess(t *testing.T) {
	t.Parallel()
	srv := deliverygrpc.NewDeliveryServer(
		&failingCourseRepo{err: errors.New("pg: upsert course: connection refused")},
		nil, nil, nil, nil)
	resp, err := srv.CreateCourse(context.Background(), &deliveryv1.CreateCourseRequest{
		TenantId:  testTenantID,
		Title:     "Intro to Kilns",
		OwnerGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.Internal)
	if resp != nil {
		t.Fatalf("a failed durable Save must not fabricate a CreateCourse success; got %+v", resp)
	}
}
