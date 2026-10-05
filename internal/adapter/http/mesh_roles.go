// mesh_roles.go: the ONE role vocabulary every chora-delivery HTTP gate reads.
//
// WHY THIS FILE EXISTS
// --------------------
// chora-delivery had fourteen role gates, each re-parsing `x-mesh-user-roles`
// with its own inline accept-list. Two of those lists spelled TRAINING_ADMIN
// with a hyphen. Nothing in the estate has ever minted a hyphen: chora-gateway
// stamps `training_admin` (mint_handler.go `TrainingAdminRole`, appended to an
// instructor membership by expandTrainingAdmin) and jwt_auth.go forwards the
// roles claim VERBATIM onto the header. So those two gates refused the very
// persona they were written for, and the refusal read as a deliberate
// permissions decision because a 403 that names a role always does.
//
// The nine gates that worked did so only because somebody had patched the
// underscore in as a second literal at the CALL SITE. Patching the instance
// left the name wrong, which is why the tenth and eleventh gates were still
// broken.
//
// THE FIX IS THE ESTATE'S OWN CONVENTION
// --------------------------------------
// Four gates elsewhere already normalise before comparing: chora-gateway's
// hasTenantAdminRole ("'-'/'_' interchangeable, case-insensitive"),
// companion_suspension_handler.go, closure_handler.go, and chora-tenancy's
// transaction_history_server.go. chora-delivery simply never adopted it.
// normaliseMeshRole is that same transform, applied once, so a spelling
// difference can no longer become an authorisation difference.
//
// WIRE VOCABULARY (verified against the mint, 2026-09-06)
// -------------------------------------------------------
// The chora-session JWT is HS256-signed by chora-gateway and its `roles` claim
// is the ONLY source of this header, so the vocabulary is closed:
//
//	learner · author · instructor · admin · auditor · owner   (membership_role enum)
//	training_admin · platform_operator                        (JWT-extension stamps)
//	proctor                                                   (ADR-191; NOT yet minted: see roleProctor)
//
// Adding a token here does NOT make it mintable. A gate is an allow-list, and
// an allow-list entry is only reachable once something upstream issues it.
package httpapi

import (
	"net/http"
	"strings"
)

// Canonical mesh role tokens: lowercase, underscore-separated, matching what
// chora-gateway mints. Compare against these ONLY after normaliseMeshRole.
const (
	roleLearner    = "learner"
	roleAuthor     = "author"
	roleInstructor = "instructor"
	roleAdmin      = "admin"
	roleAuditor    = "auditor"
	roleOwner      = "owner"

	// roleTrainingAdmin is the R+ delivery-admin label. chora-gateway derives
	// it from an instructor membership (expandTrainingAdmin) and APPENDS it,
	// so on the wire it always arrives alongside `instructor` today. Gates
	// must still honour it standing alone: ADR-239 Alternative C leaves a
	// standalone-role split open, and a branch that only ever matches because
	// a sibling role happens to co-occur is dead code wearing a passing test.
	roleTrainingAdmin = "training_admin"

	// roleTenantAdmin is the canonical uppercase-form label ADR-141 names
	// TENANT_ADMIN. It is not minted onto x-mesh-user-roles today (the mint
	// forwards the lowercase `admin` membership role); it is accepted so a
	// future canonicalisation upstream cannot 403 the estate.
	roleTenantAdmin = "tenant_admin"

	// rolePlatformOperator is the sole cross-tenant role (ADR-165).
	rolePlatformOperator = "platform_operator"

	// roleProctor is the exam-invigilation role (ADR-191 D1): tenant-scoped,
	// add-on-gated on `exam_administration`, bound per sitting by an
	// ExamInvigilator row, and EMBARGOED from exam content.
	//
	// ⚠ NOT MINTABLE TODAY. Nothing in chora-gateway or chora-identity puts
	// this token on a JWT: `proctor` is not a membership_role enum value and
	// no stamp appends it, so no live principal can currently present it. The
	// gates below admit it because ADR-191 is the ratified contract and the
	// handlers were the defect; making a real proctor session is separate,
	// upstream work. Lowercase per the JWT-extension precedent
	// (training_admin / platform_operator are both stamped lowercase) and
	// matching the token chora-creation's LIVE embargo already keys on
	// (exam_embargo.go `proctorRoleToken`).
	roleProctor = "proctor"
)

// normaliseMeshRole canonicalises one role token to the minted shape:
// trimmed, lowercase, hyphens folded to underscores. `TRAINING-ADMIN`,
// `training-admin` and `training_admin` all resolve to the same token, so a
// spelling can never again decide an authorisation.
//
// No role in the closed vocabulary above carries a semantically significant
// hyphen, which is what makes the fold safe.
func normaliseMeshRole(role string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(role), "-", "_"))
}

// meshRoleTokens splits `x-mesh-user-roles` into normalised tokens. A missing
// or empty header yields nil: "no roles", which every gate treats as a
// refusal (fail-closed; the fail-OPEN X-Role path was deleted in CHO-2072).
//
// The header is set by the gateway from validated session claims and is
// never client-supplied (upstream/http_upstream.go AuthCtx.Roles).
func meshRoleTokens(r *http.Request) []string {
	raw := r.Header.Get("x-mesh-user-roles")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if tok := normaliseMeshRole(p); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// callerHasMeshRole reports whether the caller presents any of want. want MUST
// be canonical tokens from the constants above: they are compared against
// already-normalised input, so a hyphenated literal here would never match.
func callerHasMeshRole(r *http.Request, want ...string) bool {
	for _, held := range meshRoleTokens(r) {
		for _, w := range want {
			if held == w {
				return true
			}
		}
	}
	return false
}
