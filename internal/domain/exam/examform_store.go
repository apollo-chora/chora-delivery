// examform_store.go — the hexagonal persistence ports for the Exam BC's
// ExamForm + ExamResult aggregates (W4 Brick-1).
//
// Mirrors store.go (the seed Exam aggregate's ExamStore port): the HTTP
// handlers depend ONLY on these interfaces so the backing store swaps between
// the in-memory dev adapter (inmem.ExamFormRepo / inmem.ExamResultRepo) and the
// Postgres adapters (pg.ExamFormRepo / pg.ExamResultRepo, chora_delivery.
// exam_forms / exam_results — durable across pod restart) at cmd/server wiring.
// ctx-threaded so the Postgres adapter can call rls.ApplySession before every
// query (RLS reads the tenant from tracing.TenantIDFromContext).
//
// This is a NEW file (not an edit to store.go) so the W4 Brick-1 track stays
// ADD-ONLY and never collides with the parallel Brick-3 (Candidate) track.
package exam

import "context"

// ExamFormStore is the persistence port for ExamForm aggregates.
//
//   - Save upserts a form (create + every FSM transition re-Save calls it).
//     Returns an error so a failed durable write is loud.
//   - Get resolves a form by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByExam returns a tenant's active (non-soft-deleted) forms for one exam.
type ExamFormStore interface {
	Save(ctx context.Context, f *ExamForm) error
	Get(ctx context.Context, id string) (*ExamForm, bool, error)
	ListByExam(ctx context.Context, tenantID, examID string) ([]*ExamForm, error)
}

// ExamResultStore is the persistence port for ExamResult aggregates. The
// durable ExamResult row is the source of truth for a candidate's outcome.
//
//   - Save inserts a result. Returns an error so a dropped write is loud.
//   - Get resolves a result by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error (CHO-2184).
//   - ListByForm returns a tenant's active results for one exam form.
type ExamResultStore interface {
	Save(ctx context.Context, r *ExamResult) error
	Get(ctx context.Context, id string) (*ExamResult, bool, error)
	ListByForm(ctx context.Context, tenantID, examFormID string) ([]*ExamResult, error)
	// ListByExam returns the tenant's results across ALL forms of one exam,
	// most-recent first (the R+ Results roster, CHO-2104).
	ListByExam(ctx context.Context, tenantID, examID string) ([]*ExamResult, error)
}
