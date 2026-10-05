// cohort_roster.go — Postgres adapter for delivery.CohortRoster (CHO-2153 /
// ADR-234): the roster facts behind assessment cohort authz.
//
// SCHEMA: `course_enrollments` (0001_initial.sql), `offerings` (0031), and
// `bookings` (0019) — ALL in chora_delivery. These are intra-domain reads, so
// the joins below are legal; no cross-DB query and no Pub/Sub round-trip is
// involved (ddd-enforcement #1).
//
// Every query runs inside RunInTx + rls.ApplySession. On a FORCE-RLS table a
// query without the tenant GUC does not error — it silently matches ZERO rows.
// For an authz gate that failure mode is catastrophic in the *safe* direction
// (deny everyone) rather than the unsafe one, but it would still be an outage
// masquerading as a denial, so the GUC is applied on every path.
package pg

import (
	"context"
	"errors"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
)

// SQLLearnerEnrolledInOffering — does the learner hold a live enrolment in at
// least ONE course of the offering?
//
// The offering's course set is `data->'CourseIDs'` (a JSONB array of UUID
// strings), UNIONed with the `course_id` column. Both are needed:
//
//   - `course_id` alone is NOT the full linkage. Offerings are multi-course and
//     the column carries only the first. Live: offering 019f3c77 spans courses
//     019eb059 + 019edf1c, so joining on `course_id` alone would lock out every
//     learner enrolled solely in 019edf1c.
//   - `CourseIDs` alone would strand any legacy offering whose `data` predates
//     the key. The UNION is the superset and is the safe predicate.
//
// EXISTS short-circuits on the first match; (course_id, gcid) is UNIQUE.
const SQLLearnerEnrolledInOffering = `
SELECT EXISTS (
  SELECT 1
  FROM offerings o
  CROSS JOIN LATERAL (
    SELECT o.course_id AS course_id
    UNION
    SELECT (jsonb_array_elements_text(COALESCE(o.data->'CourseIDs', '[]'::jsonb)))::uuid
  ) oc
  JOIN course_enrollments ce
    ON ce.course_id = oc.course_id
   AND ce.tenant_id = o.tenant_id
   AND ce.gcid      = $3
   AND ce.deleted_at IS NULL
  WHERE o.id = $2
    AND o.tenant_id = $1
    AND o.deleted_at IS NULL
)
`

// SQLLearnerBookedOnClass — does the learner hold a live booking on the class?
//
// Any non-soft-deleted booking counts, in any status (pending / confirmed /
// attended / no-show): each of those means "was rostered onto this class". A
// cancelled booking is soft-deleted and is therefore already excluded by the
// deleted_at guard — there is no CANCELLED status to filter.
const SQLLearnerBookedOnClass = `
SELECT EXISTS (
  SELECT 1
  FROM bookings
  WHERE tenant_id    = $1
    AND class_id     = $2
    AND learner_gcid = $3
    AND deleted_at IS NULL
)
`

// CohortRosterRepo is the pg adapter for delivery.CohortRoster.
type CohortRosterRepo struct {
	tx TxRunner
}

// NewCohortRosterRepo constructs the repo.
func NewCohortRosterRepo(tx TxRunner) *CohortRosterRepo {
	return &CohortRosterRepo{tx: tx}
}

// EnrolledInOffering implements delivery.CohortRoster.
//
// Errors are RETURNED, never folded into `false, nil`. A denial and a database
// outage are different things, and the caller must be able to tell them apart:
// it refuses either way, but only one of them is a bug worth paging about.
func (r *CohortRosterRepo) EnrolledInOffering(ctx context.Context, tenantID, offeringID, learnerGCID string) (bool, error) {
	if r == nil || r.tx == nil {
		return false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return false, errors.New("pg: tenantID required")
	}
	// Put the tenant on the context BEFORE RunInTx — rls.ApplySession reads it
	// from there, not from the SQL args. Every repo in this package does this;
	// omitting it does not merely skip the GUC, it hard-errors with "rls:
	// tenant_id missing on context". The HTTP layer hands us a bare
	// r.Context(), so the repo is where the tenant gets attached.
	ctx = tracing.WithTenantID(ctx, tenantID)

	var enrolled bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLLearnerEnrolledInOffering, tenantID, offeringID, learnerGCID)
		return row.Scan(&enrolled)
	})
	if err != nil {
		return false, err
	}
	return enrolled, nil
}

// BookedOnClass implements delivery.CohortRoster.
func (r *CohortRosterRepo) BookedOnClass(ctx context.Context, tenantID, classID, learnerGCID string) (bool, error) {
	if r == nil || r.tx == nil {
		return false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return false, errors.New("pg: tenantID required")
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var booked bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLLearnerBookedOnClass, tenantID, classID, learnerGCID)
		return row.Scan(&booked)
	})
	if err != nil {
		return false, err
	}
	return booked, nil
}
