package inmem

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/apollo-chora/chora-delivery/internal/domain/credential"
)

// CredentialRepo stores Credential aggregates, scoped by tenant. Drop-in for
// pg.CredentialRepo at cmd/server wiring (dev / single-pod) and the handler
// test harness (ADR-216 WS-1).
type CredentialRepo struct {
	mu sync.RWMutex
	by map[string]*credential.Credential // credential_id -> *Credential
}

// NewCredentialRepo returns an empty repo.
func NewCredentialRepo() *CredentialRepo {
	return &CredentialRepo{by: make(map[string]*credential.Credential)}
}

// Compile-time assertion: satisfies the domain port.
var _ credential.Store = (*CredentialRepo)(nil)

// Save upserts a credential. ctx is accepted (the pg adapter uses it for RLS);
// the in-memory store ignores it. Never errors.
func (r *CredentialRepo) Save(_ context.Context, c *credential.Credential) error {
	if c == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[c.ID] = c
	return nil
}

// Get returns a credential by ID and ok flag.
func (r *CredentialRepo) Get(_ context.Context, id string) (*credential.Credential, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.by[id]
	return c, ok, nil
}

// ListByTenant returns the tenant's non-soft-deleted credentials (the
// catalogue), ordered by Title then ID (UUIDv7 ⇒ creation-order tiebreak).
func (r *CredentialRepo) ListByTenant(_ context.Context, tenantID string) ([]*credential.Credential, error) {
	r.mu.RLock()
	out := make([]*credential.Credential, 0, len(r.by))
	for _, c := range r.by {
		if c.TenantID != tenantID || c.DeletedAt != nil {
			continue
		}
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out, nil
}
