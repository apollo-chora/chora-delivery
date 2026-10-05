// Package grpc is the chora-delivery gRPC adapter — the server-side wire
// implementation of the chora.services.delivery.v1.Delivery contract.
//
// Wave-1 D-FULL (2026-05-16, docs/m13/grpc-mass-remediation-2026-05-16.md):
// closes the systemic ADR-140 violation where chora-gateway BFF dials this
// service over plain HTTP :8080. After Wave 1 the gRPC server is registered
// on :9090; after Wave 2 the BFF switches to dial gRPC; after Wave 3 the
// image roll cuts traffic over the new port.
//
// Hexagonal: this adapter depends only on the domain ports + the
// chora-contracts gen stubs. No business logic — just translation between
// the proto wire types and the domain aggregates / registries already wired
// in cmd/server/main.go for the HTTP path. The HTTP server keeps running on
// :8080 through Wave 3 + 24h soak; this gRPC server is additive.
//
// Trust model: mTLS via Cloud Service Mesh sidecar. The server does NOT do
// JWT validation — the caller (chora-gateway / chora-creation / chora-
// consumption) is mesh-trusted by SPIFFE identity. The scoped
// AuthorizationPolicy (`authz-allow-delivery-grpc.yaml`) constrains which
// source namespaces can reach this server's RPCs.
package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/delivery/v1"

	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// CourseRepo is the persistence port for Course aggregates — the subset of
// domain.CourseRepo (ADR-236 D1) this adapter actually calls (Save +
// tenant-scoped Get; no ListByTenant — there is no gRPC list-courses RPC).
// Re-declared narrow rather than aliased, mirroring GradedSubmissionPort
// below: the gRPC adapter doesn't pull in a method it never calls, and its
// own test fakes stay minimal. Implemented by internal/adapter/inmem.CourseRepo
// (dev) + pg.CourseRepo (prod) — both satisfy domain.CourseRepo, which is a
// structural superset of this interface.
//
//   - Save upserts a Course. Returns an error so a failed durable write is
//     loud — a swallowed Save reported a CreateCourse success on the wire while
//     the row was lost.
//   - Get resolves a Course by (tenantID, id). ok=false is a GENUINE MISS —
//     no matching row, wrong tenant, or soft-deleted; an infra/RLS failure
//     returns a non-nil error. CHO-2201 (W0-F5): these were once the same
//     answer, so a dead DB read as an absent course (fail-open NotFound).
//     Widened from a bare id (ADR-236 D1) to match domain.CourseRepo.Get —
//     the pg adapter enforces the tenant scope via SQL WHERE tenant_id=$2 + RLS.
//
// ctx carries tenant_id (tracing.WithTenantID) for the pg adapter's
// rls.ApplySession — a port with no ctx + no error cannot be made durable.
type CourseRepo interface {
	Save(ctx context.Context, c *domain.Course) error
	Get(ctx context.Context, tenantID, id string) (*domain.Course, bool, error)
}

// CataloguePort mirrors delivery.CataloguePort for the public catalogue read.
// We re-declare the subset we use so the gRPC adapter doesn't pull in the
// pagination helpers it doesn't need.
type CataloguePort = domain.CataloguePort

// GradedSubmissionPort is the read-only subset of domain.SubmissionRepo the
// ListLearnerGradedSubmissions RPC needs (CHO-2040 ceremony learning-edges
// seam). Re-declared narrow — same rationale as CataloguePort above — so
// server tests can fake one method instead of the whole SubmissionRepo.
// Implemented by pg.SubmissionRepo (prod) + domain.InMemSubmissionRepo (dev).
type GradedSubmissionPort interface {
	// ListReleasedByLearner returns the learner's RELEASED submissions,
	// newest grading-completion (graded_at) first, windowed by (limit,
	// offset). RELEASED-only is the learner-visibility rule — see the
	// domain.SubmissionRepo port doc + pg.SQLListReleasedSubmissionsByLearner.
	ListReleasedByLearner(ctx context.Context, tenantID, learnerGCID string, limit, offset int) ([]*domain.Submission, error)
}

