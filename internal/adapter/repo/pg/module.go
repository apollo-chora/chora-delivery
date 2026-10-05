// module.go — pg adapter for the Module course-structure aggregate (WS-A W7).
// Persists course_modules + course_module_items per migration
// 0039_course_modules.up.sql, replacing nothing (greenfield): this is the
// backend layer that groups the flat course_content items into ordered modules.
//
// Mutating item operations (AddItem/RemoveItem/Reorder) are load-mutate-persist:
// the module + its active items are loaded inside the tx, the domain mutation
// runs (so every invariant — cap, uniqueness, requirement consistency, dense
// positions — executes on the production path, never a green-test stub), then
// the delta is written. Create dense-appends the within-course Position via a
// MAX(position)+1 subquery under the tx (a partial unique index makes a
// concurrent duplicate fail loud rather than silently collide).
//
// Every read/write wraps rls.ApplySession first so the tenant_isolation policy
// on both tables filters by chora.tenant_id. Cross-DB queries FORBIDDEN — this
// only touches chora_delivery.course_modules(+_items); the ContentItem
// reference is a bare UUID (cross-aggregate), never an FK or cross-DB link.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	module "github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// ErrModuleMissingTenant is returned when tenant_id is empty — the write cannot
// be RLS-scoped without it. Fail loud.
var ErrModuleMissingTenant = errors.New("pg: module requires a non-empty tenant_id (RLS scope)")

// ErrModuleMissingCourse is returned when course_id is empty on Create.
var ErrModuleMissingCourse = errors.New("pg: module requires a non-empty course_id")

// ErrModuleMissingID is returned when module_id is empty on a mutator.
var ErrModuleMissingID = errors.New("pg: module requires a non-empty module_id")

// -----------------------------------------------------------------------------
// SQL templates (exported so CI/lint can grep them)
// -----------------------------------------------------------------------------

const moduleSelectCols = `id, tenant_id, course_id, title, position, ` +
	`requirement_kind, requirement_threshold_n, requirement_required_item_ids, ` +
	`created_at, updated_at, deleted_at`

// SQLInsertModule dense-appends the within-course position via a MAX+1 subquery
// evaluated inside the tx; RETURNING position surfaces the assigned slot. $8 is
// reused for created_at + updated_at.
const SQLInsertModule = `
INSERT INTO course_modules (
    id, tenant_id, course_id, title, position,
    requirement_kind, requirement_threshold_n, requirement_required_item_ids,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4,
    (SELECT COALESCE(MAX(position) + 1, 0) FROM course_modules
       WHERE tenant_id = $2 AND course_id = $3 AND deleted_at IS NULL),
    $5, $6, $7::jsonb,
    $8, $8
)
RETURNING position
`

// SQLSelectModuleByID loads one active module by id (RLS scopes the tenant).
const SQLSelectModuleByID = `
SELECT ` + moduleSelectCols + `
FROM course_modules
WHERE id = $1 AND deleted_at IS NULL
`

// SQLSelectModuleByIDTenant loads one active module by (tenant, id) — the
// mutator-path load (tenant is known, defence-in-depth over RLS).
const SQLSelectModuleByIDTenant = `
SELECT ` + moduleSelectCols + `
FROM course_modules
WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
`

// SQLSelectModulesByCourse lists active modules for a course in structure order.
const SQLSelectModulesByCourse = `
SELECT ` + moduleSelectCols + `
FROM course_modules
WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
ORDER BY position ASC, id ASC
`

const moduleItemSelectCols = `item_id, module_id, tenant_id, content_item_id, position, created_at, updated_at`

// SQLSelectModuleItems returns a module's active items in order.
const SQLSelectModuleItems = `
SELECT ` + moduleItemSelectCols + `
FROM course_module_items
WHERE tenant_id = $1 AND module_id = $2 AND deleted_at IS NULL
ORDER BY position ASC, item_id ASC
`

// SQLSelectModuleItemsByModuleIDs hydrates items for many modules in one query
// (ListByCourse), avoiding an N+1.
const SQLSelectModuleItemsByModuleIDs = `
SELECT ` + moduleItemSelectCols + `
FROM course_module_items
WHERE tenant_id = $1 AND module_id = ANY($2::uuid[]) AND deleted_at IS NULL
ORDER BY module_id ASC, position ASC, item_id ASC
`

// SQLInsertModuleItem inserts one membership row. $6 is reused for created_at +
// updated_at.
const SQLInsertModuleItem = `
INSERT INTO course_module_items (
    item_id, module_id, tenant_id, content_item_id, position, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $6)
`

// SQLSoftDeleteModuleItem soft-deletes one item.
const SQLSoftDeleteModuleItem = `
UPDATE course_module_items
SET deleted_at = $3, updated_at = $3
WHERE tenant_id = $1 AND item_id = $2 AND deleted_at IS NULL
`

