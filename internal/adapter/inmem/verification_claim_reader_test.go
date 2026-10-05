// verification_claim_reader_test.go — direct unit tests for the in-memory
// exam.VerificationClaimReader DEV/TEST DOUBLE (ADR-190 identity gate). The
// double must default to NOT verified and only flip after an explicit
// MarkVerified — and SetError must make IsVerified fail loud.
package inmem_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

func TestVerificationClaimReader_DefaultsToNotVerified(t *testing.T) {
	t.Parallel()
	v := inmem.NewVerificationClaimReader()
	verified, err := v.IsVerified(context.Background(), tenantA, gcidA)
	if err != nil {
		t.Fatalf("IsVerified: %v", err)
	}
	if verified {
		t.Fatal("a fresh double must report NOT verified (never an always-verified fake)")
	}
}

func TestVerificationClaimReader_MarkVerified(t *testing.T) {
	t.Parallel()
	v := inmem.NewVerificationClaimReader()
	v.MarkVerified(tenantA, gcidA)

	verified, err := v.IsVerified(context.Background(), tenantA, gcidA)
	if err != nil {
		t.Fatalf("IsVerified: %v", err)
	}
	if !verified {
		t.Fatal("after MarkVerified the (tenant, gcid) must resolve verified")
	}

	// The verified set is keyed by tenant|gcid — a different tenant or gcid
	// must still read unverified.
	if got, _ := v.IsVerified(context.Background(), tenantB, gcidA); got {
		t.Fatal("verification must not leak across tenants")
	}
	if got, _ := v.IsVerified(context.Background(), tenantA, "other-gcid"); got {
		t.Fatal("verification must not leak across gcids")
	}
}

func TestVerificationClaimReader_SetError_FailsLoud(t *testing.T) {
	t.Parallel()
	v := inmem.NewVerificationClaimReader()
	v.MarkVerified(tenantA, gcidA)
	boom := errors.New("identity upstream down")
	v.SetError(boom)

	_, err := v.IsVerified(context.Background(), tenantA, gcidA)
	if !errors.Is(err, boom) {
		t.Fatalf("expected SetError to make IsVerified return %v, got %v", boom, err)
	}

	// Clearing the error restores normal resolution.
	v.SetError(nil)
	verified, err := v.IsVerified(context.Background(), tenantA, gcidA)
	if err != nil || !verified {
		t.Fatalf("after clearing the error the claim must resolve verified; verified=%v err=%v", verified, err)
	}
}
