// application_admin.go — admin-scoped read helpers on the in-memory
// Application repo. Distinct file from application.go so the M14 R+
// surface buildout (admin Course-Application review queue) can ADD
// new methods without touching the learner-side aggregate file
// owned by the existing S6.1 implementation.
//
// Hexagonal: this file lives in the same `inmem` package as the rest
// of the repo so it can read the package-private `by` map without
// widening the public surface. Production swaps the equivalent
// method onto pg.ApplicationRepo (Postgres) — see
// internal/adapter/repo/pg/application.go for the production-grade
// query with RLS + state-filter SQL.
//
// Strict TDD per .claude/rules/development-execution.md: this file
// is GREEN-phase implementation that makes the test file
// services/chora-delivery/internal/adapter/http/applications_admin_handler_test.go
// pass.
package inmem

import (
	"context"
	"sort"
	"strings"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// ListByTenant returns all applications in the tenant, newest-first.
// When Status is non-empty, only rows whose Status matches are
// returned. Cross-tenant rows NEVER surface (cross-tenant isolation
// guard — matches the RLS-equivalent behaviour of the pg adapter
// for parity).
//
// Pagination is the same shape as ListByGCID (offset + limit). Total
// is the post-filter pre-pagination count — the FE uses it for the
// "N applications" header pill.
//
// Inherits the RWMutex held by ApplicationRepo so concurrent
// SubmitOrGet + ListByTenant calls are safe.
func (r *ApplicationRepo) ListByTenant(_ context.Context, in application.ListByTenantInput) ([]*application.Application, int, error) {
	tenantID := strings.TrimSpace(in.TenantID)

	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*application.Application, 0, len(r.by))
	for _, app := range r.by {
		if tenantID != "" && app.TenantID != tenantID {
			continue
		}
		if in.Status != "" && app.Status != in.Status {
			continue
		}
		out = append(out, app)
	}
	// Newest-first (UUIDv7 ⇒ lexicographic creation order; matches the
	// ListByGCID convention in application.go).
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })

	total := len(out)
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	limit := in.Limit
	if limit <= 0 {
		limit = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return out[offset:end], total, nil
}
