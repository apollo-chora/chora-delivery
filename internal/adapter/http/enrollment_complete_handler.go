// enrollment_complete_handler.go — the enrollment-completion DRIVER.
//
// Command: an explicit instructor/admin "mark enrolment complete" use-case.
// This is the authoritative, non-speculative driver for the per-learner fact
// chora.delivery.enrollment.completed.v1 (consumed by the LearnerProfile
// read-model per ADR-200 + familiar verified-EXP per ADR-203).
//
// Why a command (not an event-driven auto-subscriber): no existing completion
// SIGNAL maps cleanly to "this enrolment is finished". The nearest candidate,
// chora.delivery.submission.graded.v1, is per-ASSESSMENT (a course may carry
// many) with NO assessment->enrolment linkage in the model — wiring it would
// invent a speculative trigger, which the brief forbids. The instructor/admin
// sign-off is the real, auditable completion authority.
//
// Flow (mirrors handleV1EnrolmentCancel + the certification.issued emit):
//
//	load (RLS-scoped Get) -> domain Complete() -> persist (MarkCompleted)
//	-> emit PublishEnrollmentCompleted (outbox-atomic via the wired publisher).
//
// Emission is gated on the FIRST transition (Status was not already completed)
// so a redundant re-complete is a true no-op — no duplicate event, no
// redundant UPDATE. The event idempotency-key dedupes redelivery at the bus.
//
// Route exposure: mounted on the existing v1CoursesSubHandler dispatch as
//
//	POST /v1/courses/{id}/enrolments/{enrolment_id}/complete
//
// No chora-contracts file is touched here. The OpenAPI path + the
// enrollment.completed.v1 AsyncAPI event contract + the gateway allowlist are a
// SEPARATE contracts change (fan-out + checkpoint) — see the build report.
package httpapi

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// completeEnrolmentReq is the (optional) request body. `passed` distinguishes a
// completion that met the passing requirements from a completed-without-passing
// one; it defaults to false when the body is empty.
type completeEnrolmentReq struct {
	Passed bool `json:"passed"`
}

// handleV1EnrolmentComplete serves
// POST /v1/courses/{id}/enrolments/{enrolment_id}/complete.
func handleV1EnrolmentComplete(deps Deps, courseID, enrolmentID string, w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Tenant-Id")
	ctx := tracing.WithTenantID(r.Context(), tenantID)

	var req completeEnrolmentReq
	if err := decodeBody(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if deps.Enrollments == nil {
		writeError(w, http.StatusInternalServerError, "enrolments not wired")
		return
	}

	// The store MUST support completion persistence. Fail loud if not — a
	// missing capability is a wiring bug, never a silent no-op.
	completer, ok := deps.Enrollments.(domain.EnrollmentCompletionPort)
	if !ok {
		writeError(w, http.StatusInternalServerError, "enrolment store does not support completion")
		return
	}

	e, found, err := deps.Enrollments.Get(ctx, enrolmentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enrolment lookup failed")
		return
	}
	if !found || e.CourseID != courseID || e.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "enrolment not found")
		return
	}

	// Detect the first transition BEFORE the (idempotent) domain call so a
	// redundant re-complete emits/persists nothing.
	alreadyCompleted := e.Status == domain.EnrollmentStatusCompleted

	if err := e.Complete(req.Passed, time.Now().UTC()); err != nil {
		// Only ErrEnrollmentNotActive (cancelled) is reachable here.
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if !alreadyCompleted {
		if err := completer.MarkCompleted(ctx, e); err != nil {
			writeError(w, http.StatusInternalServerError, "persist completion failed")
			return
		}
		if deps.Publisher != nil {
			completedAt := time.Now().UTC()
			if e.CompletedAt != nil {
				completedAt = *e.CompletedAt
			}
			passed := e.Passed != nil && *e.Passed
			_, _ = deps.Publisher.PublishEnrollmentCompleted(events.EnrollmentCompleted{
				TenantID:     e.TenantID,
				GCID:         e.GCID,
				EnrollmentID: e.ID,
				CourseID:     e.CourseID,
				LearnerGCID:  e.GCID,
				Passed:       passed,
				CompletedAt:  completedAt,
				Traceparent:  r.Header.Get("traceparent"),
			})
		}
	}

	writeJSON(w, http.StatusOK, enrollmentDTO(e))
}
