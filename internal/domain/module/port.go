// port.go — ModulePort (the hexagonal persistence surface for the Module
// aggregate) + InMemModuleStore (the dev/test impl). Production wires
// pg.ModuleRepo behind the same TxRunner gate as the other chora-delivery
// repos; unit/dev code wires InMemModuleStore.
//
// Every method takes ctx so the DB adapter can propagate tenant_id into
// rls.ApplySession, honour cancellation, and carry W3C trace context across the
// SQL boundary. The InMem impl accepts ctx then drops it (it is sync + ctx-free)
// — mirrors InMemEnrollmentStore.
package module

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// ModulePort is the minimum real persistence surface for the Module aggregate.
//
// Mutating item operations (AddItem/RemoveItem/Reorder) are load-mutate-persist:
// the adapter loads the module + its active items, applies the domain mutation
// (so every invariant runs on the production path — no green-test stub), then
// persists the delta. SoftDelete cascades within the aggregate.
type ModulePort interface {
	// Create persists a new module. The store assigns the dense within-course
	// Position (append) and persists any items the aggregate already holds.
	Create(ctx context.Context, m *Module) (*Module, error)
	// Get loads a module and its active items by id, tenant-scoped (RLS in pg).
	// Miss returns (nil, false, nil).
	Get(ctx context.Context, moduleID string) (*Module, bool, error)
	// ListByCourse returns the active modules of a course in Position order,
	// each hydrated with its active items.
	ListByCourse(ctx context.Context, tenantID, courseID string) ([]*Module, error)
	// AddItem appends a content-item reference to a module.
	AddItem(ctx context.Context, tenantID, moduleID, contentItemID string) (*ModuleItem, error)
	// RemoveItem soft-deletes a module item and re-compacts positions.
	RemoveItem(ctx context.Context, tenantID, moduleID, itemID string) error
	// Reorder applies an explicit permutation of a module's active item ids.
	Reorder(ctx context.Context, tenantID, moduleID string, orderedItemIDs []string) error
	// SetRequirement replaces a module's completion rule after the aggregate
	// re-validates it against the module's current active items (fail-loud on a
	// rule that references absent items or an out-of-range n_of_m threshold).
	SetRequirement(ctx context.Context, tenantID, moduleID string, req ModuleRequirement) error
	// SoftDelete soft-deletes a module and cascades to its items.
	SoftDelete(ctx context.Context, tenantID, moduleID string) error
}

// -----------------------------------------------------------------------------
// InMemModuleStore — dev/test ModulePort impl
// -----------------------------------------------------------------------------

// InMemModuleStore is a mutex-guarded, map-backed ModulePort for dev + unit
// tests. Tenant scoping is enforced in-process (the pg adapter defers to RLS).
type InMemModuleStore struct {
	mu   sync.Mutex
	byID map[string]*Module
}

// NewInMemModuleStore returns an empty store.
func NewInMemModuleStore() *InMemModuleStore {
	return &InMemModuleStore{byID: make(map[string]*Module)}
}

// Create assigns the dense within-course position (count of active sibling
// modules) then stores the module.
func (s *InMemModuleStore) Create(_ context.Context, m *Module) (*Module, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: nil module", ErrInvalidArgument)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := 0
	for _, ex := range s.byID {
		if ex.TenantID == m.TenantID && ex.CourseID == m.CourseID && ex.DeletedAt == nil {
			pos++
		}
	}
	m.Position = pos
	s.byID[m.ID] = m
	return m, nil
}

// Get returns an active module by id (miss ⇒ ok=false).
func (s *InMemModuleStore) Get(_ context.Context, moduleID string) (*Module, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[moduleID]
	if !ok || m.DeletedAt != nil {
		return nil, false, nil
	}
	return m, true, nil
}

// ListByCourse returns active modules for a (tenant, course) in Position order.
func (s *InMemModuleStore) ListByCourse(_ context.Context, tenantID, courseID string) ([]*Module, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Module, 0)
	for _, m := range s.byID {
		if m.TenantID == tenantID && m.CourseID == courseID && m.DeletedAt == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// AddItem delegates to the domain aggregate after tenant-scoped lookup.
func (s *InMemModuleStore) AddItem(_ context.Context, tenantID, moduleID, contentItemID string) (*ModuleItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.activeForTenant(tenantID, moduleID)
	if err != nil {
		return nil, err
	}
	return m.AddItem(contentItemID)
}

// RemoveItem delegates to the domain aggregate after tenant-scoped lookup.
func (s *InMemModuleStore) RemoveItem(_ context.Context, tenantID, moduleID, itemID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.activeForTenant(tenantID, moduleID)
	if err != nil {
		return err
	}
	_, err = m.RemoveItem(itemID)
	return err
}

// Reorder delegates to the domain aggregate after tenant-scoped lookup.
func (s *InMemModuleStore) Reorder(_ context.Context, tenantID, moduleID string, orderedItemIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.activeForTenant(tenantID, moduleID)
	if err != nil {
		return err
	}
	return m.ReorderItems(orderedItemIDs)
}

// SetRequirement delegates to the domain aggregate after tenant-scoped lookup
// (the aggregate re-validates the rule against the module's current items).
func (s *InMemModuleStore) SetRequirement(_ context.Context, tenantID, moduleID string, req ModuleRequirement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.activeForTenant(tenantID, moduleID)
	if err != nil {
		return err
	}
	return m.SetRequirement(req)
}

// SoftDelete soft-deletes a module (cascading to items) after tenant-scoped lookup.
func (s *InMemModuleStore) SoftDelete(_ context.Context, tenantID, moduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[moduleID]
	if !ok || m.TenantID != tenantID {
		return ErrNotFound
	}
	m.SoftDelete()
	return nil
}

// activeForTenant returns the active module iff it exists and belongs to the
// tenant — the in-process stand-in for the pg adapter's RLS scoping. Caller
// holds s.mu.
func (s *InMemModuleStore) activeForTenant(tenantID, moduleID string) (*Module, error) {
	m, ok := s.byID[moduleID]
	if !ok || m.DeletedAt != nil || m.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return m, nil
}

// Compile-time assertion: InMemModuleStore satisfies ModulePort.
var _ ModulePort = (*InMemModuleStore)(nil)
