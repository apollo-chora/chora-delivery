// offering_handler.go — HTTP handlers for the R+ Offering admin surface
// (four-delivery-mode refactor W1, per ADR-190).
//
// Endpoints (registered in handlers.go):
//
//	GET   /api/v1/offerings                 — list current-tenant offerings
//	POST  /api/v1/offerings                 — create DRAFT (training-admin/instructor/admin)
//	GET   /api/v1/offerings/{id}            — fetch one (tenant-scoped 404)
//	PATCH /api/v1/offerings/{id}/launch     — DRAFT    → LAUNCHED  (W2.B)
//	PATCH /api/v1/offerings/{id}/start      — LAUNCHED → RUNNING   (W2.B)
//	PATCH /api/v1/offerings/{id}/conclude   — RUNNING  → CONCLUDED (W2.B)
//	PATCH /api/v1/offerings/{id}/archive    — any      → ARCHIVED  (W2.B, soft-delete)
//
// Authorisation (verified in-handler, like exam_handler.go):
//   - tenantRequired enforces X-Tenant-Id presence (400 otherwise).
//   - gcid header required on writes (401 if missing).
//   - POST + PATCH transitions: caller carries instructor / admin /
//     training-admin (403 otherwise).
//   - GET: any in-tenant caller (tenant-scoped read).
//
// The offering carries delivery_type {graduate|short|async}; the FSM
// transitions (Launch/Start/Conclude/Archive) live on the domain aggregate
// (offering.go) and surface here as PATCH sub-routes. An illegal transition for
// the current state returns 409 (the domain sentinel message).
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Request DTO
// -----------------------------------------------------------------------------

type createOfferingReq struct {
	// CourseIDs is the offering→course list (1:N). CourseID is the legacy
	// single-course field, accepted as a fallback for older callers.
	CourseIDs    []string `json:"course_ids"`
	CourseID     string   `json:"course_id"`
	DeliveryType string   `json:"delivery_type"`
	Label        string   `json:"label"`
	Capacity     int      `json:"capacity"`
}

