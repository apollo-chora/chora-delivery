// credential_repo_test.go — direct unit tests for the in-memory
// CredentialRepo (ADR-216 WS-1). Pins round-trip persistence, the nil-safe
// Save, the tenant + soft-delete-scoped catalogue, and the Title-then-ID
// ordering.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/credential"
)

func mustCredential(t *testing.T, id, tenantID, title string) *credential.Credential {
	t.Helper()
	c, err := credential.NewCredential(credential.NewCredentialInput{
		TenantID:     tenantID,
		Title:        title,
		Competencies: []credential.CompetencyInput{{Code: "C1", Name: "Competency 1"}},
	})
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	c.ID = id
	return c
}

func TestCredentialRepo_SaveGet(t *testing.T) {
	t.Parallel()
	r := inmem.NewCredentialRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	c := mustCredential(t, "cred-1", tenantA, "Project Management Professional")
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, c.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != c.ID || got.Title != "Project Management Professional" {
		t.Fatalf("round-trip mismatch; got %+v", got)
	}
	if _, ok, err := r.Get(ctx, "missing"); ok || err != nil {
		t.Fatalf("Get unknown: ok=%v err=%v", ok, err)
	}
}

func TestCredentialRepo_ListByTenant_ScopeOrderAndSoftDelete(t *testing.T) {
	t.Parallel()
	r := inmem.NewCredentialRepo()
	ctx := context.Background()

	a1 := mustCredential(t, "cred-a1", tenantA, "Alpha")
	a2 := mustCredential(t, "cred-a2", tenantA, "Beta")
	b1 := mustCredential(t, "cred-b1", tenantB, "Other Tenant")
	gone := mustCredential(t, "cred-gone", tenantA, "Gamma")
	now := time.Now().UTC()
	gone.DeletedAt = &now

	for _, c := range []*credential.Credential{a1, a2, b1, gone} {
		if err := r.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant-scoped + soft-delete filtered); got %d", len(out))
	}
	// Ordered by Title then ID.
	if out[0].ID != "cred-a1" || out[1].ID != "cred-a2" {
		t.Fatalf("expected [Alpha Beta] by Title; got [%s %s]", out[0].ID, out[1].ID)
	}

	// Title tie → ID tiebreak.
	tie1 := mustCredential(t, "cred-t1", tenantA, "Same Title")
	tie2 := mustCredential(t, "cred-t2", tenantA, "Same Title")
	if tie1.ID == tie2.ID {
		t.Fatal("test bug: credentials must have distinct ids")
	}
	if err := r.Save(ctx, tie1); err != nil {
		t.Fatalf("Save tie1: %v", err)
	}
	if err := r.Save(ctx, tie2); err != nil {
		t.Fatalf("Save tie2: %v", err)
	}
	out, err = r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant (tie): %v", err)
	}
	var ids []string
	for _, c := range out {
		if c.Title == "Same Title" {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) != 2 || ids[0] > ids[1] {
		t.Fatalf("expected Title tie to order by ID ascending; got %v", ids)
	}

	empty, err := r.ListByTenant(ctx, "tenant-with-nothing")
	if err != nil {
		t.Fatalf("ListByTenant(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected 0; got %d", len(empty))
	}
}
