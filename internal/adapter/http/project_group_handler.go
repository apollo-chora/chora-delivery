// project_group_handler.go — HTTP handlers for the M15b R+ ProjectGroup
// surface (Stage C-lite wave-2b).
//
// 7 endpoints registered in handlers.go integration diff:
//
//	POST   /api/v1/project-groups                     — create FORMING
//	GET    /api/v1/project-groups?course_id=          — list (by course or tenant)
//	GET    /api/v1/project-groups/{id}                — single fetch
//	POST   /api/v1/project-groups/{id}/submit         — ACTIVE → SUBMITTED
//	POST   /api/v1/project-groups/{id}/grade          — SUBMITTED → GRADED
//	POST   /api/v1/project-groups/{id}/members        — add member (FORMING/ACTIVE)
//	DELETE /api/v1/project-groups/{id}/members/{gcid} — remove member (FORMING/ACTIVE)
//
// Authorisation (verified inside the handler, NOT at middleware):
//   - tenantRequired (X-Tenant-Id) — handled via callerTenantGCID
//   - gcid header required for caller identity
//   - POST create + POST grade require instructor / admin / training-admin
//     role via hasInstructorOrAdmin (mirrors exam_handler.go / wbl_handler.go)
//   - POST submit allows any in-tenant caller (learners trigger submit on
//     their own group) — RBAC limited to gcid presence (401 otherwise).
//   - GET list / GET by-id: any in-tenant caller (tenant-scoped read).
//
// Add-only: the existing Deps struct gains one new field
// `ProjectGroups *inmem.ProjectGroupRepo` via integration diff; nothing
// else in this file touches handlers.go / cmd/server. See INTEGRATION
// MANIFEST in the PR body for the canonical wiring.
//
// Per .claude/rules/ddd-enforcement.md cross-DB queries FORBIDDEN —
// course_id is a cross-aggregate UUID reference without FK; validation
// (e.g., does the course exist?) is intentionally NOT performed here. A
// Pub/Sub-validated upstream guard lands when the production Postgres
// adapter ships.
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	pgdomain "github.com/apollo-chora/chora-delivery/internal/domain/project_group"
)

// -----------------------------------------------------------------------------
// Request DTOs
// -----------------------------------------------------------------------------

type pgMemberReq struct {
	GCID string `json:"gcid"`
	Role string `json:"role"`
}

type createProjectGroupReq struct {
	CourseID string        `json:"course_id"`
	Name     string        `json:"name"`
	Members  []pgMemberReq `json:"members"`
}

type gradeProjectGroupReq struct {
	ScorePct float64 `json:"score_pct"`
	Feedback string  `json:"feedback"`
}

// -----------------------------------------------------------------------------
// Dispatchers — registered by handlers.go integration diff
// -----------------------------------------------------------------------------

// projectGroupsRootHandler dispatches /api/v1/project-groups (no path
// suffix).
//
//	GET  → list (filter by ?course_id= or default tenant scope)
//	POST → create FORMING (RBAC: instructor / admin / training-admin)
func projectGroupsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleProjectGroupList(deps, w, r)
		case http.MethodPost:
			handleProjectGroupCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// projectGroupsSubHandler dispatches /api/v1/project-groups/{id} and
