package wbl

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PlacementService manages placements, work logs, and skill endorsements.
type PlacementService struct {
	placements   PlacementRepository
	workLogs     WorkLogRepository
	endorsements EndorsementRepository
	internships  InternshipRepository
	events       EventPublisher
}

// NewPlacementService creates a PlacementService with the given
// repositories and event publisher.
func NewPlacementService(
	placements PlacementRepository,
	workLogs WorkLogRepository,
	endorsements EndorsementRepository,
	internships InternshipRepository,
	events EventPublisher,
) *PlacementService {
	return &PlacementService{
		placements:   placements,
		workLogs:     workLogs,
		endorsements: endorsements,
		internships:  internships,
		events:       events,
	}
}

// ---------------------------------------------------------------------------
// Placement CRUD
// ---------------------------------------------------------------------------

// CreatePlacement validates and persists a new Placement with UUIDv7 ID and
// active status. Publishes a placement.started event.
func (s *PlacementService) CreatePlacement(ctx context.Context, placement *Placement) (*Placement, error) {
	if placement.InternshipID == uuid.Nil {
		return nil, fmt.Errorf("internship_id is required: %w", ErrValidationFailed)
	}
	if placement.LearnerGCID == uuid.Nil {
		return nil, fmt.Errorf("learner_gcid is required: %w", ErrValidationFailed)
	}

	// Verify the internship exists.
	internship, err := s.internships.GetByID(ctx, placement.InternshipID, placement.TenantID)
	if err != nil {
		return nil, fmt.Errorf("lookup internship: %w", err)
	}
	if internship == nil {
		return nil, ErrInternshipNotFound
	}

	now := time.Now().UTC()
	placement.ID = uuid.Must(uuid.NewV7())
	placement.Status = PlacementStatusActive
	placement.TotalHoursLogged = 0
	placement.CreatedAt = now
	placement.UpdatedAt = now

	if err := s.placements.Create(ctx, placement); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventPlacementStarted,
		placement.TenantID,
		&placement.LearnerGCID,
		placement.ID,
		AggregatePlacement,
		map[string]interface{}{
			"placement_id":  placement.ID.String(),
			"internship_id": placement.InternshipID.String(),
			"learner_gcid":  placement.LearnerGCID.String(),
		},
	)
	if placement.SupervisorGCID != nil {
		evt.Payload["supervisor_gcid"] = placement.SupervisorGCID.String()
	}
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish placement.started: %w", err)
	}

	return placement, nil
}

// ListPlacements returns placements for a tenant with cursor-based pagination.
func (s *PlacementService) ListPlacements(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Placement, error) {
	return s.placements.List(ctx, tenantID, cursor, limit)
}

// CompletePlacement transitions an active placement to completed. Publishes a
// placement.completed event.
func (s *PlacementService) CompletePlacement(ctx context.Context, id, tenantID uuid.UUID) (*Placement, error) {
	placement, err := s.placements.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if placement == nil {
		return nil, ErrPlacementNotFound
	}

	if placement.Status != PlacementStatusActive {
		return nil, fmt.Errorf("placement is not active: %w", ErrPlacementNotActive)
	}

	placement.Status = PlacementStatusCompleted
	placement.UpdatedAt = time.Now().UTC()

	if err := s.placements.Update(ctx, placement); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventPlacementCompleted,
		placement.TenantID,
		&placement.LearnerGCID,
		placement.ID,
		AggregatePlacement,
		map[string]interface{}{
			"placement_id":       placement.ID.String(),
			"learner_gcid":       placement.LearnerGCID.String(),
			"total_hours_logged": placement.TotalHoursLogged,
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish placement.completed: %w", err)
	}

	return placement, nil
}

// ---------------------------------------------------------------------------
// Work Logs
// ---------------------------------------------------------------------------

