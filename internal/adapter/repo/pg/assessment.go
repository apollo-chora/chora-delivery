// assessment.go — pg adapter for the Assessment aggregate per ADR-155.
//
// Mirrors application.go conventions:
//   - Minimal Querier / TxRunner shims (pgx not imported here)
//   - Every read/write wraps in rls.ApplySession(ctx, tx) before queries
//   - Soft-delete-aware reads (WHERE deleted_at IS NULL)
//   - UPSERT-on-conflict for idempotent Save semantics
//
// Cross-DB queries FORBIDDEN — only reads chora_delivery.assessments +
// chora_delivery.submissions (per ddd-enforcement).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// SQLUpsertAssessment — INSERT with ON CONFLICT DO UPDATE on assessment_id.
// All mutable fields are bound in one shot for idempotent re-Save.
const SQLUpsertAssessment = `
INSERT INTO assessments (
    assessment_id, tenant_id, instructor_gcid, test_set_id,
    test_set_revision_snapshot, class_id, invited_gcids, title,
    learner_facing_name, state, scheduled_open_at, scheduled_close_at,
    max_attempts, shuffle_questions, shuffle_mcq_options,
    accommodations_jsonb, total_points, question_count,
    grading_config_snapshot, release_announcement,
    published_at, closed_at, results_released_at, archived_at,
    deleted_at, created_at, updated_at, offering_id
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8,
    $9, $10, $11, $12,
    $13, $14, $15,
    $16, $17, $18,
    $19, $20,
    $21, $22, $23, $24,
    $25, $26, $27, $28
)
ON CONFLICT (assessment_id) DO UPDATE SET
    test_set_revision_snapshot = EXCLUDED.test_set_revision_snapshot,
    class_id                   = EXCLUDED.class_id,
    offering_id                = EXCLUDED.offering_id,
    invited_gcids              = EXCLUDED.invited_gcids,
    title                      = EXCLUDED.title,
    learner_facing_name        = EXCLUDED.learner_facing_name,
    state                      = EXCLUDED.state,
    scheduled_open_at          = EXCLUDED.scheduled_open_at,
    scheduled_close_at         = EXCLUDED.scheduled_close_at,
    max_attempts               = EXCLUDED.max_attempts,
    shuffle_questions          = EXCLUDED.shuffle_questions,
    shuffle_mcq_options        = EXCLUDED.shuffle_mcq_options,
    accommodations_jsonb       = EXCLUDED.accommodations_jsonb,
    total_points               = EXCLUDED.total_points,
    question_count             = EXCLUDED.question_count,
    grading_config_snapshot    = EXCLUDED.grading_config_snapshot,
    release_announcement       = EXCLUDED.release_announcement,
    published_at               = EXCLUDED.published_at,
    closed_at                  = EXCLUDED.closed_at,
    results_released_at        = EXCLUDED.results_released_at,
    archived_at                = EXCLUDED.archived_at,
    deleted_at                 = EXCLUDED.deleted_at,
    updated_at                 = EXCLUDED.updated_at
`

const assessmentSelectCols = `
    assessment_id, tenant_id, instructor_gcid, test_set_id,
    test_set_revision_snapshot, class_id, invited_gcids, title,
    learner_facing_name, state, scheduled_open_at, scheduled_close_at,
    max_attempts, shuffle_questions, shuffle_mcq_options,
    accommodations_jsonb, total_points, question_count,
    grading_config_snapshot, release_announcement,
    published_at, closed_at, results_released_at, archived_at,
    deleted_at, created_at, updated_at, offering_id
`

// SQLSelectAssessmentByID returns one assessment.
const SQLSelectAssessmentByID = `
SELECT ` + assessmentSelectCols + `
FROM assessments
WHERE assessment_id = $1
  AND tenant_id = $2
  AND deleted_at IS NULL
`

