// bulk_tx_tee.go - BulkEnrollTxTee: the transactional-outbox writer for the
// offering roster bulk-enrol path (POST /api/v1/offerings/{id}/roster/bulk).
//
// Unlike the after-commit TransactionalOutboxPublisher.teeRow (which writes the
// outbox row on a SEPARATE connection via Store.Insert AFTER the domain tx has
// committed), this tee writes the enrollment.created.v1 row on a caller-supplied
// transaction - the SAME tx the enrolment rows are inserted on - so the enrolment
// and its event commit (or roll back) atomically (pg.EnrollmentRepo.RegisterBulk).
//
// Two invariants make this safe alongside the live dispatcher:
//
//  1. Column shape parity - the row carries the top-level tenant_id / gcid /
//     idempotency_key columns the production deliveryoutbox.PostgresStore writes
//     and FetchPending SCANS (into non-null strings). A row missing those (e.g.
//     the chora-go-common outbox.PostgresRecorder shape, which omits them) would
//     be a poison-pill the dispatcher cannot scan. buildOutboxRow is shared with
//     teeRow so the shape never drifts.
//  2. Tx-safe dedupe - the INSERT is ON CONFLICT (idempotency_key) DO NOTHING.
//     A duplicate idempotency_key (a re-enrol of a revived-after-cancel learner,
//     whose original enrollment.created row still occupies the unique
//     idempotency_key) is a no-op that does NOT raise an error and therefore does
//     NOT abort the enclosing pgx transaction (any statement error inside a pgx
//     tx poisons it). The single-path Store.Insert catches the violation in Go,
//     which is unusable inside a shared tx - hence the DO NOTHING here.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

// ExecFn runs one statement on a live transaction. The bulk-enrol repo passes a
// closure over its transaction's Querier; the tee uses it to INSERT the outbox
// row on that same tx. A raw (aliased) func type so callers need not import this
// package to satisfy the shape.
type ExecFn = func(ctx context.Context, query string, args ...any) error

// sqlBulkEnrollOutboxInsert writes one outbox_events row with the SAME columns
// the production deliveryoutbox.PostgresStore.Insert writes (id, tenant_id, gcid,
// aggregate_type, aggregate_id, event_type, topic, payload, envelope,
// idempotency_key, occurred_at, status). ON CONFLICT (idempotency_key) DO NOTHING
// targets the partial unique index outbox_events_idempotency_idx (migration
// 0004) so a duplicate is a tx-safe no-op instead of a tx-poisoning error.
const sqlBulkEnrollOutboxInsert = `
INSERT INTO outbox_events (
    id, tenant_id, gcid, aggregate_type, aggregate_id, event_type, topic,
    payload, envelope, idempotency_key, occurred_at, status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, 'pending'
)
ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
`

// BulkEnrollTxTee mints the enrollment.created.v1 event and writes its outbox
// row on a caller-supplied transaction.
type BulkEnrollTxTee struct {
	// minter shapes the envelope + payload (idempotency-key derivation, IMDA
	// tagging) exactly like the after-commit path. Only PublishEnrollmentCreated
	// is used; the returned event is teed HERE (on the tx), not by the minter -
	// so wire the INNER InMemoryPublisher, never the TransactionalOutboxPublisher
	// (which would also Store.Insert on a separate connection = a double write).
	minter events.Publisher
}

// NewBulkEnrollTxTee constructs a BulkEnrollTxTee. Panics on a nil minter
// (fail-loud misconfiguration - the tee cannot mint envelopes without it).
func NewBulkEnrollTxTee(minter events.Publisher) *BulkEnrollTxTee {
	if minter == nil {
		panic("outbox: NewBulkEnrollTxTee: minter (events.Publisher) required")
	}
	return &BulkEnrollTxTee{minter: minter}
}

// RecordEnrollmentCreated mints the enrollment.created.v1 event for `in` and
// INSERTs its outbox row via `exec` on the caller's transaction. A mint or exec
// error is returned unwrapped-enough to surface loudly so the caller rolls the
// transaction back - never a swallowed write.
func (t *BulkEnrollTxTee) RecordEnrollmentCreated(ctx context.Context, exec ExecFn, in events.EnrollmentCreated) error {
	ev, err := t.minter.PublishEnrollmentCreated(in)
	if err != nil {
		return fmt.Errorf("outbox: bulk tee mint enrollment.created: %w", err)
	}
	row, err := buildOutboxRow(ev, "enrollment", in.EnrollmentID, ev.Envelope.SourceProject, ev.Envelope.SourceService, time.Now().UTC())
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(row.Envelope)
	if err != nil {
		return fmt.Errorf("outbox: bulk tee marshal envelope: %w", err)
	}
	return exec(ctx, sqlBulkEnrollOutboxInsert,
		row.ID, row.TenantID, row.GCID, row.AggregateType, row.AggregateID,
		row.EventType, row.Topic, row.Payload, string(envJSON), row.IdempotencyKey, row.OccurredAt,
	)
}