// DeliveryServer is the gRPC adapter that implements
// chora.services.delivery.v1.Delivery by routing each RPC into the existing
// in-memory / pg-backed domain ports.
//
// Embeds UnimplementedDeliveryServer for forward compatibility (per
// chora-contracts gen convention).
type DeliveryServer struct {
	deliveryv1.UnimplementedDeliveryServer

	Courses        CourseRepo
	Catalogue      CataloguePort
	Enrollments    domain.EnrollmentPort
	Certifications domain.CertificationStore
	Submissions    GradedSubmissionPort
}

// NewDeliveryServer constructs the server with the supplied domain ports.
// All ports are required EXCEPT Submissions which may be nil (in which case
// ListLearnerGradedSubmissions returns Unimplemented); the production wiring
// in cmd/server/main.go MUST supply it.
func NewDeliveryServer(
	courses CourseRepo,
	catalogue CataloguePort,
	enrollments domain.EnrollmentPort,
	certs domain.CertificationStore,
	subs GradedSubmissionPort,
) *DeliveryServer {
	return &DeliveryServer{
		Courses:        courses,
		Catalogue:      catalogue,
		Enrollments:    enrollments,
		Certifications: certs,
		Submissions:    subs,
	}
}

// -----------------------------------------------------------------------------
// CreateCourse
// -----------------------------------------------------------------------------

// CreateCourse creates a fresh Course aggregate and persists it via the
// CourseRepo port. The proto contract carries a `description` field that the
// current domain.Course does NOT yet have a column for; the value is accepted
// at the wire but not persisted until the migration lands. Per
// feedback_no_stubs_real_wiring this is documented loudly rather than
// silently dropped — chora-creation tracks the schema follow-up.
func (s *DeliveryServer) CreateCourse(ctx context.Context, req *deliveryv1.CreateCourseRequest) (*deliveryv1.CreateCourseResponse, error) {
	if s == nil {
		return nil, status.Error(codes.Internal, "delivery server not initialised")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	title := strings.TrimSpace(req.GetTitle())
	ownerGCID := strings.TrimSpace(req.GetOwnerGcid())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if title == "" {
		return nil, status.Error(codes.InvalidArgument, "title required")
	}
	if ownerGCID == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_gcid required")
	}

	// NewCourse demands capacity > 0; the proto omits capacity at the Course
	// level (capacity is a Class-instance concept per delivery.proto). Default
	// to a large sentinel so the aggregate constructor accepts the row.
	const defaultCourseCapacity = 1000
	c, err := domain.NewCourse(tenantID, title, req.GetAtomIds(), defaultCourseCapacity)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "new course: %v", err)
	}
	c.InstructorGCID = ownerGCID
	// Decorate ctx with tenant_id so a pg CourseRepo's rls.ApplySession picks
	// it up via tracing.TenantIDFromContext (mirrors EnrollLearner).
	rctx := tracing.WithTenantID(ctx, tenantID)
	if err := s.Courses.Save(rctx, c); err != nil {
		return nil, status.Errorf(codes.Internal, "save course: %v", err)
	}

	return &deliveryv1.CreateCourseResponse{
		Course: courseToProto(c, ownerGCID, req.GetIsPublic(), req.GetDescription(),
			req.GetPriceCurrency(), req.GetPriceAmountCents(), req.GetLocale()),
	}, nil
}

// -----------------------------------------------------------------------------
// ListPublicCourses
// -----------------------------------------------------------------------------