// SQLShiftDownModuleItems re-compacts positions after a removal: every active
// item past the removed slot shifts down by one. (No unique position index on
// items → a single statement is safe.)
const SQLShiftDownModuleItems = `
UPDATE course_module_items
SET position = position - 1, updated_at = $4
WHERE tenant_id = $1 AND module_id = $2 AND deleted_at IS NULL AND position > $3
`

// SQLUpdateModuleItemPosition sets one item's position (the reorder write).
const SQLUpdateModuleItemPosition = `
UPDATE course_module_items
SET position = $3, updated_at = $4
WHERE tenant_id = $1 AND item_id = $2 AND deleted_at IS NULL
`

// SQLUpdateModuleRequirement replaces a module's inline completion rule (the
// three requirement columns). The domain re-validates the rule against the
// current item set before this fires; a rejected rule aborts the tx.
const SQLUpdateModuleRequirement = `
UPDATE course_modules
SET requirement_kind = $3, requirement_threshold_n = $4,
    requirement_required_item_ids = $5::jsonb, updated_at = $6
WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
`

// SQLSoftDeleteModule soft-deletes the module row.
const SQLSoftDeleteModule = `
UPDATE course_modules
SET deleted_at = $3, updated_at = $3
WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
`

// SQLCascadeSoftDeleteModuleItems soft-deletes every active item of a module
// (the within-aggregate cascade; never touches referenced ContentItems).
const SQLCascadeSoftDeleteModuleItems = `
UPDATE course_module_items
SET deleted_at = $3, updated_at = $3
WHERE tenant_id = $1 AND module_id = $2 AND deleted_at IS NULL
`

// -----------------------------------------------------------------------------
// Repository
// -----------------------------------------------------------------------------

// ModuleRepo is the Postgres-backed module.ModulePort impl.
type ModuleRepo struct {
	tx TxRunner
}

// NewModuleRepo constructs a ModuleRepo around a TxRunner. A nil TxRunner
// degrades every method to ErrNotImplemented (fail-loud, matches the other
// chora-delivery repos).
func NewModuleRepo(tx TxRunner) *ModuleRepo {
	return &ModuleRepo{tx: tx}
}

// Compile-time assertion: ModuleRepo satisfies module.ModulePort.
var _ module.ModulePort = (*ModuleRepo)(nil)

// Create persists a new module (dense-append position) plus any items the
// aggregate already carries, in one tx. Returns the module with the
// DB-assigned Position.
func (r *ModuleRepo) Create(ctx context.Context, m *module.Module) (*module.Module, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if m == nil {
		return nil, fmt.Errorf("pg: Create requires a non-nil module")
	}
	if strings.TrimSpace(m.TenantID) == "" {
		return nil, ErrModuleMissingTenant
	}
	if strings.TrimSpace(m.CourseID) == "" {
		return nil, ErrModuleMissingCourse
	}
	reqJSON, err := marshalRequiredItemIDs(m.Requirement.RequiredItemIDs)
	if err != nil {
		return nil, err
	}
	created := m.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	ctx = tracing.WithTenantID(ctx, m.TenantID)

	var assignedPos int
	err = r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLInsertModule,
			m.ID, m.TenantID, m.CourseID, m.Title,
			string(m.Requirement.Kind), m.Requirement.ThresholdN, reqJSON,
			created.UTC(),
		)
		if err := row.Scan(&assignedPos); err != nil {
			return fmt.Errorf("pg: insert module: %w", err)
		}
		for _, it := range m.Items {
			itCreated := it.CreatedAt
			if itCreated.IsZero() {
				itCreated = created
			}
			if _, err := q.Exec(ctx, SQLInsertModuleItem,
				it.ID, m.ID, m.TenantID, it.ContentItemID, it.Position, itCreated.UTC(),
			); err != nil {
				return fmt.Errorf("pg: insert module item %s: %w", it.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.Position = assignedPos
	return m, nil
}

// Get loads a module + its active items by id, RLS-scoped to the caller's
// tenant. Miss ⇒ (nil, false, nil).
func (r *ModuleRepo) Get(ctx context.Context, moduleID string) (*module.Module, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *module.Module
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectModuleByID, moduleID)
		m, scanErr := scanModule(row.Scan)
		if scanErr != nil {
			return nil // miss
		}
		items, err := selectModuleItems(ctx, q, m.TenantID, m.ID)
		if err != nil {
			return err
		}
		m.Items = items
		found = m
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// ListByCourse returns active modules for a course in structure order, each
// hydrated with its active items via a single ANY() query.
func (r *ModuleRepo) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*module.Module, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrModuleMissingTenant
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var out []*module.Module
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// Drain the module rows fully before issuing the items query — a single
		// pooled conn cannot interleave two live result sets.
		byID := make(map[string]*module.Module)
		order := make([]*module.Module, 0)
		rs, qErr := q.Query(ctx, SQLSelectModulesByCourse, tenantID, courseID)
		if qErr != nil {
			return qErr
		}
		for rs.Next() {
			m, scanErr := scanModule(rs.Scan)
			if scanErr != nil {
				rs.Close()
				return scanErr
			}
			byID[m.ID] = m
			order = append(order, m)
		}
		if err := rs.Err(); err != nil {
			rs.Close()
			return err
		}
		rs.Close()
		if len(order) == 0 {
			return nil
		}
		ids := make([]string, 0, len(order))
		for _, m := range order {
			ids = append(ids, m.ID)
		}
		irs, iErr := q.Query(ctx, SQLSelectModuleItemsByModuleIDs, tenantID, ids)
		if iErr != nil {
			return iErr
		}
		defer irs.Close()
		for irs.Next() {
			it, scanErr := scanModuleItem(irs.Scan)
			if scanErr != nil {
				return scanErr
			}
			if m, ok := byID[it.ModuleID]; ok {
				m.Items = append(m.Items, it)
			}
		}
		if err := irs.Err(); err != nil {
			return err
		}
		out = order
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AddItem loads the module + items, applies the domain AddItem (cap / uniqueness
// / ref validation run here), then inserts the new membership row.
func (r *ModuleRepo) AddItem(ctx context.Context, tenantID, moduleID, contentItemID string) (*module.ModuleItem, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrModuleMissingTenant
	}
	if strings.TrimSpace(moduleID) == "" {
		return nil, ErrModuleMissingID
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var added *module.ModuleItem
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		m, err := loadModuleTx(ctx, q, tenantID, moduleID)
		if err != nil {
			return err
		}
		it, err := m.AddItem(contentItemID)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLInsertModuleItem,
			it.ID, m.ID, tenantID, it.ContentItemID, it.Position, it.CreatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("pg: insert module item: %w", err)
		}
		added = it
		return nil
	})
	if err != nil {
		return nil, err
	}
	return added, nil
}

