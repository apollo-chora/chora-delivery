// delivery_server_rpc_test.go — unit coverage for the remaining Delivery
// RPCs (CreateCourse / ListPublicCourses / EnrollLearner / IssueCertification)
// plus the domain→proto mappers reachable through them.
//
// The persistence side is FAKED behind the narrow ports, mirroring the
// fakeGradedSubmissionPort discipline used by delivery_server_test.go for
// ListLearnerGradedSubmissions: the adapter's own responsibilities (validation,
// limit clamping, tenant-context decoration, mapping) are what these tests
// pin, and each fake records the adapter→port call so the wiring is asserted,
// not just the happy path.
package grpc_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/delivery/v1"

	"github.com/apollo-chora/chora-common/tracing"
	deliverygrpc "github.com/apollo-chora/chora-delivery/internal/adapter/grpc"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Fakes — one per port, each recording the adapter→port call.
// -----------------------------------------------------------------------------

// fakeCourseRepo satisfies grpc.CourseRepo. Save records the persisted
// aggregate + the ctx tenant (the pg adapter's RLS decoration).
type fakeCourseRepo struct {
	saved        []*domain.Course
	saveErr      error
	gotCtxTenant string
}

func (f *fakeCourseRepo) Save(ctx context.Context, c *domain.Course) error {
	f.saved = append(f.saved, c)
	f.gotCtxTenant = tracing.TenantIDFromContext(ctx)
	return f.saveErr
}

func (f *fakeCourseRepo) Get(_ context.Context, _, _ string) (*domain.Course, bool, error) {
	return nil, false, nil
}

// fakeCataloguePort satisfies domain.CataloguePort (only Search is exercised).
type fakeCataloguePort struct {
	rows      []*domain.PublicCourse
	searchErr error
	lastQuery domain.CatalogueQuery
	calls     int
}

func (f *fakeCataloguePort) Save(_ context.Context, _ *domain.PublicCourse) error { return nil }
func (f *fakeCataloguePort) Get(_ context.Context, _ string) (*domain.PublicCourse, bool, error) {
	return nil, false, nil
}
func (f *fakeCataloguePort) Search(_ context.Context, q domain.CatalogueQuery) ([]*domain.PublicCourse, int, error) {
	f.calls++
	f.lastQuery = q
	if f.searchErr != nil {
		return nil, 0, f.searchErr
	}
	return f.rows, len(f.rows), nil
}
func (f *fakeCataloguePort) SearchCursor(_ context.Context, _ domain.CatalogueQuery) (domain.CursorPage, error) {
	return domain.CursorPage{}, nil
}
func (f *fakeCataloguePort) ListByInstructor(_ context.Context, _, _ string, _, _ int) ([]*domain.PublicCourse, int, error) {
	return nil, 0, nil
}

// fakeEnrollmentPort satisfies domain.EnrollmentPort (only Register is used).
type fakeEnrollmentPort struct {
	enroll       *domain.Enrollment
	err          error
	calls        int
	gotTenant    string
	gotCourse    string
	gotLearner   string
	gotCtxTenant string
}

func (f *fakeEnrollmentPort) Register(ctx context.Context, tenantID, courseID, gcid string) (*domain.Enrollment, error) {
	f.calls++
	f.gotTenant, f.gotCourse, f.gotLearner = tenantID, courseID, gcid
	f.gotCtxTenant = tracing.TenantIDFromContext(ctx)
	return f.enroll, f.err
}
func (f *fakeEnrollmentPort) Get(_ context.Context, _ string) (*domain.Enrollment, bool, error) {
	return nil, false, nil
}
func (f *fakeEnrollmentPort) GetByCourseAndGCID(_ context.Context, _, _, _ string) (*domain.Enrollment, bool, error) {
	return nil, false, nil
}
func (f *fakeEnrollmentPort) ListByGCID(_ context.Context, _, _ string) ([]*domain.Enrollment, error) {
	return nil, nil
}
func (f *fakeEnrollmentPort) CountByCourse(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}
func (f *fakeEnrollmentPort) Cancel(_ context.Context, _, _ string) error { return nil }