// SQLListAssessmentsByInstructor — instructor's authored, descending by created_at.
const SQLListAssessmentsByInstructor = `
SELECT ` + assessmentSelectCols + `
FROM assessments
WHERE tenant_id = $1
  AND instructor_gcid = $2
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $3
`

// SQLListAssessmentsByOffering — offering-scoped, descending by created_at
// (W3.A — making an Offering's assessments addressable). Mirrors
// SQLListAssessmentsByInstructor; tenant + RLS scoped, soft-delete-filtered.
const SQLListAssessmentsByOffering = `
SELECT ` + assessmentSelectCols + `
FROM assessments
WHERE tenant_id = $1
  AND offering_id = $2
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $3
`

// SQLListAssessmentsVisibleToLearner — cohort scope per ADR-155 §D9 **as
// amended by ADR-234** (CHO-2153).
//
// This predicate MUST stay decision-identical to
// delivery.Assessment.IsLearnerEligible. The two encode the same rule in
// different languages, and a divergence is invisible to any test that exercises
// only one side — so TestIntegration_CohortRule_SQLMatchesDomain drives the
// whole cohort × roster matrix through BOTH and fails if they ever disagree.
//
//	OPEN_LINK, offering-attached → enrolled in ≥1 course of the offering
//	OPEN_LINK, freestanding      → any tenant member (no cohort to scope to)
//	CLASS_BOUND                  → on the class roster (bookings)
//	EXPLICIT                     → on invited_gcids
//	INTERSECTION                 → on invited_gcids AND on the class roster
//
// What it replaced was:
//
//	(cardinality(invited_gcids) = 0 AND class_id IS NULL) OR ($2 = ANY(invited_gcids))
//
// — which had NO enrolment join at all, so its open-link branch meant "any
// member of the tenant", and which matched CLASS_BOUND under neither branch, so
// a class-bound assessment was invisible even to its own rostered class. It was
// wrong in both directions at once.
//
// State filter: SCHEDULED..RELEASED only (excludes DRAFT + ARCHIVED).
const SQLListAssessmentsVisibleToLearner = `
SELECT ` + assessmentSelectCols + `
FROM assessments a
WHERE a.tenant_id = $1
  AND a.deleted_at IS NULL
  AND a.state IN ('SCHEDULED', 'OPEN', 'CLOSED', 'GRADING', 'GRADED', 'RELEASED')
  AND (
    CASE
      -- EXPLICIT: the invite list alone.
      WHEN COALESCE(cardinality(a.invited_gcids), 0) > 0 AND a.class_id IS NULL
        THEN $2 = ANY(a.invited_gcids)

      -- INTERSECTION: on the invite list AND on the class roster.
      WHEN COALESCE(cardinality(a.invited_gcids), 0) > 0 AND a.class_id IS NOT NULL
        THEN $2 = ANY(a.invited_gcids) AND EXISTS (
               SELECT 1 FROM bookings b
               WHERE b.tenant_id = a.tenant_id
                 AND b.class_id = a.class_id
                 AND b.learner_gcid = $2
                 AND b.deleted_at IS NULL
             )

      -- CLASS_BOUND: the class roster.
      WHEN a.class_id IS NOT NULL
        THEN EXISTS (
               SELECT 1 FROM bookings b
               WHERE b.tenant_id = a.tenant_id
                 AND b.class_id = a.class_id
                 AND b.learner_gcid = $2
                 AND b.deleted_at IS NULL
             )

      -- OPEN_LINK, offering-attached: enrolled in ≥1 course of the offering.
      -- The course set is data->'CourseIDs' UNION course_id — offerings are
      -- multi-course and the column carries only the first.
      WHEN a.offering_id IS NOT NULL
        THEN EXISTS (
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
                AND ce.gcid      = $2
                AND ce.deleted_at IS NULL
               WHERE o.id = a.offering_id
                 AND o.tenant_id = a.tenant_id
                 AND o.deleted_at IS NULL
             )

      -- OPEN_LINK, freestanding: no offering, no class, no invites ⇒ no cohort
      -- to scope to. Tenant-open by construction (ADR-234: discouraged).
      ELSE TRUE
    END
  )
ORDER BY a.created_at DESC
LIMIT $3
`