// RemoveItem loads the module + items, applies the domain RemoveItem (which
// refuses a removal that would break the completion requirement + re-compacts),
// then soft-deletes the item and shifts the trailing positions down.
func (r *ModuleRepo) RemoveItem(ctx context.Context, tenantID, moduleID, itemID string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return ErrModuleMissingTenant
	}
	if strings.TrimSpace(moduleID) == "" {
		return ErrModuleMissingID
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		m, err := loadModuleTx(ctx, q, tenantID, moduleID)
		if err != nil {
			return err
		}
		removed, err := m.RemoveItem(itemID)
		if err != nil {
			return err
		}
		at := removed.UpdatedAt.UTC()
		if _, err := q.Exec(ctx, SQLSoftDeleteModuleItem, tenantID, removed.ID, at); err != nil {
			return fmt.Errorf("pg: soft-delete module item: %w", err)
		}
		if _, err := q.Exec(ctx, SQLShiftDownModuleItems, tenantID, moduleID, removed.Position, at); err != nil {
			return fmt.Errorf("pg: shift module item positions: %w", err)
		}
		return nil
	})
}

// Reorder loads the module + items, applies the domain permutation, then writes
// each item's new position.
func (r *ModuleRepo) Reorder(ctx context.Context, tenantID, moduleID string, orderedItemIDs []string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return ErrModuleMissingTenant
	}
	if strings.TrimSpace(moduleID) == "" {
		return ErrModuleMissingID
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		m, err := loadModuleTx(ctx, q, tenantID, moduleID)
		if err != nil {
			return err
		}
		if err := m.ReorderItems(orderedItemIDs); err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, it := range m.Items {
			if _, err := q.Exec(ctx, SQLUpdateModuleItemPosition, tenantID, it.ID, it.Position, now); err != nil {
				return fmt.Errorf("pg: reorder update item %s: %w", it.ID, err)
			}
		}
		return nil
	})
}

// SetRequirement loads the module + items, applies the domain SetRequirement
// (which re-validates the rule against the current active item set), then
// persists the three requirement columns. A rejected rule returns the domain
// error and aborts the tx (fail-loud, atomic — the row is left unchanged).
func (r *ModuleRepo) SetRequirement(ctx context.Context, tenantID, moduleID string, req module.ModuleRequirement) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return ErrModuleMissingTenant
	}
	if strings.TrimSpace(moduleID) == "" {
		return ErrModuleMissingID
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		m, err := loadModuleTx(ctx, q, tenantID, moduleID)
		if err != nil {
			return err
		}
		if err := m.SetRequirement(req); err != nil {
			return err
		}
		reqJSON, err := marshalRequiredItemIDs(m.Requirement.RequiredItemIDs)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpdateModuleRequirement,
			tenantID, moduleID, string(m.Requirement.Kind), m.Requirement.ThresholdN, reqJSON, m.UpdatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("pg: update module requirement: %w", err)
		}
		return nil
	})
}

