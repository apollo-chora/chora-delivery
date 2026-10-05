// Package directory holds the chora-delivery user-directory projection — a
// tenant-agnostic GCID → display-name read-model kept fresh from the identity
// domain's chora.identity.user.profile_updated.v1 event (the Q3 name
// projection, delivery side).
//
// Why a local read-model (not a JOIN to chora_identity): cross-DB queries are
// FORBIDDEN across the 13-DB topology (.claude/rules/ddd-enforcement.md HARD
// RULE #1). chora-delivery therefore maintains its own copy of the display
// names it renders (course rosters), updated over Pub/Sub rather than read
// cross-domain.
//
// Tenant-agnostic by design: the directory is keyed on the GLOBAL GCID (opaque
// UUIDv7, no tenant context embedded) — a display name is not tenant-scoped.
// One row per GCID. Names are only ever surfaced by a LEFT-JOIN-style stitch
// against RLS-scoped roster rows (inmem.CourseRosterRepo), so a caller sees a
// name only for GCIDs already visible in their own tenant's roster.
//
// Hexagonal: pure domain. UserDirectoryPort is the port; pg.UserDirectoryRepo
// (prod, chora_delivery.user_directory) + InMemUserDirectory (dev/tests) are
// the adapters.
package directory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrGCIDRequired is returned by Upsert when the entry carries no GCID — the
// directory is keyed on it, so an empty GCID is a fail-loud programming error
// per feedback_no_stubs_real_wiring.
var ErrGCIDRequired = errors.New("directory: user directory entry requires a non-empty gcid")

// UserDirectoryEntry is one projected row: a global GCID → display name (+
// optional email), stamped with the source updated_at that drives last-writer-
// wins reconciliation across out-of-order Pub/Sub redeliveries.
type UserDirectoryEntry struct {
	GCID        string
	DisplayName string
	Email       string
	UpdatedAt   time.Time
}

// UserDirectoryPort is the persistence port for the user-directory projection.
type UserDirectoryPort interface {
	// Upsert applies the entry last-writer-wins on UpdatedAt: an entry whose
	// UpdatedAt is NOT strictly newer than the stored row is ignored (so a
	// stale redelivery cannot clobber a newer name, and an equal-timestamp
	// replay is a no-op). Fails loud on an empty GCID.
	Upsert(ctx context.Context, entry UserDirectoryEntry) error
	// LookupNames returns a gcid → display_name map for the supplied gcids that
	// have a directory row. Absent gcids are simply not in the map. Rows with an
	// empty display_name ARE returned (as ""); the caller (the roster stitch)
	// applies the GCID fallback for empty/absent names — keeping the empty→GCID
	// policy in one place rather than baked into the lookup.
	LookupNames(ctx context.Context, gcids []string) (map[string]string, error)
}

// InMemUserDirectory is the in-memory UserDirectoryPort for dev + unit tests.
// Safe for concurrent use.
type InMemUserDirectory struct {
	mu     sync.RWMutex
	byGCID map[string]UserDirectoryEntry
}

// NewInMemUserDirectory returns an empty in-memory directory.
func NewInMemUserDirectory() *InMemUserDirectory {
	return &InMemUserDirectory{byGCID: make(map[string]UserDirectoryEntry)}
}

// Upsert applies last-writer-wins on UpdatedAt. Mirrors the pg adapter's
// `INSERT ... ON CONFLICT (gcid) DO UPDATE ... WHERE user_directory.updated_at
// < EXCLUDED.updated_at` (strict `<`, so equal-timestamp replays no-op).
func (d *InMemUserDirectory) Upsert(_ context.Context, entry UserDirectoryEntry) error {
	if strings.TrimSpace(entry.GCID) == "" {
		return ErrGCIDRequired
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.byGCID[entry.GCID]; ok && !existing.UpdatedAt.Before(entry.UpdatedAt) {
		// Stored row is same-age-or-newer — ignore (LWW + idempotent replay).
		return nil
	}
	d.byGCID[entry.GCID] = entry
	return nil
}

// LookupNames returns display names for the found gcids (see port doc).
func (d *InMemUserDirectory) LookupNames(_ context.Context, gcids []string) (map[string]string, error) {
	out := make(map[string]string, len(gcids))
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, g := range gcids {
		if e, ok := d.byGCID[g]; ok {
			out[g] = e.DisplayName
		}
	}
	return out, nil
}

// Compile-time assertion: InMemUserDirectory must satisfy UserDirectoryPort.
var _ UserDirectoryPort = (*InMemUserDirectory)(nil)
