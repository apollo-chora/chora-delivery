// course_content.go — pg adapter for the CourseContent curriculum aggregate
// (CHO-1612 / CHO-1794, L3). Persists course_content_items per migration
// 0029_course_content_items.up.sql, replacing the in-memory repo so authored
// curricula survive pod restart and are tenant-isolated via RLS.
//
// Save reconciles the full desired item set in one tx: soft-delete the items
// no longer present, then UPSERT (un-deleting) the current ones. Get returns
// the active items ordered by position, or course_content.ErrNotFound when the
// course has no curriculum yet (so the Service auto-creates one on first add).
//
// All reads/writes wrap rls.ApplySession before queries so the tenant_isolation
// policy on course_content_items filters by chora.tenant_id. Cross-DB queries
// FORBIDDEN — only reads chora_delivery.course_content_items.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI/lint can grep them)
// -----------------------------------------------------------------------------

const courseContentSelectCols = `item_id, course_id, tenant_id, kind, ref, title, position, created_at, updated_at`

// SQLSelectCourseContentItems — active items for a course, in curriculum order.
const SQLSelectCourseContentItems = `
SELECT ` + courseContentSelectCols + `
FROM course_content_items
WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
ORDER BY position ASC, item_id ASC
`

// SQLSoftDeleteRemovedContentItems — soft-delete the active items of a course
// whose ids are NOT in the current desired set (handles RemoveItem + reorder
// shrink). With an empty id set this soft-deletes every active item.
const SQLSoftDeleteRemovedContentItems = `
UPDATE course_content_items
SET deleted_at = $3, updated_at = $3
WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
  AND NOT (item_id = ANY($4::uuid[]))
`

// SQLUpsertCourseContentItem — insert-or-update one item; re-activates a
// previously soft-deleted row (deleted_at = NULL).
const SQLUpsertCourseContentItem = `
INSERT INTO course_content_items (
    item_id, course_id, tenant_id, kind, ref, title, position, created_at, updated_at, deleted_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL)
ON CONFLICT (item_id) DO UPDATE SET
    kind       = EXCLUDED.kind,
    ref        = EXCLUDED.ref,
    title      = EXCLUDED.title,
    position   = EXCLUDED.position,
    updated_at = EXCLUDED.updated_at,
    deleted_at = NULL
`

// -----------------------------------------------------------------------------
// Repository
// -----------------------------------------------------------------------------

// CourseContentRepo is the pg-backed course_content.Repository.
type CourseContentRepo struct {
	tx TxRunner
}

// NewCourseContentRepo constructs the repo around a TxRunner.
func NewCourseContentRepo(tx TxRunner) *CourseContentRepo {
	return &CourseContentRepo{tx: tx}
}

// Compile-time guard the adapter satisfies the domain port.
var _ cc.Repository = (*CourseContentRepo)(nil)

// Get returns the active curriculum for a course, or course_content.ErrNotFound
// when none exists yet.
func (r *CourseContentRepo) Get(ctx context.Context, tenantID, courseID string) (*cc.CourseContent, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var (
		items      []*cc.ContentItem
		createdMin time.Time
		updatedMax time.Time
	)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, err := q.Query(ctx, SQLSelectCourseContentItems, tenantID, courseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			it, created, updated, scanErr := scanContentItem(rows.Scan)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, it)
			if createdMin.IsZero() || created.Before(createdMin) {
				createdMin = created
			}
			if updated.After(updatedMax) {
				updatedMax = updated
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, cc.ErrNotFound
	}
	return &cc.CourseContent{
		CourseID:  courseID,
		TenantID:  tenantID,
		Items:     items,
		CreatedAt: createdMin.UTC(),
		UpdatedAt: updatedMax.UTC(),
	}, nil
}

// Save reconciles the aggregate's full item set: soft-delete removed items,
// then UPSERT the current ones (un-deleting as needed). Idempotent + retry-safe.
func (r *CourseContentRepo) Save(ctx context.Context, c *cc.CourseContent) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return errors.New("pg: nil course content")
	}
	ctx = tracing.WithTenantID(ctx, c.TenantID)
	now := time.Now().UTC()
	ids := make([]string, 0, len(c.Items)) // non-nil empty ⇒ ANY('{}') soft-deletes all
	for _, it := range c.Items {
		ids = append(ids, it.ItemID)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLSoftDeleteRemovedContentItems, c.TenantID, c.CourseID, now, ids); err != nil {
			return fmt.Errorf("pg: soft-delete removed content items: %w", err)
		}
		for _, it := range c.Items {
			created := it.CreatedAt
			if created.IsZero() {
				created = now
			}
			updated := it.UpdatedAt
			if updated.IsZero() {
				updated = now
			}
			if _, err := q.Exec(ctx, SQLUpsertCourseContentItem,
				it.ItemID, c.CourseID, c.TenantID, string(it.Kind), it.Ref, it.Title, it.Position,
				created.UTC(), updated.UTC(),
			); err != nil {
				return fmt.Errorf("pg: upsert content item %s: %w", it.ItemID, err)
			}
		}
		return nil
	})
}

// scanContentItem scans one course_content_items row, returning the domain item
// plus the raw created/updated stamps (for aggregate-level min/max derivation).
func scanContentItem(scan func(...any) error) (*cc.ContentItem, time.Time, time.Time, error) {
	var (
		itemID, courseID, tenantID, kind, ref, title string
		position                                     int
		createdAt, updatedAt                         time.Time
	)
	if err := scan(&itemID, &courseID, &tenantID, &kind, &ref, &title, &position, &createdAt, &updatedAt); err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	return &cc.ContentItem{
		ItemID:    itemID,
		CourseID:  courseID,
		Kind:      cc.Kind(kind),
		Ref:       ref,
		Title:     title,
		Position:  position,
		CreatedAt: createdAt.UTC(),
		UpdatedAt: updatedAt.UTC(),
	}, createdAt, updatedAt, nil
}
