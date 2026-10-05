// survey_handler.go — HTTP handlers for the Wave-5 R+ /r/surveys surface
// (R+ training feedback collection — DRAFT → DISTRIBUTED → CLOSED FSM).
//
// 7 endpoints registered in handlers.go integration diff:
//
//	POST   /api/v1/surveys                   — create DRAFT (instructor / admin / training-admin)
//	GET    /api/v1/surveys[?state=]          — list tenant surveys, optional state filter
//	GET    /api/v1/surveys/{id}              — single fetch (tenant-scoped 404 on miss)
//	POST   /api/v1/surveys/{id}/publish      — DRAFT → DISTRIBUTED (training-admin)
//	POST   /api/v1/surveys/{id}/close        — DISTRIBUTED → CLOSED (training-admin)
//	POST   /api/v1/surveys/{id}/responses    — learner submits a SurveyResponse
//	GET    /api/v1/surveys/{id}/responses    — admin lists responses (training-admin)
//
// Domain ↔ URL naming reconciliation:
//
//	The domain Survey FSM is DRAFT → DISTRIBUTED → CLOSED (per
//	services/chora-delivery/internal/domain/survey/survey.go::SurveyState).
//	The URL action `/publish` maps onto Survey.Distribute() so the
//	instructor-facing terminology ("publish to learners") stays consistent
//	with the R+ FE CTA labels. The wire `state` field reflects the
//	canonical domain value (DRAFT / DISTRIBUTED / CLOSED) — the FE does
//	NOT rename it (per feedback_no_stubs_real_wiring honest wire shape).
//
// Authorisation (verified inside the handler, NOT at middleware):
//   - tenantRequired (X-Tenant-Id) — handled via callerTenantGCID
//   - gcid header required for caller identity (401 on miss)
//   - POST create / POST publish / POST close / GET responses require
//     instructor / admin / training-admin role via hasInstructorOrAdmin
//   - POST responses allows any authed in-tenant caller with a gcid
//     (learners submit on their own behalf)
//   - GET list / GET by-id: any in-tenant caller (tenant-scoped read)
//
// Add-only: the existing Deps struct gains one new field
// `Surveys *inmem.SurveyRepo` via integration diff; nothing in this file
// touches handlers.go / cmd/server. See INTEGRATION MANIFEST in the PR
// body for the canonical wiring.
//
// Per .claude/rules/ddd-enforcement.md cross-DB queries FORBIDDEN —
// course_id + gcid are cross-aggregate UUID references without FK;
// validation (e.g., does the course exist? is the gcid in the cohort?)
// is intentionally NOT performed here. A Pub/Sub-validated upstream
// guard lands when the production Postgres adapter ships.
//
// Per .claude/rules/ddd-enforcement.md SurveyResponse aggregate invariant
// — append-only: NewSurveyResponse cannot see the repo so the handler
// pre-checks via repo.HasResponse(surveyID, gcid) before constructing the
// aggregate. Duplicate submission → 409 ErrSurveyResponseDuplicate shape.
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/survey"
)

// -----------------------------------------------------------------------------
// Request DTOs
// -----------------------------------------------------------------------------

type surveyQuestionReq struct {
	QuestionID string   `json:"question_id,omitempty"`
	Prompt     string   `json:"prompt"`
	Type       string   `json:"type"`
	Options    []string `json:"options,omitempty"`
}

type createSurveyReq struct {
	CourseID  string              `json:"course_id"`
	Title     string              `json:"title"`
	Questions []surveyQuestionReq `json:"questions"`
}

type publishSurveyReq struct {
	Recipients []string `json:"recipients"`
}

type surveyAnswerReq struct {
	QuestionID string `json:"question_id"`
	Value      string `json:"value"`
}

type submitSurveyResponseReq struct {
	Answers []surveyAnswerReq `json:"answers"`
}

// -----------------------------------------------------------------------------
// Dispatchers — registered by handlers.go integration diff
// -----------------------------------------------------------------------------