// SQLReleaseAllGradedSubmissions — atomic visibility flip per ADR-155 §D9.
//
// ADR-172 §D6 HITL gate: a submission whose OE answers were AI-graded carries
// review_status='PENDING_REVIEW' until an instructor approves it. Such
// submissions are NOT released here — only review_status NULL (MCQ-only, no
// gate) or 'APPROVED' (instructor signed off) flip to RELEASED.
const SQLReleaseAllGradedSubmissions = `
UPDATE submissions
SET state = 'RELEASED', released_at = now(), updated_at = now()
WHERE assessment_id = $1
  AND tenant_id = $2
  AND state = 'GRADED_PENDING_RELEASE'
  AND (review_status IS NULL OR review_status = 'APPROVED')
  AND deleted_at IS NULL
RETURNING submission_id
`

// AssessmentRepo is the pg adapter.
type AssessmentRepo struct {
	tx TxRunner
}

// NewAssessmentRepo constructs the repo.
func NewAssessmentRepo(tx TxRunner) *AssessmentRepo {
	return &AssessmentRepo{tx: tx}
}

// Save UPSERTs the assessment within an RLS-scoped tx.
func (r *AssessmentRepo) Save(ctx context.Context, a *domain.Assessment) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if a == nil {
		return errors.New("pg: assessment is nil")
	}
	if strings.TrimSpace(a.TenantID) == "" {
		return errors.New("pg: assessment.TenantID required")
	}
	ctx = tracing.WithTenantID(ctx, a.TenantID)

	accomJSON, _ := json.Marshal(a.Accommodations)
	gcfgJSON, _ := json.Marshal(a.GradingConfigSnapshot)
	if a.Accommodations == nil {
		accomJSON = nil
	}

	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, SQLUpsertAssessment,
			a.ID, a.TenantID, a.InstructorGCID, a.TestSetID,
			a.TestSetRevisionSnapshot, nullString(a.ClassID),
			invitedGCIDsArray(a.InvitedGCIDs), a.Title,
			nullString(a.LearnerFacingName), string(a.State),
			nullTime(a.ScheduledOpenAt), nullTime(a.ScheduledCloseAt),
			a.MaxAttempts, a.ShuffleQuestions, a.ShuffleMCQOptions,
			nullBytes(accomJSON), a.TotalPoints, a.QuestionCount,
			gcfgJSON, nullString(a.ReleaseAnnouncement),
			nullTimePtr(a.PublishedAt), nullTimePtr(a.ClosedAt),
			nullTimePtr(a.ResultsReleasedAt), nullTimePtr(a.ArchivedAt),
			nullTimePtr(a.DeletedAt), a.CreatedAt, a.UpdatedAt,
			nullString(a.OfferingID),
		)
		if err != nil {
			return fmt.Errorf("pg: upsert assessment: %w", err)
		}
		return nil
	})
}

// Get reads one assessment by id.
func (r *AssessmentRepo) Get(ctx context.Context, tenantID, id string) (*domain.Assessment, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, false, errors.New("pg: tenantID required")
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var found *domain.Assessment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectAssessmentByID, id, tenantID)
		a, scanErr := scanAssessmentRow(row.Scan)
		if scanErr != nil {
			if errors.Is(scanErr, errNotFound) {
				return nil
			}
			return scanErr
		}
		found = a
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return found, found != nil, nil
}

