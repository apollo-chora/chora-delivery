// wbl_handler.go — HTTP handlers for the M15c Work-Based Learning
// placement surface.
//
// 5 endpoints registered in handlers.go:
//
//	POST   /api/v1/wbl-placements                — create SCHEDULED placement
//	GET    /api/v1/wbl-placements?state=...      — list (default excludes WITHDRAWN)
//	GET    /api/v1/wbl-placements/{id}           — fetch one
//	PATCH  /api/v1/wbl-placements/{id}           — update hours_completed / evaluator_notes
//	DELETE /api/v1/wbl-placements/{id}           — soft-delete (state=WITHDRAWN)
//
// Authorisation (verified inside the handler, NOT at middleware):
//   - tenantRequired (X-Tenant-Id) — handled via callerTenantGCID
//   - gcid header required for caller identity
//   - all writes (POST/PATCH/DELETE) require instructor / admin / training-admin
//     role via hasInstructorOrAdmin
//   - read is gated to tenant scope (no cross-tenant leak)
//
// Per ADR-164-aligned tenancy isolation — tenant scoping is enforced at
// the repo + handler. Future pg adapter will additionally rely on RLS
// (composed with database-level isolation per multi-tenant-rls).
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	wbl "github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

// -----------------------------------------------------------------------------
// Request types
// -----------------------------------------------------------------------------

type createWblPlacementReq struct {
	GCID            string    `json:"gcid"`
	CourseID        string    `json:"course_id"`
	HostOrgName     string    `json:"host_org_name"`
	SupervisorName  string    `json:"supervisor_name"`
	SupervisorEmail string    `json:"supervisor_email"`
	StartDate       time.Time `json:"start_date"`
	EndDate         time.Time `json:"end_date"`
	HoursRequired   int       `json:"hours_required"`
}

type patchWblPlacementReq struct {
	HoursCompleted *int    `json:"hours_completed,omitempty"`
	EvaluatorNotes *string `json:"evaluator_notes,omitempty"`
}

// -----------------------------------------------------------------------------
// /api/v1/wbl-placements root dispatcher (POST create + GET list)
// -----------------------------------------------------------------------------

