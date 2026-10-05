package training_admin

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// StudentPortalService manages learner results, attendance records,
// certificate requests, and student appeals.
type StudentPortalService struct {
	resultRepo  LearnerResultRepository
	attendRepo  AttendanceRecordRepository
	certReqRepo CertificateRequestRepository
	appealRepo  StudentAppealRepository
	appRepo     ApplicationRepository
	events      EventPublisher
}

// NewStudentPortalService creates a StudentPortalService with the given
// repositories and event publisher.
func NewStudentPortalService(
	resultRepo LearnerResultRepository,
	attendRepo AttendanceRecordRepository,
	certReqRepo CertificateRequestRepository,
	appealRepo StudentAppealRepository,
	appRepo ApplicationRepository,
	events EventPublisher,
) *StudentPortalService {
	return &StudentPortalService{
		resultRepo:  resultRepo,
		attendRepo:  attendRepo,
		certReqRepo: certReqRepo,
		appealRepo:  appealRepo,
		appRepo:     appRepo,
		events:      events,
	}
}

// ---------------------------------------------------------------------------
// Learner Results
// ---------------------------------------------------------------------------

// CreateResult validates and persists a new LearnerResult. The result is
// assigned a UUIDv7 ID with published=false.
func (s *StudentPortalService) CreateResult(ctx context.Context, result *LearnerResult) (*LearnerResult, error) {
	if result.SessionID == uuid.Nil {
		return nil, fmt.Errorf("session_id is required: %w", ErrValidationFailed)
	}
	if result.LearnerID == uuid.Nil {
		return nil, fmt.Errorf("learner_id is required: %w", ErrValidationFailed)
	}
	if !result.AssessmentType.IsValid() {
		return nil, fmt.Errorf("invalid assessment_type %q: %w", result.AssessmentType, ErrValidationFailed)
	}
	if result.MaxScore <= 0 {
		return nil, fmt.Errorf("max_score must be greater than 0: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	result.ID = uuid.Must(uuid.NewV7())
	result.Published = false
	result.CreatedAt = now
	result.UpdatedAt = now

	if err := s.resultRepo.Create(ctx, result); err != nil {
		return nil, err
	}

	return result, nil
}

// GetResult retrieves a learner result by ID within a tenant.
func (s *StudentPortalService) GetResult(ctx context.Context, id, tenantID uuid.UUID) (*LearnerResult, error) {
	result, err := s.resultRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrResultNotFound
	}
	return result, nil
}

// ListResults returns results for a learner with cursor-based pagination.
func (s *StudentPortalService) ListResults(ctx context.Context, learnerID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]LearnerResult, error) {
	return s.resultRepo.ListByLearner(ctx, learnerID, tenantID, cursor, limit)
}

// GetResultsSummary returns aggregated stats for a learner's results.
func (s *StudentPortalService) GetResultsSummary(ctx context.Context, learnerID, tenantID uuid.UUID) (*ResultsSummary, error) {
	results, err := s.resultRepo.ListByLearner(ctx, learnerID, tenantID, nil, 1000)
	if err != nil {
		return nil, fmt.Errorf("list results for summary: %w", err)
	}

	summary := &ResultsSummary{
		LearnerID:     learnerID,
		TotalResults:  len(results),
		ResultsByType: make(map[string]ResultTypeSummary),
	}

	if len(results) == 0 {
		return summary, nil
	}

	var totalScore, totalPct float64
	typeScores := make(map[string][]float64)

	for _, r := range results {
		if !r.Published {
			continue
		}
		totalScore += r.Score
		if r.MaxScore > 0 {
			totalPct += (r.Score / r.MaxScore) * 100
		}
		key := string(r.AssessmentType)
		typeScores[key] = append(typeScores[key], r.Score)
	}

	publishedCount := 0
	for _, scores := range typeScores {
		publishedCount += len(scores)
	}

	if publishedCount > 0 {
		summary.AverageScore = totalScore / float64(publishedCount)
		summary.AveragePct = totalPct / float64(publishedCount)
	}
	summary.TotalResults = publishedCount

	for key, scores := range typeScores {
		var avg float64
		for _, sc := range scores {
			avg += sc
		}
		if len(scores) > 0 {
			avg /= float64(len(scores))
		}
		summary.ResultsByType[key] = ResultTypeSummary{
			Count:        len(scores),
			AverageScore: avg,
		}
	}

	return summary, nil
}

