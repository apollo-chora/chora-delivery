// Wave-1 D-FULL TDD coverage for the chora-delivery gRPC server registration.
//
// Boots an in-process *grpc.Server on a bufconn listener with the same
// composition cmd/server/main.go does (Delivery server + Health), dials it,
// and round-trips Health/Check + one trivial Delivery RPC (EnrollLearner).
// Per docs/m13/grpc-mass-remediation-2026-05-16.md §3.e the acceptance test
// must (1) boot a server with the new registrations, (2) dial via
// ClientConn, (3) hit Health/Check, (4) hit one domain RPC end-to-end.
//
// The bufconn pattern (mirrors chora-sharing cmd/server/grpc_test.go +
// chora-identity mana_grpc_bufconn_test.go) is the closest in-test fidelity
// to the Cloud Service Mesh wire path the chora-gateway BFF will use post
// Wave-2 cutover.
package main_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthgrpc "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/delivery/v1"

	grpcadapter "github.com/apollo-chora/chora-delivery/internal/adapter/grpc"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const bufconnSize = 1024 * 1024

// startBufconnDeliveryServer mirrors the gRPC composition cmd/server/main.go
// performs: Delivery server bound + Health bound on the same *grpc.Server.
// Returns the in-mem submission repo so wire-level tests can seed released
// rows for the CHO-2040 graded-submissions read.
func startBufconnDeliveryServer(t *testing.T) (*grpc.ClientConn, *domain.InMemSubmissionRepo, func()) {
	t.Helper()
	lis := bufconn.Listen(bufconnSize)
	srv := grpc.NewServer()

	courseRepo := inmem.NewCourseRepo()
	catalogue := domain.NewInMemCatalogue()
	enrollments := domain.NewEnrollmentRegistry()
	certs := domain.NewCertificationRegistry()
	subs := domain.NewInMemSubmissionRepo()

	// EnrollmentPort evolved to a context-aware CountByCourse(ctx, tenant,
	// course) (int, error); *EnrollmentRegistry keeps the legacy non-ctx
	// signature, so wrap it in the InMemEnrollmentStore adapter that satisfies
	// the port (matches the production wiring in cmd/server/main.go).
	deliverySrv := grpcadapter.NewDeliveryServer(
		courseRepo, catalogue, domain.NewInMemEnrollmentStoreFrom(enrollments), certs,
		// CHO-2040: graded-submission read port — in-mem variant, matching
		// main.go's CHORA_DB_DSN-unset fallback.
		subs,
	)
	deliveryv1.RegisterDeliveryServer(srv, deliverySrv)

	healthSrv := healthgrpc.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.delivery.v1.Delivery", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn delivery server stopped: %v", err)
		}
	}()

	//nolint:staticcheck // bufconn requires the legacy DialContext API.
	conn, err := grpc.DialContext(
		context.Background(),
		"bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.GracefulStop()
		_ = lis.Close()
	}
	return conn, subs, cleanup
}