// wblRootHandler dispatches /api/v1/wbl-placements (no trailing id).
//
// repo is the wbl.WblStore port (in-memory dev adapter or pg.WblRepo at
// cmd/server wiring) — ctx-threaded so the pg adapter applies RLS.
func wblRootHandler(repo wbl.WblStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleWblCreate(repo, w, r)
		case http.MethodGet:
			handleWblList(repo, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// wblSubHandler dispatches /api/v1/wbl-placements/{id}.
func wblSubHandler(repo wbl.WblStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/wbl-placements/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" || strings.Contains(rest, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		placementID := rest
		switch r.Method {
		case http.MethodGet:
			handleWblGet(repo, placementID, w, r)
		case http.MethodPatch:
			handleWblPatch(repo, placementID, w, r)
		case http.MethodDelete:
			handleWblDelete(repo, placementID, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleWblCreate(repo wbl.WblStore, w http.ResponseWriter, r *http.Request) {
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin role")
		return
	}
	var req createWblPlacementReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := wbl.NewPlacement(wbl.NewPlacementInput{
		TenantID:        tenantID,
		GCID:            req.GCID,
		CourseID:        req.CourseID,
		HostOrgName:     req.HostOrgName,
		SupervisorName:  req.SupervisorName,
		SupervisorEmail: req.SupervisorEmail,
		StartDate:       req.StartDate,
		EndDate:         req.EndDate,
		HoursRequired:   req.HoursRequired,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	if err := repo.Save(tracing.WithTenantID(r.Context(), tenantID), p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, wblPlacementDTO(p))
}

func handleWblList(repo wbl.WblStore, w http.ResponseWriter, r *http.Request) {
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	var state wbl.PlacementState
	if s := strings.TrimSpace(r.URL.Query().Get("state")); s != "" {
		state = wbl.PlacementState(s)
		if !state.IsValid() {
			writeError(w, http.StatusBadRequest, "invalid state: "+s)
			return
		}
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	items, err := repo.ListByTenant(ctx, tenantID, state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Resolve display metadata (learner_name + course_title) when the injected
	// store carries the enrichment capability; a store without it degrades to
	// the raw-id fallback (feedback_no_stubs_real_wiring). CHO-2335.
	names, titles, err := resolveWblDisplay(ctx, repo, tenantID, items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, p := range items {
		out = append(out, wblPlacementListItemDTO(p, names, titles))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

// resolveWblDisplay batch-resolves the learner display names + course titles
// for the list rows. It returns empty maps (no error) when the store does not
// implement wbl.PlacementEnricher; the caller then falls back to the raw ids.
// Distinct gcids / course ids only, so the cost is one directory lookup + one
// deduped course pass regardless of row count.
func resolveWblDisplay(ctx context.Context, repo wbl.WblStore, tenantID string, items []*wbl.Placement) (names, titles map[string]string, err error) {
	enricher, ok := repo.(wbl.PlacementEnricher)
	if !ok || len(items) == 0 {
		return map[string]string{}, map[string]string{}, nil
	}
	gcidSet := make(map[string]struct{}, len(items))
	courseSet := make(map[string]struct{}, len(items))
	gcids := make([]string, 0, len(items))
	courseIDs := make([]string, 0, len(items))
	for _, p := range items {
		if p.GCID != "" {
			if _, seen := gcidSet[p.GCID]; !seen {
				gcidSet[p.GCID] = struct{}{}
				gcids = append(gcids, p.GCID)
			}
		}
		if p.CourseID != "" {
			if _, seen := courseSet[p.CourseID]; !seen {
				courseSet[p.CourseID] = struct{}{}
				courseIDs = append(courseIDs, p.CourseID)
			}
		}
	}
	if names, err = enricher.LearnerNames(ctx, gcids); err != nil {
		return nil, nil, err
	}
	if titles, err = enricher.CourseTitles(ctx, tenantID, courseIDs); err != nil {
		return nil, nil, err
	}
	return names, titles, nil
}

func handleWblGet(repo wbl.WblStore, placementID string, w http.ResponseWriter, r *http.Request) {
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	p, ok, err := repo.Get(tracing.WithTenantID(r.Context(), tenantID), placementID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "placement lookup failed: "+err.Error())
		return
	}
	if !ok || p.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "placement not found")
		return
	}
	writeJSON(w, http.StatusOK, wblPlacementDTO(p))
}

func handleWblPatch(repo wbl.WblStore, placementID string, w http.ResponseWriter, r *http.Request) {
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	p, ok, err := repo.Get(ctx, placementID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "placement lookup failed: "+err.Error())
		return
	}
	if !ok || p.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "placement not found")
		return
	}
	var req patchWblPlacementReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.HoursCompleted != nil {
		if err := p.RecordHours(*req.HoursCompleted); err != nil {
			writeWblDomainError(w, err)
			return
		}
	}
	if req.EvaluatorNotes != nil {
		if err := p.SetEvaluatorNotes(*req.EvaluatorNotes); err != nil {
			writeWblDomainError(w, err)
			return
		}
	}
	if err := repo.Save(ctx, p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wblPlacementDTO(p))
}

func handleWblDelete(repo wbl.WblStore, placementID string, w http.ResponseWriter, r *http.Request) {
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	p, ok, err := repo.Get(ctx, placementID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "placement lookup failed: "+err.Error())
		return
	}
	if !ok || p.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "placement not found")
		return
	}
	if err := p.Withdraw("DELETE /api/v1/wbl-placements"); err != nil {
		writeWblDomainError(w, err)
		return
	}
	if err := repo.Save(ctx, p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wblPlacementDTO(p))
}

// -----------------------------------------------------------------------------
// Error mapping — domain sentinel → HTTP status
// -----------------------------------------------------------------------------

// writeWblDomainError maps a wbl domain sentinel to an HTTP error envelope.
// Closed-state and overflow guards return 409 (CONFLICT); the rest are 400.
func writeWblDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wbl.ErrPlacementClosed),
		errors.Is(err, wbl.ErrNotScheduled),
		errors.Is(err, wbl.ErrNotInProgress),
		errors.Is(err, wbl.ErrHoursExceedsRequired):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

func wblPlacementDTO(p *wbl.Placement) map[string]interface{} {
	out := map[string]interface{}{
		"id":               p.ID,
		"tenant_id":        p.TenantID,
		"gcid":             p.GCID,
		"course_id":        p.CourseID,
		"host_org_name":    p.HostOrgName,
		"supervisor_name":  p.SupervisorName,
		"supervisor_email": p.SupervisorEmail,
		"start_date":       p.StartDate.Format(time.RFC3339),
		"end_date":         p.EndDate.Format(time.RFC3339),
		"hours_required":   p.HoursRequired,
		"hours_completed":  p.HoursCompleted,
		"state":            string(p.State),
		"created_at":       p.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":       p.UpdatedAt.Format(time.RFC3339Nano),
	}
	if p.EvaluatorNotes != "" {
		out["evaluator_notes"] = p.EvaluatorNotes
	}
	return out
}

// wblPlacementListItemDTO is wblPlacementDTO plus the resolved display fields
// (learner_name + course_title) for the list surface (CHO-2335). Both keys are
// OMITTED when unresolved (empty name / title, or the id was not in the lookup
// map) so the FE falls back to the raw gcid / course_id. The raw gcid +
// course_id always ride the envelope (backward-compat). names / titles may be
// nil (a nil-map index is a safe empty string).
func wblPlacementListItemDTO(p *wbl.Placement, names, titles map[string]string) map[string]interface{} {
	out := wblPlacementDTO(p)
	if name := strings.TrimSpace(names[p.GCID]); name != "" {
		out["learner_name"] = name
	}
	if title := strings.TrimSpace(titles[p.CourseID]); title != "" {
		out["course_title"] = title
	}
	return out
}
