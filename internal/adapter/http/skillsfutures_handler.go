// skillsfutures_handler.go — HTTP handlers for the M15c SkillsFutures
// Claims surface (R+ Rhythm+ wave-2b).
//
// 5 endpoints registered in handlers.go:
//
//	POST   /api/v1/skillsfutures-claims          — learner submits a PENDING claim
//	GET    /api/v1/skillsfutures-claims          — training-admin lists tenant claims,
//	                                                optional ?state=PENDING|APPROVED|REJECTED|DISBURSED
//	GET    /api/v1/skillsfutures-claims/{id}     — single claim (training-admin: any
//	                                                tenant row; learner: own row only)
//	POST   /api/v1/skillsfutures-claims/{id}/approve  — training-admin decision
//	POST   /api/v1/skillsfutures-claims/{id}/reject   — training-admin decision
//
// Authorisation (verified inside the handler, NOT at middleware):
//   - tenantRequired (X-Tenant-Id) — handled via callerTenantGCID
//   - gcid header required for caller identity
//   - LIST + approve + reject require training-admin / admin / instructor role
//     via hasInstructorOrAdmin (the role bundle aligns with the rest of the
//     chora-delivery RBAC: instructors who manage cohort SSG claims are the
//     canonical admin callers per ADR-141 reconciliation)
//   - POST (submit) is gated to a non-empty gcid only — any learner with a
//     tenant context can file a claim against their own GCID
//   - single-row GET is dual-mode: admin sees any tenant claim, learner sees
//     only own (cross-learner reads 404 — never 403 — to avoid leaking
//     claim existence across learners)
//
// Per `feedback_no_stubs_real_wiring` — when the SkillsFutures Deps field
// is nil, the routes are NOT mounted at all (no in-handler 503). The test
// harness wires the in-mem repo explicitly via Deps.SkillsFutures.
//
// Per ddd-enforcement.md / multi-tenant-rls — tenant scoping is enforced
// at the repo + handler. Future pg adapter will additionally rely on RLS
// (composed with database-level isolation per multi-tenant-rls). NRIC is
// NEVER raw on the wire — the FE sends sha256(NRIC) only.
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/skillsfutures"
)

// -----------------------------------------------------------------------------
// Request types
// -----------------------------------------------------------------------------

type createSkillsFuturesClaimReq struct {
	CourseID                string `json:"course_id"`
	NRICHash                string `json:"nric_hash"`
	RequestedAmountSGDCents int64  `json:"requested_amount_sgd_cents"`
}

type approveSkillsFuturesClaimReq struct {
	ApprovedAmountSGDCents int64 `json:"approved_amount_sgd_cents"`
}

type rejectSkillsFuturesClaimReq struct {
	RejectionReason string `json:"rejection_reason"`
}

// -----------------------------------------------------------------------------
// /api/v1/skillsfutures-claims root dispatcher (POST submit + GET list)
// -----------------------------------------------------------------------------

// skillsFuturesRootHandler dispatches /api/v1/skillsfutures-claims (no id suffix).
func skillsFuturesRootHandler(repo skillsfutures.SkillsFuturesStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleSkillsFuturesCreate(repo, w, r)
		case http.MethodGet:
			handleSkillsFuturesList(repo, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// skillsFuturesSubHandler dispatches /api/v1/skillsfutures-claims/{id} and
// /api/v1/skillsfutures-claims/{id}/{action} where action is approve|reject.
func skillsFuturesSubHandler(repo skillsfutures.SkillsFuturesStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/skillsfutures-claims/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		parts := strings.Split(rest, "/")
		claimID := parts[0]
		if claimID == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}

		// /{id} — single-row GET.
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleSkillsFuturesGet(repo, claimID, w, r)
			return
		}

		// /{id}/{action} — approve | reject (POST only).
		if len(parts) == 2 {
			action := parts[1]
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			switch action {
			case "approve":
				handleSkillsFuturesApprove(repo, claimID, w, r)
			case "reject":
				handleSkillsFuturesReject(repo, claimID, w, r)
			default:
				writeError(w, http.StatusNotFound, "unknown action: "+action)
			}
			return
		}

		// More than 2 segments — unknown route.
		writeError(w, http.StatusNotFound, "not found")
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

// handleSkillsFuturesCreate handles POST /api/v1/skillsfutures-claims —
// learner submits a fresh PENDING claim. The gcid header IS the learner
// identity that owns the resulting claim row; no role gate beyond a
// non-empty gcid.
func handleSkillsFuturesCreate(
	repo skillsfutures.SkillsFuturesStore,
	w http.ResponseWriter,
	r *http.Request,
) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	var req createSkillsFuturesClaimReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := skillsfutures.NewClaim(skillsfutures.NewClaimInput{
		TenantID:             tenantID,
		GCID:                 gcid,
		CourseID:             req.CourseID,
		NRICHash:             req.NRICHash,
		RequestedAmountCents: req.RequestedAmountSGDCents,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	if err := repo.Save(tracing.WithTenantID(r.Context(), tenantID), c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, skillsFuturesClaimDTO(c))
}

// handleSkillsFuturesList handles GET /api/v1/skillsfutures-claims — training
// admin lists tenant claims with optional ?state= filter. RBAC: instructor /
// admin / training-admin required (mirrors hasInstructorOrAdmin used by the
// rest of the R+ surface — instructors who manage cohort SSG claims are the
// canonical admin callers per ADR-141 reconciliation).
func handleSkillsFuturesList(
	repo skillsfutures.SkillsFuturesStore,
	w http.ResponseWriter,
	r *http.Request,
) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden,
			"caller lacks training-admin role required to list SkillsFutures claims")
		return
	}
	var state skillsfutures.ClaimState
	if s := strings.TrimSpace(r.URL.Query().Get("state")); s != "" {
		candidate := skillsfutures.ClaimState(s)
		if !candidate.IsValid() {
			writeError(w, http.StatusBadRequest, "invalid state: "+s)
			return
		}
		state = candidate
	}
	items, err := repo.ListByTenant(tracing.WithTenantID(r.Context(), tenantID), tenantID, state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, c := range items {
		out = append(out, skillsFuturesClaimDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

// handleSkillsFuturesGet handles GET /api/v1/skillsfutures-claims/{id}.
//
// Visibility rules (test-anchored):
//   - training-admin / admin / instructor sees any claim in their tenant
//   - learner sees ONLY their own claim row (matched on gcid)
//   - cross-tenant: always 404 (never leak existence)
//   - cross-learner without admin role: 404 (never leak existence)
func handleSkillsFuturesGet(
	repo skillsfutures.SkillsFuturesStore,
	claimID string,
	w http.ResponseWriter,
	r *http.Request,
) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	c, ok, err := repo.Get(tracing.WithTenantID(r.Context(), tenantID), claimID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "claim lookup failed: "+err.Error())
		return
	}
	if !ok || c.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "claim not found")
		return
	}
	// Learners (no admin role) can only see their own claim. Admins see
	// any tenant-scoped row.
	if !hasInstructorOrAdmin(r) && c.GCID != gcid {
		writeError(w, http.StatusNotFound, "claim not found")
		return
	}
	writeJSON(w, http.StatusOK, skillsFuturesClaimDTO(c))
}

