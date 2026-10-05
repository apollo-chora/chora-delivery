package training_admin

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ApplicationService manages the lifecycle of training applications and
// trainee requests (deferral, withdrawal, makeup, transfer).
type ApplicationService struct {
	appRepo     ApplicationRepository
	reqRepo     TraineeRequestRepository
	sessionRepo TrainingSessionRepository
	events      EventPublisher
}

// NewApplicationService creates an ApplicationService with the given
// repositories and event publisher.
func NewApplicationService(
	appRepo ApplicationRepository,
	reqRepo TraineeRequestRepository,
	sessionRepo TrainingSessionRepository,
	events EventPublisher,
) *ApplicationService {
	return &ApplicationService{
		appRepo:     appRepo,
		reqRepo:     reqRepo,
		sessionRepo: sessionRepo,
		events:      events,
	}
}

// ---------------------------------------------------------------------------
// TrainingApplication operations
// ---------------------------------------------------------------------------

// SubmitApplication validates and persists a new training application.
// It assigns a UUIDv7 ID, sets status to submitted, and publishes an
// application.submitted event.
func (s *ApplicationService) SubmitApplication(ctx context.Context, app *TrainingApplication) (*TrainingApplication, error) {
	if app.TrainingSessionID == uuid.Nil {
		return nil, fmt.Errorf("training_session_id is required: %w", ErrValidationFailed)
	}

	// Check that enrollment is open for the target session.
	session, err := s.sessionRepo.GetByID(ctx, app.TrainingSessionID, app.TenantID)
	if err != nil {
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	if session == nil || !session.EnrollmentOpen {
		return nil, fmt.Errorf("enrollment is not open for session %s: %w", app.TrainingSessionID, ErrEnrollmentClosed)
	}

	now := time.Now().UTC()
	app.ID = uuid.Must(uuid.NewV7())
	app.Status = ApplicationStatusSubmitted
	app.SubmittedAt = &now
	app.CreatedAt = now
	app.UpdatedAt = now

	if err := s.appRepo.Create(ctx, app); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventApplicationSubmitted,
		app.TenantID,
		&app.GCID,
		app.ID,
		AggregateTrainingApplication,
		map[string]interface{}{
			"training_session_id": app.TrainingSessionID.String(),
			"gcid":                app.GCID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, event); err != nil {
		return nil, fmt.Errorf("publish application.submitted: %w", err)
	}

	return app, nil
}

// GetApplication retrieves a training application by ID within a tenant.
func (s *ApplicationService) GetApplication(ctx context.Context, id, tenantID uuid.UUID) (*TrainingApplication, error) {
	app, err := s.appRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, ErrNotFound
	}
	return app, nil
}

// ListApplications returns applications for a session with cursor-based pagination.
func (s *ApplicationService) ListApplications(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TrainingApplication, error) {
	return s.appRepo.List(ctx, sessionID, tenantID, cursor, limit)
}

// DecideApplication approves or rejects a training application. The application
// must be in submitted or under_review status. Rejections require a reason.
// If the reason is "waitlist", the status is set to waitlisted instead of rejected.
func (s *ApplicationService) DecideApplication(ctx context.Context, id, tenantID uuid.UUID, approved bool, reason *string) (*TrainingApplication, error) {
	app, err := s.appRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, ErrNotFound
	}

	if app.Status != ApplicationStatusSubmitted && app.Status != ApplicationStatusUnderReview {
		return nil, fmt.Errorf("application %s is in status %s: %w", id, app.Status, ErrApplicationNotReviewable)
	}

	now := time.Now().UTC()
	reviewerGCID := app.GCID
	app.ReviewedByGCID = &reviewerGCID
	app.ReviewedAt = &now
	app.UpdatedAt = now

	if approved {
		app.Status = ApplicationStatusApproved
	} else {
		if reason == nil {
			return nil, fmt.Errorf("rejection reason is required: %w", ErrValidationFailed)
		}
		if *reason == "waitlist" {
			app.Status = ApplicationStatusWaitlisted
		} else {
			app.Status = ApplicationStatusRejected
			app.RejectionReason = reason
		}
	}

	if err := s.appRepo.Update(ctx, app); err != nil {
		return nil, fmt.Errorf("update application: %w", err)
	}

	event := NewDomainEvent(
		EventApplicationDecided,
		app.TenantID,
		&app.GCID,
		app.ID,
		AggregateTrainingApplication,
		map[string]interface{}{
			"status": string(app.Status),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, event); err != nil {
		return nil, fmt.Errorf("publish application.decided: %w", err)
	}

	return app, nil
}

// TimeoutStaleApplications transitions applications that have been in
// 'submitted' status longer than the given duration to 'rejected' with a
// timeout rejection reason. An application.decided event is published for each.
// Returns the count of timed-out applications.
func (s *ApplicationService) TimeoutStaleApplications(ctx context.Context, tenantID uuid.UUID, maxAge time.Duration) (int, error) {
	apps, err := s.appRepo.ListByStatus(ctx, tenantID, ApplicationStatusSubmitted)
	if err != nil {
		return 0, fmt.Errorf("list submitted applications: %w", err)
	}

	cutoff := time.Now().UTC().Add(-maxAge)
	count := 0

	for i := range apps {
		app := &apps[i]
		if app.SubmittedAt == nil || !app.SubmittedAt.Before(cutoff) {
			continue
		}

		now := time.Now().UTC()
		reason := "Application timed out — no decision within review period"
		app.Status = ApplicationStatusRejected
		app.RejectionReason = &reason
		app.ReviewedAt = &now
		app.UpdatedAt = now

		if err := s.appRepo.Update(ctx, app); err != nil {
			return count, fmt.Errorf("timeout application %s: %w", app.ID, err)
		}

		event := NewDomainEvent(
			EventApplicationDecided,
			app.TenantID,
			&app.GCID,
			app.ID,
			AggregateTrainingApplication,
			map[string]interface{}{
				"status":  string(ApplicationStatusRejected),
				"reason":  "auto_timeout",
				"timeout": true,
			},
		)
		if err := s.events.Publish(ctx, TopicTrainingEvents, event); err != nil {
			return count, fmt.Errorf("publish application.decided (timeout) for %s: %w", app.ID, err)
		}

		count++
	}

	return count, nil
}

