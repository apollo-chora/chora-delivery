// Package module is the course-structure aggregate of the Content Delivery
// domain (WS-A "W7 course structure layer"). A Module GROUPS the flat, ordered
// course_content.ContentItems of a Course into a named, ordered unit and
// carries a completion Requirement.
//
// A Module is its own aggregate root, distinct from the CourseContent
// curriculum aggregate: a ModuleItem holds a cross-aggregate reference to a
// ContentItem BY UUID (ContentItemID) — it NEVER owns the content. This mirrors
// course_content's stance (collections query atoms; they never own them) per
// .claude/rules/ddd-enforcement.md Aggregate Invariant #1/#3. The reference is
// intra-chora_delivery (same database, different aggregate) — a UUID without an
// FK constraint, never a cross-DB link.
//
// Ordering + membership invariants (all enforced here + unit-tested):
//   - item Position is dense (0..n-1) and unique within a module; AddItem
//     appends, RemoveItem re-compacts, ReorderItems applies a full permutation;
//   - a ContentItem is a member of a module at most once (active);
//   - soft-delete only (deleted_at); SoftDelete cascades to the module's items
//     WITHIN the aggregate boundary but never touches the referenced
//     ContentItem;
//   - the completion Requirement stays consistent across item mutations
//     (RemoveItem refuses a removal that would break an n_of_m threshold or
//     orphan a specific-items requirement).
//
// Module Position (order within a course) is dense-assigned by the persistence
// port on Create (append) and guarded by a partial unique index — it is a
// cross-aggregate concern (siblings), not a single-Module invariant, so it is
// not mutated here.
//
// HEXAGONAL: infrastructure-free — stdlib + google/uuid only (mirrors the
// course_content aggregate's dependency stance; no domain→domain coupling).
package module

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Requirement kind
// -----------------------------------------------------------------------------

// RequirementKind is the rule that decides when a Module counts as complete.
type RequirementKind string

const (
	// RequirementAllItems — every active item in the module must be completed.
	RequirementAllItems RequirementKind = "all_items"
	// RequirementNOfM — any ThresholdN of the module's items must be completed.
	RequirementNOfM RequirementKind = "n_of_m"
	// RequirementSpecificItems — a named subset (RequiredItemIDs) must be completed.
	RequirementSpecificItems RequirementKind = "specific_items"
)

