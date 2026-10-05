// Package delivery_test holds the RED-phase TDD specs for the Content
// Delivery domain skeleton. It exercises the public domain API exclusively —
// it MUST NOT touch HTTP, persistence, or observability concerns.
//
// Aggregates under test (per the brief):
//   - Course
//   - Booking
//   - Certification (append-only)
package delivery_test

import (
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	gcidA   = "01970000-0000-7000-9000-000000000001"
	gcidB   = "01970000-0000-7000-9000-000000000002"
	gcidC   = "01970000-0000-7000-9000-000000000003"
)

// -----------------------------------------------------------------------------
// Course
// -----------------------------------------------------------------------------

func TestCourse_New_AssignsUUIDv7AndDefaults(t *testing.T) {
	t.Parallel()

	c, err := domain.NewCourse(tenantA, "Intro to Architecture", []string{"atom-1", "atom-2"}, 30)
	if err != nil {
		t.Fatalf("NewCourse: unexpected error: %v", err)
	}
	if c.ID == "" {
		t.Fatalf("NewCourse: expected non-empty ID")
	}
	if c.TenantID != tenantA {
		t.Fatalf("NewCourse: tenant_id mismatch: got %q want %q", c.TenantID, tenantA)
	}
	if c.MaxCapacity != 30 {
		t.Fatalf("NewCourse: max_capacity mismatch: got %d want 30", c.MaxCapacity)
	}
	if c.DeletedAt != nil {
		t.Fatalf("NewCourse: expected DeletedAt nil on a fresh course")
	}
	if c.CreatedAt.IsZero() {
		t.Fatalf("NewCourse: expected CreatedAt set")
	}
}

func TestCourse_New_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()

	_, err := domain.NewCourse(tenantA, "", nil, 1)
	if err == nil {
		t.Fatalf("NewCourse(empty title): expected error, got nil")
	}
}

func TestCourse_New_RejectsNonPositiveCapacity(t *testing.T) {
	t.Parallel()

	if _, err := domain.NewCourse(tenantA, "x", nil, 0); err == nil {
		t.Fatalf("NewCourse(capacity=0): expected error, got nil")
	}
	if _, err := domain.NewCourse(tenantA, "x", nil, -1); err == nil {
		t.Fatalf("NewCourse(capacity=-1): expected error, got nil")
	}
}

// -----------------------------------------------------------------------------
// Booking
// -----------------------------------------------------------------------------