// ListByInstructor returns the instructor's assessments.
func (r *AssessmentRepo) ListByInstructor(ctx context.Context, tenantID, instructorGCID string, pageSize int, _ string) ([]*domain.Assessment, string, error) {
	if r == nil || r.tx == nil {
		return nil, "", ErrNotImplemented
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var items []*domain.Assessment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLListAssessmentsByInstructor, tenantID, instructorGCID, pageSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			a, scanErr := scanAssessmentRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	return items, "", nil
}

// ListByOffering returns the offering's assessments (W3.A). Mirrors
// ListByInstructor: tenant + RLS scoped, soft-delete-filtered, created_at DESC.
func (r *AssessmentRepo) ListByOffering(ctx context.Context, tenantID, offeringID string, pageSize int, _ string) ([]*domain.Assessment, string, error) {
	if r == nil || r.tx == nil {
		return nil, "", ErrNotImplemented
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var items []*domain.Assessment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLListAssessmentsByOffering, tenantID, offeringID, pageSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			a, scanErr := scanAssessmentRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	return items, "", nil
}

// ListVisibleToLearner returns assessments the learner can take.
func (r *AssessmentRepo) ListVisibleToLearner(ctx context.Context, tenantID, learnerGCID string, pageSize int, _ string) ([]*domain.Assessment, string, error) {
	if r == nil || r.tx == nil {
		return nil, "", ErrNotImplemented
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var items []*domain.Assessment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLListAssessmentsVisibleToLearner, tenantID, learnerGCID, pageSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			a, scanErr := scanAssessmentRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	return items, "", nil
}

// MonitorCounts computes dashboard counts via aggregation.
func (r *AssessmentRepo) MonitorCounts(ctx context.Context, tenantID, assessmentID string) (domain.AssessmentMonitorCounts, error) {
	if r == nil || r.tx == nil {
		return domain.AssessmentMonitorCounts{}, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	const SQLMonitor = `
SELECT
    coalesce(count(*) FILTER (WHERE state IN ('STARTED','IN_PROGRESS')), 0)        AS in_progress,
    coalesce(count(*) FILTER (WHERE state IN ('STARTED','IN_PROGRESS','SUBMITTED','PENDING_OE_GRADING','GRADED_PENDING_RELEASE','RELEASED')), 0) AS started,
    coalesce(count(*) FILTER (WHERE state IN ('SUBMITTED','PENDING_OE_GRADING','GRADED_PENDING_RELEASE','RELEASED')), 0) AS submitted,
    coalesce(count(*) FILTER (WHERE state IN ('GRADED_PENDING_RELEASE','RELEASED')), 0) AS graded,
    coalesce(count(*) FILTER (WHERE state = 'RELEASED'), 0) AS released,
    coalesce(avg(
        CASE WHEN state IN ('GRADED_PENDING_RELEASE','RELEASED') AND max_score > 0
             THEN (total_score / max_score) * 100
             ELSE NULL END
    ), 0) AS avg_score_pct,
    coalesce(count(*) FILTER (WHERE state IN ('GRADED_PENDING_RELEASE','RELEASED')), 0) > 0 AS has_graded
FROM submissions
WHERE tenant_id = $1
  AND assessment_id = $2
  AND deleted_at IS NULL
`
	const SQLInvited = `SELECT cardinality(invited_gcids) FROM assessments WHERE assessment_id=$1 AND tenant_id=$2 AND deleted_at IS NULL`

	out := domain.AssessmentMonitorCounts{}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLMonitor, tenantID, assessmentID)
		var avgScore float64
		if err := row.Scan(
			&out.InProgressCount, &out.TotalStarted, &out.TotalSubmitted,
			&out.TotalGraded, &out.TotalReleased, &avgScore, &out.HasGradedSamples,
		); err != nil {
			return err
		}
		if out.HasGradedSamples {
			out.AverageScorePct = avgScore
		}
		invRow := q.QueryRow(ctx, SQLInvited, assessmentID, tenantID)
		_ = invRow.Scan(&out.TotalInvited)
		return nil
	})
	if err != nil {
		return domain.AssessmentMonitorCounts{}, err
	}
	return out, nil
}

// ReleaseAllSubmissions atomically flips GRADED_PENDING_RELEASE → RELEASED.
func (r *AssessmentRepo) ReleaseAllSubmissions(ctx context.Context, tenantID, assessmentID string) ([]string, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	released := []string{}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLReleaseAllGradedSubmissions, assessmentID, tenantID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				return scanErr
			}
			released = append(released, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return released, nil
}

// -----------------------------------------------------------------------------
// Row scanning
// -----------------------------------------------------------------------------

var errNotFound = errors.New("pg: not found")

func scanAssessmentRow(scan func(...any) error) (*domain.Assessment, error) {
	var (
		id, tenantID, instructorGCID, testSetID                  string
		revSnapshot                                              int
		classID                                                  sqlNullString
		offeringID                                               sqlNullString
		invitedRaw                                               uuidArray
		title                                                    string
		learnerName                                              sqlNullString
		state                                                    string
		schedOpen, schedClose                                    sqlNullTime
		maxAttempts                                              int
		shuffleQ, shuffleMCQ                                     bool
		accomRaw                                                 []byte
		totalPoints, questionCount                               int
		gcfgRaw                                                  []byte
		releaseAnn                                               sqlNullString
		publishedAt, closedAt, releasedAt, archivedAt, deletedAt sqlNullTime
		createdAt, updatedAt                                     time.Time
	)
	if err := scan(
		&id, &tenantID, &instructorGCID, &testSetID,
		&revSnapshot, &classID, &invitedRaw, &title,
		&learnerName, &state, &schedOpen, &schedClose,
		&maxAttempts, &shuffleQ, &shuffleMCQ,
		&accomRaw, &totalPoints, &questionCount,
		&gcfgRaw, &releaseAnn,
		&publishedAt, &closedAt, &releasedAt, &archivedAt,
		&deletedAt, &createdAt, &updatedAt, &offeringID,
	); err != nil {
		if isNoRows(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	a := &domain.Assessment{
		ID:                      id,
		TenantID:                tenantID,
		InstructorGCID:          instructorGCID,
		TestSetID:               testSetID,
		TestSetRevisionSnapshot: revSnapshot,
		ClassID:                 classID.String,
		OfferingID:              offeringID.String,
		InvitedGCIDs:            []string(invitedRaw),
		Title:                   title,
		LearnerFacingName:       learnerName.String,
		State:                   domain.AssessmentState(state),
		ScheduledOpenAt:         schedOpen.Time,
		ScheduledCloseAt:        schedClose.Time,
		MaxAttempts:             maxAttempts,
		ShuffleQuestions:        shuffleQ,
		ShuffleMCQOptions:       shuffleMCQ,
		TotalPoints:             totalPoints,
		QuestionCount:           questionCount,
		ReleaseAnnouncement:     releaseAnn.String,
		CreatedAt:               createdAt.UTC(),
		UpdatedAt:               updatedAt.UTC(),
	}
	if publishedAt.Valid {
		t := publishedAt.Time.UTC()
		a.PublishedAt = &t
	}
	if closedAt.Valid {
		t := closedAt.Time.UTC()
		a.ClosedAt = &t
	}
	if releasedAt.Valid {
		t := releasedAt.Time.UTC()
		a.ResultsReleasedAt = &t
	}
	if archivedAt.Valid {
		t := archivedAt.Time.UTC()
		a.ArchivedAt = &t
	}
	if deletedAt.Valid {
		t := deletedAt.Time.UTC()
		a.DeletedAt = &t
	}
	if len(accomRaw) > 0 {
		var ac domain.Accommodations
		if err := json.Unmarshal(accomRaw, &ac); err == nil {
			a.Accommodations = &ac
		}
	}
	if len(gcfgRaw) > 0 {
		var gc domain.GradingConfigSnapshot
		_ = json.Unmarshal(gcfgRaw, &gc)
		a.GradingConfigSnapshot = gc
	}
	return a, nil
}

// Compile-time port conformance.
var _ domain.AssessmentRepo = (*AssessmentRepo)(nil)
