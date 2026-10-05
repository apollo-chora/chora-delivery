// survey_repo.go — in-memory repository adapter for the Survey +
// SurveyResponse aggregates (Wave-5 R+ /r/surveys build-out).
//
// Production wiring (M16+) will swap in a Postgres adapter against
// chora_delivery.surveys + chora_delivery.survey_responses via PgBouncer;
// the domain layer is unchanged.
//
// Per .claude/rules/ddd-enforcement.md HARD RULE — cross-database queries
// FORBIDDEN. Survey + SurveyResponse rows live in chora_delivery; cross-
// aggregate references (course_id, gcid) are UUIDs without FK constraint.
//
// Per .claude/rules/ddd-enforcement.md aggregate invariant — SurveyResponse
// is append-only at the aggregate level. The repo SaveResponse method does
// not check for duplicates because NewSurveyResponse does not see the
// repo; the handler is responsible for the cross-aggregate duplicate
// check via HasResponse before calling NewSurveyResponse.
package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/survey"
)

// SurveyRepo is an in-memory store for Survey + SurveyResponse aggregates,
// scoped by tenant. Goroutine-safe via sync.RWMutex.
type SurveyRepo struct {
	mu        sync.RWMutex
	surveys   map[string]*survey.Survey         // key = survey_id
	responses map[string]*survey.SurveyResponse // key = response_id
}

// NewSurveyRepo returns an empty repo with both maps initialised.
func NewSurveyRepo() *SurveyRepo {
	return &SurveyRepo{
		surveys:   make(map[string]*survey.Survey),
		responses: make(map[string]*survey.SurveyResponse),
	}
}

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for pg.SurveyRepo at cmd/server wiring.
var _ survey.SurveyStore = (*SurveyRepo)(nil)

// Save inserts or upserts a survey. Defensive deep copy keeps the
// caller's pointer decoupled from the stored mirror (Questions +
// DistributedTo slices are copied). ctx is accepted to satisfy
// survey.SurveyStore (the pg adapter uses it for RLS); the in-memory store
// ignores it. Never errors.
func (r *SurveyRepo) Save(_ context.Context, s *survey.Survey) error {
	if s == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	cp.Questions = append([]survey.Question(nil), s.Questions...)
	cp.DistributedTo = append([]string(nil), s.DistributedTo...)
	r.surveys[s.ID] = &cp
	return nil
}

// Get returns a survey by ID and a bool ok flag. ok=false when the survey
// is soft-deleted (deleted_at non-nil) or not found. Returns a defensive
// copy so callers cannot mutate the stored aggregate state.
func (r *SurveyRepo) Get(_ context.Context, id string) (*survey.Survey, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.surveys[id]
	if !ok || s.DeletedAt != nil {
		return nil, false, nil
	}
	cp := *s
	cp.Questions = append([]survey.Question(nil), s.Questions...)
	cp.DistributedTo = append([]string(nil), s.DistributedTo...)
	return &cp, true, nil
}

// ListByTenant returns the non-soft-deleted surveys for a tenant, optionally
// filtered by state. Empty stateFilter returns all states. Results sorted
// by created_at DESC (newest first), with stable secondary sort by ID for
// tied timestamps (UUIDv7 carries unix_ts_ms so tied stamps are rare).
//
// The pg adapter equivalent is a single SELECT … WHERE tenant_id = $1
// [AND state = $2] AND deleted_at IS NULL ORDER BY created_at DESC, id.
func (r *SurveyRepo) ListByTenant(_ context.Context, tenantID string, stateFilter survey.SurveyState) ([]*survey.Survey, error) {
	r.mu.RLock()
	out := make([]*survey.Survey, 0, len(r.surveys))
	for _, s := range r.surveys {
		if s.TenantID != tenantID || s.DeletedAt != nil {
			continue
		}
		if stateFilter != "" && s.State != stateFilter {
			continue
		}
		cp := *s
		cp.Questions = append([]survey.Question(nil), s.Questions...)
		cp.DistributedTo = append([]string(nil), s.DistributedTo...)
		out = append(out, &cp)
	}
	r.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return strings.Compare(out[i].ID, out[j].ID) < 0
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// SaveResponse appends a SurveyResponse to the repo. Defensive copy of the
// Answers slice keeps the stored row decoupled from the caller's pointer.
// The duplicate (survey_id, gcid) check is enforced at the handler layer
// via HasResponse before NewSurveyResponse is constructed. ctx is accepted
// to satisfy survey.SurveyStore (the pg adapter uses it for RLS); the
// in-memory store ignores it. Never errors.
func (r *SurveyRepo) SaveResponse(_ context.Context, resp *survey.SurveyResponse) error {
	if resp == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *resp
	cp.Answers = append([]survey.Answer(nil), resp.Answers...)
	r.responses[resp.ID] = &cp
	return nil
}

// HasResponse reports whether a SurveyResponse already exists for the
// given (survey_id, gcid) tuple. Used by the handler to enforce the
// append-only invariant before constructing NewSurveyResponse.
//
// The pg adapter equivalent is SELECT EXISTS (SELECT 1 FROM
// survey_responses WHERE survey_id = $1 AND gcid = $2). ctx is accepted to
// satisfy survey.SurveyStore (the pg adapter uses it for RLS); the in-memory
// store ignores it. Never errors.
func (r *SurveyRepo) HasResponse(_ context.Context, surveyID, gcid string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, resp := range r.responses {
		if resp.SurveyID == surveyID && resp.GCID == gcid {
			return true, nil
		}
	}
	return false, nil
}

// ListResponsesBySurvey returns all SurveyResponses for a given survey,
// sorted by submitted_at ASC (oldest first) so the admin view shows the
// arrival order. Returns an empty (non-nil) slice when no responses exist.
//
// The pg adapter equivalent is SELECT … FROM survey_responses
// WHERE survey_id = $1 ORDER BY submitted_at ASC, id.
func (r *SurveyRepo) ListResponsesBySurvey(_ context.Context, surveyID string) ([]*survey.SurveyResponse, error) {
	r.mu.RLock()
	out := make([]*survey.SurveyResponse, 0, len(r.responses))
	for _, resp := range r.responses {
		if resp.SurveyID != surveyID {
			continue
		}
		cp := *resp
		cp.Answers = append([]survey.Answer(nil), resp.Answers...)
		out = append(out, &cp)
	}
	r.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SubmittedAt.Equal(out[j].SubmittedAt) {
			return strings.Compare(out[i].ID, out[j].ID) < 0
		}
		return out[i].SubmittedAt.Before(out[j].SubmittedAt)
	})
	return out, nil
}
