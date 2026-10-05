// grading_review_handler.go — ADR-172 instructor grading-review (HITL gate).
//
// Endpoints (mounted via assessmentSubHandler):
//
//	GET   /api/v1/assessments/{id}/submissions/{subId}/grading
//	PATCH /api/v1/assessments/{id}/submissions/{subId}/grades
//	PATCH /api/v1/assessments/{id}/submissions/{subId}/overall-comment
//	POST  /api/v1/assessments/{id}/submissions/{subId}/approve
//	POST  /api/v1/assessments/{id}/approve-all
//
// All require an instructor-level role. Per-question edits are opt-in; the
// affected artifact's provenance flips AI→HUMAN, an append-only grade_overrides
// audit row is written, and score_overridden.v1 (+ model_answer_amended.v1 for
// model-answer edits) is emitted. Release stays gated until APPROVED (the pg
// release SQL + domain MarkReleased enforce the gate).
package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// provOr defaults empty provenance to AI (rows graded before the column existed
// + the AI baseline both read as AI).
func provOr(p domain.Provenance) string {
	if p == domain.ProvenanceHuman {
		return "HUMAN"
	}
	return "AI"
}

// tsqRef is a test-set question's cross-domain identity (for model-answer
// feedback to chora-creation).
type tsqRef struct {
	AtomID     string
	QuestionID string
}

func loadTestSetQuestionRefs(adeps *AssessmentDeps, assessmentID, tenantID string, r *http.Request) map[string]tsqRef {
	out := map[string]tsqRef{}
	if adeps == nil || adeps.TestSets == nil || adeps.Assessments == nil {
		return out
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, assessmentID)
	if err != nil || !ok || a == nil {
		return out
	}
	ts, ok, err := adeps.TestSets.Get(r.Context(), tenantID, a.TestSetID)
	if err != nil || !ok || ts == nil {
		return out
	}
	for _, q := range ts.Questions() {
		out[q.ID] = tsqRef{AtomID: q.QuestionAtomID, QuestionID: q.QuestionID}
	}
	return out
}

// oeAuthored is the author-generated OE content (prompt + model answer + rubric)
// captured into the test-set question's payload_snapshot at publish. It is the
// fallback model answer for a freshly AI-graded OE submission that no instructor
// has amended yet (ADR-172 — the model answer IS generated at authoring).
type oeAuthored struct {
	Prompt      string
	ModelAnswer string
	Rubric      []map[string]any
}

// parseAuthoredOESnapshot decodes the AUTHOR-SAFE OE envelope stored in
// test_set_questions.payload_snapshot (`{stem, model_answer, rubric:[{criterion_id,
// description, weight}]}`, weight already a 0-1 fraction). Empty / non-OE /
// malformed snapshots yield the zero value (fail-soft — the panel just stays bare).
func parseAuthoredOESnapshot(snapshot string) oeAuthored {
	snapshot = strings.TrimSpace(snapshot)
	if snapshot == "" {
		return oeAuthored{}
	}
	var env struct {
		Stem        string `json:"stem"`
		Prompt      string `json:"prompt"`
		ModelAnswer string `json:"model_answer"`
		Rubric      []struct {
			CriterionID string  `json:"criterion_id"`
			Description string  `json:"description"`
			Weight      float64 `json:"weight"`
		} `json:"rubric"`
	}
	if json.Unmarshal([]byte(snapshot), &env) != nil {
		return oeAuthored{}
	}
	prompt := env.Prompt
	if prompt == "" {
		prompt = env.Stem
	}
	rubric := make([]map[string]any, 0, len(env.Rubric))
	for _, c := range env.Rubric {
		rubric = append(rubric, map[string]any{
			"criterion_id": c.CriterionID,
			"description":  c.Description,
			"weight":       c.Weight,
		})
	}
	return oeAuthored{Prompt: prompt, ModelAnswer: env.ModelAnswer, Rubric: rubric}
}

// loadTestSetQuestionAuthored maps each test_set_question_id → its authored OE
// content parsed from payload_snapshot, so the grading-review projection can
// surface the model answer (+ prompt + rubric) the instructor is reviewing.
func loadTestSetQuestionAuthored(adeps *AssessmentDeps, assessmentID, tenantID string, r *http.Request) map[string]oeAuthored {
	out := map[string]oeAuthored{}
	if adeps == nil || adeps.TestSets == nil || adeps.Assessments == nil {
		return out
	}
	a, ok, err := adeps.Assessments.Get(r.Context(), tenantID, assessmentID)
	if err != nil || !ok || a == nil {
		return out
	}
	ts, ok, err := adeps.TestSets.Get(r.Context(), tenantID, a.TestSetID)
	if err != nil || !ok || ts == nil {
		return out
	}
	for _, q := range ts.Questions() {
		out[q.ID] = parseAuthoredOESnapshot(q.PayloadSnapshot)
	}
	return out
}