// courseIDs resolves the create request's courses: prefer the 1:N course_ids,
// falling back to the legacy single course_id when course_ids is omitted.
func (req createOfferingReq) courseIDs() []string {
	if len(req.CourseIDs) > 0 {
		return req.CourseIDs
	}
	if req.CourseID != "" {
		return []string{req.CourseID}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Dispatchers
// -----------------------------------------------------------------------------

// offeringsRootHandler dispatches /api/v1/offerings (no path suffix).
func offeringsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleOfferingList(deps, w, r)
		case http.MethodPost:
			handleOfferingCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// offeringsSubHandler dispatches /api/v1/offerings/{id} (GET) and the FSM
// transition sub-routes /api/v1/offerings/{id}/{launch|start|conclude|archive}
// (PATCH, W2.B). Deeper or blank-segment paths are 404.
func offeringsSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/offerings/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		for _, p := range parts {
			if p == "" { // "", "/launch", "id//launch" → not a valid leaf
				writeError(w, http.StatusNotFound, "not found")
				return
			}
		}
		switch len(parts) {
		case 1: // /api/v1/offerings/{id}
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleOfferingGet(deps, parts[0], w, r)
		case 2: // /api/v1/offerings/{id}/{action|assessments}
			// W3.A — offering-nested assessments surface (making an Offering's
			// assessments addressable). GET lists, POST attaches a PUBLISHED
			// test-set. Detach reuses POST /api/v1/assessments/{id}/archive.
			if parts[1] == "assessments" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingListAssessments(deps, parts[0], w, r)
				case http.MethodPost:
					handleOfferingCreateAssessment(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// W7/D2 — offering-nested Sections surface (intra-cohort sub-groups of
			// a graduate offering). GET lists, POST creates one section (the child
			// rides in the Offering JSONB aggregate; no own table). No DELETE.
			if parts[1] == "sections" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingListSections(deps, parts[0], w, r)
				case http.MethodPost:
					handleOfferingCreateSection(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// R+ Phase-2 S1 — offering-nested Curriculum surface. GET reads the
			// attached-course outline; POST authors it (adds one content item to an
			// attached course — a VALIDATED PROXY to the CourseContent service, so
			// the Course still owns its content). reorder/remove are POST sub-paths
			// (len==3, below). POST-only ⇒ edge-safe (offerings glob covers it).
			if parts[1] == "curriculum" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingGetCurriculum(deps, parts[0], w, r)
				case http.MethodPost:
					handleOfferingAddCurriculumItem(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// R+ Phase-2 W7 (WS-A) — offering-nested Module structure surface. GET
			// lists an attached course's modules (?course_id=); POST creates one (a
			// VALIDATED PROXY to the Module aggregate, course_id ∈ offering.CourseIDs).
			// item/requirement ops are POST sub-paths (len==3, below). GET+POST-only.
			if parts[1] == "modules" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingListModules(deps, parts[0], w, r)
				case http.MethodPost:
					handleOfferingCreateModule(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// R+ Phase-2 (ADR-226) — offering-nested Prerequisite DAG surface. GET
			// reads the attached courses' cycle-checked course→course edges + notes;
			// POST adds one edge (VALIDATED PROXY to the CoursePrerequisiteService).
			// remove is a POST sub-path (len==3, below). POST-only ⇒ edge-safe.
			if parts[1] == "prerequisites" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingGetPrerequisites(deps, parts[0], w, r)
				case http.MethodPost:
					handleOfferingAddPrerequisite(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// W2.D — offering-nested Certification surface (read-only cert config
			// of the attached courses). GET only; the panel never mutates config.
			if parts[1] == "certification" {
				// GET reads the (read-only) attached-course cert config PLUS the
				// editable offering-level completion policy (S2) + declared
				// completion requirement (CHO-2222); PATCH sets the policy.
				// Edge-safe (offerings PATCH is Armor-allowed, rule 998).
				switch r.Method {
				case http.MethodGet:
					handleOfferingGetCertification(deps, parts[0], w, r)
				case http.MethodPatch:
					handleOfferingSetCompletionPolicy(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// CHO-2222 (ADR-190 D1) — the offering's DECLARED completion
			// components. PATCH-only: it is READ via GET /certification above,
			// alongside the policy it composes with, so the Certification tab
			// loads in one round-trip. Same prefix as every other offerings
			// sub-route, so the gateway subtree + Istio /api/v1/offerings/* glob
			// already cover it (as they do the sibling PATCH /certification).
			if parts[1] == "completion-requirement" {
				switch r.Method {
				case http.MethodPatch:
					handleOfferingSetCompletionRequirement(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// R+ offering-nested Roster surface (read-only per-course learner
			// roster, aggregated across the offering's attached courses;
			// object-derived for graduate + short). GET only.
			if parts[1] == "roster" {
				// GET reads the per-course roster; POST admin-enrols a learner into
				// an attached course (S3, validated proxy to the EnrollmentPort).
				switch r.Method {
				case http.MethodGet:
					handleOfferingGetRoster(deps, parts[0], w, r)
				case http.MethodPost:
					handleOfferingEnroll(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// R+ offering-nested Analytics surface (read-only delivery-local
			// enrolment/capacity/lifecycle/assessment roll-up; object-derived
			// for async self-paced offerings). GET only.
			if parts[1] == "analytics" {
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleOfferingGetAnalytics(deps, parts[0], w, r)
				return
			}
			// R+ offering-nested Schedule & Rooms surface (scheduled delivery
			// sessions; object-derived for short offerings). GET lists, POST
			// creates one session. No DELETE (soft-delete = future POST /cancel).
			if parts[1] == "schedule" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingListSchedule(deps, parts[0], w, r)
				case http.MethodPost:
					handleOfferingCreateSession(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// R+ offering-nested Attendance surface (session-scoped presence
			// marks; object-derived for short offerings). POST records/corrects
			// one (session id in the JSON body). No DELETE.
			//
			// CHO-2186 — the READ lives one segment deeper (case 3:
			// /attendance/{sid}). The session id MUST NOT travel as a query
			// param: OWASP CRS 943110 (session fixation) denies any request
			// carrying a query/body ARG named session_id when the Referer is
			// off-domain, and this platform's SPA→API calls are ALWAYS
			// cross-subdomain (rplus.chora.site → api.chora.site). A GET here
			// with no {sid} keeps the pre-existing 400.
			if parts[1] == "attendance" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingListAttendance(deps, parts[0], "", w, r)
				case http.MethodPost:
					handleOfferingMarkAttendance(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			// R+ offering-nested Publish / Catalog-handoff surface (object-
			// derived for async self-paced offerings). GET reports each attached
			// course's catalogue visibility; PATCH publishes the listed ones
			// (edge-safe — Armor rule 998 covers offerings PATCH).
			if parts[1] == "publish" {
				switch r.Method {
				case http.MethodGet:
					handleOfferingGetPublish(deps, parts[0], w, r)
				case http.MethodPatch:
					handleOfferingPublish(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				}
				return
			}
			if r.Method != http.MethodPatch {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleOfferingTransition(deps, parts[0], parts[1], w, r)
		case 3:
			// R+ Phase-2 S1 — Curriculum authoring sub-paths (POST-only, edge-safe):
			//   POST /api/v1/offerings/{id}/curriculum/{reorder|remove}
			if parts[1] == "curriculum" && (parts[2] == "reorder" || parts[2] == "remove") {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				if parts[2] == "reorder" {
					handleOfferingReorderCurriculum(deps, parts[0], w, r)
				} else {
					handleOfferingRemoveCurriculumItem(deps, parts[0], w, r)
				}
				return
			}
			// R+ Phase-2 W7 (WS-A) — Module structure ops (POST-only, edge-safe):
			//   POST /api/v1/offerings/{id}/modules/{add-item|remove-item|reorder-items|set-requirement|remove}
			if parts[1] == "modules" {
				// W7 StudentModuleProgress read (CHO-2074, GET-only): a learner sees
				// own per-module completion, an instructor the cohort roll-up.
				//   GET /api/v1/offerings/{id}/modules/progress?course_id=X[&gcid=Y]
				if parts[2] == "progress" {
					if r.Method != http.MethodGet {
						writeError(w, http.StatusMethodNotAllowed, "method not allowed")
						return
					}
					handleOfferingModuleProgress(deps, parts[0], w, r)
					return
				}
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				switch parts[2] {
				case "add-item":
					handleOfferingAddModuleItem(deps, parts[0], w, r)
				case "remove-item":
					handleOfferingRemoveModuleItem(deps, parts[0], w, r)
				case "reorder-items":
					handleOfferingReorderModuleItems(deps, parts[0], w, r)
				case "set-requirement":
					handleOfferingSetModuleRequirement(deps, parts[0], w, r)
				case "remove":
					handleOfferingRemoveModule(deps, parts[0], w, r)
				default:
					writeError(w, http.StatusNotFound, "not found")
				}
				return
			}
			// R+ Phase-2 (ADR-226) — Prerequisite edge remove (POST-only, edge-safe):
			//   POST /api/v1/offerings/{id}/prerequisites/remove
			if parts[1] == "prerequisites" && parts[2] == "remove" {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleOfferingRemovePrerequisite(deps, parts[0], w, r)
				return
			}
			// R+ Phase-2 WS-B — Roster unenrol (POST-only, edge-safe: the offerings
			// glob already covers /roster POST enrol; this is its cancel sibling):
			//   POST /api/v1/offerings/{id}/roster/remove
			if parts[1] == "roster" && parts[2] == "remove" {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleOfferingRosterRemove(deps, parts[0], w, r)
				return
			}
			// R+ ATOMIC roster bulk-enrol (POST-only, edge-safe: the offerings glob
			// already covers /roster POST enrol; this is its all-or-nothing batch
			// sibling):
			//   POST /api/v1/offerings/{id}/roster/bulk
			if parts[1] == "roster" && parts[2] == "bulk" {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleOfferingBulkEnroll(deps, parts[0], w, r)
				return
			}
			// R+ per-offering MANUAL certificate issuance (POST-only, edge-safe:
			// the offerings glob already covers the sibling GET/PATCH
			// /certification). Distinct from that read-only config surface — this
			// mints a real credential + emits certification.issued.v1:
			//   POST /api/v1/offerings/{id}/certification/issue
			if parts[1] == "certification" && parts[2] == "issue" {
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleOfferingIssueCertification(deps, parts[0], w, r)
				return
			}
			// CHO-2186 — Attendance READ, session id as a PATH segment:
			//   GET /api/v1/offerings/{id}/attendance/{sid}
			// The query-param form (?session_id=) is edge-denied 100% of the time
			// by Cloud Armor rule 1008 → CRS 943110, so the read had NEVER worked
			// in production while the POST (body-carried) wrote rows happily —
			// attendance was durably written and permanently unreadable. A path
			// segment is invisible to the ARGS-scanning rule. Renaming the param
			// would NOT help: CRS matches the substring session_id.
			if parts[1] == "attendance" {
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleOfferingListAttendance(deps, parts[0], parts[2], w, r)
				return
			}
			// R+ Phase-2 S4 — Section detail edit (edge-safe: offerings PATCH is
			// Armor-allowed): PATCH /api/v1/offerings/{id}/sections/{sectionId}
			if parts[1] == "sections" {
				if r.Method != http.MethodPatch {
					writeError(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				handleOfferingUpdateSection(deps, parts[0], parts[2], w, r)
				return
			}
			writeError(w, http.StatusNotFound, "not found")
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleOfferingCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid // captured for future audit / event emission
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createOfferingReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    req.courseIDs(),
		DeliveryType: delivery.DeliveryType(req.DeliveryType),
		Label:        req.Label,
		Capacity:     req.Capacity,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	if err := deps.Offerings.Save(tracing.WithTenantID(r.Context(), tenantID), o); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, offeringDTO(o))
}

func handleOfferingList(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	items, err := deps.Offerings.ListByTenant(tracing.WithTenantID(r.Context(), tenantID), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, o := range items {
		out = append(out, offeringDTO(o))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
}

func handleOfferingGet(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	o, ok, err := deps.Offerings.Get(tracing.WithTenantID(r.Context(), tenantID), offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	writeJSON(w, http.StatusOK, offeringDTO(o))
}

// handleOfferingTransition applies an FSM transition to an offering:
//
//	launch   DRAFT    → LAUNCHED
//	start    LAUNCHED → RUNNING
//	conclude RUNNING  → CONCLUDED
//	archive  any      → ARCHIVED (idempotent in the domain; soft-deletes)
//
// Admin-gated write. 200 + offeringDTO on success; 409 on an illegal transition
// for the current state (domain sentinel); 404 for an unknown / cross-tenant /
// soft-deleted offering or an unknown action.
func handleOfferingTransition(deps Deps, offeringID, action string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r) // enforces tenant (400) + gcid (401)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	switch action {
	case "launch", "start", "conclude", "archive":
	default:
		writeError(w, http.StatusNotFound, "unknown transition")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	// S5 — launch readiness gate (GRADUATE only). A graduate offering must have
	// at least one curriculum content item across its attached courses before it
	// can launch (you build the curriculum via the Curriculum tab, S1) — this
	// closes the "an empty offering launches freely" gap. Short / async
	// offerings have no Curriculum tab, so the gate does not apply. It is skipped
	// when the CourseContent service is unwired (unit harnesses) so it never
	// blocks a launch on a readiness-check outage. 422 = "understood but not
	// ready" (distinct from 409 = "wrong FSM state").
	if action == "launch" && o.DeliveryType == delivery.DeliveryTypeGraduate &&
		deps.CourseCJ2 != nil && deps.CourseCJ2.Content != nil && deps.CourseCJ2.Content.Svc != nil {
		hasContent := false
		for _, cid := range o.CourseIDs {
			content, cerr := deps.CourseCJ2.Content.Svc.Get(ctx, tenantID, cid)
			if cerr == nil && content != nil && len(content.Items) > 0 {
				hasContent = true
				break
			}
		}
		if !hasContent {
			writeError(w, http.StatusUnprocessableEntity,
				"offering has no curriculum content — add content to a course before launching")
			return
		}
	}
	switch action {
	case "launch":
		err = o.Launch()
	case "start":
		err = o.Start()
	case "conclude":
		err = o.Conclude()
	case "archive":
		o.Archive() // any → ARCHIVED, no error (idempotent in the domain)
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := deps.Offerings.Save(ctx, o); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offeringDTO(o))
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

// offeringDTO renders an Offering for the JSON wire (snake_case keys).
func offeringDTO(o *delivery.Offering) map[string]interface{} {
	out := map[string]interface{}{
		"id":        o.ID,
		"tenant_id": o.TenantID,
		// course_id = the PRIMARY (first) course, kept for back-compat; course_ids
		// is the full 1:N list (offering→course).
		"course_id":     o.PrimaryCourseID(),
		"course_ids":    o.CourseIDs,
		"delivery_type": string(o.DeliveryType),
		"label":         o.Label,
		"capacity":      o.Capacity,
		"state":         string(o.State),
		"created_at":    o.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":    o.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if o.LaunchedAt != nil {
		out["launched_at"] = o.LaunchedAt.UTC().Format(time.RFC3339Nano)
	}
	if o.ConcludedAt != nil {
		out["concluded_at"] = o.ConcludedAt.UTC().Format(time.RFC3339Nano)
	}
	if o.ArchivedAt != nil {
		out["archived_at"] = o.ArchivedAt.UTC().Format(time.RFC3339Nano)
	}
	if o.DeletedAt != nil {
		out["deleted_at"] = o.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// -----------------------------------------------------------------------------
// RBAC helper
// -----------------------------------------------------------------------------

// hasOfferingAdminRole reports whether the caller may create offerings.
// Mirrors hasExamAdminRole — instructor / admin / training-admin / tenant_admin.
func hasOfferingAdminRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleInstructor, roleAdmin, roleTrainingAdmin, roleTenantAdmin)
}

// -----------------------------------------------------------------------------
// Universal-finder search (W2.A) — GET /api/v1/search/offerings
// -----------------------------------------------------------------------------

// searchOfferingsHandler dispatches the offerings finder search (GET only).
// Read surface: any in-tenant caller (tenant-scoped), like the list endpoint.
func searchOfferingsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleOfferingSearch(deps, w, r)
	}
}

func handleOfferingSearch(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	qv := r.URL.Query()
	sortField, sortDir := parseOfferingSort(qv.Get("sort"))
	limit := 0
	if l := strings.TrimSpace(qv.Get("limit")); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	query := delivery.OfferingQuery{
		TenantID:      tenantID,
		Q:             qv.Get("q"),
		CourseID:      strings.TrimSpace(qv.Get("course_id")),
		DeliveryTypes: offeringFilterValues(qv, "filter[delivery_type]"),
		States:        offeringFilterValues(qv, "filter[state]"),
		SortField:     sortField,
		SortDir:       sortDir,
		Cursor:        decodeOfferingCursor(qv.Get("cursor")),
		Limit:         limit,
	}
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	page, err := deps.Offerings.Search(tracing.WithTenantID(r.Context(), tenantID), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offeringSearchDTO(page))
}

// parseOfferingSort splits a "field:dir" sort param. Empties pass through to
// OfferingQuery.Normalize, which applies the created_at:desc default.
func parseOfferingSort(raw string) (field, dir string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		return strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:])
	}
	return raw, ""
}

// offeringFilterValues reads a repeatable filter[<facet>] param, also splitting
// comma-separated values within each occurrence. Unknown tokens are dropped by
// OfferingQuery.Normalize.
func offeringFilterValues(qv url.Values, key string) []string {
	var out []string
	for _, raw := range qv[key] {
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// offeringCursorWire is the opaque (base64url) JSON cursor on the wire.
type offeringCursorWire struct {
	S string `json:"s"` // sort value (RFC3339Nano for timestamps, raw for label)
	I string `json:"i"` // id tiebreak
}

func encodeOfferingCursor(c *delivery.OfferingCursor) string {
	if c == nil {
		return ""
	}
	b, _ := json.Marshal(offeringCursorWire{S: c.SortValue, I: c.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeOfferingCursor(raw string) *delivery.OfferingCursor {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	var wire offeringCursorWire
	if err := json.Unmarshal(b, &wire); err != nil || wire.I == "" {
		return nil
	}
	return &delivery.OfferingCursor{SortValue: wire.S, ID: wire.I}
}

// offeringSearchDTO renders an OfferingSearchPage for the finder wire contract
// (docs/design/ux_universal_search_collection.md §8): items + facets +
// next_cursor + total_estimate.
func offeringSearchDTO(p *delivery.OfferingSearchPage) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(p.Items))
	for _, o := range p.Items {
		items = append(items, offeringDTO(o))
	}
	facets := make([]map[string]interface{}, 0, len(p.Facets))
	for _, f := range p.Facets {
		vals := make([]map[string]interface{}, 0, len(f.Values))
		for _, v := range f.Values {
			vals = append(vals, map[string]interface{}{
				"value": v.Value,
				"label": v.Value, // FE i18n-maps the display label
				"count": v.Count,
			})
		}
		facets = append(facets, map[string]interface{}{"field": f.Field, "values": vals})
	}
	out := map[string]interface{}{
		"items":          items,
		"facets":         facets,
		"total_estimate": p.TotalEstimate,
		"next_cursor":    nil,
	}
	if p.NextCursor != nil {
		out["next_cursor"] = encodeOfferingCursor(p.NextCursor)
	}
	return out
}
