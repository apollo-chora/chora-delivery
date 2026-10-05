// Package httpapi — GET /api/v1/certifications list handler.
//
// ADD-ONLY 2026-05-26 for the R+ /r/certifications surface (M12 R+
// buildout). Sits alongside the legacy POST /api/certifications + GET
// /api/certifications/{id} pair (handlers.go::certificationsHandler +
// certificationSubHandler). The orchestrator wires the new route into
// NewServer's mux at integration time — this file ships the handler
// factory only so this work is parallel-safe (no edits to handlers.go).
//
// Path: /api/v1/certifications  (parallel to legacy /api/certifications)
// Method: GET only (POST stays on /api/certifications via legacy handler)
// Query parameters (all optional):
//
//	learner_gcid — filter to a single learner's certs
//	course_id    — filter to a single course's certs
//
// Tenant isolation: enforced via the X-Tenant-Id header read off the
// validated mesh claims the chora-gateway BFF stamps (RequireChora
// SessionJWT). The handler scopes the projection to the tenant id;
// cross-tenant rows are filtered in domain.CertificationRegistry
// .ListByTenant() and never reach the wire.
//
// Wire shape: { "items": [CertificationDTO, ...] } — never null, always
// a non-nil slice so the FE can iterate without a null-check. Per-item
// shape comes from the canonical certDTO() helper in handlers.go so it
// is byte-identical with the legacy POST + GET-by-id surface.
package httpapi

import (
	"net/http"
)

// CertificationsListHandler returns an http.HandlerFunc that handles
// GET /api/v1/certifications. Exported so the orchestrator can wire it
// into NewServer's mux at integration time without us editing handlers.go.
//
// The handler depends only on deps.Certifications (the CertificationRegistry);
// the other Deps fields are NOT touched, so the in-memory Deps used by
// the existing test suite continues to satisfy the wiring.
func CertificationsListHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tenantID := r.Header.Get("X-Tenant-Id")
		if tenantID == "" {
			// Parallel to the legacy tenantRequired middleware shape so
			// the orchestrator can plug the handler under tenantRequired
			// or leave the guard inline. Both forms produce the same
			// 400 envelope here.
			writeError(w, http.StatusBadRequest, "X-Tenant-Id header required")
			return
		}
		learnerGCID := r.URL.Query().Get("learner_gcid")
		courseID := r.URL.Query().Get("course_id")

		certs, _ := deps.Certifications.ListByTenantCtx(r.Context(), tenantID, learnerGCID, courseID)

		// Always send a non-nil slice so the JSON wire is `[]` not `null`.
		items := make([]map[string]interface{}, 0, len(certs))
		for _, c := range certs {
			items = append(items, certDTO(c))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items": items,
		})
	}
}
