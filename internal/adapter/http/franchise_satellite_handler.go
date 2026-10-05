// franchise_satellite_handler.go: the write-restricted admin path for the
// ADR-192 D1 franchise_satellite mapping (W5 bypass-free slice, CHO-2230).
//
// The mapping is the authoritative, revocable scope the (W5-gated, NOT built
// here) exam_owner_rollup RLS policy will read: one live row = "owner may
// aggregate satellite". Managing rows is ordinary tenant-scoped CRUD; only
// the policy reading THROUGH the table is the sanctioned 3rd bypass surface.
//
// Authorisation (ADR-192 O1 posture: the platform-owner authority writes the
// map): platform_operator (may act for ANY owner tenant, named explicitly) or
// an admin of the OWNER tenant (owner / tenant_admin / admin roles, always
// scoped to their own tenant). Verified in-handler from x-mesh-user-roles,
// mirroring exam_handler.go. Cross-tenant probes read as 404, never 403.
//
// Endpoints:
//
//	POST   /api/v1/franchise-satellites        create a mapping
//	GET    /api/v1/franchise-satellites        list an owner's live mappings
//	DELETE /api/v1/franchise-satellites/{id}   revoke (soft delete)
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

// FranchiseSatelliteDeps wires the admin surface.
type FranchiseSatelliteDeps struct {
	Store franchise.Store
}

// RegisterFranchiseSatelliteRoutes mounts the admin routes on mux.
func RegisterFranchiseSatelliteRoutes(mux *http.ServeMux, d FranchiseSatelliteDeps) {
	mux.HandleFunc("POST /api/v1/franchise-satellites", logging(tenantRequired(franchiseCreateHandler(d))))
	mux.HandleFunc("GET /api/v1/franchise-satellites", logging(tenantRequired(franchiseListHandler(d))))
	mux.HandleFunc("DELETE /api/v1/franchise-satellites/{id}", logging(tenantRequired(franchiseRevokeHandler(d))))
}

// -----------------------------------------------------------------------------
// RBAC helpers
// -----------------------------------------------------------------------------

// isPlatformOperator reports whether the caller carries the ADR-165 platform
// operator role. Spelling is normalised in mesh_roles.go, so the hyphen form
// this used to enumerate by hand is handled there for every role at once.
func isPlatformOperator(r *http.Request) bool {
	return callerHasMeshRole(r, rolePlatformOperator)
}

// hasFranchiseAdminRole reports whether the caller is an admin of its tenant
// (the owner-tenant authority for the franchise map). Deliberately NARROWER
// than hasExamAdminRole: instructors and training admins run deliveries; they
// do not define the cross-tenant rollup scope.
func hasFranchiseAdminRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleOwner, roleAdmin, roleTenantAdmin)
}

// resolveFranchiseCaller gates the request and resolves the EFFECTIVE owner
// tenant the operation is scoped to. requestedOwner comes from the body
// (create) or query (list/revoke); empty means "the caller's own tenant".
// Returns owner=="" after writing the error response.
func resolveFranchiseCaller(w http.ResponseWriter, r *http.Request, requestedOwner string) (owner, gcid string) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return "", ""
	}
	operator := isPlatformOperator(r)
	if !operator && !hasFranchiseAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks platform_operator or owner-tenant admin role")
		return "", ""
	}
	requestedOwner = strings.ToLower(strings.TrimSpace(requestedOwner))
	if operator {
		// The operator holds no tenant of its own (ADR-165: no
		// tenant_memberships row), so the owner MUST be named explicitly on a
		// write path; list/revoke default to the header tenant for symmetry.
		if requestedOwner != "" {
			return requestedOwner, gcid
		}
		return strings.ToLower(tenantID), gcid
	}
	// A tenant admin acts ONLY for its own tenant; naming another owner is a
	// scope escalation and is refused loudly.
	if requestedOwner != "" && requestedOwner != strings.ToLower(tenantID) {
		writeError(w, http.StatusForbidden, "owner_tenant_id must be the caller's own tenant")
		return "", ""
	}
	return strings.ToLower(tenantID), gcid
}

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

