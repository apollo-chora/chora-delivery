// claim_test.go — RED-phase tests for the SkillsFuturesClaim aggregate.
//
// SkillsFuturesClaim represents a Singapore-citizen learner's request to the
// SkillsFutures Singapore (SSG) scheme to fund their training enrolment. It
// is a chora-delivery aggregate per the R+ (Rhythm+) surface (training
// admin owns the approve / reject lifecycle).
//
// FSM:
//
//	PENDING → APPROVED  → DISBURSED   (happy path, training-admin approves +
//	                                    SSG eventually pays out)
//	PENDING → REJECTED                 (training-admin rejects)
//
// Rejection MUST carry a non-empty rejection_reason so the learner has
// actionable feedback. Approval MUST carry approved_amount_sgd_cents in
// [0, requested_amount_sgd_cents].
//
// Per .claude/rules/development-execution.md TDD requirement: this file
// is written FIRST and verified RED before the implementation file lands.
package skillsfutures_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/skillsfutures"
)

const (
	testTenantID    = "01970000-0000-7000-8000-000000000001"
	testLearnerGCID = "01970000-0000-7000-9000-000000000001"
	testAdminGCID   = "01970000-0000-7000-9000-000000000002"
	testCourseID    = "01970000-0000-7000-9000-000000000003"
	// NRIC hash — opaque opaque-blob the FE submits AFTER the gateway hashes
	// the raw NRIC (S-prefix Singapore National Registration ID). We never
	// store the raw NRIC anywhere.
	testNRICHash = "sha256:0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa"
)

// -----------------------------------------------------------------------------
// NewSkillsFuturesClaim — constructor + validation
// -----------------------------------------------------------------------------

func validClaimInput() skillsfutures.NewClaimInput {
	return skillsfutures.NewClaimInput{
		TenantID:             testTenantID,
		GCID:                 testLearnerGCID,
		CourseID:             testCourseID,
		NRICHash:             testNRICHash,
		RequestedAmountCents: 50000, // SGD 500.00
	}
}

func TestNewClaim_Happy_PendingWithFreshUUID(t *testing.T) {
	t.Parallel()
	c, err := skillsfutures.NewClaim(validClaimInput())
	if err != nil {
		t.Fatalf("NewClaim err=%v", err)
	}
	if c.State != skillsfutures.ClaimStatePending {
		t.Errorf("state=%q want PENDING", c.State)
	}
	if c.ID == "" {
		t.Errorf("ID empty")
	}
	if c.TenantID != testTenantID {
		t.Errorf("TenantID=%q want %q", c.TenantID, testTenantID)
	}
	if c.GCID != testLearnerGCID {
		t.Errorf("GCID=%q want %q", c.GCID, testLearnerGCID)
	}
	if c.RequestedAmountCents != 50000 {
		t.Errorf("RequestedAmountCents=%d want 50000", c.RequestedAmountCents)
	}
	if c.ApprovedAmountCents != 0 {
		t.Errorf("ApprovedAmountCents=%d want 0 (not yet approved)", c.ApprovedAmountCents)
	}
	if c.SubmittedAt.IsZero() {
		t.Errorf("SubmittedAt zero")
	}
	if c.DecidedAt != nil {
		t.Errorf("DecidedAt set on PENDING claim")
	}
}