// SoftDelete soft-deletes the module and cascades to its items (within the
// aggregate). The referenced ContentItems are never touched.
func (r *ModuleRepo) SoftDelete(ctx context.Context, tenantID, moduleID string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return ErrModuleMissingTenant
	}
	if strings.TrimSpace(moduleID) == "" {
		return ErrModuleMissingID
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	now := time.Now().UTC()
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLSoftDeleteModule, tenantID, moduleID, now); err != nil {
			return fmt.Errorf("pg: soft-delete module: %w", err)
		}
		if _, err := q.Exec(ctx, SQLCascadeSoftDeleteModuleItems, tenantID, moduleID, now); err != nil {
			return fmt.Errorf("pg: cascade soft-delete module items: %w", err)
		}
		return nil
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// loadModuleTx loads a module + its active items within an open tx (mutator
// path). Missing ⇒ module.ErrNotFound.
func loadModuleTx(ctx context.Context, q Querier, tenantID, moduleID string) (*module.Module, error) {
	row := q.QueryRow(ctx, SQLSelectModuleByIDTenant, tenantID, moduleID)
	m, scanErr := scanModule(row.Scan)
	if scanErr != nil {
		return nil, module.ErrNotFound
	}
	items, err := selectModuleItems(ctx, q, tenantID, moduleID)
	if err != nil {
		return nil, err
	}
	m.Items = items
	return m, nil
}

// selectModuleItems loads a single module's active items in order.
func selectModuleItems(ctx context.Context, q Querier, tenantID, moduleID string) ([]*module.ModuleItem, error) {
	rs, err := q.Query(ctx, SQLSelectModuleItems, tenantID, moduleID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var items []*module.ModuleItem
	for rs.Next() {
		it, scanErr := scanModuleItem(rs.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, it)
	}
	return items, rs.Err()
}

// marshalRequiredItemIDs renders the requirement's id list as a JSONB array,
// normalising nil → "[]" (never JSON null, so the NOT NULL DEFAULT '[]' column
// stays a real array).
func marshalRequiredItemIDs(ids []string) (string, error) {
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("pg: marshal required_item_ids: %w", err)
	}
	return string(b), nil
}

// scanModule maps one course_modules row into the domain aggregate (items are
// hydrated separately). Column order matches moduleSelectCols.
func scanModule(scan func(dest ...any) error) (*module.Module, error) {
	var (
		id, tenantID, courseID, title string
		position                      int
		reqKind                       string
		reqThreshold                  int
		reqIDsJSON                    []byte
		createdAt, updatedAt          time.Time
		deletedAt                     *time.Time
	)
	if err := scan(&id, &tenantID, &courseID, &title, &position,
		&reqKind, &reqThreshold, &reqIDsJSON, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}
	var reqIDs []string
	if len(reqIDsJSON) > 0 {
		if err := json.Unmarshal(reqIDsJSON, &reqIDs); err != nil {
			return nil, fmt.Errorf("pg: unmarshal requirement_required_item_ids: %w", err)
		}
	}
	return &module.Module{
		ID:       id,
		TenantID: tenantID,
		CourseID: courseID,
		Title:    title,
		Position: position,
		Requirement: module.ModuleRequirement{
			Kind:            module.RequirementKind(reqKind),
			ThresholdN:      reqThreshold,
			RequiredItemIDs: reqIDs,
		},
		CreatedAt: createdAt.UTC(),
		UpdatedAt: updatedAt.UTC(),
		DeletedAt: deletedAt,
	}, nil
}

// scanModuleItem maps one course_module_items row. Column order matches
// moduleItemSelectCols (tenant_id is selected for symmetry then discarded — the
// domain entity carries no tenant field).
func scanModuleItem(scan func(dest ...any) error) (*module.ModuleItem, error) {
	var (
		itemID, moduleID, tenantID, contentItemID string
		position                                  int
		createdAt, updatedAt                      time.Time
	)
	if err := scan(&itemID, &moduleID, &tenantID, &contentItemID, &position, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	_ = tenantID
	return &module.ModuleItem{
		ID:            itemID,
		ModuleID:      moduleID,
		ContentItemID: contentItemID,
		Position:      position,
		CreatedAt:     createdAt.UTC(),
		UpdatedAt:     updatedAt.UTC(),
	}, nil
}
