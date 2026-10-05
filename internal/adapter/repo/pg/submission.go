// submission.go — pg adapter for the Submission aggregate + its
// submission_answers child rows per ADR-155.
//
// Save replaces the answer rows on every call (idempotent autosave is the
// hot path — the UPSERT semantics on submission_answers handle the merge).
// To avoid bloating the SQL, we rely on the UNIQUE (submission_id,
// test_set_question_id) constraint + ON CONFLICT DO UPDATE.
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

const SQLUpsertSubmission = `
INSERT INTO submissions (
    submission_id, assessment_id, tenant_id, learner_gcid, attempt_number,
    state, opens_at, closes_at, time_limit_secs, started_at, last_saved_at,
    submitted_at, graded_at, released_at, mcq_score, oe_score, total_score,
    max_score, passing_percent, passed, deleted_at, created_at, updated_at,
    review_status, approved_by_gcid, approved_at, overall_comment,
    ai_overall_comment, overall_comment_provenance
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16, $17,
    $18, $19, $20, $21, $22, $23,
    $24, $25, $26, $27, $28, $29
)
ON CONFLICT (assessment_id, learner_gcid, attempt_number) DO UPDATE SET
    state                      = EXCLUDED.state,
    opens_at                   = EXCLUDED.opens_at,
    closes_at                  = EXCLUDED.closes_at,
    time_limit_secs            = EXCLUDED.time_limit_secs,
    last_saved_at              = EXCLUDED.last_saved_at,
    submitted_at               = EXCLUDED.submitted_at,
    graded_at                  = EXCLUDED.graded_at,
    released_at                = EXCLUDED.released_at,
    mcq_score                  = EXCLUDED.mcq_score,
    oe_score                   = EXCLUDED.oe_score,
    total_score                = EXCLUDED.total_score,
    max_score                  = EXCLUDED.max_score,
    passing_percent            = EXCLUDED.passing_percent,
    passed                     = EXCLUDED.passed,
    deleted_at                 = EXCLUDED.deleted_at,
    updated_at                 = EXCLUDED.updated_at,
    review_status              = EXCLUDED.review_status,
    approved_by_gcid           = EXCLUDED.approved_by_gcid,
    approved_at                = EXCLUDED.approved_at,
    overall_comment            = EXCLUDED.overall_comment,
    ai_overall_comment         = EXCLUDED.ai_overall_comment,
    overall_comment_provenance = EXCLUDED.overall_comment_provenance
`