// fakeCertStore satisfies domain.CertificationStore (only IssueCtx is used).
type fakeCertStore struct {
	cert  *domain.Certification
	err   error
	calls int
}

func (f *fakeCertStore) IssueCtx(_ context.Context, _, _, _ string, _ *int, _ []string) (*domain.Certification, error) {
	f.calls++
	return f.cert, f.err
}
func (f *fakeCertStore) GetCtx(_ context.Context, _, _ string) (*domain.Certification, bool, error) {
	return nil, false, nil
}
func (f *fakeCertStore) GetByLearnerCourseCtx(_ context.Context, _, _, _ string) (*domain.Certification, bool, error) {
	return nil, false, nil
}
func (f *fakeCertStore) ListByTenantCtx(_ context.Context, _, _, _ string) ([]*domain.Certification, error) {
	return nil, nil
}

// nilServer returns a typed nil *DeliveryServer so the `s == nil` guard on
// every RPC is exercised (a mis-wired server must fail Internal, not panic).
func nilServer() *deliverygrpc.DeliveryServer { return nil }

// -----------------------------------------------------------------------------
// CreateCourse
// -----------------------------------------------------------------------------

func TestCreateCourse_NilServer_Internal(t *testing.T) {
	_, err := nilServer().CreateCourse(context.Background(), &deliveryv1.CreateCourseRequest{})
	wantCode(t, err, codes.Internal)
}

func TestCreateCourse_NilRequest_InvalidArgument(t *testing.T) {
	srv := deliverygrpc.NewDeliveryServer(&fakeCourseRepo{}, nil, nil, nil, nil)
	_, err := srv.CreateCourse(context.Background(), nil)
	wantCode(t, err, codes.InvalidArgument)
}

func TestCreateCourse_ValidationRejects(t *testing.T) {
	srv := deliverygrpc.NewDeliveryServer(&fakeCourseRepo{}, nil, nil, nil, nil)

	_, err := srv.CreateCourse(context.Background(), &deliveryv1.CreateCourseRequest{Title: "T", OwnerGcid: "g"})
	wantCode(t, err, codes.InvalidArgument)

	_, err = srv.CreateCourse(context.Background(), &deliveryv1.CreateCourseRequest{TenantId: testTenantID, OwnerGcid: "g"})
	wantCode(t, err, codes.InvalidArgument)

	_, err = srv.CreateCourse(context.Background(), &deliveryv1.CreateCourseRequest{TenantId: testTenantID, Title: "T"})
	wantCode(t, err, codes.InvalidArgument)
}