// Valid reports whether k is a recognised requirement kind.
func (k RequirementKind) Valid() bool {
	switch k {
	case RequirementAllItems, RequirementNOfM, RequirementSpecificItems:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Constants + sentinel errors
// -----------------------------------------------------------------------------

const (
	// MaxItemsPerModule caps a single module's membership size.
	MaxItemsPerModule = 200
	// MaxTitleLength caps a module's display title.
	MaxTitleLength = 200
)

var (
	// ErrInvalidArgument signals a guard-clause failure in a constructor/mutator.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound is the repository sentinel for a missing/soft-deleted module.
	ErrNotFound = errors.New("module not found")
	// ErrItemNotFound is returned when a module item id is not a current member.
	ErrItemNotFound = errors.New("module item not found")
	// ErrDuplicateItem is returned by AddItem when the content item is already in.
	ErrDuplicateItem = errors.New("content item already in module")
	// ErrCapExceeded is returned by AddItem when MaxItemsPerModule is reached.
	ErrCapExceeded = errors.New("module has reached the item cap")
	// ErrDeleted is returned by mutators on a soft-deleted module.
	ErrDeleted = errors.New("module is soft-deleted; cannot mutate")
	// ErrInvalidReorder is returned by ReorderItems when the id list is not an
	// exact permutation of the current active item ids.
	ErrInvalidReorder = errors.New("reorder list is not a permutation of current items")
	// ErrInvalidRequirement is returned when a completion requirement is malformed
	// for the module's current item set.
	ErrInvalidRequirement = errors.New("invalid module completion requirement")
	// ErrRequirementViolation is returned when an item mutation would break the
	// module's existing completion requirement (fail-loud; the caller must adjust
	// the requirement first).
	ErrRequirementViolation = errors.New("operation would violate the module completion requirement")
)

// -----------------------------------------------------------------------------
// ModuleRequirement — completion-rule value object (1:1 with a Module)
// -----------------------------------------------------------------------------

// ModuleRequirement is the module's completion rule. It is a value object owned
// by its Module (no identity, no independent lifecycle) — persisted inline on
// the module row.
//
//   - all_items       : ThresholdN == 0, RequiredItemIDs empty.
//   - n_of_m          : 0 < ThresholdN <= (active item count), RequiredItemIDs empty.
//   - specific_items  : ThresholdN == 0, RequiredItemIDs a non-empty, de-duplicated
//     subset of the module's active content-item ids.
type ModuleRequirement struct {
	Kind            RequirementKind `json:"kind"`
	ThresholdN      int             `json:"threshold_n"`
	RequiredItemIDs []string        `json:"required_item_ids"`
}

// DefaultRequirement is the all_items rule a fresh module starts with.
func DefaultRequirement() ModuleRequirement {
	return ModuleRequirement{Kind: RequirementAllItems}
}

// Validate checks the requirement against the supplied active item set (the
// items it would apply to). Pure — no I/O.
func (r ModuleRequirement) Validate(items []*ModuleItem) error {
	if !r.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidRequirement, string(r.Kind))
	}
	switch r.Kind {
	case RequirementAllItems:
		if r.ThresholdN != 0 {
			return fmt.Errorf("%w: all_items must not set threshold_n", ErrInvalidRequirement)
		}
		if len(r.RequiredItemIDs) != 0 {
			return fmt.Errorf("%w: all_items must not set required_item_ids", ErrInvalidRequirement)
		}
	case RequirementNOfM:
		if len(r.RequiredItemIDs) != 0 {
			return fmt.Errorf("%w: n_of_m must not set required_item_ids", ErrInvalidRequirement)
		}
		if r.ThresholdN <= 0 || r.ThresholdN > len(items) {
			return fmt.Errorf("%w: n_of_m threshold_n must satisfy 0 < n <= item count (%d); got %d",
				ErrInvalidRequirement, len(items), r.ThresholdN)
		}
	case RequirementSpecificItems:
		if r.ThresholdN != 0 {
			return fmt.Errorf("%w: specific_items must not set threshold_n", ErrInvalidRequirement)
		}
		if len(r.RequiredItemIDs) == 0 {
			return fmt.Errorf("%w: specific_items requires at least one required_item_id", ErrInvalidRequirement)
		}
		present := make(map[string]bool, len(items))
		for _, it := range items {
			present[it.ContentItemID] = true
		}
		seen := make(map[string]bool, len(r.RequiredItemIDs))
		for _, id := range r.RequiredItemIDs {
			if seen[id] {
				return fmt.Errorf("%w: duplicate required_item_id %s", ErrInvalidRequirement, id)
			}
			seen[id] = true
			if !present[id] {
				return fmt.Errorf("%w: required_item_id %s is not an item in this module", ErrInvalidRequirement, id)
			}
		}
	}
	return nil
}