const SQLUpsertSubmissionAnswer = `
INSERT INTO submission_answers (
    answer_id, submission_id, test_set_question_id, question_id,
    question_type, mcq_choice_id, mcq_choice_ids, oe_response_text,
    answered_at, mcq_correct, points_earned, points_possible, oe_feedback,
    oe_criterion_jsonb, grading_dispatch, oe_graded_at,
    oe_comment, oe_ai_comment, ai_points_earned, score_provenance,
    comment_provenance, amended_model_answer, model_answer_provenance,
    quality_flagged, grading_model_id, grading_response_id,
    overridden_by_gcid, overridden_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8,
    $9, $10, $11, $12, $13,
    $14, $15, $16,
    $17, $18, $19, $20,
    $21, $22, $23,
    $24, $25, $26,
    $27, $28
)
ON CONFLICT (submission_id, test_set_question_id) DO UPDATE SET
    question_id             = EXCLUDED.question_id,
    question_type           = EXCLUDED.question_type,
    mcq_choice_id           = EXCLUDED.mcq_choice_id,
    mcq_choice_ids          = EXCLUDED.mcq_choice_ids,
    oe_response_text        = EXCLUDED.oe_response_text,
    answered_at             = EXCLUDED.answered_at,
    mcq_correct             = COALESCE(EXCLUDED.mcq_correct, submission_answers.mcq_correct),
    -- points_earned is the aggregate's authoritative score (autosave preserves
    -- it in-memory; HITL downward overrides MUST win — no GREATEST clamp).
    points_earned           = EXCLUDED.points_earned,
    points_possible         = GREATEST(EXCLUDED.points_possible, submission_answers.points_possible),
    oe_feedback             = COALESCE(NULLIF(EXCLUDED.oe_feedback, ''), submission_answers.oe_feedback),
    oe_criterion_jsonb      = COALESCE(EXCLUDED.oe_criterion_jsonb, submission_answers.oe_criterion_jsonb),
    grading_dispatch        = COALESCE(NULLIF(EXCLUDED.grading_dispatch, ''), submission_answers.grading_dispatch),
    oe_graded_at            = COALESCE(EXCLUDED.oe_graded_at, submission_answers.oe_graded_at),
    oe_comment              = COALESCE(NULLIF(EXCLUDED.oe_comment, ''), submission_answers.oe_comment),
    oe_ai_comment           = COALESCE(NULLIF(EXCLUDED.oe_ai_comment, ''), submission_answers.oe_ai_comment),
    ai_points_earned        = COALESCE(EXCLUDED.ai_points_earned, submission_answers.ai_points_earned),
    score_provenance        = COALESCE(NULLIF(EXCLUDED.score_provenance, ''), submission_answers.score_provenance),
    comment_provenance      = COALESCE(NULLIF(EXCLUDED.comment_provenance, ''), submission_answers.comment_provenance),
    amended_model_answer    = COALESCE(NULLIF(EXCLUDED.amended_model_answer, ''), submission_answers.amended_model_answer),
    model_answer_provenance = COALESCE(NULLIF(EXCLUDED.model_answer_provenance, ''), submission_answers.model_answer_provenance),
    quality_flagged         = EXCLUDED.quality_flagged,
    grading_model_id        = COALESCE(NULLIF(EXCLUDED.grading_model_id, ''), submission_answers.grading_model_id),
    grading_response_id     = COALESCE(NULLIF(EXCLUDED.grading_response_id, ''), submission_answers.grading_response_id),
    overridden_by_gcid      = COALESCE(EXCLUDED.overridden_by_gcid, submission_answers.overridden_by_gcid),
    overridden_at           = COALESCE(EXCLUDED.overridden_at, submission_answers.overridden_at)
`

const submissionSelectCols = `
    submission_id, assessment_id, tenant_id, learner_gcid, attempt_number,
    state, opens_at, closes_at, time_limit_secs, started_at, last_saved_at,
    submitted_at, graded_at, released_at, mcq_score, oe_score, total_score,
    max_score, passing_percent, passed, deleted_at, created_at, updated_at,
    review_status, approved_by_gcid, approved_at, overall_comment,
    ai_overall_comment, overall_comment_provenance
`

const SQLSelectSubmissionByID = `
SELECT ` + submissionSelectCols + `
FROM submissions
WHERE submission_id = $1 AND tenant_id = $2 AND deleted_at IS NULL
`

const SQLSelectInProgressSubmission = `
SELECT ` + submissionSelectCols + `
FROM submissions
WHERE tenant_id = $1 AND assessment_id = $2 AND learner_gcid = $3
  AND state IN ('STARTED','IN_PROGRESS')
  AND deleted_at IS NULL
ORDER BY attempt_number DESC
LIMIT 1
`

const SQLListSubmissionsByAssessment = `
SELECT ` + submissionSelectCols + `
FROM submissions
WHERE tenant_id = $1 AND assessment_id = $2 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $3
`

const SQLListSubmissionsByLearner = `
SELECT ` + submissionSelectCols + `
FROM submissions
WHERE tenant_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL
ORDER BY created_at DESC
`

const SQLCountAttempts = `
SELECT count(*) FROM submissions
WHERE tenant_id=$1 AND assessment_id=$2 AND learner_gcid=$3 AND deleted_at IS NULL
`

