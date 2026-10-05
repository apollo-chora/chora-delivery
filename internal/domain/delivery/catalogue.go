// Catalogue — public course discovery surface (Phyllis MVP, Comic Ch5 P10).
//
// Adds a marketplace-flavoured projection on top of the Course aggregate:
//
//   - PublicCourse — discovery DTO carrying instructor display name,
//     enrolled count, price, tags, and syllabus outline.
//   - Catalogue — in-memory store with public/private filter, free-text
//     search across title + tags, and 1-indexed pagination.
//
// Owns no aggregates beyond what `delivery.go` already declares; this is
// a marketplace projection. The full read-model lives in `chora_delivery`
// and is rebuilt from CourseCreated / EnrollmentCreated events post-M12.
//
// Soft-delete invisibility (per .claude/rules/ddd-enforcement.md §5): rows
// with DeletedAt != nil never surface in Search() / Get() results.
package delivery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// CataloguePort — the persistence port the HTTP handlers depend on
// -----------------------------------------------------------------------------

// CataloguePort is the hexagonal port for the public course catalogue.
//
// The HTTP adapter (v1_handlers.go + handlers.go + applications.go) depends
// ONLY on this interface — never on a concrete store. Two adapters satisfy
// it:
//
//   - InMemCatalogue (this package) — wraps the in-memory Catalogue; used
//     for unit tests + local dev when no Postgres pool is wired.
//   - pg.CatalogueRepo (internal/adapter/repo/pg) — the production adapter
//     backed by chora_delivery. The public course catalogue is DB-backed
//     and survives pod restarts; the in-memory store is dev-only.
//
// Every method takes a context.Context so the pg adapter can apply RLS
// (rls.ApplySession) for tenant-scoped reads. The public-discovery read
// path (VisibilityFilterPublic / empty TenantID) goes through the
// public_courses_catalog VIEW, which is cross-tenant by design and needs
// no tenant context — the adapter handles that branch internally.
//
// All methods return an error so DB failures propagate instead of being
// silently swallowed; the in-memory adapter always returns nil.
type CataloguePort interface {
	// Save inserts or upserts a public course (idempotent on course_id).
	Save(ctx context.Context, pc *PublicCourse) error
	// Get returns the public course with the given ID. ok=false when no
	// matching row exists (or it is soft-deleted).
	Get(ctx context.Context, id string) (*PublicCourse, bool, error)
	// Search returns the courses matching the query plus the total count
	// (pre-pagination), 1-indexed page/per pagination.
	Search(ctx context.Context, q CatalogueQuery) ([]*PublicCourse, int, error)
	// SearchCursor returns a Relay-style cursor-paginated page.
	SearchCursor(ctx context.Context, q CatalogueQuery) (CursorPage, error)
	// ListByInstructor returns the courses where instructor_gcid matches the
	// argument, RLS-scoped to the caller's tenant context. Soft-deleted rows
	// are excluded. Pagination is 1-indexed (page,per) and mirrors Search.
	//
	// Returns the page slice + total count (pre-pagination). Empty result is
	// (nil, 0, nil) — never an error.
	//
	// Closes debt #4 / A6: FE instructor-roster surface needs a single by-
	// instructor read instead of fetching the catalogue + client-filtering.
	ListByInstructor(ctx context.Context, tenantID, instructorGCID string, page, per int) ([]*PublicCourse, int, error)
}

// -----------------------------------------------------------------------------
// InMemCatalogue — in-memory adapter satisfying CataloguePort
// -----------------------------------------------------------------------------

// InMemCatalogue adapts the in-memory Catalogue to CataloguePort.
//
// It is a thin shim: ctx is ignored (no RLS in memory) and error is always
// nil. Kept so unit tests + local dev (no Postgres pool) get a clean port
// implementation without the production pg adapter. Production wiring uses
// pg.CatalogueRepo instead — see cmd/server/main.go.
type InMemCatalogue struct {
	cat *Catalogue
}

// NewInMemCatalogue returns an InMemCatalogue around a fresh empty Catalogue.
func NewInMemCatalogue() *InMemCatalogue {
	return &InMemCatalogue{cat: NewCatalogue()}
}