// AddWorkLogEntry validates and persists a new WorkLogEntry for a placement.
// The placement must be active. Updates total_hours_logged on the placement.
// Publishes a worklog.added event.
func (s *PlacementService) AddWorkLogEntry(ctx context.Context, entry *WorkLogEntry) (*WorkLogEntry, error) {
	if entry.Hours <= 0 {
		return nil, fmt.Errorf("hours must be greater than 0: %w", ErrValidationFailed)
	}
	if entry.Description == "" {
		return nil, fmt.Errorf("description is required: %w", ErrValidationFailed)
	}

	placement, err := s.placements.GetByID(ctx, entry.PlacementID, entry.TenantID)
	if err != nil {
		return nil, fmt.Errorf("lookup placement: %w", err)
	}
	if placement == nil {
		return nil, ErrPlacementNotFound
	}

	if placement.Status != PlacementStatusActive {
		return nil, fmt.Errorf("cannot log work on %s placement: %w", placement.Status, ErrPlacementNotActive)
	}

	now := time.Now().UTC()
	entry.ID = uuid.Must(uuid.NewV7())
	entry.SupervisorApproved = false
	entry.CreatedAt = now

	if err := s.workLogs.Create(ctx, entry); err != nil {
		return nil, err
	}

	// Update total hours on the placement.
	placement.TotalHoursLogged += entry.Hours
	placement.UpdatedAt = now
	if err := s.placements.Update(ctx, placement); err != nil {
		return nil, fmt.Errorf("update placement hours: %w", err)
	}

	evt := NewDomainEvent(
		EventWorkLogAdded,
		entry.TenantID,
		nil,
		entry.PlacementID,
		AggregatePlacement,
		map[string]interface{}{
			"entry_id":     entry.ID.String(),
			"placement_id": entry.PlacementID.String(),
			"hours":        entry.Hours,
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish worklog.added: %w", err)
	}

	return entry, nil
}

// ListWorkLogEntries returns work log entries for a placement with cursor-based pagination.
func (s *PlacementService) ListWorkLogEntries(ctx context.Context, placementID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]WorkLogEntry, error) {
	return s.workLogs.ListByPlacement(ctx, placementID, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Skill Endorsements
// ---------------------------------------------------------------------------

// EndorseSkill validates and persists a SkillEndorsement for a placement.
// The placement must be active. Publishes a skill.endorsed event.
func (s *PlacementService) EndorseSkill(ctx context.Context, endorsement *SkillEndorsement) (*SkillEndorsement, error) {
	if endorsement.SkillName == "" {
		return nil, fmt.Errorf("skill_name is required: %w", ErrValidationFailed)
	}
	if !endorsement.Level.IsValid() {
		return nil, fmt.Errorf("invalid endorsement level %q: %w", endorsement.Level, ErrValidationFailed)
	}

	placement, err := s.placements.GetByID(ctx, endorsement.PlacementID, endorsement.TenantID)
	if err != nil {
		return nil, fmt.Errorf("lookup placement: %w", err)
	}
	if placement == nil {
		return nil, ErrPlacementNotFound
	}

	if placement.Status != PlacementStatusActive {
		return nil, fmt.Errorf("cannot endorse on %s placement: %w", placement.Status, ErrPlacementNotActive)
	}

	now := time.Now().UTC()
	endorsement.ID = uuid.Must(uuid.NewV7())
	endorsement.EndorsedAt = now

	if err := s.endorsements.Create(ctx, endorsement); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSkillEndorsed,
		endorsement.TenantID,
		&endorsement.EndorserGCID,
		endorsement.PlacementID,
		AggregatePlacement,
		map[string]interface{}{
			"endorsement_id": endorsement.ID.String(),
			"placement_id":   endorsement.PlacementID.String(),
			"endorser_gcid":  endorsement.EndorserGCID.String(),
			"skill_name":     endorsement.SkillName,
			"level":          string(endorsement.Level),
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish skill.endorsed: %w", err)
	}

	return endorsement, nil
}
