// user_directory.go — Postgres adapter for chora_delivery.user_directory
// (Q3 GCID → display-name projection, delivery side).
//
// SCHEMA: migrations/0040_user_directory.up.sql.
//
// The user_directory table is a TENANT-AGNOSTIC, RLS-FREE global read-model:
// one row per global GCID (opaque UUIDv7, no tenant embedded). Names arrive
// from chora.identity.user.profile_updated.v1 and are only ever surfaced by a
// stitch against RLS-scoped roster rows (inmem.CourseRosterRepo). Because the
// table carries no RLS policy, these methods deliberately do NOT call
// rls.ApplySession — SET LOCAL chora.tenant_id would be a pointless no-op, and
// Upsert has no tenant to scope by. This is the one adapter in the package
// that omits ApplySession, and the reason is the no-RLS global-table design.
//
// Cross-DB queries forbidden — this reads/writes only chora_delivery.
package pg

import (
	"context"
	"errors"
	"strings"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

// ErrUserDirectoryMissingGCID is returned when the entry has no GCID — the
// directory is keyed on it. Fail loud per feedback_no_stubs_real_wiring.
var ErrUserDirectoryMissingGCID = errors.New("pg: user_directory entry requires a non-empty gcid")

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

// SQLUpsertUserDirectory idempotently upserts one directory row last-writer-
// wins: the DO UPDATE only fires when the stored row is strictly older than the
// incoming updated_at, so a stale/out-of-order redelivery can never clobber a
// newer name and an equal-timestamp replay is a no-op. Args: $1 gcid, $2
// display_name, $3 email (nullable), $4 updated_at.
const SQLUpsertUserDirectory = `
INSERT INTO user_directory (gcid, display_name, email, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (gcid) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    email        = EXCLUDED.email,
    updated_at   = EXCLUDED.updated_at
WHERE user_directory.updated_at < EXCLUDED.updated_at
`

// SQLLookupUserDirectoryNames returns (gcid, display_name) for every supplied
// gcid that has a row. $1 is a text[] of gcids cast to uuid[] so the PK index
// is used. Rows with an empty display_name are returned as-is; the caller
// applies the GCID fallback.
const SQLLookupUserDirectoryNames = `
SELECT gcid, display_name
FROM user_directory
WHERE gcid = ANY($1::uuid[])
`

// -----------------------------------------------------------------------------
// UserDirectoryRepo
// -----------------------------------------------------------------------------

// UserDirectoryRepo is the Postgres-backed directory.UserDirectoryPort impl.
type UserDirectoryRepo struct {
	tx TxRunner
}

// NewUserDirectoryRepo constructs a UserDirectoryRepo around a TxRunner.
// Passing nil yields a Repo that returns ErrNotImplemented from every method
// (matches Enrollment / Application repos). Production wires this behind the
// same pool gate as the other pg repos.
func NewUserDirectoryRepo(tx TxRunner) *UserDirectoryRepo {
	return &UserDirectoryRepo{tx: tx}
}

// Upsert applies the entry last-writer-wins on updated_at (see SQL). No RLS
// (global tenant-agnostic table) — no rls.ApplySession.
func (r *UserDirectoryRepo) Upsert(ctx context.Context, entry domain.UserDirectoryEntry) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(entry.GCID) == "" {
		return ErrUserDirectoryMissingGCID
	}
	// Empty email binds SQL NULL rather than '' so an absent email is
	// represented faithfully in the nullable column.
	var emailArg any
	if strings.TrimSpace(entry.Email) != "" {
		emailArg = entry.Email
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		_, err := q.Exec(ctx, SQLUpsertUserDirectory,
			entry.GCID, entry.DisplayName, emailArg, entry.UpdatedAt,
		)
		return err
	})
}

// LookupNames returns a gcid → display_name map for the supplied gcids that
// have a directory row. Empty input short-circuits before any query. No RLS.
func (r *UserDirectoryRepo) LookupNames(ctx context.Context, gcids []string) (map[string]string, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if len(gcids) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(gcids))
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		rs, qErr := q.Query(ctx, SQLLookupUserDirectoryNames, gcids)
		if qErr != nil {
			return qErr
		}
		defer rs.Close()
		for rs.Next() {
			var g, name string
			if scanErr := rs.Scan(&g, &name); scanErr != nil {
				return scanErr
			}
			out[g] = name
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Compile-time assertion: UserDirectoryRepo must satisfy the domain port.
var _ domain.UserDirectoryPort = (*UserDirectoryRepo)(nil)
