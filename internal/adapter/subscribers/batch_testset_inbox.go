// batch_testset_inbox.go — Lane 1c W4 (CHO-1703 / ADR-180 D10) batch→test-set
// assembly subscriber.
//
// chora-creation publishes chora.creation.question_batch.accepted.v1 when an
// author accepts a batch question-generation job with the "Create test set"
// toggle ON (D1): N candidate questions became N new LearningAtoms, and the
// event carries the author-curated test-set intent (title / description /
// per-item points + display order). THIS subscriber assembles exactly ONE
// DRAFT test set (D2/D3) in chora_delivery — no cross-DB read, per
// ddd-enforcement #3 the event payload is the only inter-domain mechanism.
//
// Idempotency (at-least-once Pub/Sub) is layered:
//
//  1. inbox short-circuit on (tenant, job_id) — stops concurrent-redelivery
//     races before any DB read (mirrors GradingInboxSubscriber).
//  2. read-side existence check: test_sets.source_job_id row present ⇒ ACK
//     no-op (covers redeliveries beyond the inbox TTL).
//  3. durable backstop: UNIQUE partial index idx_test_sets_source_job_id
//     (migration 0028) — losing the insert race re-checks + ACKs.
//
// Per-item resilience: question existence is validated through the EXISTING
// domain.QuestionSnapshotter port (the same gRPC SnapshotQuestionByID seam
// TestSet.PublishWithSnapshot uses). A permanently-missing question — or a
// per-item validation reject (unknown question_type, blank ids) — SKIPS that
// item with a log and assembles the rest: a permanently-bad item must never
// drive an infinite Nack loop (bounded retries → DLQ is reserved for
// payload-level failures). Transient snapshotter transport errors Nack so
// Pub/Sub retries.
//
// Emits the EXISTING chora.delivery.test_set.created.v1 via the wired
// outbox-backed publisher (REUSE — no new delivery event type, D10).
//
// Hexagonal: INBOUND ADAPTER. Depends on the narrow TestSetAssemblyStore
// port (pg.TestSetRepo + httpapi.InMemTestSetStore both satisfy it via the
// shared TestSetPort surface), the QuestionSnapshotter port, and the
// events.Publisher port. The subscriber path intentionally bypasses the
// HTTP role gates — invariants live in the TestSet aggregate (per
// testset_handler.go's documented design).
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// TopicQuestionBatchAccepted — chora-creation emits this on batch accept
// with the test-set toggle ON. Subscriber binding: push subscription
// chora-delivery.creation-question_batch-accepted →
// POST /api/internal/pubsub/batch-testset-inbox.
const TopicQuestionBatchAccepted = "chora.creation.question_batch.accepted.v1"

// BatchTestSetInboxTTL is the dedupe-key retention window for the batch
// test-set inbox. The inbox is the FIRST idempotency layer only — the
// durable layers (source_job_id existence check + UNIQUE partial index)
// cover redeliveries beyond this window, so 24h amply suffices.
const BatchTestSetInboxTTL = 24 * time.Hour

// defaultBatchItemPoints mirrors the composer's D5 fallback: proto3 absent
// points (0) means "no marks extracted from the source" — uniform default 10.
const defaultBatchItemPoints = 10

// QuestionBatchAcceptedPayload mirrors chora.creation.v1.QuestionBatchAccepted
// (the subset chora-delivery consumes). Hydrated by the push handler from
// the protodecode projection.
type QuestionBatchAcceptedPayload struct {
	JobID       string
	HostAtomID  string
	TenantID    string
	AuthorGCID  string
	Title       string
	Description string
	Items       []QuestionBatchItem
	Traceparent string
}

// QuestionBatchItem mirrors QuestionBatchAccepted.Item — one accepted
// candidate = one new LearningAtom + its embedded Question.
type QuestionBatchItem struct {
	QuestionAtomID string
	QuestionID     string
	QuestionType   string // "mcq" | "oe" (creation's "essay" alias canonicalised by the aggregate)
	Points         int32  // 1..100; 0 = absent ⇒ defaultBatchItemPoints
	DisplayOrder   int32  // 1-based curated position
}

// TestSetAssemblyStore is the narrow persistence port this subscriber needs.
// Satisfied by pg.TestSetRepo (production) and httpapi.InMemTestSetStore
// (dev / tests) — both already implement the wider httpapi.TestSetPort.
type TestSetAssemblyStore interface {
	// GetBySourceJobID returns the test set assembled from the supplied
	// batch job (ok=false when absent) — the idempotency read.
	GetBySourceJobID(ctx context.Context, tenantID, sourceJobID string) (*domain.TestSet, bool, error)
	// Save persists the assembled aggregate (parent + children, RLS-scoped
	// in the pg adapter via the aggregate's TenantID).
	Save(ctx context.Context, ts *domain.TestSet) error
}

