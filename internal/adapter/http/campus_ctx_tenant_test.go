// campus_ctx_tenant_test.go - the handler MUST hand the store a tenant-bearing
// context, not the bare request context (CHO-2293 regression).
//
// WHY THIS EXISTS. The first CHO-2293 deploy 500'd every /v1/campus request in
// about 3ms, with no SQL issued at all. tenantRequired stamps the tenant into a
// HEADER; it does NOT put it in the request context. pg.CampusRepo calls
// rls.ApplySession, which reads the tenant from the CONTEXT and returns
// ErrNoTenantContext when it is absent. Every other RLS-backed handler in this
// service (rooms_handler.go:82, survey_handler.go:220, ...) threads
// tracing.WithTenantID(r.Context(), tenantID) explicitly. The campus handlers
// did not.
//
// The original unit tests could NOT have caught it: the in-mem adapter ignores
// ctx entirely, and the pg adapter tests build their own tenant-bearing ctx with
// tracing.WithTenantID. Both sides passed while the seam BETWEEN them was
// broken. These tests drive that seam with a spy that asserts on the context the
// handler actually passes, which is the only place the defect was visible.
package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

// ctxSpyCampusStore records the context it was handed on each call so a test can
// assert the tenant survived the handler boundary.
type ctxSpyCampusStore struct {
	saveCtx context.Context
	listCtx context.Context
	getCtx  context.Context
}

func (s *ctxSpyCampusStore) Save(ctx context.Context, _ *campusops.Campus) error {
	s.saveCtx = ctx
	return nil
}

func (s *ctxSpyCampusStore) GetForTenant(ctx context.Context, _, _ string) (*campusops.Campus, bool, error) {
	s.getCtx = ctx
	return nil, false, nil
}

func (s *ctxSpyCampusStore) ListByTenant(ctx context.Context, _ string) ([]*campusops.Campus, error) {
	s.listCtx = ctx
	return []*campusops.Campus{}, nil
}

var _ campusops.CampusStore = (*ctxSpyCampusStore)(nil)

func TestV1Campus_List_PassesTenantBearingContextToStore(t *testing.T) {
	spy := &ctxSpyCampusStore{}
	srv := newV1ServerWithCampusStore(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/campus", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	srv.ServeHTTP(httptest.NewRecorder(), req)

	if spy.listCtx == nil {
		t.Fatalf("store was never called")
	}
	if got := tracing.TenantIDFromContext(spy.listCtx); got != tenantA {
		t.Fatalf("ListByTenant got a context with tenant %q, want %q. "+
			"rls.ApplySession reads the tenant from the CONTEXT, not the header, "+
			"so a bare r.Context() fails with ErrNoTenantContext before any SQL runs",
			got, tenantA)
	}
}

func TestV1Campus_Create_PassesTenantBearingContextToStore(t *testing.T) {
	spy := &ctxSpyCampusStore{}
	srv := newV1ServerWithCampusStore(spy)

	reqJSON(t, srv, http.MethodPost, "/v1/campus", map[string]interface{}{
		"name":    "Ctx Probe",
		"country": "SG",
	})

	if spy.saveCtx == nil {
		t.Fatalf("store was never called")
	}
	if got := tracing.TenantIDFromContext(spy.saveCtx); got != tenantA {
		t.Fatalf("Save got a context with tenant %q, want %q", got, tenantA)
	}
}

func TestV1Campus_GetByID_PassesTenantBearingContextToStore(t *testing.T) {
	spy := &ctxSpyCampusStore{}
	srv := newV1ServerWithCampusStore(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/campus/01985e7f-6666-7abc-8def-0000000000f9", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	srv.ServeHTTP(httptest.NewRecorder(), req)

	if spy.getCtx == nil {
		t.Fatalf("store was never called")
	}
	if got := tracing.TenantIDFromContext(spy.getCtx); got != tenantA {
		t.Fatalf("GetForTenant got a context with tenant %q, want %q", got, tenantA)
	}
}
