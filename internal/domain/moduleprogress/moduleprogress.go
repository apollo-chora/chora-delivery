// Package moduleprogress is the per-learner module-completion projection of the
// Content Delivery domain (WS-A "W7 course structure layer", follow-up to the
// module aggregate). A StudentModuleProgress tracks, for one learner (GCID) on
// one Module, which of the module's grouped content items the learner has
// completed, and whether the module's completion Requirement is met.
//
// It is its own aggregate root, distinct from the module.Module authoring
// aggregate: keyed by (TenantID, GCID, ModuleID), with its own lifecycle and
// table (student_module_progress). It holds a CROSS-AGGREGATE reference to a
// Module by UUID (ModuleID) and to content items by UUID (the module-item
// content-item ids) — it never owns them. Completion semantics are NOT
// re-implemented here: RecordItemCompletion delegates to
// module.ModuleRequirement.IsSatisfiedBy so the three RequirementKind rules have
// a single source of truth.
//
// The projection is fed by completion events (e.g. atom_session.completed) via a
// push-inbox subscriber that resolves an atom → its course_content item → the
// module(s) grouping it, all intra-chora_delivery (no cross-DB query). The
// aggregate itself is pure — the module is passed IN so every invariant runs on
// the production path with no I/O.
//
// HEXAGONAL: infrastructure-free — stdlib + google/uuid + the sibling module
// aggregate (same bounded context, pure) only.
package moduleprogress

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

var (
	// ErrInvalidArgument signals a guard-clause failure in a constructor/mutator.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound is the repository sentinel for a missing/soft-deleted projection.
	ErrNotFound = errors.New("student module progress not found")
	// ErrDeleted is returned by mutators on a soft-deleted projection.
	ErrDeleted = errors.New("student module progress is soft-deleted; cannot mutate")
	// ErrItemNotInModule is returned by RecordItemCompletion when the content item
	// is not a current active member of the supplied module (fail-loud: a caller
	// may only record completion of an item the module actually groups).
	ErrItemNotInModule = errors.New("content item is not a member of the module")
)

// StudentModuleProgress is the per-learner, per-module completion projection.
// CompletedContentItemIDs is an append-only log of the module's content-item ids
// the learner has completed; IsComplete is recomputed against the module's
// current items + Requirement on every RecordItemCompletion.
type StudentModuleProgress struct {
	ID                      string     `json:"id"`
	TenantID                string     `json:"tenant_id"`
	GCID                    string     `json:"gcid"`
	ModuleID                string     `json:"module_id"`
	CourseID                string     `json:"course_id"`
	CompletedContentItemIDs []string   `json:"completed_content_item_ids"`
	IsComplete              bool       `json:"is_complete"`
	CompletedAt             *time.Time `json:"completed_at,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	DeletedAt               *time.Time `json:"deleted_at,omitempty"`
}

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID string
	GCID     string
	ModuleID string
	CourseID string
}

// New constructs a fresh, incomplete projection for a (learner, module) pair.
func New(p NewParams) (*StudentModuleProgress, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(p.GCID) == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalidArgument)
	}
	if strings.TrimSpace(p.ModuleID) == "" {
		return nil, fmt.Errorf("%w: module_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(p.CourseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	return &StudentModuleProgress{
		ID:                      id.String(),
		TenantID:                p.TenantID,
		GCID:                    p.GCID,
		ModuleID:                p.ModuleID,
		CourseID:                p.CourseID,
		CompletedContentItemIDs: nil,
		IsComplete:              false,
		CreatedAt:               now,
		UpdatedAt:               now,
	}, nil
}

// IsActive reports whether the projection is not soft-deleted.
func (p *StudentModuleProgress) IsActive() bool { return p.DeletedAt == nil }

// RecordItemCompletion marks contentItemID complete for this learner+module and
// recomputes completion against the module's CURRENT items + Requirement.
//
// The module is passed IN (no I/O): the caller loads it and this method enforces
// consistency (same id + tenant) and membership. Returns changed=true iff the
// completed set or the IsComplete flag actually changed — so an idempotent
// redelivery of the same completion event is a safe no-op (changed=false).
// Fail-loud: recording an item the module does not group is ErrItemNotInModule.
func (p *StudentModuleProgress) RecordItemCompletion(contentItemID string, m *module.Module) (bool, error) {
	if p.DeletedAt != nil {
		return false, ErrDeleted
	}
	if m == nil {
		return false, fmt.Errorf("%w: nil module", ErrInvalidArgument)
	}
	if m.ID != p.ModuleID {
		return false, fmt.Errorf("%w: module id mismatch (%s != %s)", ErrInvalidArgument, m.ID, p.ModuleID)
	}
	if m.TenantID != p.TenantID {
		return false, fmt.Errorf("%w: module tenant mismatch", ErrInvalidArgument)
	}
	cid := strings.TrimSpace(contentItemID)
	if cid == "" {
		return false, fmt.Errorf("%w: content_item_id required", ErrInvalidArgument)
	}

	member := false
	for _, id := range m.ContentItemIDs() {
		if id == cid {
			member = true
			break
		}
	}
	if !member {
		return false, fmt.Errorf("%w: %s not an item in module %s", ErrItemNotInModule, cid, m.ID)
	}

	already := false
	completed := make(map[string]bool, len(p.CompletedContentItemIDs)+1)
	for _, id := range p.CompletedContentItemIDs {
		completed[id] = true
		if id == cid {
			already = true
		}
	}
	completed[cid] = true

	nowComplete := m.Requirement.IsSatisfiedBy(m.Items, completed)
	if already && nowComplete == p.IsComplete {
		return false, nil // nothing changed — idempotent redelivery
	}

	now := time.Now().UTC()
	if !already {
		p.CompletedContentItemIDs = append(p.CompletedContentItemIDs, cid)
	}
	switch {
	case nowComplete && !p.IsComplete:
		p.IsComplete = true
		p.CompletedAt = &now
	case !nowComplete && p.IsComplete:
		// The completion bar rose above the learner's set (e.g. an item was added
		// to the module). Reflect it loudly rather than leaving a stale "complete".
		p.IsComplete = false
		p.CompletedAt = nil
	}
	p.UpdatedAt = now
	return true, nil
}

// SoftDelete marks the projection deleted (idempotent). Pseudonymise + soft-delete
// — never hard-delete, per the domain closure rules.
func (p *StudentModuleProgress) SoftDelete() {
	if p.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	p.DeletedAt = &now
	p.UpdatedAt = now
}