func TestCreateCourse_Success_MapsResponseAndDecorateTenantCtx(t *testing.T) {
	repo := &fakeCourseRepo{}
	srv := deliverygrpc.NewDeliveryServer(repo, nil, nil, nil, nil)

	resp, err := srv.CreateCourse(context.Background(), &deliveryv1.CreateCourseRequest{
		TenantId:         "  " + testTenantID + "  ",
		Title:            "Intro to Kilns",
		Description:      "Clay + fire",
		OwnerGcid:        testLearnerGCID,
		IsPublic:         true,
		AtomIds:          []string{"atom-1", "atom-2"},
		PriceCurrency:    "SGD",
		PriceAmountCents: 9900,
		Locale:           "en-SG",
	})
	if err != nil {
		t.Fatalf("CreateCourse: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("expected 1 Save; got %d", len(repo.saved))
	}
	saved := repo.saved[0]
	// The tenant must ride the CONTEXT for the pg repo's rls.ApplySession.
	if repo.gotCtxTenant != testTenantID {
		t.Fatalf("expected ctx decorated with trimmed tenant %q; got %q", testTenantID, repo.gotCtxTenant)
	}
	if saved.InstructorGCID != testLearnerGCID {
		t.Errorf("instructor_gcid not stamped on aggregate: %q", saved.InstructorGCID)
	}
	if saved.MaxCapacity != 1000 {
		t.Errorf("default sentinel capacity: got %d", saved.MaxCapacity)
	}

	c := resp.GetCourse()
	if c == nil {
		t.Fatal("response must carry a Course")
	}
	if c.GetCourseId() != saved.ID || c.GetTenantId() != testTenantID || c.GetTitle() != "Intro to Kilns" {
		t.Errorf("course identity wrong: %+v", c)
	}
	if c.GetStatus() != deliveryv1.CourseStatus_COURSE_STATUS_DRAFT {
		t.Errorf("fresh course must map to DRAFT; got %v", c.GetStatus())
	}
	if !c.GetIsPublic() || c.GetDescription() != "Clay + fire" || c.GetOwnerGcid() != testLearnerGCID {
		t.Errorf("public/description/owner wrong: %+v", c)
	}
	if len(c.GetAtomIds()) != 2 || c.GetAtomIds()[0] != "atom-1" || c.GetAtomIds()[1] != "atom-2" {
		t.Errorf("atom ids not copied: %v", c.GetAtomIds())
	}
	if c.GetPriceCurrency() != "SGD" || c.GetPriceAmountCents() != 9900 || c.GetLocale() != "en-SG" {
		t.Errorf("price/locale wrong: %+v", c)
	}
	if c.GetCreatedAt() == nil || c.GetCreatedAt().AsTime().IsZero() {
		t.Error("created_at must be mapped from the aggregate")
	}
}

// -----------------------------------------------------------------------------
// ListPublicCourses
// -----------------------------------------------------------------------------

func TestListPublicCourses_NilServer_Internal(t *testing.T) {
	_, err := nilServer().ListPublicCourses(context.Background(), &deliveryv1.ListPublicCoursesRequest{})
	wantCode(t, err, codes.Internal)
}

func TestListPublicCourses_NilRequest_InvalidArgument(t *testing.T) {
	srv := deliverygrpc.NewDeliveryServer(nil, &fakeCataloguePort{}, nil, nil, nil)
	_, err := srv.ListPublicCourses(context.Background(), nil)
	wantCode(t, err, codes.InvalidArgument)
}

func TestListPublicCourses_DefaultAndCappedLimit(t *testing.T) {
	cat := &fakeCataloguePort{}
	srv := deliverygrpc.NewDeliveryServer(nil, cat, nil, nil, nil)

	// limit absent → 20
	if _, err := srv.ListPublicCourses(context.Background(), &deliveryv1.ListPublicCoursesRequest{
		TenantIdFilter: "  " + testTenantID + " ",
	}); err != nil {
		t.Fatalf("ListPublicCourses: %v", err)
	}
	if cat.lastQuery.Per != 20 {
		t.Errorf("default limit: want Per=20, got %d", cat.lastQuery.Per)
	}
	if cat.lastQuery.Visibility != domain.VisibilityFilterPublic {
		t.Errorf("visibility filter must be public; got %v", cat.lastQuery.Visibility)
	}
	if cat.lastQuery.Page != 1 {
		t.Errorf("page must be 1; got %d", cat.lastQuery.Page)
	}
	if cat.lastQuery.TenantID != testTenantID {
		t.Errorf("tenant filter must be trimmed; got %q", cat.lastQuery.TenantID)
	}

	// limit above the cap → 100
	if _, err := srv.ListPublicCourses(context.Background(), &deliveryv1.ListPublicCoursesRequest{Limit: 500}); err != nil {
		t.Fatalf("ListPublicCourses (cap): %v", err)
	}
	if cat.lastQuery.Per != 100 {
		t.Errorf("capped limit: want Per=100, got %d", cat.lastQuery.Per)
	}
}

func TestListPublicCourses_SearchError_Internal(t *testing.T) {
	cat := &fakeCataloguePort{searchErr: errors.New("rg: search index down")}
	srv := deliverygrpc.NewDeliveryServer(nil, cat, nil, nil, nil)
	_, err := srv.ListPublicCourses(context.Background(), &deliveryv1.ListPublicCoursesRequest{})
	wantCode(t, err, codes.Internal)
}

func TestListPublicCourses_MapsRowsToProto(t *testing.T) {
	created := time.Date(2026, 7, 1, 8, 30, 0, 0, time.UTC)
	cat := &fakeCataloguePort{rows: []*domain.PublicCourse{
		{ID: "pc-1", TenantID: testTenantID, Title: "CSM-Prep", InstructorGCID: testLearnerGCID,
			PriceSGDCents: 4200, Public: true, Visibility: domain.VisibilityPublic, CreatedAt: created},
		{ID: "pc-2", TenantID: testTenantID, Title: "CSPO", InstructorGCID: "00000000-0000-7000-8000-000000001111",
			PriceSGDCents: 0, Public: true, Visibility: domain.VisibilityPublic, CreatedAt: created},
	}}
	srv := deliverygrpc.NewDeliveryServer(nil, cat, nil, nil, nil)
	resp, err := srv.ListPublicCourses(context.Background(), &deliveryv1.ListPublicCoursesRequest{})
	if err != nil {
		t.Fatalf("ListPublicCourses: %v", err)
	}
	items := resp.GetCourses()
	if len(items) != 2 {
		t.Fatalf("want 2 courses; got %d", len(items))
	}
	first := items[0]
	if first.GetCourseId() != "pc-1" || first.GetTitle() != "CSM-Prep" {
		t.Errorf("first course identity wrong: %+v", first)
	}
	if first.GetStatus() != deliveryv1.CourseStatus_COURSE_STATUS_PUBLISHED {
		t.Errorf("catalogue row must map to PUBLISHED; got %v", first.GetStatus())
	}
	if first.GetOwnerGcid() != testLearnerGCID || !first.GetIsPublic() {
		t.Errorf("owner/is_public wrong: %+v", first)
	}
	if first.GetPriceCurrency() != "SGD" || first.GetPriceAmountCents() != 4200 {
		t.Errorf("price mapping wrong: %+v", first)
	}
	if first.GetCreatedAt() == nil || !first.GetCreatedAt().AsTime().Equal(created) {
		t.Errorf("created_at not mapped: %v", first.GetCreatedAt())
	}
	if resp.GetNextCursor() != "" {
		t.Errorf("next_cursor must be empty; got %q", resp.GetNextCursor())
	}
}

func TestListPublicCourses_NilRowInSearchResult(t *testing.T) {
	// A nil row in the catalogue page exercises publicCourseToProto's
	// nil-guard: the row is projected as a nil item, never a panic.
	cat := &fakeCataloguePort{rows: []*domain.PublicCourse{nil}}
	srv := deliverygrpc.NewDeliveryServer(nil, cat, nil, nil, nil)
	resp, err := srv.ListPublicCourses(context.Background(), &deliveryv1.ListPublicCoursesRequest{})
	if err != nil {
		t.Fatalf("ListPublicCourses: %v", err)
	}
	if len(resp.GetCourses()) != 1 || resp.GetCourses()[0] != nil {
		t.Fatalf("nil row must project to a nil item; got %v", resp.GetCourses())
	}
}

// -----------------------------------------------------------------------------
// EnrollLearner
// -----------------------------------------------------------------------------

func TestEnrollLearner_NilServer_Internal(t *testing.T) {
	_, err := nilServer().EnrollLearner(context.Background(), &deliveryv1.EnrollLearnerRequest{})
	wantCode(t, err, codes.Internal)
}

func TestEnrollLearner_NilRequest_InvalidArgument(t *testing.T) {
	srv := deliverygrpc.NewDeliveryServer(nil, nil, &fakeEnrollmentPort{}, nil, nil)
	_, err := srv.EnrollLearner(context.Background(), nil)
	wantCode(t, err, codes.InvalidArgument)
}

func TestEnrollLearner_MissingFields_InvalidArgument(t *testing.T) {
	f := &fakeEnrollmentPort{}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, f, nil, nil)
	_, err := srv.EnrollLearner(context.Background(), &deliveryv1.EnrollLearnerRequest{
		TenantId: "  ",
	})
	wantCode(t, err, codes.InvalidArgument)
	if f.calls != 0 {
		t.Fatalf("registry must not be called on validation failure; got %d calls", f.calls)
	}
}

