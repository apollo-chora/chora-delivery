// Package inmem — in-memory adapters for chora-delivery.
//
// ApplicationRepo provides storage + idempotent submit for the Course
// Application aggregate. Production swaps in a Postgres adapter against
// chora_delivery (see internal/adapter/repo/pg/application.go); the domain
// layer is unchanged.
//
// Thread-safety: all methods take an RWMutex; safe for concurrent use.
package inmem

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// ApplicationRepo is the in-memory store for Application aggregates.
//
// Idempotent submit invariant: the (tenant_id, course_id, gcid) tuple is
// the natural key — re-firing SubmitOrGet returns the existing aggregate
// rather than creating a duplicate.
type ApplicationRepo struct {
	mu sync.RWMutex
	// by maps application_id → aggregate
	by map[string]*application.Application
	// idx maps (tenant_id, course_id, gcid) → application_id
	idx map[string]string
}

// NewApplicationRepo returns an empty repo.
func NewApplicationRepo() *ApplicationRepo {
	return &ApplicationRepo{
		by:  make(map[string]*application.Application),
		idx: make(map[string]string),
	}
}

// Compile-time assertion: the in-memory adapter satisfies the domain port so
// it stays a drop-in for pg.ApplicationRepo at cmd/server wiring (ListByTenant
// lives in application_admin.go, same package).
var _ application.ApplicationPort = (*ApplicationRepo)(nil)

// SubmitOrGet implements idempotent submit: if an application already exists
// for (tenant_id, course_id, gcid) it is returned with created=false; otherwise
// a fresh Draft aggregate is created, transitioned to Submitted, and saved.
func (r *ApplicationRepo) SubmitOrGet(_ context.Context, in application.SubmitInput) (*application.Application, bool, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, false, errors.New("application repo: tenant_id required")
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return nil, false, errors.New("application repo: course_id required")
	}
	if strings.TrimSpace(in.GCID) == "" {
		return nil, false, errors.New("application repo: gcid required")
	}
	key := submitKey(in.TenantID, in.CourseID, in.GCID)

	r.mu.Lock()
	defer r.mu.Unlock()

	if existingID, ok := r.idx[key]; ok {
		if app, ok := r.by[existingID]; ok {
			return app, false, nil
		}
	}

	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: in.TenantID,
		CourseID: in.CourseID,
		ClassID:  in.ClassID,
		GCID:     in.GCID,
	})
	if err != nil {
		return nil, false, err
	}
	if err := app.Transition(application.StatusSubmitted); err != nil {
		return nil, false, err
	}
	r.by[app.ID] = app
	r.idx[key] = app.ID
	return app, true, nil
}

// Get returns the application for (tenant_id, application_id). Cross-tenant
// lookups return ok=false (RLS-equivalent at the adapter level).
func (r *ApplicationRepo) Get(_ context.Context, tenantID, applicationID string) (*application.Application, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	app, ok := r.by[applicationID]
	if !ok {
		return nil, false, nil
	}
	if app.TenantID != tenantID {
		return nil, false, nil
	}
	return app, true, nil
}

// ListByGCID returns the applications belonging to (tenant_id, gcid),
// newest-first (UUIDv7 ⇒ creation order, descending).
func (r *ApplicationRepo) ListByGCID(_ context.Context, tenantID, gcid string, offset, limit int) ([]*application.Application, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*application.Application, 0, len(r.by))
	for _, app := range r.by {
		if app.TenantID != tenantID || app.GCID != gcid {
			continue
		}
		out = append(out, app)
	}
	// Sort newest-first by ID (UUIDv7 = lexicographic creation order).
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })

	total := len(out)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return out[offset:end], total, nil
}

// Save persists the aggregate. Idempotent: re-saving the same aggregate
// overwrites the in-memory copy without changing the index.
func (r *ApplicationRepo) Save(_ context.Context, app *application.Application) error {
	if app == nil {
		return errors.New("application repo: nil application")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[app.ID] = app
	// Keep the natural-key index aligned (no-op if already set).
	r.idx[submitKey(app.TenantID, app.CourseID, app.GCID)] = app.ID
	return nil
}

// submitKey returns the natural-key string for SubmitOrGet idempotency.
func submitKey(tenantID, courseID, gcid string) string {
	return tenantID + ":" + courseID + ":" + gcid
}
