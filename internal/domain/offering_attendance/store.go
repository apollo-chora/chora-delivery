// store.go — the hexagonal persistence port for the session-scoped attendance
// Record. Mirrors offering_session.Store; ctx-threaded so the Postgres adapter
// can call rls.ApplySession before every query.
package offering_attendance

import "context"

// Store is the persistence port for session attendance marks.
//
//   - Upsert records a mark, idempotent on the natural key (tenant_id,
//     session_id, gcid): a re-mark UPDATES the existing row (correcting a
//     status) rather than inserting a duplicate. Returns an error so a failed
//     durable write is loud.
//   - ListBySession returns a tenant's marks for one session, ordered by gcid.
type Store interface {
	Upsert(ctx context.Context, r *Record) error
	ListBySession(ctx context.Context, tenantID, sessionID string) ([]*Record, error)
}
