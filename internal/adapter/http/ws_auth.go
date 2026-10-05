// ws_auth.go — caller authentication + tenant-scoping for the R+ classroom
// realtime WebSocket fan-out endpoints (ADR-168 / CHO-1616, 2026-06-01).
//
// The chora-gateway JWT gate validates the Chora session JWT (carried via the
// access_token query param on the browser WS handshake — browsers cannot set
// the Authorization header on a native WebSocket upgrade per RFC 6455) and
// stamps the validated tenant + gcid onto the X-Tenant-Id + gcid mesh-trust
// headers. The /ws subtrees deliberately bypass the tenantRequired middleware
// (it sets Content-Type: application/json, which conflicts with the 101
// Switching Protocols handshake — see live_quizzes_dispatcher.go), so the WS
// handlers re-apply the same trust boundary here.
//
// Why both gateway AND delivery enforce: defence in depth. The mesh authz
// policy already restricts WS callers to the gateway identity, but that is
// service-to-service trust, not END-USER auth. A caller reaching chora-delivery
// directly (bypassing the gateway) must still be tenant-scoped.
package httpapi

import (
	"net/http"
	"strings"
)

// requireWSCallerIdentity validates that a classroom-realtime WS upgrade
// carries the gateway-stamped caller identity (X-Tenant-Id + gcid). It returns
// the caller's tenant + ok=true on success. On failure it has ALREADY written
// the 401 and the caller MUST return without upgrading.
//
// MUST be called BEFORE the session/poll lookup so an unauthenticated probe
// can never use the 404-vs-401 distinction as an existence oracle: a missing
// identity is 401 regardless of whether the aggregate exists. The caller then
// compares the returned tenant against the resolved aggregate's tenant and
// 404s on mismatch (wsTenantMatches) — indistinguishable from not-found, so
// cross-tenant existence never leaks.
func requireWSCallerIdentity(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "X-Tenant-Id required (caller identity)")
		return "", false
	}
	if strings.TrimSpace(r.Header.Get("gcid")) == "" {
		writeError(w, http.StatusUnauthorized, "gcid header required (caller identity)")
		return "", false
	}
	return tenantID, true
}

// wsTenantMatches reports whether the caller's tenant matches the resolved
// aggregate's tenant. Compared case-insensitively (tenant IDs are UUIDs; the
// mesh header casing is not normalised end-to-end). Call AFTER the lookup; on
// false the caller MUST 404 (NOT 403) so cross-tenant existence never leaks.
func wsTenantMatches(callerTenant, aggregateTenant string) bool {
	return strings.EqualFold(strings.TrimSpace(callerTenant), strings.TrimSpace(aggregateTenant))
}
