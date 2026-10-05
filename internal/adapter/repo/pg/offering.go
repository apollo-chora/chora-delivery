// offering.go — Postgres adapter for chora_delivery.offerings (R+ four-mode W1).
//
// SCHEMA: see migrations/0031_offerings.up.sql (`offerings` table).
//
// Backs the delivery.OfferingPort. Storage = JSONB aggregate snapshot keyed by
// id, with tenant_id + course_id + delivery_type + state extracted for RLS
// scoping + faceted listing — the 0020_exams pattern (the Offering is a
// multi-field FSM record; the JSONB snapshot round-trips losslessly since every
// field is exported). RLS is ENABLED; rls.ApplySession runs before every query.
//
// Save returns an error (fail loud — a dropped Save loses an offering). Get
// returns its error — an infra/RLS failure is LOUD, never a silent miss.
// ok=false means a GENUINE absent row (CHO-2184: the two were once the same
// answer, and a dead read passed for an empty one).
package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	// SQLUpsertOffering populates the extracted `label` column (mig 0032)
	// alongside the JSONB snapshot so the W2 finder can sort/search/keyset on
	// label without unpacking the blob.
	SQLUpsertOffering = `
INSERT INTO offerings (id, tenant_id, course_id, delivery_type, state, label, data, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    state      = EXCLUDED.state,
    label      = EXCLUDED.label,
    data       = EXCLUDED.data,
    updated_at = EXCLUDED.updated_at`

	SQLGetOffering = `SELECT data FROM offerings WHERE id = $1 AND deleted_at IS NULL`

	SQLListOfferingsByTenant = `
SELECT data FROM offerings
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY id`
)

// -----------------------------------------------------------------------------
// OfferingRepo
// -----------------------------------------------------------------------------

// OfferingRepo is the Postgres-backed delivery.OfferingPort.
type OfferingRepo struct {
	tx TxRunner
}

// NewOfferingRepo constructs an OfferingRepo around a TxRunner. A nil TxRunner
// makes Save / ListByTenant return ErrNotImplemented (fail loud); Get returns
// ErrNotImplemented too: an unwired repo is a WIRING BUG, not an empty
// database (CHO-2184).
func NewOfferingRepo(tx TxRunner) *OfferingRepo { return &OfferingRepo{tx: tx} }

// Compile-time assertion: satisfies the domain port.
var _ domain.OfferingPort = (*OfferingRepo)(nil)

func (r *OfferingRepo) Save(ctx context.Context, o *domain.Offering) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if o == nil {
		return nil
	}
	data, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("pg: marshal offering: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// course_id is a write-only PRIMARY extract (offering→course is 1:N; the
		// full list lives in the JSONB aggregate). Reads unmarshal `data`.
		if _, err := q.Exec(ctx, SQLUpsertOffering,
			o.ID, o.TenantID, o.PrimaryCourseID(), string(o.DeliveryType), string(o.State), o.Label, data, o.CreatedAt, o.UpdatedAt); err != nil {
			return fmt.Errorf("pg: upsert offering: %w", err)
		}
		return nil
	})
}

func (r *OfferingRepo) Get(ctx context.Context, id string) (*domain.Offering, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var out *domain.Offering
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		var data []byte
		if err := q.QueryRow(ctx, SQLGetOffering, id).Scan(&data); err != nil {
			if isNoRows(err) {
				return nil // genuine miss — outer returns ok=false, err=nil
			}
			return fmt.Errorf("pg: get offering: %w", err)
		}
		var o domain.Offering
		if err := json.Unmarshal(data, &o); err != nil {
			return fmt.Errorf("pg: unmarshal offering: %w", err)
		}
		out = &o
		return nil
	})
	if err != nil {
		return nil, false, err // LOUD: infra/RLS failure is NOT a miss (CHO-2184)
	}
	if out == nil {
		return nil, false, nil // genuine miss
	}
	return out, true, nil
}

