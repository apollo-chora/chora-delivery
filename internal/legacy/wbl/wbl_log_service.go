package wbl

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// WBLService manages WBL log submission and supervisor approval workflow.
type WBLService struct {
	logs   WBLLogRepository
	events EventPublisher
}

// NewWBLService creates a WBLService with the given dependencies.
func NewWBLService(logs WBLLogRepository, events EventPublisher) *WBLService {
	return &WBLService{logs: logs, events: events}
}

// SubmitLog creates a new WBL log entry in pending_approval status.
// Publishes a wbl.log.submitted event.
func (s *WBLService) SubmitLog(ctx context.Context, log *WBLLog) (*WBLLog, error) {
	if log.ActivityDescription == "" {
		return nil, fmt.Errorf("activity_description must not be empty: %w", ErrValidationFailed)
	}
	if log.SupervisorGCID == uuid.Nil {
		return nil, fmt.Errorf("supervisor_gcid is required: %w", ErrValidationFailed)
	}
	if log.DurationHours <= 0 {
		return nil, fmt.Errorf("duration_hours must be greater than 0: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	log.ID = uuid.Must(uuid.NewV7())
	log.Status = WBLLogStatusPendingApproval
	log.SubmittedAt = now
	log.CreatedAt = now
	log.UpdatedAt = now

	if log.TopicNodeIDs == nil {
		log.TopicNodeIDs = []uuid.UUID{}
	}

	if err := s.logs.Create(ctx, log); err != nil {
		return nil, fmt.Errorf("save WBL log: %w", err)
	}

	evt := NewDomainEvent(
		EventWBLLogSubmitted,
		log.TenantID,
		&log.GCID,
		log.ID,
		AggregateWBLLog,
		map[string]interface{}{
			"log_id":          log.ID.String(),
			"supervisor_gcid": log.SupervisorGCID.String(),
			"duration_hours":  log.DurationHours,
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish wbl.log.submitted: %w", err)
	}

	return log, nil
}

// ApproveLog approves a pending WBL log entry. Publishes a wbl.log.approved event.
func (s *WBLService) ApproveLog(ctx context.Context, id, tenantID uuid.UUID, feedback *string) (*WBLLog, error) {
	log, err := s.logs.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get WBL log %s: %w", id, err)
	}
	if log == nil {
		return nil, ErrWBLLogNotFound
	}

	if log.Status != WBLLogStatusPendingApproval {
		return nil, fmt.Errorf("log is not pending approval: %w", ErrWBLLogNotPending)
	}

	now := time.Now().UTC()
	log.Status = WBLLogStatusApproved
	log.SupervisorFeedback = feedback
	log.ReviewedAt = &now
	log.UpdatedAt = now

	if err := s.logs.Update(ctx, log); err != nil {
		return nil, fmt.Errorf("update WBL log: %w", err)
	}

	evt := NewDomainEvent(
		EventWBLLogApproved,
		log.TenantID,
		&log.GCID,
		log.ID,
		AggregateWBLLog,
		map[string]interface{}{
			"log_id":          log.ID.String(),
			"supervisor_gcid": log.SupervisorGCID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish wbl.log.approved: %w", err)
	}

	return log, nil
}

// RejectLog rejects a pending WBL log entry with feedback. Publishes a
// wbl.log.rejected event.
func (s *WBLService) RejectLog(ctx context.Context, id, tenantID uuid.UUID, feedback string) (*WBLLog, error) {
	log, err := s.logs.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get WBL log %s: %w", id, err)
	}
	if log == nil {
		return nil, ErrWBLLogNotFound
	}

	if log.Status != WBLLogStatusPendingApproval {
		return nil, fmt.Errorf("log is not pending approval: %w", ErrWBLLogNotPending)
	}

	if feedback == "" {
		return nil, fmt.Errorf("feedback is required when rejecting: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	log.Status = WBLLogStatusRejected
	log.SupervisorFeedback = &feedback
	log.ReviewedAt = &now
	log.UpdatedAt = now

	if err := s.logs.Update(ctx, log); err != nil {
		return nil, fmt.Errorf("update WBL log: %w", err)
	}

	evt := NewDomainEvent(
		EventWBLLogRejected,
		log.TenantID,
		&log.GCID,
		log.ID,
		AggregateWBLLog,
		map[string]interface{}{
			"log_id":          log.ID.String(),
			"supervisor_gcid": log.SupervisorGCID.String(),
			"feedback":        feedback,
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish wbl.log.rejected: %w", err)
	}

	return log, nil
}

// ResubmitLog allows a learner to resubmit a rejected WBL log with updated details.
func (s *WBLService) ResubmitLog(ctx context.Context, id, tenantID uuid.UUID, activityDescription string, durationHours float64) (*WBLLog, error) {
	log, err := s.logs.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get WBL log %s: %w", id, err)
	}
	if log == nil {
		return nil, ErrWBLLogNotFound
	}

	if log.Status != WBLLogStatusRejected {
		return nil, fmt.Errorf("log is not in rejected status: %w", ErrWBLLogNotRejected)
	}

	if activityDescription == "" {
		return nil, fmt.Errorf("activity_description must not be empty: %w", ErrValidationFailed)
	}
	if durationHours <= 0 {
		return nil, fmt.Errorf("duration_hours must be greater than 0: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	log.ActivityDescription = activityDescription
	log.DurationHours = durationHours
	log.Status = WBLLogStatusPendingApproval
	log.SupervisorFeedback = nil
	log.ReviewedAt = nil
	log.SubmittedAt = now
	log.UpdatedAt = now

	if err := s.logs.Update(ctx, log); err != nil {
		return nil, fmt.Errorf("update WBL log: %w", err)
	}

	evt := NewDomainEvent(
		EventWBLLogSubmitted,
		log.TenantID,
		&log.GCID,
		log.ID,
		AggregateWBLLog,
		map[string]interface{}{
			"log_id":   log.ID.String(),
			"resubmit": true,
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish wbl.log.submitted (resubmit): %w", err)
	}

	return log, nil
}

// ListLogs returns WBL logs for a tenant with cursor-based pagination.
func (s *WBLService) ListLogs(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]WBLLog, error) {
	return s.logs.List(ctx, tenantID, cursor, limit)
}

// GetLog retrieves a WBL log by ID and tenant.
func (s *WBLService) GetLog(ctx context.Context, id, tenantID uuid.UUID) (*WBLLog, error) {
	log, err := s.logs.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get WBL log %s: %w", id, err)
	}
	if log == nil {
		return nil, ErrWBLLogNotFound
	}
	return log, nil
}
