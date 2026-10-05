// store.go — the hexagonal persistence port for the Survey +
// SurveyResponse aggregates.
//
// Mirrors exam.ExamStore (R+ durability sweep): the HTTP handlers depend
// ONLY on this interface so the backing store swaps between the in-memory
// dev adapter (inmem.SurveyRepo) and pg.SurveyRepo (chora_delivery.surveys
// + chora_delivery.survey_responses — durable across pod restart) at
// cmd/server wiring. ctx-threaded so the Postgres adapter can call
// rls.ApplySession before every query (RLS reads the tenant from
// tracing.TenantIDFromContext). This closes the R+ durability debt:
// Surveys + SurveyResponses were inmem-only (ephemeral, lost on restart).
package survey

import "context"

// SurveyStore is the persistence port for the Survey + SurveyResponse
// aggregates (two tables in chora_delivery; one port because the handler
// treats them as a single feature surface).
//
//   - Save upserts a Survey (create + every FSM transition + the
//     response-count bump re-Save it). Returns an error so a failed durable
//     write is loud.
//   - Get resolves a Survey by id. ok=false is a GENUINE MISS; an infra/RLS
//     failure returns a non-nil error. CHO-2184: these were once the same
//     answer, so a dead DB read as an absent row.
//   - ListByTenant returns the tenant's active (non-soft-deleted) surveys,
//     optionally filtered by state (empty stateFilter ⇒ all states).
//   - SaveResponse appends a SurveyResponse (append-only at the aggregate
//     level; the handler pre-checks HasResponse).
//   - HasResponse reports whether a (survey_id, gcid) response already
//     exists — the duplicate guard the handler enforces before constructing
//     NewSurveyResponse.
//   - ListResponsesBySurvey returns every response for a survey (admin view).
type SurveyStore interface {
	Save(ctx context.Context, s *Survey) error
	Get(ctx context.Context, id string) (*Survey, bool, error)
	ListByTenant(ctx context.Context, tenantID string, stateFilter SurveyState) ([]*Survey, error)
	SaveResponse(ctx context.Context, resp *SurveyResponse) error
	HasResponse(ctx context.Context, surveyID, gcid string) (bool, error)
	ListResponsesBySurvey(ctx context.Context, surveyID string) ([]*SurveyResponse, error)
}