func TestEnrollLearner_InvalidArgumentFromPort_InvalidArgument(t *testing.T) {
	f := &fakeEnrollmentPort{err: domain.ErrInvalidArgument}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, f, nil, nil)
	_, err := srv.EnrollLearner(context.Background(), &deliveryv1.EnrollLearnerRequest{
		TenantId: testTenantID, CourseId: "c-1", LearnerGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.InvalidArgument)
}

func TestEnrollLearner_InfraError_Internal(t *testing.T) {
	f := &fakeEnrollmentPort{err: errors.New("pg: register enrollment: connection refused")}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, f, nil, nil)
	_, err := srv.EnrollLearner(context.Background(), &deliveryv1.EnrollLearnerRequest{
		TenantId: testTenantID, CourseId: "c-1", LearnerGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.Internal)
}

func TestEnrollLearner_Success_MapsResponseAndDecorateTenantCtx(t *testing.T) {
	enrolledAt := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	f := &fakeEnrollmentPort{enroll: &domain.Enrollment{
		ID: "enr-1", TenantID: testTenantID, CourseID: "c-1", GCID: testLearnerGCID,
		EnrolledAt: enrolledAt, Status: domain.EnrollmentStatusActive,
	}}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, f, nil, nil)

	resp, err := srv.EnrollLearner(context.Background(), &deliveryv1.EnrollLearnerRequest{
		TenantId: "  " + testTenantID + " ", CourseId: " c-1 ", LearnerGcid: "  " + testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("EnrollLearner: %v", err)
	}
	if f.gotTenant != testTenantID || f.gotCourse != "c-1" || f.gotLearner != testLearnerGCID {
		t.Errorf("port args not trimmed: %q/%q/%q", f.gotTenant, f.gotCourse, f.gotLearner)
	}
	if f.gotCtxTenant != testTenantID {
		t.Errorf("ctx must carry the tenant for rls.ApplySession; got %q", f.gotCtxTenant)
	}
	e := resp.GetEnrollment()
	if e == nil {
		t.Fatal("response must carry an Enrollment")
	}
	if e.GetEnrollmentId() != "enr-1" || e.GetCourseId() != "c-1" || e.GetLearnerGcid() != testLearnerGCID || e.GetTenantId() != testTenantID {
		t.Errorf("enrollment identity wrong: %+v", e)
	}
	if e.GetStatus() != deliveryv1.EnrollmentStatus_ENROLLMENT_STATUS_ACTIVE {
		t.Errorf("status must map to ACTIVE; got %v", e.GetStatus())
	}
	if e.GetEnrolledAt() == nil || !e.GetEnrolledAt().AsTime().Equal(enrolledAt) {
		t.Errorf("enrolled_at not mapped: %v", e.GetEnrolledAt())
	}
}