type createFranchiseSatelliteReq struct {
	OwnerTenantID     string `json:"owner_tenant_id"`
	SatelliteTenantID string `json:"satellite_tenant_id"`
}

func franchiseCreateHandler(d FranchiseSatelliteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			writeError(w, http.StatusServiceUnavailable, "franchise satellite store not wired")
			return
		}
		var req createFranchiseSatelliteReq
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		owner, gcid := resolveFranchiseCaller(w, r, req.OwnerTenantID)
		if owner == "" {
			return
		}
		// An operator creating a mapping must be explicit about the owner: a
		// default scope on a cross-tenant grant is how a wrong row gets minted.
		if isPlatformOperator(r) && strings.TrimSpace(req.OwnerTenantID) == "" {
			writeError(w, http.StatusBadRequest, "owner_tenant_id: required when acting as platform_operator")
			return
		}
		m, err := franchise.New(owner, req.SatelliteTenantID, gcid)
		if err != nil {
			writeError(w, franchiseStatus(err), err.Error())
			return
		}
		// RLS reads the tenant from the context (the OWNER tenant scopes the
		// row), so scope it before the write, never from a SQL arg.
		ctx := tracing.WithTenantID(r.Context(), m.OwnerTenantID)
		if err := d.Store.Save(ctx, m); err != nil {
			if errors.Is(err, franchise.ErrDuplicateMapping) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "franchise satellite write failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, franchiseSatelliteDTO(m))
	}
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

func franchiseListHandler(d FranchiseSatelliteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			writeError(w, http.StatusServiceUnavailable, "franchise satellite store not wired")
			return
		}
		owner, _ := resolveFranchiseCaller(w, r, r.URL.Query().Get("owner_tenant_id"))
		if owner == "" {
			return
		}
		ctx := tracing.WithTenantID(r.Context(), owner)
		items, err := d.Store.ListByOwner(ctx, owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "franchise satellite list failed: "+err.Error())
			return
		}
		out := make([]map[string]interface{}, 0, len(items))
		for _, m := range items {
			out = append(out, franchiseSatelliteDTO(m))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
	}
}

// -----------------------------------------------------------------------------
// Revoke (soft delete: the rollup scope shrinks the moment the row is gone)
// -----------------------------------------------------------------------------

func franchiseRevokeHandler(d FranchiseSatelliteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Store == nil {
			writeError(w, http.StatusServiceUnavailable, "franchise satellite store not wired")
			return
		}
		owner, _ := resolveFranchiseCaller(w, r, r.URL.Query().Get("owner_tenant_id"))
		if owner == "" {
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		ctx := tracing.WithTenantID(r.Context(), owner)
		m, ok, err := d.Store.Get(ctx, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "franchise satellite lookup failed: "+err.Error())
			return
		}
		// Cross-tenant probes read as not-found: existence never leaks.
		if !ok || m == nil || m.OwnerTenantID != owner {
			writeError(w, http.StatusNotFound, "franchise satellite mapping not found")
			return
		}
		revoked, err := d.Store.Revoke(ctx, id, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "franchise satellite revoke failed: "+err.Error())
			return
		}
		if !revoked {
			writeError(w, http.StatusNotFound, "franchise satellite mapping not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"revoked": true, "id": id})
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// franchiseStatus maps domain sentinels to HTTP status codes.
func franchiseStatus(err error) int {
	switch {
	case errors.Is(err, franchise.ErrOwnerTenantRequired),
		errors.Is(err, franchise.ErrSatelliteTenantRequired),
		errors.Is(err, franchise.ErrCreatedByRequired),
		errors.Is(err, franchise.ErrOwnerTenantInvalid),
		errors.Is(err, franchise.ErrSatelliteTenantInvalid),
		errors.Is(err, franchise.ErrSelfMapping):
		return http.StatusBadRequest
	case errors.Is(err, franchise.ErrDuplicateMapping):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func franchiseSatelliteDTO(m *franchise.FranchiseSatellite) map[string]interface{} {
	return map[string]interface{}{
		"id":                  m.ID,
		"owner_tenant_id":     m.OwnerTenantID,
		"satellite_tenant_id": m.SatelliteTenantID,
		"created_by_gcid":     m.CreatedByGCID,
		"created_at":          m.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":          m.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}
