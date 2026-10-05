// offering_roster_bulk_handler.go - R+ ATOMIC roster bulk-enrol surface.
//
// Endpoint (dispatched from offeringsSubHandler in offering_handler.go):
//
//	POST /api/v1/offerings/{id}/roster/bulk
//	     body { "course_id": uuid, "gcids": [uuid, ...] }
//
// Enrols MANY learners into one of the offering's attached courses in a SINGLE
// all-or-nothing request - the batch sibling of the single-learner
// handleOfferingEnroll. It mirrors that handler's gates (wiring 503, tenant 400
// / gcid 401, admin 403, offering 404, course-attached 400, per-course capacity
// 409) but applies the capacity check against the COUNT of NEW learners in the
// batch, and enrols the whole batch atomically.
//
// Two enrol paths, chosen by what Enrollments is wired to:
//
//   - TRANSACTIONAL (production pg.EnrollmentRepo, which satisfies bulkEnroller,
//     with EnrollmentTxTee wired): RegisterBulk inserts every enrolment row AND
//     writes every learner's chora.delivery.enrollment.created.v1 outbox row
//     inside ONE transaction - a true transactional outbox. A mid-batch failure
//     rolls the whole batch back (no enrolment, no partial event).
//   - FALLBACK (dev / in-mem, no tx to tee onto): per-learner Register + an
//     after-commit publish for the NEW enrolments (matching the single path).
//
// Intra-chora_delivery only (Offering + Course + Enrollment share the DB) - NO
// cross-DB query, NO new aggregate, NO migration (reuses course_enrollments +
// outbox_events). The Course still owns enrolment; this endpoint is a validated
// proxy over the canonical EnrollmentPort, exactly like handleOfferingEnroll.
package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// bulkEnroller is the transactional bulk-enrol capability. The production
// pg.EnrollmentRepo satisfies it; the in-mem store does not (⇒ fallback). Kept
// as a structural interface with a raw func param so httpapi need not import the
// repo/pg package.
type bulkEnroller interface {
	RegisterBulk(
		ctx context.Context, tenantID, courseID string, gcids []string,
		onInserted func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *domain.Enrollment) error,
	) ([]*domain.Enrollment, error)
}

// BulkEnrollTee writes a new learner's enrollment.created.v1 outbox row on a
// caller-supplied transaction (the true transactional outbox). deliveryoutbox's
// *BulkEnrollTxTee satisfies it structurally, so httpapi does not import the
// outbox adapter. Wired into Deps.EnrollmentTxTee in cmd/server.
type BulkEnrollTee interface {
	RecordEnrollmentCreated(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, in events.EnrollmentCreated) error
}

// bulkEnrollOfferingReq is the POST /roster/bulk body.
type bulkEnrollOfferingReq struct {
	CourseID string   `json:"course_id"`
	GCIDs    []string `json:"gcids"`
}

