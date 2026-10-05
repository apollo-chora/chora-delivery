// Campus Operations — BE-CO2 skeleton (CHO-19 successor).
//
// Per S4.3 brief: minimal aggregates to unblock the Phyllis-MVP path.
// Full Roster integration deferred (R+ surface owns the rich classroom
// operator features at M13.B).
//
// Aggregates:
//   - Campus  : a learning campus (e.g., "MTM Singapore — Bras Basah")
//   - Branch  : a sub-location inside a campus (optional grouping)
//   - Room    : a physical room inside a campus / branch
//
// Cross-DB references: campus_id flows out via room_booked.v1 etc.
package campusops_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

const tenantA = "01970000-0000-7000-8000-000000000001"

// -----------------------------------------------------------------------------
// Campus
// -----------------------------------------------------------------------------

func TestNewCampus_AssignsDefaults(t *testing.T) {
	t.Parallel()
	c, err := campusops.NewCampus(campusops.NewCampusInput{
		TenantID:  tenantA,
		Name:      "MTM Singapore — Bras Basah",
		AddressL1: "123 Bras Basah Rd",
		Country:   "SG",
	})
	if err != nil {
		t.Fatalf("NewCampus: %v", err)
	}
	if c.ID == "" {
		t.Fatalf("expected UUIDv7 id")
	}
	if c.TenantID != tenantA {
		t.Fatalf("tenant_id mismatch")
	}
	if c.Name != "MTM Singapore — Bras Basah" {
		t.Fatalf("name mismatch")
	}
	if c.DeletedAt != nil {
		t.Fatalf("fresh campus should not be soft-deleted")
	}
}

func TestNewCampus_RejectsEmptyName(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewCampus(campusops.NewCampusInput{
		TenantID:  tenantA,
		Name:      "",
		AddressL1: "x",
		Country:   "SG",
	})
	if err == nil {
		t.Fatalf("expected error for empty name")
	}
}

func TestNewCampus_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: "",
		Name:     "x",
		Country:  "SG",
	})
	if err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}

func TestNewCampus_RejectsBadCountry(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA,
		Name:     "x",
		Country:  "Singapore", // expected ISO-2 ("SG"), not country name
	})
	if err == nil {
		t.Fatalf("expected error for non-ISO-2 country code")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "country") {
		t.Fatalf("error should mention country, got %v", err)
	}
}

func TestCampus_SoftDelete_Idempotent(t *testing.T) {
	t.Parallel()
	c, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "x", Country: "SG",
	})
	c.SoftDelete()
	first := *c.DeletedAt
	c.SoftDelete()
	if !c.DeletedAt.Equal(first) {
		t.Fatalf("SoftDelete should be idempotent")
	}
}

// -----------------------------------------------------------------------------
// Branch
// -----------------------------------------------------------------------------

func TestNewBranch_AssignsDefaults(t *testing.T) {
	t.Parallel()
	parent, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "Main", Country: "SG",
	})
	b, err := campusops.NewBranch(campusops.NewBranchInput{
		TenantID: tenantA,
		CampusID: parent.ID,
		Name:     "Floor 3",
	})
	if err != nil {
		t.Fatalf("NewBranch: %v", err)
	}
	if b.CampusID != parent.ID {
		t.Fatalf("campus_id mismatch")
	}
	if b.Name != "Floor 3" {
		t.Fatalf("name mismatch")
	}
}

func TestNewBranch_RejectsEmptyCampusID(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewBranch(campusops.NewBranchInput{
		TenantID: tenantA,
		Name:     "x",
	})
	if err == nil {
		t.Fatalf("expected error for empty campus_id")
	}
}

// -----------------------------------------------------------------------------
// Room
// -----------------------------------------------------------------------------

func TestNewRoom_AssignsDefaults(t *testing.T) {
	t.Parallel()
	parent, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "Main", Country: "SG",
	})
	r, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA,
		CampusID: parent.ID,
		Name:     "Lab A",
		Capacity: 30,
	})
	if err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	if r.Capacity != 30 {
		t.Fatalf("capacity mismatch")
	}
	if r.Name != "Lab A" {
		t.Fatalf("name mismatch")
	}
}

func TestNewRoom_RejectsZeroCapacity(t *testing.T) {
	t.Parallel()
	parent, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "Main", Country: "SG",
	})
	_, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA,
		CampusID: parent.ID,
		Name:     "x",
		Capacity: 0,
	})
	if err == nil {
		t.Fatalf("expected error for capacity=0")
	}
}

// -----------------------------------------------------------------------------
// Registry — happy path
// -----------------------------------------------------------------------------

func TestRegistry_SaveGetCampus(t *testing.T) {
	t.Parallel()
	reg := campusops.NewRegistry()
	c, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "x", Country: "SG",
	})
	reg.SaveCampus(c)
	got, ok := reg.GetCampus(c.ID)
	if !ok {
		t.Fatalf("Get: miss")
	}
	if got.ID != c.ID {
		t.Fatalf("id round-trip")
	}
}

func TestRegistry_SoftDeletedCampusHidden(t *testing.T) {
	t.Parallel()
	reg := campusops.NewRegistry()
	c, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "x", Country: "SG",
	})
	reg.SaveCampus(c)
	c.SoftDelete()
	reg.SaveCampus(c)
	if _, ok := reg.GetCampus(c.ID); ok {
		t.Fatalf("expected soft-deleted campus to be hidden from Get")
	}
}