// NewInMemCatalogueFrom wraps an existing Catalogue (e.g., a test fixture
// pre-seeded via Catalogue.Save).
func NewInMemCatalogueFrom(cat *Catalogue) *InMemCatalogue {
	return &InMemCatalogue{cat: cat}
}

// Underlying returns the wrapped Catalogue — for tests that need the
// concrete store's seed helpers.
func (a *InMemCatalogue) Underlying() *Catalogue { return a.cat }

// Save satisfies CataloguePort.
func (a *InMemCatalogue) Save(_ context.Context, pc *PublicCourse) error {
	a.cat.Save(pc)
	return nil
}

// Get satisfies CataloguePort.
func (a *InMemCatalogue) Get(_ context.Context, id string) (*PublicCourse, bool, error) {
	pc, ok := a.cat.Get(id)
	return pc, ok, nil
}

// Search satisfies CataloguePort.
func (a *InMemCatalogue) Search(_ context.Context, q CatalogueQuery) ([]*PublicCourse, int, error) {
	items, total := a.cat.Search(q)
	return items, total, nil
}

// SearchCursor satisfies CataloguePort.
func (a *InMemCatalogue) SearchCursor(_ context.Context, q CatalogueQuery) (CursorPage, error) {
	return a.cat.SearchCursor(q), nil
}

// ListByInstructor satisfies CataloguePort.
func (a *InMemCatalogue) ListByInstructor(_ context.Context, tenantID, instructorGCID string, page, per int) ([]*PublicCourse, int, error) {
	items, total := a.cat.ListByInstructor(tenantID, instructorGCID, page, per)
	return items, total, nil
}

// -----------------------------------------------------------------------------
// Visibility — public catalogue access scope
// -----------------------------------------------------------------------------

// Visibility expresses the cross-tenant access scope for a Course /
// PublicCourse. It supersedes the legacy boolean `Public` flag (which
// remains as a denorm derived from `Visibility == VisibilityPublic`).
//
// Three values:
//   - VisibilityPrivate    : only the creator + invited members
//   - VisibilityTenantOnly : every member of the same tenant
//   - VisibilityPublic     : visible cross-tenant (e.g., Mr. Chen's CSM-Prep)
type Visibility string

const (
	VisibilityPrivate    Visibility = "private"
	VisibilityTenantOnly Visibility = "tenant_only"
	VisibilityPublic     Visibility = "public"
)

// IsValid reports whether v is a recognised value.
func (v Visibility) IsValid() bool {
	switch v {
	case VisibilityPrivate, VisibilityTenantOnly, VisibilityPublic:
		return true
	default:
		return false
	}
}

// VisibilityFilter governs the visibility projection for catalogue queries.
type VisibilityFilter int

const (
	// VisibilityFilterAny returns courses regardless of visibility.
	VisibilityFilterAny VisibilityFilter = iota
	// VisibilityFilterPublic returns ONLY courses with Visibility=public.
	VisibilityFilterPublic
	// VisibilityFilterTenantOrPublic returns tenant_only + public for the
	// tenant scope passed in CatalogueQuery.TenantID (cross-tenant aware:
	// tenant_only OUTSIDE the active tenant is filtered out).
	VisibilityFilterTenantOrPublic
)

// -----------------------------------------------------------------------------
// PublicCourse
// -----------------------------------------------------------------------------