// UpdateResult applies mutable field changes to an existing result.
func (s *StudentPortalService) UpdateResult(ctx context.Context, result *LearnerResult) (*LearnerResult, error) {
	existing, err := s.resultRepo.GetByID(ctx, result.ID, result.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrResultNotFound
	}

	existing.Score = result.Score
	existing.MaxScore = result.MaxScore
	existing.Grade = result.Grade
	existing.Remarks = result.Remarks
	existing.UpdatedAt = time.Now().UTC()

	if err := s.resultRepo.Update(ctx, existing); err != nil {
		return nil, err
	}

	return existing, nil
}

// PublishSessionResults marks all unpublished results for a session as published
// and emits result.published events. Returns the number of published results.
func (s *StudentPortalService) PublishSessionResults(ctx context.Context, sessionID, tenantID uuid.UUID, publisherGCID uuid.UUID) (int, error) {
	results, err := s.resultRepo.ListUnpublishedBySession(ctx, sessionID, tenantID)
	if err != nil {
		return 0, fmt.Errorf("list unpublished results: %w", err)
	}

	now := time.Now().UTC()
	count := 0

	for i := range results {
		results[i].Published = true
		results[i].PublishedAt = &now
		results[i].UpdatedAt = now

		if err := s.resultRepo.Update(ctx, &results[i]); err != nil {
			return count, fmt.Errorf("publish result %s: %w", results[i].ID, err)
		}
		count++
	}

	if count > 0 {
		evt := NewDomainEvent(
			EventResultPublished,
			tenantID,
			&publisherGCID,
			sessionID,
			AggregateTrainingSession,
			map[string]interface{}{
				"session_id":      sessionID.String(),
				"published_count": count,
			},
		)
		if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
			return count, fmt.Errorf("publish result.published event: %w", err)
		}
	}

	return count, nil
}

// ---------------------------------------------------------------------------
// Attendance Records
// ---------------------------------------------------------------------------