// Search runs the W2 finder query (free-text + delivery_type/state filters,
// server multi-sort, keyset cursor pagination, query-minus-self facet counts,
// total estimate) entirely in SQL, mirroring the pure domain.SearchOfferings
// reference. One RunInTx applies RLS once, then runs: items (with a +1
// lookahead row for next_cursor), a total count, and one GROUP BY per facet
// (each excluding its own dimension). nil-tx is fail-loud (ErrNotImplemented).
func (r *OfferingRepo) Search(ctx context.Context, q domain.OfferingQuery) (*domain.OfferingSearchPage, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	q = q.Normalize()

	page := &domain.OfferingSearchPage{Items: []*domain.Offering{}, Facets: []domain.OfferingFacet{}}

	err := r.tx.RunInTx(ctx, func(ctx context.Context, qx Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(qx)); err != nil {
			return err
		}

		// ---- items (one-past lookahead → next_cursor without a 2nd query) ----
		args := []any{q.TenantID}
		where := offeringSearchWhere(q, &args, "")
		if q.Cursor != nil {
			where += offeringKeysetClause(q, &args)
		}
		dir := "DESC"
		if q.SortDir == domain.OfferingSortDirAsc {
			dir = "ASC"
		}
		col := offeringSortColumn(q.SortField)
		args = append(args, q.Limit+1)
		itemsSQL := fmt.Sprintf(
			`SELECT data FROM offerings WHERE %s ORDER BY %s %s, id %s LIMIT $%d`,
			where, col, dir, dir, len(args))

		items, err := scanOfferingData(ctx, qx, itemsSQL, args)
		if err != nil {
			return err
		}
		if len(items) > q.Limit {
			items = items[:q.Limit]
			last := items[len(items)-1]
			page.NextCursor = &domain.OfferingCursor{
				SortValue: offeringCursorValue(last, q.SortField),
				ID:        last.ID,
			}
		}
		page.Items = items

		// ---- total estimate (full filter, no cursor) ----
		cargs := []any{q.TenantID}
		cwhere := offeringSearchWhere(q, &cargs, "")
		var total int
		if err := qx.QueryRow(ctx, `SELECT count(*) FROM offerings WHERE `+cwhere, cargs...).Scan(&total); err != nil {
			return err
		}
		page.TotalEstimate = total

		// ---- facets (query-minus-self) ----
		dtFacet, err := offeringFacetQuery(ctx, qx, q, domain.OfferingFacetDeliveryType)
		if err != nil {
			return err
		}
		stFacet, err := offeringFacetQuery(ctx, qx, q, domain.OfferingFacetState)
		if err != nil {
			return err
		}
		page.Facets = []domain.OfferingFacet{dtFacet, stFacet}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// offeringSearchWhere builds the tenant + active + free-text + facet-filter
// predicate, EXCLUDING the dimension named in `exclude` (""=none). $1 is the
// pre-seeded tenant_id; further args are appended in order.
func offeringSearchWhere(q domain.OfferingQuery, args *[]any, exclude string) string {
	clauses := []string{"tenant_id = $1", "deleted_at IS NULL"}
	if q.Q != "" {
		*args = append(*args, "%"+q.Q+"%")
		clauses = append(clauses, fmt.Sprintf("label ILIKE $%d", len(*args)))
	}
	if q.CourseID != "" {
		// Match the full CourseIDs JSONB array (offering→course is 1:N), not just
		// the denormalised primary course_id column — so a course that is a
		// secondary member of a multi-course offering still counts toward the
		// blast-radius "used by N offerings" total (B1.1).
		*args = append(*args, q.CourseID)
		clauses = append(clauses, fmt.Sprintf("data->'CourseIDs' @> to_jsonb($%d::text)", len(*args)))
	}
	if exclude != domain.OfferingFacetDeliveryType && len(q.DeliveryTypes) > 0 {
		*args = append(*args, q.DeliveryTypes)
		clauses = append(clauses, fmt.Sprintf("delivery_type = ANY($%d)", len(*args)))
	}
	if exclude != domain.OfferingFacetState && len(q.States) > 0 {
		*args = append(*args, q.States)
		clauses = append(clauses, fmt.Sprintf("state = ANY($%d)", len(*args)))
	}
	return strings.Join(clauses, " AND ")
}

// offeringKeysetClause appends the keyset cursor predicate as a SQL row-value
// comparison (same direction on both columns so it composes with ORDER BY).
func offeringKeysetClause(q domain.OfferingQuery, args *[]any) string {
	op := "<"
	if q.SortDir == domain.OfferingSortDirAsc {
		op = ">"
	}
	col := offeringSortColumn(q.SortField)
	*args = append(*args, q.Cursor.SortValue)
	vPos := len(*args)
	*args = append(*args, q.Cursor.ID)
	idPos := len(*args)
	cast := "::timestamptz"
	if col == "label" {
		cast = ""
	}
	return fmt.Sprintf(" AND (%s, id) %s ($%d%s, $%d::uuid)", col, op, vPos, cast, idPos)
}

func offeringSortColumn(field string) string {
	switch field {
	case domain.OfferingSortLabel:
		return "label"
	case domain.OfferingSortUpdatedAt:
		return "updated_at"
	default:
		return "created_at"
	}
}

func offeringCursorValue(o *domain.Offering, field string) string {
	switch field {
	case domain.OfferingSortLabel:
		return o.Label
	case domain.OfferingSortUpdatedAt:
		return o.UpdatedAt.UTC().Format(time.RFC3339Nano)
	default:
		return o.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
}

func offeringFacetQuery(ctx context.Context, qx Querier, q domain.OfferingQuery, field string) (domain.OfferingFacet, error) {
	col := "delivery_type"
	if field == domain.OfferingFacetState {
		col = "state"
	}
	args := []any{q.TenantID}
	where := offeringSearchWhere(q, &args, field)
	sql := fmt.Sprintf(`SELECT %s, count(*) FROM offerings WHERE %s GROUP BY %s ORDER BY %s`, col, where, col, col)
	rows, err := qx.Query(ctx, sql, args...)
	if err != nil {
		return domain.OfferingFacet{}, err
	}
	defer rows.Close()
	facet := domain.OfferingFacet{Field: field}
	for rows.Next() {
		var val string
		var cnt int
		if err := rows.Scan(&val, &cnt); err != nil {
			return domain.OfferingFacet{}, err
		}
		facet.Values = append(facet.Values, domain.OfferingFacetValue{Value: val, Count: cnt})
	}
	return facet, rows.Err()
}

func scanOfferingData(ctx context.Context, qx Querier, sql string, args []any) ([]*domain.Offering, error) {
	rows, err := qx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Offering
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var o domain.Offering
		if err := json.Unmarshal(data, &o); err != nil {
			return nil, err
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

func (r *OfferingRepo) ListByTenant(ctx context.Context, tenantID string) ([]*domain.Offering, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*domain.Offering
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLListOfferingsByTenant, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var o domain.Offering
			if err := json.Unmarshal(data, &o); err != nil {
				return err
			}
			out = append(out, &o)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
