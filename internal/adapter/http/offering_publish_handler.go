// offering_publish_handler.go — HTTP handlers for the R+ offering-nested
// Publish / Catalog-handoff surface (four-mode async delivery_type):
//
//	GET   /api/v1/offerings/{id}/publish  — catalogue visibility of attached courses
//	PATCH /api/v1/offerings/{id}/publish  — publish (make public) the attached
//	                                        courses that are LISTED in the catalogue
//
// "Publishing" a self-paced offering makes its attached course(s) publicly
// discoverable in the learner catalogue. The write is intra-chora_delivery
// (deps.Catalogue over the offering's CourseIDs); the cross-domain fan-out is
// the ALREADY-LIVE chora.delivery.course.published.v1 (chora-sharing discovery +
// chora-identity already subscribe) — NO new event/proto/subscriber, NO
// migration.
//
// BOUNDED + robust: an attached course that is NOT in the catalogue (never
// listed) is SKIPPED and reported `listed:false` — the tab never fabricates a
// catalogue entry (that happens in course authoring). So the handler is correct
// regardless of which attached courses have a PublicCourse row. Idempotent:
// SetVisibility returns a publish-signal ONLY on the transition INTO public, so
// re-publishing an already-public course emits NO duplicate event.
//
// Authorisation mirrors the offering surface: instructor / admin / training-admin
// (hasOfferingAdminRole) + X-Tenant-Id + gcid. GET + PATCH only (edge-safe:
// Cloud Armor rule 998 covers offerings PATCH); the dispatcher 405s otherwise.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// handleOfferingGetPublish — GET /api/v1/offerings/{id}/publish.
func handleOfferingGetPublish(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Catalogue == nil {
		writeError(w, http.StatusServiceUnavailable, "catalogue repo not wired")
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
	courses := make([]map[string]interface{}, 0, len(o.CourseIDs))
	for _, courseID := range o.CourseIDs {
		row, err := offeringPublishRow(ctx, deps, tenantID, courseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "catalogue lookup failed: "+err.Error())
			return
		}
		courses = append(courses, row)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"courses": courses})
}