// BatchTestSetSubscriber consumes question_batch.accepted.v1 and assembles
// ONE DRAFT test set per batch job.
type BatchTestSetSubscriber struct {
	testSets    TestSetAssemblyStore
	snapshotter domain.QuestionSnapshotter // nil ⇒ existence validation skipped (dev wiring)
	publisher   events.Publisher           // nil ⇒ created.v1 emit skipped (tests)
	inbox       idempotent.Store
	ttl         time.Duration
}

// NewBatchTestSetSubscriber wires the store + snapshotter + publisher + inbox.
//
// snapshotter is OPTIONAL — nil mirrors the main.go QuestionSnapshotter
// wiring (SVC_CREATION_GRPC_URL unset ⇒ nil ⇒ no existence validation; the
// publish path will still fail loud later per feedback_no_stubs_real_wiring).
// inbox is OPTIONAL — nil triggers a MemoryStore fallback; production SHOULD
// pass a PostgresStore-backed inbox so the first dedup layer survives pod
// restart + works across replicas (the 0028 unique index backstops either way).
func NewBatchTestSetSubscriber(
	testSets TestSetAssemblyStore,
	snapshotter domain.QuestionSnapshotter,
	publisher events.Publisher,
	inbox idempotent.Store,
) *BatchTestSetSubscriber {
	if inbox == nil {
		inbox = idempotent.NewMemoryStore()
	}
	return &BatchTestSetSubscriber{
		testSets:    testSets,
		snapshotter: snapshotter,
		publisher:   publisher,
		inbox:       inbox,
		ttl:         BatchTestSetInboxTTL,
	}
}

// WithTTL overrides the dedupe-key retention window.
func (s *BatchTestSetSubscriber) WithTTL(ttl time.Duration) *BatchTestSetSubscriber {
	if s == nil || ttl <= 0 {
		return s
	}
	s.ttl = ttl
	return s
}

// SubscribedTopic returns the inbound topic this subscriber binds to.
func (s *BatchTestSetSubscriber) SubscribedTopic() string { return TopicQuestionBatchAccepted }