func TestEnrollLearner_NilEnrollmentFromPort(t *testing.T) {
	// A degenerate (nil, nil) port result must not panic — enrollmentToProto's
	// nil-guard projects a nil Enrollment onto the wire.
	f := &fakeEnrollmentPort{enroll: nil, err: nil}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, f, nil, nil)
	resp, err := srv.EnrollLearner(context.Background(), &deliveryv1.EnrollLearnerRequest{
		TenantId: testTenantID, CourseId: "c-1", LearnerGcid: testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("EnrollLearner: %v", err)
	}
	if resp.GetEnrollment() != nil {
		t.Fatalf("nil port result must map to a nil enrollment; got %+v", resp.GetEnrollment())
	}
}

// -----------------------------------------------------------------------------
// IssueCertification
// -----------------------------------------------------------------------------

func TestIssueCertification_NilServer_Internal(t *testing.T) {
	_, err := nilServer().IssueCertification(context.Background(), &deliveryv1.IssueCertificationRequest{})
	wantCode(t, err, codes.Internal)
}

func TestIssueCertification_NilRequest_InvalidArgument(t *testing.T) {
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, &fakeCertStore{}, nil)
	_, err := srv.IssueCertification(context.Background(), nil)
	wantCode(t, err, codes.InvalidArgument)
}