// State machine: pending → confirmed → attended (or no-show).
// Disallowed transitions: pending → attended, confirmed → pending, attended → anything,
// no-show → anything.
func TestBooking_StatusTransition_AllowedPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		from, to domain.BookingStatus
		ok       bool
	}{
		{domain.BookingStatusPending, domain.BookingStatusConfirmed, true},
		{domain.BookingStatusConfirmed, domain.BookingStatusAttended, true},
		{domain.BookingStatusConfirmed, domain.BookingStatusNoShow, true},
		// disallowed
		{domain.BookingStatusPending, domain.BookingStatusAttended, false},
		{domain.BookingStatusPending, domain.BookingStatusNoShow, false},
		{domain.BookingStatusConfirmed, domain.BookingStatusPending, false},
		{domain.BookingStatusAttended, domain.BookingStatusConfirmed, false},
		{domain.BookingStatusAttended, domain.BookingStatusNoShow, false},
		{domain.BookingStatusNoShow, domain.BookingStatusAttended, false},
		{domain.BookingStatusNoShow, domain.BookingStatusConfirmed, false},
		// idempotent same-status: rejected (forces consumers to check)
		{domain.BookingStatusPending, domain.BookingStatusPending, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			t.Parallel()
			b, _ := domain.NewBookingForClass("class-1", "course-1", tenantA, gcidB)
			b.Status = tc.from
			err := b.TransitionStatus(tc.to)
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected error, got ok")
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Certification (append-only)
// -----------------------------------------------------------------------------

func TestCertification_Issue_ProducesUUIDv7AndHash(t *testing.T) {
	t.Parallel()

	c, _ := domain.NewCourse(tenantA, "Intro", nil, 5)
	accomplishments := []string{"atom-1:passed", "exam:passed"}
	cert, err := domain.IssueCertification(tenantA, gcidB, c.ID, accomplishments)
	if err != nil {
		t.Fatalf("IssueCertification: unexpected error: %v", err)
	}
	if cert.ID == "" {
		t.Fatalf("IssueCertification: expected non-empty cert ID")
	}
	if cert.Hash == "" {
		t.Fatalf("IssueCertification: expected hash present")
	}
	if cert.IssuedAt.IsZero() {
		t.Fatalf("IssueCertification: expected issued_at set")
	}
	// Hash must be deterministic for identical accomplishments.
	cert2, _ := domain.IssueCertification(tenantA, gcidB, c.ID, accomplishments)
	if cert.Hash != cert2.Hash {
		t.Fatalf("IssueCertification: hash should be deterministic over (gcid, course_id, accomplishments)")
	}
}

// Append-only invariant: cannot UPDATE or DELETE a Certification. The repo
// layer enforces this — the domain Certification value object exposes no
// mutators. Below we assert that the registry rejects re-issue for the
// same (gcid, course_id) pair.
func TestCertification_AppendOnly_RegistryRejectsReissue(t *testing.T) {
	t.Parallel()

	reg := domain.NewCertificationRegistry()
	c, _ := domain.NewCourse(tenantA, "Intro", nil, 5)
	if _, err := reg.Issue(tenantA, gcidB, c.ID, []string{"x"}); err != nil {
		t.Fatalf("Registry.Issue #1: unexpected error: %v", err)
	}
	_, err := reg.Issue(tenantA, gcidB, c.ID, []string{"x"})
	if err == nil {
		t.Fatalf("Registry.Issue #2: expected ErrCertAlreadyIssued, got nil")
	}
	if err != domain.ErrCertAlreadyIssued {
		t.Fatalf("Registry.Issue #2: expected ErrCertAlreadyIssued, got %v", err)
	}
}

// Different gcids on the same course are independent — registry must
// allow each learner to receive a cert exactly once.
func TestCertification_AppendOnly_RegistryAllowsDifferentLearners(t *testing.T) {
	t.Parallel()

	reg := domain.NewCertificationRegistry()
	c, _ := domain.NewCourse(tenantA, "Intro", nil, 5)
	if _, err := reg.Issue(tenantA, gcidB, c.ID, []string{"x"}); err != nil {
		t.Fatalf("Registry.Issue gcidB: unexpected error: %v", err)
	}
	if _, err := reg.Issue(tenantA, gcidC, c.ID, []string{"x"}); err != nil {
		t.Fatalf("Registry.Issue gcidC: unexpected error: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Soft-delete coverage
// -----------------------------------------------------------------------------

func TestCourse_SoftDelete_IsIdempotent(t *testing.T) {
	t.Parallel()
	c, _ := domain.NewCourse(tenantA, "x", nil, 1)
	if c.DeletedAt != nil {
		t.Fatalf("expected fresh course not soft-deleted")
	}
	c.SoftDelete()
	if c.DeletedAt == nil {
		t.Fatalf("expected DeletedAt set after first SoftDelete")
	}
	first := *c.DeletedAt
	c.SoftDelete() // second call must NOT bump the timestamp
	if !c.DeletedAt.Equal(first) {
		t.Fatalf("SoftDelete should be idempotent")
	}
}

func TestBooking_SoftDelete_IsIdempotent(t *testing.T) {
	t.Parallel()
	b, _ := domain.NewBookingForClass("class-1", "course-1", tenantA, gcidB)
	b.SoftDelete()
	if b.DeletedAt == nil {
		t.Fatalf("expected DeletedAt set on booking")
	}
	first := *b.DeletedAt
	b.SoftDelete()
	if !b.DeletedAt.Equal(first) {
		t.Fatalf("Booking.SoftDelete should be idempotent")
	}
}

// -----------------------------------------------------------------------------
// Guard-clause coverage on constructors
// -----------------------------------------------------------------------------

func TestNewCourse_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	if _, err := domain.NewCourse("", "x", nil, 1); err == nil {
		t.Fatalf("expected error for empty tenant_id")
	}
}

// -----------------------------------------------------------------------------
// IssueCertification guard-clauses + registry getters
// -----------------------------------------------------------------------------

func TestIssueCertification_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	if _, err := domain.IssueCertification("", gcidB, "course", []string{"x"}); err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}

func TestIssueCertification_RejectsEmptyLearner(t *testing.T) {
	t.Parallel()
	if _, err := domain.IssueCertification(tenantA, "", "course", []string{"x"}); err == nil {
		t.Fatalf("expected error for empty learner")
	}
}

func TestIssueCertification_RejectsEmptyCourse(t *testing.T) {
	t.Parallel()
	if _, err := domain.IssueCertification(tenantA, gcidB, "", []string{"x"}); err == nil {
		t.Fatalf("expected error for empty course_id")
	}
}

func TestRegistry_GetByID(t *testing.T) {
	t.Parallel()
	reg := domain.NewCertificationRegistry()
	c, _ := domain.NewCourse(tenantA, "x", nil, 1)
	cert, err := reg.Issue(tenantA, gcidB, c.ID, []string{"x"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, ok := reg.Get(cert.ID)
	if !ok {
		t.Fatalf("Get: expected cert to be present")
	}
	if got.ID != cert.ID {
		t.Fatalf("Get: ID mismatch")
	}
	if _, ok := reg.Get("does-not-exist"); ok {
		t.Fatalf("Get: expected miss for unknown id")
	}
}

func TestRegistry_GetByLearnerCourse(t *testing.T) {
	t.Parallel()
	reg := domain.NewCertificationRegistry()
	c, _ := domain.NewCourse(tenantA, "x", nil, 1)
	if _, err := reg.Issue(tenantA, gcidB, c.ID, []string{"x"}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, ok := reg.GetByLearnerCourse(gcidB, c.ID); !ok {
		t.Fatalf("GetByLearnerCourse: expected hit")
	}
	if _, ok := reg.GetByLearnerCourse(gcidC, c.ID); ok {
		t.Fatalf("GetByLearnerCourse: expected miss for different gcid")
	}
}