// handleSkillsFuturesApprove handles POST /{id}/approve — training-admin
// approves a PENDING claim with an explicit approved amount.
func handleSkillsFuturesApprove(
	repo skillsfutures.SkillsFuturesStore,
	claimID string,
	w http.ResponseWriter,
	r *http.Request,
) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden,
			"caller lacks training-admin role required to approve SkillsFutures claims")
		return
	}
	c, ok, err := repo.Get(tracing.WithTenantID(r.Context(), tenantID), claimID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "claim lookup failed: "+err.Error())
		return
	}
	if !ok || c.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "claim not found")
		return
	}
	var req approveSkillsFuturesClaimReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := c.Approve(gcid, req.ApprovedAmountSGDCents); err != nil {
		writeSkillsFuturesDomainError(w, err)
		return
	}
	if err := repo.Save(tracing.WithTenantID(r.Context(), tenantID), c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, skillsFuturesClaimDTO(c))
}

// handleSkillsFuturesReject handles POST /{id}/reject — training-admin
// rejects a PENDING claim with an actionable rejection_reason.
func handleSkillsFuturesReject(
	repo skillsfutures.SkillsFuturesStore,
	claimID string,
	w http.ResponseWriter,
	r *http.Request,
) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" || gcid == "" {
		return
	}
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden,
			"caller lacks training-admin role required to reject SkillsFutures claims")
		return
	}
	c, ok, err := repo.Get(tracing.WithTenantID(r.Context(), tenantID), claimID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "claim lookup failed: "+err.Error())
		return
	}
	if !ok || c.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "claim not found")
		return
	}
	var req rejectSkillsFuturesClaimReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := c.Reject(gcid, req.RejectionReason); err != nil {
		writeSkillsFuturesDomainError(w, err)
		return
	}
	if err := repo.Save(tracing.WithTenantID(r.Context(), tenantID), c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, skillsFuturesClaimDTO(c))
}

// -----------------------------------------------------------------------------
// Error mapping — domain sentinel → HTTP status
// -----------------------------------------------------------------------------

// writeSkillsFuturesDomainError maps a skillsfutures domain sentinel to an
// HTTP error envelope:
//   - state-machine guards (ErrNotPending / ErrNotApproved) → 409 CONFLICT
//   - validation failures (amount / reason / decider) → 400 BAD REQUEST
func writeSkillsFuturesDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, skillsfutures.ErrNotPending),
		errors.Is(err, skillsfutures.ErrNotApproved):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

// skillsFuturesClaimDTO renders the wire shape consumed by the R+ FE +
// `skillsfutures_handler_test.go`. Field naming aligns with the
// chora_delivery.skillsfutures_claims column names the pg adapter will
// emit at M12.3+ (snake_case + `_sgd_cents` amount-with-currency suffix).
func skillsFuturesClaimDTO(c *skillsfutures.SkillsFuturesClaim) map[string]interface{} {
	out := map[string]interface{}{
		"id":                         c.ID,
		"tenant_id":                  c.TenantID,
		"gcid":                       c.GCID,
		"course_id":                  c.CourseID,
		"nric_hash":                  c.NRICHash,
		"requested_amount_sgd_cents": c.RequestedAmountCents,
		"approved_amount_sgd_cents":  c.ApprovedAmountCents,
		"state":                      string(c.State),
		"submitted_at":               c.SubmittedAt.Format(time.RFC3339Nano),
	}
	if c.DecidedAt != nil {
		out["decided_at"] = c.DecidedAt.Format(time.RFC3339Nano)
	}
	if c.DecidedByGCID != "" {
		out["decided_by_gcid"] = c.DecidedByGCID
	}
	if c.RejectionReason != "" {
		out["rejection_reason"] = c.RejectionReason
	}
	return out
}
