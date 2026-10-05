// credential_test.go — unit tests for pg.CredentialRepo. Mirrors
// offering_session_test.go: stubs the Querier (shared stubQuerier / stubTxRunner
// / stubRow / stubRows + tenantID from application_test.go) so the SQL surface +
// RLS contract are exercised without a live DB. Credential persists as a JSONB
// snapshot keyed by id (extracted cols: tenant_id for RLS, title for ordering).
package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/credential"
)

func newTestCredential(t *testing.T) *credential.Credential {
	t.Helper()
	c, err := credential.NewCredential(credential.NewCredentialInput{
		TenantID:    tenantID,
		Title:       "Project Management Professional",
		Code:        "PMP",
		IssuingBody: "PMI",
		Competencies: []credential.CompetencyInput{
			{Code: "People", Name: "People", Weight: 42},
			{Code: "Process", Name: "Process", Weight: 50},
		},
	})
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	return c
}

func credentialJSON(t *testing.T, c *credential.Credential) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	return b
}

func TestCredentialRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewCredentialRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newTestCredential(t)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "x"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestCredentialRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCredentialRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	c := newTestCredential(t)

	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO credentials") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert; got %q", last)
	}
	// args: id, tenant_id, title, data, created_at, updated_at, deleted_at.
	args := q.args[len(q.args)-1]
	if len(args) != 7 {
		t.Fatalf("expected 7 bind args; got %d (%#v)", len(args), args)
	}
	if args[1] != tenantID || args[2] != "Project Management Professional" {
		t.Fatalf("expected (tenant, title) extracted; got tenant=%v title=%v", args[1], args[2])
	}
}

func TestCredentialRepo_Get_Hit_RoundTrips(t *testing.T) {
	t.Parallel()
	want := newTestCredential(t)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = credentialJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewCredentialRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	c, ok, _ := r.Get(ctx, want.ID)
	if !ok || c == nil {
		t.Fatalf("Get: expected hit")
	}
	if c.ID != want.ID || c.Title != "Project Management Professional" || len(c.Competencies) != 2 {
		t.Fatalf("Get: rehydration mismatch; got %+v", c)
	}
	if c.Competencies[0].Code != "people" {
		t.Fatalf("Get: competency child collection not preserved; got %+v", c.Competencies)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM credentials") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT; got %q", last)
	}
}

func TestCredentialRepo_ListByTenant_BindsTenantOrdersByTitle(t *testing.T) {
	t.Parallel()
	c := newTestCredential(t)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = credentialJSON(t, c); return nil },
			}}, nil
		},
	}
	r := pg.NewCredentialRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 1 || out[0].ID != c.ID {
		t.Fatalf("expected 1 rehydrated row; got %+v", out)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY title") {
		t.Fatalf("list query must bind tenant + filter soft-delete + order by title; got %q", last)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 1 || args[0] != tenantID {
		t.Fatalf("expected (tenant) arg; got %#v", args)
	}
}