func TestIssueCertification_MissingFields_InvalidArgument(t *testing.T) {
	f := &fakeCertStore{}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, f, nil)
	_, err := srv.IssueCertification(context.Background(), &deliveryv1.IssueCertificationRequest{
		TenantId: testTenantID,
	})
	wantCode(t, err, codes.InvalidArgument)
	if f.calls != 0 {
		t.Fatalf("cert store must not be called on validation failure; got %d calls", f.calls)
	}
}

func TestIssueCertification_AlreadyIssued_AlreadyExists(t *testing.T) {
	f := &fakeCertStore{err: domain.ErrCertAlreadyIssued}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, f, nil)
	_, err := srv.IssueCertification(context.Background(), &deliveryv1.IssueCertificationRequest{
		TenantId: testTenantID, CourseId: "c-1", RecipientGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.AlreadyExists)
}

func TestIssueCertification_InvalidArgumentFromPort(t *testing.T) {
	f := &fakeCertStore{err: domain.ErrInvalidArgument}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, f, nil)
	_, err := srv.IssueCertification(context.Background(), &deliveryv1.IssueCertificationRequest{
		TenantId: testTenantID, CourseId: "c-1", RecipientGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.InvalidArgument)
}

func TestIssueCertification_InfraError_Internal(t *testing.T) {
	f := &fakeCertStore{err: errors.New("pg: issue certification: deadlock detected")}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, f, nil)
	_, err := srv.IssueCertification(context.Background(), &deliveryv1.IssueCertificationRequest{
		TenantId: testTenantID, CourseId: "c-1", RecipientGcid: testLearnerGCID,
	})
	wantCode(t, err, codes.Internal)
}

