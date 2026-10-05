// sitting_store.go — the hexagonal persistence ports for the W4 Brick-B
// operational-sitting aggregates (ExamSitting, ExamInvigilator, IncidentReport).
//
// Three ports live here (all defined in the domain; adapters implement them —
// the dependency arrow points inward, never out). Handlers depend ONLY on these
// interfaces so the backing store swaps between inmem (dev/tests) and the pg
// repos (chora_delivery, migration 0048 — durable + RLS-isolated) at
// cmd/server wiring. This is a NEW store file (mirrors Brick-3's
// candidate_store.go); store.go (Brick-1 ExamStore) is left untouched.
//
// ctx is threaded so the Postgres adapters can call rls.ApplySession before
// every query (RLS reads the tenant from tracing.TenantIDFromContext).
package exam

import "context"

// ExamSittingStore is the persistence port for ExamSitting aggregates.
//
//   - Save upserts a sitting (create + every FSM transition + soft-delete
//     re-Save calls it). Returns an error so a failed durable write is loud.
//   - Get resolves a sitting by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByExam returns an exam's active (non-soft-deleted) sittings.
type ExamSittingStore interface {
	Save(ctx context.Context, s *ExamSitting) error
	Get(ctx context.Context, id string) (*ExamSitting, bool, error)
	ListByExam(ctx context.Context, tenantID, examID string) ([]*ExamSitting, error)
}

// ExamInvigilatorStore is the persistence port for ExamInvigilator aggregates.
//
//   - Save upserts an assignment (create + unassign re-Save calls it).
//   - ListBySitting returns a sitting's active (non-soft-deleted) invigilators —
//     the roster the assign use-case passes to exam.EnsureSingleChief, and the
//     source for the GET roster view.
//   - Get resolves an assignment by id (for the DELETE/unassign path).
type ExamInvigilatorStore interface {
	Save(ctx context.Context, iv *ExamInvigilator) error
	ListBySitting(ctx context.Context, tenantID, sittingID string) ([]*ExamInvigilator, error)
	Get(ctx context.Context, id string) (*ExamInvigilator, bool, error)
}

// IncidentReportStore is the APPEND-ONLY persistence port for IncidentReport
// records — there is deliberately no Update / Delete (the aggregate is
// immutable once created; the pg adapter is granted SELECT + INSERT only).
//
//   - Append inserts a new immutable record. Returns an error so a dropped
//     write is loud.
//   - ListBySitting returns a sitting's incident records (the audit trail).
//   - Get resolves one record by id.
type IncidentReportStore interface {
	Append(ctx context.Context, ir *IncidentReport) error
	ListBySitting(ctx context.Context, tenantID, sittingID string) ([]*IncidentReport, error)
	Get(ctx context.Context, id string) (*IncidentReport, bool, error)
}