// handleOfferingBulkEnroll - POST /api/v1/offerings/{id}/roster/bulk.
func handleOfferingBulkEnroll(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Enrollments == nil {
		writeError(w, http.StatusServiceUnavailable, "enrollments repo not wired")
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
	var req bulkEnrollOfferingReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	gcids, err := normalizeBulkGCIDs(req.GCIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(gcids) == 0 {
		writeError(w, http.StatusBadRequest, "gcids required")
		return
	}

	// Which learners are NEW? Only NEW enrolments count against capacity and
	// only they emit an event (mirrors the single path's `!existed` guard).
	newGCIDs := make([]string, 0, len(gcids))
	for _, g := range gcids {
		_, existed, gerr := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, req.CourseID, g)
		if gerr != nil {
			writeError(w, http.StatusInternalServerError, "enrolment lookup failed: "+gerr.Error())
			return
		}
		if !existed {
			newGCIDs = append(newGCIDs, g)
		}
	}

	// Capacity gate - the batch of NEW enrolments must fit remaining capacity
	// (0 = unbounded). Re-enrolments of already-active learners never block.
	if o.Capacity > 0 && len(newGCIDs) > 0 {
		count, cerr := deps.Enrollments.CountByCourse(ctx, tenantID, req.CourseID)
		if cerr != nil {
			writeError(w, http.StatusInternalServerError, "enrolment count failed: "+cerr.Error())
			return
		}
		if count+len(newGCIDs) > o.Capacity {
			writeError(w, http.StatusConflict, "offering is at capacity for this batch")
			return
		}
	}

	traceparent := r.Header.Get("traceparent")
	insertedIDs := make([]string, 0, len(gcids))

	if be, ok := deps.Enrollments.(bulkEnroller); ok && deps.EnrollmentTxTee != nil {
		// TRUE transactional outbox: enrolment rows + enrollment.created rows in
		// ONE tx. The tee writes each new learner's outbox row on the same tx.
		onInserted := func(ctx context.Context, exec func(ctx context.Context, query string, args ...any) error, e *domain.Enrollment) error {
			return deps.EnrollmentTxTee.RecordEnrollmentCreated(ctx, exec, events.EnrollmentCreated{
				TenantID:     e.TenantID,
				GCID:         e.GCID,
				EnrollmentID: e.ID,
				CourseID:     e.CourseID,
				LearnerGCID:  e.GCID,
				Traceparent:  traceparent,
			})
		}
		ins, rerr := be.RegisterBulk(ctx, tenantID, req.CourseID, gcids, onInserted)
		if rerr != nil {
			writeError(w, http.StatusInternalServerError, "bulk enrol failed: "+rerr.Error())
			return
		}
		for _, e := range ins {
			insertedIDs = append(insertedIDs, e.ID)
		}
	} else {
		// Fallback (no transaction to tee onto): per-learner Register, then an
		// after-commit publish for the NEW enrolments. WEAKER guarantee - the
		// event is not committed atomically with the row - but the in-mem store
		// has no durable outbox, so this matches the single path exactly.
		for _, g := range gcids {
			_, existed, gerr := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, req.CourseID, g)
			if gerr != nil {
				writeError(w, http.StatusInternalServerError, "enrolment lookup failed: "+gerr.Error())
				return
			}
			e, eerr := deps.Enrollments.Register(ctx, tenantID, req.CourseID, g)
			if eerr != nil {
				writeError(w, http.StatusInternalServerError, "enrol failed: "+eerr.Error())
				return
			}
			if existed {
				continue
			}
			insertedIDs = append(insertedIDs, e.ID)
			if deps.Publisher != nil {
				if _, perr := deps.Publisher.PublishEnrollmentCreated(events.EnrollmentCreated{
					TenantID:     e.TenantID,
					GCID:         e.GCID,
					EnrollmentID: e.ID,
					CourseID:     e.CourseID,
					LearnerGCID:  e.GCID,
					Traceparent:  traceparent,
				}); perr != nil {
					log.Printf("delivery: roster bulk enrol: publish %s failed course=%s gcid=%s err=%v",
						events.TopicEnrollmentCreated, e.CourseID, e.GCID, perr)
				}
			}
		}
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"course_id":       req.CourseID,
		"requested_count": len(gcids),
		"inserted_count":  len(insertedIDs),
		"enrollment_ids":  insertedIDs,
	})
}

// normalizeBulkGCIDs trims + de-duplicates (order-preserving) the requested
// GCIDs and rejects a blank entry (fail-loud - a blank GCID cannot be enrolled).
func normalizeBulkGCIDs(gcids []string) ([]string, error) {
	out := make([]string, 0, len(gcids))
	seen := make(map[string]struct{}, len(gcids))
	for _, g := range gcids {
		g = strings.TrimSpace(g)
		if g == "" {
			return nil, errors.New("gcids must not contain a blank entry")
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return out, nil
}