// ListPublicCourses returns the public catalogue page. Reuses the existing
// CataloguePort.Search path that already powers the HTTP /api/catalog
// endpoint. The proto's `cursor` is mapped to a 1-indexed page number (best-
// effort — full Relay cursor semantics will land when SearchCursor is wired).
func (s *DeliveryServer) ListPublicCourses(ctx context.Context, req *deliveryv1.ListPublicCoursesRequest) (*deliveryv1.ListPublicCoursesResponse, error) {
	if s == nil {
		return nil, status.Error(codes.Internal, "delivery server not initialised")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := domain.CatalogueQuery{
		TenantID:   strings.TrimSpace(req.GetTenantIdFilter()),
		Visibility: domain.VisibilityFilterPublic,
		Page:       1,
		Per:        limit,
	}
	items, _, err := s.Catalogue.Search(ctx, q)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "catalogue search: %v", err)
	}
	out := make([]*deliveryv1.Course, 0, len(items))
	for _, pc := range items {
		out = append(out, publicCourseToProto(pc))
	}
	return &deliveryv1.ListPublicCoursesResponse{
		Courses:    out,
		NextCursor: "",
	}, nil
}

// -----------------------------------------------------------------------------
// EnrollLearner
// -----------------------------------------------------------------------------

// EnrollLearner registers a learner in a course via EnrollmentRegistry.
// Idempotent on (tenant_id, course_id, learner_gcid) per the Comic Ch4 P8
// "same identity" invariant.
func (s *DeliveryServer) EnrollLearner(ctx context.Context, req *deliveryv1.EnrollLearnerRequest) (*deliveryv1.EnrollLearnerResponse, error) {
	if s == nil {
		return nil, status.Error(codes.Internal, "delivery server not initialised")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	courseID := strings.TrimSpace(req.GetCourseId())
	learnerGCID := strings.TrimSpace(req.GetLearnerGcid())
	if tenantID == "" || courseID == "" || learnerGCID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, course_id, learner_gcid required")
	}
	// Decorate ctx with tenant_id so pg.EnrollmentRepo's rls.ApplySession
	// picks it up via tracing.TenantIDFromContext.
	rctx := tracing.WithTenantID(ctx, tenantID)
	e, err := s.Enrollments.Register(rctx, tenantID, courseID, learnerGCID)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidArgument) {
			return nil, status.Errorf(codes.InvalidArgument, "register: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "register: %v", err)
	}
	return &deliveryv1.EnrollLearnerResponse{
		Enrollment: enrollmentToProto(e),
	}, nil
}

// -----------------------------------------------------------------------------
// IssueCertification
// -----------------------------------------------------------------------------

