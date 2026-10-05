// offering_assessments_handler.go — HTTP handlers for the W3.A offering-nested
// assessment surface: making an Offering's assessments addressable.
//
// Endpoints (dispatched from offeringsSubHandler in offering_handler.go):
//
//	GET  /api/v1/offerings/{id}/assessments  — list this offering's assessments
//	POST /api/v1/offerings/{id}/assessments  — attach (create) a PUBLISHED
//	                                            test-set as a live assessment
//
// Intra-chora_delivery only (Offering + Assessment + TestSet share the DB per
// ddd-enforcement #3) — NO cross-DB query, NO new Pub/Sub event. The POST
// reuses the existing chora.delivery.assessment.created.v1 (+ published/opened)
// emission via the shared buildAndSaveAssessment core (ZERO duplication).
//
// Detach is NOT a DELETE here — it reuses POST /api/v1/assessments/{id}/archive
// (soft-delete) per the W3.A contract.
//
// Authorisation mirrors the assessment surface: instructor / training-admin
// (hasInstructorRole) + X-Tenant-Id + gcid (callerTenantGCID).
package httpapi

import (
	"net/http"

	"github.com/apollo-chora/chora-common/tracing"
)

// handleOfferingListAssessments — GET /api/v1/offerings/{id}/assessments.
//
// Instructor / training-admin gated; returns the same
// `{items:[assessmentDTO...], next_page_token}` envelope as
// listAssessmentsHandler, scoped to the offering via
// AssessmentRepo.ListByOffering (tenant + RLS scoped, created_at DESC).
func handleOfferingListAssessments(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	adeps := deps.AssessmentDeps
	if adeps == nil || adeps.Assessments == nil {
		writeError(w, http.StatusServiceUnavailable, "assessments repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return
	}
	pageSize := parseTestSetPageSize(r.URL.Query().Get("page_size"))
	items, nextToken, err := adeps.Assessments.ListByOffering(
		r.Context(), tenantID, offeringID, pageSize, r.URL.Query().Get("page_token"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "assessment list failed: "+err.Error())
		return
	}
	dtos := make([]map[string]interface{}, 0, len(items))
	for _, a := range items {
		dtos = append(dtos, assessmentDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":           dtos,
		"next_page_token": nullableToken(nextToken),
	})
}

// handleOfferingCreateAssessment — POST /api/v1/offerings/{id}/assessments.
//
// Instructor / training-admin gated. Steps:
//
//	(a) validate the offering exists for this tenant via the OfferingPort
//	    (404 if missing / cross-tenant / soft-deleted);
//	(b-e) delegate to the shared buildAndSaveAssessment core with
//	    OfferingID = path id + the PUBLISHED test-set gate ON — which decodes
//	    the body, validates test_set_id exists AND is PUBLISHED (400 otherwise),
//	    snapshots totals, runs the auto-publish FSM, Saves, emits
//	    created (+published/opened), and writes 201 assessmentDTO.
func handleOfferingCreateAssessment(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	adeps := deps.AssessmentDeps
	if adeps == nil || adeps.Assessments == nil {
		writeError(w, http.StatusServiceUnavailable, "assessments repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return
	}
	// (a) Offering must exist for this tenant (same-DB soft FK; no Go FK).
	o, ok, err := deps.Offerings.Get(tracing.WithTenantID(r.Context(), tenantID), offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	// (b-e) Shared create core — offering-scoped + PUBLISHED test-set gate ON.
	buildAndSaveAssessment(adeps, w, r, tenantID, gcid, offeringID, true)
}
