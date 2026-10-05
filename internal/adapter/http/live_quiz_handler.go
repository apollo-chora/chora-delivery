// live_quiz_handler.go — HTTP handlers for the R+ classroom-realtime
// LiveQuiz aggregate (M9, Stage C-lite wave-5).
//
// 5 endpoints registered via the handlers.go integration diff:
//
//	POST  /api/v1/live-quizzes              — create DRAFT (instructor/admin)
//	GET   /api/v1/live-quizzes              — list (current tenant)
//	GET   /api/v1/live-quizzes/{id}         — fetch one (tenant-scoped 404)
//	PATCH /api/v1/live-quizzes/{id}         — EditDraft (DRAFT-only; 409 otherwise)
//	POST  /api/v1/live-quizzes/{id}/publish — DRAFT → PUBLISHED transition
//
// Authorisation (verified inside the handler, NOT at middleware):
//   - tenantRequired enforces X-Tenant-Id presence (400 otherwise).
//   - gcid header required on writes (401 if missing) — Bucket 4 servicemesh
//     propagation guarantees this when the caller's JWT is valid.
//   - POST / PATCH / publish: caller carries `instructor`, `admin`, or
//     `training-admin` role (403 otherwise). Mirrors the CJ#2 course pattern
//   - exam_handler.go.
//   - GET list / GET by-id: any in-tenant caller (tenant-scoped read; the
//     presenter UI loads its quiz list from here without the admin role
//     gate, but X-Tenant-Id MUST resolve to the caller's tenant).
//
// Add-only: the existing Deps struct gains one new field `LiveQuizzes
// *inmem.LiveQuizRepo` via integration diff; nothing else in this file
// touches handlers.go / cmd/server. Per
// `feedback_parallel_agent_contract_drift` master integrates handlers.go.
//
// Per the R+ Stage C-lite wave-5 brief — closes the wave-3 mock-data debt
// (QuizBuilderService.getCspoDraft hard-coded the CSPO Sprint Planning
// fixture) by surfacing real DRAFT → PUBLISHED authoring via the BFF.
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// Request DTOs
// -----------------------------------------------------------------------------

// createLiveQuizReq is the POST /api/v1/live-quizzes body.
type createLiveQuizReq struct {
	CourseID string `json:"course_id"`
	Title    string `json:"title"`
}

// liveQuizOptionReq matches the domain LiveQuizOption JSON shape. Defined
// locally so the wire schema is explicit + reviewable in one place.
type liveQuizOptionReq struct {
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
	// Explainer — post-reveal explanation (ADR-168). FE quiz-builder supplies
	// it; surfaced per the quiz's ExplainerMode.
	Explainer string `json:"explainer,omitempty"`
}

// liveQuizQuestionReq matches the domain LiveQuizQuestion JSON shape.
type liveQuizQuestionReq struct {
	QuestionID string              `json:"question_id"`
	Prompt     string              `json:"prompt"`
	Options    []liveQuizOptionReq `json:"options"`
	TimerSecs  int                 `json:"timer_seconds"`
	Points     int                 `json:"points"`
	// L5.2 (ADR-179 ruling 4): ×2 round flag from the quiz-builder toggle.
	DoublePoints bool `json:"double_points"`
	// CR2-C3 atoms-as-questions: when the FE composes a question by linking a
	// LearningAtom, AtomID is its UUID (provenance, no FK) and TopicTags are
	// the atom's tags denormalised so score_awarded can carry them downstream.
	AtomID    string   `json:"atom_id,omitempty"`
	TopicTags []string `json:"topic_tags,omitempty"`
}

// patchLiveQuizReq is the PATCH /api/v1/live-quizzes/{id} body. Both fields
// are required by EditDraft() — the entire title+questions tuple is replaced
// atomically per the domain aggregate.
type patchLiveQuizReq struct {
	Title     string                `json:"title"`
	Questions []liveQuizQuestionReq `json:"questions"`
	// L5: the FE quiz-builder sets the per-quiz overall time-limit + explainer-
	// reveal mode alongside title+questions on every save. Applied via
	// ConfigureAuthoring when ExplainerMode is non-empty (DRAFT-only, ADR-168).
	QuizTimeLimitSecs int    `json:"quiz_time_limit_seconds"`
	ExplainerMode     string `json:"explainer_mode"`
}