// IssueCertification mints a Certificate for (course, recipient) via the
// CertificationRegistry domain port. The proto's `final_score` is recorded
// on the response (the domain Certification today doesn't persist score —
// schema follow-up tracked alongside the CourseDescription work).
func (s *DeliveryServer) IssueCertification(ctx context.Context, req *deliveryv1.IssueCertificationRequest) (*deliveryv1.IssueCertificationResponse, error) {
	if s == nil {
		return nil, status.Error(codes.Internal, "delivery server not initialised")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	courseID := strings.TrimSpace(req.GetCourseId())
	recipient := strings.TrimSpace(req.GetRecipientGcid())
	if tenantID == "" || courseID == "" || recipient == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, course_id, recipient_gcid required")
	}
	cert, err := s.Certifications.IssueCtx(ctx, tenantID, recipient, courseID, nil, nil)
	if err != nil {
		if errors.Is(err, domain.ErrCertAlreadyIssued) {
			return nil, status.Errorf(codes.AlreadyExists, "certification already issued for (%s, %s)", courseID, recipient)
		}
		if errors.Is(err, domain.ErrInvalidArgument) {
			return nil, status.Errorf(codes.InvalidArgument, "issue: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "issue: %v", err)
	}
	return &deliveryv1.IssueCertificationResponse{
		Certificate: certToProto(cert, req.GetFinalScore(), req.GetExpiresAt()),
	}, nil
}

// -----------------------------------------------------------------------------
// ListLearnerGradedSubmissions (CHO-2040 ceremony learning-edges seam)
// -----------------------------------------------------------------------------

// ListLearnerGradedSubmissions returns the learner's past graded submissions
// + their ADR-172 whole-assessment overall comments — consumed lazily at
// ceremony time by chora-consumption's Familiar edge-scout skill (cross-DB
// reads are forbidden, so chora_delivery exposes this gRPC read instead).
//
// VISIBILITY — mirrors the learner-facing result endpoint
// (internal/adapter/http/assessment_handler.go myResultHandler) EXACTLY:
// only state = 'RELEASED' submissions are returned (filter lives in
// pg.SQLListReleasedSubmissionsByLearner). MarkReleased enforces the
// ADR-172 §D6 HITL gate, so a released row's overall_comment is precisely
// what the learner already sees in the product; a released row with no
// comment is returned with overall_comment = "" (the result payload omits
// the field in that case — nothing extra leaks).
//
// Pagination: opaque base64url cursor over the offset (framing owned here,
// mirroring the offering_handler cursor idiom; the SQL stays offset+limit
// per the SQLListCJ2CoursesByState precedent). One extra row is fetched to
// detect whether a next page exists.
func (s *DeliveryServer) ListLearnerGradedSubmissions(ctx context.Context, req *deliveryv1.ListLearnerGradedSubmissionsRequest) (*deliveryv1.ListLearnerGradedSubmissionsResponse, error) {
	if s == nil {
		return nil, status.Error(codes.Internal, "delivery server not initialised")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	if s.Submissions == nil {
		return nil, status.Error(codes.Unimplemented, "graded-submission port not wired")
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	learnerGCID := strings.TrimSpace(req.GetLearnerGcid())
	if tenantID == "" || learnerGCID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, learner_gcid required")
	}
	// Page size 1..50, default 20 (per the CHO-2040 seam contract).
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	offset, err := decodeGradedSubmissionsCursor(req.GetCursor())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cursor: %v", err)
	}
	// Decorate ctx with tenant_id so pg.SubmissionRepo's rls.ApplySession
	// picks it up via tracing.TenantIDFromContext (mirrors EnrollLearner).
	rctx := tracing.WithTenantID(ctx, tenantID)
	// +1 probe row detects whether another page exists.
	rows, err := s.Submissions.ListReleasedByLearner(rctx, tenantID, learnerGCID, limit+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list released submissions: %v", err)
	}
	nextCursor := ""
	if len(rows) > limit {
		rows = rows[:limit]
		nextCursor = encodeGradedSubmissionsCursor(offset + limit)
	}
	items := make([]*deliveryv1.GradedSubmissionSummary, 0, len(rows))
	for _, sub := range rows {
		items = append(items, gradedSubmissionSummaryToProto(sub))
	}
	return &deliveryv1.ListLearnerGradedSubmissionsResponse{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

// gradedSubmissionsCursorWire is the opaque (base64url) JSON cursor on the
// wire — mirrors the offering_handler offeringCursorWire idiom.
type gradedSubmissionsCursorWire struct {
	Offset int `json:"o"`
}

func encodeGradedSubmissionsCursor(offset int) string {
	b, _ := json.Marshal(gradedSubmissionsCursorWire{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeGradedSubmissionsCursor returns the offset carried by the cursor;
// empty cursor = first page (offset 0). Unlike the HTTP offering handler's
// silent first-page fallback, a malformed cursor is a hard error here —
// gRPC callers get InvalidArgument (fail-loud, no silent reset).
func decodeGradedSubmissionsCursor(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, fmt.Errorf("malformed cursor: %w", err)
	}
	var wire gradedSubmissionsCursorWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return 0, fmt.Errorf("malformed cursor: %w", err)
	}
	if wire.Offset < 0 {
		return 0, errors.New("malformed cursor: negative offset")
	}
	return wire.Offset, nil
}

// gradedSubmissionSummaryToProto projects a released Submission onto the
// learner-visible summary shape.
//
//   - outcome derives from Submission.Passed EXACTLY as the learner result
//     payload does (myResultHandler renders `passed: sub.Passed != nil &&
//     *sub.Passed`), so a NULL passed coerces to FAILED — never fabricated.
//   - completed_at = graded_at: stamped by Submission.MarkGradedPendingRelease,
//     which every RELEASED row passed through; it is the grading-completion
//     instant. Left unset for a (schema-anomalous) released row missing it —
//     honest absence over a coalesced guess.
func gradedSubmissionSummaryToProto(sub *domain.Submission) *deliveryv1.GradedSubmissionSummary {
	if sub == nil {
		return nil
	}
	outcome := "FAILED"
	if sub.Passed != nil && *sub.Passed {
		outcome = "PASSED"
	}
	out := &deliveryv1.GradedSubmissionSummary{
		SubmissionId:   sub.ID,
		AssessmentId:   sub.AssessmentID,
		OverallComment: sub.OverallComment,
		Outcome:        outcome,
	}
	if sub.GradedAt != nil {
		out.CompletedAt = timestamppb.New(sub.GradedAt.UTC())
	}
	return out
}

// -----------------------------------------------------------------------------
// Helpers — domain ↔ proto mapping
// -----------------------------------------------------------------------------

func courseToProto(c *domain.Course, ownerGCID string, isPublic bool, description, priceCurrency string, priceAmountCents int64, locale string) *deliveryv1.Course {
	if c == nil {
		return nil
	}
	pcStatus := deliveryv1.CourseStatus_COURSE_STATUS_DRAFT
	return &deliveryv1.Course{
		CourseId:         c.ID,
		TenantId:         c.TenantID,
		Title:            c.Title,
		Description:      description,
		OwnerGcid:        ownerGCID,
		Status:           pcStatus,
		IsPublic:         isPublic,
		AtomIds:          append([]string(nil), c.AtomIDs...),
		PriceCurrency:    priceCurrency,
		PriceAmountCents: priceAmountCents,
		Locale:           locale,
		CreatedAt:        timestamppb.New(c.CreatedAt),
	}
}

func publicCourseToProto(pc *domain.PublicCourse) *deliveryv1.Course {
	if pc == nil {
		return nil
	}
	statusVal := deliveryv1.CourseStatus_COURSE_STATUS_PUBLISHED
	return &deliveryv1.Course{
		CourseId:         pc.ID,
		TenantId:         pc.TenantID,
		Title:            pc.Title,
		OwnerGcid:        pc.InstructorGCID,
		Status:           statusVal,
		IsPublic:         pc.Public,
		PriceCurrency:    "SGD",
		PriceAmountCents: int64(pc.PriceSGDCents),
		CreatedAt:        timestamppb.New(pc.CreatedAt),
	}
}

func enrollmentToProto(e *domain.Enrollment) *deliveryv1.Enrollment {
	if e == nil {
		return nil
	}
	return &deliveryv1.Enrollment{
		EnrollmentId: e.ID,
		CourseId:     e.CourseID,
		LearnerGcid:  e.GCID,
		TenantId:     e.TenantID,
		Status:       deliveryv1.EnrollmentStatus_ENROLLMENT_STATUS_ACTIVE,
		EnrolledAt:   timestamppb.New(e.EnrolledAt),
	}
}

func certToProto(c *domain.Certification, finalScore float32, expiresAt *timestamppb.Timestamp) *deliveryv1.Certificate {
	if c == nil {
		return nil
	}
	return &deliveryv1.Certificate{
		CertificateId: c.ID,
		CourseId:      c.CourseID,
		RecipientGcid: c.LearnerID,
		TenantId:      c.TenantID,
		Status:        deliveryv1.CertificateStatus_CERTIFICATE_STATUS_ISSUED,
		FinalScore:    finalScore,
		IssuedAt:      timestamppb.New(c.IssuedAt),
		ExpiresAt:     expiresAt,
	}
}
