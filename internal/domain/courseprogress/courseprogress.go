// Package courseprogress is the per-learner, per-COURSE self-paced traversal
// projection of the Content Delivery domain - the aggregate that lets the R+
// ASYNC Analytics tab report avg_progress_pct and completion_rate for an
// offering (R+ Four-Mode DoD §10.3 step 3, epic CHO-1827).
//
// # Why this exists when student_module_progress already does
//
// They measure DIFFERENT facts and are fed by different events:
//
//   - moduleprogress.StudentModuleProgress answers "which MODULES of a
//     structured course has this learner completed?" It is keyed on module_id and
//     its resolver joins course_module_items ⋈ course_modules, so it only ever
//     produces a row for a course that HAS a module structure. The async workspace
//     has no Curriculum tab (tab set = Overview · Publish/Catalog-handoff ·
//     Analytics), so an async product has no modules and that projection is
//     structurally empty for it. Rolling analytics off it would have shipped a
//     metric that reads 0 forever and looks like "nobody is learning".
//
//   - CourseLearnerProgress (this type) answers "what fraction of the course's
//     ATOMS has this learner traversed?" - which is what self-paced progress IS.
//     It needs no module structure, only atoms.
//
// # Where the numbers come from (and why no cross-DB query)
//
// Learner progress lives in chora_consumption; analytics lives in chora_delivery.
// Cross-DB queries are FORBIDDEN, so the only legal bridge is an event. The
// bridge already exists and needed no new topic:
//
//	chora.delivery.enrollment.created.v1  → consumption bootstraps a LearningPath
//	                                        carrying CourseID (the delivery BINDING)
//	learner completes atoms               → consumption advances that path
//	chora.consumption.learning_path.advanced.v1   → carries course_id + learner_gcid
//	                                                + current_index + total_atoms
//	chora.consumption.learning_path.completed.v1  → carries course_id + completed_at
//
// This aggregate is the delivery-side landing point for those two events. It is
// its own root, keyed by (TenantID, GCID, CourseID); the course reference is a
// UUID with NO FK (cross-aggregate ref per ddd-enforcement #3).
//
// HEXAGONAL: infrastructure-free - stdlib + google/uuid only.
package courseprogress

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInvalidArgument signals a guard-clause failure in a constructor/mutator.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound is the repository sentinel for a missing/soft-deleted projection.
	ErrNotFound = errors.New("course learner progress not found")
	// ErrDeleted is returned by mutators on a soft-deleted projection.
	ErrDeleted = errors.New("course learner progress is soft-deleted; cannot mutate")
)

// CourseLearnerProgress is one learner's traversal of one course's atoms.
//
// CompletedAtoms/TotalAtoms are stored as the INTEGERS the producer sends
// (current_index / total_atoms) rather than the wire's `progress_percent`
// float. That field is misnamed at the source - LearningPath.ProgressPercent()
// returns a FRACTION in [0.0, 1.0], not a percent - so consuming it as a
// "percent" would under-report progress by 100x. Integers carry the same
// information with no naming trap and no float drift, and let the roll-up divide
// exactly once, at the end.
type CourseLearnerProgress struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	GCID     string `json:"gcid"`
	CourseID string `json:"course_id"`
	// PathID correlates back to the chora_consumption LearningPath. Reference
	// only: delivery never reads it across the DB boundary.
	PathID         string     `json:"path_id,omitempty"`
	CompletedAtoms int        `json:"completed_atoms"`
	TotalAtoms     int        `json:"total_atoms"`
	IsComplete     bool       `json:"is_complete"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	// LastAdvanceAt is the producer clock of the newest advance folded in - the
	// watermark that makes an UNORDERED redelivery safe. It deliberately tracks
	// the advance stream ONLY: completion is idempotent via IsComplete and must
	// not be able to fence out a legitimately later advance.
	LastAdvanceAt *time.Time `json:"last_advance_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
}

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID string
	GCID     string
	CourseID string
	PathID   string
}

