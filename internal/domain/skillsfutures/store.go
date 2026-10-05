// store.go — the hexagonal persistence port for the SkillsFuturesClaim
// aggregate.
//
// Mirrors exam.ExamStore (CHO-1580): the HTTP handlers depend ONLY on this
// interface so the backing store swaps between the in-memory dev adapter
// (inmem.SkillsFuturesRepo) and pg.SkillsFuturesRepo
// (chora_delivery.skillsfutures_claims — durable across pod restart) at
// cmd/server wiring. ctx-threaded so the Postgres adapter can call
// rls.ApplySession before every query (RLS reads the tenant from
// tracing.TenantIDFromContext). This closes the R+ durability debt:
// SkillsFutures claims were inmem-only (ephemeral, lost on pod restart) —
// unacceptable for SSG funding queue rows kept for government audit.
package skillsfutures

import "context"

// SkillsFuturesStore is the persistence port for SkillsFuturesClaim aggregates.
//
//   - Save upserts a claim (create + every FSM transition — approve / reject /
//     disburse — re-Save call it). Returns an error so a failed durable write
//     is loud.
//   - Get resolves a claim by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row. The caller enforces tenant +
//     visibility scope on top of the returned row.
//   - ListByTenant returns the tenant's claims, optionally filtered by state.
//     An empty stateFilter returns all states (mirrors the in-mem semantics).
type SkillsFuturesStore interface {
	Save(ctx context.Context, c *SkillsFuturesClaim) error
	Get(ctx context.Context, id string) (*SkillsFuturesClaim, bool, error)
	ListByTenant(ctx context.Context, tenantID string, stateFilter ClaimState) ([]*SkillsFuturesClaim, error)
}
