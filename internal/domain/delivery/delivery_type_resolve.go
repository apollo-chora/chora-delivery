// delivery_type_resolve.go — resolve the delivery_type (mode) that produced a
// graded submission (CHO-2224, §10.6 capstone criterion 1).
//
// ADR-190 D1 puts delivery_type on the Offering, not the Course, so one
// curriculum stays reusable across modes. A graded submission therefore learns
// its mode by walking Assessment.OfferingID -> Offering.DeliveryType. Both hops
// are INTRA-domain (chora_delivery), so no cross-DB rule is engaged.
//
// This is the SINGLE copy of that walk, shared by both publish sites (the HTTP
// emit funnel in adapter/http and the OE batch path in adapter/subscribers).
// It lives here, in the domain, rather than at either call site because two
// hand-maintained copies of one lookup drift, and a drifted mode is worse than
// no mode: it is a wrong fact in a learner's academic record.
package delivery

import (
	"context"
	"strings"
)

// ResolveDeliveryType returns the Offering's delivery_type for the Assessment's
// parent offering, or "" when it is not resolvable.
//
// "" means GENUINELY UNATTRIBUTABLE, never a default. assessments.offering_id is
// nullable (migration 0033), so a freestanding assessment has no Offering and
// thus no mode; inventing one (say, "graduate") would write a fact into a
// learner's transcript that nothing in the system supports.
//
// FAIL-SOFT, deliberately: an unwired port, a blank id, a genuine miss, a dead
// read and an unrecognised value ALL yield "". delivery_type is metadata riding
// on a graded outcome; the score is the payload. No lookup failure here may cost
// a learner their grade, so this function returns no error and the caller has
// nothing to swallow. It is total by construction.
//
// The caller MUST have put the tenant on ctx (tracing.WithTenantID) before
// calling: OfferingPort.Get reads the RLS tenant from the context, not a
// parameter. That coupling stays at the adapter boundary so this stays pure.
func ResolveDeliveryType(ctx context.Context, offerings OfferingPort, a *Assessment) string {
	if offerings == nil || a == nil {
		return ""
	}
	offeringID := strings.TrimSpace(a.OfferingID)
	if offeringID == "" {
		// Freestanding assessment: no offering, no mode. Not an error, and not
		// worth a round-trip that could only answer spuriously.
		return ""
	}
	o, ok, err := offerings.Get(ctx, offeringID)
	if err != nil || !ok || o == nil {
		return ""
	}
	// offerings.delivery_type is bare TEXT with no database CHECK (migration
	// 0031) — the value set is a DOMAIN invariant. Emit only what the contract
	// recognises, so a garbage value cannot travel the wire and land in a
	// transcript as a "mode".
	if !o.DeliveryType.IsValid() {
		return ""
	}
	return string(o.DeliveryType)
}
