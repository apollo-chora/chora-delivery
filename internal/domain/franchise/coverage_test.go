// coverage_test.go — closes the remaining statement gaps in
// franchise_satellite.go by driving isCanonicalUUID's dash-position and
// non-hex-character rejections through the public New() guard (both surface as
// ErrOwnerTenantInvalid / ErrSatelliteTenantInvalid).
package franchise_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

func TestNew_RejectsNonDashAtHyphenPosition(t *testing.T) {
	t.Parallel()
	// 36 chars, every non-dash slot a hex digit, but ':' at position 8 where the
	// first hyphen must sit → the dash-position branch of isCanonicalUUID.
	bad := "019e2f93:d586-71b5-8c3d-e2b0d0d5aa01"
	if _, err := franchise.New(bad, satelliteTenant, creatorGCID); !errors.Is(err, franchise.ErrOwnerTenantInvalid) {
		t.Errorf("bad dash: want ErrOwnerTenantInvalid, got %v", err)
	}
}

func TestNew_RejectsNonHexAtDataPosition(t *testing.T) {
	t.Parallel()
	// 36 chars with correct hyphens but 'g' (not 0-9a-f) in a data slot.
	bad := "019e2f93-g586-71b5-8c3d-e2b0d0d5aa01"
	if _, err := franchise.New(ownerTenant, bad, creatorGCID); !errors.Is(err, franchise.ErrSatelliteTenantInvalid) {
		t.Errorf("non-hex: want ErrSatelliteTenantInvalid, got %v", err)
	}
}