// SQLListReleasedSubmissionsByLearner — the CHO-2040 ceremony learning-edges
// read: a learner's past graded submissions + their ADR-172 overall_comments,
// consumed via the ListLearnerGradedSubmissions gRPC by chora-consumption's
// Familiar edge-scout (cross-DB reads forbidden).
//
// VISIBILITY GATE — mirrors the learner-facing result endpoint
// (internal/adapter/http/assessment_handler.go myResultHandler) EXACTLY:
// grading artifacts incl. overall_comment are revealed to the learner ONLY
// once state = 'RELEASED'. MarkReleased enforces the ADR-172 §D6 HITL gate
// (a PENDING_REVIEW submission cannot release), so filtering on state alone
// subsumes the review_status check — nothing pre-release or unapproved can
// appear here. Soft-delete respected per ddd-enforcement #4.
//
// ORDERING — newest grading-completion first: graded_at is stamped by
// Submission.MarkGradedPendingRelease, which every RELEASED row passed
// through, making it the completion timestamp (proto completed_at). NULLS
// LAST guards schema-anomalous released rows (PG default for DESC is NULLS
// FIRST); submission_id DESC is a deterministic tiebreak. Offset+limit
// windowing mirrors SQLListCJ2CoursesByState (cursor framing is the gRPC
// adapter's concern). Access path is covered by idx_submissions_learner
// (tenant_id, learner_gcid) WHERE deleted_at IS NULL from migration 0010.
const SQLListReleasedSubmissionsByLearner = `
SELECT ` + submissionSelectCols + `
FROM submissions
WHERE tenant_id = $1 AND learner_gcid = $2
  AND state = 'RELEASED'
  AND deleted_at IS NULL
ORDER BY graded_at DESC NULLS LAST, submission_id DESC
LIMIT $3 OFFSET $4
`

// SQLSelectLatestSubmissionByLearner returns the learner's most-recent
// submission for an assessment (highest attempt_number wins, fall back on
// created_at). Backs the LearnerAssessmentSummary projection's
// learner_latest_submission_{id,state} fields per
// E2E-BE-RESULT-LINK-SUBMISSION-ID.
const SQLSelectLatestSubmissionByLearner = `
SELECT ` + submissionSelectCols + `
FROM submissions
WHERE tenant_id = $1 AND assessment_id = $2 AND learner_gcid = $3
  AND deleted_at IS NULL
ORDER BY attempt_number DESC, created_at DESC
LIMIT 1
`

const submissionAnswerCols = `
    answer_id, submission_id, test_set_question_id, question_id,
    question_type, mcq_choice_id, mcq_choice_ids, oe_response_text,
    answered_at, mcq_correct, points_earned, points_possible, oe_feedback,
    oe_criterion_jsonb, grading_dispatch, oe_graded_at,
    oe_comment, oe_ai_comment, ai_points_earned, score_provenance,
    comment_provenance, amended_model_answer, model_answer_provenance,
    quality_flagged, grading_model_id, grading_response_id,
    overridden_by_gcid, overridden_at
`

const SQLSelectAnswersBySubmission = `
SELECT ` + submissionAnswerCols + `
FROM submission_answers
WHERE submission_id = $1
ORDER BY answered_at ASC
`

// SubmissionRepo is the pg adapter.
type SubmissionRepo struct {
	tx TxRunner
}

// NewSubmissionRepo constructs the repo.
func NewSubmissionRepo(tx TxRunner) *SubmissionRepo {
	return &SubmissionRepo{tx: tx}
}

