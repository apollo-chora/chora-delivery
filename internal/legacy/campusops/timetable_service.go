package campusops

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TimetableService manages TimetablePublication lifecycle.
type TimetableService struct {
	timetables TimetableRepository
	sections   ClassSectionRepository
	timeSlots  TimeSlotRepository
	events     EventPublisher
}

// NewTimetableService creates a TimetableService with the given repositories and event publisher.
func NewTimetableService(
	timetables TimetableRepository,
	sections ClassSectionRepository,
	timeSlots TimeSlotRepository,
	events EventPublisher,
) *TimetableService {
	return &TimetableService{
		timetables: timetables,
		sections:   sections,
		timeSlots:  timeSlots,
		events:     events,
	}
}

// PublishTimetable creates a snapshot of the current sections and timeslots for a term,
// supersedes any previous publications, and publishes a timetable.published event.
func (s *TimetableService) PublishTimetable(ctx context.Context, termID, publishedBy, tenantID uuid.UUID) (*TimetablePublication, error) {
	// Get all sections for the term
	sections, err := s.sections.ListByTerm(ctx, termID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list sections for term: %w", err)
	}

	// Build snapshot
	sectionSnapshots := make([]map[string]interface{}, 0, len(sections))
	for _, sec := range sections {
		slots, err := s.timeSlots.ListBySection(ctx, sec.ID)
		if err != nil {
			return nil, fmt.Errorf("list timeslots for section %s: %w", sec.ID, err)
		}
		slotData := make([]map[string]interface{}, 0, len(slots))
		for _, slot := range slots {
			sd := map[string]interface{}{
				"id":          slot.ID.String(),
				"day_of_week": string(slot.DayOfWeek),
				"start_time":  slot.StartTime,
				"end_time":    slot.EndTime,
			}
			if slot.RoomID != nil {
				sd["room_id"] = slot.RoomID.String()
			}
			slotData = append(slotData, sd)
		}
		sectionSnapshots = append(sectionSnapshots, map[string]interface{}{
			"id":              sec.ID.String(),
			"section_code":    sec.SectionCode,
			"instructor_gcid": sec.InstructorGCID.String(),
			"max_capacity":    sec.MaxCapacity,
			"enrolled_count":  sec.EnrolledCount,
			"status":          string(sec.Status),
			"timeslots":       slotData,
		})
	}

	snapshot := map[string]interface{}{
		"sections": sectionSnapshots,
	}

	// Get next version
	maxVersion, err := s.timetables.GetMaxVersion(ctx, termID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get max version: %w", err)
	}

	// Supersede existing publications
	if err := s.timetables.SupersedeAll(ctx, termID, tenantID); err != nil {
		return nil, fmt.Errorf("supersede existing publications: %w", err)
	}

	now := time.Now().UTC()
	pub := &TimetablePublication{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		TermID:      termID,
		PublishedBy: publishedBy,
		PublishedAt: now,
		Version:     maxVersion + 1,
		Status:      PublicationStatusPublished,
		Snapshot:    snapshot,
		CreatedAt:   now,
	}

	if err := s.timetables.Create(ctx, pub); err != nil {
		return nil, err
	}

	// Publish event
	evt := NewDomainEvent(
		EventTimetablePublished,
		tenantID,
		&publishedBy,
		pub.ID,
		AggregateTimetablePublication,
		map[string]interface{}{
			"term_id": termID.String(),
			"version": pub.Version,
		},
	)
	_ = s.events.Publish(ctx, TopicCampusEvents, evt)

	return pub, nil
}

// GetTimetable returns the latest published timetable for a term.
func (s *TimetableService) GetTimetable(ctx context.Context, termID, tenantID uuid.UUID) (*TimetablePublication, error) {
	pub, err := s.timetables.GetLatest(ctx, termID, tenantID)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		return nil, ErrTimetableNotFound
	}
	return pub, nil
}

// ListVersions returns all timetable publication versions for a term.
func (s *TimetableService) ListVersions(ctx context.Context, termID, tenantID uuid.UUID) ([]TimetablePublication, error) {
	return s.timetables.ListByTerm(ctx, termID, tenantID)
}
