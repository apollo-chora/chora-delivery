// Package course_content is the heterogeneous course-curriculum aggregate of
// the Content Delivery domain (CHO-1612, 2026-05-29).
//
// A Course is a COLLECTION of ordered, heterogeneous content items — atoms,
// videos, YouTube videos, documents, live classrooms, and assessments. A
// Course and an atom exist INDEPENDENTLY: a ContentItem holds a cross-aggregate
// reference (atom_id / test_set_id / classroom_id / url) — it NEVER owns atom
// content. LearningAtom remains the primary aggregate root per
// .claude/rules/ddd-enforcement.md (Aggregate Invariant #1) and CLAUDE.md §1
// ("collections in other domains query atoms; they never own them").
//
// Cross-aggregate references are UUIDs without FK constraint per ddd-enforcement
// Aggregate Invariant #3; existence is validated by the domain service against
// the owning domain before persistence.
//
// Soft-delete only (deleted_at) per Aggregate Invariant #5/#6. Positions are
// append-only; removal compacts; Reorder applies an explicit full permutation.
//
// HEXAGONAL: dependency-free w.r.t. infrastructure — only stdlib + google/uuid.
package course_content

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Kind
// -----------------------------------------------------------------------------

// Kind is the type of a course content item.
type Kind string

const (
	KindAtom          Kind = "atom"
	KindVideo         Kind = "video"
	KindYouTube       Kind = "youtube"
	KindDocument      Kind = "document"
	KindLiveClassroom Kind = "live_classroom"
	KindAssessment    Kind = "assessment"
)

// Valid reports whether k is a recognised content kind.
func (k Kind) Valid() bool {
	switch k {
	case KindAtom, KindVideo, KindYouTube, KindDocument, KindLiveClassroom, KindAssessment:
		return true
	}
	return false
}