// Save UPSERTs the submission + each of its answers within the same tx.
func (r *SubmissionRepo) Save(ctx context.Context, s *domain.Submission) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if s == nil {
		return errors.New("pg: submission is nil")
	}
	if strings.TrimSpace(s.TenantID) == "" {
		return errors.New("pg: submission.TenantID required")
	}
	ctx = tracing.WithTenantID(ctx, s.TenantID)

	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// Submission row
		var passed any
		if s.Passed != nil {
			passed = *s.Passed
		} else {
			passed = nil
		}
		_, err := q.Exec(ctx, SQLUpsertSubmission,
			s.ID, s.AssessmentID, s.TenantID, s.LearnerGCID, s.AttemptNumber,
			string(s.State), nullTime(s.OpensAt), nullTime(s.ClosesAt),
			s.TimeLimitSecs, s.StartedAt, nullTimePtr(s.LastSavedAt),
			nullTimePtr(s.SubmittedAt), nullTimePtr(s.GradedAt), nullTimePtr(s.ReleasedAt),
			s.MCQScore, s.OEScore, s.TotalScore,
			s.MaxScore, s.PassingPercent, passed,
			nullTimePtr(s.DeletedAt), s.CreatedAt, s.UpdatedAt,
			nullString(string(s.ReviewStatus)), nullString(s.ApprovedByGCID),
			nullTimePtr(s.ApprovedAt), nullString(s.OverallComment),
			nullString(s.AIOverallComment), nullString(string(s.OverallCommentProvenance)),
		)
		if err != nil {
			return fmt.Errorf("pg: upsert submission: %w", err)
		}
		// Answers
		for _, ans := range s.Answers {
			criterionJSON := []byte(nil)
			if ans.OECriterionJSON != "" {
				criterionJSON = []byte(ans.OECriterionJSON)
			}
			var mcqCorrect any
			if ans.MCQCorrect != nil {
				mcqCorrect = *ans.MCQCorrect
			}
			var answeredAt time.Time
			if ans.AnsweredAt != nil {
				answeredAt = *ans.AnsweredAt
			}
			_, aerr := q.Exec(ctx, SQLUpsertSubmissionAnswer,
				domain.NewUUIDv7(), s.ID, ans.TestSetQuestionID, ans.QuestionID,
				string(ans.QuestionType), nullString(ans.MCQChoiceID),
				nullableUUIDArray(ans.MCQChoiceIDs), nullString(ans.OEResponseText),
				answeredAt, mcqCorrect, ans.PointsEarned, ans.PointsPossible,
				nullString(ans.OEFeedback), nullBytes(criterionJSON),
				nullString(string(ans.GradingDispatch)), nullTimePtr(ans.OEGradedAt),
				nullString(ans.OEComment), nullString(ans.AIComment),
				nullFloatPtr(ans.AIPointsEarned), nullString(string(ans.ScoreProvenance)),
				nullString(string(ans.CommentProvenance)), nullString(ans.AmendedModelAnswer),
				nullString(string(ans.ModelAnswerProvenance)), ans.QualityFlagged,
				nullString(ans.GradingModelID), nullString(ans.GradingResponseID),
				nullString(ans.OverriddenByGCID), nullTimePtr(ans.OverriddenAt),
			)
			if aerr != nil {
				return fmt.Errorf("pg: upsert submission_answer: %w", aerr)
			}
		}
		return nil
	})
}

// Get returns the submission + its answers.
func (r *SubmissionRepo) Get(ctx context.Context, tenantID, id string) (*domain.Submission, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var found *domain.Submission
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectSubmissionByID, id, tenantID)
		s, scanErr := scanSubmissionRow(row.Scan)
		if scanErr != nil {
			if errors.Is(scanErr, errNotFound) {
				return nil
			}
			return scanErr
		}
		// Load answers
		answers, aerr := loadAnswers(ctx, q, s.ID)
		if aerr != nil {
			return aerr
		}
		s.Answers = answers
		found = s
		return nil
	})
	return found, found != nil, err
}

// GetInProgressByLearner returns the learner's pending submission.
func (r *SubmissionRepo) GetInProgressByLearner(ctx context.Context, tenantID, assessmentID, learnerGCID string) (*domain.Submission, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var found *domain.Submission
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectInProgressSubmission, tenantID, assessmentID, learnerGCID)
		s, scanErr := scanSubmissionRow(row.Scan)
		if scanErr != nil {
			if errors.Is(scanErr, errNotFound) {
				return nil
			}
			return scanErr
		}
		answers, aerr := loadAnswers(ctx, q, s.ID)
		if aerr != nil {
			return aerr
		}
		s.Answers = answers
		found = s
		return nil
	})
	return found, found != nil, err
}