// Handle processes one chora.creation.question_batch.accepted.v1 event.
//
// Returns:
//   - nil on success OR on any deliberate no-op (duplicate job, lost unique
//     race) — Pub/Sub ACKs.
//   - error on required-field violations, transient snapshotter/storage
//     failures — Pub/Sub Nacks → bounded retries → DLQ
//     (chora.dlq.creation.question_batch.accepted.v1).
func (s *BatchTestSetSubscriber) Handle(ctx context.Context, env events.EventEnvelope, p QuestionBatchAcceptedPayload) error {
	if s == nil || s.testSets == nil {
		return errors.New("subscribers: nil batch test-set inbox")
	}
	jobID := strings.TrimSpace(p.JobID)
	if jobID == "" {
		return errors.New("subscribers: batch test-set inbox job_id required")
	}
	tenantID := strings.TrimSpace(p.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(env.TenantID)
	}
	if tenantID == "" {
		return errors.New("subscribers: batch test-set inbox tenant_id required")
	}
	authorGCID := strings.TrimSpace(p.AuthorGCID)
	if authorGCID == "" {
		authorGCID = strings.TrimSpace(env.GCID)
	}
	if authorGCID == "" {
		return errors.New("subscribers: batch test-set inbox author_gcid required")
	}
	traceparent := p.Traceparent
	if traceparent == "" {
		traceparent = env.Traceparent
	}

	// Layer 1 — inbox short-circuit on the BUSINESS key (tenant, job_id),
	// not event_id: chora-creation publishes at most one accepted.v1 per
	// job, so a redelivered envelope with a fresh broker message id still
	// collapses here when within TTL.
	key := "batch_testset:" + tenantID + ":" + jobID
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		// Layer 2 — durable existence check (idempotency FIRST).
		if _, ok, err := s.testSets.GetBySourceJobID(ctx, tenantID, jobID); err != nil {
			return fmt.Errorf("subscribers: batch test-set idempotency read job %s: %w", jobID, err)
		} else if ok {
			log.Printf("delivery: batch-testset job %s already assembled — ack no-op (at-least-once redelivery)", jobID)
			return nil
		}

		ts, err := domain.NewTestSet(domain.NewTestSetInput{
			TenantID:    tenantID,
			AuthorGCID:  authorGCID,
			Title:       p.Title,
			Description: p.Description,
			SourceJobID: jobID,
		})
		if err != nil {
			// Deterministic producer violation (blank/oversize title…) —
			// Nack: bounded redelivery surfaces it, then the DLQ owns it.
			return fmt.Errorf("subscribers: batch test-set job %s unassemblable: %w", jobID, err)
		}

		// Items in curated order (defensive sort by display_order — the
		// proto contract is ascending+contiguous, but the aggregate's
		// ordering invariant should not depend on producer discipline).
		items := make([]QuestionBatchItem, len(p.Items))
		copy(items, p.Items)
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].DisplayOrder < items[j].DisplayOrder
		})

		skipped := 0
		for _, item := range items {
			// Validate question existence via the EXISTING snapshotter port.
			// NotFound = permanently-bad item ⇒ skip + log; transport error =
			// transient ⇒ Nack (retry).
			if s.snapshotter != nil {
				// ADR-229 WS-2: the batch author is the reuse actor —
				// creation gates the existence probe like any snapshot.
				if _, snapErr := s.snapshotter.SnapshotQuestion(ctx, tenantID, item.QuestionID, authorGCID); snapErr != nil {
					if errors.Is(snapErr, domain.ErrQuestionSnapshotNotFound) {
						skipped++
						log.Printf("delivery: batch-testset job %s skipping missing question %s (atom %s): %v",
							jobID, item.QuestionID, item.QuestionAtomID, snapErr)
						continue
					}
					return fmt.Errorf("subscribers: batch test-set job %s snapshot question %s: %w",
						jobID, item.QuestionID, snapErr)
				}
			}
			points := float64(item.Points)
			if points <= 0 {
				// proto3 absent (0) ⇒ composer's uniform default (D5).
				points = defaultBatchItemPoints
			}
			if _, addErr := ts.AddQuestion(domain.AddQuestionInput{
				QuestionAtomID: item.QuestionAtomID,
				QuestionID:     item.QuestionID,
				QuestionType:   item.QuestionType,
				DisplayOrder:   int(item.DisplayOrder),
				Points:         points,
			}); addErr != nil {
				// Deterministic per-item validation reject — skip + log,
				// assemble the rest (never infinite-retry a bad item).
				skipped++
				log.Printf("delivery: batch-testset job %s skipping invalid item (atom %s, question %s): %v",
					jobID, item.QuestionAtomID, item.QuestionID, addErr)
				continue
			}
		}
		if skipped > 0 {
			log.Printf("delivery: batch-testset job %s assembled with %d/%d items (%d skipped)",
				jobID, len(items)-skipped, len(items), skipped)
		}

		if saveErr := s.testSets.Save(ctx, ts); saveErr != nil {
			// Layer 3 — losing the 0028 unique-index race to a concurrent
			// redelivery on another pod surfaces as a Save error here. The
			// re-check distinguishes "winner already landed" (ACK no-op)
			// from a genuine storage failure (Nack → retry).
			if _, ok, reErr := s.testSets.GetBySourceJobID(ctx, tenantID, jobID); reErr == nil && ok {
				log.Printf("delivery: batch-testset job %s lost the unique race — winner row present, ack no-op", jobID)
				return nil
			}
			return fmt.Errorf("subscribers: batch test-set job %s save: %w", jobID, saveErr)
		}

		s.emitTestSetCreated(ts, traceparent)
		log.Printf("delivery: batch-testset job %s assembled DRAFT test set %s (%d questions, author %s)",
			jobID, ts.ID, ts.QuestionCount(), authorGCID)
		return nil
	})
}

// emitTestSetCreated publishes the EXISTING chora.delivery.test_set.created.v1
// via the wired publisher (outbox-backed in production). Best-effort to
// mirror the HTTP create path's semantics (publishTestSetCreated in
// testset_handler.go) — the aggregate is already durable; the FE discovery
// poll reads the row, not the event.
func (s *BatchTestSetSubscriber) emitTestSetCreated(ts *domain.TestSet, traceparent string) {
	if s == nil || s.publisher == nil || ts == nil {
		return
	}
	_, _ = s.publisher.PublishTestSetCreated(events.TestSetCreated{
		TenantID:    ts.TenantID,
		GCID:        ts.AuthorGCID,
		TestSetID:   ts.ID,
		AuthorGCID:  ts.AuthorGCID,
		Title:       ts.Title,
		Traceparent: traceparent,
	})
}
