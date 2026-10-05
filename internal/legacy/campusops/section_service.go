package campusops

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SectionService manages ClassSection lifecycle, time slots, attendance, and enrollments.
type SectionService struct {
	sections    ClassSectionRepository
	timeSlots   TimeSlotRepository
	attend      AttendanceRepository
	enrollments EnrollmentRepository
	events      EventPublisher
}

// NewSectionService creates a SectionService with the given repositories and event publisher.
func NewSectionService(
	sections ClassSectionRepository,
	timeSlots TimeSlotRepository,
	attend AttendanceRepository,
	events EventPublisher,
) *SectionService {
	return &SectionService{
		sections:  sections,
		timeSlots: timeSlots,
		attend:    attend,
		events:    events,
	}
}

// SetEnrollmentRepo sets the enrollment repository (allows adding after construction).
func (s *SectionService) SetEnrollmentRepo(repo EnrollmentRepository) {
	s.enrollments = repo
}

// ---------------------------------------------------------------------------
// Section CRUD
// ---------------------------------------------------------------------------

// CreateSection validates and persists a new ClassSection with UUIDv7 ID
// and active status. Publishes a section.created event.
func (s *SectionService) CreateSection(ctx context.Context, section *ClassSection) (*ClassSection, error) {
	if section.SectionCode == "" {
		return nil, fmt.Errorf("section_code must not be empty: %w", ErrValidationFailed)
	}
	if section.MaxCapacity <= 0 {
		return nil, fmt.Errorf("max_capacity must be greater than 0: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	section.ID = uuid.Must(uuid.NewV7())
	section.Status = SectionStatusActive
	section.EnrolledCount = 0
	section.CreatedAt = now
	section.UpdatedAt = now

	if err := s.sections.Create(ctx, section); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSectionCreated,
		section.TenantID,
		&section.InstructorGCID,
		section.ID,
		AggregateClassSection,
		map[string]interface{}{
			"section_code":        section.SectionCode,
			"training_session_id": section.TrainingSessionID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicCampusEvents, evt); err != nil {
		return nil, fmt.Errorf("publish section.created event: %w", err)
	}

	return section, nil
}

// GetSection retrieves a class section by ID and tenant.
func (s *SectionService) GetSection(ctx context.Context, id, tenantID uuid.UUID) (*ClassSection, error) {
	section, err := s.sections.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if section == nil {
		return nil, ErrSectionNotFound
	}
	return section, nil
}

// ListSections returns class sections with cursor-based pagination.
func (s *SectionService) ListSections(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ClassSection, error) {
	return s.sections.List(ctx, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Time Slots
// ---------------------------------------------------------------------------

// AddTimeSlot validates and persists a new time slot for a class section.
func (s *SectionService) AddTimeSlot(ctx context.Context, slot *SectionTimeSlot) (*SectionTimeSlot, error) {
	if slot.EndTime <= slot.StartTime {
		return nil, fmt.Errorf("end_time must be after start_time: %w", ErrValidationFailed)
	}
	if !slot.DayOfWeek.IsValid() {
		return nil, fmt.Errorf("invalid day_of_week %q: %w", slot.DayOfWeek, ErrValidationFailed)
	}

	now := time.Now().UTC()
	slot.ID = uuid.Must(uuid.NewV7())
	slot.CreatedAt = now

	if err := s.timeSlots.Create(ctx, slot); err != nil {
		return nil, err
	}
	return slot, nil
}

// ListTimeSlots returns all time slots for a class section.
func (s *SectionService) ListTimeSlots(ctx context.Context, sectionID uuid.UUID) ([]SectionTimeSlot, error) {
	return s.timeSlots.ListBySection(ctx, sectionID)
}

// ---------------------------------------------------------------------------
// Attendance
// ---------------------------------------------------------------------------

// RecordAttendance validates and persists a batch of attendance records.
// Each record is assigned a UUIDv7 ID and recorded_at timestamp.
// Publishes an attendance.recorded event.
func (s *SectionService) RecordAttendance(ctx context.Context, records []Attendance) error {
	now := time.Now().UTC()
	for i := range records {
		if !records[i].Status.IsValid() {
			return fmt.Errorf("invalid attendance status %q at index %d: %w", records[i].Status, i, ErrValidationFailed)
		}
		if !records[i].CheckInMethod.IsValid() {
			return fmt.Errorf("invalid check_in_method %q at index %d: %w", records[i].CheckInMethod, i, ErrValidationFailed)
		}
		records[i].ID = uuid.Must(uuid.NewV7())
		records[i].RecordedAt = now
	}

	if err := s.attend.Create(ctx, records); err != nil {
		return err
	}

	first := records[0]
	evt := NewDomainEvent(
		EventAttendanceRecorded,
		first.TenantID,
		nil,
		first.SectionID,
		AggregateClassSection,
		map[string]interface{}{
			"count": len(records),
		},
	)
	if err := s.events.Publish(ctx, TopicCampusEvents, evt); err != nil {
		return fmt.Errorf("publish attendance.recorded event: %w", err)
	}

	return nil
}

// ListAttendance returns attendance records for a section with cursor-based pagination.
func (s *SectionService) ListAttendance(ctx context.Context, sectionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Attendance, error) {
	return s.attend.ListBySection(ctx, sectionID, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Section Update & Delete
// ---------------------------------------------------------------------------

// UpdateSection applies mutable field changes to an existing class section.
func (s *SectionService) UpdateSection(ctx context.Context, section *ClassSection) (*ClassSection, error) {
	existing, err := s.sections.GetByID(ctx, section.ID, section.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSectionNotFound
	}
	if existing.Status != SectionStatusActive {
		return nil, ErrSectionNotModifiable
	}

	if section.SectionCode != "" {
		existing.SectionCode = section.SectionCode
	}
	if section.RoomID != nil {
		existing.RoomID = section.RoomID
	}
	if section.InstructorGCID != (uuid.UUID{}) {
		existing.InstructorGCID = section.InstructorGCID
	}
	if section.MaxCapacity > 0 {
		existing.MaxCapacity = section.MaxCapacity
	}
	if section.Status != "" && section.Status.IsValid() {
		existing.Status = section.Status
	}
	existing.UpdatedAt = time.Now().UTC()

	if err := s.sections.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// DeleteSection soft-deletes a class section.
func (s *SectionService) DeleteSection(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.sections.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrSectionNotFound
	}
	return s.sections.Delete(ctx, id, tenantID)
}

// ---------------------------------------------------------------------------
// Time Slot Update & Delete
// ---------------------------------------------------------------------------

// UpdateTimeSlot applies changes to an existing time slot.
func (s *SectionService) UpdateTimeSlot(ctx context.Context, slot *SectionTimeSlot) (*SectionTimeSlot, error) {
	existing, err := s.timeSlots.GetByID(ctx, slot.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrTimeSlotNotFound
	}

	if !slot.DayOfWeek.IsValid() {
		return nil, fmt.Errorf("invalid day_of_week %q: %w", slot.DayOfWeek, ErrValidationFailed)
	}
	if slot.EndTime <= slot.StartTime {
		return nil, fmt.Errorf("end_time must be after start_time: %w", ErrValidationFailed)
	}

	existing.DayOfWeek = slot.DayOfWeek
	existing.StartTime = slot.StartTime
	existing.EndTime = slot.EndTime
	existing.RoomID = slot.RoomID
	existing.EffectiveFrom = slot.EffectiveFrom
	existing.EffectiveUntil = slot.EffectiveUntil
	existing.UpdatedAt = time.Now().UTC()

	if err := s.timeSlots.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// DeleteTimeSlot soft-deletes a time slot.
func (s *SectionService) DeleteTimeSlot(ctx context.Context, id uuid.UUID) error {
	existing, err := s.timeSlots.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrTimeSlotNotFound
	}
	return s.timeSlots.Delete(ctx, id)
}

// ---------------------------------------------------------------------------
// Enrollment
// ---------------------------------------------------------------------------

// EnrollLearner enrolls a learner in a section. If the section is full,
// the learner is auto-waitlisted. Emits enrollment.confirmed or enrollment.waitlisted.
func (s *SectionService) EnrollLearner(ctx context.Context, sectionID, learnerGCID, tenantID uuid.UUID) (*SectionEnrollment, error) {
	// Verify section exists
	section, err := s.sections.GetByID(ctx, sectionID, tenantID)
	if err != nil {
		return nil, err
	}
	if section == nil {
		return nil, ErrSectionNotFound
	}

	// Check if already enrolled
	existing, err := s.enrollments.GetByLearnerAndSection(ctx, learnerGCID, sectionID, tenantID)
	if err != nil {
		return nil, err
	}
	if existing != nil && (existing.Status == EnrollmentStatusEnrolled || existing.Status == EnrollmentStatusWaitlisted) {
		return nil, ErrAlreadyEnrolled
	}

	now := time.Now().UTC()
	enrollment := &SectionEnrollment{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		SectionID:   sectionID,
		LearnerGCID: learnerGCID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Check capacity
	enrolled, err := s.enrollments.CountEnrolled(ctx, sectionID, tenantID)
	if err != nil {
		return nil, err
	}

	if enrolled >= section.MaxCapacity {
		// Waitlist
		pos, err := s.enrollments.NextWaitlistPosition(ctx, sectionID, tenantID)
		if err != nil {
			return nil, err
		}
		enrollment.Status = EnrollmentStatusWaitlisted
		enrollment.WaitlistPosition = &pos
	} else {
		// Enroll
		enrollment.Status = EnrollmentStatusEnrolled
		enrollment.EnrolledAt = &now
	}

	if err := s.enrollments.Create(ctx, enrollment); err != nil {
		return nil, err
	}

	// Update enrolled count on section
	section.EnrolledCount = enrolled
	if enrollment.Status == EnrollmentStatusEnrolled {
		section.EnrolledCount = enrolled + 1
	}
	section.UpdatedAt = now
	_ = s.sections.Update(ctx, section)

	// Publish event
	eventType := EventEnrollmentConfirmed
	if enrollment.Status == EnrollmentStatusWaitlisted {
		eventType = EventEnrollmentWaitlisted
	}
	evt := NewDomainEvent(
		eventType,
		tenantID,
		&learnerGCID,
		enrollment.ID,
		AggregateSectionEnrollment,
		map[string]interface{}{
			"section_id":   sectionID.String(),
			"learner_gcid": learnerGCID.String(),
			"status":       string(enrollment.Status),
		},
	)
	_ = s.events.Publish(ctx, TopicCampusEvents, evt)

	return enrollment, nil
}

// DropLearner drops a learner from a section and promotes the next waitlisted learner.
// Emits enrollment.dropped and optionally enrollment.promoted.
func (s *SectionService) DropLearner(ctx context.Context, sectionID, learnerGCID, tenantID uuid.UUID) (*SectionEnrollment, error) {
	enrollment, err := s.enrollments.GetByLearnerAndSection(ctx, learnerGCID, sectionID, tenantID)
	if err != nil {
		return nil, err
	}
	if enrollment == nil {
		return nil, ErrEnrollmentNotFound
	}

	wasEnrolled := enrollment.Status == EnrollmentStatusEnrolled
	now := time.Now().UTC()
	enrollment.Status = EnrollmentStatusDropped
	enrollment.DroppedAt = &now
	enrollment.UpdatedAt = now

	if err := s.enrollments.Update(ctx, enrollment); err != nil {
		return nil, err
	}

	// Publish drop event
	evt := NewDomainEvent(
		EventEnrollmentDropped,
		tenantID,
		&learnerGCID,
		enrollment.ID,
		AggregateSectionEnrollment,
		map[string]interface{}{
			"section_id":   sectionID.String(),
			"learner_gcid": learnerGCID.String(),
		},
	)
	_ = s.events.Publish(ctx, TopicCampusEvents, evt)

	// Promote from waitlist if the dropped learner was enrolled
	if wasEnrolled {
		waitlisted, err := s.enrollments.ListWaitlisted(ctx, sectionID, tenantID)
		if err == nil && len(waitlisted) > 0 {
			promoted := &waitlisted[0]
			promoted.Status = EnrollmentStatusEnrolled
			promoted.EnrolledAt = &now
			promoted.WaitlistPosition = nil
			promoted.UpdatedAt = now
			if updateErr := s.enrollments.Update(ctx, promoted); updateErr == nil {
				promEvt := NewDomainEvent(
					EventEnrollmentPromoted,
					tenantID,
					&promoted.LearnerGCID,
					promoted.ID,
					AggregateSectionEnrollment,
					map[string]interface{}{
						"section_id":   sectionID.String(),
						"learner_gcid": promoted.LearnerGCID.String(),
					},
				)
				_ = s.events.Publish(ctx, TopicCampusEvents, promEvt)
			}
		}
	}

	return enrollment, nil
}

// ListEnrollments returns enrollments for a section with cursor-based pagination.
func (s *SectionService) ListEnrollments(ctx context.Context, sectionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SectionEnrollment, error) {
	return s.enrollments.ListBySection(ctx, sectionID, tenantID, cursor, limit)
}

// ListWaitlist returns waitlisted enrollments for a section ordered by position.
func (s *SectionService) ListWaitlist(ctx context.Context, sectionID, tenantID uuid.UUID) ([]SectionEnrollment, error) {
	return s.enrollments.ListWaitlisted(ctx, sectionID, tenantID)
}

// ListEnrolledByLearnerAndTerm returns enrolled sections for a learner in a specific term.
func (s *SectionService) ListEnrolledByLearnerAndTerm(ctx context.Context, learnerGCID, termID, tenantID uuid.UUID) ([]SectionEnrollment, error) {
	return s.enrollments.ListByLearnerAndTerm(ctx, learnerGCID, termID, tenantID)
}

// ListSectionsByTerm returns all class sections for a specific term.
func (s *SectionService) ListSectionsByTerm(ctx context.Context, termID, tenantID uuid.UUID) ([]ClassSection, error) {
	return s.sections.ListByTerm(ctx, termID, tenantID)
}