// ListByAssessment returns submissions for an assessment.
func (r *SubmissionRepo) ListByAssessment(ctx context.Context, tenantID, assessmentID string, pageSize int, _ string) ([]*domain.Submission, string, error) {
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
	var items []*domain.Submission
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLListSubmissionsByAssessment, tenantID, assessmentID, pageSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			s, scanErr := scanSubmissionRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	// Lazy-load answers — for the demo, just leave Answers nil (list view
	// doesn't display them). Result endpoint loads via Get.
	return items, "", nil
}

// ListByLearner returns the learner's submissions.
func (r *SubmissionRepo) ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*domain.Submission, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var items []*domain.Submission
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLListSubmissionsByLearner, tenantID, learnerGCID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			s, scanErr := scanSubmissionRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListReleasedByLearner returns the learner's RELEASED submissions, newest
// grading-completion (graded_at) first, windowed by (limit, offset). See
// SQLListReleasedSubmissionsByLearner for the CHO-2040 visibility rule this
// query mirrors. Answers are intentionally NOT loaded — the ceremony
// edge-scout consumes the summary projection only.
func (r *SubmissionRepo) ListReleasedByLearner(ctx context.Context, tenantID, learnerGCID string, limit, offset int) ([]*domain.Submission, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var items []*domain.Submission
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qerr := q.Query(ctx, SQLListReleasedSubmissionsByLearner, tenantID, learnerGCID, limit, offset)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			s, scanErr := scanSubmissionRow(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// CountAttemptsByLearner returns the attempt count.
func (r *SubmissionRepo) CountAttemptsByLearner(ctx context.Context, tenantID, assessmentID, learnerGCID string) (int, error) {
	if r == nil || r.tx == nil {
		return 0, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var n int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLCountAttempts, tenantID, assessmentID, learnerGCID)
		return row.Scan(&n)
	})
	return n, err
}

// FindLatestByLearner returns the learner's most-recent submission for an
// assessment by attempt_number (created_at as a tie-breaker). (nil, false,
// nil) when no row exists. Backs the LearnerAssessmentSummary projection's
// `learner_latest_submission_{id,state}` fields so the FE deep-link to
// /a/me/assessments/{id}/result/{subId} works without a second round-trip
// (E2E-BE-RESULT-LINK-SUBMISSION-ID).
func (r *SubmissionRepo) FindLatestByLearner(ctx context.Context, tenantID, assessmentID, learnerGCID string) (*domain.Submission, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var found *domain.Submission
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectLatestSubmissionByLearner, tenantID, assessmentID, learnerGCID)
		s, scanErr := scanSubmissionRow(row.Scan)
		if scanErr != nil {
			if errors.Is(scanErr, errNotFound) {
				return nil
			}
			return scanErr
		}
		found = s
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return found, found != nil, nil
}