// -----------------------------------------------------------------------------
// Dispatchers — registered by handlers.go integration diff
// -----------------------------------------------------------------------------

// liveQuizzesRootHandler dispatches /api/v1/live-quizzes (no path suffix).
//
//	GET  → list
//	POST → create DRAFT (RBAC: instructor / admin / training-admin)
func liveQuizzesRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleLiveQuizList(deps, w, r)
		case http.MethodPost:
			handleLiveQuizCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// liveQuizzesSubHandler dispatches /api/v1/live-quizzes/{id} and
// /api/v1/live-quizzes/{id}/publish.
//
//	GET   /{id}         → fetch one (tenant-scoped 404)
//	PATCH /{id}         → EditDraft (RBAC + 409 when not DRAFT)
//	POST  /{id}/publish → DRAFT → PUBLISHED
func liveQuizzesSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/live-quizzes/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		parts := strings.Split(rest, "/")
		quizID := parts[0]
		if quizID == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}

		// /{id} — single resource (GET fetch + PATCH EditDraft).
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				handleLiveQuizGet(deps, quizID, w, r)
			case http.MethodPatch:
				handleLiveQuizPatch(deps, quizID, w, r)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}

		// /{id}/{action} — only `publish` is wired in M9.
		if len(parts) == 2 {
			action := parts[1]
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			switch action {
			case "publish":
				handleLiveQuizPublish(deps, quizID, w, r)
			default:
				writeError(w, http.StatusNotFound, "unknown action: "+action)
			}
			return
		}

		writeError(w, http.StatusNotFound, "not found")
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleLiveQuizCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.LiveQuizzes == nil {
		writeError(w, http.StatusServiceUnavailable, "live quizzes repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	if !hasLiveQuizAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createLiveQuizReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q, err := classroom.NewLiveQuiz(classroom.NewLiveQuizInput{
		TenantID:       tenantID,
		CourseID:       req.CourseID,
		InstructorGCID: gcid,
		Title:          req.Title,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !persistOK(w, deps.LiveQuizzes.Save(q)) {
		return
	}
	writeJSON(w, http.StatusCreated, liveQuizDTO(q))
}

func handleLiveQuizList(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.LiveQuizzes == nil {
		writeError(w, http.StatusServiceUnavailable, "live quizzes repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	items := deps.LiveQuizzes.ListByTenant(tenantID)
	// CHO-2134: optional `?q=` case-insensitive title substring filter, mirroring
	// the test-sets list semantics (blank ⇒ all rows unchanged; non-blank ⇒
	// substring match). Applied in the handler over the tenant-scoped slice —
	// the per-classroom quiz list is small + unpaginated, so filtering here
	// keeps identical behaviour across the inmem + pg QuizStore adapters and
	// leaves the learner-safe projection below untouched.
	needle := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	includeAnswerKey := hasLiveQuizAdminRole(r)
	out := make([]map[string]interface{}, 0, len(items))
	for _, q := range items {
		if needle != "" && !strings.Contains(strings.ToLower(q.Title), needle) {
			continue
		}
		out = append(out, liveQuizDTOProjected(q, includeAnswerKey))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

func handleLiveQuizGet(deps Deps, quizID string, w http.ResponseWriter, r *http.Request) {
	if deps.LiveQuizzes == nil {
		writeError(w, http.StatusServiceUnavailable, "live quizzes repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	q, ok, err := deps.LiveQuizzes.Get(quizID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "live quiz lookup failed: "+err.Error())
		return
	}
	if !ok || q == nil || q.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "live quiz not found")
		return
	}
	// ADR-179 D3 (leak #1): non-admin roles receive the learner-safe
	// projection — is_correct + explainer are STRIPPED from every option.
	// Instructor/admin/training-admin keep the full authoring DTO.
	writeJSON(w, http.StatusOK, liveQuizDTOProjected(q, hasLiveQuizAdminRole(r)))
}

func handleLiveQuizPatch(deps Deps, quizID string, w http.ResponseWriter, r *http.Request) {
	if deps.LiveQuizzes == nil {
		writeError(w, http.StatusServiceUnavailable, "live quizzes repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	_ = gcid // captured for audit / future event emission
	if !hasLiveQuizAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	q, ok, err := deps.LiveQuizzes.Get(quizID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "live quiz lookup failed: "+err.Error())
		return
	}
	if !ok || q == nil || q.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "live quiz not found")
		return
	}
	var req patchLiveQuizReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	questions := liveQuizQuestionsFromReq(req.Questions)
	if err := q.EditDraft(req.Title, questions); err != nil {
		writeLiveQuizDomainError(w, err)
		return
	}
	// L5: apply the quiz-builder's overall time-limit + explainer-reveal mode
	// when supplied (the FE sends them with every save). Left unchanged when
	// ExplainerMode is absent so title+questions-only PATCHes still succeed.
	if req.ExplainerMode != "" {
		if err := q.ConfigureAuthoring(req.QuizTimeLimitSecs, classroom.ExplainerMode(req.ExplainerMode)); err != nil {
			writeLiveQuizDomainError(w, err)
			return
		}
	}
	if !persistOK(w, deps.LiveQuizzes.Save(q)) {
		return
	}
	writeJSON(w, http.StatusOK, liveQuizDTO(q))
}

func handleLiveQuizPublish(deps Deps, quizID string, w http.ResponseWriter, r *http.Request) {
	if deps.LiveQuizzes == nil {
		writeError(w, http.StatusServiceUnavailable, "live quizzes repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	_ = gcid // captured for audit / future event emission
	if !hasLiveQuizAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	q, ok, err := deps.LiveQuizzes.Get(quizID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "live quiz lookup failed: "+err.Error())
		return
	}
	if !ok || q == nil || q.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "live quiz not found")
		return
	}
	if err := q.Publish(time.Now()); err != nil {
		writeLiveQuizDomainError(w, err)
		return
	}
	if !persistOK(w, deps.LiveQuizzes.Save(q)) {
		return
	}
	writeJSON(w, http.StatusOK, liveQuizDTO(q))
}

// -----------------------------------------------------------------------------
// Mapping + DTO
// -----------------------------------------------------------------------------

// liveQuizQuestionsFromReq narrows the wire request shape into the domain
// LiveQuizQuestion value-object slice. Pure transformation — no I/O.
func liveQuizQuestionsFromReq(in []liveQuizQuestionReq) []classroom.LiveQuizQuestion {
	out := make([]classroom.LiveQuizQuestion, 0, len(in))
	for _, q := range in {
		opts := make([]classroom.LiveQuizOption, 0, len(q.Options))
		for _, opt := range q.Options {
			opts = append(opts, classroom.LiveQuizOption{
				Label:     opt.Label,
				IsCorrect: opt.IsCorrect,
				Explainer: opt.Explainer,
			})
		}
		out = append(out, classroom.LiveQuizQuestion{
			QuestionID:   q.QuestionID,
			Prompt:       q.Prompt,
			Options:      opts,
			TimerSecs:    q.TimerSecs,
			Points:       q.Points,
			DoublePoints: q.DoublePoints,
			AtomID:       q.AtomID,
			TopicTags:    q.TopicTags,
		})
	}
	return out
}

// liveQuizDTO renders a LiveQuiz for the JSON wire with the full answer key —
// the instructor/authoring projection. Mirrors the field map the FE composer +
// presenter consume.
func liveQuizDTO(q *classroom.LiveQuiz) map[string]interface{} {
	return liveQuizDTOProjected(q, true)
}

// liveQuizDTOProjected renders a LiveQuiz for the JSON wire. When
// includeAnswerKey is false the LEARNER-SAFE projection is emitted: every
// option keeps its label but loses is_correct + explainer (ADR-179 D3 leak
// closure #1) — post-reveal correctness reaches learners only via the session
// snapshot's gated `reveal` block.
func liveQuizDTOProjected(q *classroom.LiveQuiz, includeAnswerKey bool) map[string]interface{} {
	out := map[string]interface{}{
		"id":              q.ID,
		"tenant_id":       q.TenantID,
		"course_id":       q.CourseID,
		"instructor_gcid": q.InstructorGCID,
		"title":           q.Title,
		"state":           string(q.State),
		"questions":       liveQuizQuestionsDTO(q.Questions, includeAnswerKey),
		// L5: surface the authoring config so the FE quiz-builder + presenter
		// read back the persisted explainer-reveal mode + overall time-limit.
		"quiz_time_limit_seconds": q.QuizTimeLimitSecs,
		"explainer_mode":          string(q.ExplainerMode),
		"created_at":              q.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":              q.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if q.PublishedAt != nil {
		out["published_at"] = q.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// liveQuizQuestionsDTO renders the question list — always returns a non-nil
// slice so the FE never sees a JSON `null`. includeAnswerKey=false strips
// is_correct + explainer from every option (learner-safe projection).
func liveQuizQuestionsDTO(in []classroom.LiveQuizQuestion, includeAnswerKey bool) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(in))
	for _, q := range in {
		options := make([]map[string]interface{}, 0, len(q.Options))
		for _, opt := range q.Options {
			o := map[string]interface{}{
				"label": opt.Label,
			}
			if includeAnswerKey {
				o["is_correct"] = opt.IsCorrect
				if opt.Explainer != "" {
					o["explainer"] = opt.Explainer
				}
			}
			options = append(options, o)
		}
		question := map[string]interface{}{
			"question_id":   q.QuestionID,
			"prompt":        q.Prompt,
			"options":       options,
			"timer_seconds": q.TimerSecs,
			"points":        q.Points,
			"double_points": q.DoublePoints,
		}
		// CR2-C3 atom-link provenance — rendered only when the question was
		// composed from a LearningAtom (omitted for ad-hoc questions).
		if q.AtomID != "" {
			question["atom_id"] = q.AtomID
		}
		if len(q.TopicTags) > 0 {
			question["topic_tags"] = q.TopicTags
		}
		out = append(out, question)
	}
	return out
}

// -----------------------------------------------------------------------------
// Error mapping — domain sentinel → HTTP status
// -----------------------------------------------------------------------------

// writeLiveQuizDomainError maps a classroom.LiveQuiz domain sentinel to an
// HTTP error envelope:
//   - state-machine guards (ErrLiveQuizNotDraft / ErrLiveQuizCannotPublish) → 409 CONFLICT
//   - validation failures (title / question / no-questions / etc.) → 400 BAD REQUEST
func writeLiveQuizDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, classroom.ErrLiveQuizNotDraft),
		errors.Is(err, classroom.ErrLiveQuizCannotPublish):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// -----------------------------------------------------------------------------
// RBAC helper
// -----------------------------------------------------------------------------

// hasLiveQuizAdminRole reports whether the caller carries one of the roles
// permitted to author or publish a LiveQuiz. Accepts the canonical values
// as well as their schema-enum forms (case-insensitive parse).
//
// Mirrors hasInstructorOrAdmin + hasExamAdminRole — kept local so the R+
// live-quiz track stays self-contained per
// `feedback_parallel_agent_contract_drift`.
func hasLiveQuizAdminRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleInstructor, roleAdmin, roleTrainingAdmin, roleTenantAdmin)
}

// Keep the inmem import referenced even if a future refactor inlines the
// deps reference. The actual reference lives via deps.LiveQuizzes.
var _ = func() *inmem.LiveQuizRepo { return nil }
