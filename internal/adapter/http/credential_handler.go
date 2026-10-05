// credential_handler.go — HTTP handlers for the operator Credential-with-
// competencies catalogue surface (ADR-216 WS-1, D1/D2).
//
// Endpoints (registered in handlers.go):
//
//	POST /api/v1/credentials       — operator create a Credential (admin-gated)
//	GET  /api/v1/credentials       — catalogue list (tenant-scoped read)
//	GET  /api/v1/credentials/{id}  — catalogue read one (tenant-scoped 404)
//
// A Credential (credential domain, chora_delivery.credentials — mig 0038) is an
// operator-curated catalogue entity (e.g. PMP) carrying a structured set of
// Competencies (the shared-vocabulary seed, D2). The catalogue is DECOUPLED from
// courses — a Credential references no course/offering. This is the CATALOGUE
// entity only; it does NOT touch the opaque issued-cert model (certifications).
//
// Authorisation:
//   - Writes (POST): tenantRequired + gcid + operator role (instructor / admin /
//     training-admin / tenant_admin), same gate as offerings. Learner-immutable
//     (D1) is a property of this gate: a learner is 403 and has no write path.
//   - Reads (GET list / get): any in-tenant caller (tenant-scoped read).
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/credential"
)

// -----------------------------------------------------------------------------
// Request DTOs (snake_case wire)
// -----------------------------------------------------------------------------

type createCompetencyReq struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Weight      int    `json:"weight"`
}

type createCredentialReq struct {
	Title        string                `json:"title"`
	Code         string                `json:"code"`
	IssuingBody  string                `json:"issuing_body"`
	Description  string                `json:"description"`
	Competencies []createCompetencyReq `json:"competencies"`
}

func (req createCredentialReq) competencyInputs() []credential.CompetencyInput {
	out := make([]credential.CompetencyInput, 0, len(req.Competencies))
	for _, c := range req.Competencies {
		out = append(out, credential.CompetencyInput{
			Code:        c.Code,
			Name:        c.Name,
			Description: c.Description,
			Weight:      c.Weight,
		})
	}
	return out
}

// -----------------------------------------------------------------------------
// Dispatchers
// -----------------------------------------------------------------------------

// credentialsRootHandler dispatches /api/v1/credentials (no path suffix).
func credentialsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleCredentialList(deps, w, r)
		case http.MethodPost:
			handleCredentialCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// credentialsSubHandler dispatches /api/v1/credentials/{id} (GET only). Deeper
// or blank-segment paths are 404.
func credentialsSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/credentials/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		for _, p := range parts {
			if p == "" {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
		}
		if len(parts) != 1 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleCredentialGet(deps, parts[0], w, r)
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleCredentialCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r) // enforces tenant (400) + gcid (401)
	if tenantID == "" {
		return
	}
	_ = gcid // captured for future audit / event emission (operator provenance)
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createCredentialReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := credential.NewCredential(credential.NewCredentialInput{
		TenantID:     tenantID,
		Title:        req.Title,
		Code:         req.Code,
		IssuingBody:  req.IssuingBody,
		Description:  req.Description,
		Competencies: req.competencyInputs(),
	})
	if err != nil {
		// Domain guard failure (blank title, no competencies, dup code, …) → 400.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	if err := deps.Credentials.Save(tracing.WithTenantID(r.Context(), tenantID), c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, credentialDTO(c))
}

func handleCredentialList(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	items, err := deps.Credentials.ListByTenant(tracing.WithTenantID(r.Context(), tenantID), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, c := range items {
		out = append(out, credentialDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
}

func handleCredentialGet(deps Deps, credentialID string, w http.ResponseWriter, r *http.Request) {
	if deps.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	c, ok, err := deps.Credentials.Get(tracing.WithTenantID(r.Context(), tenantID), credentialID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential lookup failed: "+err.Error())
		return
	}
	if !ok || c == nil || c.TenantID != tenantID || c.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	writeJSON(w, http.StatusOK, credentialDTO(c))
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

// credentialDTO renders a Credential (with its competency breakdown) for the
// JSON wire (snake_case keys).
func credentialDTO(c *credential.Credential) map[string]interface{} {
	comps := make([]map[string]interface{}, 0, len(c.Competencies))
	for _, cp := range c.Competencies {
		comps = append(comps, map[string]interface{}{
			"competency_id": cp.CompetencyID,
			"code":          cp.Code,
			"name":          cp.Name,
			"description":   cp.Description,
			"weight":        cp.Weight,
		})
	}
	out := map[string]interface{}{
		"id":           c.ID,
		"tenant_id":    c.TenantID,
		"title":        c.Title,
		"code":         c.Code,
		"issuing_body": c.IssuingBody,
		"description":  c.Description,
		"competencies": comps,
		"created_at":   c.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":   c.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if c.DeletedAt != nil {
		out["deleted_at"] = c.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