// refIsUUID reports whether this kind references another aggregate by UUID
// (atom_id / test_set_id / classroom_id) as opposed to a URL.
func (k Kind) refIsUUID() bool {
	switch k {
	case KindAtom, KindAssessment, KindLiveClassroom:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Constants + sentinel errors
// -----------------------------------------------------------------------------

const (
	// MaxItemsPerCourse caps the curriculum size; the FE assumes this when
	// sizing the content editor.
	MaxItemsPerCourse = 500
	// MaxTitleLength caps a content item's display title.
	MaxTitleLength = 200
)

var (
	// ErrInvalidArgument signals a guard-clause failure in a constructor or mutator.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound is the repository sentinel for a missing-or-soft-deleted
	// CourseContent aggregate (no curriculum exists yet for the course).
	ErrNotFound = errors.New("course content not found")
	// ErrItemNotFound is returned by RemoveItem when the item is not a member.
	ErrItemNotFound = errors.New("content item not found")
	// ErrDuplicateItem is returned by AddItem when (kind, ref) is already present.
	ErrDuplicateItem = errors.New("content item already in course")
	// ErrCapExceeded is returned by AddItem when MaxItemsPerCourse is reached.
	ErrCapExceeded = errors.New("course content has reached the item cap")
	// ErrDeleted is returned by mutators on a soft-deleted aggregate.
	ErrDeleted = errors.New("course content is soft-deleted; cannot mutate")
	// ErrInvalidReorder is returned by Reorder when the supplied id list is not
	// an exact permutation of the current item ids.
	ErrInvalidReorder = errors.New("reorder list is not a permutation of current items")
)

// -----------------------------------------------------------------------------
// Aggregate root + child entity
// -----------------------------------------------------------------------------

// CourseContent is the curriculum aggregate root, identified by CourseID.
type CourseContent struct {
	CourseID  string         `json:"course_id"`
	TenantID  string         `json:"tenant_id"`
	Items     []*ContentItem `json:"items"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at,omitempty"`
}

// ContentItem is a child entity: one ordered, typed reference. Accessed only
// through CourseContent methods per Aggregate Invariant #2.
type ContentItem struct {
	ItemID    string    `json:"item_id"`
	CourseID  string    `json:"course_id"`
	Kind      Kind      `json:"kind"`
	Ref       string    `json:"ref"`
	Title     string    `json:"title"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

// NewParams is the constructor input for New.
type NewParams struct {
	CourseID string
	TenantID string
}

// New constructs an empty curriculum for a course.
func New(p NewParams) (*CourseContent, error) {
	if strings.TrimSpace(p.CourseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &CourseContent{
		CourseID:  p.CourseID,
		TenantID:  p.TenantID,
		Items:     nil,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// -----------------------------------------------------------------------------
// Mutators
// -----------------------------------------------------------------------------

// AddItemParams is the input to AddItem.
type AddItemParams struct {
	Kind  Kind
	Ref   string
	Title string
}

// AddItem appends a typed content item at the next free position. Enforces
// kind validity, ref shape, title bounds, cap, and (kind,ref) uniqueness.
func (c *CourseContent) AddItem(p AddItemParams) (*ContentItem, error) {
	if c.DeletedAt != nil {
		return nil, ErrDeleted
	}
	if !p.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidArgument, string(p.Kind))
	}
	ref := strings.TrimSpace(p.Ref)
	if ref == "" {
		return nil, fmt.Errorf("%w: ref required", ErrInvalidArgument)
	}
	if err := validateRef(p.Kind, ref); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: title required", ErrInvalidArgument)
	}
	if len(title) > MaxTitleLength {
		return nil, fmt.Errorf("%w: title too long (%d > %d)", ErrInvalidArgument, len(title), MaxTitleLength)
	}
	if len(c.Items) >= MaxItemsPerCourse {
		return nil, fmt.Errorf("%w (cap=%d)", ErrCapExceeded, MaxItemsPerCourse)
	}
	for _, it := range c.Items {
		if it.Kind == p.Kind && it.Ref == ref {
			return nil, fmt.Errorf("%w: %s/%s", ErrDuplicateItem, p.Kind, ref)
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuidv7: %w", err)
	}
	now := time.Now().UTC()
	item := &ContentItem{
		ItemID:    id.String(),
		CourseID:  c.CourseID,
		Kind:      p.Kind,
		Ref:       ref,
		Title:     title,
		Position:  len(c.Items),
		CreatedAt: now,
		UpdatedAt: now,
	}
	c.Items = append(c.Items, item)
	c.UpdatedAt = now
	return item, nil
}

// RemoveItem drops an item by id and compacts positions.
func (c *CourseContent) RemoveItem(itemID string) error {
	if c.DeletedAt != nil {
		return ErrDeleted
	}
	idx := -1
	for i, it := range c.Items {
		if it.ItemID == itemID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("%w: %s", ErrItemNotFound, itemID)
	}
	c.Items = append(c.Items[:idx], c.Items[idx+1:]...)
	now := time.Now().UTC()
	for i, it := range c.Items {
		it.Position = i
		it.UpdatedAt = now
	}
	c.UpdatedAt = now
	return nil
}

// Reorder applies an explicit full permutation of the current item ids.
func (c *CourseContent) Reorder(orderedItemIDs []string) error {
	if c.DeletedAt != nil {
		return ErrDeleted
	}
	if len(orderedItemIDs) != len(c.Items) {
		return fmt.Errorf("%w: got %d ids for %d items", ErrInvalidReorder, len(orderedItemIDs), len(c.Items))
	}
	byID := make(map[string]*ContentItem, len(c.Items))
	for _, it := range c.Items {
		byID[it.ItemID] = it
	}
	seen := make(map[string]bool, len(orderedItemIDs))
	reordered := make([]*ContentItem, 0, len(orderedItemIDs))
	now := time.Now().UTC()
	for _, id := range orderedItemIDs {
		it, ok := byID[id]
		if !ok || seen[id] {
			return fmt.Errorf("%w: %s", ErrInvalidReorder, id)
		}
		seen[id] = true
		it.Position = len(reordered)
		it.UpdatedAt = now
		reordered = append(reordered, it)
	}
	c.Items = reordered
	c.UpdatedAt = now
	return nil
}

// SoftDelete sets DeletedAt. Idempotent.
func (c *CourseContent) SoftDelete() error {
	if c.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	c.DeletedAt = &now
	c.UpdatedAt = now
	return nil
}

// IsActive reports whether the curriculum is not soft-deleted.
func (c *CourseContent) IsActive() bool { return c.DeletedAt == nil }

// -----------------------------------------------------------------------------
// Ref validation
// -----------------------------------------------------------------------------

// kindAcceptsUploadedBlob reports whether a kind may reference a gs:// object
// minted by POST .../content/upload-url (video + document only — youtube is
// always an external watch URL, never an uploaded blob).
func kindAcceptsUploadedBlob(k Kind) bool {
	return k == KindVideo || k == KindDocument
}

// validateRef enforces the reference shape for a kind: UUID for
// atom/assessment/live_classroom; http(s) URL for video/youtube/document, and
// additionally a gs://bucket/key object URI for uploaded video/document
// (minted by the content upload-url endpoint, resolved to a signed GET URL on
// the read-path).
func validateRef(k Kind, ref string) error {
	if k.refIsUUID() {
		if _, err := uuid.Parse(ref); err != nil {
			return fmt.Errorf("%w: %s ref must be a UUID", ErrInvalidArgument, k)
		}
		return nil
	}
	if kindAcceptsUploadedBlob(k) && strings.HasPrefix(ref, "gs://") {
		if err := validateGSObjectRef(ref); err != nil {
			return fmt.Errorf("%w: %s %v", ErrInvalidArgument, k, err)
		}
		return nil
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: %s ref must be an http(s) URL or gs:// object URI", ErrInvalidArgument, k)
	}
	return nil
}

// validateGSObjectRef checks gs://{bucket}/{key} shape: a non-empty bucket AND
// a non-empty object key. Pure shape validation — existence is not checked.
func validateGSObjectRef(ref string) error {
	const scheme = "gs://"
	rest := ref[len(scheme):]
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i >= len(rest)-1 {
		return fmt.Errorf("malformed gs:// URI (want gs://bucket/key)")
	}
	return nil
}
