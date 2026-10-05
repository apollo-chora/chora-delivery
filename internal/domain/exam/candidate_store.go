// candidate_store.go — the hexagonal ports for the Candidate aggregate.
//
// Two ports live here (both defined in the domain; adapters implement them —
// dependency arrow points inward, never out):
//
//  1. CandidateStore — the PERSISTENCE port (mirrors ExamStore in store.go).
//     Handlers depend ONLY on this interface so the backing store swaps
//     between inmem (dev/tests) and pg.CandidateRepo (chora_delivery
//     .exam_candidates — durable + RLS-isolated) at cmd/server wiring.
//
//  2. VerificationClaimReader — the OUTBOUND port to the Identity domain's
//     verification claim (Singpass/KYC), per ADR-190 D2 "admission is gated by
//     an Identity-owned verification claim".
//
// ctx is threaded so the Postgres adapter can call rls.ApplySession before
// every query (RLS reads the tenant from tracing.TenantIDFromContext).
package exam

import "context"

// CandidateStore is the persistence port for Candidate aggregates.
//
//   - Save upserts a candidate (create + every FSM transition re-Save calls it).
//     Returns an error so a failed durable write is loud.
//   - GetByExamAndGCID resolves the active (non-soft-deleted) candidate for a
//     (tenant, exam, gcid) triple — the natural admission-flow lookup.
//     ok=false is a GENUINE MISS; an infra/RLS failure returns a non-nil error.
//     CHO-2184: these were once the same answer, so a dead DB read as an
//     absent row.
//   - ListByExam returns a sitting's active candidates for the roster view.
type CandidateStore interface {
	Save(ctx context.Context, c *Candidate) error
	GetByExamAndGCID(ctx context.Context, tenantID, examID, gcid string) (*Candidate, bool, error)
	ListByExam(ctx context.Context, tenantID, examID string) ([]*Candidate, error)
}

// VerificationClaimReader is the OUTBOUND port to the Identity domain's
// identity-verification claim (Singpass / KYC). IsVerified reports whether the
// given learner GCID currently holds a VERIFIED verification claim, scoped to
// the tenant.
//
// ADR-190 D2: candidate admission is gated by an Identity-owned verification
// claim. Cross-domain reference (gcid) is an opaque UUID validated over the
// wire — no cross-DB query, no FK.
//
// DEPLOYED-REALITY NOTE (fail-loud, no fake): as of 2026-07-09 chora-identity
// exposes NO service-to-service / by-GCID verification-claim read endpoint —
// its KYC surface (GET /v1/me/kyc/status) is self/"me"-scoped (bound to the
// caller's own bearer-token GCID). A production adapter therefore CANNOT be
// wired yet without a NEW chora-identity endpoint (e.g. an internal
// GET /internal/v1/kyc/status?gcid= or an IsVerified gRPC, gated to the
// service SA). Until that lands, the verify + admit routes are DARK — see the
// crib in exam_candidate_handler.go. The inmem double drives TDD; NO
// always-verified production adapter is shipped (that would be a forbidden
// fake).
type VerificationClaimReader interface {
	IsVerified(ctx context.Context, tenantID, gcid string) (bool, error)
}