// handleOfferingPublish — PATCH /api/v1/offerings/{id}/publish.
func handleOfferingPublish(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Catalogue == nil {
		writeError(w, http.StatusServiceUnavailable, "catalogue repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	// B2.2 — cross-tenant PUBLIC exposure is a higher bar than in-tenant offering
	// admin: a bare instructor may build/curate a course but may NOT expose it
	// beyond their tenant. admin / training-admin / tenant_admin only.
	if !hasCrossTenantPublishRole(r) {
		writeError(w, http.StatusForbidden, "cross-tenant publish requires admin / training-admin")
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
	courses := make([]map[string]interface{}, 0, len(o.CourseIDs))
	publishedCount := 0
	for _, courseID := range o.CourseIDs {
		pc, found, err := deps.Catalogue.Get(ctx, courseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "catalogue lookup failed: "+err.Error())
			return
		}
		if !found || pc == nil {
			// Not in the catalogue — cannot publish from here; report honestly.
			courses = append(courses, publishRowFromTitle(deps, ctx, tenantID, courseID))
			continue
		}
		// B2.1 — a course may only be flipped cross-tenant PUBLIC when its CJ#2
		// authoring lifecycle is PUBLISHED. Prevents a DRAFT/AWAITING course from
		// leaking into the cross-tenant catalogue via the publish path (the
		// public flag and the state FSM were previously decoupled). Only gates
		// the TRANSITION into public; an already-public course is left untouched
		// (idempotent). Fail-closed: an unknown CJ#2 course refuses the flip.
		if pc.Visibility != delivery.VisibilityPublic &&
			!offeringPublishStateAllows(ctx, deps, tenantID, courseID) {
			courses = append(courses, map[string]interface{}{
				"id":             courseID,
				"title":          pc.Title,
				"visibility":     string(pc.Visibility),
				"published":      false,
				"listed":         true,
				"blocked_reason": "course must be in PUBLISHED state before cross-tenant publish",
			})
			continue
		}
		signal := pc.SetVisibility(delivery.VisibilityPublic)
		pc.UpdatedAt = time.Now().UTC()
		if err := deps.Catalogue.Save(ctx, pc); err != nil {
			writeError(w, http.StatusInternalServerError, "catalogue write failed: "+err.Error())
			return
		}
		if signal {
			publishedCount++
			// Reuse the LIVE course.published.v1 fan-out (sharing + identity).
			if deps.Publisher != nil {
				_, _ = deps.Publisher.PublishCoursePublished(events.CoursePublished{
					TenantID:       pc.TenantID,
					GCID:           gcid,
					CourseID:       pc.ID,
					Title:          pc.Title,
					InstructorGCID: pc.InstructorGCID,
					Traceparent:    r.Header.Get("traceparent"),
				})
			}
		}
		courses = append(courses, map[string]interface{}{
			"id":         courseID,
			"title":      pc.Title,
			"visibility": string(pc.Visibility),
			"published":  pc.Visibility == delivery.VisibilityPublic,
			"listed":     true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"courses":         courses,
		"published_count": publishedCount,
	})
}

// offeringPublishRow builds the per-course status row for the GET view: title
// (catalogue's copy, else the CJ#2 course title if that port is wired),
// visibility, published + listed flags.
func offeringPublishRow(ctx context.Context, deps Deps, tenantID, courseID string) (map[string]interface{}, error) {
	pc, found, err := deps.Catalogue.Get(ctx, courseID)
	if err != nil {
		return nil, err
	}
	if found && pc != nil {
		return map[string]interface{}{
			"id":         courseID,
			"title":      pc.Title,
			"visibility": string(pc.Visibility),
			"published":  pc.Visibility == delivery.VisibilityPublic,
			"listed":     true,
		}, nil
	}
	return publishRowFromTitle(deps, ctx, tenantID, courseID), nil
}

// publishRowFromTitle builds a "not listed" row, enriching the title from the
// CJ#2 course port when it is wired (optional — an unlisted course has no
// catalogue title). Errors resolving the title degrade to an empty title (the
// row is still honest: listed:false).
func publishRowFromTitle(deps Deps, ctx context.Context, tenantID, courseID string) map[string]interface{} {
	title := ""
	if deps.CourseCJ2 != nil && deps.CourseCJ2.Courses != nil {
		if c, ok, err := deps.CourseCJ2.Courses.Get(ctx, tenantID, courseID); err == nil && ok && c != nil {
			title = c.Title
		}
	}
	return map[string]interface{}{
		"id":         courseID,
		"title":      title,
		"visibility": "",
		"published":  false,
		"listed":     false,
	}
}

// hasCrossTenantPublishRole reports whether the caller may flip a course to
// cross-tenant PUBLIC (B2.2). A higher bar than hasOfferingAdminRole — a bare
// instructor is deliberately excluded; admin / training-admin / tenant_admin
// only, since public exposure crosses the tenant boundary.
func hasCrossTenantPublishRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleAdmin, roleTrainingAdmin, roleTenantAdmin)
}

// offeringPublishStateAllows reports whether the course's CJ#2 authoring
// lifecycle permits a cross-tenant public flip (B2.1) — true only when the
// state is PUBLISHED. When the CJ#2 course port is unwired (unit harnesses) the
// gate is not enforceable and returns true (production always wires the port).
// A wired-but-missing course fails CLOSED (refuses the flip).
func offeringPublishStateAllows(ctx context.Context, deps Deps, tenantID, courseID string) bool {
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Courses == nil {
		return true
	}
	c, ok, err := deps.CourseCJ2.Courses.Get(ctx, tenantID, courseID)
	if err != nil || !ok || c == nil {
		return false
	}
	return c.State == delivery.CourseStatePublished
}
