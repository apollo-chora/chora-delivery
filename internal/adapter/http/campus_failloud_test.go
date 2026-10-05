// campus_failloud_test.go - honest-failure contract for the /v1/campus handlers
// once Campus moves off the in-memory Registry onto a durable CampusStore
// (CHO-2293).
//
// Before this change the list handler rendered {"items": []} whenever the store
// was absent, so a dead backend was indistinguishable from a tenant with no
// campuses. That is the exact swallowed-error shape the engineering standard
// forbids: a 200 is not persistence, and an empty list is not evidence.
//
// These tests pin the three honest outcomes:
//   - a store error on LIST is a 5xx, never a 200 with an empty array
//   - a store error on CREATE is a 5xx, never a fabricated 201
//   - a store error on GET-by-id is a 5xx, never a 404 (a dead read must not
//     read as an absent row, CHO-2184)
package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// failingCampusStore fails every operation loudly. It stands in for a dead DB
// connection or a denied RLS policy.
type failingCampusStore struct{ err error }

func (s failingCampusStore) Save(context.Context, *campusops.Campus) error { return s.err }

func (s failingCampusStore) GetForTenant(context.Context, string, string) (*campusops.Campus, bool, error) {
	return nil, false, s.err
}

func (s failingCampusStore) ListByTenant(context.Context, string) ([]*campusops.Campus, error) {
	return nil, s.err
}

// Compile-time assertion: the stub really does satisfy the production port, so
// this test cannot drift away from the interface it is guarding.
var _ campusops.CampusStore = failingCampusStore{}

// newV1ServerWithCampusStore wires the standard v1 deps but injects a caller-
// supplied CampusStore.
func newV1ServerWithCampusStore(store campusops.CampusStore) http.Handler {
	return httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
		CampusOps:      store,
	})
}

func TestV1Campus_ListStoreError_Is5xxNotEmptyList(t *testing.T) {
	srv := newV1ServerWithCampusStore(failingCampusStore{err: errors.New("boom: connection reset")})

	req := httptest.NewRequest(http.MethodGet, "/v1/campus", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code < 500 {
		t.Fatalf("a dead campus store must surface as 5xx, got %d body=%q. "+
			"An empty list would make a dead backend look like a tenant with no campuses",
			w.Code, w.Body.String())
	}
}

func TestV1Campus_CreateStoreError_Is5xxNotFabricated201(t *testing.T) {
	srv := newV1ServerWithCampusStore(failingCampusStore{err: errors.New("boom: write failed")})

	w := reqJSON(t, srv, http.MethodPost, "/v1/campus", map[string]interface{}{
		"name":    "Never Persisted",
		"country": "SG",
	})

	if w.Code == http.StatusCreated {
		t.Fatalf("a failed durable write must NOT report 201; got %d body=%q", w.Code, w.Body.String())
	}
	if w.Code < 500 {
		t.Fatalf("a failed durable write must surface as 5xx; got %d body=%q", w.Code, w.Body.String())
	}
}

func TestV1Campus_GetByIDStoreError_Is5xxNot404(t *testing.T) {
	srv := newV1ServerWithCampusStore(failingCampusStore{err: errors.New("boom: RLS denied")})

	req := httptest.NewRequest(http.MethodGet, "/v1/campus/01985e7f-6666-7abc-8def-0000000000f9", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("a dead read must NOT read as an absent row (CHO-2184); got 404 body=%q", w.Body.String())
	}
	if w.Code < 500 {
		t.Fatalf("a dead read must surface as 5xx; got %d body=%q", w.Code, w.Body.String())
	}
}

// Positive control: the SAME handlers on a healthy store still behave. Without
// this, a handler that 500s unconditionally would pass every test above.
func TestV1Campus_HealthyStore_StillServes(t *testing.T) {
	srv := newV1ServerWithCampusStore(repoinmem.NewCampusRepo())

	w := reqJSON(t, srv, http.MethodPost, "/v1/campus", map[string]interface{}{
		"name":    "Healthy Campus",
		"country": "SG",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("healthy store must still create; got %d body=%q", w.Code, w.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/campus", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	lw := httptest.NewRecorder()
	srv.ServeHTTP(lw, req)
	if lw.Code != http.StatusOK {
		t.Fatalf("healthy store must still list; got %d body=%q", lw.Code, lw.Body.String())
	}
}