func TestNewClaim_ValidationErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*skillsfutures.NewClaimInput)
		want   error
	}{
		{"no tenant", func(i *skillsfutures.NewClaimInput) { i.TenantID = "" }, skillsfutures.ErrTenantRequired},
		{"whitespace tenant", func(i *skillsfutures.NewClaimInput) { i.TenantID = "   " }, skillsfutures.ErrTenantRequired},
		{"no gcid", func(i *skillsfutures.NewClaimInput) { i.GCID = "" }, skillsfutures.ErrGCIDRequired},
		{"no course", func(i *skillsfutures.NewClaimInput) { i.CourseID = "" }, skillsfutures.ErrCourseIDRequired},
		{"no nric hash", func(i *skillsfutures.NewClaimInput) { i.NRICHash = "" }, skillsfutures.ErrNRICHashRequired},
		{"zero amount", func(i *skillsfutures.NewClaimInput) { i.RequestedAmountCents = 0 }, skillsfutures.ErrAmountPositive},
		{"negative amount", func(i *skillsfutures.NewClaimInput) { i.RequestedAmountCents = -1 }, skillsfutures.ErrAmountPositive},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := validClaimInput()
			tc.mutate(&in)
			_, err := skillsfutures.NewClaim(in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Approve — PENDING → APPROVED
// -----------------------------------------------------------------------------

func TestApprove_HappyPath(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	if err := c.Approve(testAdminGCID, 40000); err != nil {
		t.Fatalf("Approve err=%v", err)
	}
	if c.State != skillsfutures.ClaimStateApproved {
		t.Errorf("state=%q want APPROVED", c.State)
	}
	if c.ApprovedAmountCents != 40000 {
		t.Errorf("ApprovedAmountCents=%d want 40000", c.ApprovedAmountCents)
	}
	if c.DecidedByGCID != testAdminGCID {
		t.Errorf("DecidedByGCID=%q want %q", c.DecidedByGCID, testAdminGCID)
	}
	if c.DecidedAt == nil {
		t.Errorf("DecidedAt nil after Approve")
	}
}

func TestApprove_NotPending_Rejected(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	_ = c.Reject(testAdminGCID, "incomplete docs")
	err := c.Approve(testAdminGCID, 40000)
	if !errors.Is(err, skillsfutures.ErrNotPending) {
		t.Errorf("err=%v want ErrNotPending", err)
	}
}

func TestApprove_AmountOverRequested(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	err := c.Approve(testAdminGCID, 60000) // > 50000 requested
	if !errors.Is(err, skillsfutures.ErrApprovedExceedsRequested) {
		t.Errorf("err=%v want ErrApprovedExceedsRequested", err)
	}
}

func TestApprove_NegativeAmount(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	err := c.Approve(testAdminGCID, -1)
	if !errors.Is(err, skillsfutures.ErrAmountNonNegative) {
		t.Errorf("err=%v want ErrAmountNonNegative", err)
	}
}

func TestApprove_NoDeciderGCID(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	err := c.Approve("  ", 40000)
	if !errors.Is(err, skillsfutures.ErrDeciderRequired) {
		t.Errorf("err=%v want ErrDeciderRequired", err)
	}
}

// -----------------------------------------------------------------------------
// Reject — PENDING → REJECTED
// -----------------------------------------------------------------------------

func TestReject_HappyPath(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	if err := c.Reject(testAdminGCID, "incomplete supporting docs"); err != nil {
		t.Fatalf("Reject err=%v", err)
	}
	if c.State != skillsfutures.ClaimStateRejected {
		t.Errorf("state=%q want REJECTED", c.State)
	}
	if c.RejectionReason != "incomplete supporting docs" {
		t.Errorf("RejectionReason=%q want %q", c.RejectionReason, "incomplete supporting docs")
	}
	if c.DecidedAt == nil {
		t.Errorf("DecidedAt nil after Reject")
	}
	if c.DecidedByGCID != testAdminGCID {
		t.Errorf("DecidedByGCID=%q want %q", c.DecidedByGCID, testAdminGCID)
	}
}

func TestReject_NotPending(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	_ = c.Approve(testAdminGCID, 40000)
	err := c.Reject(testAdminGCID, "too late")
	if !errors.Is(err, skillsfutures.ErrNotPending) {
		t.Errorf("err=%v want ErrNotPending", err)
	}
}

func TestReject_EmptyReason(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	err := c.Reject(testAdminGCID, "")
	if !errors.Is(err, skillsfutures.ErrRejectionReasonRequired) {
		t.Errorf("err=%v want ErrRejectionReasonRequired", err)
	}
}

func TestReject_WhitespaceReason(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	err := c.Reject(testAdminGCID, "   \t  ")
	if !errors.Is(err, skillsfutures.ErrRejectionReasonRequired) {
		t.Errorf("err=%v want ErrRejectionReasonRequired", err)
	}
}

func TestReject_TrimsReasonWhitespace(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	_ = c.Reject(testAdminGCID, "  late submission  ")
	if c.RejectionReason != "late submission" {
		t.Errorf("RejectionReason=%q want trimmed %q", c.RejectionReason, "late submission")
	}
}

func TestReject_NoDeciderGCID(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	err := c.Reject(" ", "incomplete")
	if !errors.Is(err, skillsfutures.ErrDeciderRequired) {
		t.Errorf("err=%v want ErrDeciderRequired", err)
	}
}

// -----------------------------------------------------------------------------
// Disburse — APPROVED → DISBURSED
// -----------------------------------------------------------------------------

func TestDisburse_HappyPath(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	_ = c.Approve(testAdminGCID, 40000)
	if err := c.Disburse(); err != nil {
		t.Fatalf("Disburse err=%v", err)
	}
	if c.State != skillsfutures.ClaimStateDisbursed {
		t.Errorf("state=%q want DISBURSED", c.State)
	}
}

func TestDisburse_NotApproved(t *testing.T) {
	t.Parallel()
	c, _ := skillsfutures.NewClaim(validClaimInput())
	err := c.Disburse()
	if !errors.Is(err, skillsfutures.ErrNotApproved) {
		t.Errorf("err=%v want ErrNotApproved", err)
	}
}

// -----------------------------------------------------------------------------
// ClaimState.IsValid
// -----------------------------------------------------------------------------

func TestClaimState_IsValid(t *testing.T) {
	t.Parallel()
	valid := []skillsfutures.ClaimState{
		skillsfutures.ClaimStatePending,
		skillsfutures.ClaimStateApproved,
		skillsfutures.ClaimStateRejected,
		skillsfutures.ClaimStateDisbursed,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("IsValid(%q)=false want true", s)
		}
	}
	invalid := []skillsfutures.ClaimState{
		"", "pending", "WHATEVER", "PEDNING",
	}
	for _, s := range invalid {
		if s.IsValid() {
			t.Errorf("IsValid(%q)=true want false", s)
		}
	}
}

// -----------------------------------------------------------------------------
// Input trimming — tenant / gcid / course / nric_hash get trimmed
// -----------------------------------------------------------------------------

func TestNewClaim_TrimsStringFields(t *testing.T) {
	t.Parallel()
	in := validClaimInput()
	in.TenantID = "  " + testTenantID + "  "
	in.GCID = "\t" + testLearnerGCID + "\n"
	in.CourseID = " " + testCourseID
	in.NRICHash = testNRICHash + " "
	c, err := skillsfutures.NewClaim(in)
	if err != nil {
		t.Fatalf("NewClaim err=%v", err)
	}
	if c.TenantID != testTenantID {
		t.Errorf("TenantID=%q want trimmed %q", c.TenantID, testTenantID)
	}
	if c.GCID != testLearnerGCID {
		t.Errorf("GCID=%q want trimmed %q", c.GCID, testLearnerGCID)
	}
	if c.CourseID != testCourseID {
		t.Errorf("CourseID=%q want trimmed %q", c.CourseID, testCourseID)
	}
	if !strings.HasSuffix(c.NRICHash, "0000aaaa") {
		t.Errorf("NRICHash=%q want trimmed", c.NRICHash)
	}
	if strings.HasSuffix(c.NRICHash, " ") {
		t.Errorf("NRICHash trailing whitespace: %q", c.NRICHash)
	}
}