// New constructs a fresh, zero-progress projection for a (learner, course) pair.
func New(p NewParams) (*CourseLearnerProgress, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(p.GCID) == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	if strings.TrimSpace(p.CourseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	return &CourseLearnerProgress{
		ID:        id.String(),
		TenantID:  strings.TrimSpace(p.TenantID),
		GCID:      strings.TrimSpace(p.GCID),
		CourseID:  strings.TrimSpace(p.CourseID),
		PathID:    strings.TrimSpace(p.PathID),
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// IsActive reports whether the projection is not soft-deleted.
func (p *CourseLearnerProgress) IsActive() bool { return p.DeletedAt == nil }

// RecordAdvance folds one learning_path.advanced.v1 into the projection.
//
// Returns changed=true iff stored state actually moved, so an at-least-once
// redelivery is a no-op rather than a spurious write.
//
// Ordering: Pub/Sub does not order messages, so an OLDER advance can arrive
// after a newer one. Applying it would rewind the learner's progress and make
// the cohort average sag for no reason. occurredAt is the producer clock and is
// the watermark: anything strictly older than what we already folded in is
// dropped. An event at exactly the watermark is still evaluated, because that is
// how an identical redelivery proves itself a no-op rather than being guessed at.
//
// The denominator may legitimately GROW (consumption's AppendAtom adds atoms to
// a path retroactively), so a rising TotalAtoms with an unchanged CompletedAtoms
// is a real update, not a rewind.
func (p *CourseLearnerProgress) RecordAdvance(completedAtoms, totalAtoms int, occurredAt time.Time) (bool, error) {
	if p.DeletedAt != nil {
		return false, ErrDeleted
	}
	if completedAtoms < 0 {
		return false, fmt.Errorf("%w: completed_atoms must not be negative (got %d)", ErrInvalidArgument, completedAtoms)
	}
	if totalAtoms < 0 {
		return false, fmt.Errorf("%w: total_atoms must not be negative (got %d)", ErrInvalidArgument, totalAtoms)
	}
	if completedAtoms > totalAtoms {
		// Refuse loudly rather than clamp: a cursor past the end of its own atom
		// list means the producer and this projection disagree about the path, and
		// silently storing 100% would bury that.
		return false, fmt.Errorf("%w: completed_atoms %d exceeds total_atoms %d", ErrInvalidArgument, completedAtoms, totalAtoms)
	}
	if p.LastAdvanceAt != nil && occurredAt.Before(*p.LastAdvanceAt) {
		return false, nil // stale redelivery - never rewind
	}
	if p.CompletedAtoms == completedAtoms && p.TotalAtoms == totalAtoms {
		return false, nil // identical redelivery
	}
	now := time.Now().UTC()
	p.CompletedAtoms = completedAtoms
	p.TotalAtoms = totalAtoms
	at := occurredAt.UTC()
	p.LastAdvanceAt = &at
	p.UpdatedAt = now
	return true, nil
}

// RecordCompletion folds one learning_path.completed.v1 into the projection.
//
// Completion is TERMINAL and the stamp is written once - consumption sets
// LearningPath.CompletedAt exactly once, so a second arrival is a redelivery and
// must not move the stamp. Returns changed=false in that case.
func (p *CourseLearnerProgress) RecordCompletion(occurredAt time.Time) (bool, error) {
	if p.DeletedAt != nil {
		return false, ErrDeleted
	}
	if p.IsComplete {
		return false, nil // already complete - keep the first stamp
	}
	at := occurredAt.UTC()
	p.IsComplete = true
	p.CompletedAt = &at
	p.UpdatedAt = time.Now().UTC()
	return true, nil
}

// ProgressFraction reports traversal in [0.0, 1.0].
//
// A COMPLETE path is 1.0 by definition, whatever the counts say. That is not a
// convenience: completed.v1 and advanced.v1 are separate messages with no
// relative ordering, so a completion can land before the advance that would have
// pushed the counts to full. Deriving the fraction purely from the counters
// would report a finished learner as partially done until the other message
// happened to arrive, and the cohort average would wobble with broker timing.
//
// A zero denominator is 0.0, never NaN - a course with no atoms cannot have been
// partly traversed, and a NaN would poison every average it touches.
func (p *CourseLearnerProgress) ProgressFraction() float64 {
	if p.IsComplete {
		return 1.0
	}
	if p.TotalAtoms <= 0 {
		return 0.0
	}
	return float64(p.CompletedAtoms) / float64(p.TotalAtoms)
}

// SoftDelete marks the projection deleted (idempotent). Soft-delete + pseudonymise
// - never hard-delete, per the domain closure rules.
func (p *CourseLearnerProgress) SoftDelete() {
	if p.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	p.DeletedAt = &now
	p.UpdatedAt = now
}