func TestRegistry_TenantIsolation(t *testing.T) {
	t.Parallel()
	reg := campusops.NewRegistry()
	a, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "a", Country: "SG",
	})
	b, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: "01970000-0000-7000-8000-FFFFFFFFFFFF",
		Name:     "b", Country: "SG",
	})
	reg.SaveCampus(a)
	reg.SaveCampus(b)
	got, ok := reg.GetCampusForTenant(tenantA, a.ID)
	if !ok {
		t.Fatalf("expected tenant-A campus visible to tenant A")
	}
	if got.ID != a.ID {
		t.Fatalf("id round-trip")
	}
	if _, ok := reg.GetCampusForTenant(tenantA, b.ID); ok {
		t.Fatalf("tenant-B campus must NOT be visible to tenant A")
	}
	// Unknown id → false (covers the "not found" branch).
	if _, ok := reg.GetCampusForTenant(tenantA, "01970000-0000-7000-8000-AAAA"); ok {
		t.Fatalf("unknown id should miss")
	}
}

// -----------------------------------------------------------------------------
// Branch + Room — guard clauses + Registry coverage
// -----------------------------------------------------------------------------

func TestRegistry_SaveGetBranch(t *testing.T) {
	t.Parallel()
	reg := campusops.NewRegistry()
	c, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "x", Country: "SG",
	})
	reg.SaveCampus(c)
	b, _ := campusops.NewBranch(campusops.NewBranchInput{
		TenantID: tenantA, CampusID: c.ID, Name: "Floor 3",
	})
	reg.SaveBranch(b)
	got, ok := reg.GetBranch(b.ID)
	if !ok {
		t.Fatalf("Get branch: miss")
	}
	if got.ID != b.ID {
		t.Fatalf("id round-trip")
	}
	// Soft-deleted hidden.
	b.SoftDelete()
	reg.SaveBranch(b)
	if _, ok := reg.GetBranch(b.ID); ok {
		t.Fatalf("soft-deleted branch still visible")
	}
	// Idempotent SoftDelete.
	first := *b.DeletedAt
	b.SoftDelete()
	if !b.DeletedAt.Equal(first) {
		t.Fatalf("Branch.SoftDelete must be idempotent")
	}
}

func TestRegistry_SaveGetRoom(t *testing.T) {
	t.Parallel()
	reg := campusops.NewRegistry()
	c, _ := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "x", Country: "SG",
	})
	reg.SaveCampus(c)
	rm, _ := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA, CampusID: c.ID, Name: "A1", Capacity: 30,
	})
	reg.SaveRoom(rm)
	got, ok := reg.GetRoom(rm.ID)
	if !ok {
		t.Fatalf("Get room: miss")
	}
	if got.ID != rm.ID {
		t.Fatalf("id round-trip")
	}
	rm.SoftDelete()
	reg.SaveRoom(rm)
	if _, ok := reg.GetRoom(rm.ID); ok {
		t.Fatalf("soft-deleted room still visible")
	}
	// Idempotent SoftDelete.
	first := *rm.DeletedAt
	rm.SoftDelete()
	if !rm.DeletedAt.Equal(first) {
		t.Fatalf("Room.SoftDelete must be idempotent")
	}
}

func TestNewBranch_RejectsEmptyName(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewBranch(campusops.NewBranchInput{
		TenantID: tenantA, CampusID: "campus-1", Name: "",
	})
	if err == nil {
		t.Fatalf("expected error for empty name")
	}
}

func TestNewBranch_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewBranch(campusops.NewBranchInput{
		CampusID: "campus-1", Name: "x",
	})
	if err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}

func TestNewRoom_RejectsEmptyName(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA, CampusID: "campus-1", Name: "", Capacity: 5,
	})
	if err == nil {
		t.Fatalf("expected error for empty name")
	}
}

func TestNewRoom_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	_, err := campusops.NewRoom(campusops.NewRoomInput{
		CampusID: "campus-1", Name: "x", Capacity: 5,
	})
	if err == nil {
		t.Fatalf("expected error for empty tenant")
	}
}

// TestNewRoom_AllowsEmptyCampus — CHO-2191 SP1 relaxation: campus_id is now
// OPTIONAL. A scheduling room needs no campus (there are ZERO rooms in prod, so
// this is a safe widening), so a blank CampusID must construct cleanly. This
// replaces the prior TestNewRoom_RejectsEmptyCampus (campus-required) assertion.
func TestNewRoom_AllowsEmptyCampus(t *testing.T) {
	t.Parallel()
	rm, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA, Name: "Scheduling Room", Capacity: 5,
	})
	if err != nil {
		t.Fatalf("NewRoom with blank campus_id should succeed; got %v", err)
	}
	if rm.CampusID != "" {
		t.Fatalf("expected blank campus_id to round-trip empty; got %q", rm.CampusID)
	}
	if rm.Capacity != 5 || rm.Name != "Scheduling Room" || rm.TenantID != tenantA {
		t.Fatalf("field round-trip mismatch; got %+v", rm)
	}
}

func TestNewCampus_AcceptsLowerCaseISORejected(t *testing.T) {
	t.Parallel()
	// validISO3166Alpha2 demands uppercase — lower-case "sg" fails.
	_, err := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantA, Name: "x", Country: "sg",
	})
	if err == nil {
		t.Fatalf("expected error for lowercase country code")
	}
}