// IsSatisfiedBy reports whether the learner's set of completed content-item ids
// satisfies this requirement against the module's CURRENT active items. Pure —
// no I/O — so a StudentModuleProgress projection (a different aggregate) can
// reuse the completion semantics without duplicating them.
//
//   - all_items      : the module is NON-EMPTY and every active item is completed
//     (an empty module is never "complete" — nothing has been achieved);
//   - n_of_m         : at least ThresholdN of the module's CURRENT items are
//     completed (a completion for an id that is no longer a member does not
//     count — the rule is about the module's items, not history);
//   - specific_items : every RequiredItemID is completed.
//
// An unknown/malformed kind is never satisfied (fail-closed).
func (r ModuleRequirement) IsSatisfiedBy(items []*ModuleItem, completed map[string]bool) bool {
	switch r.Kind {
	case RequirementAllItems:
		if len(items) == 0 {
			return false
		}
		for _, it := range items {
			if !completed[it.ContentItemID] {
				return false
			}
		}
		return true
	case RequirementNOfM:
		if r.ThresholdN <= 0 {
			return false
		}
		n := 0
		for _, it := range items {
			if completed[it.ContentItemID] {
				n++
			}
		}
		return n >= r.ThresholdN
	case RequirementSpecificItems:
		if len(r.RequiredItemIDs) == 0 {
			return false
		}
		for _, id := range r.RequiredItemIDs {
			if !completed[id] {
				return false
			}
		}
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Aggregate root + child entity
// -----------------------------------------------------------------------------

// ModuleItem is a child entity of a Module: one ordered membership entry that
// references a course_content.ContentItem by UUID. Accessed only through its
// Module root.
type ModuleItem struct {
	ID            string     `json:"id"`
	ModuleID      string     `json:"module_id"`
	ContentItemID string     `json:"content_item_id"`
	Position      int        `json:"position"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
}

// Module is the course-structure aggregate root: a named, ordered group of
// content-item references within a Course, plus a completion Requirement.
type Module struct {
	ID          string            `json:"id"`
	TenantID    string            `json:"tenant_id"`
	CourseID    string            `json:"course_id"`
	Title       string            `json:"title"`
	Position    int               `json:"position"`
	Items       []*ModuleItem     `json:"items"`
	Requirement ModuleRequirement `json:"requirement"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	DeletedAt   *time.Time        `json:"deleted_at,omitempty"`
}

// NewParams is the constructor input for New.
type NewParams struct {
	TenantID string
	CourseID string
	Title    string
}

// New constructs an empty, active module with the default (all_items)
// requirement. Position is left 0 — the persistence port assigns the dense
// within-course position on Create.
func New(p NewParams) (*Module, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(p.CourseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: title required", ErrInvalidArgument)
	}
	if len(title) > MaxTitleLength {
		return nil, fmt.Errorf("%w: title too long (%d > %d)", ErrInvalidArgument, len(title), MaxTitleLength)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	return &Module{
		ID:          id.String(),
		TenantID:    p.TenantID,
		CourseID:    p.CourseID,
		Title:       title,
		Position:    0,
		Items:       nil,
		Requirement: DefaultRequirement(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// IsActive reports whether the module is not soft-deleted.
func (m *Module) IsActive() bool { return m.DeletedAt == nil }

// AddItem appends a content-item reference at the next free position. Enforces
// module-active, ref shape (UUID), cap, and content-item uniqueness.
func (m *Module) AddItem(contentItemID string) (*ModuleItem, error) {
	if m.DeletedAt != nil {
		return nil, ErrDeleted
	}
	cid := strings.TrimSpace(contentItemID)
	if cid == "" {
		return nil, fmt.Errorf("%w: content_item_id required", ErrInvalidArgument)
	}
	if _, err := uuid.Parse(cid); err != nil {
		return nil, fmt.Errorf("%w: content_item_id must be a UUID", ErrInvalidArgument)
	}
	if len(m.Items) >= MaxItemsPerModule {
		return nil, fmt.Errorf("%w (cap=%d)", ErrCapExceeded, MaxItemsPerModule)
	}
	for _, it := range m.Items {
		if it.ContentItemID == cid {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateItem, cid)
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	item := &ModuleItem{
		ID:            id.String(),
		ModuleID:      m.ID,
		ContentItemID: cid,
		Position:      len(m.Items),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	m.Items = append(m.Items, item)
	m.UpdatedAt = now
	return item, nil
}

// RemoveItem soft-deletes the item and re-compacts the remaining positions to
// stay dense. It first checks the removal keeps the completion Requirement
// satisfiable (fail-loud via ErrRequirementViolation) — the aggregate never
// silently leaves a rule referencing content that no longer exists.
//
// Returns the removed item (soft-deleted, retaining its pre-removal Position so
// a persistence adapter can shift the trailing rows). The referenced
// ContentItem is never touched (cross-aggregate).
func (m *Module) RemoveItem(itemID string) (*ModuleItem, error) {
	if m.DeletedAt != nil {
		return nil, ErrDeleted
	}
	idx := -1
	for i, it := range m.Items {
		if it.ID == itemID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf("%w: %s", ErrItemNotFound, itemID)
	}
	removed := m.Items[idx]

	// Would-be remaining set — validate the requirement against it BEFORE mutating.
	remaining := make([]*ModuleItem, 0, len(m.Items)-1)
	remaining = append(remaining, m.Items[:idx]...)
	remaining = append(remaining, m.Items[idx+1:]...)
	if err := m.Requirement.Validate(remaining); err != nil {
		return nil, fmt.Errorf("%w: removing item %s: %v", ErrRequirementViolation, itemID, err)
	}

	now := time.Now().UTC()
	removed.DeletedAt = &now
	removed.UpdatedAt = now
	m.Items = remaining
	for i, it := range m.Items {
		if it.Position != i {
			it.Position = i
			it.UpdatedAt = now
		}
	}
	m.UpdatedAt = now
	return removed, nil
}

// ReorderItems applies an explicit full permutation of the current active item
// ids, re-stamping dense positions.
func (m *Module) ReorderItems(orderedItemIDs []string) error {
	if m.DeletedAt != nil {
		return ErrDeleted
	}
	if len(orderedItemIDs) != len(m.Items) {
		return fmt.Errorf("%w: got %d ids for %d items", ErrInvalidReorder, len(orderedItemIDs), len(m.Items))
	}
	byID := make(map[string]*ModuleItem, len(m.Items))
	for _, it := range m.Items {
		byID[it.ID] = it
	}
	seen := make(map[string]bool, len(orderedItemIDs))
	reordered := make([]*ModuleItem, 0, len(orderedItemIDs))
	now := time.Now().UTC()
	for _, id := range orderedItemIDs {
		it, ok := byID[id]
		if !ok || seen[id] {
			return fmt.Errorf("%w: %s", ErrInvalidReorder, id)
		}
		seen[id] = true
		if it.Position != len(reordered) {
			it.Position = len(reordered)
			it.UpdatedAt = now
		}
		reordered = append(reordered, it)
	}
	m.Items = reordered
	m.UpdatedAt = now
	return nil
}

// SetRequirement replaces the completion rule after validating it against the
// module's current active items. A rejected requirement leaves the module
// unchanged (fail-loud, atomic).
func (m *Module) SetRequirement(req ModuleRequirement) error {
	if m.DeletedAt != nil {
		return ErrDeleted
	}
	if err := req.Validate(m.Items); err != nil {
		return err
	}
	m.Requirement = req
	m.UpdatedAt = time.Now().UTC()
	return nil
}

// SoftDelete marks the module deleted and cascades the soft-delete to its items
// (WITHIN the aggregate boundary only). Idempotent. Never touches referenced
// ContentItems (a different aggregate).
func (m *Module) SoftDelete() {
	if m.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	m.DeletedAt = &now
	m.UpdatedAt = now
	for _, it := range m.Items {
		if it.DeletedAt == nil {
			it.DeletedAt = &now
			it.UpdatedAt = now
		}
	}
}

// ContentItemIDs returns the module's active content-item references in order —
// a convenience for callers assembling a StudentModuleProgress projection (a
// deferred follow-up) or rendering the structure.
func (m *Module) ContentItemIDs() []string {
	out := make([]string, 0, len(m.Items))
	for _, it := range m.Items {
		out = append(out, it.ContentItemID)
	}
	return out
}
