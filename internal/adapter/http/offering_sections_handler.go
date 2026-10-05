// offering_sections_handler.go — HTTP handlers for the offering-nested Sections
// surface (R+ four-mode W7 / D2): intra-cohort sub-groups of a GRADUATE offering.
//
// Endpoints (dispatched from offeringsSubHandler in offering_handler.go):
//
//	GET  /api/v1/offerings/{id}/sections  — list this offering's sections
//	POST /api/v1/offerings/{id}/sections  — create one intra-cohort section
//
// A Section is a CHILD of the Offering aggregate (its own lead / room / delivery
// dates; SHARES the cohort's curriculum + gradebook). It is persisted inside the
// Offering's JSONB snapshot — NO own table, NO migration, NO new Pub/Sub event,
// NO cross-DB query (ddd-enforcement #3). Sections exist only on a graduate
// offering (the create 409s otherwise — the domain ErrSectionNotGraduate). There
// is NO DELETE here (soft-delete via a future PATCH status field, per the design).
//
// Authorisation mirrors the offering surface: instructor / admin / training-admin
// (hasOfferingAdminRole) + X-Tenant-Id + gcid (callerTenantGCID). Tenant-scoped
// throughout.
package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// createSectionReq is the POST body (snake_case wire → domain input). Only
// `name` is required; the rest are optional delivery logistics.
type createSectionReq struct {
	Name               string `json:"name"`
	LeadInstructorGCID string `json:"lead_instructor_gcid"`
	Room               string `json:"room"`
	StartDate          string `json:"start_date"`
	EndDate            string `json:"end_date"`
}

// handleOfferingListSections — GET /api/v1/offerings/{id}/sections.
//
// Admin-gated. Verifies the offering exists for this tenant (404), then returns
// `{sections:[sectionDTO...]}` in stored (append) order. Read-only.
func handleOfferingListSections(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
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
	dtos := make([]map[string]interface{}, 0, len(o.Sections))
	for _, s := range o.Sections {
		dtos = append(dtos, offeringSectionDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sections": dtos})
}

// handleOfferingCreateSection — POST /api/v1/offerings/{id}/sections.
//
// Admin-gated. Steps:
//
//	(a) verify the offering exists for this tenant (404 if missing / cross-tenant
//	    / soft-deleted);
//	(b) decode the body (400 on malformed JSON);
//	(c) o.AddSection(...) — graduate-only + non-empty name (409 ErrSectionNotGraduate,
//	    400 on a blank name);
//	(d) Save the offering via the existing OfferingPort (the section rides in the
//	    JSONB aggregate — no own table); 500 on a write error;
//	(e) 201 + the created sectionDTO.
func handleOfferingCreateSection(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	// (a) Offering must exist for this tenant (same-DB soft FK; no Go FK).
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	// (b) Decode the body.
	var req createSectionReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// (c) Domain mutator — graduate-only + non-empty name.
	sec, err := o.AddSection(delivery.AddSectionInput{
		Name:               req.Name,
		LeadInstructorGCID: req.LeadInstructorGCID,
		Room:               req.Room,
		StartDate:          req.StartDate,
		EndDate:            req.EndDate,
	})
	if err != nil {
		// A non-graduate offering is a precondition conflict (409); a blank name
		// is a bad request (400) — same split idiom as the transition handler.
		if errors.Is(err, delivery.ErrSectionNotGraduate) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// (d) Persist the mutated aggregate (section lives in the JSONB snapshot).
	if err := deps.Offerings.Save(ctx, o); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// (e) 201 with the created section.
	writeJSON(w, http.StatusCreated, offeringSectionDTO(*sec))
}

// updateSectionReq is the PATCH body (R+ Phase-2 S4). Every field is optional
// (pointer) so a client can edit just one attribute; omitted fields are left
// unchanged.
type updateSectionReq struct {
	Name               *string `json:"name"`
	LeadInstructorGCID *string `json:"lead_instructor_gcid"`
	Room               *string `json:"room"`
	StartDate          *string `json:"start_date"`
	EndDate            *string `json:"end_date"`
}

// handleOfferingUpdateSection — PATCH /api/v1/offerings/{id}/sections/{sectionId}.
//
// Admin-gated. Edits an existing section's delivery logistics (name / lead /
// room / dates); only provided (non-nil) fields change. 404 for a missing
// offering OR section; 400 on a blank name / malformed body; 200 + the updated
// sectionDTO on success. The section rides in the Offering JSONB snapshot.
func handleOfferingUpdateSection(deps Deps, offeringID, sectionID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
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
	var req updateSectionReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sec, err := o.UpdateSection(sectionID, delivery.UpdateSectionInput{
		Name:               req.Name,
		LeadInstructorGCID: req.LeadInstructorGCID,
		Room:               req.Room,
		StartDate:          req.StartDate,
		EndDate:            req.EndDate,
	})
	if err != nil {
		if errors.Is(err, delivery.ErrSectionNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error()) // blank name → 400
		return
	}
	if err := deps.Offerings.Save(ctx, o); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offeringSectionDTO(*sec))
}

// offeringSectionDTO renders a Section for the JSON wire (snake_case keys).
func offeringSectionDTO(s delivery.Section) map[string]interface{} {
	return map[string]interface{}{
		"section_id":           s.SectionID,
		"name":                 s.Name,
		"lead_instructor_gcid": s.LeadInstructorGCID,
		"room":                 s.Room,
		"start_date":           s.StartDate,
		"end_date":             s.EndDate,
		"created_at":           s.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":           s.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}