// /api/v1/project-groups/{id}/{action}.
//
//	GET    /api/v1/project-groups/{id}                → fetch one (tenant-scoped 404)
//	POST   /api/v1/project-groups/{id}/submit         → ACTIVE → SUBMITTED
//	POST   /api/v1/project-groups/{id}/grade          → SUBMITTED → GRADED
//	POST   /api/v1/project-groups/{id}/members        → add member (FORMING/ACTIVE)
//	DELETE /api/v1/project-groups/{id}/members/{gcid} → remove member (FORMING/ACTIVE)
func projectGroupsSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/project-groups/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		parts := strings.Split(rest, "/")
		switch len(parts) {
		case 1:
			groupID := parts[0]
			if groupID == "" {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleProjectGroupGet(deps, groupID, w, r)
		case 2:
			groupID := parts[0]
			action := parts[1]
			if groupID == "" || action == "" {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			switch action {
			case "submit":
				handleProjectGroupSubmit(deps, groupID, w, r)
			case "grade":
				handleProjectGroupGrade(deps, groupID, w, r)
			case "members":
				handleProjectGroupAddMember(deps, groupID, w, r)
			default:
				writeError(w, http.StatusNotFound, "not found")
			}
		case 3:
			// /{id}/members/{gcid} — DELETE removes a member.
			groupID := parts[0]
			if groupID == "" || parts[1] != "members" || parts[2] == "" {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			if r.Method != http.MethodDelete {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleProjectGroupRemoveMember(deps, groupID, parts[2], w, r)
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleProjectGroupCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.ProjectGroups == nil {
		writeError(w, http.StatusServiceUnavailable, "project groups repo not wired")
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
	var req createProjectGroupReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	members := make([]pgdomain.Member, 0, len(req.Members))
	for _, m := range req.Members {
		members = append(members, pgdomain.Member{
			GCID: m.GCID,
			Role: pgdomain.MemberRole(m.Role),
		})
	}
	g, err := pgdomain.NewProjectGroup(pgdomain.NewProjectGroupInput{
		TenantID: tenantID,
		CourseID: req.CourseID,
		Name:     req.Name,
		Members:  members,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	if err := deps.ProjectGroups.Save(tracing.WithTenantID(r.Context(), tenantID), g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, projectGroupDTO(g))
}

func handleProjectGroupList(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.ProjectGroups == nil {
		writeError(w, http.StatusServiceUnavailable, "project groups repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		// tenantRequired middleware enforces this, but defensive.
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	courseID := strings.TrimSpace(r.URL.Query().Get("course_id"))
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	var (
		items []*pgdomain.ProjectGroup
		err   error
	)
	if courseID != "" {
		items, err = deps.ProjectGroups.ListByCourse(ctx, tenantID, courseID)
	} else {
		items, err = deps.ProjectGroups.ListByTenant(ctx, tenantID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, g := range items {
		out = append(out, projectGroupDTO(g))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

func handleProjectGroupGet(deps Deps, groupID string, w http.ResponseWriter, r *http.Request) {
	if deps.ProjectGroups == nil {
		writeError(w, http.StatusServiceUnavailable, "project groups repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	g, ok, err := deps.ProjectGroups.Get(tracing.WithTenantID(r.Context(), tenantID), groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "project group lookup failed: "+err.Error())
		return
	}
	if !ok || g == nil || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "project group not found")
		return
	}
	writeJSON(w, http.StatusOK, projectGroupDTO(g))
}

// handleProjectGroupSubmit serves POST /api/v1/project-groups/{id}/submit.
//
// Allows any in-tenant caller with a gcid (learners trigger submit on
// their own group). The FSM guard inside the aggregate (ErrNotActive)
// rejects non-ACTIVE groups with 409.
func handleProjectGroupSubmit(deps Deps, groupID string, w http.ResponseWriter, r *http.Request) {
	if deps.ProjectGroups == nil {
		writeError(w, http.StatusServiceUnavailable, "project groups repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid // captured for future audit / event emission
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	g, ok, err := deps.ProjectGroups.Get(ctx, groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "project group lookup failed: "+err.Error())
		return
	}
	if !ok || g == nil || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "project group not found")
		return
	}
	if err := g.Submit(); err != nil {
		writeProjectGroupDomainError(w, err)
		return
	}
	if err := deps.ProjectGroups.Save(ctx, g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectGroupDTO(g))
}

// handleProjectGroupGrade serves POST /api/v1/project-groups/{id}/grade.
//
// Restricted to instructor / admin / training-admin. The grader's gcid
// flows into Grade(GraderGCID=...) for audit trail.
func handleProjectGroupGrade(deps Deps, groupID string, w http.ResponseWriter, r *http.Request) {
	if deps.ProjectGroups == nil {
		writeError(w, http.StatusServiceUnavailable, "project groups repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req gradeProjectGroupReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	g, ok, err := deps.ProjectGroups.Get(ctx, groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "project group lookup failed: "+err.Error())
		return
	}
	if !ok || g == nil || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "project group not found")
		return
	}
	if err := g.Grade(pgdomain.GradeInput{
		ScorePct:   req.ScorePct,
		GraderGCID: gcid,
		Feedback:   req.Feedback,
	}); err != nil {
		writeProjectGroupDomainError(w, err)
		return
	}
	if err := deps.ProjectGroups.Save(ctx, g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectGroupDTO(g))
}

// handleProjectGroupAddMember serves POST /api/v1/project-groups/{id}/members.
//
// Adds a member to a FORMING/ACTIVE group. Restricted to instructor / admin /
// training-admin — roster management is an admin operation. The roster freezes
// once the group is SUBMITTED/GRADED (409 ErrMembersLocked): the grade attaches
// to a fixed roster, so a later change would silently alter who was assessed.
func handleProjectGroupAddMember(deps Deps, groupID string, w http.ResponseWriter, r *http.Request) {
	if deps.ProjectGroups == nil {
		writeError(w, http.StatusServiceUnavailable, "project groups repo not wired")
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
	var req pgMemberReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	g, ok, err := deps.ProjectGroups.Get(ctx, groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "project group lookup failed: "+err.Error())
		return
	}
	if !ok || g == nil || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "project group not found")
		return
	}
	// Roster-lock guard lives in the domain (MembersMutable); the add path
	// consults it here because AddMember is also the constructor's building
	// block (unguarded by design). RemoveMember self-enforces the same rule.
	if !g.MembersMutable() {
		writeProjectGroupDomainError(w, pgdomain.ErrMembersLocked)
		return
	}
	if err := g.AddMember(pgdomain.Member{GCID: req.GCID, Role: pgdomain.MemberRole(req.Role)}); err != nil {
		writeProjectGroupDomainError(w, err)
		return
	}
	if err := deps.ProjectGroups.Save(ctx, g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectGroupDTO(g))
}

// handleProjectGroupRemoveMember serves DELETE
// /api/v1/project-groups/{id}/members/{gcid}. Same RBAC + roster-lock rules as
// add. Removing a gcid that is not a member 404s (ErrMemberNotFound).
func handleProjectGroupRemoveMember(deps Deps, groupID, memberGCID string, w http.ResponseWriter, r *http.Request) {
	if deps.ProjectGroups == nil {
		writeError(w, http.StatusServiceUnavailable, "project groups repo not wired")
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
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	g, ok, err := deps.ProjectGroups.Get(ctx, groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "project group lookup failed: "+err.Error())
		return
	}
	if !ok || g == nil || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "project group not found")
		return
	}
	if err := g.RemoveMember(memberGCID); err != nil {
		writeProjectGroupDomainError(w, err)
		return
	}
	if err := deps.ProjectGroups.Save(ctx, g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectGroupDTO(g))
}

// -----------------------------------------------------------------------------
// Error mapping — domain sentinel → HTTP status
// -----------------------------------------------------------------------------

// writeProjectGroupDomainError maps a project_group domain sentinel to an
// HTTP error envelope. FSM guards return 409 (CONFLICT); validation
// guards return 400.
func writeProjectGroupDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgdomain.ErrNotForming),
		errors.Is(err, pgdomain.ErrNotActive),
		errors.Is(err, pgdomain.ErrNotSubmitted),
		errors.Is(err, pgdomain.ErrMembersLocked),
		errors.Is(err, pgdomain.ErrMemberDuplicate):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, pgdomain.ErrMemberNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, pgdomain.ErrScoreOutOfRange):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

// projectGroupDTO renders a ProjectGroup for the JSON wire. The FE
// adapter (chora-web project-groups.service.ts) maps this shape onto
// the ProjectGroup TS interface.
func projectGroupDTO(g *pgdomain.ProjectGroup) map[string]interface{} {
	members := make([]map[string]interface{}, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, map[string]interface{}{
			"gcid": m.GCID,
			"role": string(m.Role),
		})
	}
	out := map[string]interface{}{
		"id":         g.ID,
		"tenant_id":  g.TenantID,
		"course_id":  g.CourseID,
		"name":       g.Name,
		"members":    members,
		"state":      string(g.State),
		"created_at": g.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": g.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if g.SubmittedAt != nil {
		out["submitted_at"] = g.SubmittedAt.UTC().Format(time.RFC3339Nano)
	}
	if g.GradedAt != nil {
		out["graded_at"] = g.GradedAt.UTC().Format(time.RFC3339Nano)
	}
	if g.State == pgdomain.StateGraded {
		out["score_pct"] = g.ScorePct
	}
	if g.GraderGCID != "" {
		out["grader_gcid"] = g.GraderGCID
	}
	if g.Feedback != "" {
		out["feedback"] = g.Feedback
	}
	if g.DeletedAt != nil {
		out["deleted_at"] = g.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
