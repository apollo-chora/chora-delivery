// verification_claim_reader.go — an in-memory / test DOUBLE for the
// exam.VerificationClaimReader outbound port (Identity Singpass/KYC claim).
//
// HONEST DOUBLE — NOT A PRODUCTION ADAPTER. This is explicitly a dev/test
// stand-in used to drive TDD of the candidate admission gate. It defaults to
// NOT verified (empty set ⇒ IsVerified returns false), so it can NEVER be
// mistaken for a forbidden "always verified" production fake: a claim only
// resolves VERIFIED after an explicit MarkVerified call (in tests, seeding the
// real-world fact that Identity resolved the claim).
//
// The REAL adapter — a client against a (not-yet-built) chora-identity
// service-to-service by-GCID verification-claim endpoint — is the ADR-190
// follow-up. See the crib in exam_candidate_handler.go. Until it exists the
// verify + admit routes stay DARK (the handler returns 501 when the port is
// nil); no fake production adapter is shipped.
package inmem

import (
	"context"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// VerificationClaimReader is an in-memory VerificationClaimReader double. The
// verified set is keyed by "tenantID|gcid".
type VerificationClaimReader struct {
	mu       sync.RWMutex
	verified map[string]bool
	// err, when set, is returned by IsVerified to exercise the fail-loud
	// upstream-error path in tests.
	err error
}

// NewVerificationClaimReader returns an empty (nothing verified) double.
func NewVerificationClaimReader() *VerificationClaimReader {
	return &VerificationClaimReader{verified: make(map[string]bool)}
}

// Compile-time assertion: satisfies the domain outbound port.
var _ exam.VerificationClaimReader = (*VerificationClaimReader)(nil)

func vcKey(tenantID, gcid string) string { return tenantID + "|" + gcid }

// MarkVerified seeds a (tenant, gcid) as holding a VERIFIED claim (test setup).
func (v *VerificationClaimReader) MarkVerified(tenantID, gcid string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.verified[vcKey(tenantID, gcid)] = true
}

// SetError makes IsVerified return err (exercises the upstream-failure path).
func (v *VerificationClaimReader) SetError(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
}

// IsVerified reports whether the (tenant, gcid) holds a VERIFIED claim.
func (v *VerificationClaimReader) IsVerified(_ context.Context, tenantID, gcid string) (bool, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.err != nil {
		return false, v.err
	}
	return v.verified[vcKey(tenantID, gcid)], nil
}