// PublicCourse is the marketplace-facing read model for a Course.
//
// Cross-domain references travel as UUIDs without FK (per ddd-enforcement
// invariant #3): InstructorGCID is materialised by chora-identity event
// subscribers; InstructorName is denormalised here for low-latency reads
// and refreshed on identity update.
type PublicCourse struct {
	ID              string
	TenantID        string
	Title           string
	InstructorGCID  string
	InstructorName  string
	PriceSGDCents   int32
	Public          bool       // legacy denorm — kept in sync with Visibility==Public
	Visibility      Visibility // canonical scope (since S4.3)
	SFEligible      bool
	Tags            []string
	SyllabusOutline []string
	EnrolledCount   int32
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// NewPublicCourseInput is the value-bag for NewPublicCourse to keep the
// constructor signature stable while the schema evolves.
//
// Visibility precedence (S4.3):
//
//  1. Visibility set explicitly → used as-is
//  2. Visibility empty + Public=true → coerce to VisibilityPublic
//  3. Visibility empty + Public=false → default VisibilityTenantOnly
type NewPublicCourseInput struct {
	TenantID        string
	Title           string
	InstructorGCID  string
	InstructorName  string
	PriceSGDCents   int32
	Public          bool
	Visibility      Visibility // optional; defaults via the rules above
	SFEligible      bool
	Tags            []string
	SyllabusOutline []string
}

// NewPublicCourse constructs a PublicCourse with default values populated.
//
// Validation guards (rejected with ErrInvalidArgument):
//   - tenant_id required
//   - title trim-non-empty
//   - instructor_gcid required
//   - price_sgd_cents >= 0 (free courses are price=0; negative is invalid).
//   - if Visibility is set, it must be one of the recognised values.
func NewPublicCourse(in NewPublicCourseInput) (*PublicCourse, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: title required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.InstructorGCID) == "" {
		return nil, fmt.Errorf("%w: instructor_gcid required", ErrInvalidArgument)
	}
	if in.PriceSGDCents < 0 {
		return nil, fmt.Errorf("%w: price_sgd_cents must be >= 0", ErrInvalidArgument)
	}
	visibility := in.Visibility
	if visibility == "" {
		// No explicit visibility — coerce from the legacy Public flag.
		if in.Public {
			visibility = VisibilityPublic
		} else {
			visibility = VisibilityTenantOnly
		}
	}
	if !visibility.IsValid() {
		return nil, fmt.Errorf("%w: visibility must be one of private|tenant_only|public", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &PublicCourse{
		ID:              NewUUIDv7(),
		TenantID:        in.TenantID,
		Title:           in.Title,
		InstructorGCID:  in.InstructorGCID,
		InstructorName:  in.InstructorName,
		PriceSGDCents:   in.PriceSGDCents,
		Public:          visibility == VisibilityPublic,
		Visibility:      visibility,
		SFEligible:      in.SFEligible,
		Tags:            append([]string(nil), in.Tags...),
		SyllabusOutline: append([]string(nil), in.SyllabusOutline...),
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// SetVisibility transitions the course to a new visibility scope.
//
// Returns true ONLY when the transition crosses INTO VisibilityPublic from
// a non-public scope — that's the published-signal the HTTP handler uses to
// fan-out a chora.delivery.course.published.v1 event (chora-sharing
// subscribes for the discovery feed).
func (pc *PublicCourse) SetVisibility(next Visibility) bool {
	if !next.IsValid() {
		return false
	}
	if pc.Visibility == next {
		return false
	}
	wasPublic := pc.Visibility == VisibilityPublic
	pc.Visibility = next
	pc.Public = next == VisibilityPublic
	pc.UpdatedAt = time.Now().UTC()
	// Emit only on private/tenant_only -> public.
	return !wasPublic && next == VisibilityPublic
}

// IsFree returns true when the course has no price.
func (pc *PublicCourse) IsFree() bool { return pc.PriceSGDCents == 0 }

// SyllabusOutlineCount returns the number of syllabus outline items.
func (pc *PublicCourse) SyllabusOutlineCount() int { return len(pc.SyllabusOutline) }

// SoftDelete marks the public course deleted (preserves history).
func (pc *PublicCourse) SoftDelete() {
	if pc.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	pc.DeletedAt = &now
}

// IncrementEnrolled bumps EnrolledCount; called by Catalogue when a fresh
// EnrollmentCreated row lands. Negative deltas are rejected to keep the
// invariant simple — un-enroll cascades through SoftDelete elsewhere.
func (pc *PublicCourse) IncrementEnrolled() {
	pc.EnrolledCount++
	pc.UpdatedAt = time.Now().UTC()
}

// -----------------------------------------------------------------------------
// CatalogueQuery + filter enums
// -----------------------------------------------------------------------------

// PublicFilter governs the public flag projection.
type PublicFilter int

const (
	// PublicFilterAny returns courses regardless of public flag.
	PublicFilterAny PublicFilter = iota
	// PublicFilterTrue returns only courses with Public=true.
	PublicFilterTrue
	// PublicFilterFalse returns only courses with Public=false.
	PublicFilterFalse
)

// CatalogueQuery is the search input shape for Catalogue.Search.
//
// Pagination is 1-indexed: Page=1 returns the first Per items.
// Page=0 / Per=0 default to Page=1, Per=20.
//
// Visibility precedence over the legacy Public field:
//
//  1. Visibility != VisibilityFilterAny → applies the visibility rules
//     (and TenantID, when set, scopes tenant_only courses).
//  2. Visibility == VisibilityFilterAny + Public=PublicFilter{True|False}
//     → falls back to legacy boolean filter (kept for backwards compat).
//
// Cross-tenant catalogue access:
//   - VisibilityFilterPublic ignores TenantID for catalogue rows whose
//     Visibility == VisibilityPublic — public courses surface across tenants.
type CatalogueQuery struct {
	TenantID   string           // optional tenant scope (see precedence)
	Public     PublicFilter     // legacy public/private/any
	Visibility VisibilityFilter // canonical scope — supersedes Public when non-default
	Q          string           // free-text query — case-insensitive substring on title + tags
	Page       int              // 1-indexed page number
	Per        int              // page size
	// Cursor (Relay-style) — opaque ID; if set, returns rows with ID > cursor.
	// Mutually exclusive with Page/Per (cursor wins when set).
	AfterCursor string
	First       int // page size for cursor pagination (1..200)
}

// -----------------------------------------------------------------------------
// Catalogue — in-memory store with marketplace queries
// -----------------------------------------------------------------------------

// Catalogue is an in-memory PublicCourse store with marketplace queries.
//
// Production swaps to a chora_delivery Postgres adapter at M12; the
// Search() contract stays stable.
type Catalogue struct {
	mu sync.RWMutex
	by map[string]*PublicCourse // key = course_id
}

// NewCatalogue returns an empty catalogue.
func NewCatalogue() *Catalogue {
	return &Catalogue{by: make(map[string]*PublicCourse)}
}

// Save inserts or upserts a public course. EnrolledCount is preserved on
// upsert; the caller is expected to have already mutated the value.
func (c *Catalogue) Save(pc *PublicCourse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.by[pc.ID] = pc
}

// Get returns the public course with the given ID and a bool ok flag.
// Soft-deleted courses are NOT returned (per ddd-enforcement §5).
func (c *Catalogue) Get(id string) (*PublicCourse, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pc, ok := c.by[id]
	if !ok || pc.DeletedAt != nil {
		return nil, false
	}
	return pc, true
}

// ListByInstructor returns the courses owned by an instructor inside a
// tenant. Stable-sorted by course_id (UUIDv7 ⇒ creation order). 1-indexed
// (page, per) pagination mirrors Search. tenant + instructor must both be
// non-empty (an empty instructor would conflate with "any" — the SQL repo
// would reject it; mirror that here so the in-memory adapter matches).
func (c *Catalogue) ListByInstructor(tenantID, instructorGCID string, page, per int) ([]*PublicCourse, int) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(instructorGCID) == "" {
		return []*PublicCourse{}, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	matches := make([]*PublicCourse, 0, len(c.by))
	for _, pc := range c.by {
		if pc.DeletedAt != nil {
			continue
		}
		if pc.TenantID != tenantID || pc.InstructorGCID != instructorGCID {
			continue
		}
		matches = append(matches, pc)
	}
	sort.Slice(matches, func(i, j int) bool {
		return strings.Compare(matches[i].ID, matches[j].ID) < 0
	})
	total := len(matches)
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = 20
	}
	start := (page - 1) * per
	if start >= total {
		return []*PublicCourse{}, total
	}
	end := start + per
	if end > total {
		end = total
	}
	return matches[start:end], total
}

// Search returns the courses matching the query plus the total count
// (pre-pagination). Sort order is stable by ID (UUIDv7 ⇒ creation order).
func (c *Catalogue) Search(q CatalogueQuery) ([]*PublicCourse, int) {
	matches := c.filtered(q)

	sort.Slice(matches, func(i, j int) bool {
		return strings.Compare(matches[i].ID, matches[j].ID) < 0
	})

	total := len(matches)

	page := q.Page
	if page < 1 {
		page = 1
	}
	per := q.Per
	if per < 1 {
		per = 20
	}
	start := (page - 1) * per
	if start >= total {
		return []*PublicCourse{}, total
	}
	end := start + per
	if end > total {
		end = total
	}
	return matches[start:end], total
}

// CursorPage is the Relay-style page-info envelope.
type CursorPage struct {
	Items       []*PublicCourse
	HasNextPage bool
	EndCursor   string // ID of the last item returned ("" when no items)
	Total       int    // total matching rows (pre-pagination)
}

// SearchCursor returns a Relay-style cursor-paginated page.
//
// Rules:
//   - q.First clamps to 1..200; default 20 when 0/negative.
//   - q.AfterCursor (if set) returns rows with ID > cursor. The empty
//     cursor returns the first page.
//   - HasNextPage = true if there are more rows after EndCursor.
//   - EndCursor is the last item's ID (or "" on empty results).
//   - Total is the total number of matches BEFORE pagination — useful for
//     UI disclosure ("12 results"); HasNextPage is the canonical drive
//     for fetch-more.
func (c *Catalogue) SearchCursor(q CatalogueQuery) CursorPage {
	all := c.filtered(q)
	sort.Slice(all, func(i, j int) bool {
		return strings.Compare(all[i].ID, all[j].ID) < 0
	})
	total := len(all)

	first := q.First
	if first < 1 {
		first = 20
	}
	if first > 200 {
		first = 200
	}

	startIdx := 0
	if q.AfterCursor != "" {
		// Linear scan is fine for in-memory fixture; production swaps to
		// indexed lookup at M12+. Find the smallest idx whose ID > cursor.
		for i, pc := range all {
			if strings.Compare(pc.ID, q.AfterCursor) > 0 {
				startIdx = i
				break
			}
			startIdx = len(all) // cursor past everything
		}
	}
	if startIdx >= total {
		return CursorPage{Items: []*PublicCourse{}, HasNextPage: false, EndCursor: "", Total: total}
	}
	end := startIdx + first
	if end > total {
		end = total
	}
	out := all[startIdx:end]
	endCursor := ""
	if len(out) > 0 {
		endCursor = out[len(out)-1].ID
	}
	return CursorPage{
		Items:       out,
		HasNextPage: end < total,
		EndCursor:   endCursor,
		Total:       total,
	}
}

// filtered is the shared filter pipeline used by both pagination modes.
func (c *Catalogue) filtered(q CatalogueQuery) []*PublicCourse {
	c.mu.RLock()
	defer c.mu.RUnlock()
	matches := make([]*PublicCourse, 0, len(c.by))
	for _, pc := range c.by {
		if pc.DeletedAt != nil {
			continue
		}
		if !visibilityMatch(pc, q) {
			continue
		}
		// Legacy Public filter — applied only when the canonical
		// Visibility filter is "Any" (preserves backwards compat with
		// the existing /courses?public=true|false handler).
		if q.Visibility == VisibilityFilterAny {
			switch q.Public {
			case PublicFilterTrue:
				if !pc.Public {
					continue
				}
			case PublicFilterFalse:
				if pc.Public {
					continue
				}
			}
		}
		if q.Q != "" && !matchQ(pc, q.Q) {
			continue
		}
		matches = append(matches, pc)
	}
	return matches
}

// visibilityMatch implements the cross-tenant visibility rules.
//
//   - VisibilityFilterAny:        TenantID, when set, must match.
//   - VisibilityFilterPublic:     ONLY public rows; TenantID is ignored
//     (public catalogue is cross-tenant).
//   - VisibilityFilterTenantOrPublic: tenant_only rows must match the
//     active TenantID; public rows always.
func visibilityMatch(pc *PublicCourse, q CatalogueQuery) bool {
	switch q.Visibility {
	case VisibilityFilterAny:
		if q.TenantID != "" && pc.TenantID != q.TenantID {
			return false
		}
		return true
	case VisibilityFilterPublic:
		return pc.Visibility == VisibilityPublic
	case VisibilityFilterTenantOrPublic:
		if pc.Visibility == VisibilityPublic {
			return true
		}
		if pc.Visibility == VisibilityTenantOnly {
			return q.TenantID == "" || pc.TenantID == q.TenantID
		}
		// VisibilityPrivate is never returned in this filter mode.
		return false
	default:
		return true
	}
}

// matchQ is a case-insensitive substring search over title + tags.
func matchQ(pc *PublicCourse, q string) bool {
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(pc.Title), needle) {
		return true
	}
	for _, t := range pc.Tags {
		if strings.Contains(strings.ToLower(t), needle) {
			return true
		}
	}
	return false
}
