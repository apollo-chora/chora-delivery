// port.go — the hexagonal ports the StudentModuleProgress projection depends on,
// plus the InMemProgressStore dev/test impl.
//
//   - ProgressPort       : persist + read the projection rows (pg / in-mem);
//   - CompletionResolver : map a completed atom/assessment reference to the
//     ENROLLED learner's module targets, intra-chora_delivery (pg / fake);
//   - ModuleLoader       : load a Module (items + requirement) — the read subset
//     of module.ModulePort the projector needs (module.InMemModuleStore + the pg
//     ModuleRepo both satisfy it).
//
// Every method takes ctx so the pg adapter can propagate tenant_id into
// rls.ApplySession and carry trace context across the SQL boundary. The InMem
// impl accepts ctx then drops it (sync, ctx-free).
package moduleprogress

import (
	"context"
	"sort"
	"sync"

	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// CompletionTarget is one (content item, module) a learner's completion of a
// referenced atom/assessment advances. The resolver returns only targets the
// learner is ENROLLED for, so the projector never pollutes progress with rows
// for modules of courses the learner is not taking.
type CompletionTarget struct {
	ContentItemID string
	ModuleID      string
	CourseID      string
}

// CompletionResolver maps a completed reference (kind+ref: an atom id or an
// assessment id) to the module targets it advances for an ENROLLED learner —
// resolving course_content_items → course_module_items → course_modules joined
// to course_enrollments, all in chora_delivery. Cross-DB queries FORBIDDEN.
type CompletionResolver interface {
	ResolveEnrolledTargets(ctx context.Context, tenantID, gcid, kind, ref string) ([]CompletionTarget, error)
}

// ModuleLoader loads a module (items + requirement) by id — the read subset of
// module.ModulePort the projector needs to evaluate completion.
type ModuleLoader interface {
	Get(ctx context.Context, moduleID string) (*module.Module, bool, error)
}

// ProgressPort persists + reads StudentModuleProgress projections.
type ProgressPort interface {
	// Advance atomically loads-or-creates the learner's projection for the
	// module, records contentItemID against it (recomputing completion via m),
	// and persists — under a row lock in pg so two concurrent completions of
	// DIFFERENT items in the same module by the same learner cannot race and
	// drop one. Returns changed=true iff the projection's state changed (false
	// on an idempotent redelivery / already-recorded item).
	Advance(ctx context.Context, tenantID, gcid, moduleID, courseID, contentItemID string, m *module.Module) (bool, error)
	// GetByLearnerModule loads a learner's active projection for one module.
	// Miss returns (nil, false, nil). Read/test convenience.
	GetByLearnerModule(ctx context.Context, tenantID, gcid, moduleID string) (*StudentModuleProgress, bool, error)
	// ListByModuleIDs returns all active projections for the given modules
	// (tenant-scoped). The read handler filters to a single learner (own view)
	// or the whole cohort (instructor view).
	ListByModuleIDs(ctx context.Context, tenantID string, moduleIDs []string) ([]*StudentModuleProgress, error)
}

// -----------------------------------------------------------------------------
// InMemProgressStore — dev/test ProgressPort impl
// -----------------------------------------------------------------------------

// InMemProgressStore is a mutex-guarded, map-backed ProgressPort for dev + unit
// tests. Tenant scoping is enforced in-process (the pg adapter defers to RLS).
type InMemProgressStore struct {
	mu   sync.Mutex
	byID map[string]*StudentModuleProgress
}

// NewInMemProgressStore returns an empty store.
func NewInMemProgressStore() *InMemProgressStore {
	return &InMemProgressStore{byID: make(map[string]*StudentModuleProgress)}
}

// GetByLearnerModule returns the active projection for (tenant, gcid, module).
func (s *InMemProgressStore) GetByLearnerModule(_ context.Context, tenantID, gcid, moduleID string) (*StudentModuleProgress, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.byID {
		if p.TenantID == tenantID && p.GCID == gcid && p.ModuleID == moduleID && p.DeletedAt == nil {
			return p, true, nil
		}
	}
	return nil, false, nil
}

// Advance load-or-creates the learner's projection under the store mutex and
// records contentItemID against it — the in-process analogue of the pg row lock.
func (s *InMemProgressStore) Advance(_ context.Context, tenantID, gcid, moduleID, courseID, contentItemID string, m *module.Module) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prog *StudentModuleProgress
	for _, p := range s.byID {
		if p.TenantID == tenantID && p.GCID == gcid && p.ModuleID == moduleID && p.DeletedAt == nil {
			prog = p
			break
		}
	}
	if prog == nil {
		var err error
		prog, err = New(NewParams{TenantID: tenantID, GCID: gcid, ModuleID: moduleID, CourseID: courseID})
		if err != nil {
			return false, err
		}
	}
	changed, err := prog.RecordItemCompletion(contentItemID, m)
	if err != nil {
		return false, err
	}
	if changed {
		s.byID[prog.ID] = prog
	}
	return changed, nil
}

// ListByModuleIDs returns active projections for the given modules, tenant-scoped.
func (s *InMemProgressStore) ListByModuleIDs(_ context.Context, tenantID string, moduleIDs []string) ([]*StudentModuleProgress, error) {
	want := make(map[string]bool, len(moduleIDs))
	for _, id := range moduleIDs {
		want[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*StudentModuleProgress, 0)
	for _, p := range s.byID {
		if p.TenantID == tenantID && p.DeletedAt == nil && want[p.ModuleID] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModuleID != out[j].ModuleID {
			return out[i].ModuleID < out[j].ModuleID
		}
		return out[i].GCID < out[j].GCID
	})
	return out, nil
}

// Compile-time assertions.
var (
	_ ProgressPort = (*InMemProgressStore)(nil)
	_ ModuleLoader = (*module.InMemModuleStore)(nil)
)