// guardInstructorSubmission resolves the submission + enforces instructor role
// + tenant scope. Returns (sub, tenantID, gcid, true) on success; writes the
// error + returns ok=false otherwise.
func guardInstructorSubmission(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) (*domain.Submission, string, string, bool) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return nil, "", "", false
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return nil, "", "", false
	}
	sub, ok, err := adeps.Submissions.Get(r.Context(), tenantID, submissionID)
	if err != nil || !ok || sub == nil {
		writeError(w, http.StatusNotFound, "submission not found")
		return nil, "", "", false
	}
	if sub.AssessmentID != assessmentID {
		writeError(w, http.StatusNotFound, "submission not found")
		return nil, "", "", false
	}
	return sub, tenantID, gcid, true
}

// buildGradingReviewDetail projects a submission into the GradingReviewDetail
// response shape (instructor view — current + AI-original + provenance).
func buildGradingReviewDetail(sub *domain.Submission, authored map[string]oeAuthored) map[string]any {
	questions := make([]map[string]any, 0, len(sub.Answers))
	for _, ans := range sub.Answers {
		q := map[string]any{
			"test_set_question_id": ans.TestSetQuestionID,
			"question_id":          ans.QuestionID,
			"question_type":        string(ans.QuestionType),
			"points_earned":        ans.PointsEarned,
			"points_possible":      ans.PointsPossible,
			"grading_dispatch":     string(ans.GradingDispatch),
		}
		if ans.MCQCorrect != nil {
			q["correct"] = *ans.MCQCorrect
		}
		la := map[string]any{}
		if ans.MCQChoiceID != "" {
			la["mcq_choice_id"] = ans.MCQChoiceID
		}
		if len(ans.MCQChoiceIDs) > 0 {
			la["mcq_choice_ids"] = ans.MCQChoiceIDs
		}
		if ans.OEResponseText != "" {
			la["oe_response_text"] = ans.OEResponseText
		}
		q["learner_answer"] = la
		if ans.QuestionType == domain.QuestionTypeOE {
			q["comment"] = ans.OEComment
			q["ai_comment"] = ans.AIComment
			q["comment_provenance"] = provOr(ans.CommentProvenance)
			q["score_provenance"] = provOr(ans.ScoreProvenance)
			q["model_answer_provenance"] = provOr(ans.ModelAnswerProvenance)
			q["quality_flagged"] = ans.QualityFlagged
			if ans.AIPointsEarned != nil {
				q["ai_points_earned"] = *ans.AIPointsEarned
			}
			au := authored[ans.TestSetQuestionID]
			// Model answer: an instructor amendment wins; otherwise fall back to
			// the author-generated model answer from the payload snapshot (so a
			// freshly AI-graded OE still shows a model answer to review).
			if ans.AmendedModelAnswer != "" {
				q["model_answer"] = ans.AmendedModelAnswer
			} else if au.ModelAnswer != "" {
				q["model_answer"] = au.ModelAnswer
			}
			// The review panel also renders the question prompt + rubric when
			// present — neither rides the submission Answer, so project them from
			// the authored snapshot too.
			if au.Prompt != "" {
				q["prompt"] = au.Prompt
			}
			if len(au.Rubric) > 0 {
				q["rubric"] = au.Rubric
			}
			if ans.OECriterionJSON != "" {
				var crit any
				if json.Unmarshal([]byte(ans.OECriterionJSON), &crit) == nil {
					q["criterion_scores"] = crit
				}
			}
		}
		questions = append(questions, q)
	}
	detail := map[string]any{
		"submission_id":              sub.ID,
		"assessment_id":              sub.AssessmentID,
		"learner_gcid":               sub.LearnerGCID,
		"attempt_number":             sub.AttemptNumber,
		"state":                      sub.State.WireState(),
		"review_status":              string(sub.ReviewStatus),
		"total_points_earned":        sub.TotalScore,
		"total_points_possible":      sub.MaxScore,
		"passing_threshold_percent":  sub.PassingPercent,
		"overall_comment":            sub.OverallComment,
		"ai_overall_comment":         sub.AIOverallComment,
		"overall_comment_provenance": provOr(sub.OverallCommentProvenance),
		"questions":                  questions,
	}
	if sub.Passed != nil {
		detail["passed"] = *sub.Passed
	}
	if sub.ApprovedByGCID != "" {
		detail["approved_by_gcid"] = sub.ApprovedByGCID
	}
	if sub.ApprovedAt != nil {
		detail["approved_at"] = sub.ApprovedAt.Format(time.RFC3339Nano)
	}
	return detail
}

func getSubmissionGradingDetailHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	sub, tenantID, _, ok := guardInstructorSubmission(adeps, assessmentID, submissionID, w, r)
	if !ok {
		return
	}
	authored := loadTestSetQuestionAuthored(adeps, assessmentID, tenantID, r)
	writeJSON(w, http.StatusOK, buildGradingReviewDetail(sub, authored))
}

type editGradeItem struct {
	TestSetQuestionID string   `json:"test_set_question_id"`
	PointsEarned      *float64 `json:"points_earned"`
	Comment           *string  `json:"comment"`
	ModelAnswer       *string  `json:"model_answer"`
	Reason            string   `json:"reason"`
}

func editSubmissionGradesHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	sub, tenantID, gcid, ok := guardInstructorSubmission(adeps, assessmentID, submissionID, w, r)
	if !ok {
		return
	}
	var body struct {
		QuestionEdits []editGradeItem `json:"question_edits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.QuestionEdits) == 0 {
		writeError(w, http.StatusBadRequest, "question_edits required")
		return
	}
	now := time.Now().UTC()
	refs := loadTestSetQuestionRefs(adeps, assessmentID, tenantID, r)
	var audits []domain.GradeOverride
	for _, e := range body.QuestionEdits {
		if e.PointsEarned != nil || e.Comment != nil {
			ov, err := sub.OverrideQuestionGrade(now, gcid, e.TestSetQuestionID, e.PointsEarned, e.Comment, e.Reason)
			if err != nil {
				writeGradeOverrideError(w, err)
				return
			}
			audits = append(audits, ov...)
		}
		if e.ModelAnswer != nil {
			ov, err := sub.AmendModelAnswer(now, gcid, e.TestSetQuestionID, *e.ModelAnswer)
			if err != nil {
				writeGradeOverrideError(w, err)
				return
			}
			audits = append(audits, ov...)
			// Cross-domain feedback to chora-creation (ADR-172 §D8).
			ref := refs[e.TestSetQuestionID]
			emitModelAnswerAmended(adeps, sub, e.TestSetQuestionID, ref, *e.ModelAnswer, gcid, now)
		}
	}
	if err := adeps.Submissions.Save(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	if err := adeps.Submissions.AppendGradeOverrides(r.Context(), tenantID, sub.ID, audits); err != nil {
		writeError(w, http.StatusInternalServerError, "audit failed: "+err.Error())
		return
	}
	for _, o := range audits {
		emitScoreOverridden(adeps, sub, o)
	}
	authored := loadTestSetQuestionAuthored(adeps, assessmentID, tenantID, r)
	writeJSON(w, http.StatusOK, buildGradingReviewDetail(sub, authored))
}

func editSubmissionOverallCommentHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	sub, tenantID, gcid, ok := guardInstructorSubmission(adeps, assessmentID, submissionID, w, r)
	if !ok {
		return
	}
	var body struct {
		OverallComment string `json:"overall_comment"`
		Reason         string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.OverallComment) == "" {
		writeError(w, http.StatusBadRequest, "overall_comment required")
		return
	}
	audits, err := sub.EditOverallComment(time.Now().UTC(), gcid, body.OverallComment)
	if err != nil {
		writeGradeOverrideError(w, err)
		return
	}
	if err := adeps.Submissions.Save(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	_ = adeps.Submissions.AppendGradeOverrides(r.Context(), tenantID, sub.ID, audits)
	for _, o := range audits {
		emitScoreOverridden(adeps, sub, o)
	}
	authored := loadTestSetQuestionAuthored(adeps, assessmentID, tenantID, r)
	writeJSON(w, http.StatusOK, buildGradingReviewDetail(sub, authored))
}

func approveSubmissionGradingHandler(adeps *AssessmentDeps, assessmentID, submissionID string, w http.ResponseWriter, r *http.Request) {
	sub, _, gcid, ok := guardInstructorSubmission(adeps, assessmentID, submissionID, w, r)
	if !ok {
		return
	}
	hadEdits := sub.OverallCommentProvenance == domain.ProvenanceHuman || anyAnswerHumanEdited(sub)
	if err := sub.ApproveAsIs(time.Now().UTC(), gcid); err != nil {
		writeGradeOverrideError(w, err)
		return
	}
	if err := adeps.Submissions.Save(r.Context(), sub); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	emitSubmissionApproved(adeps, sub, gcid, hadEdits)
	// CHO-2154 — THIS is the grade of record. The AI's provisional number was
	// withheld while review_status was PENDING_REVIEW (see the grading inbox);
	// the instructor has now blessed a final score, so the platform hears it
	// once, with their numbers. sub carries the recomputed TotalScore/Passed —
	// recomputeTotals() re-derives both on every override, so a correction that
	// drops the learner below the cut-score emits passed=false.
	emitGradeOfRecord(adeps, assessmentID, sub, r)
	writeJSON(w, http.StatusOK, map[string]any{
		"submission_id": sub.ID,
		"state":         sub.State.WireState(),
		"review_status": string(sub.ReviewStatus),
	})
}

// emitGradeOfRecord publishes chora.delivery.submission.graded.v1 for a
// submission whose grade an instructor has just approved — the single, final
// announcement of the learner's outcome (CHO-2154).
//
// The parent assessment is loaded only to snapshot its title onto the event
// (ADR-205 WS-6); a miss is non-fatal — the title is a nicety, the score is not.
func emitGradeOfRecord(adeps *AssessmentDeps, assessmentID string, sub *domain.Submission, r *http.Request) {
	if adeps == nil || adeps.OutboxPublisher == nil || sub == nil {
		return
	}
	tenantID, _ := callerTenantGCID(nil, r)
	var a *domain.Assessment
	if tenantID != "" && adeps.Assessments != nil {
		if got, ok, err := adeps.Assessments.Get(r.Context(), tenantID, assessmentID); err == nil && ok {
			a = got
		}
	}
	emitSubmissionGradedEvent(adeps, a, sub, r)
}

func approveAllSubmissionsHandler(adeps *AssessmentDeps, assessmentID string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return
	}
	var body struct {
		Release             bool   `json:"release"`
		ReleaseAnnouncement string `json:"release_announcement"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional

	subs, _, err := adeps.Submissions.ListByAssessment(r.Context(), tenantID, assessmentID, 10000, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed: "+err.Error())
		return
	}
	now := time.Now().UTC()
	approved := 0
	skipped := []string{}
	for _, sub := range subs {
		if sub.State != domain.SubmissionStateGradedPendingRelease {
			if sub.State == domain.SubmissionStatePendingOEGrading {
				skipped = append(skipped, sub.ID)
			}
			continue
		}
		if sub.ReviewStatus == domain.ReviewStatusApproved {
			approved++
			continue
		}
		if err := sub.ApproveAsIs(now, gcid); err != nil {
			continue
		}
		if err := adeps.Submissions.Save(r.Context(), sub); err != nil {
			writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
			return
		}
		emitSubmissionApproved(adeps, sub, gcid, false)
		// CHO-2154 — bulk approve blesses each grade just as the per-submission
		// approve does, so each one is announced as a grade of record too.
		// Without this, an instructor who uses approve-all leaves every learner's
		// transcript reading the AI's number.
		emitGradeOfRecord(adeps, assessmentID, sub, r)
		approved++
	}

	released := false
	if body.Release {
		// CHO-2349 - this branch flipped the assessment AND every submission to
		// RELEASED and then announced NOTHING: it dropped ReleaseAllSubmissions'
		// returned ids into `_`. The auto-issue certificate engine (CHO-2157)
		// rides chora.delivery.submission.released.v1 and nothing else, so every
		// learner released through the grading queue's bulk button silently lost
		// the credential they had earned. The dedicated release route always
		// announced; the two paths simply disagreed. They now share one emitter.
		//
		// Errors here were also swallowed whole. They are LOUD now: a release
		// that half-happened must not read as a clean success in the log.
		a, _, aerr := adeps.Assessments.Get(r.Context(), tenantID, assessmentID)
		if aerr != nil {
			log.Printf("approve-all: cannot load assessment %s to release: %v", assessmentID, aerr)
		}
		if a != nil {
			if rerr := a.ReleaseResults(now, body.ReleaseAnnouncement); rerr != nil {
				log.Printf("approve-all: assessment %s refused release: %v", assessmentID, rerr)
			} else if serr := adeps.Assessments.Save(r.Context(), a); serr != nil {
				log.Printf("approve-all: assessment %s released in memory but NOT saved: %v", assessmentID, serr)
			}
		}
		releasedIDs, rerr := adeps.Assessments.ReleaseAllSubmissions(r.Context(), tenantID, assessmentID)
		if rerr != nil {
			log.Printf("approve-all: release submissions failed for assessment %s: %v", assessmentID, rerr)
		} else {
			released = true
			emitReleaseEvents(adeps, tenantID, a, releasedIDs, r)
		}
	}
	emitAssessmentApproved(adeps, tenantID, assessmentID, gcid, approved, released, now)
	writeJSON(w, http.StatusOK, map[string]any{
		"assessment_id":             assessmentID,
		"approved_submission_count": approved,
		"skipped_submission_ids":    skipped,
		"released":                  released,
	})
}

