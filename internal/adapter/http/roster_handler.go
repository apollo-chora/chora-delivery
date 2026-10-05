// roster_handler.go — GET /api/v1/rosters/{courseId} — R+ M4
// course-centric Roster READ VIEW (CourseRoster aggregate).
//
// Distinct from the existing class-centric class-roster screen wired at the
// older /api/v1/classes/{id}/roster surface. This handler serves the
// course-scoped projection (rostering.CourseRoster) that joins the canonical
// EnrollmentPort membership set with two TODO upstream projections (display
// name from chora-identity, progress proxy from chora-consumption). Both
// projections fall back to GCID + 0 today per `feedback_no_stubs_real_wiring`
// — the FE renders those visibly so the unwired-projection state is observable
// rather than hidden behind faked data.
//
// Pinned wire invariants (mirrored from roster_handler_test.go):
//
//   - 400 missing X-Tenant-Id is enforced by the tenantRequired middleware
//     master wires around this handler in handlers.go (we do NOT re-check
//     X-Tenant-Id here — middleware short-circuits before dispatch).
//   - 404 when the {courseId} path segment is empty (bare GET to
//     /api/v1/rosters/ with no leaf).
//   - 405 for any method other than GET — write paths live elsewhere
//     (enrolment domain owns mutations).
//   - 200 + `{"course_id": "...", "learners": []}` when the course has zero
//     enrolments (empty array, NOT null, NOT a placeholder).
//   - 200 + populated learners[] when enrolments exist for the
//     (tenantID, courseID) tuple; cross-tenant + cross-course rows MUST be
//     filtered (defence in depth — repo + adapter already filter, the
//     handler shape is what the FE binds to).
//
// Response shape per the FE contract:
//
//	{
//	  "course_id":   "<UUIDv7>",
//	  "tenant_id":   "<UUIDv7>",            // echoed for FE audit display
//	  "learners":    [ { gcid, display_name, progress_pct, enrolled_at } ],
//	  "learner_count": <int>                 // convenience aggregate
//	}
//
// Hexagonal: ADAPTER. This file depends on the domain `rostering` package
// (CourseRoster + RosterLearner + CourseRosterRepo port). Domain code never
// imports this file. The repo is injected as an explicit interface arg —
// see rostersSubHandler doc for the master-integration wiring pattern.
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

// rostersSubHandler dispatches /api/v1/rosters/{courseId}.
//
// Method dispatch lives inside so non-GET methods yield a clean 405 instead
// of falling through to the next mux entry. Sub-resources are not currently
// supported — any path beyond /{courseId} 404s so future endpoints fail
// loud instead of silently 200-ing the wrong shape.
//
// Takes the CourseRosterRepo as an explicit interface argument (rather than
// the whole Deps struct) so that:
//
//  1. The handler is unit-testable in isolation against any
//     rostering.CourseRosterRepo implementation (in-memory adapter today,
//     pg-backed adapter once chora_delivery materialised-view ships).
//
//  2. The handler can be wired BEFORE the `Rosters` field lands on Deps in
//     handlers.go — the master integration commit later wires this as:
//
//     mux.HandleFunc("/api/v1/rosters/", logging(tenantRequired(
//     rostersSubHandler(deps.Rosters))))
//     mux.HandleFunc("/api/v1/rosters",  logging(tenantRequired(
//     rostersSubHandler(deps.Rosters))))
//
//     (paired exact + subtree so the bare collection 404s cleanly via the
//     leaf-extraction guard below; this surface is course-scoped, there is
//     no list-all).
//
// Per `feedback_no_stubs_real_wiring`: a nil repo argument is a boot-time
// wiring bug — the handler 503s loud rather than fake an empty roster.
func rostersSubHandler(rosters rostering.CourseRosterRepo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Path leaf extraction. Empty leaf or nested sub-resource → 404.
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/rosters/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" || strings.Contains(rest, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		courseID := rest

		// Method dispatch. Only GET is supported on this READ VIEW; writes
		// to the enrolment membership set go through the enrolment endpoints
		// in v1_handlers.go.
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if rosters == nil {
			// Fail loud per `feedback_no_stubs_real_wiring`: a missing
			// repo is a wiring bug, not a "no learners" surface — the
			// FE must NEVER see a fake empty array because of unwired
			// boot.
			writeError(w, http.StatusServiceUnavailable, "rosters repo not wired")
			return
		}

		// tenantRequired middleware has already validated presence; the
		// header read here cannot be empty. Trim for defence in depth.
		tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))

		// Thread the tenant onto ctx so the pg-backed CourseRosterRepo can
		// SET LOCAL chora.tenant_id for its RLS SELECT (the in-mem adapter
		// ignores it). Mirrors the exam/applications-admin handler pattern.
		ctx := tracing.WithTenantID(r.Context(), tenantID)

		roster, err := rosters.ListByCourse(ctx, tenantID, courseID)
		if err != nil {
			// Bubble repo errors as 5xx so the real cause surfaces in
			// logs rather than masquerading as an empty roster.
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, courseRosterDTO(roster))
	}
}

// courseRosterDTO renders a CourseRoster as the JSON shape the FE binds to.
//
// Critical invariant: `learners` is always a non-nil slice so JSON marshalling
// yields `[]` (not `null`) for empty courses — the wire contract demands an
// empty array as the canonical "no learners" representation, NOT a placeholder.
// The domain constructor (rostering.NewCourseRoster) initialises the field
// to a zero-length slice; we defend in depth here in case a future call site
// constructs a CourseRoster directly without going through the constructor.
func courseRosterDTO(r *rostering.CourseRoster) map[string]interface{} {
	learners := make([]map[string]interface{}, 0, len(r.Learners))
	for _, l := range r.Learners {
		learners = append(learners, map[string]interface{}{
			"gcid":         l.GCID,
			"display_name": l.DisplayName,
			"progress_pct": l.ProgressPct,
			"enrolled_at":  l.EnrolledAt.UTC().Format(time.RFC3339),
		})
	}
	return map[string]interface{}{
		"course_id":     r.CourseID,
		"tenant_id":     r.TenantID,
		"learners":      learners,
		"learner_count": len(r.Learners),
	}
}
