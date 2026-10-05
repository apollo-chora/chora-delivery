// Package rostering owns the class-roster aggregate for chora-delivery.
//
// Scope: assign learners (GCIDs) to a Class in bulk, dedupe within the
// roster, enforce capacity, support unassign + soft-delete. Cross-domain
// references (class_id, learner_gcid) travel as opaque UUIDs without FK
// constraints (per .claude/rules/ddd-enforcement.md aggregate-invariant #3).
//
// Hexagonal: pure domain. UUIDv7 IDs, soft delete, mutex-guarded mutations.
package rostering

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	// ErrInvalidArgument signals a guard-clause failure.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrRosterAtCapacity is returned by AssignBulk when adding the next
	// learner would exceed Roster.MaxCapacity.
	ErrRosterAtCapacity = errors.New("roster at capacity")
)

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7.
func NewUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Roster pairs a Class with its assigned learner GCIDs.
type Roster struct {
	ID          string
	TenantID    string
	ClassID     string
	MaxCapacity int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time

	mu       sync.RWMutex
	learners map[string]time.Time // gcid -> assignedAt
}

// NewRoster constructs an empty Roster bound to (tenant, class).
//
// Validation:
//   - tenant_id, class_id required
//   - capacity > 0.
func NewRoster(tenantID, classID string, capacity int) (*Roster, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(classID) == "" {
		return nil, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	if capacity <= 0 {
		return nil, fmt.Errorf("%w: capacity must be > 0", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Roster{
		ID:          NewUUIDv7(),
		TenantID:    tenantID,
		ClassID:     classID,
		MaxCapacity: capacity,
		CreatedAt:   now,
		UpdatedAt:   now,
		learners:    make(map[string]time.Time),
	}, nil
}

// AssignBulk adds gcids to the roster, deduplicating within-call AND against
// existing assignments. Returns the slice of NEWLY-added GCIDs.
//
// Errors:
//   - any blank gcid → ErrInvalidArgument (validated up-front; nothing added)
//   - if total post-add count would exceed MaxCapacity → ErrRosterAtCapacity
//     (nothing added, atomic).
func (r *Roster) AssignBulk(gcids []string) ([]string, error) {
	// Up-front validation: reject blanks BEFORE acquiring the lock.
	for _, g := range gcids {
		if strings.TrimSpace(g) == "" {
			return nil, fmt.Errorf("%w: empty gcid in batch", ErrInvalidArgument)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Compute set of new (not-yet-rostered) gcids while preserving input order.
	seen := make(map[string]struct{}, len(gcids))
	var newOnes []string
	for _, g := range gcids {
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		if _, exists := r.learners[g]; exists {
			continue
		}
		newOnes = append(newOnes, g)
	}
	if len(r.learners)+len(newOnes) > r.MaxCapacity {
		return nil, fmt.Errorf("%w: roster_size=%d capacity=%d would_add=%d",
			ErrRosterAtCapacity, len(r.learners), r.MaxCapacity, len(newOnes))
	}
	now := time.Now().UTC()
	for _, g := range newOnes {
		r.learners[g] = now
	}
	if len(newOnes) > 0 {
		r.UpdatedAt = now
	}
	return newOnes, nil
}

// Unassign removes a learner. Returns true if a removal happened.
func (r *Roster) Unassign(gcid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.learners[gcid]; !ok {
		return false
	}
	delete(r.learners, gcid)
	r.UpdatedAt = time.Now().UTC()
	return true
}

// Has reports whether gcid is currently rostered.
func (r *Roster) Has(gcid string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.learners[gcid]
	return ok
}

// Size returns the count of rostered learners.
func (r *Roster) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.learners)
}

// List returns a COPY of the rostered GCIDs (sorted ASCII for determinism).
//
// We always return a fresh slice — callers may mutate the returned value
// without affecting the roster.
func (r *Roster) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.learners))
	for g := range r.learners {
		out = append(out, g)
	}
	sortStrings(out)
	return out
}

// SoftDelete marks the roster deleted; idempotent.
func (r *Roster) SoftDelete() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	r.DeletedAt = &now
}

// sortStrings is a tiny ASCII sort to keep the package dependency-free.
func sortStrings(s []string) {
	// Insertion sort — bounded N (capacity is room-sized).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
