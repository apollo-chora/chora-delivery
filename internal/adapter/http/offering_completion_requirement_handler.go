// offering_completion_requirement_handler.go - the WRITE path for an
// offering's DECLARED completion components (CHO-2222, ADR-190 D1).
//
// Endpoint (dispatched from offeringsSubHandler in offering_handler.go):
//
//	PATCH /api/v1/offerings/{id}/completion-requirement
//
// The declaration is READ back via GET /api/v1/offerings/{id}/certification,
// alongside the completion policy it composes with (offering_certification_
// handler.go): one round-trip renders the whole Certification tab. Hence
// PATCH-only here, and a 405 on anything else.
//
// # Why this exists at all
//
// ADR-190 D1 says a per-offering CompletionRequirement declares which
// components THIS offering needs. The type shipped in sub-phase 1 with NO write
// path, so no offering could declare anything and the gate was inert. This is
// the path that arms it - and it ships in the SAME change as the resolver that
// answers it, because an editor without a resolver would leave every declared
// component permanently unsatisfied and withhold every certificate on the
// offering.
//
// # Shape
//
// Mirrors handleOfferingSetCompletionPolicy exactly: same wiring 503, same
// tenant 400 / gcid 401 via callerTenantGCID, same hasOfferingAdminRole 403,
// same offering 404, same domain-error 400, same hand-built snake_case DTO.
// They are the same kind of thing - editable offering-level delivery policy on
// the same aggregate - so they behave identically at the edge.
//
// Intra-chora_delivery only: this touches the Offering aggregate and nothing
// else. NO new table (the requirement rides the Offering JSONB snapshot, proven
// by pg/offering_completion_requirement_test.go), NO migration, NO event.
package httpapi

import (
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// completionComponentReq is one declared component on the wire.
type completionComponentReq struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// setCompletionRequirementReq is the PATCH /completion-requirement body.
//
// An ABSENT or empty components list is a legitimate declaration ("this
// offering requires nothing beyond its policy") and the way an admin CLEARS a
// previous one - so it is a 200, not a 400.
type setCompletionRequirementReq struct {
	Components []completionComponentReq `json:"components"`
}

// handleOfferingSetCompletionRequirement - PATCH
// /api/v1/offerings/{id}/completion-requirement.
func handleOfferingSetCompletionRequirement(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
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
	var req setCompletionRequirementReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	components := make([]delivery.CompletionComponent, 0, len(req.Components))
	for _, c := range req.Components {
		components = append(components, delivery.CompletionComponent{
			Kind: delivery.CompletionComponentKind(c.Kind),
			Ref:  c.Ref,
		})
	}

	// Every guard is a 400 carrying the domain's own message, which names the
	// offending index + value. A 400 (not a 500) because these are all the
	// CALLER's input: a 5xx here would blame the platform for an admin's typo,
	// and would never alert as the client error it is.
	if err := o.SetCompletionRequirement(delivery.SetCompletionRequirementInput{Components: components}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.Offerings.Save(ctx, o); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offeringCompletionRequirementDTO(o.CompletionRequirement))
}

// offeringCompletionRequirementDTO renders the declared requirement
// (snake_case), mirroring offeringCompletionPolicyDTO.
//
// components is always a LIST, never null: an offering that declares nothing
// renders [], so the FE has one shape to handle rather than two.
func offeringCompletionRequirementDTO(req *delivery.CompletionRequirement) map[string]interface{} {
	components := make([]map[string]interface{}, 0)
	if req != nil {
		for _, c := range req.Components {
			components = append(components, map[string]interface{}{
				"kind": string(c.Kind),
				"ref":  c.Ref,
			})
		}
	}
	out := map[string]interface{}{"components": components}
	if req != nil {
		out["updated_at"] = req.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
