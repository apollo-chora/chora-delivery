// Package campusops is the BE-CO2 Campus Operations skeleton.
//
// Per S4.3 brief (CHO-19 successor): minimal aggregates to unblock the
// Phyllis-MVP path. Full Roster integration deferred to M13.B (R+ surface
// owns the rich classroom operator features). The 3 aggregates here are:
//
//   - Campus  — a learning campus (e.g., "MTM Singapore — Bras Basah")
//   - Branch  — a sub-location inside a campus (optional grouping)
//   - Room    — a physical room inside a campus / branch
//
// Cross-DB references: campus_id is a UUID without FK; chora-delivery
// publishes RoomBooked events (campusops.room_booked.v1) carrying the
// campus_id for downstream consumers.
//
// Hexagonal: this is a PURE domain package — no infra imports, no DB
// driver. Persistence (Postgres adapter) lands at M12 alongside the rest
// of chora-delivery's repo layer.
package campusops

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// ErrInvalidArgument is returned by constructor guard-clauses.
var ErrInvalidArgument = errors.New("invalid argument")

// -----------------------------------------------------------------------------
// Campus
// -----------------------------------------------------------------------------

// Campus is a top-level learning location.
type Campus struct {
	ID        string
	TenantID  string
	Name      string
	AddressL1 string
	AddressL2 string
	City      string
	Country   string // ISO 3166-1 alpha-2
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// NewCampusInput is the value-bag for NewCampus.
type NewCampusInput struct {
	TenantID  string
	Name      string
	AddressL1 string
	AddressL2 string
	City      string
	Country   string // ISO 3166-1 alpha-2 (e.g., "SG")
}

// NewCampus constructs a Campus with a UUIDv7 ID.
func NewCampus(in NewCampusInput) (*Campus, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidArgument)
	}
	if !validISO3166Alpha2(in.Country) {
		return nil, fmt.Errorf("%w: country must be ISO 3166-1 alpha-2 (e.g., SG)", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Campus{
		ID:        newUUIDv7(),
		TenantID:  in.TenantID,
		Name:      in.Name,
		AddressL1: in.AddressL1,
		AddressL2: in.AddressL2,
		City:      in.City,
		Country:   in.Country,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// SoftDelete marks the campus deleted (idempotent).
func (c *Campus) SoftDelete() {
	if c.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	c.DeletedAt = &now
}

// -----------------------------------------------------------------------------
// Branch
// -----------------------------------------------------------------------------

// Branch is an optional sub-location grouping inside a Campus.
type Branch struct {
	ID        string
	TenantID  string
	CampusID  string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// NewBranchInput is the value-bag for NewBranch.
type NewBranchInput struct {
	TenantID string
	CampusID string
	Name     string
}

// NewBranch constructs a Branch with a UUIDv7 ID.
func NewBranch(in NewBranchInput) (*Branch, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.CampusID) == "" {
		return nil, fmt.Errorf("%w: campus_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Branch{
		ID:        newUUIDv7(),
		TenantID:  in.TenantID,
		CampusID:  in.CampusID,
		Name:      in.Name,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// SoftDelete marks the branch deleted (idempotent).
func (b *Branch) SoftDelete() {
	if b.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	b.DeletedAt = &now
}

// -----------------------------------------------------------------------------
// Room
// -----------------------------------------------------------------------------

// Room is a physical (or virtual) room. Originally a child of a campus/branch;
// as of CHO-2191 SP1 it is also the standalone bookable unit the R+ scheduler
// double-book/over-capacity gate keys on, so CampusID + BranchID are OPTIONAL
// (a scheduling room needs no campus).
type Room struct {
	ID        string
	TenantID  string
	CampusID  string // optional (CHO-2191 SP1)
	BranchID  string // optional
	Name      string
	Capacity  int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// NewRoomInput is the value-bag for NewRoom.
type NewRoomInput struct {
	TenantID string
	CampusID string
	BranchID string // optional
	Name     string
	Capacity int
}

// NewRoom constructs a Room with a UUIDv7 ID.
//
// Guards: tenant_id + name required; capacity strictly > 0. CampusID + BranchID
// are OPTIONAL (blank allowed) — a scheduling room needs no campus (CHO-2191
// SP1; safe because there are ZERO rooms in prod). Values are trimmed.
func NewRoom(in NewRoomInput) (*Room, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidArgument)
	}
	if in.Capacity <= 0 {
		return nil, fmt.Errorf("%w: capacity must be > 0", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Room{
		ID:        newUUIDv7(),
		TenantID:  in.TenantID,
		CampusID:  strings.TrimSpace(in.CampusID),
		BranchID:  strings.TrimSpace(in.BranchID),
		Name:      strings.TrimSpace(in.Name),
		Capacity:  in.Capacity,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// SoftDelete marks the room deleted (idempotent).
func (r *Room) SoftDelete() {
	if r.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	r.DeletedAt = &now
}

// Rename changes the room's display name, reusing the NewRoom guard (non-blank,
// trimmed) so an Edit can never write a name a create would have rejected. The
// aggregate is left untouched when the guard fails; UpdatedAt bumps on success.
func (r *Room) Rename(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: name required", ErrInvalidArgument)
	}
	r.Name = trimmed
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// SetCapacity changes the room's capacity, reusing the NewRoom guard (strictly
// > 0). The over-capacity-vs-bookings invariant is NOT enforced here: it is a
// scheduling-time concern keyed on room_id (the ratified double-book /
// over-capacity gate), so the Room aggregate keeps its own invariant minimal.
// The aggregate is left untouched when the guard fails; UpdatedAt bumps on
// success.
func (r *Room) SetCapacity(n int) error {
	if n <= 0 {
		return fmt.Errorf("%w: capacity must be > 0", ErrInvalidArgument)
	}
	r.Capacity = n
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// -----------------------------------------------------------------------------
// Registry — in-memory store for the 3 aggregates
// -----------------------------------------------------------------------------

// Registry is an in-memory store for Campus / Branch / Room aggregates.
//
// Production swaps to a Postgres adapter at M12; the public API is stable.
type Registry struct {
	mu       sync.RWMutex
	campuses map[string]*Campus // key = campus_id
	branches map[string]*Branch // key = branch_id
	rooms    map[string]*Room   // key = room_id
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		campuses: make(map[string]*Campus),
		branches: make(map[string]*Branch),
		rooms:    make(map[string]*Room),
	}
}

// SaveCampus inserts/updates a campus.
func (r *Registry) SaveCampus(c *Campus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.campuses[c.ID] = c
}

// GetCampus returns a campus by ID and a bool ok flag.
// Soft-deleted campuses return (nil, false).
func (r *Registry) GetCampus(id string) (*Campus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.campuses[id]
	if !ok || c.DeletedAt != nil {
		return nil, false
	}
	return c, true
}

// GetCampusForTenant scopes the lookup by tenant — used at the HTTP layer
// to enforce tenant isolation when the caller's tenant is known.
func (r *Registry) GetCampusForTenant(tenantID, id string) (*Campus, bool) {
	c, ok := r.GetCampus(id)
	if !ok {
		return nil, false
	}
	if c.TenantID != tenantID {
		return nil, false
	}
	return c, true
}

// SaveBranch inserts/updates a branch.
func (r *Registry) SaveBranch(b *Branch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.branches[b.ID] = b
}

// GetBranch returns a branch by ID; soft-deleted is hidden.
func (r *Registry) GetBranch(id string) (*Branch, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.branches[id]
	if !ok || b.DeletedAt != nil {
		return nil, false
	}
	return b, true
}

// SaveRoom inserts/updates a room.
func (r *Registry) SaveRoom(rm *Room) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rooms[rm.ID] = rm
}

// GetRoom returns a room by ID; soft-deleted is hidden.
func (r *Registry) GetRoom(id string) (*Room, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rm, ok := r.rooms[id]
	if !ok || rm.DeletedAt != nil {
		return nil, false
	}
	return rm, true
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// validISO3166Alpha2 returns true for a 2-letter uppercase ASCII string.
//
// We deliberately do not bake in the full ISO list — for MVP the structural
// check is enough; production validation is M17 SG-gov compliance work.
func validISO3166Alpha2(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// newUUIDv7 — package-local generator to avoid importing the delivery package.
func newUUIDv7() string {
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