// surveysRootHandler dispatches /api/v1/surveys (no path suffix).
//
//	GET  → list (tenant-scoped, optional ?state=)
//	POST → create DRAFT (RBAC: instructor / admin / training-admin)
func surveysRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleSurveyList(deps, w, r)
		case http.MethodPost:
			handleSurveyCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// surveysSubHandler dispatches /api/v1/surveys/{id}, /{id}/{action}, and
// /{id}/responses.
//
//	GET    /api/v1/surveys/{id}              → fetch one (tenant-scoped 404)
//	POST   /api/v1/surveys/{id}/publish      → DRAFT → DISTRIBUTED
//	POST   /api/v1/surveys/{id}/close        → DISTRIBUTED → CLOSED
//	POST   /api/v1/surveys/{id}/responses    → learner submission
//	GET    /api/v1/surveys/{id}/responses    → admin lists responses
func surveysSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/surveys/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		parts := strings.Split(rest, "/")
		switch len(parts) {
		case 1:
			surveyID := parts[0]
			if surveyID == "" {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleSurveyGet(deps, surveyID, w, r)
		case 2:
			surveyID := parts[0]
			action := parts[1]
			if surveyID == "" || action == "" {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			switch action {
			case "publish":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleSurveyPublish(deps, surveyID, w, r)
			case "close":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleSurveyClose(deps, surveyID, w, r)
			case "responses":
				switch r.Method {
				case http.MethodPost:
					handleSurveyResponseCreate(deps, surveyID, w, r)
				case http.MethodGet:
					handleSurveyResponsesList(deps, surveyID, w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
			default:
				writeError(w, http.StatusNotFound, "not found")
			}
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleSurveyCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Surveys == nil {
		writeError(w, http.StatusServiceUnavailable, "surveys repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid // captured for future audit / event emission
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createSurveyReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	qs := make([]survey.Question, 0, len(req.Questions))
	for _, q := range req.Questions {
		qs = append(qs, survey.Question{
			QuestionID: q.QuestionID,
			Prompt:     q.Prompt,
			Type:       survey.QuestionType(q.Type),
			Options:    q.Options,
		})
	}
	s, err := survey.NewSurvey(survey.NewSurveyInput{
		TenantID:  tenantID,
		CourseID:  req.CourseID,
		Title:     req.Title,
		Questions: qs,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	if err := deps.Surveys.Save(ctx, s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, surveyDTO(s))
}

// callerGCID reads the mesh-stamped learner identity. The BFF stamps lowercase
// `gcid`; X-Chora-GCID is the canonical fallback, matching the other handlers.
func callerGCID(r *http.Request) string {
	if g := strings.TrimSpace(r.Header.Get("gcid")); g != "" {
		return g
	}
	return strings.TrimSpace(r.Header.Get("X-Chora-GCID"))
}

// surveysVisibleToLearner narrows a tenant survey list to the ones the given
// learner may see: DISTRIBUTED or CLOSED, and naming them in DistributedTo.
//
// A DRAFT is never visible: it is unpublished admin content, and Distribute()
// is the only transition that populates DistributedTo, so a DRAFT has no
// recipients to match anyway. The state check is kept explicit so a future
// transition that seeds recipients early cannot silently leak drafts.
//
// An empty gcid matches nothing, so an unidentified caller receives an empty
// list rather than the tenant's surveys.
func surveysVisibleToLearner(items []*survey.Survey, gcid string) []*survey.Survey {
	out := make([]*survey.Survey, 0, len(items))
	if gcid == "" {
		return out
	}
	for _, s := range items {
		if s == nil || s.State == survey.SurveyStateDraft {
			continue
		}
		for _, rcpt := range s.DistributedTo {
			if rcpt == gcid {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func handleSurveyList(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Surveys == nil {
		writeError(w, http.StatusServiceUnavailable, "surveys repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	var state survey.SurveyState
	if s := strings.TrimSpace(r.URL.Query().Get("state")); s != "" {
		candidate := survey.SurveyState(s)
		if !candidate.IsValid() {
			writeError(w, http.StatusBadRequest, "invalid state: "+s)
			return
		}
		state = candidate
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	items, err := deps.Surveys.ListByTenant(ctx, tenantID, state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// CHO-2352 - role-scope the list. Tenant scoping alone handed every learner
	// the whole tenant's surveys, including DRAFT (unpublished) titles, course
	// ids and question prompts, plus the distributed_to recipient gcids of other
	// learners. Staff keep the full list; everyone else sees ONLY surveys
	// actually distributed to them, which is what the A+ learner surface needs.
	// Fails CLOSED: an unknown caller (no roles, or no gcid) gets nothing rather
	// than everything.
	if !hasInstructorOrAdmin(r) {
		items = surveysVisibleToLearner(items, callerGCID(r))
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, s := range items {
		out = append(out, surveyDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

func handleSurveyGet(deps Deps, surveyID string, w http.ResponseWriter, r *http.Request) {
	if deps.Surveys == nil {
		writeError(w, http.StatusServiceUnavailable, "surveys repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	s, ok, err := deps.Surveys.Get(ctx, surveyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "survey lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "survey not found")
		return
	}
	writeJSON(w, http.StatusOK, surveyDTO(s))
}

// handleSurveyPublish serves POST /api/v1/surveys/{id}/publish.
//
// Maps the instructor-facing "publish" action onto Survey.Distribute(),
// which requires ≥1 recipient gcid. The recipient roster lands on the
// aggregate's DistributedTo field and bumps the FSM into DISTRIBUTED.
func handleSurveyPublish(deps Deps, surveyID string, w http.ResponseWriter, r *http.Request) {
	if deps.Surveys == nil {
		writeError(w, http.StatusServiceUnavailable, "surveys repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req publishSurveyReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	s, ok, err := deps.Surveys.Get(ctx, surveyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "survey lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "survey not found")
		return
	}
	if err := s.Distribute(req.Recipients); err != nil {
		writeSurveyDomainError(w, err)
		return
	}
	if err := deps.Surveys.Save(ctx, s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, surveyDTO(s))
}

// handleSurveyClose serves POST /api/v1/surveys/{id}/close.
//
// Transitions DISTRIBUTED → CLOSED via Survey.Close(). Existing responses
// remain readable; no new responses accepted after close.
func handleSurveyClose(deps Deps, surveyID string, w http.ResponseWriter, r *http.Request) {
	if deps.Surveys == nil {
		writeError(w, http.StatusServiceUnavailable, "surveys repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	s, ok, err := deps.Surveys.Get(ctx, surveyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "survey lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "survey not found")
		return
	}
	if err := s.Close(); err != nil {
		writeSurveyDomainError(w, err)
		return
	}
	if err := deps.Surveys.Save(ctx, s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, surveyDTO(s))
}

// handleSurveyResponseCreate serves POST /api/v1/surveys/{id}/responses.
//
// Allows any authed in-tenant caller (learners submit on their own behalf
// — the gcid header is the responding learner identity). The duplicate
// (survey_id, gcid) guard is pre-checked here because
// NewSurveyResponse cannot see the repo (per the aggregate's documented
// contract). On duplicate → 409 with the canonical
// ErrSurveyResponseDuplicate message.
func handleSurveyResponseCreate(deps Deps, surveyID string, w http.ResponseWriter, r *http.Request) {
	if deps.Surveys == nil {
		writeError(w, http.StatusServiceUnavailable, "surveys repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	s, ok, err := deps.Surveys.Get(ctx, surveyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "survey lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "survey not found")
		return
	}
	var req submitSurveyResponseReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dup, err := deps.Surveys.HasResponse(ctx, surveyID, gcid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dup {
		writeError(w, http.StatusConflict, survey.ErrSurveyResponseDuplicate.Error())
		return
	}
	answers := make([]survey.Answer, 0, len(req.Answers))
	for _, a := range req.Answers {
		answers = append(answers, survey.Answer{
			QuestionID: a.QuestionID,
			Value:      a.Value,
		})
	}
	resp, err := survey.NewSurveyResponse(survey.NewSurveyResponseInput{
		Survey:  s,
		GCID:    gcid,
		Answers: answers,
	})
	if err != nil {
		writeSurveyDomainError(w, err)
		return
	}
	if err := deps.Surveys.SaveResponse(ctx, resp); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Bump the aggregate response_count projection + persist.
	s.IncrementResponseCount()
	if err := deps.Surveys.Save(ctx, s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, surveyResponseDTO(resp))
}

// handleSurveyResponsesList serves GET /api/v1/surveys/{id}/responses.
//
// RBAC: instructor / admin / training-admin only (no learner self-view —
// that's a future iteration when the FE gains a per-learner "your past
// submissions" surface).
func handleSurveyResponsesList(deps Deps, surveyID string, w http.ResponseWriter, r *http.Request) {
	if deps.Surveys == nil {
		writeError(w, http.StatusServiceUnavailable, "surveys repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	s, ok, err := deps.Surveys.Get(ctx, surveyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "survey lookup failed: "+err.Error())
		return
	}
	if !ok || s == nil || s.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "survey not found")
		return
	}
	items, err := deps.Surveys.ListResponsesBySurvey(ctx, surveyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, resp := range items {
		out = append(out, surveyResponseDTO(resp))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

// -----------------------------------------------------------------------------
// Error mapping — domain sentinel → HTTP status
// -----------------------------------------------------------------------------

// writeSurveyDomainError maps a survey domain sentinel to an HTTP error
// envelope:
//   - FSM guards (ErrSurveyNotDraft / ErrSurveyNotDistributed /
//     ErrSurveyResponseSurveyNotOpen / ErrSurveyResponseDuplicate) → 409 CONFLICT
//   - validation guards (ErrSurveyTitleRequired / ErrSurveyDistribute
//     RecipientsRequired / ErrSurveyResponseLikertOutOfRange / etc.) → 400 BAD REQUEST
func writeSurveyDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, survey.ErrSurveyNotDraft),
		errors.Is(err, survey.ErrSurveyNotDistributed),
		errors.Is(err, survey.ErrSurveyResponseSurveyNotOpen),
		errors.Is(err, survey.ErrSurveyResponseDuplicate):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// -----------------------------------------------------------------------------
// DTOs
// -----------------------------------------------------------------------------

// surveyDTO renders a Survey for the JSON wire. Field naming aligns with
// the chora_delivery.surveys columns the pg adapter will emit at M12.3+
// (snake_case + timestamp ISO-8601 nano precision).
func surveyDTO(s *survey.Survey) map[string]interface{} {
	qs := make([]map[string]interface{}, 0, len(s.Questions))
	for _, q := range s.Questions {
		row := map[string]interface{}{
			"question_id": q.QuestionID,
			"prompt":      q.Prompt,
			"type":        string(q.Type),
		}
		if len(q.Options) > 0 {
			row["options"] = q.Options
		}
		qs = append(qs, row)
	}
	distributedTo := make([]string, 0, len(s.DistributedTo))
	distributedTo = append(distributedTo, s.DistributedTo...)
	out := map[string]interface{}{
		"id":             s.ID,
		"tenant_id":      s.TenantID,
		"course_id":      s.CourseID,
		"title":          s.Title,
		"questions":      qs,
		"distributed_to": distributedTo,
		"state":          string(s.State),
		"response_count": s.ResponseCount,
		"created_at":     s.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":     s.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if s.DistributedAt != nil {
		out["distributed_at"] = s.DistributedAt.UTC().Format(time.RFC3339Nano)
	}
	if s.ClosedAt != nil {
		out["closed_at"] = s.ClosedAt.UTC().Format(time.RFC3339Nano)
	}
	if s.DeletedAt != nil {
		out["deleted_at"] = s.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// surveyResponseDTO renders a SurveyResponse for the JSON wire.
func surveyResponseDTO(resp *survey.SurveyResponse) map[string]interface{} {
	answers := make([]map[string]interface{}, 0, len(resp.Answers))
	for _, a := range resp.Answers {
		answers = append(answers, map[string]interface{}{
			"question_id": a.QuestionID,
			"value":       a.Value,
		})
	}
	return map[string]interface{}{
		"id":           resp.ID,
		"survey_id":    resp.SurveyID,
		"gcid":         resp.GCID,
		"answers":      answers,
		"submitted_at": resp.SubmittedAt.UTC().Format(time.RFC3339Nano),
	}
}