// ---------------------------------------------------------------------------
// TraineeRequest operations
// ---------------------------------------------------------------------------

// SubmitRequest validates and persists a new trainee request. It assigns a
// UUIDv7 ID, sets status to submitted, and publishes a request.submitted event.
func (s *ApplicationService) SubmitRequest(ctx context.Context, req *TraineeRequest) (*TraineeRequest, error) {
	if req.Reason == "" {
		return nil, fmt.Errorf("reason is required: %w", ErrValidationFailed)
	}

	if req.RequestType == RequestTypeTransfer {
		if req.TargetSessionID == nil {
			return nil, fmt.Errorf("target_session_id is required for transfer requests: %w", ErrValidationFailed)
		}
		if *req.TargetSessionID == req.TrainingSessionID {
			return nil, fmt.Errorf("cannot transfer to the same session: %w", ErrTransferSameSession)
		}
	}

	now := time.Now().UTC()
	req.ID = uuid.Must(uuid.NewV7())
	req.Status = RequestStatusSubmitted
	req.CreatedAt = now
	req.UpdatedAt = now

	if err := s.reqRepo.Create(ctx, req); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventRequestSubmitted,
		req.TenantID,
		&req.GCID,
		req.ID,
		AggregateTrainingApplication,
		map[string]interface{}{
			"request_type":        string(req.RequestType),
			"training_session_id": req.TrainingSessionID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, event); err != nil {
		return nil, fmt.Errorf("publish request.submitted: %w", err)
	}

	return req, nil
}

// GetRequest retrieves a trainee request by ID within a tenant.
func (s *ApplicationService) GetRequest(ctx context.Context, id, tenantID uuid.UUID) (*TraineeRequest, error) {
	req, err := s.reqRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNotFound
	}
	return req, nil
}

// ListRequests returns trainee requests for a session with cursor-based pagination.
func (s *ApplicationService) ListRequests(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TraineeRequest, error) {
	return s.reqRepo.List(ctx, sessionID, tenantID, cursor, limit)
}

// DecideRequest approves or rejects a trainee request. The request must be in
// submitted or under_review status.
func (s *ApplicationService) DecideRequest(ctx context.Context, id, tenantID uuid.UUID, approved bool, notes *string) (*TraineeRequest, error) {
	req, err := s.reqRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNotFound
	}

	if req.Status != RequestStatusSubmitted && req.Status != RequestStatusUnderReview {
		return nil, fmt.Errorf("request %s is in status %s: %w", id, req.Status, ErrRequestNotDecidable)
	}

	now := time.Now().UTC()
	reviewerGCID := req.GCID
	req.ReviewedByGCID = &reviewerGCID
	req.ReviewedAt = &now
	req.ResolutionNotes = notes
	req.UpdatedAt = now

	if approved {
		req.Status = RequestStatusApproved
	} else {
		req.Status = RequestStatusRejected
	}

	if err := s.reqRepo.Update(ctx, req); err != nil {
		return nil, fmt.Errorf("update request: %w", err)
	}

	event := NewDomainEvent(
		EventRequestDecided,
		req.TenantID,
		&req.GCID,
		req.ID,
		AggregateTrainingApplication,
		map[string]interface{}{
			"status":       string(req.Status),
			"request_type": string(req.RequestType),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, event); err != nil {
		return nil, fmt.Errorf("publish request.decided: %w", err)
	}

	return req, nil
}

// TimeoutStaleRequests transitions trainee requests that have been in
// 'submitted' status longer than the given duration to 'rejected' with a
// timeout resolution note. A request.decided event is published for each.
// Returns the count of timed-out requests.
func (s *ApplicationService) TimeoutStaleRequests(ctx context.Context, tenantID uuid.UUID, maxAge time.Duration) (int, error) {
	reqs, err := s.reqRepo.ListByStatus(ctx, tenantID, RequestStatusSubmitted)
	if err != nil {
		return 0, fmt.Errorf("list submitted requests: %w", err)
	}

	cutoff := time.Now().UTC().Add(-maxAge)
	count := 0

	for i := range reqs {
		req := &reqs[i]
		if !req.CreatedAt.Before(cutoff) {
			continue
		}

		now := time.Now().UTC()
		notes := "Request timed out — no decision within review period"
		req.Status = RequestStatusRejected
		req.ResolutionNotes = &notes
		req.ReviewedAt = &now
		req.UpdatedAt = now

		if err := s.reqRepo.Update(ctx, req); err != nil {
			return count, fmt.Errorf("timeout request %s: %w", req.ID, err)
		}

		event := NewDomainEvent(
			EventRequestDecided,
			req.TenantID,
			&req.GCID,
			req.ID,
			AggregateTrainingApplication,
			map[string]interface{}{
				"status":       string(RequestStatusRejected),
				"request_type": string(req.RequestType),
				"reason":       "auto_timeout",
				"timeout":      true,
			},
		)
		if err := s.events.Publish(ctx, TopicTrainingEvents, event); err != nil {
			return count, fmt.Errorf("publish request.decided (timeout) for %s: %w", req.ID, err)
		}

		count++
	}

	return count, nil
}