// TestBufconn_HealthCheck verifies the gRPC Health service is bound and
// reports SERVING for the Delivery service. Cloud Service Mesh probe
// routing depends on this contract.
func TestBufconn_HealthCheck(t *testing.T) {
	t.Parallel()
	conn, _, cleanup := startBufconnDeliveryServer(t)
	defer cleanup()

	client := healthpb.NewHealthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Generic SERVING for the gRPC server (empty service name).
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatalf("Health/Check (default): %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("default health status=%v want SERVING", resp.Status)
	}

	// Service-scoped SERVING for the Delivery service.
	resp, err = client.Check(ctx, &healthpb.HealthCheckRequest{Service: "chora.services.delivery.v1.Delivery"})
	if err != nil {
		t.Fatalf("Health/Check (Delivery): %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("Delivery health status=%v want SERVING", resp.Status)
	}
}

// TestBufconn_Delivery_EnrollLearner_RoundTrip exercises the full chora-
// gateway → chora-delivery wire path via gRPC: EnrollLearner inserts an
// Enrollment row, returns the canonical proto Enrollment. Mirrors the BFF
// call shape the Wave-2 switch will dispatch when SVC_DELIVERY_GRPC_URL
// lands.
func TestBufconn_Delivery_EnrollLearner_RoundTrip(t *testing.T) {
	t.Parallel()
	conn, _, cleanup := startBufconnDeliveryServer(t)
	defer cleanup()

	client := deliveryv1.NewDeliveryClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	const (
		tenantID = "01970000-0000-7000-9000-tenant-aaaaa"
		courseID = "01970000-0000-7000-9000-course-aaaaa"
		alice    = "01970000-0000-7000-9000-000000000001"
	)
	resp, err := client.EnrollLearner(ctx, &deliveryv1.EnrollLearnerRequest{
		CourseId:    courseID,
		LearnerGcid: alice,
		TenantId:    tenantID,
	})
	if err != nil {
		t.Fatalf("EnrollLearner: %v", err)
	}
	if resp.GetEnrollment() == nil {
		t.Fatalf("EnrollLearner response nil enrollment")
	}
	if got := resp.GetEnrollment().GetCourseId(); got != courseID {
		t.Errorf("course_id=%q want %q", got, courseID)
	}
	if got := resp.GetEnrollment().GetLearnerGcid(); got != alice {
		t.Errorf("learner_gcid=%q want %q", got, alice)
	}
	if got := resp.GetEnrollment().GetTenantId(); got != tenantID {
		t.Errorf("tenant_id=%q want %q", got, tenantID)
	}
	if got := resp.GetEnrollment().GetStatus(); got != deliveryv1.EnrollmentStatus_ENROLLMENT_STATUS_ACTIVE {
		t.Errorf("status=%v want ACTIVE", got)
	}
	if !resp.GetEnrollment().GetEnrolledAt().IsValid() {
		t.Errorf("enrolled_at not set")
	}

	// Idempotent re-enroll returns the same row (course_id+learner_gcid key).
	resp2, err := client.EnrollLearner(ctx, &deliveryv1.EnrollLearnerRequest{
		CourseId:    courseID,
		LearnerGcid: alice,
		TenantId:    tenantID,
	})
	if err != nil {
		t.Fatalf("EnrollLearner (repeat): %v", err)
	}
	if resp.GetEnrollment().GetEnrollmentId() != resp2.GetEnrollment().GetEnrollmentId() {
		t.Errorf("idempotent EnrollLearner re-created row (enrollment_id changed)")
	}

	// Missing required fields rejected with InvalidArgument.
	if _, err := client.EnrollLearner(ctx, &deliveryv1.EnrollLearnerRequest{
		CourseId:    "",
		LearnerGcid: alice,
		TenantId:    tenantID,
	}); err == nil {
		t.Errorf("expected error on missing course_id")
	}
}

// TestBufconn_Delivery_ListLearnerGradedSubmissions_RoundTrip exercises the
// CHO-2040 ceremony learning-edges read over the real gRPC wire: a RELEASED
// submission (with its ADR-172 overall_comment) comes back; a
// GRADED_PENDING_RELEASE sibling must NOT leak (mirrors the learner-facing
// myResultHandler visibility gate — pre-release grading is invisible in the
// product, so it is invisible here).
func TestBufconn_Delivery_ListLearnerGradedSubmissions_RoundTrip(t *testing.T) {
	t.Parallel()
	conn, subs, cleanup := startBufconnDeliveryServer(t)
	defer cleanup()

	client := deliveryv1.NewDeliveryClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	const (
		tenantID = "01970000-0000-7000-9000-tenant-aaaaa"
		bella    = "01970000-0000-7000-9000-000000000002"
	)
	gradedAt := time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)
	passed := true
	if err := subs.Save(ctx, &domain.Submission{
		ID:             "01970000-0000-7000-9000-00000000subA",
		AssessmentID:   "01970000-0000-7000-9000-0000000assA",
		TenantID:       tenantID,
		LearnerGCID:    bella,
		State:          domain.SubmissionStateReleased,
		Passed:         &passed,
		OverallComment: "Strong grasp; revise unit conversions.",
		GradedAt:       &gradedAt,
	}); err != nil {
		t.Fatalf("seed released: %v", err)
	}
	if err := subs.Save(ctx, &domain.Submission{
		ID:             "01970000-0000-7000-9000-00000000subB",
		AssessmentID:   "01970000-0000-7000-9000-0000000assA",
		TenantID:       tenantID,
		LearnerGCID:    bella,
		State:          domain.SubmissionStateGradedPendingRelease,
		OverallComment: "AI draft — NOT yet instructor-approved",
		GradedAt:       &gradedAt,
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	resp, err := client.ListLearnerGradedSubmissions(ctx, &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    tenantID,
		LearnerGcid: bella,
	})
	if err != nil {
		t.Fatalf("ListLearnerGradedSubmissions: %v", err)
	}
	if len(resp.GetItems()) != 1 {
		t.Fatalf("expected exactly the RELEASED row; got %d items", len(resp.GetItems()))
	}
	item := resp.GetItems()[0]
	if item.GetSubmissionId() != "01970000-0000-7000-9000-00000000subA" {
		t.Errorf("submission_id=%q want the released row", item.GetSubmissionId())
	}
	if item.GetOverallComment() != "Strong grasp; revise unit conversions." {
		t.Errorf("overall_comment=%q", item.GetOverallComment())
	}
	if item.GetOutcome() != "PASSED" {
		t.Errorf("outcome=%q want PASSED", item.GetOutcome())
	}
	if !item.GetCompletedAt().IsValid() || !item.GetCompletedAt().AsTime().Equal(gradedAt) {
		t.Errorf("completed_at=%v want %v", item.GetCompletedAt(), gradedAt)
	}
	if resp.GetNextCursor() != "" {
		t.Errorf("next_cursor=%q want empty (single page)", resp.GetNextCursor())
	}

	// Missing learner_gcid rejected with InvalidArgument over the wire.
	if _, err := client.ListLearnerGradedSubmissions(ctx, &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId: tenantID,
	}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument on missing learner_gcid; got %v", err)
	}
}