// loadAnswers fetches all submission_answers for a submission.
func loadAnswers(ctx context.Context, q Querier, submissionID string) ([]domain.SubmissionAnswer, error) {
	rows, err := q.Query(ctx, SQLSelectAnswersBySubmission, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SubmissionAnswer{}
	for rows.Next() {
		a, scanErr := scanSubmissionAnswerRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// scanSubmissionRow consumes a row into a Submission (without Answers).
func scanSubmissionRow(scan func(...any) error) (*domain.Submission, error) {
	var (
		id, assessmentID, tenantID, learnerGCID            string
		attemptNumber, timeLimit, maxScore, passingPercent int
		state                                              string
		opensAt, closesAt                                  sqlNullTime
		startedAt                                          time.Time
		lastSavedAt, submittedAt, gradedAt, releasedAt     sqlNullTime
		mcqScore, oeScore, totalScore                      float64
		passed                                             sqlNullBool
		deletedAt                                          sqlNullTime
		createdAt, updatedAt                               time.Time
		reviewStatus, approvedByGCID                       sqlNullString
		approvedAt                                         sqlNullTime
		overallComment, aiOverallComment, overallProv      sqlNullString
	)
	if err := scan(
		&id, &assessmentID, &tenantID, &learnerGCID, &attemptNumber,
		&state, &opensAt, &closesAt, &timeLimit, &startedAt, &lastSavedAt,
		&submittedAt, &gradedAt, &releasedAt, &mcqScore, &oeScore, &totalScore,
		&maxScore, &passingPercent, &passed, &deletedAt, &createdAt, &updatedAt,
		&reviewStatus, &approvedByGCID, &approvedAt, &overallComment,
		&aiOverallComment, &overallProv,
	); err != nil {
		if isNoRows(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	s := &domain.Submission{
		ID:                       id,
		AssessmentID:             assessmentID,
		TenantID:                 tenantID,
		LearnerGCID:              learnerGCID,
		AttemptNumber:            attemptNumber,
		State:                    domain.SubmissionState(state),
		OpensAt:                  opensAt.Time,
		ClosesAt:                 closesAt.Time,
		TimeLimitSecs:            timeLimit,
		StartedAt:                startedAt.UTC(),
		MCQScore:                 mcqScore,
		OEScore:                  oeScore,
		TotalScore:               totalScore,
		MaxScore:                 maxScore,
		PassingPercent:           passingPercent,
		CreatedAt:                createdAt.UTC(),
		UpdatedAt:                updatedAt.UTC(),
		ReviewStatus:             domain.ReviewStatus(reviewStatus.String),
		ApprovedByGCID:           approvedByGCID.String,
		OverallComment:           overallComment.String,
		AIOverallComment:         aiOverallComment.String,
		OverallCommentProvenance: domain.Provenance(overallProv.String),
	}
	if approvedAt.Valid {
		t := approvedAt.Time.UTC()
		s.ApprovedAt = &t
	}
	if lastSavedAt.Valid {
		t := lastSavedAt.Time.UTC()
		s.LastSavedAt = &t
	}
	if submittedAt.Valid {
		t := submittedAt.Time.UTC()
		s.SubmittedAt = &t
	}
	if gradedAt.Valid {
		t := gradedAt.Time.UTC()
		s.GradedAt = &t
	}
	if releasedAt.Valid {
		t := releasedAt.Time.UTC()
		s.ReleasedAt = &t
	}
	if deletedAt.Valid {
		t := deletedAt.Time.UTC()
		s.DeletedAt = &t
	}
	if passed.Valid {
		v := passed.Bool
		s.Passed = &v
	}
	return s, nil
}

// scanSubmissionAnswerRow consumes a row into a SubmissionAnswer.
func scanSubmissionAnswerRow(scan func(...any) error) (domain.SubmissionAnswer, error) {
	var (
		_id, submissionID, testSetQID, questionID string
		questionType                              string
		mcqChoiceID                               sqlNullString
		mcqChoiceIDs                              textArray
		oeResponse                                sqlNullString
		answeredAt                                time.Time
		mcqCorrect                                sqlNullBool
		pointsEarned                              float64
		pointsPossible                            int
		oeFeedback                                sqlNullString
		criterionJSON                             []byte
		gradingDispatch                           sqlNullString
		oeGradedAt                                sqlNullTime
		oeComment, oeAIComment                    sqlNullString
		aiPointsEarned                            sqlNullFloat64
		scoreProv, commentProv                    sqlNullString
		amendedModelAnswer, modelAnswerProv       sqlNullString
		qualityFlagged                            sqlNullBool
		gradingModelID, gradingResponseID         sqlNullString
		overriddenByGCID                          sqlNullString
		overriddenAt                              sqlNullTime
	)
	if err := scan(
		&_id, &submissionID, &testSetQID, &questionID,
		&questionType, &mcqChoiceID, &mcqChoiceIDs, &oeResponse,
		&answeredAt, &mcqCorrect, &pointsEarned, &pointsPossible,
		&oeFeedback, &criterionJSON, &gradingDispatch, &oeGradedAt,
		&oeComment, &oeAIComment, &aiPointsEarned, &scoreProv,
		&commentProv, &amendedModelAnswer, &modelAnswerProv,
		&qualityFlagged, &gradingModelID, &gradingResponseID,
		&overriddenByGCID, &overriddenAt,
	); err != nil {
		return domain.SubmissionAnswer{}, err
	}
	at := answeredAt.UTC()
	a := domain.SubmissionAnswer{
		TestSetQuestionID:     testSetQID,
		QuestionID:            questionID,
		QuestionType:          domain.QuestionType(questionType),
		MCQChoiceID:           mcqChoiceID.String,
		MCQChoiceIDs:          []string(mcqChoiceIDs),
		OEResponseText:        oeResponse.String,
		AnsweredAt:            &at,
		PointsEarned:          pointsEarned,
		PointsPossible:        pointsPossible,
		OEFeedback:            oeFeedback.String,
		GradingDispatch:       domain.GradingDispatch(gradingDispatch.String),
		OEComment:             oeComment.String,
		AIComment:             oeAIComment.String,
		ScoreProvenance:       domain.Provenance(scoreProv.String),
		CommentProvenance:     domain.Provenance(commentProv.String),
		AmendedModelAnswer:    amendedModelAnswer.String,
		ModelAnswerProvenance: domain.Provenance(modelAnswerProv.String),
		QualityFlagged:        qualityFlagged.Bool,
		GradingModelID:        gradingModelID.String,
		GradingResponseID:     gradingResponseID.String,
		OverriddenByGCID:      overriddenByGCID.String,
	}
	if mcqCorrect.Valid {
		v := mcqCorrect.Bool
		a.MCQCorrect = &v
	}
	if aiPointsEarned.Valid {
		v := aiPointsEarned.Float64
		a.AIPointsEarned = &v
	}
	if len(criterionJSON) > 0 {
		a.OECriterionJSON = string(criterionJSON)
	}
	if oeGradedAt.Valid {
		t := oeGradedAt.Time.UTC()
		a.OEGradedAt = &t
	}
	if overriddenAt.Valid {
		t := overriddenAt.Time.UTC()
		a.OverriddenAt = &t
	}
	return a, nil
}

// nullableUUIDArray returns nil for empty so Postgres NULL maps; otherwise
// returns the textual `{a,b,c}` form. Despite the name, the target column
// (submission_answers.mcq_choice_ids) is text[] post migration 0015 — the
// `{a,b,c}` bind form is parsed identically by both text[] and uuid[] so
// this helper is column-agnostic. Renaming deferred to minimise churn.
func nullableUUIDArray(in []string) any {
	if len(in) == 0 {
		return nil
	}
	return "{" + strings.Join(in, ",") + "}"
}

// SQLInsertGradeOverride appends one append-only HITL audit row (ADR-172 §D7).
const SQLInsertGradeOverride = `
INSERT INTO grade_overrides (
    override_id, tenant_id, submission_id, answer_id, test_set_question_id,
    question_id, field, old_value, new_value, actor_gcid, reason, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
`

// AppendGradeOverrides persists the append-only audit entries produced by a
// HITL override. answer_id is left NULL — the audit links by submission_id +
// test_set_question_id (the domain aggregate does not carry the DB answer_id).
func (r *SubmissionRepo) AppendGradeOverrides(ctx context.Context, tenantID, submissionID string, overrides []domain.GradeOverride) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if len(overrides) == 0 {
		return nil
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		for _, o := range overrides {
			_, err := q.Exec(ctx, SQLInsertGradeOverride,
				domain.NewUUIDv7(), tenantID, submissionID, nil,
				nullString(o.TestSetQuestionID), nullString(o.QuestionID),
				o.Field, nullString(o.OldValue), nullString(o.NewValue),
				o.ActorGCID, nullString(o.Reason), o.At,
			)
			if err != nil {
				return fmt.Errorf("pg: insert grade_override: %w", err)
			}
		}
		return nil
	})
}

// Domain helper — JSON shim for Accommodations Marshal/Unmarshal.
//
//nolint:unused
var _ = json.Marshal

// Compile-time port conformance.
var _ domain.SubmissionRepo = (*SubmissionRepo)(nil)
