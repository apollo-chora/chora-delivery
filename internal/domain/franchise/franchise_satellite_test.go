// franchise_satellite_test.go: RED-first unit tests for the FranchiseSatellite
// aggregate (ADR-192 D1 mapping table, W5 bypass-free slice, CHO-2230).
//
// The mapping row is the authoritative, revocable rollup scope: one row says
// "owner_tenant_id may aggregate satellite_tenant_id". The aggregate guards:
//   - required owner/satellite/created-by
//   - both tenant ids MUST be canonical UUIDs (the offerings 22P02 lesson:
//     the future exam_owner_rollup policy casts these columns to uuid in SQL,
//     so a non-UUID write today is an armed bomb for that query tomorrow)
//   - owner <> satellite (a tenant cannot be its own satellite)
//   - revoke = soft delete (never hard delete), idempotence surfaced loudly
package franchise_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

const (
	ownerTenant     = "019e2f93-d586-71b5-8c3d-e2b0d0d5aa01"
	satelliteTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d5bb02"
	creatorGCID     = "019e2f93-d586-71b5-8c3d-e2b0d0d5cc03"
)

func TestNew_Valid(t *testing.T) {
	t.Parallel()
	m, err := franchise.New(ownerTenant, satelliteTenant, creatorGCID)
	if err != nil {
		t.Fatalf("New: unexpected error %v", err)
	}
	if m.ID == "" {
		t.Fatal("New: ID must be minted (UUIDv7)")
	}
	if len(m.ID) != 36 || m.ID[14] != '7' {
		t.Fatalf("New: ID %q is not a UUIDv7", m.ID)
	}
	if m.OwnerTenantID != ownerTenant || m.SatelliteTenantID != satelliteTenant {
		t.Fatalf("New: tenants not carried: %+v", m)
	}
	if m.CreatedByGCID != creatorGCID {
		t.Fatalf("New: created_by not carried: %+v", m)
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		t.Fatal("New: timestamps must be set")
	}
	if m.DeletedAt != nil {
		t.Fatal("New: fresh mapping must not be soft-deleted")
	}
}

func TestNew_NormalisesCase(t *testing.T) {
	t.Parallel()
	m, err := franchise.New(strings.ToUpper(ownerTenant), strings.ToUpper(satelliteTenant), creatorGCID)
	if err != nil {
		t.Fatalf("New: unexpected error %v", err)
	}
	if m.OwnerTenantID != ownerTenant || m.SatelliteTenantID != satelliteTenant {
		t.Fatalf("New: UUIDs must be normalised to lowercase: %+v", m)
	}
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		owner, satellite string
		createdBy        string
		want             error
	}{
		{"empty owner", "", satelliteTenant, creatorGCID, franchise.ErrOwnerTenantRequired},
		{"empty satellite", ownerTenant, "", creatorGCID, franchise.ErrSatelliteTenantRequired},
		{"empty created_by", ownerTenant, satelliteTenant, "", franchise.ErrCreatedByRequired},
		{"owner not a uuid", "tenant-alpha", satelliteTenant, creatorGCID, franchise.ErrOwnerTenantInvalid},
		{"satellite not a uuid", ownerTenant, "not-a-uuid", creatorGCID, franchise.ErrSatelliteTenantInvalid},
		{"owner short hex", ownerTenant[:35], satelliteTenant, creatorGCID, franchise.ErrOwnerTenantInvalid},
		{"self mapping", ownerTenant, ownerTenant, creatorGCID, franchise.ErrSelfMapping},
		{"self mapping case-insensitive", ownerTenant, strings.ToUpper(ownerTenant), creatorGCID, franchise.ErrSelfMapping},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := franchise.New(tc.owner, tc.satellite, tc.createdBy); !errors.Is(err, tc.want) {
				t.Fatalf("New(%q,%q): want %v, got %v", tc.owner, tc.satellite, tc.want, err)
			}
		})
	}
}

func TestRevoke(t *testing.T) {
	t.Parallel()
	m, err := franchise.New(ownerTenant, satelliteTenant, creatorGCID)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	when := time.Now().UTC()
	if err := m.Revoke(when); err != nil {
		t.Fatalf("Revoke: unexpected error %v", err)
	}
	if m.DeletedAt == nil || !m.DeletedAt.Equal(when) {
		t.Fatalf("Revoke: DeletedAt not stamped: %+v", m.DeletedAt)
	}
	if !m.UpdatedAt.Equal(when) {
		t.Fatalf("Revoke: UpdatedAt not advanced: %+v", m.UpdatedAt)
	}
	// A second revoke must be surfaced, not silently absorbed.
	if err := m.Revoke(when.Add(time.Minute)); !errors.Is(err, franchise.ErrAlreadyRevoked) {
		t.Fatalf("Revoke twice: want ErrAlreadyRevoked, got %v", err)
	}
}
