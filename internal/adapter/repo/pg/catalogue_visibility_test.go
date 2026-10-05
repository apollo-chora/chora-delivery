// catalogue_visibility_test.go — unit tests for the B2.3 visibility
// reconciliation in scanTenantCourseRow + the safeVisibility write guard.
//
// Two writers touch `courses`: SaveCJ2 sets `public` (from CJ#2 state, never
// `visibility`) and the catalogue Save sets the real `visibility` scope. So the
// scan keeps `public` authoritative for cross-tenant public-ness and only reads
// the persisted `visibility` to distinguish private vs tenant_only on a
// NON-public row (private no longer collapses to tenant_only).
package pg

import (
	"testing"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// fakeTenantRowScan mimics pgx Row.Scan for the tenantCourseCols column order:
// id, tenant_id, instructor_gcid, title, description, public, price_sgd_cents,
// sf_eligible, max_capacity, created_at, updated_at, visibility.
func fakeTenantRowScan(public bool, visibility string) func(...any) error {
	return func(dest ...any) error {
		*dest[0].(*string) = "019e0000-0000-7000-8000-00000000c0de"
		*dest[1].(*string) = "11111111-1111-7111-8111-111111111111"
		*dest[2].(*string) = "019e0000-0000-7000-8000-00000000aaaa"
		*dest[3].(*string) = "Title"
		*dest[4].(*string) = ""
		*dest[5].(*bool) = public
		*dest[6].(*int64) = 0
		*dest[7].(*bool) = false
		*dest[8].(*int) = 0
		// dest[9], dest[10] are time.Time (created/updated) — left zero.
		*dest[11].(*string) = visibility
		return nil
	}
}

func TestScanTenantCourseRow_VisibilityReconciliation(t *testing.T) {
	cases := []struct {
		name       string
		public     bool
		visibility string
		want       domain.Visibility
	}{
		{"public bool authoritative over tenant_only column", true, "tenant_only", domain.VisibilityPublic},
		{"public bool authoritative over private column", true, "private", domain.VisibilityPublic},
		{"non-public private persists (the B2.3 fix)", false, "private", domain.VisibilityPrivate},
		{"non-public tenant_only", false, "tenant_only", domain.VisibilityTenantOnly},
		{"non-public stale public column → tenant_only", false, "public", domain.VisibilityTenantOnly},
		{"non-public empty column → tenant_only", false, "", domain.VisibilityTenantOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc, err := scanTenantCourseRow(fakeTenantRowScan(tc.public, tc.visibility))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if pc.Visibility != tc.want {
				t.Fatalf("Visibility = %q, want %q", pc.Visibility, tc.want)
			}
			if pc.Public != tc.public {
				t.Fatalf("Public = %v, want %v (denorm must round-trip)", pc.Public, tc.public)
			}
		})
	}
}

func TestSafeVisibility(t *testing.T) {
	cases := []struct {
		vis    domain.Visibility
		public bool
		want   string
	}{
		{domain.VisibilityPrivate, false, "private"},
		{domain.VisibilityTenantOnly, false, "tenant_only"},
		{domain.VisibilityPublic, true, "public"},
		{domain.Visibility(""), true, "public"},            // invalid → coerce from public
		{domain.Visibility("bogus"), false, "tenant_only"}, // invalid + not public → tenant_only
	}
	for _, tc := range cases {
		got := safeVisibility(&domain.PublicCourse{Visibility: tc.vis, Public: tc.public})
		if got != tc.want {
			t.Fatalf("safeVisibility(vis=%q, public=%v) = %q, want %q", tc.vis, tc.public, got, tc.want)
		}
	}
}