func TestIssueCertification_Success_MapsResponse(t *testing.T) {
	issuedAt := time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC)
	expiresAt := timestamppb.New(time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC))
	f := &fakeCertStore{cert: &domain.Certification{
		ID: "cert-1", TenantID: testTenantID, LearnerID: testLearnerGCID,
		CourseID: "c-1", IssuedAt: issuedAt,
	}}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, f, nil)

	resp, err := srv.IssueCertification(context.Background(), &deliveryv1.IssueCertificationRequest{
		TenantId: testTenantID, CourseId: "c-1", RecipientGcid: testLearnerGCID,
		FinalScore: 95.5, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("IssueCertification: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("expected 1 IssueCtx call; got %d", f.calls)
	}
	c := resp.GetCertificate()
	if c == nil {
		t.Fatal("response must carry a Certificate")
	}
	if c.GetCertificateId() != "cert-1" || c.GetCourseId() != "c-1" || c.GetRecipientGcid() != testLearnerGCID || c.GetTenantId() != testTenantID {
		t.Errorf("certificate identity wrong: %+v", c)
	}
	if c.GetStatus() != deliveryv1.CertificateStatus_CERTIFICATE_STATUS_ISSUED {
		t.Errorf("status must map to ISSUED; got %v", c.GetStatus())
	}
	if c.GetFinalScore() != 95.5 {
		t.Errorf("final_score must pass through: got %v", c.GetFinalScore())
	}
	if c.GetIssuedAt() == nil || !c.GetIssuedAt().AsTime().Equal(issuedAt) {
		t.Errorf("issued_at not mapped: %v", c.GetIssuedAt())
	}
	if c.GetExpiresAt() == nil || !c.GetExpiresAt().AsTime().Equal(expiresAt.AsTime()) {
		t.Errorf("expires_at not mapped: %v", c.GetExpiresAt())
	}
}

func TestIssueCertification_NilCertFromPort(t *testing.T) {
	// A degenerate (nil, nil) issue must not panic — certToProto's nil-guard.
	f := &fakeCertStore{cert: nil, err: nil}
	srv := deliverygrpc.NewDeliveryServer(nil, nil, nil, f, nil)
	resp, err := srv.IssueCertification(context.Background(), &deliveryv1.IssueCertificationRequest{
		TenantId: testTenantID, CourseId: "c-1", RecipientGcid: testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("IssueCertification: %v", err)
	}
	if resp.GetCertificate() != nil {
		t.Fatalf("nil port result must map to a nil certificate; got %+v", resp.GetCertificate())
	}
}

// -----------------------------------------------------------------------------
// ListLearnerGradedSubmissions — remaining guards (nil server / nil request /
// nil row in the page / negative-offset cursor).
// -----------------------------------------------------------------------------

func TestListLearnerGradedSubmissions_NilServer_Internal(t *testing.T) {
	_, err := nilServer().ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{})
	wantCode(t, err, codes.Internal)
}

func TestListLearnerGradedSubmissions_NilRequestRejected(t *testing.T) {
	srv := newGradedServer(&fakeGradedSubmissionPort{})
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), nil)
	wantCode(t, err, codes.InvalidArgument)
}

func TestListLearnerGradedSubmissions_NilRowMapsToNilItem(t *testing.T) {
	f := &fakeGradedSubmissionPort{rows: []*domain.Submission{nil}}
	srv := newGradedServer(f)
	resp, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
	})
	if err != nil {
		t.Fatalf("ListLearnerGradedSubmissions: %v", err)
	}
	if len(resp.GetItems()) != 1 || resp.GetItems()[0] != nil {
		t.Fatalf("nil row must project to a nil item; got %v", resp.GetItems())
	}
}

func TestListLearnerGradedSubmissions_NegativeOffsetCursor_InvalidArgument(t *testing.T) {
	// `{"o":-1}` base64url-encoded — the decode path rejects a negative offset.
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"o":-1}`))
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
		Cursor:      cursor,
	})
	wantCode(t, err, codes.InvalidArgument)
	if f.calls != 0 {
		t.Fatalf("repo must not be called on invalid cursor; got %d calls", f.calls)
	}
}

func TestListLearnerGradedSubmissions_BlankCursorIsFirstPage(t *testing.T) {
	f := &fakeGradedSubmissionPort{rows: []*domain.Submission{
		releasedSubmission("sub-1", "assess-1", boolPtr(true), "c", nil),
	}}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
		Cursor:      "   ",
	})
	if err != nil {
		t.Fatalf("whitespace cursor must decode to the first page: %v", err)
	}
	if f.gotOffset != 0 {
		t.Fatalf("expected offset 0 for a blank cursor; got %d", f.gotOffset)
	}
}

func TestListLearnerGradedSubmissions_ValidBase64NonJSONCursor_InvalidArgument(t *testing.T) {
	// Base64 decodes fine but the payload is not the cursor JSON contract —
	// the wire envelope must be opaque AND well-formed.
	cursor := base64.RawURLEncoding.EncodeToString([]byte("definitely not json"))
	f := &fakeGradedSubmissionPort{}
	srv := newGradedServer(f)
	_, err := srv.ListLearnerGradedSubmissions(context.Background(), &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    testTenantID,
		LearnerGcid: testLearnerGCID,
		Cursor:      cursor,
	})
	wantCode(t, err, codes.InvalidArgument)
	if f.calls != 0 {
		t.Fatalf("repo must not be called on invalid cursor; got %d calls", f.calls)
	}
}
