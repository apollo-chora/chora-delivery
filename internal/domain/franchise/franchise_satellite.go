// Package franchise carries the FranchiseSatellite aggregate: the ADR-192 D1
// owner-to-satellite mapping that is the authoritative, revocable scope of the
// (W5-gated) cross-tenant exam rollup.
//
// One row states "owner_tenant_id may aggregate satellite_tenant_id". The
// future exam_owner_rollup RLS policy (ADR-192 D1, NOT built in this slice)
// widens an owner's read to exactly the satellites mapped here, so this table
// is deliberately ordinary tenant-scoped data: creating or revoking a row is
// plain admin CRUD; only the policy reading THROUGH it is the sanctioned
// bypass surface.
//
// Invariants:
//   - owner_tenant_id and satellite_tenant_id MUST be canonical UUIDs. The
//     rollup policy casts these columns to uuid in SQL, so a non-UUID value
//     written today is an armed 22P02 for that query tomorrow (the offerings
//     CourseIDs lesson: an armed cast with an unguarded writer).
//   - owner <> satellite: a tenant cannot be its own satellite.
//   - Revoke = soft delete (deleted_at), never hard delete.
//
// Pure domain: no http, no persistence imports.
package franchise

import (
	"errors"
	"strings"
	"time"
)

var (
	// ErrOwnerTenantRequired: owner_tenant_id must be non-empty.
	ErrOwnerTenantRequired = errors.New("franchise: owner_tenant_id required")
	// ErrSatelliteTenantRequired: satellite_tenant_id must be non-empty.
	ErrSatelliteTenantRequired = errors.New("franchise: satellite_tenant_id required")
	// ErrCreatedByRequired: the acting GCID must be recorded (ADR-192 O1
	// accountability: the mapping is written by an identified authority).
	ErrCreatedByRequired = errors.New("franchise: created_by_gcid required")
	// ErrOwnerTenantInvalid: owner_tenant_id is not a canonical UUID.
	ErrOwnerTenantInvalid = errors.New("franchise: owner_tenant_id is not a canonical UUID")
	// ErrSatelliteTenantInvalid: satellite_tenant_id is not a canonical UUID.
	ErrSatelliteTenantInvalid = errors.New("franchise: satellite_tenant_id is not a canonical UUID")
	// ErrSelfMapping: a tenant cannot be mapped as its own satellite.
	ErrSelfMapping = errors.New("franchise: owner and satellite tenant must differ")
	// ErrAlreadyRevoked: the mapping is already soft-deleted.
	ErrAlreadyRevoked = errors.New("franchise: mapping already revoked")
	// ErrDuplicateMapping is returned by stores when a live (non-revoked) row
	// for the same (owner, satellite) pair already exists.
	ErrDuplicateMapping = errors.New("franchise: mapping already exists for this owner and satellite")
)

// FranchiseSatellite is one revocable owner-to-satellite aggregation grant.
type FranchiseSatellite struct {
	ID                string
	OwnerTenantID     string
	SatelliteTenantID string
	CreatedByGCID     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
}

// New validates + constructs a live mapping. Tenant ids are normalised to
// lowercase canonical form so equality checks and the SQL uuid casts agree.
func New(ownerTenantID, satelliteTenantID, createdByGCID string) (*FranchiseSatellite, error) {
	owner := strings.ToLower(strings.TrimSpace(ownerTenantID))
	satellite := strings.ToLower(strings.TrimSpace(satelliteTenantID))
	createdBy := strings.TrimSpace(createdByGCID)
	if owner == "" {
		return nil, ErrOwnerTenantRequired
	}
	if satellite == "" {
		return nil, ErrSatelliteTenantRequired
	}
	if createdBy == "" {
		return nil, ErrCreatedByRequired
	}
	if !isCanonicalUUID(owner) {
		return nil, ErrOwnerTenantInvalid
	}
	if !isCanonicalUUID(satellite) {
		return nil, ErrSatelliteTenantInvalid
	}
	if owner == satellite {
		return nil, ErrSelfMapping
	}
	now := time.Now().UTC()
	return &FranchiseSatellite{
		ID:                newUUIDv7(),
		OwnerTenantID:     owner,
		SatelliteTenantID: satellite,
		CreatedByGCID:     createdBy,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// Revoke soft-deletes the mapping (access gone for the future rollup policy:
// delete a row, access disappears, per ADR-192). A second revoke is surfaced
// loudly rather than silently absorbed.
func (m *FranchiseSatellite) Revoke(when time.Time) error {
	if m.DeletedAt != nil {
		return ErrAlreadyRevoked
	}
	w := when.UTC()
	m.DeletedAt = &w
	m.UpdatedAt = w
	return nil
}

// isCanonicalUUID reports whether s is a 36-char, lowercase, hyphenated UUID
// (8-4-4-4-12). Version-agnostic on purpose: tenant ids predate the UUIDv7
// convention; the guard is against the SQL ::uuid cast, not a version pin.
func isCanonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !isHex {
				return false
			}
		}
	}
	return true
}