// ListAttendanceRecords returns aggregated attendance records for a learner.
func (s *StudentPortalService) ListAttendanceRecords(ctx context.Context, learnerID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]AttendanceRecord, error) {
	return s.attendRepo.ListByLearner(ctx, learnerID, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Certificate Requests
// ---------------------------------------------------------------------------

// CreateCertificateRequest submits a new certificate request.
func (s *StudentPortalService) CreateCertificateRequest(ctx context.Context, req *CertificateRequest) (*CertificateRequest, error) {
	if req.ProgramID == uuid.Nil {
		return nil, fmt.Errorf("program_id is required: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	req.ID = uuid.Must(uuid.NewV7())
	req.Status = CertReqStatusSubmitted
	req.CreatedAt = now
	req.UpdatedAt = now

	if err := s.certReqRepo.Create(ctx, req); err != nil {
		return nil, err
	}

	return req, nil
}

// GetCertificateRequest retrieves a certificate request by ID within a tenant.
func (s *StudentPortalService) GetCertificateRequest(ctx context.Context, id, tenantID uuid.UUID) (*CertificateRequest, error) {
	req, err := s.certReqRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrCertificateRequestNotFound
	}
	return req, nil
}

// ListCertificateRequests returns certificate requests for a tenant.
func (s *StudentPortalService) ListCertificateRequests(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CertificateRequest, error) {
	return s.certReqRepo.List(ctx, tenantID, cursor, limit)
}

// ReviewCertificateRequest approves or rejects a certificate request.
func (s *StudentPortalService) ReviewCertificateRequest(ctx context.Context, id, tenantID uuid.UUID, approved bool, rejectionReason *string, reviewerGCID uuid.UUID) (*CertificateRequest, error) {
	req, err := s.certReqRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrCertificateRequestNotFound
	}

	if req.Status != CertReqStatusSubmitted && req.Status != CertReqStatusUnderReview {
		return nil, ErrCertificateRequestNotReviewable
	}

	now := time.Now().UTC()
	req.ReviewerID = &reviewerGCID
	req.ReviewedAt = &now
	req.UpdatedAt = now

	if approved {
		req.Status = CertReqStatusApproved
	} else {
		req.Status = CertReqStatusRejected
		req.RejectionReason = rejectionReason
	}

	if err := s.certReqRepo.Update(ctx, req); err != nil {
		return nil, fmt.Errorf("update certificate request: %w", err)
	}

	return req, nil
}

// IssueCertificate issues a certificate for an approved request.
func (s *StudentPortalService) IssueCertificate(ctx context.Context, id, tenantID uuid.UUID) (*CertificateRequest, error) {
	req, err := s.certReqRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrCertificateRequestNotFound
	}

	if req.Status != CertReqStatusApproved {
		return nil, ErrCertificateRequestNotIssuable
	}

	now := time.Now().UTC()
	certURL := fmt.Sprintf("https://certificates.chora.app/%s", req.ID.String())
	req.Status = CertReqStatusIssued
	req.CertificateURL = &certURL
	req.UpdatedAt = now

	if err := s.certReqRepo.Update(ctx, req); err != nil {
		return nil, fmt.Errorf("issue certificate: %w", err)
	}

	evt := NewDomainEvent(
		EventCertificateRequested,
		tenantID,
		nil,
		req.ID,
		AggregateCertificateRequest,
		map[string]interface{}{
			"request_id":      req.ID.String(),
			"learner_gcid":    req.LearnerID.String(),
			"program_id":      req.ProgramID.String(),
			"certificate_url": certURL,
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish certificate.requested event: %w", err)
	}

	return req, nil
}

// ---------------------------------------------------------------------------
// Student Appeals
// ---------------------------------------------------------------------------

// CreateAppeal submits a new student appeal.
func (s *StudentPortalService) CreateAppeal(ctx context.Context, appeal *StudentAppeal) (*StudentAppeal, error) {
	if appeal.ResultID == uuid.Nil {
		return nil, fmt.Errorf("result_id is required: %w", ErrValidationFailed)
	}
	if !appeal.AppealType.IsValid() {
		return nil, fmt.Errorf("invalid appeal_type %q: %w", appeal.AppealType, ErrValidationFailed)
	}
	if appeal.Reason == "" {
		return nil, fmt.Errorf("reason is required: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	appeal.ID = uuid.Must(uuid.NewV7())
	appeal.Status = AppealStatusSubmitted
	appeal.CreatedAt = now
	appeal.UpdatedAt = now

	if err := s.appealRepo.Create(ctx, appeal); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventAppealSubmitted,
		appeal.TenantID,
		&appeal.LearnerID,
		appeal.ID,
		AggregateStudentAppeal,
		map[string]interface{}{
			"appeal_id":    appeal.ID.String(),
			"learner_gcid": appeal.LearnerID.String(),
			"result_id":    appeal.ResultID.String(),
			"appeal_type":  string(appeal.AppealType),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish appeal.submitted event: %w", err)
	}

	return appeal, nil
}

// GetAppeal retrieves a student appeal by ID within a tenant.
func (s *StudentPortalService) GetAppeal(ctx context.Context, id, tenantID uuid.UUID) (*StudentAppeal, error) {
	appeal, err := s.appealRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if appeal == nil {
		return nil, ErrAppealNotFound
	}
	return appeal, nil
}

// ListAppeals returns student appeals for a tenant with cursor-based pagination.
func (s *StudentPortalService) ListAppeals(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]StudentAppeal, error) {
	return s.appealRepo.List(ctx, tenantID, cursor, limit)
}

// AddEvidence appends evidence URLs to an existing appeal.
func (s *StudentPortalService) AddEvidence(ctx context.Context, id, tenantID uuid.UUID, urls []string) (*StudentAppeal, error) {
	appeal, err := s.appealRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if appeal == nil {
		return nil, ErrAppealNotFound
	}

	appeal.EvidenceURLs = append(appeal.EvidenceURLs, urls...)
	appeal.UpdatedAt = time.Now().UTC()

	if err := s.appealRepo.Update(ctx, appeal); err != nil {
		return nil, fmt.Errorf("add evidence: %w", err)
	}

	return appeal, nil
}

// ReviewAppeal resolves a student appeal (upheld, overturned, dismissed).
func (s *StudentPortalService) ReviewAppeal(ctx context.Context, id, tenantID uuid.UUID, decision AppealStatus, notes *string, reviewerGCID uuid.UUID) (*StudentAppeal, error) {
	appeal, err := s.appealRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if appeal == nil {
		return nil, ErrAppealNotFound
	}

	if appeal.Status != AppealStatusSubmitted && appeal.Status != AppealStatusUnderReview {
		return nil, ErrAppealNotReviewable
	}

	now := time.Now().UTC()
	appeal.Status = decision
	appeal.ReviewerID = &reviewerGCID
	appeal.ReviewerNotes = notes
	appeal.ReviewedAt = &now
	appeal.UpdatedAt = now

	if err := s.appealRepo.Update(ctx, appeal); err != nil {
		return nil, fmt.Errorf("review appeal: %w", err)
	}

	evt := NewDomainEvent(
		EventAppealResolved,
		appeal.TenantID,
		&appeal.LearnerID,
		appeal.ID,
		AggregateStudentAppeal,
		map[string]interface{}{
			"appeal_id":     appeal.ID.String(),
			"learner_gcid":  appeal.LearnerID.String(),
			"result_id":     appeal.ResultID.String(),
			"resolution":    string(decision),
			"reviewer_gcid": reviewerGCID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish appeal.resolved event: %w", err)
	}

	return appeal, nil
}

// ---------------------------------------------------------------------------
// Application Workflow Enhancements
// ---------------------------------------------------------------------------

// ListApplicationsByStatus returns applications filtered by status for a tenant.
func (s *StudentPortalService) ListApplicationsByStatus(ctx context.Context, tenantID uuid.UUID, status ApplicationStatus) ([]TrainingApplication, error) {
	return s.appRepo.ListByStatus(ctx, tenantID, status)
}

// BatchDecideApplications approves or rejects multiple applications at once.
// Returns processed count, failed count, and individual errors.
func (s *StudentPortalService) BatchDecideApplications(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, approved bool, reason *string) (int, int, []map[string]string) {
	processed := 0
	failed := 0
	var errors []map[string]string

	for _, id := range ids {
		app, err := s.appRepo.GetByID(ctx, id, tenantID)
		if err != nil || app == nil {
			failed++
			errors = append(errors, map[string]string{
				"application_id": id.String(),
				"error":          "not found",
			})
			continue
		}

		if app.Status != ApplicationStatusSubmitted && app.Status != ApplicationStatusUnderReview {
			failed++
			errors = append(errors, map[string]string{
				"application_id": id.String(),
				"error":          fmt.Sprintf("not reviewable: status is %s", app.Status),
			})
			continue
		}

		now := time.Now().UTC()
		app.ReviewedAt = &now
		app.UpdatedAt = now

		if approved {
			app.Status = ApplicationStatusApproved
		} else {
			app.Status = ApplicationStatusRejected
			app.RejectionReason = reason
		}

		if err := s.appRepo.Update(ctx, app); err != nil {
			failed++
			errors = append(errors, map[string]string{
				"application_id": id.String(),
				"error":          err.Error(),
			})
			continue
		}

		processed++
	}

	return processed, failed, errors
}

// PromoteWaitlist promotes waitlisted applications to approved for a session.
// Returns the number of promoted applications.
func (s *StudentPortalService) PromoteWaitlist(ctx context.Context, tenantID, sessionID uuid.UUID, count int) (int, error) {
	apps, err := s.appRepo.ListByStatus(ctx, tenantID, ApplicationStatusWaitlisted)
	if err != nil {
		return 0, fmt.Errorf("list waitlisted applications: %w", err)
	}

	// Filter to only those for the given session.
	var filtered []TrainingApplication
	for _, app := range apps {
		if app.TrainingSessionID == sessionID {
			filtered = append(filtered, app)
		}
	}

	promoted := 0
	for i := range filtered {
		if promoted >= count {
			break
		}
		app := &filtered[i]
		now := time.Now().UTC()
		app.Status = ApplicationStatusApproved
		app.ReviewedAt = &now
		app.UpdatedAt = now

		if err := s.appRepo.Update(ctx, app); err != nil {
			return promoted, fmt.Errorf("promote application %s: %w", app.ID, err)
		}
		promoted++
	}

	return promoted, nil
}