func anyAnswerHumanEdited(sub *domain.Submission) bool {
	for _, a := range sub.Answers {
		if a.ScoreProvenance == domain.ProvenanceHuman ||
			a.CommentProvenance == domain.ProvenanceHuman ||
			a.ModelAnswerProvenance == domain.ProvenanceHuman {
			return true
		}
	}
	return false
}

// writeGradeOverrideError maps domain HITL errors to HTTP status codes.
func writeGradeOverrideError(w http.ResponseWriter, err error) {
	switch err {
	case domain.ErrSubmissionNotGraded:
		writeError(w, http.StatusConflict, "DELIVERY_SUBMISSION_NOT_GRADED")
	case domain.ErrSubmissionAlreadyApproved:
		writeError(w, http.StatusConflict, "DELIVERY_SUBMISSION_ALREADY_APPROVED")
	case domain.ErrOEAnswerNotFound:
		writeError(w, http.StatusNotFound, "oe answer not found")
	case domain.ErrScoreOutOfRange:
		writeError(w, http.StatusBadRequest, "score out of range")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// -----------------------------------------------------------------------------
// Event emitters (ADR-172) — via the outbox publishCustomEvent path.
// -----------------------------------------------------------------------------

func emitScoreOverridden(adeps *AssessmentDeps, sub *domain.Submission, o domain.GradeOverride) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	payload := map[string]any{
		"submission_id":        sub.ID,
		"assessment_id":        sub.AssessmentID,
		"test_set_question_id": o.TestSetQuestionID,
		"question_id":          o.QuestionID,
		"field":                o.Field,
		"old_value":            o.OldValue,
		"new_value":            o.NewValue,
		"actor_gcid":           o.ActorGCID,
		"reason":               o.Reason,
		"overridden_at":        o.At.Format(time.RFC3339Nano),
		"chora_imda_dimension": "fairness_and_human_oversight",
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.grading.score_overridden.v1", sub.TenantID, o.ActorGCID, payload)
}

func emitModelAnswerAmended(adeps *AssessmentDeps, sub *domain.Submission, tsqID string, ref tsqRef, newModelAnswer, actorGCID string, now time.Time) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	payload := map[string]any{
		"submission_id":        sub.ID,
		"assessment_id":        sub.AssessmentID,
		"test_set_question_id": tsqID,
		"question_id":          ref.QuestionID,
		"atom_id":              ref.AtomID,
		"new_model_answer":     newModelAnswer,
		"actor_gcid":           actorGCID,
		"amended_at":           now.Format(time.RFC3339Nano),
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.grading.model_answer_amended.v1", sub.TenantID, actorGCID, payload)
}

func emitSubmissionApproved(adeps *AssessmentDeps, sub *domain.Submission, approverGCID string, hadEdits bool) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	payload := map[string]any{
		"submission_id": sub.ID,
		"assessment_id": sub.AssessmentID,
		"approver_gcid": approverGCID,
		"had_edits":     hadEdits,
		"approved_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.grading.submission_approved.v1", sub.TenantID, approverGCID, payload)
}

func emitAssessmentApproved(adeps *AssessmentDeps, tenantID, assessmentID, approverGCID string, count int, released bool, now time.Time) {
	if adeps == nil || adeps.OutboxPublisher == nil {
		return
	}
	payload := map[string]any{
		"assessment_id":             assessmentID,
		"approver_gcid":             approverGCID,
		"approved_submission_count": count,
		"released":                  released,
		"approved_at":               now.Format(time.RFC3339Nano),
	}
	publishCustomEvent(adeps.OutboxPublisher, "chora.delivery.grading.assessment_approved.v1", tenantID, approverGCID, payload)
}
